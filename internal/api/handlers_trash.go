package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"strconv"

	"github.com/XProject25/Storix/internal/events"
	"github.com/XProject25/Storix/internal/store"
	"github.com/XProject25/Storix/internal/vfs"
)

// The recycle bin, plus the two shortcut lists the sidebar is built on:
// pinned locations and recently touched files.

// trashIDsRequest is the body of the restore and permanent delete calls.
type trashIDsRequest struct {
	IDs []int64 `json:"ids"`
}

// trashEmptyRequest is the body of the empty call. Administrators may clear
// the bins of every account at once.
type trashEmptyRequest struct {
	AllUsers bool `json:"allUsers"`
}

// trashFailure explains why one item could not be handled.
type trashFailure struct {
	ID     int64  `json:"id"`
	Reason string `json:"reason"`
}

// trashFavoriteRequest is the body of POST /api/v1/favorites.
type trashFavoriteRequest struct {
	Path string `json:"path"`
}

// handleTrashList returns what the caller has in the recycle bin.
// Administrators may ask for every account with all=1.
func (a *API) handleTrashList(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	ctx := r.Context()

	owner := user.ID
	if queryBool(r, "all") && user.IsAdmin() {
		owner = 0
	}
	items, err := a.Store.ListTrash(ctx, owner, queryInt(r, "limit", 0))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	count, size, err := a.Store.TrashStats(ctx, owner)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	noCache(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"items":         items,
		"count":         count,
		"bytes":         size,
		"retentionDays": a.trashRetentionDays(),
	})
}

