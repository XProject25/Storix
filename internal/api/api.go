// Package api exposes the Storix HTTP interface: a JSON API under /api/v1
// and the embedded web application on every other path.
//
// Storix - modern web file manager for servers.
// Developed by X Project.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/XProject25/Storix/internal/auth"
	"github.com/XProject25/Storix/internal/config"
	"github.com/XProject25/Storix/internal/events"
	"github.com/XProject25/Storix/internal/jobs"
	"github.com/XProject25/Storix/internal/store"
	"github.com/XProject25/Storix/internal/thumbs"
	"github.com/XProject25/Storix/internal/updater"
	"github.com/XProject25/Storix/internal/upload"
	"github.com/XProject25/Storix/internal/vfs"
)

// Deps are the collaborators the API needs.
type Deps struct {
	Config  *config.Config
	Store   *store.Store
	VFS     *vfs.VFS
	Session *auth.Manager
	Jobs    *jobs.Manager
	Events  *events.Hub
	Thumbs  *thumbs.Cache
	Uploads *upload.Manager
	Updater *updater.Updater
	Logger  *slog.Logger
	Static  http.Handler
}

// API is the HTTP surface of Storix.
type API struct {
	Deps

	loginLimiter *auth.Limiter
	writeLimiter *auth.Limiter
	startedAt    time.Time
}

// New builds the API.
func New(d Deps) *API {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	window := d.Config.Security.LoginRateWindow.D()
	burst := d.Config.Security.LoginRateBurst
	return &API{
		Deps:         d,
		loginLimiter: auth.NewLimiter(burst, window),
		writeLimiter: auth.NewLimiter(600, time.Minute),
		startedAt:    time.Now(),
	}
}

// Close releases background resources.
func (a *API) Close() {
	a.loginLimiter.Close()
	a.writeLimiter.Close()
}

// Uptime reports how long the server has been running.
func (a *API) Uptime() time.Duration { return time.Since(a.startedAt) }

// ---- request context --------------------------------------------------------

type ctxKey int

const (
	ctxUser ctxKey = iota
	ctxSession
	ctxShare
)

// currentUser returns the authenticated account, or nil.
func currentUser(r *http.Request) *store.User {
	u, _ := r.Context().Value(ctxUser).(*store.User)
	return u
}

// currentSession returns the active session, or nil.
func currentSession(r *http.Request) *store.Session {
	s, _ := r.Context().Value(ctxSession).(*store.Session)
	return s
}

// currentShare returns the public share being served, or nil.
func currentShare(r *http.Request) *store.Share {
	s, _ := r.Context().Value(ctxShare).(*store.Share)
	return s
}

// withUser attaches the account and session to the request context.
func withUser(r *http.Request, u *store.User, s *store.Session) *http.Request {
	ctx := context.WithValue(r.Context(), ctxUser, u)
	ctx = context.WithValue(ctx, ctxSession, s)
	return r.WithContext(ctx)
}

// scopeFor builds the guarded file system scope for an account. Administrators
// see every configured root; everyone else sees only their own mounts.
func (a *API) scopeFor(ctx context.Context, u *store.User) (vfs.Scope, error) {
	if u == nil {
		return vfs.Scope{}, errUnauthorized
	}
	if u.IsAdmin() {
		roots, err := a.Store.ListRoots(ctx)
		if err != nil {
			return vfs.Scope{}, err
		}
		mounts := make([]vfs.Mount, 0, len(roots))
		for _, r := range roots {
			mounts = append(mounts, vfs.Mount{Path: r.Path, Label: r.Label, Icon: r.Icon, ReadOnly: r.ReadOnly})
		}
		return vfs.Scope{Mounts: mounts, Admin: true}, nil
	}
	mounts := make([]vfs.Mount, 0, len(u.Mounts))
	for _, m := range u.Mounts {
		mounts = append(mounts, vfs.Mount{Path: m.Path, Label: m.Label, Icon: m.Icon, ReadOnly: m.ReadOnly || !u.Can(store.PermUpload)})
	}
	return vfs.Scope{Mounts: mounts}, nil
}

// clientIP resolves the caller address, honouring proxies only when trusted.
func (a *API) clientIP(r *http.Request) string {
	return auth.ClientIP(r, a.Config.Server.TrustedProxies)
}

// ---- responses --------------------------------------------------------------

// Error is the JSON error envelope.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
	status  int
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Status reports the HTTP status the error maps to.
func (e *Error) Status() int {
	if e.status == 0 {
		return http.StatusInternalServerError
	}
	return e.status
}

// apiError builds an error envelope.
func apiError(status int, code, message string) *Error {
	return &Error{Code: code, Message: message, status: status}
}

// Frequently used errors.
var (
	errUnauthorized = apiError(http.StatusUnauthorized, "unauthorized", "Sign in to continue")
	errForbidden    = apiError(http.StatusForbidden, "forbidden", "You do not have access to this")
	errNotFound     = apiError(http.StatusNotFound, "not_found", "Not found")
	errCSRF         = apiError(http.StatusForbidden, "csrf", "Security token mismatch, reload the page")
	errSetupDone    = apiError(http.StatusConflict, "setup_completed", "Storix is already set up")
	errRateLimited  = apiError(http.StatusTooManyRequests, "rate_limited", "Too many attempts, try again later")
)

func badRequest(msg string) *Error { return apiError(http.StatusBadRequest, "bad_request", msg) }
func conflict(msg string) *Error   { return apiError(http.StatusConflict, "conflict", msg) }

