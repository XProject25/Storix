package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/XProject25/Storix/internal/events"
	"github.com/XProject25/Storix/internal/jobs"
	"github.com/XProject25/Storix/internal/store"
	"github.com/XProject25/Storix/internal/vfs"
)

// Bounds for the file handlers. They keep a single request from turning into
// an unbounded walk of the server.
const (
	// fsSyncDeleteLimit is how many plain files a delete may remove inside the
	// request itself before the work moves to a background job.
	fsSyncDeleteLimit = 50
	// fsMaxTreeDepth caps the folder tree the move and copy dialog asks for.
	fsMaxTreeDepth = 3
	// fsTreeMaxNodes caps how many folders one tree level reports.
	fsTreeMaxNodes = 2000
	// fsTreeProbeLimit is how many directory entries the "has children" probe
	// reads before it gives up and answers no.
	fsTreeProbeLimit = 512
	// fsMaxSelection caps how many paths a single operation may carry.
	fsMaxSelection = 10000
	// fsDuTimeout bounds the recursive size calculation.
	fsDuTimeout = 20 * time.Second
	// fsStoreTimeout bounds the bookkeeping writes that follow an operation.
	fsStoreTimeout = 10 * time.Second
	// fsAuditDetailMax caps how much of a selection lands in the audit log.
	fsAuditDetailMax = 300
)

// ---- response shapes --------------------------------------------------------

// fsCrumb is one step of the breadcrumb trail.
type fsCrumb struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// fsListResponse is a directory listing plus the browser context the UI needs.
// The listing fields are inlined, so the frontend sees one flat object.
type fsListResponse struct {
	*vfs.Listing
	Favorite    bool      `json:"favorite"`
	CanWrite    bool      `json:"canWrite"`
	Breadcrumbs []fsCrumb `json:"breadcrumbs"`
}

// fsStatResponse is a single entry plus whether it is pinned.
type fsStatResponse struct {
	vfs.Entry
	Favorite bool `json:"favorite"`
}

// fsTreeNode is one folder in the sidebar or in the move dialog.
type fsTreeNode struct {
	Name        string       `json:"name"`
	Path        string       `json:"path"`
	HasChildren bool         `json:"hasChildren"`
	Children    []fsTreeNode `json:"children,omitempty"`
}

// ---- request bodies ---------------------------------------------------------

type fsNameRequest struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

type fsTouchRequest struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

type fsTransferRequest struct {
	Sources  []string `json:"sources"`
	Dest     string   `json:"dest"`
	Conflict string   `json:"conflict"`
}

type fsDeleteRequest struct {
	Paths     []string `json:"paths"`
	Permanent bool     `json:"permanent"`
}

type fsTextRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type fsChmodRequest struct {
	Path      string `json:"path"`
	Mode      string `json:"mode"`
	Recursive bool   `json:"recursive"`
}

type fsChownRequest struct {
	Path      string `json:"path"`
	Owner     string `json:"owner"`
	Group     string `json:"group"`
	Recursive bool   `json:"recursive"`
}

// ---- browsing ---------------------------------------------------------------

// handleList reads one directory, or the mount list when no path is given.
func (a *API) handleList(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	scope, err := a.scopeFor(r.Context(), user)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	noCache(w)

	raw := strings.TrimSpace(r.URL.Query().Get("path"))
	if raw == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"path":    "",
			"mounts":  a.VFS.Mounts(scope),
			"entries": []vfs.Entry{},
			"isRoot":  true,
		})
		return
	}

	listing, err := a.VFS.List(scope, vfs.Clean(raw), vfs.ListOptions{
		ShowHidden:   queryBool(r, "hidden"),
		Sort:         strings.TrimSpace(r.URL.Query().Get("sort")),
		Order:        strings.TrimSpace(r.URL.Query().Get("order")),
		Filter:       strings.TrimSpace(r.URL.Query().Get("filter")),
		FoldersFirst: true,
		Limit:        queryInt(r, "limit", a.Config.Limits.ListPageSize),
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, fsListResponse{
		Listing:     listing,
		Favorite:    a.fsIsFavorite(r.Context(), user, listing.Path),
		CanWrite:    !listing.ReadOnly && (user.Can(store.PermCreate) || user.Can(store.PermUpload)),
		Breadcrumbs: fsBreadcrumbs(listing.Mount, listing.Path),
	})
}

