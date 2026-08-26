// Resumable transfers. The tus 1.0.0 engine lives in internal/upload; these
// handlers only decide who the caller is, where that caller is allowed to
// write, and then hand the request over. Chunk bodies are never buffered here.
//
// Storix - modern web file manager for servers.
// Developed by X Project.
package api

import (
	"encoding/base64"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/XProject25/Storix/internal/upload"
	"github.com/XProject25/Storix/internal/vfs"
)

// upTusBase is the endpoint prefix the engine uses to build Location headers
// for signed in callers.
const upTusBase = "/api/v1/tus"

// upActor builds the upload identity for the current session. The router has
// already checked authentication and the upload permission; what is resolved
// here is the writable area, which is what actually bounds the transfer.
func upActor(a *API, r *http.Request, base string) (upload.Actor, error) {
	user := currentUser(r)
	if user == nil {
		return upload.Actor{}, errUnauthorized
	}
	scope, err := a.scopeFor(r.Context(), user)
	if err != nil {
		return upload.Actor{}, err
	}
	return upload.Actor{
		UserID:   user.ID,
		Username: user.Username,
		Scope:    scope,
		Base:     base,
		MaxSize:  a.Config.Limits.MaxUploadSize,
	}, nil
}

// handleTusOptions answers the tus discovery request. It carries no session
// requirement, so it is safe on both the private and the public prefix.
func (a *API) handleTusOptions(w http.ResponseWriter, r *http.Request) {
	a.Uploads.Options(w, r)
}

// handleTusCreate starts a resumable upload. POST /api/v1/tus
func (a *API) handleTusCreate(w http.ResponseWriter, r *http.Request) {
	actor, err := upActor(a, r, upTusBase)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.Uploads.Create(w, r, actor)

	// The engine sets Location only once the session exists on disk and in the
	// database, which makes it the honest signal for whether the start worked.
	started := w.Header().Get("Location") != ""
	meta := upMetadata(r.Header.Get("Upload-Metadata"))
	a.audit(r, "upload.start", upDestination(meta), upSizeDetail(r), started)
}

// handleTusHead reports the current offset so a client can resume.
// HEAD /api/v1/tus/{id}
func (a *API) handleTusHead(w http.ResponseWriter, r *http.Request) {
	actor, err := upActor(a, r, upTusBase)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.Uploads.Head(w, r, actor)
}

// handleTusPatch appends a chunk. PATCH /api/v1/tus/{id}
//
// The body here can be gigabytes, so it is streamed straight into the engine:
// no decoding, no buffering, no deadline of our own.
func (a *API) handleTusPatch(w http.ResponseWriter, r *http.Request) {
	actor, err := upActor(a, r, upTusBase)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.Uploads.Patch(w, r, actor)
}

// handleTusDelete aborts an upload and drops the partial data.
// DELETE /api/v1/tus/{id}
func (a *API) handleTusDelete(w http.ResponseWriter, r *http.Request) {
	actor, err := upActor(a, r, upTusBase)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.Uploads.Delete(w, r, actor)
}

// upItem is one row of the transfer dock.
type upItem struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Dir       string     `json:"dir"`
	Offset    int64      `json:"offset"`
	Size      int64      `json:"size"`
	CreatedAt time.Time  `json:"createdAt"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	Completed bool       `json:"completed,omitempty"`
}

// handleListUploads returns the caller's transfers still in flight, so the
// dock can be rebuilt after a page reload. GET /api/v1/uploads?all=
func (a *API) handleListUploads(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		a.fail(w, r, errUnauthorized)
		return
	}
	ctx := r.Context()
	sessions, err := a.Store.ListUserUploads(ctx, user.ID, queryBool(r, "all"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	active, bytes, err := a.Store.UploadStats(ctx, user.ID)
	if err != nil {
		a.fail(w, r, err)
		return
	}

	items := make([]upItem, 0, len(sessions))
	for _, sess := range sessions {
		if sess == nil || sess.UserID != user.ID {
			continue
		}
		item := upItem{
			ID:        sess.ID,
			Name:      sess.Filename,
			Dir:       sess.TargetDir,
			Offset:    sess.Offset,
			Size:      sess.Size,
			CreatedAt: sess.CreatedAt,
			Completed: sess.Completed,
		}
		if !sess.ExpiresAt.IsZero() {
			expires := sess.ExpiresAt
			item.ExpiresAt = &expires
		}
		items = append(items, item)
	}

	noCache(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"uploads": items,
		"active":  active,
		"bytes":   bytes,
	})
}

// ---- helpers ----------------------------------------------------------------

// upMetadata decodes the tus Upload-Metadata header, which is a comma
// separated list of "key base64value" pairs. Unreadable pairs are skipped
// rather than rejected: this copy is only used for the audit line.
func upMetadata(raw string) map[string]string {
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

// upDestination renders where the client asked the file to land, for the audit
// trail. The engine does its own validation; this is a label, not a path used
// for any file operation.
func upDestination(meta map[string]string) string {
	dir := vfs.Clean(meta["dir"])
	name := strings.TrimSpace(meta["filename"])
	name = strings.ReplaceAll(name, "\\", "/")
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	switch {
	case dir == "" && name == "":
		return ""
	case name == "":
		return dir
	case dir == "":
		return name
	}
	return truncate(path.Join(dir, name), 500)
}

// upSizeDetail describes the declared transfer size for the audit trail.
func upSizeDetail(r *http.Request) string {
	raw := strings.TrimSpace(r.Header.Get("Upload-Length"))
	if raw == "" {
		return ""
	}
	size, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || size < 0 {
		return ""
	}
	return strconv.FormatInt(size, 10) + " bytes"
}
