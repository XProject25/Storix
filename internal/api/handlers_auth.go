// Sign in, the signed in account and everything it owns: password, two step
// verification, sessions, preferences and the role catalogue.
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

// authLockThreshold is how many failed attempts an account tolerates before it
// is locked for the configured period.
const authLockThreshold = 8

// authRecoveryCodes is how many single use codes an enrolment hands out.
const authRecoveryCodes = 10

// Errors this file answers with. Sign in deliberately reports one message for
// an unknown account and for a wrong password, so the response cannot be used
// to find out which usernames exist.
var (
	errAuthInvalid  = apiError(http.StatusUnauthorized, "invalid_credentials", "Wrong username or password")
	errAuthDisabled = apiError(http.StatusForbidden, "disabled", "This account is disabled")
	errAuthTOTP     = apiError(http.StatusUnauthorized, "invalid_totp", "That verification code is not correct")
	errAuthPassword = apiError(http.StatusForbidden, "wrong_password", "Your current password is not correct")
)

// authDummyHash is verified against when the username does not exist, so an
// unknown account costs the same time as a wrong password. It is built once,
// on first use, with the settings real passwords are hashed with.
var authDummyHash = sync.OnceValue(func() string {
	hash, err := auth.HashPassword("storix-timing-equalizer")
	if err != nil {
		return ""
	}
	return hash
})

// authLoginRequest is the body of POST /api/v1/auth/login.
type authLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	TOTP     string `json:"totp"`
	// Remember is accepted for the sign in form. Session lifetime is a server
	// setting, so the flag does not shorten or extend it.
	Remember bool `json:"remember"`
}

// handleLogin verifies credentials and opens a session.
func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	noCache(w)

	var req authLoginRequest
	if err := decode(r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	username := strings.TrimSpace(req.Username)
	ip := a.clientIP(r)
	limitKey := "login:" + ip

	if !a.loginLimiter.Allow(limitKey) {
		a.authRecordAttempt(ctx, ip, username, false)
		a.audit(r, "auth.login", username, "too many attempts", false)
		if wait := a.loginLimiter.RetryAfter(limitKey); wait > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		}
		a.fail(w, r, errRateLimited)
		return
	}

	if username == "" || req.Password == "" {
		a.authRecordAttempt(ctx, ip, username, false)
		a.fail(w, r, errAuthInvalid)
		return
	}

	user, err := a.Store.GetUserByName(ctx, username)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			a.fail(w, r, err)
			return
		}
		// Spend the same work as a real verification before answering.
		_, _ = auth.VerifyPassword(authDummyHash(), req.Password)
		a.authRecordAttempt(ctx, ip, username, false)
		a.audit(r, "auth.login", username, "unknown account", false)
		a.fail(w, r, errAuthInvalid)
		return
	}

	if !user.Active {
		a.authRecordAttempt(ctx, ip, username, false)
		a.audit(r, "auth.login", user.Username, "account disabled", false)
		a.fail(w, r, errAuthDisabled)
		return
	}
	if left := authLockRemaining(user); left > 0 {
		a.authRecordAttempt(ctx, ip, username, false)
		a.audit(r, "auth.login", user.Username, "account locked", false)
		a.fail(w, r, apiError(http.StatusForbidden, "locked",
			"Too many failed attempts. Try again in "+authMinutes(left)))
		return
	}

	ok, err := auth.VerifyPassword(user.PasswordHash, req.Password)
	if err != nil {
		// The stored hash cannot be read, which is an operator problem rather
		// than a wrong password, but the caller still learns nothing.
		a.Logger.Error("stored password hash is unreadable", "user", user.Username, "err", err)
		ok = false
	}
	if !ok {
		a.authFailed(ctx, r, user, ip, "wrong password")
		// The attempt that trips the lock still answers with the shared
		// message; the next one names the wait.
		a.fail(w, r, errAuthInvalid)
		return
	}

	if user.TOTPEnabled && user.TOTPSecret != "" {
		code := strings.TrimSpace(req.TOTP)
		if code == "" {
			a.audit(r, "auth.login", user.Username, "verification code required", false)
			a.fail(w, r, apiError(http.StatusUnauthorized, "totp_required",
				"Enter the code from your authenticator app"))
			return
		}
		if !auth.Verify(user.TOTPSecret, code, time.Now(), 1) && !a.authConsumeRecovery(ctx, user.ID, code) {
			a.authFailed(ctx, r, user, ip, "wrong verification code")
			a.fail(w, r, errAuthTOTP)
			return
		}
	}

	if err := a.Store.RecordLogin(ctx, user.ID, ip); err != nil {
		a.Logger.Warn("last sign in not recorded", "user", user.Username, "err", err)
	}
	a.authRecordAttempt(ctx, ip, user.Username, true)
	a.loginLimiter.Reset(limitKey)
	if err := a.Store.ClearLoginAttempts(ctx, ip); err != nil {
		a.Logger.Debug("login attempts not cleared", "err", err)
	}
	a.authRehash(ctx, user, req.Password)

	sess, err := a.Session.Start(ctx, w, user.ID, ip, r.UserAgent())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	r = withUser(r, user, sess)
	a.audit(r, "auth.login", user.Username, "", true)

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                 true,
		"user":               setupSafeUser(user),
		"csrf":               sess.CSRF,
		"mustChangePassword": user.MustChangePassword,
	})
}

