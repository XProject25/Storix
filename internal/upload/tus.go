// Package upload implements the tus 1.0.0 resumable upload protocol.
//
// A transfer that is interrupted at 79 of 80 GB resumes at that byte instead
// of starting over. Partial data is written straight into the destination
// directory under a hidden scratch name, so completing an upload is a single
// atomic rename on the same volume rather than a second full copy.
//
// Storix - modern web file manager for servers.
// Developed by X Project.
package upload

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/XProject25/Storix/internal/events"
	"github.com/XProject25/Storix/internal/store"
	"github.com/XProject25/Storix/internal/vfs"
)

// Protocol constants.
const (
	Version = "1.0.0"

	hdrResumable = "Tus-Resumable"
	hdrVersion   = "Tus-Version"
	hdrExtension = "Tus-Extension"
	hdrMaxSize   = "Tus-Max-Size"
	hdrOffset    = "Upload-Offset"
	hdrLength    = "Upload-Length"
	hdrMetadata  = "Upload-Metadata"
	hdrExpires   = "Upload-Expires"
	hdrChecksum  = "Upload-Checksum"

	contentTypeOffset = "application/offset+octet-stream"
	extensions        = "creation,creation-with-upload,termination,expiration"
)

// Deps are the collaborators the upload manager needs.
type Deps struct {
	Store   *store.Store
	VFS     *vfs.VFS
	Events  *events.Hub
	Logger  *slog.Logger
	MaxSize int64
	Expiry  time.Duration
	// QuotaCheck, when set, reports whether a user may add this many bytes.
	// It is consulted when a transfer is declared, which is the only moment
	// the full size is known before any of it lands on disk.
	QuotaCheck func(ctx context.Context, userID int64, size int64) (ok bool, remaining int64, err error)
	// QuotaAdd, when set, records bytes that landed.
	QuotaAdd func(ctx context.Context, userID int64, bytes int64)
}

