// Public status and the first run wizard.
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
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/XProject25/Storix/internal/auth"
	"github.com/XProject25/Storix/internal/build"
	"github.com/XProject25/Storix/internal/config"
	"github.com/XProject25/Storix/internal/store"
	"github.com/XProject25/Storix/internal/vfs"
)

// setupTokenKey names the setting that holds the one time token unlocking the
// first run wizard. The command line prints it on the first boot and
// "storix setup-token" prints it again.
const setupTokenKey = "setup.token"

// setupMaxFolders bounds the folder list a single wizard submission may carry.
const setupMaxFolders = 32

// setupRequest is the body of POST /api/v1/setup.
type setupRequest struct {
	Token       string   `json:"token"`
	Username    string   `json:"username"`
	Password    string   `json:"password"`
	DisplayName string   `json:"displayName"`
	Email       string   `json:"email"`
	Folders     []string `json:"folders"`
	Domain      string   `json:"domain"`
}

// handleSystemStatus reports what an anonymous browser is allowed to know:
// which product and version answered, whether the wizard still has to run, and
// the branding needed to paint the sign in screen. Nothing else belongs here,
// because this endpoint answers before anybody has authenticated.
func (a *API) handleSystemStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	info := build.Current()
	noCache(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"product":       info.Product,
		"version":       info.Version,
		"setupRequired": !a.Store.SetupCompleted(ctx),
		"branding":      a.setupBranding(ctx),
		"platform":      info.Platform,
		"developer":     info.Developer,
	})
}

// handleSetup runs the first run wizard: it checks the setup token, creates
// the administrator account and the folders Storix may serve, optionally
// records the public domain, closes the wizard and signs the new
// administrator in.
//
// The steps that touch the database are undone when a later one fails, so a
// half finished install never blocks a second attempt.
func (a *API) handleSetup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	noCache(w)

	if a.Store.SetupCompleted(ctx) {
		a.fail(w, r, errSetupDone)
		return
	}

	var req setupRequest
	if err := decode(r, &req); err != nil {
		a.fail(w, r, err)
		return
	}

	// Guessing the token is the only way into this endpoint, so slow it down
	// per address with the same limiter that guards sign in.
	if !a.loginLimiter.Allow("setup:" + a.clientIP(r)) {
		a.audit(r, "setup.denied", "", "too many attempts", false)
		a.fail(w, r, errRateLimited)
		return
	}

	expected, err := a.Store.GetSetting(ctx, setupTokenKey)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	presented := strings.TrimSpace(req.Token)
	// An install with no token stored cannot be unlocked by anyone, so the
	// empty case is rejected before the comparison rather than matching an
	// empty submission.
	if expected == "" || subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) != 1 {
		a.audit(r, "setup.denied", "", "wrong token", false)
		a.fail(w, r, apiError(http.StatusForbidden, "bad_token", "That setup link is not valid"))
		return
	}

	// ---- validate everything before a single row is written ----------------

	username := strings.TrimSpace(req.Username)
	if !setupValidUsername(username) {
		a.fail(w, r, badRequest("Choose a username of 2 to 32 characters using letters, digits, dot, dash or underscore"))
		return
	}
	if err := auth.ValidatePassword(req.Password); err != nil {
		a.fail(w, r, setupPasswordError(err))
		return
	}
	email := strings.TrimSpace(req.Email)
	if email != "" && !setupValidEmail(email) {
		a.fail(w, r, badRequest("Enter an email address in the form name@example.com"))
		return
	}
	domain := strings.ToLower(strings.TrimSpace(req.Domain))
	if domain != "" && !setupValidDomain(domain) {
		a.fail(w, r, badRequest("Enter a domain name in the form files.example.com"))
		return
	}
	if len(req.Folders) > setupMaxFolders {
		a.fail(w, r, badRequest(fmt.Sprintf("Add up to %d folders here, the rest can follow in settings", setupMaxFolders)))
		return
	}
	planned, err := a.setupPlanRoots(req.Folders)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		a.fail(w, r, err)
		return
	}

	// ---- write ------------------------------------------------------------

	admin := &store.User{
		Username:    username,
		DisplayName: truncate(strings.TrimSpace(req.DisplayName), 64),
		Email:       email,
		Role:        store.RoleAdmin,
		Permissions: store.PermissionsForRole(store.RoleAdmin),
		Active:      true,
		Theme:       "dark",
		Locale:      "en",
	}
	admin.PasswordHash = hash

	userID, err := a.Store.CreateUser(ctx, admin)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			a.fail(w, r, conflict("That username is already taken"))
			return
		}
		a.fail(w, r, err)
		return
	}

	created := make([]int64, 0, len(planned))
	// undo rolls the install back to an empty state so the operator can simply
	// open the wizard again. It runs on its own context because the request
	// may already be on its way out.
	undo := func() {
		bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, id := range created {
			if err := a.Store.DeleteRoot(bg, id); err != nil {
				a.Logger.Warn("setup rollback: root not removed", "id", id, "err", err)
			}
		}
		if err := a.Store.DeleteUser(bg, userID); err != nil {
			a.Logger.Warn("setup rollback: account not removed", "id", userID, "err", err)
		}
	}

	for _, root := range planned {
		id, err := a.Store.CreateRoot(ctx, root)
		if err != nil {
			// A path an earlier run already exposed is fine, keep it as it is.
			if errors.Is(err, store.ErrConflict) {
				continue
			}
			undo()
			a.fail(w, r, err)
			return
		}
		created = append(created, id)
	}

	warning := ""
	if domain != "" {
		a.Config.Server.Domain = domain
		a.Config.Server.TLS.Mode = config.TLSACME
		a.Config.Security.CookieSecure = true
		// The listener and the cookie settings are read at boot, so the
		// certificate is requested and the cookies turn secure once the
		// service restarts.
		if err := a.Config.Save(); err != nil {
			a.Logger.Warn("setup: configuration not saved", "err", err)
			warning = "The domain is active for this run but could not be written to " +
				setupConfigPath(a.Config) + ". Make the file writable and set the domain again in settings."
		}
	}

	if err := a.Store.MarkSetupCompleted(ctx); err != nil {
		undo()
		a.fail(w, r, err)
		return
	}
	if err := a.Store.DeleteSetting(ctx, setupTokenKey); err != nil {
		a.Logger.Warn("setup: token not cleared", "err", err)
	}
	// The installer prints whatever is in this file, so leaving it behind
	// means a later upgrade greets the operator with a dead setup link. The
	// token is already refused by the server, but a stale credential lying on
	// disk is worth removing on its own account.
	if dir := a.Config.Storage.DataDir; dir != "" {
		if err := os.Remove(filepath.Join(dir, "setup-token")); err != nil && !os.IsNotExist(err) {
			a.Logger.Warn("setup: token file not removed", "err", err)
		}
	}

	csrf := ""
	sess, err := a.Session.Start(ctx, w, userID, a.clientIP(r), r.UserAgent())
	if err != nil {
		// Setup itself succeeded, so the install stays usable. Report the
		// failed sign in instead of undoing the work.
		a.Logger.Error("setup: sign in failed", "err", err)
		warning = strings.TrimSpace(warning + " Your account is ready, sign in to continue.")
	} else {
		csrf = sess.CSRF
		r = withUser(r, admin, sess)
	}

	a.audit(r, "setup.completed", username, fmt.Sprintf("%d folders", len(planned)), true)
	a.Logger.Info("setup completed", "user", username, "folders", len(planned), "domain", domain)

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"user":    setupSafeUser(admin),
		"csrf":    csrf,
		"warning": warning,
	})
}