// handleStat describes a single file or folder.
func (a *API) handleStat(w http.ResponseWriter, r *http.Request) {
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
	noCache(w)
	writeJSON(w, http.StatusOK, fsStatResponse{
		Entry:    entry,
		Favorite: a.fsIsFavorite(r.Context(), user, entry.Path),
	})
}

// handleTree returns folders only, for the sidebar and the move dialog.
func (a *API) handleTree(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	scope, err := a.scopeFor(r.Context(), user)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	depth := queryInt(r, "depth", 1)
	if depth < 1 {
		depth = 1
	}
	if depth > fsMaxTreeDepth {
		depth = fsMaxTreeDepth
	}
	hidden := queryBool(r, "hidden")
	noCache(w)

	raw := strings.TrimSpace(r.URL.Query().Get("path"))
	if raw == "" {
		mounts := a.VFS.Mounts(scope)
		children := make([]fsTreeNode, 0, len(mounts))
		for _, m := range mounts {
			node := fsTreeNode{Name: fsMountLabel(m), Path: m.Path}
			if depth > 1 {
				if kids, err := a.fsTreeChildren(scope, m.Path, depth-1, hidden); err == nil {
					node.Children = kids
					node.HasChildren = len(kids) > 0
					children = append(children, node)
					continue
				}
			}
			node.HasChildren = a.fsHasSubfolder(scope, m.Path, hidden)
			children = append(children, node)
		}
		writeJSON(w, http.StatusOK, map[string]any{"path": "", "children": children})
		return
	}

	p := vfs.Clean(raw)
	children, err := a.fsTreeChildren(scope, p, depth, hidden)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "children": children})
}

// handleSearch walks the scope for names, and optionally file content.
func (a *API) handleSearch(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		a.fail(w, r, badRequest("Type something to search for"))
		return
	}
	scope, err := a.scopeFor(r.Context(), user)
	if err != nil {
		a.fail(w, r, err)
		return
	}

	maxResults := a.Config.Limits.SearchMaxResults
	limit := queryInt(r, "limit", maxResults)
	if limit <= 0 || limit > maxResults {
		limit = maxResults
	}
	start := strings.TrimSpace(r.URL.Query().Get("path"))
	if start != "" {
		start = vfs.Clean(start)
	}

	result, err := a.VFS.Search(r.Context(), scope, vfs.SearchOptions{
		Query:         q,
		Path:          start,
		Kinds:         fsParseKinds(r.URL.Query().Get("kind")),
		MaxResults:    limit,
		Timeout:       a.Config.Limits.SearchTimeout.D(),
		IncludeHidden: queryBool(r, "hidden"),
		Content:       queryBool(r, "content"),
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	noCache(w)
	writeJSON(w, http.StatusOK, result)
}

// handleDu reports the recursive size of a folder.
func (a *API) handleDu(w http.ResponseWriter, r *http.Request) {
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
	ctx, cancel := context.WithTimeout(r.Context(), fsDuTimeout)
	defer cancel()

	bytes, items, err := a.VFS.DirSize(ctx, scope, p)
	partial := errors.Is(err, context.DeadlineExceeded)
	if err != nil && !partial {
		a.fail(w, r, err)
		return
	}
	noCache(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"path":    p,
		"bytes":   bytes,
		"items":   items,
		"partial": partial,
	})
}

// handleDisk reports usage of the volume that holds a path.
func (a *API) handleDisk(w http.ResponseWriter, r *http.Request) {
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
	usage, err := a.VFS.Disk(scope, p)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	noCache(w)
	writeJSON(w, http.StatusOK, usage)
}

// ---- creating ---------------------------------------------------------------