// writeJSON sends a JSON payload.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// writeOK sends the empty success envelope.
func writeOK(w http.ResponseWriter) { writeJSON(w, http.StatusOK, map[string]bool{"ok": true}) }

// fail maps an error onto the JSON envelope, translating the domain sentinels
// so handlers can simply return what the lower layers gave them.
func (a *API) fail(w http.ResponseWriter, r *http.Request, err error) {
	if err == nil {
		return
	}
	var apiErr *Error
	if errors.As(err, &apiErr) {
		writeJSON(w, apiErr.Status(), map[string]any{"error": apiErr})
		return
	}

	var out *Error
	switch {
	case errors.Is(err, vfs.ErrForbidden):
		out = apiError(http.StatusForbidden, "forbidden", "That path is outside the area you can access")
	case errors.Is(err, vfs.ErrDenied):
		out = apiError(http.StatusForbidden, "denied", "That path is protected")
	case errors.Is(err, vfs.ErrReadOnly):
		out = apiError(http.StatusForbidden, "read_only", "This location is read only")
	case errors.Is(err, vfs.ErrNotFound), errors.Is(err, store.ErrNotFound):
		out = apiError(http.StatusNotFound, "not_found", "Not found")
	case errors.Is(err, vfs.ErrExists), errors.Is(err, store.ErrConflict):
		out = apiError(http.StatusConflict, "exists", "A file with that name is already here")
	case errors.Is(err, vfs.ErrInvalidName):
		out = badRequest("That name is not allowed")
	case errors.Is(err, vfs.ErrNotDir):
		out = badRequest("That path is not a folder")
	case errors.Is(err, vfs.ErrIsDir):
		out = badRequest("That path is a folder")
	case errors.Is(err, vfs.ErrTooLarge):
		out = apiError(http.StatusRequestEntityTooLarge, "too_large", "That file is too large for this operation")
	case errors.Is(err, context.Canceled):
		out = apiError(499, "canceled", "Request canceled")
	default:
		a.Logger.Error("request failed", "path", r.URL.Path, "method", r.Method, "err", err)
		out = apiError(http.StatusInternalServerError, "internal", "Something went wrong on the server")
	}
	writeJSON(w, out.Status(), map[string]any{"error": out})
}

// decode reads a JSON body with a sane size limit.
func decode(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 16<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return badRequest("Malformed request body")
	}
	return nil
}

// ---- query helpers ----------------------------------------------------------

func queryBool(r *http.Request, key string) bool {
	v := strings.ToLower(strings.TrimSpace(r.URL.Query().Get(key)))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func queryInt(r *http.Request, key string, def int) int {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}

func queryInt64(r *http.Request, key string, def int64) int64 {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return def
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return def
	}
	return n
}

// pathParam reads a required ?path= parameter.
func pathParam(r *http.Request, key string) (string, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return "", badRequest("Missing " + key + " parameter")
	}
	return vfs.Clean(raw), nil
}

// idParam reads a numeric path value such as /users/{id}.
func idParam(r *http.Request, name string) (int64, error) {
	raw := r.PathValue(name)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, badRequest("Invalid identifier")
	}
	return id, nil
}

// ---- audit ------------------------------------------------------------------

// audit records a security relevant action, best effort.
func (a *API) audit(r *http.Request, action, target, detail string, ok bool) {
	u := currentUser(r)
	entry := store.AuditEntry{
		Action: action,
		Target: target,
		Detail: detail,
		IP:     a.clientIP(r),
		UA:     truncate(r.UserAgent(), 250),
		OK:     ok,
		At:     time.Now(),
	}
	if u != nil {
		entry.UserID = u.ID
		entry.Username = u.Username
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := a.Store.Audit(ctx, entry); err != nil {
		a.Logger.Warn("audit write failed", "action", action, "err", err)
	}
}

// touchRecent remembers a file the user just worked with.
func (a *API) touchRecent(ctx context.Context, userID int64, e vfs.Entry, action string) {
	if userID == 0 {
		return
	}
	rec := &store.Recent{
		UserID: userID,
		Path:   e.Path,
		Name:   e.Name,
		IsDir:  e.IsDir,
		Size:   e.Size,
		Action: action,
		At:     time.Now(),
	}
	if err := a.Store.TouchRecent(ctx, rec); err != nil {
		a.Logger.Debug("recent write failed", "err", err)
	}
}

// publish emits an event to a single user.
func (a *API) publish(userID int64, typ string, data any) {
	if a.Events == nil {
		return
	}
	a.Events.Publish(userID, events.Event{Type: typ, Data: data, At: time.Now()})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// hostOnly strips the port from a host:port pair.
func hostOnly(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// baseURL reconstructs the externally visible origin for building share links.
func (a *API) baseURL(r *http.Request) string {
	if u := a.Config.PublicURL(); u != "" && !strings.Contains(u, "localhost") {
		return u
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" && a.trustsPeer(r) {
		scheme = proto
	}
	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" && a.trustsPeer(r) {
		host = fwd
	}
	return fmt.Sprintf("%s://%s", scheme, host)
}

// trustsPeer reports whether forwarding headers may be believed.
func (a *API) trustsPeer(r *http.Request) bool {
	if len(a.Config.Server.TrustedProxies) == 0 {
		return false
	}
	return a.clientIP(r) != hostOnly(r.RemoteAddr)
}
