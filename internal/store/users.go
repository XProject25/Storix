package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// userColumns lists the users table in the order scanUser expects.
const userColumns = `id, username, display_name, email, password_hash, role, permissions, ` +
	`totp_secret, totp_enabled, active, must_change_password, theme, locale, quota, ` +
	`failed_logins, locked_until, last_login_at, last_login_ip, created_at, updated_at`

// mountColumns lists the user_mounts table in the order scanMount expects.
const mountColumns = `id, user_id, path, label, icon, read_only, sort_order, created_at`

// scanUser reads one users row. The parameter is satisfied by both *sql.Row
// and *sql.Rows.
func scanUser(sc interface{ Scan(dest ...any) error }) (*User, error) {
	var (
		u                    User
		perms                string
		totpEnabled          int
		active               int
		mustChange           int
		lockedUntil          sql.NullInt64
		lastLoginAt          sql.NullInt64
		createdAt, updatedAt int64
	)
	err := sc.Scan(
		&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.PasswordHash, &u.Role, &perms,
		&u.TOTPSecret, &totpEnabled, &active, &mustChange, &u.Theme, &u.Locale, &u.Quota,
		&u.FailedLogins, &lockedUntil, &lastLoginAt, &u.LastLoginIP, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	u.Permissions = splitPermissions(perms)
	u.TOTPEnabled = totpEnabled != 0
	u.Active = active != 0
	u.MustChangePassword = mustChange != 0
	u.LockedUntil = fromNullTS(lockedUntil)
	u.LastLoginAt = fromNullTS(lastLoginAt)
	u.CreatedAt = fromTS(createdAt)
	u.UpdatedAt = fromTS(updatedAt)
	u.Mounts = make([]Mount, 0)
	return &u, nil
}

// scanMount reads one user_mounts row.
func scanMount(sc interface{ Scan(dest ...any) error }) (Mount, error) {
	var (
		m         Mount
		readOnly  int
		createdAt int64
	)
	if err := sc.Scan(&m.ID, &m.UserID, &m.Path, &m.Label, &m.Icon, &readOnly, &m.SortOrder, &createdAt); err != nil {
		return Mount{}, err
	}
	m.ReadOnly = readOnly != 0
	m.CreatedAt = fromTS(createdAt)
	return m, nil
}

// normalizeUser trims free text and applies the defaults the schema declares,
// so a value written by Storix reads back identically.
func normalizeUser(u *User) {
	u.Username = strings.TrimSpace(u.Username)
	u.DisplayName = strings.TrimSpace(u.DisplayName)
	u.Email = strings.TrimSpace(u.Email)
	u.LastLoginIP = strings.TrimSpace(u.LastLoginIP)
	if u.Role == "" {
		u.Role = RoleUser
	}
	if u.Theme == "" {
		u.Theme = "dark"
	}
	if u.Locale == "" {
		u.Locale = "en"
	}
	if u.Quota < 0 {
		u.Quota = 0
	}
	u.Permissions = NormalizePermissions(u.Permissions)
}

// CreateUser inserts an account and returns its identifier. CreatedAt and
// UpdatedAt are set to the current time, a blank role becomes RoleUser and any
// mounts carried on the struct are inserted in the same transaction. A
// username already in use returns ErrConflict.
func (s *Store) CreateUser(ctx context.Context, u *User) (int64, error) {
	if u == nil {
		return 0, errors.New("store: create user: nil user")
	}
	normalizeUser(u)
	if u.Username == "" {
		return 0, errors.New("store: create user: empty username")
	}
	now := time.Now().UTC().Truncate(time.Second)
	u.CreatedAt = now
	u.UpdatedAt = now

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: create user: begin: %w", err)
	}
	// Rollback after a successful commit is a no-op, so the deferred call is
	// safe on every path.
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO users (
			username, display_name, email, password_hash, role, permissions,
			totp_secret, totp_enabled, active, must_change_password, theme, locale,
			quota, failed_logins, locked_until, last_login_at, last_login_ip,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.Username, u.DisplayName, u.Email, u.PasswordHash, string(u.Role),
		joinPermissions(u.Permissions), u.TOTPSecret, boolToInt(u.TOTPEnabled),
		boolToInt(u.Active), boolToInt(u.MustChangePassword), u.Theme, u.Locale,
		u.Quota, u.FailedLogins, nullTS(u.LockedUntil), nullTS(u.LastLoginAt),
		u.LastLoginIP, ts(now), ts(now),
	)
	if err != nil {
		if isUnique(err) {
			return 0, ErrConflict
		}
		return 0, fmt.Errorf("store: create user: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: create user: id: %w", err)
	}
	if len(u.Mounts) > 0 {
		if err := replaceUserMounts(ctx, tx, id, u.Mounts, now); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: create user: commit: %w", err)
	}
	u.ID = id
	return id, nil
}

