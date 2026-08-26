package api

import (
	"context"
	"errors"
	"net/http"
	"path"
	"regexp"
	"strings"

	"github.com/XProject25/Storix/internal/auth"
	"github.com/XProject25/Storix/internal/store"
	"github.com/XProject25/Storix/internal/vfs"
)

// Account administration. The router already required the users permission,
// so these handlers concentrate on the rules that permission does not cover:
// nobody may hand out a capability they do not hold, no change may leave the
// panel without an administrator, and every folder assigned to an account has
// to sit inside a storage location the operator exposed.

// usrNamePattern is the accepted username shape: two to thirty two letters,
// digits, dots, dashes or underscores.
var usrNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{2,32}$`)

// usrView is one account as the administration screen sees it: the safe user
// record plus how many browser sessions it currently holds.
type usrView struct {
	*store.User
	Sessions int `json:"sessions"`
}

// usrMountInput is one folder assigned to an account. Create asks Storix to
// make the directory when it is not there yet.
type usrMountInput struct {
	Path     string `json:"path"`
	Label    string `json:"label"`
	Icon     string `json:"icon"`
	ReadOnly bool   `json:"readOnly"`
	Create   bool   `json:"create"`
}

// usrCreateRequest is the body of POST /api/v1/users.
type usrCreateRequest struct {
	Username           string             `json:"username"`
	Password           string             `json:"password"`
	DisplayName        string             `json:"displayName"`
	Email              string             `json:"email"`
	Role               string             `json:"role"`
	Permissions        []store.Permission `json:"permissions"`
	Mounts             []usrMountInput    `json:"mounts"`
	Active             *bool              `json:"active"`
	MustChangePassword bool               `json:"mustChangePassword"`
	Quota              int64              `json:"quota"`
}

// usrUpdateRequest is the body of PATCH /api/v1/users/{id}. Every field is
// optional; an absent field leaves the stored value alone.
type usrUpdateRequest struct {
	Username           *string             `json:"username"`
	Password           *string             `json:"password"`
	DisplayName        *string             `json:"displayName"`
	Email              *string             `json:"email"`
	Role               *string             `json:"role"`
	Permissions        *[]store.Permission `json:"permissions"`
	Mounts             *[]usrMountInput    `json:"mounts"`
	Active             *bool               `json:"active"`
	MustChangePassword *bool               `json:"mustChangePassword"`
	Quota              *int64              `json:"quota"`
}

// handleListUsers returns every account with its folders and session count.
func (a *API) handleListUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	users, err := a.Store.ListUsers(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := make([]usrView, 0, len(users))
	for _, u := range users {
		out = append(out, a.usrWithSessions(ctx, u))
	}
	noCache(w)
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

// handleCreateUser adds an account.
func (a *API) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	caller := currentUser(r)
	var req usrCreateRequest
	if err := decode(r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	ctx := r.Context()

	username := strings.TrimSpace(req.Username)
	if err := usrCheckName(username); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := auth.ValidatePassword(req.Password); err != nil {
		a.fail(w, r, usrPasswordError(err))
		return
	}
	role, err := usrParseRole(req.Role)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if role == store.RoleAdmin && !caller.IsAdmin() {
		a.audit(r, "user.create", username, "role admin", false)
		a.fail(w, r, apiError(http.StatusForbidden, "forbidden",
			"Only an administrator can create another administrator"))
		return
	}

	perms, err := usrEffectivePermissions(role, req.Permissions, len(req.Permissions) > 0)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := usrCheckGrant(caller, perms); err != nil {
		a.audit(r, "user.create", username, "permission not held by caller", false)
		a.fail(w, r, err)
		return
	}

	mounts, err := a.usrResolveMounts(ctx, req.Mounts)
	if err != nil {
		a.fail(w, r, err)
		return
	}

	if _, err := a.Store.GetUserByName(ctx, username); err == nil {
		a.fail(w, r, conflict("That username is already taken"))
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		a.fail(w, r, err)
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		a.fail(w, r, err)
		return
	}

	active := true
	if req.Active != nil {
		active = *req.Active
	}
	quota := req.Quota
	if quota < 0 {
		quota = 0
	}
	account := &store.User{
		Username:           username,
		DisplayName:        strings.TrimSpace(req.DisplayName),
		Email:              strings.TrimSpace(req.Email),
		PasswordHash:       hash,
		Role:               role,
		Permissions:        perms,
		Active:             active,
		MustChangePassword: req.MustChangePassword,
		Quota:              quota,
		Mounts:             mounts,
	}
	id, err := a.Store.CreateUser(ctx, account)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			a.fail(w, r, conflict("That username is already taken"))
			return
		}
		a.audit(r, "user.create", username, "", false)
		a.fail(w, r, err)
		return
	}

	a.audit(r, "user.create", username, string(role), true)

	created, err := a.Store.GetUser(ctx, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, usrView{User: created})
}

// handleUpdateUser edits an account. Every field is optional, including the
// password, which is reset rather than verified because an administrator is
// making the change.
func (a *API) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	caller := currentUser(r)
	id, err := idParam(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var req usrUpdateRequest
	if err := decode(r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	ctx := r.Context()

	target, err := a.Store.GetUser(ctx, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	self := caller.ID == target.ID
	if target.IsAdmin() && !caller.IsAdmin() {
		a.audit(r, "user.update", target.Username, "administrator account", false)
		a.fail(w, r, apiError(http.StatusForbidden, "forbidden",
			"Only an administrator can change an administrator account"))
		return
	}

	changed := make([]string, 0, 8)

	if req.Username != nil {
		name := strings.TrimSpace(*req.Username)
		if !strings.EqualFold(name, target.Username) {
			if err := usrCheckName(name); err != nil {
				a.fail(w, r, err)
				return
			}
			if _, err := a.Store.GetUserByName(ctx, name); err == nil {
				a.fail(w, r, conflict("That username is already taken"))
				return
			} else if !errors.Is(err, store.ErrNotFound) {
				a.fail(w, r, err)
				return
			}
			target.Username = name
			changed = append(changed, "username")
		}
	}
	if req.DisplayName != nil {
		target.DisplayName = strings.TrimSpace(*req.DisplayName)
		changed = append(changed, "displayName")
	}
	if req.Email != nil {
		target.Email = strings.TrimSpace(*req.Email)
		changed = append(changed, "email")
	}
	if req.Quota != nil {
		quota := *req.Quota
		if quota < 0 {
			quota = 0
		}
		target.Quota = quota
		changed = append(changed, "quota")
	}

	// The role decides the default permission set, so it is resolved before
	// the permissions themselves.
	role := target.Role
	if req.Role != nil {
		parsed, err := usrParseRole(*req.Role)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if parsed != role {
			if parsed == store.RoleAdmin && !caller.IsAdmin() {
				a.fail(w, r, apiError(http.StatusForbidden, "forbidden",
					"Only an administrator can promote an account to administrator"))
				return
			}
			if target.IsAdmin() {
				if self {
					a.fail(w, r, conflict("You cannot remove your own administrator role"))
					return
				}
				if err := a.usrGuardLastAdmin(ctx, target); err != nil {
					a.fail(w, r, err)
					return
				}
			}
			role = parsed
			target.Role = parsed
			changed = append(changed, "role")
		}
	}

	if req.Permissions != nil {
		perms, err := usrEffectivePermissions(role, *req.Permissions, true)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if err := usrCheckGrant(caller, perms); err != nil {
			a.audit(r, "user.update", target.Username, "permission not held by caller", false)
			a.fail(w, r, err)
			return
		}
		target.Permissions = perms
		changed = append(changed, "permissions")
	} else if req.Role != nil && role != store.RoleCustom {
		// A role change without an explicit permission list adopts the
		// defaults of the new role.
		perms := store.PermissionsForRole(role)
		if err := usrCheckGrant(caller, perms); err != nil {
			a.fail(w, r, err)
			return
		}
		target.Permissions = perms
	}

	deactivating := false
	if req.Active != nil && *req.Active != target.Active {
		if !*req.Active {
			if self {
				a.fail(w, r, conflict("You cannot deactivate your own account"))
				return
			}
			if target.IsAdmin() {
				if err := a.usrGuardLastAdmin(ctx, target); err != nil {
					a.fail(w, r, err)
					return
				}
			}
			deactivating = true
		}
		target.Active = *req.Active
		changed = append(changed, "active")
	}

	if req.MustChangePassword != nil {
		target.MustChangePassword = *req.MustChangePassword
		changed = append(changed, "mustChangePassword")
	}

	var mounts []store.Mount
	if req.Mounts != nil {
		mounts, err = a.usrResolveMounts(ctx, *req.Mounts)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		changed = append(changed, "mounts")
	}

	var hash string
	if req.Password != nil {
		if err := auth.ValidatePassword(*req.Password); err != nil {
			a.fail(w, r, usrPasswordError(err))
			return
		}
		hash, err = auth.HashPassword(*req.Password)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		changed = append(changed, "password")
	}

	if len(changed) == 0 {
		writeJSON(w, http.StatusOK, a.usrWithSessions(ctx, target))
		return
	}

	if err := a.Store.UpdateUser(ctx, target); err != nil {
		if errors.Is(err, store.ErrConflict) {
			a.fail(w, r, conflict("That username is already taken"))
			return
		}
		a.audit(r, "user.update", target.Username, strings.Join(changed, ","), false)
		a.fail(w, r, err)
		return
	}
	if req.Mounts != nil {
		if err := a.Store.SetUserMounts(ctx, target.ID, mounts); err != nil {
			a.fail(w, r, err)
			return
		}
	}
	if hash != "" {
		if err := a.Store.SetPassword(ctx, target.ID, hash, target.MustChangePassword); err != nil {
			a.fail(w, r, err)
			return
		}
	}

	// A new password or a suspended account must not leave a signed in browser
	// working. The session making the change is kept when it belongs to the
	// same account, so an administrator editing themselves stays signed in.
	if hash != "" || deactivating {
		keep := ""
		if self {
			if sess := currentSession(r); sess != nil {
				keep = sess.ID
			}
		}
		if keep != "" {
			if err := a.Store.DeleteOtherUserSessions(ctx, target.ID, keep); err != nil {
				a.Logger.Warn("revoke sessions failed", "user", target.ID, "err", err)
			}
		} else if err := a.Store.DeleteUserSessions(ctx, target.ID); err != nil {
			a.Logger.Warn("revoke sessions failed", "user", target.ID, "err", err)
		}
	}

	a.audit(r, "user.update", target.Username, strings.Join(changed, ","), true)

	updated, err := a.Store.GetUser(ctx, target.ID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, a.usrWithSessions(ctx, updated))
}

// handleDeleteUser removes an account together with everything that belongs
// to it.
func (a *API) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	caller := currentUser(r)
	id, err := idParam(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ctx := r.Context()

	target, err := a.Store.GetUser(ctx, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if caller.ID == target.ID {
		a.fail(w, r, conflict("You cannot delete your own account"))
		return
	}
	if target.IsAdmin() && !caller.IsAdmin() {
		a.audit(r, "user.delete", target.Username, "administrator account", false)
		a.fail(w, r, apiError(http.StatusForbidden, "forbidden",
			"Only an administrator can delete an administrator account"))
		return
	}
	if target.IsAdmin() {
		if err := a.usrGuardLastAdmin(ctx, target); err != nil {
			a.fail(w, r, err)
			return
		}
	}

	if err := a.Store.DeleteUserSessions(ctx, target.ID); err != nil {
		a.Logger.Warn("delete sessions failed", "user", target.ID, "err", err)
	}
	if shares, err := a.Store.ListShares(ctx, target.ID); err == nil {
		for _, sh := range shares {
			if err := a.Store.DeleteShare(ctx, sh.ID); err != nil {
				a.Logger.Warn("delete share failed", "share", sh.ID, "err", err)
			}
		}
	}
	if favorites, err := a.Store.ListFavorites(ctx, target.ID); err == nil {
		for _, f := range favorites {
			if err := a.Store.RemoveFavorite(ctx, target.ID, f.Path); err != nil &&
				!errors.Is(err, store.ErrNotFound) {
				a.Logger.Warn("remove favorite failed", "user", target.ID, "err", err)
			}
		}
	}
	if err := a.Store.TrimRecents(ctx, target.ID, 0); err != nil {
		a.Logger.Warn("clear recents failed", "user", target.ID, "err", err)
	}
	// Recycle bin rows do not follow the account away on their own, so the
	// stored copies would sit on disk forever.
	a.usrPurgeTrash(ctx, target.ID)

	if err := a.Store.DeleteUser(ctx, target.ID); err != nil {
		a.audit(r, "user.delete", target.Username, "", false)
		a.fail(w, r, err)
		return
	}
	a.audit(r, "user.delete", target.Username, "", true)
	writeOK(w)
}

// ---- helpers ----------------------------------------------------------------

// usrWithSessions decorates an account with its live session count.
func (a *API) usrWithSessions(ctx context.Context, u *store.User) usrView {
	view := usrView{User: u}
	if sessions, err := a.Store.ListUserSessions(ctx, u.ID); err == nil {
		view.Sessions = len(sessions)
	}
	return view
}

// usrCheckName validates the username shape.
func usrCheckName(name string) error {
	if name == "" {
		return badRequest("Enter a username")
	}
	if !usrNamePattern.MatchString(name) {
		return badRequest("A username is 2 to 32 characters and may hold letters, digits, dots, dashes and underscores")
	}
	return nil
}

// usrParseRole validates the role, treating an empty value as the standard
// user role.
func usrParseRole(raw string) (store.Role, error) {
	value := store.Role(strings.ToLower(strings.TrimSpace(raw)))
	if value == "" {
		return store.RoleUser, nil
	}
	if !value.Valid() {
		return "", badRequest("That role is not one Storix knows")
	}
	return value, nil
}

// usrPasswordError turns a policy failure into a plain message.
func usrPasswordError(err error) error {
	switch {
	case errors.Is(err, auth.ErrPasswordBlank):
		return badRequest("Enter a password")
	case errors.Is(err, auth.ErrPasswordTooShort):
		return badRequest("Use at least 8 characters for the password")
	case errors.Is(err, auth.ErrPasswordCommon):
		return badRequest("That password is too easy to guess, choose another one")
	}
	return badRequest("That password cannot be used")
}

// usrEffectivePermissions resolves what an account ends up holding. An absent
// list falls back to the defaults of the role, which a custom role does not
// have.
func usrEffectivePermissions(role store.Role, requested []store.Permission, provided bool) ([]store.Permission, error) {
	if !provided {
		if role == store.RoleCustom {
			return nil, badRequest("Choose at least one permission for a custom role")
		}
		return store.PermissionsForRole(role), nil
	}
	perms := store.NormalizePermissions(requested)
	if len(perms) == 0 {
		if role == store.RoleCustom {
			return nil, badRequest("Choose at least one permission for a custom role")
		}
		return store.PermissionsForRole(role), nil
	}
	return perms, nil
}

// usrCheckGrant refuses a caller who tries to hand out a capability they do
// not hold themselves. Administrators may grant anything.
func usrCheckGrant(caller *store.User, perms []store.Permission) error {
	if caller.IsAdmin() {
		return nil
	}
	for _, p := range perms {
		if !caller.Can(p) {
			return apiError(http.StatusForbidden, "forbidden",
				"You cannot grant the "+string(p)+" permission because you do not have it yourself")
		}
	}
	return nil
}

// usrGuardLastAdmin refuses a change that would leave no administrator able to
// sign in.
func (a *API) usrGuardLastAdmin(ctx context.Context, target *store.User) error {
	if !target.IsAdmin() || !target.Active {
		return nil
	}
	count, err := a.Store.CountAdmins(ctx)
	if err != nil {
		return err
	}
	if count <= 1 {
		return conflict("This is the last active administrator, promote another account first")
	}
	return nil
}

// usrResolveMounts validates the folders assigned to an account. Each one has
// to sit inside a configured storage location, and it is created when the
// caller asked for that.
func (a *API) usrResolveMounts(ctx context.Context, in []usrMountInput) ([]store.Mount, error) {
	out := make([]store.Mount, 0, len(in))
	if len(in) == 0 {
		return out, nil
	}
	roots, err := a.Store.ListRoots(ctx)
	if err != nil {
		return nil, err
	}
	if len(roots) == 0 {
		return nil, badRequest("No storage location is configured yet, add one in settings first")
	}

	scope := vfs.Scope{Admin: true, Mounts: make([]vfs.Mount, 0, len(roots))}
	for _, root := range roots {
		scope.Mounts = append(scope.Mounts, vfs.Mount{
			Path:     root.Path,
			Label:    root.Label,
			Icon:     root.Icon,
			ReadOnly: root.ReadOnly,
		})
	}

	seen := make(map[string]bool, len(in))
	for i, m := range in {
		p := vfs.Clean(m.Path)
		if p == "" {
			return nil, badRequest("Every folder needs a path")
		}
		if seen[p] {
			continue
		}
		seen[p] = true

		inside := false
		for _, root := range roots {
			if vfs.Contains(root.Path, p) {
				inside = true
				break
			}
		}
		if !inside {
			return nil, badRequest("The folder " + p + " is outside every storage location you configured")
		}
		if a.VFS.Denied(p) {
			return nil, badRequest("The folder " + p + " is protected and cannot be assigned")
		}

		if m.Create {
			if _, err := a.VFS.MkdirAll(scope, p); err != nil {
				return nil, err
			}
		} else if _, err := a.VFS.Stat(scope, p); err != nil {
			if errors.Is(err, vfs.ErrNotFound) {
				return nil, badRequest("The folder " + p + " does not exist, ask Storix to create it or pick another one")
			}
			return nil, err
		}

		label := strings.TrimSpace(m.Label)
		if label == "" {
			label = path.Base(p)
		}
		icon := strings.TrimSpace(m.Icon)
		if icon == "" {
			icon = "folder"
		}
		out = append(out, store.Mount{
			Path:      p,
			Label:     label,
			Icon:      icon,
			ReadOnly:  m.ReadOnly,
			SortOrder: i + 1,
		})
	}
	return out, nil
}

// usrPurgeTrash erases what an account left in the recycle bin. It is best
// effort: a file that resists removal is logged, never fatal to the delete.
func (a *API) usrPurgeTrash(ctx context.Context, userID int64) {
	items, err := a.Store.ClaimTrash(ctx, userID)
	if err != nil {
		a.Logger.Warn("claim trash failed", "user", userID, "err", err)
		return
	}
	for _, item := range items {
		record := vfs.TrashRecord{
			Name:         item.Name,
			OriginalPath: item.OriginalPath,
			StoredPath:   item.StoredPath,
			IsDir:        item.IsDir,
			Size:         item.Size,
		}
		if err := a.VFS.PurgeTrash(record); err != nil {
			a.Logger.Warn("purge trash failed", "user", userID, "item", item.ID, "err", err)
		}
	}
}