// handleMkdir creates one folder.
func (a *API) handleMkdir(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var body fsNameRequest
	if err := decode(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	parent := vfs.Clean(body.Path)
	name := strings.TrimSpace(body.Name)
	if parent == "" {
		a.fail(w, r, badRequest("Missing parent folder"))
		return
	}
	scope, err := a.scopeFor(r.Context(), user)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	entry, err := a.VFS.Mkdir(scope, parent, name)
	if err != nil {
		a.audit(r, "folder.create", path.Join(parent, name), fsMessage(err), false)
		a.fail(w, r, err)
		return
	}
	a.audit(r, "folder.create", entry.Path, "", true)
	a.fsNotify(user.ID, parent, "mkdir")
	writeJSON(w, http.StatusOK, entry)
}

// handleTouch creates one empty file, optionally with starting content.
func (a *API) handleTouch(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var body fsTouchRequest
	if err := decode(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	parent := vfs.Clean(body.Path)
	name := strings.TrimSpace(body.Name)
	if parent == "" {
		a.fail(w, r, badRequest("Missing parent folder"))
		return
	}
	if err := vfs.ValidName(name); err != nil {
		a.fail(w, r, err)
		return
	}
	if int64(len(body.Content)) > a.VFS.MaxTextBytes() {
		a.fail(w, r, vfs.ErrTooLarge)
		return
	}
	scope, err := a.scopeFor(r.Context(), user)
	if err != nil {
		a.fail(w, r, err)
		return
	}

	target := path.Join(parent, name)
	f, virtual, err := a.VFS.Create(scope, target, false)
	if err != nil {
		a.audit(r, "file.create", target, fsMessage(err), false)
		a.fail(w, r, err)
		return
	}
	if body.Content != "" {
		if _, err := io.WriteString(f, body.Content); err != nil {
			f.Close()
			a.fail(w, r, err)
			return
		}
	}
	if err := f.Close(); err != nil {
		a.fail(w, r, err)
		return
	}

	entry, err := a.VFS.Stat(scope, virtual)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.audit(r, "file.create", entry.Path, "", true)
	a.fsNotify(user.ID, parent, "touch")
	writeJSON(w, http.StatusOK, entry)
}

// handleRename renames a file or folder in place.
func (a *API) handleRename(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var body fsNameRequest
	if err := decode(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	source := vfs.Clean(body.Path)
	name := strings.TrimSpace(body.Name)
	if source == "" {
		a.fail(w, r, badRequest("Missing path"))
		return
	}
	scope, err := a.scopeFor(r.Context(), user)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	entry, err := a.VFS.Rename(scope, source, name)
	if err != nil {
		a.audit(r, "file.rename", source, fsMessage(err), false)
		a.fail(w, r, err)
		return
	}

	if err := a.Store.RenameFavorites(r.Context(), fsOwnerScope(user), source, entry.Path); err != nil {
		a.Logger.Warn("move pinned locations after rename", "path", source, "err", err)
	}
	a.audit(r, "file.rename", entry.Path, "was "+path.Base(source), true)
	a.fsNotify(user.ID, path.Dir(source), "rename")
	writeJSON(w, http.StatusOK, entry)
}

// ---- moving and copying -----------------------------------------------------

// handleMove relocates a selection into another folder as a background job.
func (a *API) handleMove(w http.ResponseWriter, r *http.Request) { a.fsTransfer(w, r, true) }

// handleCopy duplicates a selection into another folder as a background job.
func (a *API) handleCopy(w http.ResponseWriter, r *http.Request) { a.fsTransfer(w, r, false) }

// fsTransfer is the shared body of move and copy. The scope is resolved once,
// here in the request, and captured by the job so nothing touches the request
// after the handler has returned.
func (a *API) fsTransfer(w http.ResponseWriter, r *http.Request, move bool) {
	user := currentUser(r)
	var body fsTransferRequest
	if err := decode(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	sources := fsCleanList(body.Sources)
	dest := vfs.Clean(body.Dest)
	if len(sources) == 0 {
		a.fail(w, r, badRequest("Select at least one item"))
		return
	}
	if len(sources) > fsMaxSelection {
		a.fail(w, r, badRequest("Too many items in one operation"))
		return
	}
	if dest == "" {
		a.fail(w, r, badRequest("Choose a destination folder"))
		return
	}
	policy, err := fsParseConflict(body.Conflict)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	for _, src := range sources {
		if vfs.Contains(src, dest) {
			a.fail(w, r, badRequest("A folder cannot be moved into itself"))
			return
		}
	}
	scope, err := a.scopeFor(r.Context(), user)
	if err != nil {
		a.fail(w, r, err)
		return
	}

	typ, action, verb := "copy", "file.copy", "Copying"
	if move {
		typ, action, verb = "move", "file.move", "Moving"
	}
	title := fmt.Sprintf("%s %s to %s", verb, fsCountLabel(len(sources)), fsDisplayName(dest))
	parents := fsParentsOf(sources)
	userID := user.ID

	job, err := a.Jobs.Submit(r.Context(), userID, typ, title, body, func(ctx context.Context, j *jobs.Job) error {
		progress := vfs.Progress(func(st vfs.ProgressState) {
			j.SetTotal(st.TotalBytes, st.TotalItems)
			j.Progress(st.Bytes, st.Items, st.Current)
		})
		var result *vfs.OpResult
		var opErr error
		if move {
			result, opErr = a.VFS.Move(ctx, scope, sources, dest, policy, progress)
		} else {
			result, opErr = a.VFS.Copy(ctx, scope, sources, dest, policy, progress)
		}
		if opErr != nil {
			if errors.Is(opErr, context.Canceled) {
				return opErr
			}
			return fmt.Errorf("could not finish: %s", fsMessage(opErr))
		}
		j.SetResult(result)
		for _, parent := range parents {
			a.fsNotify(userID, parent, typ)
		}
		a.fsNotify(userID, dest, typ)
		return nil
	})
	if err != nil {
		a.audit(r, action, dest, fsMessage(err), false)
		a.fail(w, r, err)
		return
	}
	a.audit(r, action, dest, fsSelectionDetail(sources), true)
	writeJSON(w, http.StatusAccepted, job)
}

// ---- deleting ---------------------------------------------------------------

// handleDelete moves a selection to the recycle bin, or erases it for good.
// A short selection of plain files is handled inside the request so the
// interface reacts at once; anything larger becomes a background job.
func (a *API) handleDelete(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var body fsDeleteRequest
	if err := decode(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	paths := fsCleanList(body.Paths)
	if len(paths) == 0 {
		a.fail(w, r, badRequest("Select at least one item"))
		return
	}
	if len(paths) > fsMaxSelection {
		a.fail(w, r, badRequest("Too many items in one operation"))
		return
	}
	scope, err := a.scopeFor(r.Context(), user)
	if err != nil {
		a.fail(w, r, err)
		return
	}

	permanent := body.Permanent
	action, reason := "file.trash", "trash"
	if permanent {
		action, reason = "file.delete", "delete"
	}
	parents := fsParentsOf(paths)
	userID := user.ID

	if !a.fsNeedsDeleteJob(scope, paths) {
		var failures []string
		var last error
		done := 0
		for _, p := range paths {
			if err := a.fsDeleteOne(r.Context(), scope, user, p, permanent); err != nil {
				last = err
				failures = append(failures, fmt.Sprintf("%s: %s", path.Base(p), fsMessage(err)))
				continue
			}
			done++
		}
		a.audit(r, action, fsDisplayName(parents[0]), fsSelectionDetail(paths), len(failures) == 0)
		for _, parent := range parents {
			a.fsNotify(userID, parent, reason)
		}
		if done == 0 && last != nil {
			a.fail(w, r, last)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":        true,
			"deleted":   done,
			"permanent": permanent,
			"errors":    failures,
		})
		return
	}

	title := fmt.Sprintf("Moving %s to the recycle bin", fsCountLabel(len(paths)))
	if permanent {
		title = fmt.Sprintf("Deleting %s", fsCountLabel(len(paths)))
	}
	owner := user

	job, err := a.Jobs.Submit(r.Context(), userID, "delete", title, body, func(ctx context.Context, j *jobs.Job) error {
		j.SetTotal(0, int64(len(paths)))
		var failures []string
		var done int64
		for _, p := range paths {
			if err := ctx.Err(); err != nil {
				return err
			}
			j.Progress(0, done, path.Base(p))
			if err := a.fsDeleteOne(ctx, scope, owner, p, permanent); err != nil {
				failures = append(failures, fmt.Sprintf("%s: %s", path.Base(p), fsMessage(err)))
				continue
			}
			done++
			j.Progress(0, done, path.Base(p))
		}
		j.SetResult(map[string]any{"deleted": done, "permanent": permanent, "errors": failures})
		for _, parent := range parents {
			a.fsNotify(userID, parent, reason)
		}
		if done == 0 && len(failures) > 0 {
			return errors.New("Nothing could be removed")
		}
		return nil
	})
	if err != nil {
		a.audit(r, action, fsDisplayName(parents[0]), fsMessage(err), false)
		a.fail(w, r, err)
		return
	}
	a.audit(r, action, fsDisplayName(parents[0]), fsSelectionDetail(paths), true)
	writeJSON(w, http.StatusAccepted, job)
}

// fsNeedsDeleteJob reports whether a delete is too heavy for the request cycle.
// A folder is always a job, since its size is unknown until it is walked.
func (a *API) fsNeedsDeleteJob(scope vfs.Scope, paths []string) bool {
	if len(paths) > fsSyncDeleteLimit {
		return true
	}
	for _, p := range paths {
		entry, err := a.VFS.Stat(scope, p)
		if err != nil {
			continue
		}
		if entry.IsDir {
			return true
		}
	}
	return false
}

// fsDeleteOne removes a single path and clears what pointed at it.
func (a *API) fsDeleteOne(ctx context.Context, scope vfs.Scope, user *store.User, p string, permanent bool) error {
	p = vfs.Clean(p)
	if p == "" {
		return badRequest("Missing path")
	}
	if permanent {
		loc, err := a.VFS.ResolveWritable(scope, p)
		if err != nil {
			return err
		}
		if loc.Rel == "." {
			return apiError(http.StatusForbidden, "forbidden", "A mounted folder cannot be deleted here")
		}
		if _, err := loc.Root.Lstat(loc.Rel); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return vfs.ErrNotFound
			}
			return err
		}
		if err := loc.Root.RemoveAll(loc.Rel); err != nil {
			return err
		}
	} else {
		rec, err := a.VFS.MoveToTrash(ctx, scope, p)
		if err != nil {
			return err
		}
		a.fsRecordTrash(ctx, user, rec)
	}
	a.fsForgetPath(ctx, user, p)
	return nil
}

// fsRecordTrash persists the recycle bin entry for a moved item. The write uses
// a context of its own, so an operation cancelled at the wrong moment cannot
// leave data in the bin that nothing knows about.
func (a *API) fsRecordTrash(ctx context.Context, user *store.User, rec *vfs.TrashRecord) {
	if rec == nil {
		return
	}
	now := time.Now().UTC()
	item := &store.TrashItem{
		UserID:       user.ID,
		Name:         rec.Name,
		OriginalPath: rec.OriginalPath,
		StoredPath:   rec.StoredPath,
		IsDir:        rec.IsDir,
		Size:         rec.Size,
		DeletedAt:    now,
	}
	if retention := a.Config.Limits.TrashRetention.D(); retention > 0 {
		item.ExpiresAt = now.Add(retention)
	}
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fsStoreTimeout)
	defer cancel()
	if _, err := a.Store.AddTrashItem(dbCtx, item); err != nil {
		a.Logger.Error("record recycle bin entry", "path", rec.OriginalPath, "err", err)
	}
}

// fsForgetPath drops the public links and the pin that pointed at a path that
// no longer exists.
func (a *API) fsForgetPath(ctx context.Context, user *store.User, p string) {
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fsStoreTimeout)
	defer cancel()
	if _, err := a.Store.DeleteSharesUnder(dbCtx, fsOwnerScope(user), p); err != nil {
		a.Logger.Warn("revoke links for removed path", "path", p, "err", err)
	}
	if err := a.Store.RemoveFavorite(dbCtx, user.ID, p); err != nil && !errors.Is(err, store.ErrNotFound) {
		a.Logger.Warn("unpin removed path", "path", p, "err", err)
	}
}

