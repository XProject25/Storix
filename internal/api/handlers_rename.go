package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"unicode"

	"github.com/XProject25/Storix/internal/store"
	"github.com/XProject25/Storix/internal/vfs"
)

// rnMaxBatch caps how many items one bulk rename may carry. Renaming is a
// metadata operation, so a batch this size still finishes inside the request,
// but the ceiling keeps a single call from walking a whole volume.
const rnMaxBatch = 5000

// rnBulkRequest is the body both bulk rename endpoints take.
type rnBulkRequest struct {
	Paths []string       `json:"paths"`
	Rule  vfs.RenameRule `json:"rule"`
}

// rnFailure is one item a bulk rename could not rename, and why.
type rnFailure struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// handleRenameBulkPreview works out what a rename rule would do to a selection,
// without touching anything on disk.
func (a *API) handleRenameBulkPreview(w http.ResponseWriter, r *http.Request) {
	body, _, err := a.rnRead(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	scope, err := a.scopeFor(r.Context(), currentUser(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	preview, err := a.VFS.PreviewRename(scope, body.Paths, body.Rule)
	if err != nil {
		a.fail(w, r, rnRuleError(err))
		return
	}
	noCache(w)
	writeJSON(w, http.StatusOK, preview)
}

// handleRenameBulk applies a rename rule to a selection. Items that would land
// on a name already in use are reported back rather than forced.
func (a *API) handleRenameBulk(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	body, folder, err := a.rnRead(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	scope, err := a.scopeFor(r.Context(), user)
	if err != nil {
		a.fail(w, r, err)
		return
	}

	// The dry run is taken first, so the pinned locations that have to follow
	// the rename can be looked up by their old and their new path once the
	// batch has run.
	preview, err := a.VFS.PreviewRename(scope, body.Paths, body.Rule)
	if err != nil {
		a.audit(r, "file.rename.bulk", folder, fsMessage(err), false)
		a.fail(w, r, rnRuleError(err))
		return
	}

	renamed, failures, err := a.VFS.ApplyRename(r.Context(), scope, body.Paths, body.Rule)
	if err != nil {
		a.audit(r, "file.rename.bulk", folder, fsMessage(err), false)
		a.fail(w, r, rnRuleError(err))
		return
	}

	failed := make([]rnFailure, 0, len(failures))
	blocked := make(map[string]bool, len(failures))
	for _, change := range failures {
		blocked[change.Path] = true
		reason := change.Reason
		if reason == "" {
			reason = "could not be renamed"
		}
		failed = append(failed, rnFailure{Path: change.Path, Reason: reason})
	}

	a.rnFollowFavorites(r.Context(), user, preview.Changes, blocked)
	a.audit(r, "file.rename.bulk", folder, rnAuditDetail(renamed, len(failed)), len(failed) == 0)
	if renamed > 0 {
		a.fsNotify(user.ID, folder, "rename")
	}
	writeJSON(w, http.StatusOK, map[string]any{"renamed": renamed, "failed": failed})
}

// rnRead validates the request body and reports the one folder the whole
// selection sits in. A bulk rename works inside a single folder, so the preview
// the user approved is the batch the server carries out.
func (a *API) rnRead(r *http.Request) (*rnBulkRequest, string, error) {
	var body rnBulkRequest
	if err := decode(r, &body); err != nil {
		return nil, "", err
	}
	body.Paths = fsCleanList(body.Paths)
	if len(body.Paths) == 0 {
		return nil, "", badRequest("Select at least one item to rename")
	}
	if len(body.Paths) > rnMaxBatch {
		return nil, "", badRequest(fmt.Sprintf("A rename can cover up to %d items at once", rnMaxBatch))
	}
	folder := path.Dir(body.Paths[0])
	for _, p := range body.Paths[1:] {
		if path.Dir(p) != folder {
			return nil, "", badRequest("Select items from a single folder")
		}
	}
	return &body, folder, nil
}

// rnFollowFavorites moves pinned locations along with the items that really
// were renamed, so a pin never points at a name that no longer exists.
func (a *API) rnFollowFavorites(ctx context.Context, user *store.User, changes []vfs.RenameChange, blocked map[string]bool) {
	owner := fsOwnerScope(user)
	for _, change := range changes {
		if change.Conflict || change.Unchanged || blocked[change.Path] {
			continue
		}
		target := path.Join(path.Dir(change.Path), change.To)
		if err := a.Store.RenameFavorites(ctx, owner, change.Path, target); err != nil {
			a.Logger.Warn("move pinned locations after bulk rename", "path", change.Path, "err", err)
		}
	}
}

// rnAuditDetail summarizes the outcome of a batch for the audit log.
func rnAuditDetail(renamed, failed int) string {
	detail := fmt.Sprintf("%s renamed", fsCountLabel(renamed))
	if failed > 0 {
		detail += fmt.Sprintf(", %s skipped", fsCountLabel(failed))
	}
	return detail
}

// rnRuleError turns a rejected rule into a bad request the interface can show
// as it stands, rather than an unexplained server error.
func rnRuleError(err error) error {
	if !errors.Is(err, vfs.ErrInvalidRule) {
		return err
	}
	detail := strings.TrimPrefix(err.Error(), vfs.ErrInvalidRule.Error()+": ")
	return badRequest(rnSentence(detail))
}

// rnSentence upper cases the first letter, so a detail reads as a sentence.
func rnSentence(s string) string {
	if s == "" {
		return "That rename rule cannot be used"
	}
	runes := []rune(s)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}