// ---- helpers ----------------------------------------------------------------

// setupSafeUser returns a copy of an account with every secret removed and the
// slices filled in, which is the only shape an account may leave the server in.
func setupSafeUser(u *store.User) *store.User {
	if u == nil {
		return nil
	}
	safe := *u
	safe.PasswordHash = ""
	safe.TOTPSecret = ""
	safe.Permissions = setupEffectivePermissions(u)
	if safe.Mounts == nil {
		safe.Mounts = []store.Mount{}
	}
	return &safe
}

// setupEffectivePermissions is what an account can actually do, which for an
// administrator is everything regardless of what the row stores.
func setupEffectivePermissions(u *store.User) []store.Permission {
	if u == nil {
		return []store.Permission{}
	}
	if u.IsAdmin() {
		return store.PermissionsForRole(store.RoleAdmin)
	}
	perms := store.NormalizePermissions(u.Permissions)
	if perms == nil {
		return []store.Permission{}
	}
	return perms
}

// setupBranding reads the stored identity, falling back to the stock one for
// any field left blank so the interface always has something to paint.
func (a *API) setupBranding(ctx context.Context) store.Branding {
	fallback := store.DefaultBranding()
	b := fallback
	if _, err := a.Store.GetJSON(ctx, store.SettingBranding, &b); err != nil {
		a.Logger.Warn("branding not readable", "err", err)
		return fallback
	}
	if strings.TrimSpace(b.Name) == "" {
		b.Name = fallback.Name
	}
	if strings.TrimSpace(b.AccentFrom) == "" {
		b.AccentFrom = fallback.AccentFrom
	}
	if strings.TrimSpace(b.AccentTo) == "" {
		b.AccentTo = fallback.AccentTo
	}
	if strings.TrimSpace(b.Footer) == "" {
		b.Footer = fallback.Footer
	}
	return b
}

