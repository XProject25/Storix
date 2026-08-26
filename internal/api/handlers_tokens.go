// Programmatic access. Everything else in Storix is driven by a browser that
// holds a session cookie and echoes a CSRF header, which a backup script cannot
// do and a WebDAV client has no way to express, so an account can also mint
// tokens and present them in the Authorization header.
//
// A token reads as sxp_<prefix>_<secret>. The prefix is stored in the clear and
// is what the lookup indexes, the secret is kept only as a digest and is shown
// to the owner exactly once, at creation. A token never widens an account: a
// read scoped one narrows it to browsing and downloading, and a write scoped
// one carries no more than the account already had.
//
// Storix - modern web file manager for servers.
// Developed by X Project.
package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/XProject25/Storix/internal/auth"
	"github.com/XProject25/Storix/internal/store"
)

const (
	// tokTag opens every Storix token, so one that turns up in a log file or a
	// repository is recognisable for what it is.
	tokTag = "sxp_"
	// tokPrefixLen is the length of the part kept in the clear.
	tokPrefixLen = 8
	// tokSecretLen is the length of the part only the owner ever sees.
	tokSecretLen = 32
	// tokNameMax is the longest token name accepted.
	tokNameMax = 64
	// tokMaxPerUser caps how many live tokens one account may hold, which keeps
	// a forgotten script from minting credentials without end.
	tokMaxPerUser = 20
	// tokTouchEvery is how often the last used stamp is written back. A busy
	// script would otherwise turn every read into a write.
	tokTouchEvery = time.Minute
	// tokTouchTimeout bounds that write, which nothing is waiting on.
	tokTouchTimeout = 5 * time.Second
	// tokSweepAt is the size at which the last used map drops stale entries.
	tokSweepAt = 256
)

// tokLastUse remembers when each token last had its stamp written back.
// tokLastUseMu guards it.
var (
	tokLastUseMu sync.Mutex
	tokLastUse   = make(map[int64]time.Time)
)

// tokWebDAVInfo is the network drive panel that sits beside the token list: the
// address to mount and the one line each platform needs to mount it.
type tokWebDAVInfo struct {
	Enabled bool   `json:"enabled"`
	URL     string `json:"url"`
	Windows string `json:"windows"`
	MacOS   string `json:"macos"`
	Linux   string `json:"linux"`
}

// handleListTokens returns the credentials of the signed in account together
// with the instructions for mounting Storix as a drive. Expired tokens are
// listed too, marked as such, so the owner can see what to clear out.
// GET /api/v1/auth/tokens
func (a *API) handleListTokens(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	noCache(w)

	tokens, err := a.Store.ListTokens(r.Context(), user.ID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tokens": tokens,
		"webdav": a.tokWebDAV(r, user),
	})
}

// tokCreateRequest is the body of POST /api/v1/auth/tokens.
type tokCreateRequest struct {
	Name      string `json:"name"`
	Scope     string `json:"scope"`
	ExpiresIn string `json:"expiresIn"`
}

// handleCreateToken mints a credential. The full token comes back once, in this
// response, and cannot be recovered afterwards: only its digest is stored.
// POST /api/v1/auth/tokens
func (a *API) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(r)
	noCache(w)

	var body tokCreateRequest
	if err := decode(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	name := strings.TrimSpace(body.Name)
	if n := len([]rune(name)); n == 0 || n > tokNameMax {
		a.fail(w, r, badRequest("Give the token a name of 1 to 64 characters"))
		return
	}
	scope, err := tokParseScope(body.Scope)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	expires, err := tokParseExpiry(body.ExpiresIn, time.Now())
	if err != nil {
		a.fail(w, r, err)
		return
	}

	existing, err := a.Store.ListTokens(ctx, user.ID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if tokLiveCount(existing) >= tokMaxPerUser {
		a.audit(r, "token.create", name, "the account is at its token limit", false)
		a.fail(w, r, conflict("This account already holds "+strconv.Itoa(tokMaxPerUser)+
			" tokens, revoke one before adding another"))
		return
	}

	record := &store.APIToken{
		UserID:    user.ID,
		Name:      name,
		Scope:     scope,
		ExpiresAt: expires,
		CreatedAt: time.Now().UTC(),
	}
	// A prefix collision is astronomically unlikely, but retrying is cheaper
	// than handing the caller an error it cannot act on.
	var (
		created bool
		secret  string
	)
	for attempt := 0; attempt < 5; attempt++ {
		prefix, candidate := tokMint()
		record.Prefix = prefix
		record.Hash = auth.HashToken(candidate)
		_, err = a.Store.CreateToken(ctx, record)
		if err == nil {
			created = true
			secret = candidate
			break
		}
		if !errors.Is(err, store.ErrConflict) {
			break
		}
	}
	if !created {
		a.audit(r, "token.create", name, "could not be stored", false)
		a.fail(w, r, err)
		return
	}

	a.audit(r, "token.create", record.Prefix, name+", "+scope.Label()+", "+tokExpiryLabel(expires), true)
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":  record,
		"secret": tokFormat(record.Prefix, secret),
	})
}