// UpdateUser writes every mutable column except password_hash and totp_secret,
// which have dedicated setters, and bumps updated_at. It returns ErrNotFound
// when the account is gone and ErrConflict when the username is taken.
func (s *Store) UpdateUser(ctx context.Context, u *User) error {
	if u == nil {
		return errors.New("store: update user: nil user")
	}
	if u.ID <= 0 {
		return ErrNotFound
	}
	normalizeUser(u)
	if u.Username == "" {
		return errors.New("store: update user: empty username")
	}
	now := time.Now().UTC().Truncate(time.Second)
	res, err := s.db.ExecContext(ctx, `
		UPDATE users SET
			username = ?, display_name = ?, email = ?, role = ?, permissions = ?,
			totp_enabled = ?, active = ?, must_change_password = ?, theme = ?,
			locale = ?, quota = ?, failed_logins = ?, locked_until = ?,
			last_login_at = ?, last_login_ip = ?, updated_at = ?
		WHERE id = ?`,
		u.Username, u.DisplayName, u.Email, string(u.Role), joinPermissions(u.Permissions),
		boolToInt(u.TOTPEnabled), boolToInt(u.Active), boolToInt(u.MustChangePassword),
		u.Theme, u.Locale, u.Quota, u.FailedLogins, nullTS(u.LockedUntil),
		nullTS(u.LastLoginAt), u.LastLoginIP, ts(now), u.ID,
	)
	if err != nil {
		if isUnique(err) {
			return ErrConflict
		}
		return fmt.Errorf("store: update user %d: %w", u.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update user %d: %w", u.ID, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	u.UpdatedAt = now
	return nil
}

// DeleteUser removes an account. Mounts, sessions, shares, favorites and
// recents cascade with it. Missing accounts return ErrNotFound.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete user %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete user %d: %w", id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetUser loads one account with its mounts, or ErrNotFound.
func (s *Store) GetUser(ctx context.Context, id int64) (*User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id)
	u, err := scanUser(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: get user %d: %w", id, err)
	}
	mounts, err := s.ListUserMounts(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	u.Mounts = mounts
	return u, nil
}

// GetUserByName loads one account by username with its mounts. The lookup is
// case insensitive, matching the unique index. Missing accounts return
// ErrNotFound.
func (s *Store) GetUserByName(ctx context.Context, username string) (*User, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE username = ? COLLATE NOCASE`, username)
	u, err := scanUser(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: get user %q: %w", username, err)
	}
	mounts, err := s.ListUserMounts(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	u.Mounts = mounts
	return u, nil
}

// ListUsers returns every account ordered by username, each with its mounts
// loaded. Mounts are fetched in a single extra query rather than one per user.
func (s *Store) ListUsers(ctx context.Context) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+userColumns+` FROM users ORDER BY username COLLATE NOCASE`)
	if err != nil {
		return nil, fmt.Errorf("store: list users: %w", err)
	}
	defer func() { _ = rows.Close() }()

	users := make([]*User, 0, 16)
	byID := make(map[int64]*User)
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list users: %w", err)
		}
		users = append(users, u)
		byID[u.ID] = u
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list users: %w", err)
	}
	if len(users) == 0 {
		return users, nil
	}

	mountRows, err := s.db.QueryContext(ctx,
		`SELECT `+mountColumns+` FROM user_mounts ORDER BY user_id, sort_order, path`)
	if err != nil {
		return nil, fmt.Errorf("store: list users: mounts: %w", err)
	}
	defer func() { _ = mountRows.Close() }()
	for mountRows.Next() {
		m, err := scanMount(mountRows)
		if err != nil {
			return nil, fmt.Errorf("store: list users: mounts: %w", err)
		}
		if u, ok := byID[m.UserID]; ok {
			u.Mounts = append(u.Mounts, m)
		}
	}
	if err := mountRows.Err(); err != nil {
		return nil, fmt.Errorf("store: list users: mounts: %w", err)
	}
	return users, nil
}

// CountUsers reports how many accounts exist, active or not.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count users: %w", err)
	}
	return n, nil
}

// CountAdmins reports how many active administrators exist. Callers use it to
// refuse the change that would lock everyone out of the panel.
func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE role = ? AND active = 1`, string(RoleAdmin)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count admins: %w", err)
	}
	return n, nil
}

// SetPassword stores an already hashed password and the flag that forces a
// change at next sign in. Missing accounts return ErrNotFound.
func (s *Store) SetPassword(ctx context.Context, id int64, hash string, mustChange bool) error {
	if hash == "" {
		return errors.New("store: set password: empty hash")
	}
	return s.updateUserRow(ctx, id, `password_hash = ?, must_change_password = ?`,
		hash, boolToInt(mustChange))
}

// SetTOTP stores the shared secret and whether two factor authentication is
// active. Passing an empty secret with enabled false clears enrolment.
func (s *Store) SetTOTP(ctx context.Context, id int64, secret string, enabled bool) error {
	if enabled && secret == "" {
		return errors.New("store: set totp: cannot enable without a secret")
	}
	return s.updateUserRow(ctx, id, `totp_secret = ?, totp_enabled = ?`, secret, boolToInt(enabled))
}

// SetUserActive enables or suspends an account. Suspending also clears the
// lockout counters, so a reinstated account starts clean.
func (s *Store) SetUserActive(ctx context.Context, id int64, active bool) error {
	return s.updateUserRow(ctx, id,
		`active = ?, failed_logins = 0, locked_until = NULL`, boolToInt(active))
}

// RecordLogin marks a successful sign in and clears the failure counter and
// any lockout.
func (s *Store) RecordLogin(ctx context.Context, id int64, ip string) error {
	now := time.Now().UTC().Truncate(time.Second)
	return s.updateUserRow(ctx, id,
		`last_login_at = ?, last_login_ip = ?, failed_logins = 0, locked_until = NULL`,
		ts(now), strings.TrimSpace(ip))
}

// RecordFailedLogin increments the failure counter and locks the account for
// lockFor once the counter reaches threshold. It reports whether the account
// is now locked. A threshold or duration of zero disables locking and only
// counts the attempt.
func (s *Store) RecordFailedLogin(ctx context.Context, id int64, threshold int, lockFor time.Duration) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("store: failed login %d: begin: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()

	var failed int
	err = tx.QueryRowContext(ctx, `SELECT failed_logins FROM users WHERE id = ?`, id).Scan(&failed)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, fmt.Errorf("store: failed login %d: %w", id, err)
	}
	failed++

	locked := false
	// A nil binding leaves any lock already in place untouched.
	var lockedUntil any
	if threshold > 0 && lockFor > 0 && failed >= threshold {
		locked = true
		lockedUntil = time.Now().UTC().Add(lockFor).Truncate(time.Second).Unix()
	}
	now := time.Now().UTC().Truncate(time.Second)
	_, err = tx.ExecContext(ctx, `
		UPDATE users SET failed_logins = ?, locked_until = COALESCE(?, locked_until), updated_at = ?
		WHERE id = ?`, failed, lockedUntil, ts(now), id)
	if err != nil {
		return false, fmt.Errorf("store: failed login %d: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("store: failed login %d: commit: %w", id, err)
	}
	return locked, nil
}

// ListUserMounts returns the directories an account may work in, ordered for
// display.
func (s *Store) ListUserMounts(ctx context.Context, userID int64) ([]Mount, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+mountColumns+` FROM user_mounts WHERE user_id = ? ORDER BY sort_order, path`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list mounts for %d: %w", userID, err)
	}
	defer func() { _ = rows.Close() }()

	mounts := make([]Mount, 0, 4)
	for rows.Next() {
		m, err := scanMount(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list mounts for %d: %w", userID, err)
		}
		mounts = append(mounts, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list mounts for %d: %w", userID, err)
	}
	return mounts, nil
}

// SetUserMounts replaces the whole mount set of an account inside a single
// transaction, so a failure leaves the previous set intact. Entries with an
// empty path and repeats of a path already in the slice are skipped, a blank
// icon becomes "folder" and a sort order left at zero takes the slice
// position. The passed slice is updated in place with the assigned ids.
func (s *Store) SetUserMounts(ctx context.Context, userID int64, mounts []Mount) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: set mounts for %d: begin: %w", userID, err)
	}
	defer func() { _ = tx.Rollback() }()

	var exists int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id = ?`, userID).Scan(&exists)
	if err != nil {
		return fmt.Errorf("store: set mounts for %d: %w", userID, err)
	}
	if exists == 0 {
		return ErrNotFound
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := replaceUserMounts(ctx, tx, userID, mounts, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: set mounts for %d: commit: %w", userID, err)
	}
	return nil
}

// replaceUserMounts swaps the mount set of one account within a transaction.
func replaceUserMounts(ctx context.Context, tx *sql.Tx, userID int64, mounts []Mount, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_mounts WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("store: clear mounts for %d: %w", userID, err)
	}
	if len(mounts) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO user_mounts (user_id, path, label, icon, read_only, sort_order, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("store: prepare mount insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	seen := make(map[string]bool, len(mounts))
	for i := range mounts {
		m := &mounts[i]
		m.Path = strings.TrimSpace(m.Path)
		if m.Path == "" || seen[m.Path] {
			continue
		}
		seen[m.Path] = true
		m.Label = strings.TrimSpace(m.Label)
		if m.Icon == "" {
			m.Icon = "folder"
		}
		if m.SortOrder == 0 {
			m.SortOrder = i
		}
		if m.CreatedAt.IsZero() {
			m.CreatedAt = now
		}
		res, err := stmt.ExecContext(ctx, userID, m.Path, m.Label, m.Icon,
			boolToInt(m.ReadOnly), m.SortOrder, ts(m.CreatedAt))
		if err != nil {
			return fmt.Errorf("store: add mount %s for %d: %w", m.Path, userID, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("store: add mount %s for %d: %w", m.Path, userID, err)
		}
		m.ID = id
		m.UserID = userID
	}
	return nil
}

// updateUserRow applies a partial column assignment and always bumps
// updated_at. It returns ErrNotFound when no row matched.
func (s *Store) updateUserRow(ctx context.Context, id int64, assignments string, args ...any) error {
	args = append(args, ts(time.Now().UTC().Truncate(time.Second)), id)
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET `+assignments+`, updated_at = ? WHERE id = ?`, args...)
	if err != nil {
		return fmt.Errorf("store: update user %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update user %d: %w", id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
