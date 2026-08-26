package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/XProject25/Storix/internal/archive"
	"github.com/XProject25/Storix/internal/auth"
	"github.com/XProject25/Storix/internal/store"
	"github.com/XProject25/Storix/internal/thumbs"
	"github.com/XProject25/Storix/internal/upload"
	"github.com/XProject25/Storix/internal/vfs"
)

// Public links have two very different audiences. The owner side runs behind
// the session middleware and speaks the normal JSON envelope. The public side
// runs for anonymous visitors, so it never reveals a server path, never tells
// a missing link apart from an expired one, and reads every permission from
// the stored link rather than from the request.

const (
	// shCookiePrefix scopes the unlock cookie to a single link.
	shCookiePrefix = "storix_share_"
	// shUnlockTTL is how long an unlocked link stays unlocked in a browser.
	shUnlockTTL = 12 * time.Hour
	// shContentPolicy neutralizes anything served from a public link, so a
	// hosted page can never run in the Storix origin.
	shContentPolicy = "default-src 'none'; sandbox"
	// shMaxNote caps the free text an owner can attach to a link.
	shMaxNote = 500
	// shTokenMaxLen is a sanity bound on the token taken from the URL.
	shTokenMaxLen = 64
)

// Errors the public side reports. A missing link and an expired link answer
// with exactly the same body on purpose.
var (
	shErrGone   = apiError(http.StatusNotFound, "gone", "This link is no longer available")
	shErrLocked = apiError(http.StatusUnauthorized, "password_required", "This link is protected by a password")
)

// ---- payloads ---------------------------------------------------------------

// shView is a link as its owner sees it, with the address to hand out.
type shView struct {
	*store.Share
	URL string `json:"url"`
}

// shCreateRequest is the body of POST /api/v1/shares.
type shCreateRequest struct {
	Path          string  `json:"path"`
	Name          string  `json:"name"`
	Kind          string  `json:"kind"`
	Password      string  `json:"password"`
	ExpiresIn     string  `json:"expiresIn"`
	MaxDownloads  int     `json:"maxDownloads"`
	AllowDownload *bool   `json:"allowDownload"`
	AllowUpload   *bool   `json:"allowUpload"`
	AllowList     *bool   `json:"allowList"`
	Note          *string `json:"note"`
}

// shUpdateRequest is the body of PATCH /api/v1/shares/{id}. Path is accepted
// and ignored: a link can never be pointed at different content.
type shUpdateRequest struct {
	Path          string  `json:"path"`
	Name          *string `json:"name"`
	Kind          *string `json:"kind"`
	Password      string  `json:"password"`
	ClearPassword bool    `json:"clearPassword"`
	ExpiresIn     *string `json:"expiresIn"`
	MaxDownloads  *int    `json:"maxDownloads"`
	AllowDownload *bool   `json:"allowDownload"`
	AllowUpload   *bool   `json:"allowUpload"`
	AllowList     *bool   `json:"allowList"`
	Note          *string `json:"note"`
}

// shCrumb is one step of the public breadcrumb trail.
type shCrumb struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// shEntry is a listing row as a visitor sees it: the path is relative to the
// share root and the ownership and mode fields are dropped.
type shEntry struct {
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	IsDir       bool      `json:"isDir"`
	Size        int64     `json:"size"`
	Modified    time.Time `json:"modified"`
	Kind        vfs.Kind  `json:"kind"`
	MIME        string    `json:"mime"`
	Ext         string    `json:"ext"`
	Previewable bool      `json:"previewable"`
	Thumbnail   bool      `json:"thumbnail"`
}

// shMeta is the public description of a link.
type shMeta struct {
	Name          string          `json:"name"`
	Kind          store.ShareKind `json:"kind"`
	IsDir         bool            `json:"isDir"`
	HasPassword   bool            `json:"hasPassword"`
	AllowDownload bool            `json:"allowDownload"`
	AllowUpload   bool            `json:"allowUpload"`
	AllowList     bool            `json:"allowList"`
	Note          string          `json:"note"`
	ExpiresAt     *time.Time      `json:"expiresAt,omitempty"`
	Owner         string          `json:"owner"`
	Path          string          `json:"path"`
	Entries       []shEntry       `json:"entries"`
	Breadcrumbs   []shCrumb       `json:"breadcrumbs"`
}

// ---- owner side --------------------------------------------------------------