// setupPasswordError turns a policy failure into a message a person can act on.
func setupPasswordError(err error) *Error {
	switch {
	case errors.Is(err, auth.ErrPasswordBlank):
		return badRequest("Enter a password")
	case errors.Is(err, auth.ErrPasswordTooShort):
		return badRequest(fmt.Sprintf("Use at least %d characters for the password", auth.MinPasswordLength))
	case errors.Is(err, auth.ErrPasswordCommon):
		return badRequest("That password is one of the first attackers try, choose another one")
	}
	return badRequest("That password cannot be used")
}

// setupValidUsername accepts 2 to 32 characters of letters, digits, dot, dash
// and underscore.
func setupValidUsername(name string) bool {
	runes := []rune(name)
	if len(runes) < 2 || len(runes) > 32 {
		return false
	}
	for _, c := range runes {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// setupValidEmail is a deliberately light check. The address is only used for
// display and notices, so anything shaped like an address is accepted.
func setupValidEmail(email string) bool {
	if len(email) > 254 || strings.ContainsAny(email, " \t\r\n") {
		return false
	}
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 {
		return false
	}
	host := email[at+1:]
	return strings.Contains(host, ".") && !strings.HasPrefix(host, ".") && !strings.HasSuffix(host, ".")
}

// setupValidDomain checks the host name a certificate would be issued for.
func setupValidDomain(domain string) bool {
	if domain == "" || len(domain) > 253 {
		return false
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 {
			return false
		}
		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, c := range label {
			switch {
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
			default:
				return false
			}
		}
	}
	return true
}

// setupPlanRoots turns the folder list from the wizard into the roots Storix
// will expose, refusing anything that is not an existing, reachable directory.
// An empty list falls back to a single sensible root.
func (a *API) setupPlanRoots(folders []string) ([]*store.Root, error) {
	planned := make([]*store.Root, 0, len(folders))
	seen := make(map[string]bool, len(folders))
	for _, raw := range folders {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if !setupAbsolute(raw) {
			return nil, badRequest("Use a full path that starts with a slash, for example /home")
		}
		p := vfs.Clean(raw)
		if p == "" || seen[p] {
			continue
		}
		if err := a.setupCheckDir(p); err != nil {
			return nil, err
		}
		seen[p] = true
		planned = append(planned, &store.Root{
			Path:      p,
			Label:     setupRootLabel(p),
			Icon:      "folder",
			SortOrder: len(planned),
		})
	}
	if len(planned) == 0 {
		p := setupDefaultRoot()
		planned = append(planned, &store.Root{Path: p, Label: setupRootLabel(p), Icon: "folder"})
	}
	return planned, nil
}

// setupCheckDir refuses a folder that does not exist, is not a directory or
// that the guarded file system layer protects.
func (a *API) setupCheckDir(p string) error {
	info, err := os.Stat(filepath.FromSlash(p))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return badRequest("There is no folder at " + p)
	case err != nil:
		return badRequest("Storix cannot open " + p)
	case !info.IsDir():
		return badRequest(p + " is a file, pick a folder")
	}
	if a.VFS.Denied(p) {
		return apiError(http.StatusForbidden, "denied", p+" is protected and cannot be served")
	}
	// Resolving against a scope holding only this path proves the guarded
	// layer can actually open it before the root is written to the database.
	scope := vfs.Scope{Mounts: []vfs.Mount{{Path: p}}, Admin: true}
	if _, err := a.VFS.Resolve(scope, p); err != nil {
		return err
	}
	return nil
}

// setupAbsolute reports whether the operator typed a full path. Windows drive
// letters are accepted so the binary can also be run on a workstation.
func setupAbsolute(raw string) bool {
	p := strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/")
	if strings.HasPrefix(p, "/") {
		return true
	}
	if runtime.GOOS == "windows" && len(p) > 2 && p[1] == ':' && p[2] == '/' {
		return true
	}
	return false
}

// setupDefaultRoot is the folder Storix serves when the operator names none.
func setupDefaultRoot() string {
	if runtime.GOOS == "linux" {
		if info, err := os.Stat("/home"); err == nil && info.IsDir() {
			return "/home"
		}
	}
	return "/"
}

// setupRootLabel is the name shown in the sidebar for an exposed folder.
func setupRootLabel(p string) string {
	if p == "/" {
		return "Root volume"
	}
	base := path.Base(p)
	if base == "" || base == "." || base == "/" {
		return "Root volume"
	}
	return base
}

// setupConfigPath names the configuration file for a message, or describes it
// when the path is unknown.
func setupConfigPath(c *config.Config) string {
	if p := strings.TrimSpace(c.Path()); p != "" {
		return p
	}
	return "the configuration file"
}
