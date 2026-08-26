package api

// Content endpoints: everything that moves file bytes rather than metadata.
// Downloads, inline previews, thumbnails, streaming zips and the two archive
// jobs live here.
//
// Two rules shape this file. Bytes that came from a user are never allowed to
// execute in the application origin, so every response carries a vetted content
// type, the nosniff header and a locked down policy. And nothing that can grow
// without bound is ever buffered: a download streams from the file handle and a
// selection is zipped straight into the response, so a forty gigabyte archive
// needs no temporary space at all.
//
// Storix - modern web file manager for servers.
// Developed by X Project.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/XProject25/Storix/internal/archive"
	"github.com/XProject25/Storix/internal/events"
	"github.com/XProject25/Storix/internal/jobs"
	"github.com/XProject25/Storix/internal/thumbs"
	"github.com/XProject25/Storix/internal/vfs"
)

// ctStreamPolicy is the content policy sent with every raw file response. A
// stored page therefore has no origin to reach back into: no scripts, no
// styles, no network, and the sandbox strips it of same origin privileges.
const ctStreamPolicy = "default-src 'none'; sandbox"

// ctMaxSelection bounds how many paths one zip or compress request may name.
const ctMaxSelection = 2000

// ctSafeTypes is the allowlist of media types Storix is willing to label a
// file with. Anything outside it is served as a plain byte stream, which is
// what keeps a stored .html or .svg from ever being rendered by the browser.
var ctSafeTypes = map[string]bool{
	"image/jpeg":      true,
	"image/png":       true,
	"image/gif":       true,
	"image/webp":      true,
	"image/bmp":       true,
	"image/tiff":      true,
	"image/avif":      true,
	"image/heic":      true,
	"audio/mpeg":      true,
	"audio/mp4":       true,
	"audio/ogg":       true,
	"audio/wav":       true,
	"audio/x-wav":     true,
	"audio/webm":      true,
	"audio/flac":      true,
	"audio/x-flac":    true,
	"audio/aac":       true,
	"video/mp4":       true,
	"video/webm":      true,
	"video/ogg":       true,
	"video/quicktime": true,
	"application/pdf": true,
	"text/plain":      true,
}

// ctArchiveExts are the extensions stripped from a requested archive name so
// the caller cannot end up with backup.zip.zip.
var ctArchiveExts = []string{".tar.gz", ".tar.bz2", ".tgz", ".tbz2", ".tbz", ".tar", ".zip"}

// ---- download ---------------------------------------------------------------

// handleDownload streams one file as an attachment.
//
// The work is handed to http.ServeContent, which answers range requests and
// conditional requests from the file handle, so an interrupted transfer resumes
// at the byte it stopped at instead of starting over.
func (a *API) handleDownload(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	p, err := pathParam(r, "path")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	scope, err := a.scopeFor(r.Context(), user)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	entry, err := a.VFS.Stat(scope, p)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	// A folder cannot be a single file response, so send the caller to the
	// endpoint that can answer with a zip of it.
	if entry.IsDir {
		q := url.Values{}
		q.Set("paths", entry.Path)
		q.Set("name", entry.Name)
		ctHarden(w)
		http.Redirect(w, r, "/api/v1/fs/download-zip?"+q.Encode(), http.StatusFound)
		return
	}

	f, info, err := a.VFS.Open(scope, p)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defer f.Close()

	ctHarden(w)
	h := w.Header()
	h.Set("Content-Type", ctSafeContentType(entry.Name))
	h.Set("Cache-Control", "private, no-store")
	ctDisposition(w, "attachment", entry.Name)

	// A download manager issues many range requests for one file. Only the
	// opening request is recorded, so one download stays one log line.
	if !ctIsResume(r) {
		a.touchRecent(r.Context(), user.ID, entry, "download")
		a.audit(r, "file.download", entry.Path, fmt.Sprintf("%d bytes", info.Size()), true)
	}
	http.ServeContent(w, r, entry.Name, info.ModTime(), f)
}