// handleLogout ends the session. Signing out twice is not an error, so the
// browser can always clear itself.
func (a *API) handleLogout(w http.ResponseWriter, r *http.Request) {
	noCache(w)
	user := currentUser(r)
	if err := a.Session.Destroy(r.Context(), w, r); err != nil {
		a.Logger.Warn("sign out failed", "err", err)
	}
	if user != nil {
		a.audit(r, "auth.logout", user.Username, "", true)
	}
	writeOK(w)
}

// handleMe returns everything the interface needs to draw itself for the
// signed in account.
func (a *API) handleMe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(r)
	noCache(w)

	scope, err := a.scopeFor(ctx, user)
	if err != nil {
		a.fail(w, r, err)
		return
	}

	csrf := ""
	if sess := currentSession(r); sess != nil {
		csrf = sess.CSRF
		// Refresh the readable cookie so a browser that dropped it can carry
		// on making changes without signing in again.
		a.Session.IssueCSRF(w, sess)
	}

	textMax := a.Config.Limits.TextEditMaxBytes
	if textMax <= 0 {
		textMax = a.VFS.MaxTextBytes()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"user":        setupSafeUser(user),
		"permissions": setupEffectivePermissions(user),
		"mounts":      a.VFS.Mounts(scope),
		"csrf":        csrf,
		"branding":    a.setupBranding(ctx),
		"preferences": a.authLoadPrefs(ctx, user),
		"limits": map[string]any{
			"maxUploadSize":    a.Config.Limits.MaxUploadSize,
			"textEditMaxBytes": textMax,
		},
		"features": map[string]any{
			"advanced": a.Config.Security.AllowAdvanced,
			"shares":   true,
			"totp":     true,
		},
	})
}

// authPasswordRequest is the body of POST /api/v1/auth/password.
type authPasswordRequest struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

// handleChangePassword replaces the caller password and signs every other
// browser out, which is what makes a change useful after a leak.
func (a *API) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(r)
	noCache(w)

	var req authPasswordRequest
	if err := decode(r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	ok, err := auth.VerifyPassword(user.PasswordHash, req.Current)
	if err != nil {
		a.Logger.Error("stored password hash is unreadable", "user", user.Username, "err", err)
	}
	if !ok {
		a.audit(r, "auth.password", user.Username, "current password rejected", false)
		a.fail(w, r, errAuthPassword)
		return
	}
	if err := auth.ValidatePassword(req.New); err != nil {
		a.fail(w, r, setupPasswordError(err))
		return
	}
	if req.New == req.Current {
		a.fail(w, r, badRequest("The new password has to be different from the current one"))
		return
	}

	hash, err := auth.HashPassword(req.New)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.Store.SetPassword(ctx, user.ID, hash, false); err != nil {
		a.fail(w, r, err)
		return
	}

	keep := ""
	if sess := currentSession(r); sess != nil {
		keep = sess.ID
	}
	if err := a.Store.DeleteOtherUserSessions(ctx, user.ID, keep); err != nil {
		a.Logger.Warn("other sessions not revoked", "user", user.Username, "err", err)
	}
	a.audit(r, "auth.password", user.Username, "password changed", true)
	writeOK(w)
}