// handleListShares returns the links the caller owns. An administrator may
// pass all=1 to review every link on the server.
func (a *API) handleListShares(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	owner := user.ID
	all := queryBool(r, "all") && user.IsAdmin()
	if all {
		owner = 0
	}
	list, err := a.Store.ListShares(r.Context(), owner)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	base := a.baseURL(r)
	rows := make([]shView, 0, len(list))
	for _, sh := range list {
		rows = append(rows, shView{Share: sh, URL: shLinkURL(base, sh.Token)})
	}
	noCache(w)
	writeJSON(w, http.StatusOK, map[string]any{"shares": rows, "total": len(rows), "all": all})
}

// handleCreateShare publishes a file or folder behind a public address.
func (a *API) handleCreateShare(w http.ResponseWriter, r *http.Request) {
	var body shCreateRequest
	if err := decode(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	user := currentUser(r)
	ctx := r.Context()

	target := vfs.Clean(body.Path)
	if target == "" {
		a.fail(w, r, badRequest("Choose a file or folder to share"))
		return
	}
	kind, err := shParseKind(body.Kind)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	expires, err := shParseExpiry(body.ExpiresIn, time.Now())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if body.MaxDownloads < 0 {
		a.fail(w, r, badRequest("The download limit cannot be negative"))
		return
	}

	scope, err := a.scopeFor(ctx, user)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	entry, err := a.VFS.Stat(scope, target)
	if err != nil {
		a.audit(r, "share.create", target, "path not available", false)
		a.fail(w, r, err)
		return
	}

	sh := &store.Share{
		OwnerID:       user.ID,
		Path:          entry.Path,
		Name:          shDisplayName(entry.Path, entry.Name),
		Kind:          kind,
		IsDir:         entry.IsDir,
		MaxDownloads:  body.MaxDownloads,
		AllowDownload: kind == store.ShareDownload,
		AllowList:     entry.IsDir && kind == store.ShareDownload,
		AllowUpload:   kind == store.ShareUpload,
		ExpiresAt:     expires,
		CreatedAt:     time.Now().UTC(),
	}
	if name := strings.TrimSpace(body.Name); name != "" {
		sh.Name = truncate(name, 200)
	}
	if body.Note != nil {
		sh.Note = truncate(strings.TrimSpace(*body.Note), shMaxNote)
	}
	shApplyFlags(sh, body.AllowDownload, body.AllowUpload, body.AllowList)

	if err := a.shCheckCapabilities(r, scope, sh, entry.IsDir); err != nil {
		a.audit(r, "share.create", target, "not permitted", false)
		a.fail(w, r, err)
		return
	}
	if strings.TrimSpace(body.Password) != "" {
		hashed, err := auth.HashPassword(body.Password)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		sh.PasswordHash = hashed
	}

	// A token collision is astronomically unlikely, but retrying is cheaper
	// than handing the caller an error it cannot act on.
	var created bool
	for attempt := 0; attempt < 5; attempt++ {
		sh.Token = auth.ShareToken()
		_, err = a.Store.CreateShare(ctx, sh)
		if err == nil {
			created = true
			break
		}
		if !errors.Is(err, store.ErrConflict) {
			break
		}
	}
	if !created {
		a.audit(r, "share.create", target, "could not be stored", false)
		a.fail(w, r, err)
		return
	}

	a.audit(r, "share.create", sh.Path, string(sh.Kind)+" "+shExpiryLabel(sh.ExpiresAt), true)
	a.publish(user.ID, "share.created", map[string]any{"id": sh.ID, "path": sh.Path, "token": sh.Token})
	writeJSON(w, http.StatusCreated, shView{Share: sh, URL: shLinkURL(a.baseURL(r), sh.Token)})
}

// handleUpdateShare edits an existing link. The content it points at is fixed
// for the life of the link.
func (a *API) handleUpdateShare(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var body shUpdateRequest
	if err := decode(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	ctx := r.Context()
	user := currentUser(r)

	sh, err := a.Store.GetShare(ctx, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if sh.OwnerID != user.ID && !user.IsAdmin() {
		a.audit(r, "share.update", strconv.FormatInt(id, 10), "not the owner", false)
		a.fail(w, r, errForbidden)
		return
	}

	if body.Kind != nil {
		kind, err := shParseKind(*body.Kind)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		sh.Kind = kind
	}
	if body.Name != nil {
		if name := strings.TrimSpace(*body.Name); name != "" {
			sh.Name = truncate(name, 200)
		}
	}
	if body.Note != nil {
		sh.Note = truncate(strings.TrimSpace(*body.Note), shMaxNote)
	}
	if body.MaxDownloads != nil {
		if *body.MaxDownloads < 0 {
			a.fail(w, r, badRequest("The download limit cannot be negative"))
			return
		}
		sh.MaxDownloads = *body.MaxDownloads
	}
	if body.ExpiresIn != nil {
		expires, err := shParseExpiry(*body.ExpiresIn, time.Now())
		if err != nil {
			a.fail(w, r, err)
			return
		}
		sh.ExpiresAt = expires
	}
	shApplyFlags(sh, body.AllowDownload, body.AllowUpload, body.AllowList)

	switch {
	case body.ClearPassword:
		sh.PasswordHash = ""
	case strings.TrimSpace(body.Password) != "":
		hashed, err := auth.HashPassword(body.Password)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		sh.PasswordHash = hashed
	}

	scope, err := a.scopeFor(ctx, user)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.shCheckCapabilities(r, scope, sh, sh.IsDir); err != nil {
		a.audit(r, "share.update", sh.Path, "not permitted", false)
		a.fail(w, r, err)
		return
	}
	if err := a.Store.UpdateShare(ctx, sh); err != nil {
		a.fail(w, r, err)
		return
	}

	a.audit(r, "share.update", sh.Path, string(sh.Kind)+" "+shExpiryLabel(sh.ExpiresAt), true)
	a.publish(sh.OwnerID, "share.updated", map[string]any{"id": sh.ID, "path": sh.Path, "token": sh.Token})
	writeJSON(w, http.StatusOK, shView{Share: sh, URL: shLinkURL(a.baseURL(r), sh.Token)})
}

// handleDeleteShare revokes a link.
func (a *API) handleDeleteShare(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ctx := r.Context()
	user := currentUser(r)

	sh, err := a.Store.GetShare(ctx, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if sh.OwnerID != user.ID && !user.IsAdmin() {
		a.audit(r, "share.revoke", strconv.FormatInt(id, 10), "not the owner", false)
		a.fail(w, r, errForbidden)
		return
	}
	if err := a.Store.DeleteShare(ctx, sh.ID); err != nil {
		a.fail(w, r, err)
		return
	}
	a.audit(r, "share.revoke", sh.Path, string(sh.Kind), true)
	a.publish(sh.OwnerID, "share.revoked", map[string]any{"id": sh.ID, "token": sh.Token})
	writeOK(w)
}

// shCheckCapabilities re-checks the permissions that depend on what the body
// asked for. The router only proved the caller may share at all.
func (a *API) shCheckCapabilities(r *http.Request, scope vfs.Scope, sh *store.Share, isDir bool) error {
	user := currentUser(r)
	if sh.AllowDownload && !user.Can(store.PermDownload) {
		return apiError(http.StatusForbidden, "forbidden", "You cannot publish a link that allows downloads")
	}
	if sh.Kind == store.ShareUpload || sh.AllowUpload {
		if !isDir {
			return badRequest("An upload request has to point at a folder")
		}
		if !user.Can(store.PermUpload) {
			return apiError(http.StatusForbidden, "forbidden", "You cannot publish a link that allows uploads")
		}
		if _, err := a.VFS.ResolveWritable(scope, sh.Path); err != nil {
			return err
		}
	}
	if sh.AllowList && !isDir {
		sh.AllowList = false
	}
	return nil
}

// ---- public side -------------------------------------------------------------

// handlePublicMeta describes a link to a visitor and lists a folder share.
func (a *API) handlePublicMeta(w http.ResponseWriter, r *http.Request) {
	sh, err := shLoad(a, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	noCache(w)

	if !shUnlocked(a, r, sh) {
		// Everything else about the link stays hidden until it is unlocked.
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error":       shErrLocked,
			"name":        sh.Name,
			"kind":        sh.Kind,
			"hasPassword": true,
		})
		return
	}

	ctx := r.Context()
	owner, err := shOwner(a, ctx, sh)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	scope, err := a.scopeFor(ctx, owner)
	if err != nil {
		a.fail(w, r, shErrGone)
		return
	}
	target, err := shJoin(sh, r.URL.Query().Get("path"))
	if err != nil {
		a.fail(w, r, err)
		return
	}

	meta := shMeta{
		Name:          sh.Name,
		Kind:          sh.Kind,
		IsDir:         sh.IsDir,
		HasPassword:   sh.HasPassword,
		AllowDownload: sh.AllowDownload,
		AllowUpload:   sh.AllowUpload,
		AllowList:     sh.AllowList,
		Note:          sh.Note,
		ExpiresAt:     sh.ExpiresAt,
		Owner:         shOwnerName(sh, owner),
		Path:          shRelative(sh, target),
		Entries:       []shEntry{},
		Breadcrumbs:   shCrumbs(sh, target),
	}

	switch {
	case !sh.IsDir:
		entry, err := a.VFS.Stat(scope, target)
		if err != nil {
			a.fail(w, r, shErrGone)
			return
		}
		meta.Entries = append(meta.Entries, shHide(sh, entry))

	case sh.AllowList:
		listing, err := a.VFS.List(scope, target, vfs.ListOptions{
			FoldersFirst: true,
			Sort:         strings.TrimSpace(r.URL.Query().Get("sort")),
			Order:        strings.TrimSpace(r.URL.Query().Get("order")),
			Limit:        a.Config.Limits.ListPageSize,
		})
		if err != nil {
			a.fail(w, r, shErrGone)
			return
		}
		for _, entry := range listing.Entries {
			meta.Entries = append(meta.Entries, shHide(sh, entry))
		}
	}

	shTouch(a, sh, false)
	writeJSON(w, http.StatusOK, meta)
}

// handlePublicAuth unlocks a password protected link for this browser.
func (a *API) handlePublicAuth(w http.ResponseWriter, r *http.Request) {
	sh, err := shLoad(a, r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := decode(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	noCache(w)

	if sh.PasswordHash == "" {
		// Nothing to unlock; the link is already open.
		writeOK(w)
		return
	}

	key := "share:" + a.clientIP(r)
	if !a.loginLimiter.Allow(key) {
		if wait := a.loginLimiter.RetryAfter(key); wait > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		}
		a.audit(r, "share.unlock", sh.Path, "rate limited", false)
		a.fail(w, r, errRateLimited)
		return
	}

	ok, err := auth.VerifyPassword(sh.PasswordHash, body.Password)
	if err != nil {
		a.Logger.Warn("share password hash unreadable", "share", sh.ID, "err", err)
		a.fail(w, r, shErrGone)
		return
	}
	if !ok {
		a.audit(r, "share.unlock", sh.Path, "wrong password", false)
		a.fail(w, r, apiError(http.StatusUnauthorized, "password_invalid", "That password does not match"))
		return
	}

	a.loginLimiter.Reset(key)
	expires := time.Now().Add(shUnlockTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     shCookieName(sh.Token),
		Value:    shCookieValue(sh, expires),
		Path:     "/",
		HttpOnly: true,
		Secure:   a.Config.Security.CookieSecure || r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
		MaxAge:   int(shUnlockTTL.Seconds()),
	})
	a.audit(r, "share.unlock", sh.Path, "", true)
	shTouch(a, sh, false)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "expiresAt": expires.UTC()})
}

// handlePublicDownload streams one file from a link as an attachment.
func (a *API) handlePublicDownload(w http.ResponseWriter, r *http.Request) {
	a.shServeFile(w, r, true)
}

// handlePublicRaw streams one file from a link for inline preview.
func (a *API) handlePublicRaw(w http.ResponseWriter, r *http.Request) {
	a.shServeFile(w, r, false)
}

// shServeFile is the body of both public file endpoints.
func (a *API) shServeFile(w http.ResponseWriter, r *http.Request, attachment bool) {
	sh, scope, ok := a.shGate(w, r, true)
	if !ok {
		return
	}
	target, err := shJoin(sh, r.URL.Query().Get("path"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	file, info, err := a.VFS.Open(scope, target)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defer file.Close()

	name := path.Base(target)
	// A player that seeks through a video sends a fresh range request for
	// every jump. Those are continuations of one transfer, so only a request
	// that starts a transfer moves the download counter.
	shTouch(a, sh, shStartsTransfer(r))

	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", shContentPolicy)
	h.Set("Cache-Control", "private, max-age=0, must-revalidate")
	h.Set("Accept-Ranges", "bytes")
	if attachment {
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Disposition", shDisposition("attachment", name))
	} else {
		h.Set("Content-Type", vfs.MIMEFor(name, vfs.KindFor(name, false)))
		h.Set("Content-Disposition", shDisposition("inline", name))
	}
	http.ServeContent(w, r, name, info.ModTime(), file)
}

// handlePublicZip streams a folder from a link as a zip.
func (a *API) handlePublicZip(w http.ResponseWriter, r *http.Request) {
	sh, scope, ok := a.shGate(w, r, true)
	if !ok {
		return
	}
	target, err := shJoin(sh, r.URL.Query().Get("path"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	loc, err := a.VFS.Resolve(scope, target)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	info, err := loc.Root.Stat(loc.Rel)
	if err != nil {
		a.fail(w, r, shErrGone)
		return
	}
	if !info.IsDir() {
		a.fail(w, r, badRequest("Use the download address for a single file"))
		return
	}

	// A visitor may pick individual children, but only when the link lets
	// them see the folder in the first place.
	items := r.URL.Query()["item"]
	if len(items) > 0 && !sh.AllowList {
		a.fail(w, r, errForbidden)
		return
	}
	sources := []string{loc.Rel}
	name := sh.Name
	if len(items) > 0 {
		sources = sources[:0]
		for _, item := range items {
			if err := vfs.ValidName(item); err != nil {
				a.fail(w, r, badRequest("That selection is not valid"))
				return
			}
			rel := shJoinRel(loc.Rel, item)
			if _, err := loc.Root.Lstat(rel); err != nil {
				a.fail(w, r, shErrGone)
				return
			}
			sources = append(sources, rel)
		}
		if len(items) == 1 {
			name = items[0]
		}
	} else if target != vfs.Clean(sh.Path) {
		name = path.Base(target)
	}

	shTouch(a, sh, true)

	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", shContentPolicy)
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", shDisposition("attachment", shZipName(name)))
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	if err := archive.StreamZip(r.Context(), loc.Root, sources, w, nil); err != nil {
		// The response is already on the wire, so all that is left is a log
		// line and a truncated archive the client will notice.
		a.Logger.Warn("public zip failed", "share", sh.ID, "err", err)
	}
}

// handlePublicThumb serves a cached thumbnail for an image behind a link.
func (a *API) handlePublicThumb(w http.ResponseWriter, r *http.Request) {
	sh, scope, ok := a.shGate(w, r, false)
	if !ok {
		return
	}
	if a.Thumbs == nil {
		a.fail(w, r, errNotFound)
		return
	}
	// A preview is a downgraded view of the file, so it needs the link to
	// allow either downloading or browsing. A link that allows neither hands
	// out nothing at all.
	if !sh.AllowDownload && !sh.AllowList {
		a.fail(w, r, errForbidden)
		return
	}
	target, err := shJoin(sh, r.URL.Query().Get("path"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	loc, err := a.VFS.Resolve(scope, target)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	res, err := a.Thumbs.Thumbnail(r.Context(), loc.Root, loc.Rel, queryInt(r, "size", 256), nil)
	if err != nil {
		if errors.Is(err, thumbs.ErrUnsupported) || errors.Is(err, thumbs.ErrTooLarge) {
			a.fail(w, r, apiError(http.StatusNotFound, "no_preview", "There is no preview for this file"))
			return
		}
		a.fail(w, r, err)
		return
	}
	file, err := os.Open(res.Path)
	if err != nil {
		a.fail(w, r, errNotFound)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		a.fail(w, r, errNotFound)
		return
	}

	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", shContentPolicy)
	h.Set("Content-Type", res.ContentType)
	h.Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, path.Base(res.Path), info.ModTime(), file)
}

// ---- public uploads ----------------------------------------------------------

// handlePublicTusCreate starts a resumable upload into an upload request.
func (a *API) handlePublicTusCreate(w http.ResponseWriter, r *http.Request) {
	sh, actor, ok := a.shUploadActor(w, r)
	if !ok {
		return
	}
	a.Uploads.Create(w, r, actor)
	// creation-with-upload can deliver the whole file in this one request, so
	// the address the manager just handed out names the upload to check.
	if location := strings.TrimSpace(w.Header().Get("Location")); location != "" {
		a.shAnnounceUpload(sh, path.Base(strings.TrimSuffix(location, "/")))
	}
}

// handlePublicTusHead reports the offset a visitor should resume from.
func (a *API) handlePublicTusHead(w http.ResponseWriter, r *http.Request) {
	_, actor, ok := a.shUploadActor(w, r)
	if !ok {
		return
	}
	a.Uploads.Head(w, r, actor)
}

// handlePublicTusPatch appends a chunk to an upload in flight.
func (a *API) handlePublicTusPatch(w http.ResponseWriter, r *http.Request) {
	sh, actor, ok := a.shUploadActor(w, r)
	if !ok {
		return
	}
	a.Uploads.Patch(w, r, actor)
	a.shAnnounceUpload(sh, r.PathValue("id"))
}

// shUploadActor validates an upload request link and builds the actor the tus
// manager writes as. The actor carries the owner scope, so the guarded layer
// refuses anything outside the owner mounts, and ForcedDir pins every file to
// the shared folder.
func (a *API) shUploadActor(w http.ResponseWriter, r *http.Request) (*store.Share, upload.Actor, bool) {
	sh, err := shLoad(a, r)
	if err != nil {
		a.fail(w, r, err)
		return nil, upload.Actor{}, false
	}
	if !shUnlocked(a, r, sh) {
		a.fail(w, r, shErrLocked)
		return nil, upload.Actor{}, false
	}
	if sh.Kind != store.ShareUpload || !sh.AllowUpload || !sh.IsDir {
		a.fail(w, r, apiError(http.StatusForbidden, "forbidden", "This link does not accept uploads"))
		return nil, upload.Actor{}, false
	}
	if a.Uploads == nil {
		a.fail(w, r, errNotFound)
		return nil, upload.Actor{}, false
	}
	ctx := r.Context()
	owner, err := shOwner(a, ctx, sh)
	if err != nil {
		a.fail(w, r, err)
		return nil, upload.Actor{}, false
	}
	if !owner.Can(store.PermUpload) {
		a.fail(w, r, apiError(http.StatusForbidden, "forbidden", "This link does not accept uploads"))
		return nil, upload.Actor{}, false
	}
	scope, err := a.scopeFor(ctx, owner)
	if err != nil {
		a.fail(w, r, shErrGone)
		return nil, upload.Actor{}, false
	}
	return sh, upload.Actor{
		UserID:     sh.OwnerID,
		Username:   owner.Username,
		Scope:      scope,
		ShareToken: sh.Token,
		Base:       "/api/v1/public/" + sh.Token + "/tus",
		ForcedDir:  vfs.Clean(sh.Path),
		MaxSize:    a.Config.Limits.MaxUploadSize,
	}, true
}

// shAnnounceUpload tells the owner that a file landed in their upload request,
// so the transfer panel shows it while the visitor is still on the page.
func (a *API) shAnnounceUpload(sh *store.Share, id string) {
	if sh == nil || strings.TrimSpace(id) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	sess, err := a.Store.GetUpload(ctx, id)
	if err != nil || sess == nil || !sess.Completed || sess.ShareToken != sh.Token {
		return
	}
	a.publish(sh.OwnerID, "share.upload", map[string]any{
		"token": sh.Token,
		"share": sh.Name,
		"name":  sess.Filename,
		"path":  sess.FinalPath,
		"dir":   sess.TargetDir,
		"size":  sess.Size,
	})
	shTouch(a, sh, false)
}

// ---- helpers -----------------------------------------------------------------

// shLoad resolves the {token} path value into a live link. A missing link, an
// expired one and one that has spent its download budget all answer the same.
func shLoad(a *API, r *http.Request) (*store.Share, error) {
	if sh := currentShare(r); sh != nil {
		return sh, nil
	}
	token := strings.TrimSpace(r.PathValue("token"))
	if token == "" || len(token) > shTokenMaxLen {
		return nil, shErrGone
	}
	sh, err := a.Store.GetShareByToken(r.Context(), token)
	if err != nil || sh == nil {
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			a.Logger.Warn("share lookup failed", "err", err)
		}
		return nil, shErrGone
	}
	if sh.Expired(time.Now()) {
		return nil, shErrGone
	}
	return sh, nil
}

// shOwner loads the account behind a link. A deleted or disabled owner takes
// their links down with them.
func shOwner(a *API, ctx context.Context, sh *store.Share) (*store.User, error) {
	owner, err := a.Store.GetUser(ctx, sh.OwnerID)
	if err != nil || owner == nil || !owner.Active {
		return nil, shErrGone
	}
	return owner, nil
}

// shScope builds the guarded scope of the link owner. Every public read and
// write runs inside it, so a visitor can never reach past the owner mounts.
func shScope(a *API, ctx context.Context, sh *store.Share) (vfs.Scope, error) {
	owner, err := shOwner(a, ctx, sh)
	if err != nil {
		return vfs.Scope{}, err
	}
	scope, err := a.scopeFor(ctx, owner)
	if err != nil {
		return vfs.Scope{}, shErrGone
	}
	return scope, nil
}

// shGate is the common opening of every public streaming endpoint: load the
// link, require it to be unlocked, optionally require downloads, and hand back
// the owner scope.
func (a *API) shGate(w http.ResponseWriter, r *http.Request, needDownload bool) (*store.Share, vfs.Scope, bool) {
	sh, err := shLoad(a, r)
	if err != nil {
		a.fail(w, r, err)
		return nil, vfs.Scope{}, false
	}
	if !shUnlocked(a, r, sh) {
		a.fail(w, r, shErrLocked)
		return nil, vfs.Scope{}, false
	}
	if needDownload && !sh.AllowDownload {
		a.fail(w, r, apiError(http.StatusForbidden, "forbidden", "This link does not allow downloads"))
		return nil, vfs.Scope{}, false
	}
	scope, err := shScope(a, r.Context(), sh)
	if err != nil {
		a.fail(w, r, err)
		return nil, vfs.Scope{}, false
	}
	return sh, scope, true
}

// shCookieName is the unlock cookie of one link.
func shCookieName(token string) string { return shCookiePrefix + token }

// shCookieValue signs the link identity for the unlock cookie. The key is
// derived from the stored password hash, so no extra secret has to be kept and
// changing the password invalidates every cookie already handed out.
func shCookieValue(sh *store.Share, expires time.Time) string {
	unix := expires.Unix()
	mac := hmac.New(sha256.New, []byte("storix.share.unlock.v1|"+sh.PasswordHash))
	fmt.Fprintf(mac, "%d|%s|%d", sh.ID, sh.Token, unix)
	return strconv.FormatInt(unix, 10) + "." + hex.EncodeToString(mac.Sum(nil))
}

// shUnlocked reports whether this browser has already entered the password.
// The comparison is constant time.
func shUnlocked(a *API, r *http.Request, sh *store.Share) bool {
	if sh == nil {
		return false
	}
	if sh.PasswordHash == "" {
		return true
	}
	cookie, err := r.Cookie(shCookieName(sh.Token))
	if err != nil || cookie == nil || cookie.Value == "" {
		return false
	}
	rawExpiry, _, found := strings.Cut(cookie.Value, ".")
	if !found {
		return false
	}
	unix, err := strconv.ParseInt(rawExpiry, 10, 64)
	if err != nil {
		return false
	}
	expires := time.Unix(unix, 0)
	if time.Now().After(expires) {
		return false
	}
	return hmac.Equal([]byte(cookie.Value), []byte(shCookieValue(sh, expires)))
}

// shJoin turns the visitor supplied relative path into an absolute virtual
// path and proves it is still inside the shared folder.
func shJoin(sh *store.Share, rel string) (string, error) {
	root := vfs.Clean(sh.Path)
	if root == "" {
		return "", shErrGone
	}
	if !sh.IsDir {
		// A single file link has exactly one address.
		return root, nil
	}
	rel = strings.TrimSpace(rel)
	if rel == "" || rel == "/" || rel == "." {
		return root, nil
	}
	rel = strings.ReplaceAll(rel, "\\", "/")
	if strings.ContainsRune(rel, 0) {
		return "", shErrGone
	}
	joined := vfs.Clean(path.Join(root, path.Clean("/"+rel)))
	if !vfs.Contains(root, joined) {
		return "", shErrGone
	}
	return joined, nil
}

// shStartsTransfer reports whether a request begins a new transfer rather than
// continuing one that is already under way.
func shStartsTransfer(r *http.Request) bool {
	rng := strings.TrimSpace(r.Header.Get("Range"))
	if rng == "" {
		return true
	}
	return strings.HasPrefix(rng, "bytes=0-")
}

// shJoinRel appends a child name to a mount relative path.
func shJoinRel(base, name string) string {
	if base == "." || base == "" {
		return name
	}
	return base + "/" + name
}

// shRelative renders an absolute virtual path the way a visitor may see it,
// with the share root stripped off.
func shRelative(sh *store.Share, abs string) string {
	root := vfs.Clean(sh.Path)
	abs = vfs.Clean(abs)
	if abs == "" || abs == root || !vfs.Contains(root, abs) {
		return "/"
	}
	rel := strings.TrimPrefix(abs, strings.TrimSuffix(root, "/"))
	if !strings.HasPrefix(rel, "/") {
		rel = "/" + rel
	}
	return rel
}

// shHide converts a listing row into the public shape, dropping the server
// path along with the ownership and mode details.
func shHide(sh *store.Share, e vfs.Entry) shEntry {
	rel := shRelative(sh, e.Path)
	if !sh.IsDir && rel == "/" {
		// A single file link has no folder above it, so name the file itself
		// rather than reporting the root.
		rel = "/" + e.Name
	}
	return shEntry{
		Name:        e.Name,
		Path:        rel,
		IsDir:       e.IsDir,
		Size:        e.Size,
		Modified:    e.Modified,
		Kind:        e.Kind,
		MIME:        e.MIME,
		Ext:         e.Ext,
		Previewable: e.Previewable,
		Thumbnail:   e.Thumbnail,
	}
}

// shCrumbs builds the trail from the share root down to the current folder.
func shCrumbs(sh *store.Share, abs string) []shCrumb {
	out := []shCrumb{{Name: sh.Name, Path: "/"}}
	rel := shRelative(sh, abs)
	if rel == "/" {
		return out
	}
	walked := ""
	for _, part := range strings.Split(strings.Trim(rel, "/"), "/") {
		if part == "" {
			continue
		}
		walked += "/" + part
		out = append(out, shCrumb{Name: part, Path: walked})
	}
	return out
}

// shTouch records a visit, and the download when one was served.
func shTouch(a *API, sh *store.Share, download bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := a.Store.TouchShare(ctx, sh.ID, download); err != nil {
		a.Logger.Debug("share touch failed", "share", sh.ID, "err", err)
	}
}

// shParseKind validates the link kind.
func shParseKind(raw string) (store.ShareKind, error) {
	switch store.ShareKind(strings.ToLower(strings.TrimSpace(raw))) {
	case "", store.ShareDownload:
		return store.ShareDownload, nil
	case store.ShareUpload:
		return store.ShareUpload, nil
	}
	return "", badRequest("A link is either a download or an upload request")
}

// shParseExpiry turns the requested lifetime into a moment in time. A nil
// result means the link never expires.
func shParseExpiry(raw string, now time.Time) (*time.Time, error) {
	var window time.Duration
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "never":
		return nil, nil
	case "1h":
		window = time.Hour
	case "24h":
		window = 24 * time.Hour
	case "7d":
		window = 7 * 24 * time.Hour
	case "30d":
		window = 30 * 24 * time.Hour
	case "90d":
		window = 90 * 24 * time.Hour
	default:
		return nil, badRequest("Choose 1h, 24h, 7d, 30d, 90d or never")
	}
	at := now.Add(window).UTC()
	return &at, nil
}

// shExpiryLabel renders the expiry for the audit trail.
func shExpiryLabel(at *time.Time) string {
	if at == nil {
		return "no expiry"
	}
	return "until " + at.UTC().Format(time.RFC3339)
}

// shApplyFlags overwrites the capability flags the body actually carried.
func shApplyFlags(sh *store.Share, download, uploads, list *bool) {
	if download != nil {
		sh.AllowDownload = *download
	}
	if uploads != nil {
		sh.AllowUpload = *uploads
	}
	if list != nil {
		sh.AllowList = *list
	}
	// Uploads belong to an upload request. Keeping the stored record in step
	// with what the endpoints actually enforce means the owner is never shown
	// a promise the server will not keep.
	if sh.Kind == store.ShareUpload {
		sh.AllowUpload = true
	} else {
		sh.AllowUpload = false
	}
	if !sh.IsDir {
		sh.AllowList = false
	}
}

// shDisplayName is the label a link carries, falling back to the mount name
// when the whole tree is published.
func shDisplayName(virtual, name string) string {
	name = strings.TrimSpace(name)
	if name != "" && name != "/" && name != "." {
		return truncate(name, 200)
	}
	base := path.Base(vfs.Clean(virtual))
	if base == "" || base == "/" || base == "." {
		return "Shared files"
	}
	return truncate(base, 200)
}

// shOwnerName is the display name a visitor sees.
func shOwnerName(sh *store.Share, owner *store.User) string {
	if name := strings.TrimSpace(sh.OwnerName); name != "" {
		return name
	}
	if owner == nil {
		return ""
	}
	if name := strings.TrimSpace(owner.DisplayName); name != "" {
		return name
	}
	return owner.Username
}

// shLinkURL builds the address an owner hands out.
func shLinkURL(base, token string) string {
	return strings.TrimSuffix(base, "/") + "/s/" + token
}

// shZipName names the archive a folder link produces.
func shZipName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || name == "/" || name == "." {
		name = "download"
	}
	if strings.HasSuffix(strings.ToLower(name), ".zip") {
		return name
	}
	return name + ".zip"
}

// shDisposition builds a Content-Disposition value that survives both old and
// modern clients: a plain ASCII name plus the RFC 5987 encoded original.
func shDisposition(kind, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "download"
	}
	return fmt.Sprintf("%s; filename=%q; filename*=UTF-8''%s", kind, shASCIIName(name), shEncodeName(name))
}

// shASCIIName reduces a file name to characters every client can quote.
func shASCIIName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' || r == '/' {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "download"
	}
	return out
}

// shEncodeName percent encodes a file name for the filename* parameter,
// keeping only the characters RFC 5987 leaves untouched.
func shEncodeName(name string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '.', c == '_', c == '~':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0x0f])
		}
	}
	return b.String()
}