// handleRaw streams a file inline for the preview panel. Video and audio
// seeking depends on range support, which ServeContent provides.
func (a *API) handleRaw(w http.ResponseWriter, r *http.Request) {
	p, err := pathParam(r, "path")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	scope, err := a.scopeFor(r.Context(), currentUser(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	f, info, err := a.VFS.Open(scope, p)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defer f.Close()

	name := path.Base(p)
	ctHarden(w)
	h := w.Header()
	h.Set("Content-Type", ctSafeContentType(name))
	// Short lived so a preview that is scrubbed back and forth is not refetched,
	// short enough that an edited file shows its new contents quickly.
	h.Set("Cache-Control", "private, max-age=300")
	ctDisposition(w, "inline", name)

	http.ServeContent(w, r, name, info.ModTime(), f)
}

// handleThumb serves a cached thumbnail, generating it on first request.
func (a *API) handleThumb(w http.ResponseWriter, r *http.Request) {
	if a.Thumbs == nil {
		a.fail(w, r, apiError(http.StatusServiceUnavailable, "unavailable", "Thumbnails are turned off"))
		return
	}
	p, err := pathParam(r, "path")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	scope, err := a.scopeFor(r.Context(), currentUser(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	loc, err := a.VFS.Resolve(scope, p)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	info, err := loc.Root.Stat(loc.Rel)
	if err != nil {
		a.fail(w, r, ctFSError(err))
		return
	}
	if info.IsDir() {
		a.fail(w, r, ctNoPreview())
		return
	}

	res, err := a.Thumbs.Thumbnail(r.Context(), loc.Root, loc.Rel, queryInt(r, "size", 256), info)
	if err != nil {
		a.fail(w, r, a.ctThumbError(loc.Virtual, err))
		return
	}

	cached, err := os.Open(res.Path)
	if err != nil {
		// The entry was purged between generation and this read. Asking again
		// regenerates it, so report it as missing rather than as a failure.
		a.fail(w, r, errNotFound)
		return
	}
	defer cached.Close()
	st, err := cached.Stat()
	if err != nil {
		a.fail(w, r, errNotFound)
		return
	}

	// The cache key covers the source path, its modification time, its size and
	// the requested edge length, which is exactly what a validator needs.
	base := filepath.Base(res.Path)
	key := strings.TrimSuffix(base, filepath.Ext(base))

	ctHarden(w)
	h := w.Header()
	h.Set("Content-Type", res.ContentType)
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	h.Set("ETag", `"`+key+`"`)
	http.ServeContent(w, r, base, st.ModTime(), cached)
}

// ---- streaming zip ----------------------------------------------------------

// handleDownloadZip streams a zip of a selection, built as it is sent.
//
// Nothing is staged on disk and no length is declared, so the size of the
// selection is bounded only by the caller's patience.
func (a *API) handleDownloadZip(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	scope, err := a.scopeFor(r.Context(), user)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	paths := ctSelection(r)
	if len(paths) == 0 {
		a.fail(w, r, badRequest("Select at least one item to download"))
		return
	}
	items, mount, err := a.ctResolve(scope, paths)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	root, err := a.ctRootFor(scope, mount.Path)
	if err != nil {
		a.fail(w, r, err)
		return
	}

	name := ctZipName(r.URL.Query().Get("name"), items)
	rels := make([]string, 0, len(items))
	for _, it := range items {
		rels = append(rels, it.rel)
	}

	if len(items) == 1 {
		if entry, statErr := a.VFS.Stat(scope, items[0].virtual); statErr == nil {
			a.touchRecent(r.Context(), user.ID, entry, "download")
		}
	}
	a.audit(r, "file.download", items[0].virtual, fmt.Sprintf("%d items as %s", len(items), name), true)

	ctHarden(w)
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Cache-Control", "private, no-store")
	// Buffering a response of unknown length helps nobody, and a reverse proxy
	// that holds it back makes the browser look stalled.
	h.Set("X-Accel-Buffering", "no")
	ctDisposition(w, "attachment", name)
	w.WriteHeader(http.StatusOK)

	if err := archive.StreamZip(r.Context(), root, rels, w, nil); err != nil {
		// The status line is long gone, so there is no error to report. A
		// caller that hung up is the ordinary case and stays quiet.
		if r.Context().Err() != nil || errors.Is(err, context.Canceled) {
			return
		}
		a.Logger.Error("zip stream failed", "path", items[0].virtual, "err", err)
	}
}

// ---- archives ---------------------------------------------------------------

// handleArchivePreview lists what is inside an archive without extracting it.
func (a *API) handleArchivePreview(w http.ResponseWriter, r *http.Request) {
	p, err := pathParam(r, "path")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	scope, err := a.scopeFor(r.Context(), currentUser(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	loc, err := a.VFS.Resolve(scope, p)
	if err != nil {
		a.fail(w, r, err)
		return
	}

	limit := queryInt(r, "limit", 200)
	if limit <= 0 {
		limit = 200
	}
	if limit > 5000 {
		limit = 5000
	}

	// One entry beyond the limit is read so the answer can say honestly that
	// there is more to see.
	items, format, err := archive.Inspect(r.Context(), loc.Root, loc.Rel, limit+1)
	if err != nil {
		a.fail(w, r, a.ctArchiveError(loc.Virtual, err))
		return
	}
	truncated := len(items) > limit
	if truncated {
		items = items[:limit]
	}
	if items == nil {
		items = []archive.Item{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"format":    string(format),
		"items":     items,
		"truncated": truncated,
	})
}

// ctCompressRequest is the body of POST /api/v1/fs/compress.
type ctCompressRequest struct {
	Sources []string `json:"sources"`
	Dest    string   `json:"dest"`
	Name    string   `json:"name"`
	Format  string   `json:"format"`
}

// handleCompress creates an archive of a selection as a background job.
func (a *API) handleCompress(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var req ctCompressRequest
	if err := decode(r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	if len(req.Sources) == 0 {
		a.fail(w, r, badRequest("Select at least one item to compress"))
		return
	}
	format, err := ctParseFormat(req.Format)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	scope, err := a.scopeFor(r.Context(), user)
	if err != nil {
		a.fail(w, r, err)
		return
	}

	items, mount, err := a.ctResolve(scope, ctCleanAll(req.Sources))
	if err != nil {
		a.fail(w, r, err)
		return
	}

	dest := vfs.Clean(req.Dest)
	if dest == "" {
		dest = path.Dir(items[0].virtual)
	}
	destLoc, err := a.VFS.ResolveWritable(scope, dest)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	destInfo, err := destLoc.Root.Stat(destLoc.Rel)
	if err != nil {
		a.fail(w, r, ctFSError(err))
		return
	}
	if !destInfo.IsDir() {
		a.fail(w, r, badRequest("The destination is not a folder"))
		return
	}

	name, err := ctArchiveName(req.Name, format, items, destLoc.Name())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	// A free name keeps an existing archive from being overwritten silently.
	name, err = a.VFS.UniqueName(scope, destLoc.Virtual, name)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	target := path.Join(destLoc.Virtual, name)

	// Writing the archive into one of the folders being archived would fold a
	// half written copy of itself into the result.
	for _, it := range items {
		if it.isDir && vfs.Contains(it.virtual, target) {
			a.fail(w, r, badRequest("Choose a destination outside the items you are compressing"))
			return
		}
	}

	sources := make([]string, 0, len(items))
	rels := make([]string, 0, len(items))
	for _, it := range items {
		sources = append(sources, it.virtual)
		rels = append(rels, it.rel)
	}
	mountPath := mount.Path
	destVirtual := destLoc.Virtual
	userID := user.ID

	params := map[string]any{
		"sources": sources,
		"dest":    destVirtual,
		"name":    name,
		"format":  string(format),
		"path":    target,
	}

	job, err := a.Jobs.Submit(r.Context(), userID, "compress", "Creating "+name, params,
		func(ctx context.Context, j *jobs.Job) error {
			root, err := a.ctRootFor(scope, mountPath)
			if err != nil {
				return err
			}

			var totalBytes, totalItems int64
			for _, src := range sources {
				b, n, err := a.VFS.DirSize(ctx, scope, src)
				if err != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					// A source that cannot be measured still gets archived.
					continue
				}
				totalBytes += b
				totalItems += n
			}
			j.SetTotal(totalBytes, totalItems)

			f, created, err := a.VFS.Create(scope, target, false)
			if err != nil {
				return err
			}
			writeErr := archive.Create(ctx, root, rels, f, format, func(bytes, count int64, current string) {
				j.Progress(bytes, count, current)
			})
			closeErr := f.Close()
			if writeErr == nil {
				writeErr = closeErr
			}
			if writeErr != nil {
				// Nothing usable was produced, so the half written file goes.
				a.ctRemove(scope, created)
				return writeErr
			}

			var size int64
			if entry, err := a.VFS.Stat(scope, created); err == nil {
				size = entry.Size
			}
			j.SetResult(map[string]any{"path": created, "bytes": size})
			j.SetMessage("Created " + path.Base(created))
			a.publish(userID, events.EventFSChanged, map[string]any{"path": destVirtual})
			return nil
		})
	if err != nil {
		a.fail(w, r, err)
		return
	}

	a.audit(r, "fs.compress", target, fmt.Sprintf("%d items, %s", len(rels), format), true)
	writeJSON(w, http.StatusAccepted, job)
}

// ctExtractRequest is the body of POST /api/v1/fs/extract.
type ctExtractRequest struct {
	Path string `json:"path"`
	Dest string `json:"dest"`
}

// handleExtract unpacks an archive into a folder as a background job.
func (a *API) handleExtract(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var req ctExtractRequest
	if err := decode(r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	source := vfs.Clean(req.Path)
	if source == "" {
		a.fail(w, r, badRequest("Choose an archive to extract"))
		return
	}
	scope, err := a.scopeFor(r.Context(), user)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	srcLoc, err := a.VFS.Resolve(scope, source)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	srcInfo, err := srcLoc.Root.Stat(srcLoc.Rel)
	if err != nil {
		a.fail(w, r, ctFSError(err))
		return
	}
	if srcInfo.IsDir() {
		a.fail(w, r, badRequest("That path is a folder, not an archive"))
		return
	}
	format := archive.DetectFormat(srcLoc.Name())
	if format == "" {
		a.fail(w, r, apiError(http.StatusUnsupportedMediaType, "unsupported", "Storix cannot open this kind of archive"))
		return
	}

	dest := vfs.Clean(req.Dest)
	if dest == "" {
		dest = ctDefaultExtractDest(srcLoc.Virtual, format)
	}
	destLoc, err := a.VFS.ResolveWritable(scope, dest)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if info, err := destLoc.Root.Stat(destLoc.Rel); err == nil {
		if !info.IsDir() {
			a.fail(w, r, badRequest("The destination is not a folder"))
			return
		}
	} else if errors.Is(err, fs.ErrNotExist) {
		if _, err := a.VFS.MkdirAll(scope, destLoc.Virtual); err != nil {
			a.fail(w, r, err)
			return
		}
	} else {
		a.fail(w, r, ctFSError(err))
		return
	}

	srcVirtual := srcLoc.Virtual
	srcMount := srcLoc.Mount.Path
	srcRel := srcLoc.Rel
	destVirtual := destLoc.Virtual
	destMount := destLoc.Mount.Path
	destRel := destLoc.Rel
	archiveSize := srcInfo.Size()
	userID := user.ID

	params := map[string]any{"path": srcVirtual, "dest": destVirtual, "format": string(format)}
	title := "Extracting " + path.Base(srcVirtual)

	job, err := a.Jobs.Submit(r.Context(), userID, "extract", title, params,
		func(ctx context.Context, j *jobs.Job) error {
			srcRoot, err := a.ctRootFor(scope, srcMount)
			if err != nil {
				return err
			}
			dstRoot, err := a.ctRootFor(scope, destMount)
			if err != nil {
				return err
			}

			totalBytes, totalItems := ctExtractTotals(ctx, srcRoot, srcRel, format, archiveSize)
			j.SetTotal(totalBytes, totalItems)

			report, err := archive.Extract(ctx, srcRoot, srcRel, dstRoot, destRel, func(bytes, count int64, current string) {
				j.Progress(bytes, count, current)
			})
			if err != nil {
				return err
			}
			j.SetResult(report)
			if n := len(report.Skipped); n > 0 {
				j.SetMessage(fmt.Sprintf("%d entries were skipped because their names pointed outside the folder", n))
			} else {
				j.SetMessage("Extracted into " + path.Base(destVirtual))
			}
			a.publish(userID, events.EventFSChanged, map[string]any{"path": destVirtual})
			return nil
		})
	if err != nil {
		a.fail(w, r, err)
		return
	}

	a.audit(r, "fs.extract", srcVirtual, "into "+destVirtual, true)
	writeJSON(w, http.StatusAccepted, job)
}

// ---- helpers ----------------------------------------------------------------

// ctItem is one resolved member of a selection.
type ctItem struct {
	virtual string
	rel     string
	name    string
	isDir   bool
}

// ctSelection reads a selection from the query string. Both a comma separated
// paths list and repeated path parameters are accepted, so a name holding a
// comma can still be requested one parameter at a time.
func ctSelection(r *http.Request) []string {
	q := r.URL.Query()
	raw := make([]string, 0, len(q["paths"])+len(q["path"]))
	raw = append(raw, q["paths"]...)
	raw = append(raw, q["path"]...)

	out := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, group := range raw {
		for _, part := range strings.Split(group, ",") {
			p := vfs.Clean(part)
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// ctCleanAll normalizes a list of virtual paths and drops duplicates.
func ctCleanAll(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, raw := range in {
		p := vfs.Clean(raw)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// ctResolve resolves a selection and insists every member lives in one mount,
// because an archive is built from a single guarded root.
func (a *API) ctResolve(scope vfs.Scope, paths []string) ([]ctItem, vfs.Mount, error) {
	var mount vfs.Mount
	if len(paths) == 0 {
		return nil, mount, badRequest("Select at least one item")
	}
	if len(paths) > ctMaxSelection {
		return nil, mount, badRequest("That is too many items for one operation")
	}

	items := make([]ctItem, 0, len(paths))
	for i, p := range paths {
		loc, err := a.VFS.Resolve(scope, p)
		if err != nil {
			return nil, mount, err
		}
		if i == 0 {
			mount = loc.Mount
		} else if loc.Mount.Path != mount.Path {
			return nil, mount, badRequest("Select items from a single location")
		}
		info, err := loc.Root.Lstat(loc.Rel)
		if err != nil {
			return nil, mount, ctFSError(err)
		}
		isDir := info.IsDir()
		if info.Mode()&fs.ModeSymlink != 0 {
			if target, err := loc.Root.Stat(loc.Rel); err == nil {
				isDir = target.IsDir()
			}
		}
		items = append(items, ctItem{virtual: loc.Virtual, rel: loc.Rel, name: loc.Name(), isDir: isDir})
	}
	return items, mount, nil
}

// ctRootFor returns the guarded root of a mount. Jobs call it when they start
// rather than holding a handle from the request that created them.
func (a *API) ctRootFor(scope vfs.Scope, mountPath string) (*os.Root, error) {
	loc, err := a.VFS.Resolve(scope, mountPath)
	if err != nil {
		return nil, err
	}
	return loc.Root, nil
}

// ctRemove deletes a file that an operation left half written.
func (a *API) ctRemove(scope vfs.Scope, virtual string) {
	if virtual == "" {
		return
	}
	loc, err := a.VFS.ResolveWritable(scope, virtual)
	if err != nil {
		return
	}
	if err := loc.Root.Remove(loc.Rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
		a.Logger.Warn("remove partial archive", "path", virtual, "err", err)
	}
}

// ctHarden applies the headers that keep stored bytes inert.
func ctHarden(w http.ResponseWriter) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", ctStreamPolicy)
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("X-Frame-Options", "DENY")
}

// ctSafeContentType maps a file name onto a media type Storix is willing to
// declare. Everything else becomes a byte stream.
func ctSafeContentType(name string) string {
	typ := mime.TypeByExtension(strings.ToLower(path.Ext(name)))
	if typ == "" {
		return "application/octet-stream"
	}
	base := typ
	if i := strings.IndexByte(base, ';'); i >= 0 {
		base = base[:i]
	}
	if !ctSafeTypes[strings.ToLower(strings.TrimSpace(base))] {
		return "application/octet-stream"
	}
	return typ
}

// ctDisposition writes a disposition header carrying the name twice: a plain
// ASCII form every client understands, and the RFC 5987 form that keeps
// accents, Cyrillic and Chinese names intact.
func ctDisposition(w http.ResponseWriter, mode, name string) {
	w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename=%q; filename*=UTF-8''%s",
		mode, ctASCIIName(name), ctEncodeName(name)))
}

// ctASCIIName reduces a file name to a form that is safe inside a quoted
// header value.
func ctASCIIName(name string) string {
	name = path.Base(strings.TrimSpace(name))
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c < 0x20 || c > 0x7e, c == '"', c == '\\', c == '/', c == ';', c == ',':
			b.WriteByte('_')
		default:
			b.WriteByte(c)
		}
	}
	out := strings.Trim(b.String(), "._ ")
	if out == "" {
		return "download"
	}
	if len(out) > 180 {
		out = out[:180]
	}
	return out
}

// ctEncodeName percent encodes a name for the filename* parameter, keeping only
// the characters RFC 5987 allows unescaped.
func ctEncodeName(name string) string {
	name = path.Base(strings.TrimSpace(name))
	if name == "" {
		return "download"
	}
	const keep = "!#$&+-.^_`|~"
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			strings.IndexByte(keep, c) >= 0:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// ctIsResume reports whether the request continues a transfer from somewhere
// other than the first byte.
func ctIsResume(r *http.Request) bool {
	rng := strings.TrimSpace(r.Header.Get("Range"))
	if rng == "" {
		return false
	}
	return !strings.HasPrefix(rng, "bytes=0-")
}

// ctSanitizeName strips path separators and control characters from a name the
// caller supplied.
func ctSanitizeName(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	raw = strings.ReplaceAll(raw, "\\", "/")
	raw = path.Base(path.Clean("/" + raw))
	var b strings.Builder
	for _, ru := range raw {
		if ru < 0x20 || ru == 0x7f || ru == '/' {
			continue
		}
		b.WriteRune(ru)
	}
	out := strings.Trim(b.String(), ". ")
	if out == "." || out == ".." {
		return ""
	}
	if len(out) > 120 {
		out = strings.TrimSpace(out[:120])
	}
	return out
}

// ctZipName picks the file name of a streamed selection.
func ctZipName(requested string, items []ctItem) string {
	name := ctSanitizeName(requested)
	if name == "" && len(items) == 1 && items[0].isDir {
		name = ctSanitizeName(items[0].name)
	}
	if name == "" {
		name = "storix-selection"
	}
	if !strings.HasSuffix(strings.ToLower(name), ".zip") {
		name += ".zip"
	}
	return name
}

// ctArchiveName builds the file name of an archive that is about to be created.
func ctArchiveName(requested string, format archive.Format, items []ctItem, destName string) (string, error) {
	name := ctSanitizeName(requested)
	if name == "" {
		if len(items) == 1 {
			name = ctSanitizeName(items[0].name)
		} else {
			name = ctSanitizeName(destName)
		}
	}
	if name == "" {
		name = "archive"
	}
	lower := strings.ToLower(name)
	for _, ext := range ctArchiveExts {
		if strings.HasSuffix(lower, ext) {
			name = strings.TrimSpace(name[:len(name)-len(ext)])
			break
		}
	}
	if name == "" {
		name = "archive"
	}
	name += format.Extension()
	if err := vfs.ValidName(name); err != nil {
		return "", badRequest("That archive name is not allowed")
	}
	return name, nil
}

// ctParseFormat reads the requested container, defaulting to zip.
func ctParseFormat(raw string) (archive.Format, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "zip":
		return archive.FormatZip, nil
	case "tar":
		return archive.FormatTar, nil
	case "tar.gz", "targz", "tgz", "gz", "gzip":
		return archive.FormatTarGz, nil
	}
	return "", badRequest("Choose zip, tar.gz or tar")
}

// ctDefaultExtractDest suggests a folder beside the archive, named after it.
func ctDefaultExtractDest(source string, format archive.Format) string {
	base := path.Base(source)
	if ext := format.Extension(); ext != "" && strings.HasSuffix(strings.ToLower(base), ext) {
		base = base[:len(base)-len(ext)]
	} else if ext := path.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	if base == "" {
		base = "extracted"
	}
	return path.Join(path.Dir(source), base)
}

// ctExtractTotals estimates the work ahead so the progress bar means something.
// A zip carries its uncompressed sizes in the central directory, which is cheap
// to read. A plain tar is as large as its contents. A compressed tar would have
// to be decompressed twice to be measured, so its total is left unknown and the
// job reports the file it is on instead.
func ctExtractTotals(ctx context.Context, root *os.Root, rel string, format archive.Format, size int64) (int64, int64) {
	switch format {
	case archive.FormatZip:
		items, _, err := archive.Inspect(ctx, root, rel, 0)
		if err != nil {
			return 0, 0
		}
		var bytes, count int64
		for _, it := range items {
			count++
			if !it.IsDir {
				bytes += it.Size
			}
		}
		return bytes, count
	case archive.FormatTar:
		return size, 0
	}
	return 0, 0
}

// ctFSError maps a raw file system error onto the JSON envelope.
func ctFSError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return errNotFound
	case errors.Is(err, fs.ErrPermission):
		return errForbidden
	}
	return err
}

// ctNoPreview is the answer for a file that simply has no thumbnail.
func ctNoPreview() error {
	return apiError(http.StatusUnsupportedMediaType, "unsupported", "There is no preview for this file")
}

// ctThumbError keeps a file that is not an image out of the error log and off
// the 500 path. Only a cancelled request and a genuinely missing file are
// reported as anything other than an absent preview.
func (a *API) ctThumbError(virtual string, err error) error {
	switch {
	case errors.Is(err, thumbs.ErrUnsupported):
		return ctNoPreview()
	case errors.Is(err, thumbs.ErrTooLarge):
		return apiError(http.StatusRequestEntityTooLarge, "too_large", "That image is too large to preview")
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, vfs.ErrNotFound):
		return errNotFound
	case errors.Is(err, fs.ErrPermission), errors.Is(err, vfs.ErrDenied), errors.Is(err, vfs.ErrForbidden):
		return errForbidden
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	}
	// A truncated or malformed image lands here. It is worth a log line, but
	// the caller only needs to know that there is no preview.
	a.Logger.Debug("thumbnail failed", "path", virtual, "err", err)
	return ctNoPreview()
}

// ctArchiveError maps the archive package errors onto the JSON envelope.
func (a *API) ctArchiveError(virtual string, err error) error {
	switch {
	case errors.Is(err, archive.ErrUnsupportedFormat):
		return apiError(http.StatusUnsupportedMediaType, "unsupported", "Storix cannot read this kind of archive")
	case errors.Is(err, archive.ErrInvalidPath), errors.Is(err, archive.ErrNotDirectory):
		return badRequest("That path is not an archive")
	case errors.Is(err, archive.ErrNoSources):
		return badRequest("Select at least one item")
	case errors.Is(err, archive.ErrTooManyEntries):
		return apiError(http.StatusUnprocessableEntity, "too_many_entries", "That archive holds too many entries to open")
	case errors.Is(err, archive.ErrBomb):
		return apiError(http.StatusUnprocessableEntity, "unsafe_archive", "That archive expands far beyond its size and was refused")
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, vfs.ErrNotFound):
		return errNotFound
	case errors.Is(err, fs.ErrPermission):
		return errForbidden
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	}
	a.Logger.Warn("archive read failed", "path", virtual, "err", err)
	return apiError(http.StatusUnprocessableEntity, "unreadable", "That archive could not be read")
}