// Manager serves the tus endpoints.
type Manager struct {
	deps  Deps
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// Actor describes who is uploading and where they are allowed to write.
type Actor struct {
	UserID     int64
	Username   string
	Scope      vfs.Scope
	ShareToken string
	// Base is the endpoint prefix used to build the Location header, for
	// example "/api/v1/tus".
	Base string
	// ForcedDir pins every upload to one directory, used by upload requests
	// where the visitor must not choose the destination.
	ForcedDir string
	// MaxSize overrides the global ceiling for this actor, 0 means inherit.
	MaxSize int64
}

// New builds an upload manager.
func New(d Deps) *Manager {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Expiry <= 0 {
		d.Expiry = 72 * time.Hour
	}
	return &Manager{deps: d, locks: make(map[string]*sync.Mutex)}
}

// lockFor serializes writes to a single upload.
func (m *Manager) lockFor(id string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.locks[id]
	if !ok {
		l = &sync.Mutex{}
		m.locks[id] = l
	}
	return l
}

func (m *Manager) forget(id string) {
	m.mu.Lock()
	delete(m.locks, id)
	m.mu.Unlock()
}

// Options answers the tus discovery request.
func (m *Manager) Options(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set(hdrResumable, Version)
	h.Set(hdrVersion, Version)
	h.Set(hdrExtension, extensions)
	if m.deps.MaxSize > 0 {
		h.Set(hdrMaxSize, strconv.FormatInt(m.deps.MaxSize, 10))
	}
	w.WriteHeader(http.StatusNoContent)
}

// Create starts a resumable upload.
func (m *Manager) Create(w http.ResponseWriter, r *http.Request, actor Actor) {
	w.Header().Set(hdrResumable, Version)

	length, err := strconv.ParseInt(r.Header.Get(hdrLength), 10, 64)
	if err != nil || length < 0 {
		http.Error(w, "Upload-Length is required", http.StatusBadRequest)
		return
	}
	limit := actor.MaxSize
	if limit == 0 {
		limit = m.deps.MaxSize
	}
	if limit > 0 && length > limit {
		http.Error(w, "Upload exceeds the size limit", http.StatusRequestEntityTooLarge)
		return
	}
	if m.deps.QuotaCheck != nil {
		ok, remaining, err := m.deps.QuotaCheck(r.Context(), actor.UserID, length)
		switch {
		case err != nil:
			// An allowance that cannot be read is not permission to ignore it.
			m.deps.Logger.Warn("quota check failed", "user", actor.UserID, "err", err)
			http.Error(w, "Your storage allowance could not be checked", http.StatusInternalServerError)
			return
		case !ok:
			http.Error(w, "Your storage allowance is full, "+quotaSizeText(remaining)+" remaining",
				http.StatusRequestEntityTooLarge)
			return
		}
	}

	meta := parseMetadata(r.Header.Get(hdrMetadata))
	filename := sanitizeName(meta["filename"])
	if filename == "" {
		http.Error(w, "A filename is required", http.StatusBadRequest)
		return
	}
	if err := vfs.ValidName(filename); err != nil {
		http.Error(w, "That file name is not allowed", http.StatusBadRequest)
		return
	}

	dir := actor.ForcedDir
	if dir == "" {
		dir = vfs.Clean(meta["dir"])
	}
	if dir == "" {
		http.Error(w, "A destination folder is required", http.StatusBadRequest)
		return
	}

	// A folder upload carries the path of the file inside the dropped tree.
	relDir, err := safeRelativeDir(meta["relativePath"], filename)
	if err != nil {
		http.Error(w, "Invalid relative path", http.StatusBadRequest)
		return
	}
	targetDir := dir
	if relDir != "" {
		targetDir = path.Join(dir, relDir)
		if _, err := m.deps.VFS.MkdirAll(actor.Scope, targetDir); err != nil {
			writeVFSError(w, err)
			return
		}
	}

	loc, err := m.deps.VFS.ResolveWritable(actor.Scope, targetDir)
	if err != nil {
		writeVFSError(w, err)
		return
	}
	if info, err := loc.Root.Stat(loc.Rel); err != nil {
		writeVFSError(w, err)
		return
	} else if !info.IsDir() {
		http.Error(w, "The destination is not a folder", http.StatusBadRequest)
		return
	}

	id, err := randomID()
	if err != nil {
		http.Error(w, "Could not start the upload", http.StatusInternalServerError)
		return
	}
	partName := vfs.PartName(id)
	partRel := joinRel(loc.Rel, partName)
	f, err := loc.Root.OpenFile(partRel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		writeVFSError(w, err)
		return
	}
	f.Close()

	now := time.Now()
	sess := &store.UploadSession{
		ID:         id,
		UserID:     actor.UserID,
		ShareToken: actor.ShareToken,
		TargetDir:  targetDir,
		Filename:   filename,
		RelPath:    relDir,
		Size:       length,
		TempPath:   path.Join(targetDir, partName),
		Metadata:   r.Header.Get(hdrMetadata),
		Overwrite:  isTrue(meta["overwrite"]),
		CreatedAt:  now,
		UpdatedAt:  now,
		ExpiresAt:  now.Add(m.deps.Expiry),
	}
	if err := m.deps.Store.CreateUpload(r.Context(), sess); err != nil {
		_ = loc.Root.Remove(partRel)
		http.Error(w, "Could not start the upload", http.StatusInternalServerError)
		return
	}

	h := w.Header()
	h.Set("Location", strings.TrimSuffix(actor.Base, "/")+"/"+id)
	h.Set(hdrExpires, sess.ExpiresAt.UTC().Format(http.TimeFormat))

	// creation-with-upload: the client may send the first chunk right away.
	if strings.HasPrefix(r.Header.Get("Content-Type"), contentTypeOffset) {
		offset, err := m.write(r.Context(), sess, r.Body, 0, actor)
		if err != nil {
			h.Set(hdrOffset, strconv.FormatInt(offset, 10))
			w.WriteHeader(http.StatusCreated)
			return
		}
		h.Set(hdrOffset, strconv.FormatInt(offset, 10))
	}
	w.WriteHeader(http.StatusCreated)
}

// Head reports the current offset so a client can resume.
func (m *Manager) Head(w http.ResponseWriter, r *http.Request, actor Actor) {
	w.Header().Set(hdrResumable, Version)
	sess, err := m.lookup(r, actor)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set(hdrOffset, strconv.FormatInt(sess.Offset, 10))
	h.Set(hdrLength, strconv.FormatInt(sess.Size, 10))
	if sess.Metadata != "" {
		h.Set(hdrMetadata, sess.Metadata)
	}
	if !sess.ExpiresAt.IsZero() {
		h.Set(hdrExpires, sess.ExpiresAt.UTC().Format(http.TimeFormat))
	}
	w.WriteHeader(http.StatusOK)
}

// Patch appends a chunk.
func (m *Manager) Patch(w http.ResponseWriter, r *http.Request, actor Actor) {
	w.Header().Set(hdrResumable, Version)
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, contentTypeOffset) {
		http.Error(w, "Unsupported media type", http.StatusUnsupportedMediaType)
		return
	}
	sess, err := m.lookup(r, actor)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if sess.Completed {
		w.Header().Set(hdrOffset, strconv.FormatInt(sess.Size, 10))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	clientOffset, err := strconv.ParseInt(r.Header.Get(hdrOffset), 10, 64)
	if err != nil || clientOffset < 0 {
		http.Error(w, "Upload-Offset is required", http.StatusBadRequest)
		return
	}

	offset, err := m.write(r.Context(), sess, r.Body, clientOffset, actor)
	if err != nil {
		if errors.Is(err, errOffsetMismatch) {
			w.Header().Set(hdrOffset, strconv.FormatInt(offset, 10))
			http.Error(w, "Offset does not match the server state", http.StatusConflict)
			return
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, io.ErrUnexpectedEOF) {
			// The client hung up; the bytes that did arrive are kept so the
			// transfer can resume from here.
			w.Header().Set(hdrOffset, strconv.FormatInt(offset, 10))
			w.WriteHeader(http.StatusNoContent)
			return
		}
		m.deps.Logger.Warn("upload chunk failed", "id", sess.ID, "err", err)
		writeVFSError(w, err)
		return
	}
	w.Header().Set(hdrOffset, strconv.FormatInt(offset, 10))
	w.WriteHeader(http.StatusNoContent)
}

// Delete aborts an upload and removes the partial data.
func (m *Manager) Delete(w http.ResponseWriter, r *http.Request, actor Actor) {
	w.Header().Set(hdrResumable, Version)
	sess, err := m.lookup(r, actor)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	lock := m.lockFor(sess.ID)
	lock.Lock()
	defer lock.Unlock()
	m.discard(r.Context(), sess, actor.Scope)
	m.forget(sess.ID)
	w.WriteHeader(http.StatusNoContent)
}

var errOffsetMismatch = errors.New("upload: offset mismatch")

// write appends the request body to the scratch file and finalizes the upload
// when the declared length has been reached. It returns the offset reached,
// which is meaningful even when an error is returned.
func (m *Manager) write(ctx context.Context, sess *store.UploadSession, body io.Reader, clientOffset int64, actor Actor) (int64, error) {
	lock := m.lockFor(sess.ID)
	lock.Lock()
	defer lock.Unlock()

	loc, err := m.deps.VFS.ResolveWritable(actor.Scope, sess.TempPath)
	if err != nil {
		return sess.Offset, err
	}
	info, err := loc.Root.Stat(loc.Rel)
	if err != nil {
		return sess.Offset, err
	}
	// The file on disk is the source of truth, not the stored counter.
	actual := info.Size()
	if actual != sess.Offset {
		if err := m.deps.Store.SetUploadOffset(ctx, sess.ID, actual); err == nil {
			sess.Offset = actual
		}
	}
	if clientOffset != actual {
		return actual, errOffsetMismatch
	}

	f, err := loc.Root.OpenFile(loc.Rel, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return actual, err
	}
	defer f.Close()

	remaining := sess.Size - actual
	if remaining < 0 {
		remaining = 0
	}
	reader := io.LimitReader(body, remaining)

	buf := make([]byte, 1<<20)
	offset := actual
	lastPersist := time.Now()
	lastEvent := time.Time{}
	var writeErr error

	for {
		if err := ctx.Err(); err != nil {
			writeErr = err
			break
		}
		n, readErr := reader.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				writeErr = err
				break
			}
			offset += int64(n)
			if time.Since(lastPersist) > 2*time.Second {
				lastPersist = time.Now()
				_ = m.deps.Store.SetUploadOffset(ctx, sess.ID, offset)
			}
			if time.Since(lastEvent) > 500*time.Millisecond {
				lastEvent = time.Now()
				m.progress(sess, offset)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			writeErr = readErr
			break
		}
	}

	if err := f.Sync(); err != nil && writeErr == nil {
		writeErr = err
	}
	sess.Offset = offset
	if err := m.deps.Store.SetUploadOffset(ctx, sess.ID, offset); err != nil {
		m.deps.Logger.Warn("upload offset write failed", "id", sess.ID, "err", err)
	}
	m.progress(sess, offset)

	if writeErr != nil {
		return offset, writeErr
	}
	if offset >= sess.Size {
		final, err := m.finalize(ctx, sess, loc, actor)
		if err != nil {
			return offset, err
		}
		m.deps.Logger.Info("upload complete", "file", final, "bytes", offset, "user", actor.Username)
	}
	return offset, nil
}

