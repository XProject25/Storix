package auth

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/XProject25/Storix/internal/store"
)

// CSRFCookie is the readable cookie the browser echoes back on every mutating
// request.
const CSRFCookie = "storix_csrf"

// CSRFHeader is the request header carrying the value of CSRFCookie.
const CSRFHeader = "X-Storix-CSRF"

// CSRFField is the form field checked when a request cannot set a header,
// such as a plain HTML form post.
const CSRFField = "_csrf"

// Session errors.
var (
	// ErrNoSession means the request carried no usable session cookie.
	ErrNoSession = errors.New("auth: no session")
	// ErrSessionExpired means the session existed but is past its absolute
	// lifetime or its idle window.
	ErrSessionExpired = errors.New("auth: session expired")
)

// Defaults applied when Options leaves a field at its zero value.
const (
	defaultCookieName  = "storix_session"
	defaultSessionTTL  = 7 * 24 * time.Hour
	defaultCookiePath  = "/"
	sessionTokenBytes  = 32
	csrfTokenBytes     = 32
	maxUserAgentLength = 512
	// lastSeenInterval bounds how often a read only request is allowed to
	// write to the database. Without it every poll of the event stream would
	// issue an UPDATE.
	lastSeenInterval = time.Minute
	// multipartMemory is the in memory budget when a CSRF token has to be
	// read out of a multipart body.
	multipartMemory = 10 << 20
)

// Options configures cookie behaviour and session lifetimes.
type Options struct {
	// CookieName is the session cookie name. Defaults to storix_session.
	CookieName string
	// TTL is the absolute lifetime of a session.
	TTL time.Duration
	// Idle expires a session that has not been seen for this long. Zero
	// disables the idle window and leaves only the absolute lifetime.
	Idle time.Duration
	// Secure marks the cookies HTTPS only.
	Secure bool
	// SameSite is the SameSite attribute for both cookies.
	SameSite http.SameSite
	// Path scopes the cookies. Defaults to "/".
	Path string
}

// normalize fills in the defaults so a zero Options is still safe to use.
func (o Options) normalize() Options {
	if o.CookieName == "" {
		o.CookieName = defaultCookieName
	}
	if o.TTL <= 0 {
		o.TTL = defaultSessionTTL
	}
	if o.Idle < 0 {
		o.Idle = 0
	}
	if o.Path == "" {
		o.Path = defaultCookiePath
	}
	if o.SameSite == http.SameSiteDefaultMode {
		o.SameSite = http.SameSiteLaxMode
	}
	return o
}

// Manager issues, resolves and revokes browser sessions.
//
// The cookie holds a random token that never touches the database. What is
// stored is its SHA-256 digest, which is also the primary key of the sessions
// row and therefore the identifier the API exposes when it lists or revokes
// sessions. Reading a session row out of a stolen database backup does not
// let anyone forge the matching cookie.
//
// The manager reads and writes the sessions table through store.Store.DB() so
// that the whole session lifecycle, cookies and digests included, lives in
// one place.
type Manager struct {
	st   *store.Store
	opts Options
}

// NewManager returns a session manager bound to a store.
func NewManager(st *store.Store, opts Options) *Manager {
	return &Manager{st: st, opts: opts.normalize()}
}

// Options returns the effective cookie and lifetime settings, with defaults
// already applied.
func (m *Manager) Options() Options { return m.opts }

// db returns the pool, or nil when the manager was built without a store.
func (m *Manager) db() *sql.DB {
	if m == nil || m.st == nil {
		return nil
	}
	return m.st.DB()
}