// handleTrashRestore puts items back where they came from. A name that is
// taken again gets a numbered suffix rather than overwriting what is there.
func (a *API) handleTrashRestore(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var req trashIDsRequest
	if err := decode(r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	if len(req.IDs) == 0 {
		a.fail(w, r, badRequest("Select at least one item to restore"))
		return
	}
	ctx := r.Context()
	scope, err := a.scopeFor(ctx, user)
	if err != nil {
		a.fail(w, r, err)
		return
	}

	restored := 0
	failed := make([]trashFailure, 0)
	touched := make(map[string]bool)

	for _, id := range req.IDs {
		item, err := a.trashOwned(r, id)
		if err != nil {
			failed = append(failed, trashFailure{ID: id, Reason: trashReason(err)})
			continue
		}
		target, err := a.VFS.RestoreFromTrash(ctx, scope, trashRecordOf(item), vfs.ConflictRename)
		if err != nil {
			failed = append(failed, trashFailure{ID: id, Reason: trashReason(err)})
			continue
		}
		if err := a.Store.DeleteTrashItem(ctx, id); err != nil && !errors.Is(err, store.ErrNotFound) {
			a.Logger.Warn("trash row survived a restore", "item", id, "err", err)
		}
		restored++
		touched[path.Dir(target)] = true
	}

	for dir := range touched {
		a.publish(user.ID, events.EventFSChanged, map[string]any{"path": dir, "reason": "restore"})
	}
	a.audit(r, "trash.restore", strconv.Itoa(restored)+" item(s)", "", len(failed) == 0)
	writeJSON(w, http.StatusOK, map[string]any{"restored": restored, "failed": failed})
}

// handleTrashDelete erases items for good.
func (a *API) handleTrashDelete(w http.ResponseWriter, r *http.Request) {
	var req trashIDsRequest
	if err := decode(r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	if len(req.IDs) == 0 {
		a.fail(w, r, badRequest("Select at least one item to delete"))
		return
	}
	ctx := r.Context()

	deleted := 0
	failed := make([]trashFailure, 0)

	for _, id := range req.IDs {
		item, err := a.trashOwned(r, id)
		if err != nil {
			failed = append(failed, trashFailure{ID: id, Reason: trashReason(err)})
			continue
		}
		if err := a.VFS.PurgeTrash(trashRecordOf(item)); err != nil {
			failed = append(failed, trashFailure{ID: id, Reason: trashReason(err)})
			continue
		}
		if err := a.Store.DeleteTrashItem(ctx, id); err != nil && !errors.Is(err, store.ErrNotFound) {
			a.Logger.Warn("trash row survived a purge", "item", id, "err", err)
		}
		deleted++
	}

	a.audit(r, "trash.delete", strconv.Itoa(deleted)+" item(s)", "", len(failed) == 0)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": deleted, "failed": failed})
}

// handleTrashEmpty clears the bin of the caller, or of everybody when an
// administrator asks for it.
func (a *API) handleTrashEmpty(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var req trashEmptyRequest
	if err := trashDecodeOptional(r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	owner := user.ID
	target := "own bin"
	if req.AllUsers {
		if !user.IsAdmin() {
			a.audit(r, "trash.empty", "all accounts", "", false)
			a.fail(w, r, errForbidden)
			return
		}
		owner = 0
		target = "every bin"
	}

	// The rows are claimed and removed in one transaction, so two clicks on
	// the button cannot erase the same item twice.
	items, err := a.Store.ClaimTrash(r.Context(), owner)
	if err != nil {
		a.fail(w, r, err)
		return
	}

	emptied := 0
	failures := 0
	var freed int64
	for _, item := range items {
		if err := a.VFS.PurgeTrash(trashRecordOf(item)); err != nil {
			failures++
			a.Logger.Warn("purge trash failed", "item", item.ID, "path", item.StoredPath, "err", err)
			continue
		}
		emptied++
		freed += item.Size
	}

	a.audit(r, "trash.empty", target, strconv.Itoa(emptied)+" item(s)", failures == 0)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"emptied": emptied,
		"failed":  failures,
		"bytes":   freed,
	})
}

// handleFavorites returns the pinned locations of the caller.
func (a *API) handleFavorites(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	list, err := a.Store.ListFavorites(r.Context(), user.ID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	noCache(w)
	writeJSON(w, http.StatusOK, map[string]any{"favorites": list, "count": len(list)})
}

// handleAddFavorite pins a location. The path has to resolve inside the area
// the caller can reach before it is stored.
func (a *API) handleAddFavorite(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var req trashFavoriteRequest
	if err := decode(r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	target := vfs.Clean(req.Path)
	if target == "" {
		a.fail(w, r, badRequest("Missing path parameter"))
		return
	}
	ctx := r.Context()
	scope, err := a.scopeFor(ctx, user)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	entry, err := a.VFS.Stat(scope, target)
	if err != nil {
		a.fail(w, r, err)
		return
	}

	favorite := &store.Favorite{
		UserID: user.ID,
		Path:   entry.Path,
		Name:   entry.Name,
		IsDir:  entry.IsDir,
	}
	if _, err := a.Store.AddFavorite(ctx, favorite); err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, favorite)
}

// handleRemoveFavorite unpins a location. Unpinning something that was not
// pinned still reports success, since the end state is what the caller asked
// for.
func (a *API) handleRemoveFavorite(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	target, err := pathParam(r, "path")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.Store.RemoveFavorite(r.Context(), user.ID, target); err != nil &&
		!errors.Is(err, store.ErrNotFound) {
		a.fail(w, r, err)
		return
	}
	writeOK(w)
}

// handleRecent returns the files the caller worked with lately. Entries whose
// path no longer resolves are left out, and the ones that are gone for good
// are dropped from the history as they are found.
func (a *API) handleRecent(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	limit := queryInt(r, "limit", 50)
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	ctx := r.Context()
	scope, err := a.scopeFor(ctx, user)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	// A margin covers the entries that are filtered out below, so a full page
	// is still returned when a few files have moved on.
	rows, err := a.Store.ListRecents(ctx, user.ID, limit+20)
	if err != nil {
		a.fail(w, r, err)
		return
	}

	out := make([]*store.Recent, 0, limit)
	for _, rec := range rows {
		if len(out) >= limit {
			break
		}
		entry, err := a.VFS.Stat(scope, rec.Path)
		if err != nil {
			if errors.Is(err, vfs.ErrNotFound) {
				// The file is gone, so the history entry has no use left.
				if err := a.Store.RemoveRecent(ctx, user.ID, rec.Path); err != nil &&
					!errors.Is(err, store.ErrNotFound) {
					a.Logger.Debug("recent cleanup failed", "path", rec.Path, "err", err)
				}
			}
			continue
		}
		rec.Name = entry.Name
		rec.IsDir = entry.IsDir
		if !entry.IsDir {
			rec.Size = entry.Size
		}
		out = append(out, rec)
	}

	noCache(w)
	writeJSON(w, http.StatusOK, map[string]any{"recent": out, "count": len(out)})
}

// ---- helpers ----------------------------------------------------------------

// trashRetentionDays reports how long a deleted item is kept, in whole days.
func (a *API) trashRetentionDays() int {
	hours := a.Config.Limits.TrashRetention.D().Hours()
	if hours <= 0 {
		return 0
	}
	days := int(hours / 24)
	if days < 1 {
		days = 1
	}
	return days
}

// trashRecordOf converts a stored bin row into the record the guarded file
// system layer works with.
func trashRecordOf(item *store.TrashItem) vfs.TrashRecord {
	return vfs.TrashRecord{
		Name:         item.Name,
		OriginalPath: item.OriginalPath,
		StoredPath:   item.StoredPath,
		IsDir:        item.IsDir,
		Size:         item.Size,
	}
}

// trashOwned loads one bin row and checks it belongs to the caller. An item
// owned by somebody else reads as missing, so the identifiers of other
// accounts cannot be probed.
func (a *API) trashOwned(r *http.Request, id int64) (*store.TrashItem, error) {
	user := currentUser(r)
	item, err := a.Store.GetTrashItem(r.Context(), id)
	if err != nil {
		return nil, err
	}
	if item.UserID != user.ID && !user.IsAdmin() {
		return nil, store.ErrNotFound
	}
	return item, nil
}

// trashReason renders a per item failure in plain words.
func trashReason(err error) string {
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, vfs.ErrNotFound):
		return "That item is no longer in the recycle bin"
	case errors.Is(err, vfs.ErrExists):
		return "Something with that name is already back in place"
	case errors.Is(err, vfs.ErrReadOnly):
		return "The folder it came from is read only"
	case errors.Is(err, vfs.ErrForbidden), errors.Is(err, vfs.ErrDenied):
		return "The folder it came from is outside the area you can reach"
	case errors.Is(err, vfs.ErrInvalidName):
		return "The stored name cannot be used here"
	}
	return "That item could not be handled"
}

// trashDecodeOptional reads a JSON body that may be absent. The plain empty
// the bin button sends no body at all.
func trashDecodeOptional(r *http.Request, dst any) error {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	raw, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err != nil {
		return badRequest("Malformed request body")
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return badRequest("Malformed request body")
	}
	return nil
}