// finalize renames the scratch file into its final name.
func (m *Manager) finalize(ctx context.Context, sess *store.UploadSession, loc *vfs.Location, actor Actor) (string, error) {
	dirLoc, err := m.deps.VFS.ResolveWritable(actor.Scope, sess.TargetDir)
	if err != nil {
		return "", err
	}
	name := sess.Filename
	if !sess.Overwrite {
		unique, err := m.deps.VFS.UniqueName(actor.Scope, sess.TargetDir, name)
		if err == nil && unique != "" {
			name = unique
		}
	}
	finalRel := joinRel(dirLoc.Rel, name)
	if sess.Overwrite {
		// Rename replaces an existing regular file atomically; a directory in
		// the way is a genuine conflict.
		if info, err := dirLoc.Root.Lstat(finalRel); err == nil && info.IsDir() {
			return "", fmt.Errorf("%w: a folder with that name exists", vfs.ErrExists)
		}
	}
	if err := loc.Root.Rename(loc.Rel, finalRel); err != nil {
		return "", err
	}
	finalPath := path.Join(sess.TargetDir, name)
	if err := m.deps.Store.CompleteUpload(ctx, sess.ID, finalPath); err != nil {
		m.deps.Logger.Warn("upload record update failed", "id", sess.ID, "err", err)
	}
	sess.Completed = true
	sess.FinalPath = finalPath
	m.forget(sess.ID)

	// The storage figure moves with the file so an allowance is current for
	// the next transfer. Replacing an existing file overstates the figure by
	// what the old copy held, which the next full measurement corrects.
	if m.deps.QuotaAdd != nil {
		m.deps.QuotaAdd(ctx, sess.UserID, sess.Size)
	}

	if m.deps.Events != nil {
		m.deps.Events.Publish(sess.UserID, events.Event{
			Type: "upload.done",
			At:   time.Now(),
			Data: map[string]any{
				"id":     sess.ID,
				"name":   name,
				"path":   finalPath,
				"dir":    sess.TargetDir,
				"size":   sess.Size,
				"shared": sess.ShareToken != "",
			},
		})
		m.deps.Events.Publish(sess.UserID, events.Event{
			Type: "fs.changed",
			At:   time.Now(),
			Data: map[string]any{"path": sess.TargetDir, "reason": "upload"},
		})
	}
	return finalPath, nil
}