// ---- editing ----------------------------------------------------------------

// handleReadText loads a file into the built in editor.
func (a *API) handleReadText(w http.ResponseWriter, r *http.Request) {
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
	text, err := a.VFS.ReadText(scope, p)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.touchRecent(r.Context(), user.ID, vfs.Entry{
		Name: text.Name,
		Path: text.Path,
		Size: text.Size,
	}, "open")
	noCache(w)
	writeJSON(w, http.StatusOK, text)
}

// handleWriteText saves editor content back to disk.
func (a *API) handleWriteText(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var body fsTextRequest
	if err := decode(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	p := vfs.Clean(body.Path)
	if p == "" {
		a.fail(w, r, badRequest("Missing path"))
		return
	}
	scope, err := a.scopeFor(r.Context(), user)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	entry, err := a.VFS.WriteText(scope, p, body.Content)
	if err != nil {
		a.audit(r, "file.edit", p, fsMessage(err), false)
		a.fail(w, r, err)
		return
	}
	a.audit(r, "file.edit", entry.Path, fmt.Sprintf("%d bytes", entry.Size), true)
	a.touchRecent(r.Context(), user.ID, entry, "edit")
	a.fsNotify(user.ID, path.Dir(entry.Path), "edit")
	writeJSON(w, http.StatusOK, entry)
}

// ---- advanced ---------------------------------------------------------------

// handleChmod changes permissions on a path, optionally through the tree.
func (a *API) handleChmod(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if !a.Config.Security.AllowAdvanced {
		a.fail(w, r, apiError(http.StatusForbidden, "advanced_disabled", "Advanced file operations are turned off on this server"))
		return
	}
	var body fsChmodRequest
	if err := decode(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	p := vfs.Clean(body.Path)
	if p == "" {
		a.fail(w, r, badRequest("Missing path"))
		return
	}
	mode, err := vfs.ParseMode(body.Mode)
	if err != nil {
		a.fail(w, r, badRequest("That permission value is not valid, use a form such as 0755"))
		return
	}
	scope, err := a.scopeFor(r.Context(), user)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.VFS.Chmod(r.Context(), scope, p, mode, body.Recursive); err != nil {
		a.audit(r, "file.chmod", p, fsMessage(err), false)
		a.fail(w, r, err)
		return
	}
	detail := fmt.Sprintf("%04o", mode.Perm())
	if body.Recursive {
		detail += ", including everything inside"
	}
	a.audit(r, "file.chmod", p, detail, true)
	a.fsNotify(user.ID, path.Dir(p), "chmod")

	entry, err := a.VFS.Stat(scope, p)
	if err != nil {
		writeOK(w)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

// handleChown changes the owner of a path. Only administrators may call it:
// the route guarantees the advanced permission, nothing more.
func (a *API) handleChown(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if !user.IsAdmin() {
		a.audit(r, "file.chown", r.URL.Path, "not an administrator", false)
		a.fail(w, r, errForbidden)
		return
	}
	if !a.Config.Security.AllowAdvanced {
		a.fail(w, r, apiError(http.StatusForbidden, "advanced_disabled", "Advanced file operations are turned off on this server"))
		return
	}
	var body fsChownRequest
	if err := decode(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	p := vfs.Clean(body.Path)
	if p == "" {
		a.fail(w, r, badRequest("Missing path"))
		return
	}
	owner := strings.TrimSpace(body.Owner)
	group := strings.TrimSpace(body.Group)
	if owner == "" && group == "" {
		a.fail(w, r, badRequest("Give a user, a group, or both"))
		return
	}

	// A value of minus one leaves that side of the ownership untouched.
	uid, gid := -1, -1
	if owner != "" {
		resolved, err := vfs.LookupUID(owner)
		if err != nil {
			a.fail(w, r, badRequest("There is no user called "+owner+" on this server"))
			return
		}
		uid = resolved
	}
	if group != "" {
		resolved, err := vfs.LookupGID(group)
		if err != nil {
			a.fail(w, r, badRequest("There is no group called "+group+" on this server"))
			return
		}
		gid = resolved
	}

	scope, err := a.scopeFor(r.Context(), user)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.VFS.Chown(r.Context(), scope, p, uid, gid, body.Recursive); err != nil {
		a.audit(r, "file.chown", p, fsMessage(err), false)
		a.fail(w, r, err)
		return
	}
	detail := strings.TrimSuffix(owner+":"+group, ":")
	if body.Recursive {
		detail += ", including everything inside"
	}
	a.audit(r, "file.chown", p, detail, true)
	a.fsNotify(user.ID, path.Dir(p), "chown")

	entry, err := a.VFS.Stat(scope, p)
	if err != nil {
		writeOK(w)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

// ---- local helpers ----------------------------------------------------------

// fsNotify tells the browser that a folder needs to be reloaded.
func (a *API) fsNotify(userID int64, folder, reason string) {
	folder = vfs.Clean(folder)
	if folder == "" {
		return
	}
	a.publish(userID, events.EventFSChanged, map[string]any{"path": folder, "reason": reason})
}

// fsIsFavorite reports whether a user pinned a path, treating a database
// hiccup as "not pinned" rather than failing the listing over it.
func (a *API) fsIsFavorite(ctx context.Context, user *store.User, p string) bool {
	if user == nil || p == "" {
		return false
	}
	pinned, err := a.Store.IsFavorite(ctx, user.ID, p)
	if err != nil {
		a.Logger.Debug("read pinned state", "path", p, "err", err)
		return false
	}
	return pinned
}

// fsTreeChildren lists the folders below a path, nesting as deep as asked.
func (a *API) fsTreeChildren(scope vfs.Scope, p string, depth int, hidden bool) ([]fsTreeNode, error) {
	listing, err := a.VFS.List(scope, p, vfs.ListOptions{ShowHidden: hidden, FoldersFirst: true})
	if err != nil {
		return nil, err
	}
	out := make([]fsTreeNode, 0, listing.Folders)
	for _, entry := range listing.Entries {
		if !entry.IsDir {
			continue
		}
		if len(out) >= fsTreeMaxNodes {
			break
		}
		node := fsTreeNode{Name: entry.Name, Path: entry.Path}
		if depth > 1 {
			if kids, err := a.fsTreeChildren(scope, entry.Path, depth-1, hidden); err == nil {
				node.Children = kids
				node.HasChildren = len(kids) > 0
				out = append(out, node)
				continue
			}
		}
		node.HasChildren = a.fsHasSubfolder(scope, entry.Path, hidden)
		out = append(out, node)
	}
	return out, nil
}

// fsHasSubfolder reports whether a folder holds at least one folder. It reads
// only the first entries, so a directory with a hundred thousand files still
// answers immediately. A folder it may not read simply reports no children.
func (a *API) fsHasSubfolder(scope vfs.Scope, p string, hidden bool) bool {
	loc, err := a.VFS.Resolve(scope, p)
	if err != nil {
		return false
	}
	dir, err := loc.Root.Open(loc.Rel)
	if err != nil {
		return false
	}
	defer dir.Close()

	for scanned := 0; scanned < fsTreeProbeLimit; {
		batch, err := dir.ReadDir(64)
		for _, de := range batch {
			scanned++
			name := de.Name()
			if !de.IsDir() || vfs.IsInternal(name) {
				continue
			}
			if !hidden && strings.HasPrefix(name, ".") {
				continue
			}
			if a.VFS.Denied(path.Join(p, name)) {
				continue
			}
			return true
		}
		if err != nil || len(batch) == 0 {
			return false
		}
	}
	return false
}

// fsBreadcrumbs builds the trail from the mount root down to the current
// folder. The first step carries the mount label rather than a bare directory
// name, so the user sees where they are.
func fsBreadcrumbs(mount vfs.Mount, current string) []fsCrumb {
	root := vfs.Clean(mount.Path)
	current = vfs.Clean(current)
	crumbs := []fsCrumb{{Name: fsMountLabel(mount), Path: root}}
	if root == "" || current == "" || current == root {
		return crumbs
	}
	rest := strings.Trim(strings.TrimPrefix(current, root), "/")
	if rest == "" {
		return crumbs
	}
	acc := root
	for _, segment := range strings.Split(rest, "/") {
		if segment == "" {
			continue
		}
		acc = path.Join(acc, segment)
		crumbs = append(crumbs, fsCrumb{Name: segment, Path: acc})
	}
	return crumbs
}

// fsMountLabel is the display name of a mount.
func fsMountLabel(m vfs.Mount) string {
	if label := strings.TrimSpace(m.Label); label != "" {
		return label
	}
	p := vfs.Clean(m.Path)
	if p == "/" {
		return "Root volume"
	}
	if base := path.Base(p); base != "" && base != "." {
		return base
	}
	return p
}

// fsDisplayName is the short name of a folder, for titles and messages.
func fsDisplayName(p string) string {
	p = vfs.Clean(p)
	if p == "" {
		return "the destination"
	}
	base := path.Base(p)
	if base == "" || base == "." || base == "/" {
		return p
	}
	return base
}

// fsCountLabel renders an item count in plain English.
func fsCountLabel(n int) string {
	if n == 1 {
		return "1 item"
	}
	return fmt.Sprintf("%d items", n)
}

// fsCleanList normalizes a selection, dropping blanks and duplicates while
// keeping the order the user picked.
func fsCleanList(in []string) []string {
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

// fsParentsOf returns the containing folders of a selection, without repeats.
func fsParentsOf(paths []string) []string {
	out := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		parent := path.Dir(vfs.Clean(p))
		if parent == "" || seen[parent] {
			continue
		}
		seen[parent] = true
		out = append(out, parent)
	}
	if len(out) == 0 {
		out = append(out, "/")
	}
	return out
}

// fsSelectionDetail summarizes a selection for the audit log.
func fsSelectionDetail(paths []string) string {
	names := make([]string, 0, len(paths))
	for _, p := range paths {
		names = append(names, path.Base(p))
	}
	return truncate(fmt.Sprintf("%s: %s", fsCountLabel(len(paths)), strings.Join(names, ", ")), fsAuditDetailMax)
}

// fsParseConflict maps the requested conflict policy onto the file system layer.
func fsParseConflict(raw string) (vfs.Conflict, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "rename":
		return vfs.ConflictRename, nil
	case "overwrite":
		return vfs.ConflictOverwrite, nil
	case "skip":
		return vfs.ConflictSkip, nil
	case "fail":
		return vfs.ConflictFail, nil
	}
	return "", badRequest("Unknown option for what to do with existing files")
}

// fsParseKinds turns a comma separated kind filter into the search option.
func fsParseKinds(raw string) []vfs.Kind {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	kinds := make([]vfs.Kind, 0, len(parts))
	for _, part := range parts {
		if k := strings.ToLower(strings.TrimSpace(part)); k != "" {
			kinds = append(kinds, vfs.Kind(k))
		}
	}
	return kinds
}

// fsMessage renders an error as a short phrase for a per item report, without
// leaking server paths or driver text.
func fsMessage(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, vfs.ErrNotFound), errors.Is(err, store.ErrNotFound):
		return "not found"
	case errors.Is(err, vfs.ErrForbidden):
		return "outside the area you can access"
	case errors.Is(err, vfs.ErrDenied):
		return "protected"
	case errors.Is(err, vfs.ErrReadOnly):
		return "read only location"
	case errors.Is(err, vfs.ErrExists), errors.Is(err, store.ErrConflict):
		return "already exists"
	case errors.Is(err, vfs.ErrInvalidName):
		return "name is not allowed"
	case errors.Is(err, vfs.ErrIsDir):
		return "is a folder"
	case errors.Is(err, vfs.ErrNotDir):
		return "is not a folder"
	case errors.Is(err, vfs.ErrTooLarge):
		return "too large"
	case errors.Is(err, context.DeadlineExceeded):
		return "took too long"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr.Message
	}
	return "could not be completed"
}

// fsOwnerScope picks the owner filter for the bookkeeping tables. An
// administrator acts on content that may belong to anyone, so their operations
// clean up every owner's rows.
func fsOwnerScope(user *store.User) int64 {
	if user == nil {
		return 0
	}
	if user.IsAdmin() {
		return 0
	}
	return user.ID
}