// Start creates a session for a user, writes the row and sets both cookies.
// The returned Session carries the database identifier, not the cookie token.
func (m *Manager) Start(ctx context.Context, w http.ResponseWriter, userID int64, ip, ua string) (*store.Session, error) {
	db := m.db()
	if db == nil {
		return nil, errors.New("auth: session manager has no store")
	}
	token, err := GenerateToken(sessionTokenBytes)
	if err != nil {
		return nil, err
	}
	csrf, err := GenerateToken(csrfTokenBytes)
	if err != nil {
		return nil, err
	}
	if len(ua) > maxUserAgentLength {
		ua = ua[:maxUserAgentLength]
	}

	now := time.Now().UTC()
	sess := &store.Session{
		ID:         HashToken(token),
		UserID:     userID,
		CSRF:       csrf,
		IP:         ip,
		UserAgent:  ua,
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(m.opts.TTL),
	}
	const q = `INSERT INTO sessions
	    (id, user_id, csrf, ip, user_agent, created_at, last_seen_at, expires_at)
	    VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	if _, err := db.ExecContext(ctx, q,
		sess.ID, sess.UserID, sess.CSRF, sess.IP, sess.UserAgent,
		sess.CreatedAt.Unix(), sess.LastSeenAt.Unix(), sess.ExpiresAt.Unix(),
	); err != nil {
		return nil, fmt.Errorf("auth: create session: %w", err)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     m.opts.CookieName,
		Value:    token,
		Path:     m.opts.Path,
		HttpOnly: true,
		Secure:   m.opts.Secure,
		SameSite: m.opts.SameSite,
		Expires:  sess.ExpiresAt,
		MaxAge:   int(m.opts.TTL / time.Second),
	})
	m.IssueCSRF(w, sess)
	return sess, nil
}

// Resolve looks up the session referenced by the request cookie. It enforces
// the absolute lifetime and the idle window, deleting the row when either has
// passed, and refreshes last_seen_at at most once a minute.
func (m *Manager) Resolve(ctx context.Context, r *http.Request) (*store.Session, error) {
	db := m.db()
	if db == nil {
		return nil, errors.New("auth: session manager has no store")
	}
	c, err := r.Cookie(m.opts.CookieName)
	if err != nil || c.Value == "" {
		return nil, ErrNoSession
	}
	id := HashToken(c.Value)

	const q = `SELECT id, user_id, csrf, ip, user_agent, created_at, last_seen_at, expires_at
	    FROM sessions WHERE id = ?`
	var (
		sess                       store.Session
		created, lastSeen, expires int64
	)
	err = db.QueryRowContext(ctx, q, id).Scan(
		&sess.ID, &sess.UserID, &sess.CSRF, &sess.IP, &sess.UserAgent,
		&created, &lastSeen, &expires,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoSession
	}
	if err != nil {
		return nil, fmt.Errorf("auth: load session: %w", err)
	}
	sess.CreatedAt = time.Unix(created, 0).UTC()
	sess.LastSeenAt = time.Unix(lastSeen, 0).UTC()
	sess.ExpiresAt = time.Unix(expires, 0).UTC()

	now := time.Now().UTC()
	idleCutoff := m.opts.Idle > 0 && now.Sub(sess.LastSeenAt) > m.opts.Idle
	if now.After(sess.ExpiresAt) || idleCutoff {
		// Drop the dead row now rather than waiting for the next purge, so a
		// replayed cookie cannot keep it alive. A failed delete is reported
		// alongside the expiry rather than instead of it, so the caller still
		// sees ErrSessionExpired and signs the browser out.
		if _, delErr := db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, sess.ID); delErr != nil {
			return nil, errors.Join(ErrSessionExpired, fmt.Errorf("auth: drop expired session: %w", delErr))
		}
		return nil, ErrSessionExpired
	}

	if now.Sub(sess.LastSeenAt) >= lastSeenInterval {
		if _, err := db.ExecContext(ctx,
			`UPDATE sessions SET last_seen_at = ? WHERE id = ?`, now.Unix(), sess.ID); err != nil {
			return nil, fmt.Errorf("auth: touch session: %w", err)
		}
		sess.LastSeenAt = now
	}
	return &sess, nil
}

// Destroy removes the session named by the request cookie and clears both
// cookies. A request without a session is not an error, so signing out twice
// behaves the same as signing out once.
func (m *Manager) Destroy(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	defer m.Clear(w)
	db := m.db()
	if db == nil {
		return errors.New("auth: session manager has no store")
	}
	c, err := r.Cookie(m.opts.CookieName)
	if err != nil || c.Value == "" {
		return nil
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, HashToken(c.Value)); err != nil {
		return fmt.Errorf("auth: delete session: %w", err)
	}
	return nil
}

// Clear expires both cookies in the browser without touching the database.
func (m *Manager) Clear(w http.ResponseWriter) {
	expired := time.Unix(0, 0).UTC()
	for _, name := range []string{m.opts.CookieName, CSRFCookie} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     m.opts.Path,
			HttpOnly: name == m.opts.CookieName,
			Secure:   m.opts.Secure,
			SameSite: m.opts.SameSite,
			Expires:  expired,
			MaxAge:   -1,
		})
	}
}

// IssueCSRF writes the CSRF cookie. It is deliberately readable by scripts so
// the single page frontend can copy it into the CSRFHeader; the value is
// useless without the HttpOnly session cookie that it is bound to.
func (m *Manager) IssueCSRF(w http.ResponseWriter, sess *store.Session) {
	if sess == nil {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookie,
		Value:    sess.CSRF,
		Path:     m.opts.Path,
		HttpOnly: false,
		Secure:   m.opts.Secure,
		SameSite: m.opts.SameSite,
		Expires:  sess.ExpiresAt,
		MaxAge:   int(m.opts.TTL / time.Second),
	})
}

// ValidateCSRF reports whether a request carries the CSRF token of its
// session. Safe methods always pass. Everything else has to present the token
// in CSRFHeader or, for form submissions, in the _csrf field.
func (m *Manager) ValidateCSRF(r *http.Request, sess *store.Session) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if sess == nil || sess.CSRF == "" {
		return false
	}
	presented := r.Header.Get(CSRFHeader)
	if presented == "" {
		presented = formCSRF(r)
	}
	if presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(sess.CSRF)) == 1
}

// formCSRF pulls the token out of a form body. The body is only parsed when
// the content type says it is a form, because parsing a JSON body here would
// consume it before the handler ever sees it.
func formCSRF(r *http.Request) string {
	if r.Body == nil {
		return ""
	}
	ct, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return ""
	}
	switch ct {
	case "application/x-www-form-urlencoded":
		if err := r.ParseForm(); err != nil {
			return ""
		}
	case "multipart/form-data":
		if err := r.ParseMultipartForm(multipartMemory); err != nil {
			return ""
		}
	default:
		return ""
	}
	if v := r.PostForm.Get(CSRFField); v != "" {
		return v
	}
	return strings.TrimSpace(r.Form.Get(CSRFField))
}

// Purge deletes every session that has expired or fallen outside the idle
// window and reports how many rows went away.
func (m *Manager) Purge(ctx context.Context) (int64, error) {
	db := m.db()
	if db == nil {
		return 0, errors.New("auth: session manager has no store")
	}
	now := time.Now().UTC().Unix()
	var (
		res sql.Result
		err error
	)
	if m.opts.Idle > 0 {
		idleCutoff := now - int64(m.opts.Idle/time.Second)
		res, err = db.ExecContext(ctx,
			`DELETE FROM sessions WHERE expires_at <= ? OR last_seen_at <= ?`, now, idleCutoff)
	} else {
		res, err = db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now)
	}
	if err != nil {
		return 0, fmt.Errorf("auth: purge sessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		// The driver knows the statement succeeded even when it cannot
		// report a count, so this is not worth failing the purge over.
		return 0, nil
	}
	return n, nil
}