// progress publishes a throttled progress event.
func (m *Manager) progress(sess *store.UploadSession, offset int64) {
	if m.deps.Events == nil {
		return
	}
	m.deps.Events.Publish(sess.UserID, events.Event{
		Type: "upload.progress",
		At:   time.Now(),
		Data: map[string]any{
			"id":     sess.ID,
			"name":   sess.Filename,
			"dir":    sess.TargetDir,
			"offset": offset,
			"size":   sess.Size,
		},
	})
}

// discard removes the scratch file and the record.
func (m *Manager) discard(ctx context.Context, sess *store.UploadSession, scope vfs.Scope) {
	if loc, err := m.deps.VFS.ResolveWritable(scope, sess.TempPath); err == nil {
		if err := loc.Root.Remove(loc.Rel); err != nil && !errors.Is(err, os.ErrNotExist) {
			m.deps.Logger.Warn("upload cleanup failed", "id", sess.ID, "err", err)
		}
	}
	if err := m.deps.Store.DeleteUpload(ctx, sess.ID); err != nil {
		m.deps.Logger.Warn("upload record delete failed", "id", sess.ID, "err", err)
	}
}

// lookup loads the session named in the URL and checks it belongs to the actor.
func (m *Manager) lookup(r *http.Request, actor Actor) (*store.UploadSession, error) {
	id := r.PathValue("id")
	if id == "" {
		return nil, errors.New("upload: missing id")
	}
	sess, err := m.deps.Store.GetUpload(r.Context(), id)
	if err != nil {
		return nil, err
	}
	if actor.ShareToken != "" {
		if sess.ShareToken != actor.ShareToken {
			return nil, errors.New("upload: not yours")
		}
		return sess, nil
	}
	if sess.UserID != actor.UserID {
		return nil, errors.New("upload: not yours")
	}
	return sess, nil
}