// handleDeleteToken revokes a credential. A token belonging to another account
// is refused, and the caller cannot tell that apart from one that never
// existed. DELETE /api/v1/auth/tokens/{id}
func (a *API) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	user := currentUser(r)
	noCache(w)

	if err := a.Store.DeleteToken(r.Context(), id, user.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			a.audit(r, "token.revoke", strconv.FormatInt(id, 10), "not a token of this account", false)
		}
		a.fail(w, r, err)
		return
	}
	a.audit(r, "token.revoke", strconv.FormatInt(id, 10), "token revoked", true)
	writeOK(w)
}

// ---- authentication ----------------------------------------------------------

// tokenUser resolves the credential on a request into the account it stands
// for, or nil when there is none. The authenticate middleware calls it on every
// request, so the common case, a browser with no Authorization header, costs one
// map lookup and nothing else.
//
// Any failure at all reports nil rather than an error: the request simply
// carries on as anonymous and the endpoint it reaches decides what that means.
// The secret is never logged.
func (a *API) tokenUser(r *http.Request) *store.User {
	if a == nil || a.Store == nil || r == nil {
		return nil
	}
	raw, basicUser := tokCredential(r)
	if raw == "" {
		return nil
	}
	prefix, secret, ok := tokParse(raw)
	if !ok {
		return nil
	}
	ctx := r.Context()
	record, err := a.Store.GetTokenByPrefix(ctx, prefix)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			a.Logger.Warn("token lookup failed", "prefix", prefix, "err", err)
		}
		return nil
	}
	// The digest of the presented secret is compared without an early exit, so
	// the answer takes the same time whichever character first differs.
	if subtle.ConstantTimeCompare([]byte(record.Hash), []byte(auth.HashToken(secret))) != 1 {
		return nil
	}
	// Expired is stamped when the row is read, against the current time.
	if record.Expired {
		return nil
	}
	user, err := a.Store.GetUser(ctx, record.UserID)
	if err != nil || user == nil || !user.Active {
		return nil
	}
	// A WebDAV client sends a username beside the token. It has to name the
	// account the token belongs to, so a mount cannot quietly land somewhere
	// other than where the person typing it expected.
	if basicUser != "" && !strings.EqualFold(strings.TrimSpace(basicUser), user.Username) {
		return nil
	}
	a.tokTouch(record.ID, a.clientIP(r))
	return a.tokNarrow(ctx, user, record.Scope)
}

// TokenAuthUser resolves the credential on a request exactly as the
// authenticate middleware does, and returns nil when there is none. The WebDAV
// handler goes through it, since a mounted drive never carries a session cookie
// and has nowhere to put a CSRF header.
func (a *API) TokenAuthUser(r *http.Request) *store.User { return a.tokenUser(r) }

// tokCredential pulls the presented token out of the Authorization header. Two
// forms are accepted: a bearer token, which is what a script sends, and Basic
// credentials, which is all a WebDAV client can offer. The username of a Basic
// pair comes back with it so the caller can insist the two agree.
func tokCredential(r *http.Request) (secret, username string) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return "", ""
	}
	// Scheme names are case insensitive, and clients differ on how they write
	// them.
	const bearer = "bearer "
	if len(header) > len(bearer) && strings.EqualFold(header[:len(bearer)], bearer) {
		return strings.TrimSpace(header[len(bearer):]), ""
	}
	if user, pass, ok := r.BasicAuth(); ok {
		return strings.TrimSpace(pass), user
	}
	return "", ""
}

// tokParse splits a presented token into the prefix that is stored in the clear
// and the secret behind it. The shape is checked before the database is
// touched, so a header carrying anything else costs nothing at all.
func tokParse(raw string) (prefix, secret string, ok bool) {
	rest, found := strings.CutPrefix(raw, tokTag)
	if !found || len(rest) != tokPrefixLen+1+tokSecretLen || rest[tokPrefixLen] != '_' {
		return "", "", false
	}
	return rest[:tokPrefixLen], rest[tokPrefixLen+1:], true
}

// tokNarrow returns the account a token stands for. A write scoped token hands
// back what the store loaded; a read scoped one hands back a copy holding only
// view and download, never the loaded pointer, so nothing downstream can write
// through it into an account another request is also reading.
//
// An administrator needs one more step. That role is allowed everything without
// its permissions being consulted, so the copy is demoted as well, and a demoted
// copy would see nothing at all because the served folders are only read for an
// administrator. They are therefore carried across as read only mounts, which
// leaves the same tree visible with none of the write paths open.
func (a *API) tokNarrow(ctx context.Context, u *store.User, scope store.TokenScope) *store.User {
	if u == nil || scope != store.ScopeRead {
		return u
	}
	narrowed := *u
	narrowed.Permissions = []store.Permission{store.PermView, store.PermDownload}
	narrowed.Mounts = append([]store.Mount(nil), u.Mounts...)
	if u.IsAdmin() {
		narrowed.Role = store.RoleCustom
		roots, err := a.Store.ListRoots(ctx)
		if err != nil {
			// Showing nothing is the safe way to fail here.
			a.Logger.Warn("served folders unavailable for a read token", "user", u.Username, "err", err)
			return &narrowed
		}
		mounts := make([]store.Mount, 0, len(roots))
		for _, root := range roots {
			mounts = append(mounts, store.Mount{
				UserID:    u.ID,
				Path:      root.Path,
				Label:     root.Label,
				Icon:      root.Icon,
				ReadOnly:  true,
				SortOrder: root.SortOrder,
			})
		}
		narrowed.Mounts = mounts
	}
	return &narrowed
}