// handleTOTPSetup starts two step enrolment. The secret is stored but stays
// inactive until a code proves the authenticator app holds it.
func (a *API) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(r)
	noCache(w)

	if user.TOTPEnabled {
		a.fail(w, r, conflict("Two step verification is already on, turn it off first"))
		return
	}
	secret, err := auth.NewSecret()
	if err != nil {
		a.fail(w, r, err)
		return
	}
	codes, err := auth.RecoveryCodes(authRecoveryCodes)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.Store.SetTOTP(ctx, user.ID, secret, false); err != nil {
		a.fail(w, r, err)
		return
	}
	// Only the digests are kept, so the stored copy cannot be used to sign in.
	digests := make([]string, 0, len(codes))
	for _, code := range codes {
		digests = append(digests, auth.HashToken(code))
	}
	if err := a.Store.SetJSON(ctx, authRecoveryKey(user.ID), digests); err != nil {
		a.Logger.Warn("recovery codes not stored", "user", user.Username, "err", err)
	}
	a.audit(r, "auth.totp.setup", user.Username, "enrolment started", true)

	writeJSON(w, http.StatusOK, map[string]any{
		"secret":   secret,
		"uri":      auth.ProvisioningURI(secret, user.Username, "Storix"),
		"recovery": codes,
	})
}

// authCodeRequest is the body of POST /api/v1/auth/totp/enable.
type authCodeRequest struct {
	Code string `json:"code"`
}