// Cleanup removes expired uploads. It is safe to call periodically.
func (m *Manager) Cleanup(ctx context.Context, adminScope vfs.Scope) (int, error) {
	sessions, err := m.deps.Store.ListExpiredUploads(ctx, time.Now())
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, sess := range sessions {
		if sess.Completed {
			_ = m.deps.Store.DeleteUpload(ctx, sess.ID)
			removed++
			continue
		}
		m.discard(ctx, sess, adminScope)
		removed++
	}
	return removed, nil
}

// ---- helpers ---------------------------------------------------------------

// parseMetadata decodes the tus Upload-Metadata header.
func parseMetadata(raw string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		key, value, found := strings.Cut(pair, " ")
		if !found {
			out[key] = ""
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
		if err != nil {
			continue
		}
		out[key] = string(decoded)
	}
	return out
}

// sanitizeName strips any directory component a client may have sent.
func sanitizeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\\", "/")
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	name = strings.TrimSpace(strings.Trim(name, "."))
	if vfs.IsInternal(name) {
		name = strings.TrimPrefix(name, vfs.InternalPrefix)
	}
	return name
}

// safeRelativeDir validates the folder part of a folder upload.
func safeRelativeDir(relative, filename string) (string, error) {
	relative = strings.TrimSpace(strings.ReplaceAll(relative, "\\", "/"))
	if relative == "" {
		return "", nil
	}
	relative = strings.TrimPrefix(relative, "/")
	dir := path.Dir(relative)
	if dir == "." || dir == "/" {
		return "", nil
	}
	cleaned := path.Clean(dir)
	if strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", errors.New("upload: unsafe relative path")
	}
	for _, part := range strings.Split(cleaned, "/") {
		if part == "" || part == "." || part == ".." {
			return "", errors.New("upload: unsafe relative path")
		}
		if err := vfs.ValidName(part); err != nil {
			return "", err
		}
	}
	return cleaned, nil
}

func joinRel(base, name string) string {
	if base == "." || base == "" {
		return name
	}
	return base + "/" + name
}

func isTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// randomID returns a 32 character hex identifier.
func randomID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	const hex = "0123456789abcdef"
	out := make([]byte, 32)
	for i, b := range buf {
		out[i*2] = hex[b>>4]
		out[i*2+1] = hex[b&0x0f]
	}
	return string(out), nil
}

// quotaSizeText renders a byte count the short way the interface does, so a
// refused upload reads like the storage figure on screen rather than a raw
// number of bytes.
func quotaSizeText(n int64) string {
	if n <= 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	size := float64(n)
	idx := 0
	for size >= 1024 && idx < len(units)-1 {
		size /= 1024
		idx++
	}
	digits := 2
	switch {
	case idx == 0 || size >= 100:
		digits = 0
	case size >= 10:
		digits = 1
	}
	return strconv.FormatFloat(size, 'f', digits, 64) + " " + units[idx]
}

// writeVFSError maps guarded file system errors onto tus friendly statuses.
func writeVFSError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, vfs.ErrForbidden), errors.Is(err, vfs.ErrDenied), errors.Is(err, vfs.ErrReadOnly):
		http.Error(w, "You cannot write there", http.StatusForbidden)
	case errors.Is(err, vfs.ErrNotFound):
		http.Error(w, "The destination does not exist", http.StatusNotFound)
	case errors.Is(err, vfs.ErrExists):
		http.Error(w, "A file with that name already exists", http.StatusConflict)
	case errors.Is(err, vfs.ErrInvalidName):
		http.Error(w, "That name is not allowed", http.StatusBadRequest)
	default:
		http.Error(w, "The upload could not be written", http.StatusInternalServerError)
	}
}