// tokTouch records that a token was used, at most once a minute per token and
// never on the request goroutine. A script polling a folder every second would
// otherwise turn a read only workload into a write on every call.
func (a *API) tokTouch(id int64, ip string) {
	now := time.Now()
	tokLastUseMu.Lock()
	if last, seen := tokLastUse[id]; seen && now.Sub(last) < tokTouchEvery {
		tokLastUseMu.Unlock()
		return
	}
	tokLastUse[id] = now
	// Revoked tokens would otherwise sit in the map for the life of the
	// process, so anything older than the interval is dropped once the map has
	// grown past a size no real account reaches.
	if len(tokLastUse) > tokSweepAt {
		for other, at := range tokLastUse {
			if now.Sub(at) >= tokTouchEvery {
				delete(tokLastUse, other)
			}
		}
	}
	tokLastUseMu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), tokTouchTimeout)
		defer cancel()
		if err := a.Store.TouchToken(ctx, id, now, ip); err != nil {
			a.Logger.Debug("token use not recorded", "token", id, "err", err)
		}
	}()
}

// ---- helpers -----------------------------------------------------------------

// tokMint returns a fresh credential: the prefix stored in the clear and the
// secret the owner sees once.
func tokMint() (prefix, secret string) {
	return tokRandom(tokPrefixLen), tokRandom(tokSecretLen)
}

// tokRandom draws n characters from the alphabet public links are minted with,
// which leaves out the characters people misread when they copy a token by
// hand. auth.ShareToken is that generator, so drawing from it repeatedly keeps
// one sampler in the build rather than a second copy here.
func tokRandom(n int) string {
	var out strings.Builder
	for out.Len() < n {
		out.WriteString(auth.ShareToken())
	}
	return out.String()[:n]
}

// tokFormat assembles what the owner copies out of the interface.
func tokFormat(prefix, secret string) string {
	return tokTag + prefix + "_" + secret
}

// tokParseScope reads the requested scope, defaulting to the narrower one.
func tokParseScope(raw string) (store.TokenScope, error) {
	scope := store.TokenScope(strings.ToLower(strings.TrimSpace(raw)))
	if scope == "" {
		return store.ScopeRead, nil
	}
	if !scope.Valid() {
		return "", badRequest("Choose read or write")
	}
	return scope, nil
}

// tokParseExpiry turns the requested lifetime into a moment. A token is a long
// lived credential, so the choices are longer than the ones a public link
// offers, and never is a legitimate answer for a machine that has to keep
// running unattended.
func tokParseExpiry(raw string, now time.Time) (*time.Time, error) {
	var window time.Duration
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "never":
		return nil, nil
	case "30d":
		window = 30 * 24 * time.Hour
	case "90d":
		window = 90 * 24 * time.Hour
	case "1y":
		window = 365 * 24 * time.Hour
	default:
		return nil, badRequest("Choose 30d, 90d, 1y or never")
	}
	at := now.Add(window).UTC()
	return &at, nil
}

// tokExpiryLabel renders a lifetime for the audit trail.
func tokExpiryLabel(at *time.Time) string {
	if at == nil {
		return "no expiry"
	}
	return "until " + at.UTC().Format(time.RFC3339)
}

// tokLiveCount reports how many of an account's tokens are still usable, so an
// expired one that nobody cleared away does not consume the allowance.
func tokLiveCount(tokens []*store.APIToken) int {
	live := 0
	for _, t := range tokens {
		if t != nil && !t.Expired {
			live++
		}
	}
	return live
}

// tokWebDAV builds the mounting instructions shown beside the token list. The
// address is the one this request arrived on, so an install reached through a
// proxy or a domain describes itself correctly.
func (a *API) tokWebDAV(r *http.Request, u *store.User) tokWebDAVInfo {
	url := strings.TrimRight(a.baseURL(r), "/") + "/dav/"
	name := "your username"
	if u != nil && u.Username != "" {
		name = u.Username
	}
	return tokWebDAVInfo{
		Enabled: true,
		URL:     url,
		Windows: fmt.Sprintf("net use Z: %s /user:%s <token>", url, name),
		MacOS:   fmt.Sprintf("Open the Finder, press Command K and enter %s, then sign in as %s with a token as the password", url, name),
		Linux:   fmt.Sprintf("mount -t davfs %s /mnt/storix", url),
	}
}