// handleTOTPEnable turns two step verification on once a code checks out.
func (a *API) handleTOTPEnable(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(r)
	noCache(w)

	var req authCodeRequest
	if err := decode(r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	if user.TOTPEnabled {
		a.fail(w, r, conflict("Two step verification is already on"))
		return
	}
	if user.TOTPSecret == "" {
		a.fail(w, r, badRequest("Start the setup before entering a code"))
		return
	}
	if !auth.Verify(user.TOTPSecret, strings.TrimSpace(req.Code), time.Now(), 1) {
		a.audit(r, "auth.totp.enable", user.Username, "code rejected", false)
		a.fail(w, r, errAuthTOTP)
		return
	}
	if err := a.Store.SetTOTP(ctx, user.ID, user.TOTPSecret, true); err != nil {
		a.fail(w, r, err)
		return
	}
	a.audit(r, "auth.totp.enable", user.Username, "two step verification on", true)
	writeOK(w)
}

// authPasswordOnlyRequest is the body of POST /api/v1/auth/totp/disable.
type authPasswordOnlyRequest struct {
	Password string `json:"password"`
}

// handleTOTPDisable turns two step verification off. The account password is
// required, so a borrowed session alone cannot remove the second factor.
func (a *API) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(r)
	noCache(w)

	var req authPasswordOnlyRequest
	if err := decode(r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	ok, err := auth.VerifyPassword(user.PasswordHash, req.Password)
	if err != nil {
		a.Logger.Error("stored password hash is unreadable", "user", user.Username, "err", err)
	}
	if !ok {
		a.audit(r, "auth.totp.disable", user.Username, "password rejected", false)
		a.fail(w, r, errAuthPassword)
		return
	}
	if err := a.Store.SetTOTP(ctx, user.ID, "", false); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.Store.DeleteSetting(ctx, authRecoveryKey(user.ID)); err != nil {
		a.Logger.Warn("recovery codes not cleared", "user", user.Username, "err", err)
	}
	a.audit(r, "auth.totp.disable", user.Username, "two step verification off", true)
	writeOK(w)
}

// authSessionView is one signed in browser as the account owner sees it.
type authSessionView struct {
	*store.Session
	Current bool `json:"current"`
}

// handleListSessions lists the browsers the caller is signed in on.
func (a *API) handleListSessions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(r)
	noCache(w)

	sessions, err := a.Store.ListUserSessions(ctx, user.ID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	currentID := ""
	if sess := currentSession(r); sess != nil {
		currentID = sess.ID
	}
	out := make([]authSessionView, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, authSessionView{Session: sess, Current: sess.ID == currentID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

// handleRevokeSession signs one browser out. A session belonging to another
// account is refused even though the identifier is unguessable.
func (a *API) handleRevokeSession(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(r)
	noCache(w)

	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		a.fail(w, r, badRequest("Missing session identifier"))
		return
	}
	sess, err := a.Store.GetSession(ctx, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if sess.UserID != user.ID {
		a.audit(r, "auth.session.revoke", id, "not the caller session", false)
		a.fail(w, r, errForbidden)
		return
	}
	if err := a.Store.DeleteSession(ctx, id); err != nil {
		a.fail(w, r, err)
		return
	}
	// Revoking the session in use also clears the cookies of this browser.
	if current := currentSession(r); current != nil && current.ID == id {
		a.Session.Clear(w)
	}
	a.audit(r, "auth.session.revoke", user.Username, "session revoked", true)
	writeOK(w)
}

// authPrefs is the interface state Storix remembers for an account.
type authPrefs struct {
	Theme        string `json:"theme"`
	Locale       string `json:"locale"`
	View         string `json:"view"`
	Sort         string `json:"sort"`
	Order        string `json:"order"`
	ShowHidden   bool   `json:"showHidden"`
	FoldersFirst bool   `json:"foldersFirst"`
}

// authStoredPrefs is the part that lives in settings. Theme and locale are
// columns on the account instead, because the server itself reads them.
type authStoredPrefs struct {
	View         string `json:"view"`
	Sort         string `json:"sort"`
	Order        string `json:"order"`
	ShowHidden   bool   `json:"showHidden"`
	FoldersFirst bool   `json:"foldersFirst"`
}

// handlePreferences saves the interface state and returns what was kept, so a
// value that could not be understood is visible to the caller.
func (a *API) handlePreferences(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(r)
	noCache(w)

	var req authPrefs
	if err := decode(r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	prefs := a.authLoadPrefs(ctx, user)
	prefs.Theme = authPickOne(req.Theme, prefs.Theme, "dark", "light")
	prefs.Locale = authPickLocale(req.Locale, prefs.Locale)
	prefs.View = authPickOne(req.View, prefs.View, "list", "grid", "gallery")
	prefs.Sort = authPickOne(req.Sort, prefs.Sort, "name", "size", "modified", "kind", "ext")
	prefs.Order = authPickOne(req.Order, prefs.Order, "asc", "desc")
	prefs.ShowHidden = req.ShowHidden
	prefs.FoldersFirst = req.FoldersFirst

	// Reload the account so the write does not carry a stale copy of the row
	// back to the database.
	fresh, err := a.Store.GetUser(ctx, user.ID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	fresh.Theme = prefs.Theme
	fresh.Locale = prefs.Locale
	if err := a.Store.UpdateUser(ctx, fresh); err != nil {
		a.fail(w, r, err)
		return
	}
	stored := authStoredPrefs{
		View:         prefs.View,
		Sort:         prefs.Sort,
		Order:        prefs.Order,
		ShowHidden:   prefs.ShowHidden,
		FoldersFirst: prefs.FoldersFirst,
	}
	if err := a.Store.SetJSON(ctx, authPrefsKey(user.ID), stored); err != nil {
		a.fail(w, r, err)
		return
	}
	a.audit(r, "auth.preferences", user.Username, prefs.Theme+" "+prefs.View, true)

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "preferences": prefs})
}

// authRoleView is one role preset in the catalogue.
type authRoleView struct {
	ID          store.Role         `json:"id"`
	Label       string             `json:"label"`
	Permissions []store.Permission `json:"permissions"`
}

// authPermissionView explains one capability in plain words.
type authPermissionView struct {
	ID          store.Permission `json:"id"`
	Label       string           `json:"label"`
	Description string           `json:"description"`
}

// handleRoles returns the role presets and the permission catalogue the
// account editor is built from.
func (a *API) handleRoles(w http.ResponseWriter, r *http.Request) {
	roles := []store.Role{store.RoleAdmin, store.RoleManager, store.RoleUser, store.RoleReadOnly, store.RoleCustom}
	out := make([]authRoleView, 0, len(roles))
	for _, role := range roles {
		perms := store.PermissionsForRole(role)
		if perms == nil {
			perms = []store.Permission{}
		}
		out = append(out, authRoleView{ID: role, Label: role.Label(), Permissions: perms})
	}

	all := store.AllPermissions()
	catalogue := make([]authPermissionView, 0, len(all))
	for _, p := range all {
		label, description := authPermissionText(p)
		catalogue = append(catalogue, authPermissionView{ID: p, Label: label, Description: description})
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": out, "permissions": catalogue})
}

// ---- helpers ----------------------------------------------------------------

// authRecordAttempt appends one sign in attempt to the throttle log.
func (a *API) authRecordAttempt(ctx context.Context, ip, username string, ok bool) {
	if err := a.Store.RecordLoginAttempt(ctx, ip, truncate(username, 64), ok); err != nil {
		a.Logger.Warn("sign in attempt not recorded", "err", err)
	}
}

// authFailed books a rejected sign in against both the address and the
// account, locking the account once it has failed too often.
func (a *API) authFailed(ctx context.Context, r *http.Request, user *store.User, ip, reason string) {
	a.authRecordAttempt(ctx, ip, user.Username, false)
	locked, err := a.Store.RecordFailedLogin(ctx, user.ID, authLockThreshold, a.Config.Security.LoginLockout.D())
	if err != nil {
		a.Logger.Warn("failed sign in not recorded", "user", user.Username, "err", err)
	}
	detail := reason
	if locked {
		detail = reason + ", account locked"
	}
	a.audit(r, "auth.login", user.Username, detail, false)
}

// authLockRemaining reports how long an account stays locked.
func authLockRemaining(u *store.User) time.Duration {
	if u == nil || u.LockedUntil == nil {
		return 0
	}
	left := time.Until(*u.LockedUntil)
	if left <= 0 {
		return 0
	}
	return left
}

// authMinutes renders a wait as whole minutes, rounded up.
func authMinutes(d time.Duration) string {
	minutes := int((d + time.Minute - 1) / time.Minute)
	if minutes <= 1 {
		return "1 minute"
	}
	return strconv.Itoa(minutes) + " minutes"
}

// authRehash upgrades a stored password that was hashed with settings weaker
// than the ones now in force. It runs after a correct password, which is the
// only moment the plain text is available.
func (a *API) authRehash(ctx context.Context, u *store.User, password string) {
	if !auth.NeedsRehash(u.PasswordHash, auth.DefaultParams()) {
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		a.Logger.Warn("password not rehashed", "user", u.Username, "err", err)
		return
	}
	if err := a.Store.SetPassword(ctx, u.ID, hash, u.MustChangePassword); err != nil {
		a.Logger.Warn("password not rehashed", "user", u.Username, "err", err)
		return
	}
	u.PasswordHash = hash
}

// authRecoveryKey names the setting holding the recovery code digests.
func authRecoveryKey(userID int64) string { return fmt.Sprintf("totp.recovery.%d", userID) }

// authPrefsKey names the setting holding the interface preferences.
func authPrefsKey(userID int64) string { return fmt.Sprintf("prefs.%d", userID) }

// authConsumeRecovery accepts a recovery code once and then removes it. A code
// that cannot be removed is refused rather than allowed twice.
func (a *API) authConsumeRecovery(ctx context.Context, userID int64, code string) bool {
	code = strings.TrimSpace(code)
	if code == "" {
		return false
	}
	var digests []string
	found, err := a.Store.GetJSON(ctx, authRecoveryKey(userID), &digests)
	if err != nil || !found || len(digests) == 0 {
		return false
	}
	wanted := auth.HashToken(code)
	match := -1
	for i, digest := range digests {
		if subtle.ConstantTimeCompare([]byte(digest), []byte(wanted)) == 1 {
			match = i
		}
	}
	if match < 0 {
		return false
	}
	left := make([]string, 0, len(digests)-1)
	left = append(left, digests[:match]...)
	left = append(left, digests[match+1:]...)
	if err := a.Store.SetJSON(ctx, authRecoveryKey(userID), left); err != nil {
		a.Logger.Error("recovery code not consumed", "user", userID, "err", err)
		return false
	}
	a.Logger.Info("recovery code used", "user", userID, "left", len(left))
	return true
}

// authLoadPrefs builds the current preferences from the account row and the
// stored blob, filling anything missing with the defaults.
func (a *API) authLoadPrefs(ctx context.Context, u *store.User) authPrefs {
	prefs := authPrefs{
		Theme:        "dark",
		Locale:       "en",
		View:         "list",
		Sort:         "name",
		Order:        "asc",
		FoldersFirst: true,
	}
	if u == nil {
		return prefs
	}
	stored := authStoredPrefs{
		View:         prefs.View,
		Sort:         prefs.Sort,
		Order:        prefs.Order,
		FoldersFirst: prefs.FoldersFirst,
	}
	if _, err := a.Store.GetJSON(ctx, authPrefsKey(u.ID), &stored); err != nil {
		a.Logger.Warn("preferences not readable", "user", u.Username, "err", err)
	}
	prefs.View = authPickOne(stored.View, prefs.View, "list", "grid", "gallery")
	prefs.Sort = authPickOne(stored.Sort, prefs.Sort, "name", "size", "modified", "kind", "ext")
	prefs.Order = authPickOne(stored.Order, prefs.Order, "asc", "desc")
	prefs.ShowHidden = stored.ShowHidden
	prefs.FoldersFirst = stored.FoldersFirst
	prefs.Theme = authPickOne(u.Theme, prefs.Theme, "dark", "light")
	prefs.Locale = authPickLocale(u.Locale, prefs.Locale)
	return prefs
}

// authPickOne keeps a value only when it is one Storix understands, otherwise
// the previous one stands.
func authPickOne(value, fallback string, allowed ...string) string {
	v := strings.ToLower(strings.TrimSpace(value))
	for _, candidate := range allowed {
		if v == candidate {
			return candidate
		}
	}
	return fallback
}

// authPickLocale accepts a short language tag such as "en" or "bs-BA".
func authPickLocale(value, fallback string) string {
	v := strings.TrimSpace(value)
	if len(v) < 2 || len(v) > 10 {
		return fallback
	}
	for _, c := range v {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '-', c == '_':
		default:
			return fallback
		}
	}
	return v
}

// authPermissionText is the friendly name and one sentence explanation shown
// beside a permission in the account editor.
func authPermissionText(p store.Permission) (string, string) {
	switch p {
	case store.PermView:
		return "View files", "Browse folders and open the details of a file."
	case store.PermDownload:
		return "Download", "Download single files and whole folders as an archive."
	case store.PermUpload:
		return "Upload", "Add files to any folder this account can reach."
	case store.PermCreate:
		return "Create", "Create new folders and empty files."
	case store.PermRename:
		return "Rename", "Give a file or folder a different name."
	case store.PermMove:
		return "Move", "Move files and folders to another location."
	case store.PermCopy:
		return "Copy", "Copy files and folders to another location."
	case store.PermDelete:
		return "Delete", "Send items to the recycle bin and remove them for good."
	case store.PermShare:
		return "Share links", "Publish a file or folder as a link, with a password and an expiry."
	case store.PermArchive:
		return "Archives", "Pack items into an archive and extract archives again."
	case store.PermEdit:
		return "Edit text", "Open text and code files in the editor and save changes."
	case store.PermAdvanced:
		return "Advanced tools", "Change file permissions and ownership on the server."
	case store.PermUsers:
		return "Manage users", "Create accounts, set their folders and change what they may do."
	case store.PermSettings:
		return "Server settings", "Change the served folders, branding and the rest of the settings."
	}
	return string(p), "Additional capability."
}
