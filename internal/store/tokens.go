// Credentials for programmatic access. A browser session lives in a cookie and
// is proved with a CSRF header, which no backup script and no WebDAV client can
// send, so an account can also mint long lived tokens and present them in the
// Authorization header instead.
//
// A token is split in two. The short prefix is stored in the clear, which is
// what turns a lookup into a single indexed query, and the secret behind it is
// kept only as a SHA-256 digest, exactly as sessions and share links are. A
// copy of the database therefore cannot be replayed against a running server,
// and the owner still recognises their own token by its prefix in the listing.
//
// Storix - modern web file manager for servers.
// Developed by X Project.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// tokenSchemaSQL is the third migration: the table holding the access tokens of
// every account. Installs that predate it get the table on the next start,
// empty, so nothing changes until an owner mints their first token.
const tokenSchemaSQL = `
CREATE TABLE IF NOT EXISTS api_tokens (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT    NOT NULL DEFAULT '',
    prefix       TEXT    NOT NULL,
    hash         TEXT    NOT NULL,
    scope        TEXT    NOT NULL DEFAULT 'read',
    expires_at   INTEGER,
    last_used_at INTEGER,
    last_used_ip TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_api_tokens_prefix ON api_tokens(prefix);
CREATE INDEX IF NOT EXISTS idx_api_tokens_user ON api_tokens(user_id);`

// TokenScope is how much of an account a token may use.
type TokenScope string

// Token scopes. A read token narrows the account it belongs to, a write token
// carries whatever the account itself may do. Neither ever widens it.
const (
	ScopeRead  TokenScope = "read"
	ScopeWrite TokenScope = "write"
)

// Valid reports whether the scope is one Storix knows.
func (s TokenScope) Valid() bool {
	switch s {
	case ScopeRead, ScopeWrite:
		return true
	}
	return false
}

// Label is the human readable scope name.
func (s TokenScope) Label() string {
	switch s {
	case ScopeRead:
		return "Read only"
	case ScopeWrite:
		return "Read and write"
	}
	return string(s)
}

// APIToken is one credential a script or a mounted drive signs in with. Hash is
// never serialized: the secret is shown once, at creation, and only its digest
// is kept. Expired is derived from ExpiresAt when the row is read, so a listing
// can mark a dead token without every caller repeating the comparison.
type APIToken struct {
	ID         int64      `json:"id"`
	UserID     int64      `json:"userId"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Hash       string     `json:"-"`
	Scope      TokenScope `json:"scope"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	LastUsedIP string     `json:"lastUsedIp,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	Expired    bool       `json:"expired"`
}

// tokColumns lists the api_tokens table in the order tokScan expects.
const tokColumns = `id, user_id, name, prefix, hash, scope, expires_at, last_used_at, last_used_ip, created_at`

// tokExpired reports whether an expiry stamp has passed. A token without one
// never expires.
func tokExpired(at *time.Time, now time.Time) bool {
	return at != nil && !at.IsZero() && now.After(*at)
}

// tokScan reads one api_tokens row. The parameter is satisfied by both *sql.Row
// and *sql.Rows.
func tokScan(sc interface{ Scan(dest ...any) error }) (*APIToken, error) {
	var (
		t        APIToken
		scope    string
		expires  sql.NullInt64
		lastUsed sql.NullInt64
		created  int64
	)
	err := sc.Scan(&t.ID, &t.UserID, &t.Name, &t.Prefix, &t.Hash, &scope,
		&expires, &lastUsed, &t.LastUsedIP, &created)
	if err != nil {
		return nil, err
	}
	t.Scope = TokenScope(scope)
	t.ExpiresAt = fromNullTS(expires)
	t.LastUsedAt = fromNullTS(lastUsed)
	t.CreatedAt = fromTS(created)
	t.Expired = tokExpired(t.ExpiresAt, time.Now())
	return &t, nil
}

// CreateToken stores a new credential and returns its identifier. The caller
// mints the secret and passes only its digest; a blank scope becomes ScopeRead
// and a missing creation time is stamped now. The prefix has to be unique: a
// collision reports ErrConflict so the caller can mint a fresh token and retry.
// The new identifier is written back into t.
func (s *Store) CreateToken(ctx context.Context, t *APIToken) (int64, error) {
	if t == nil {
		return 0, errors.New("store: nil token")
	}
	t.Name = strings.TrimSpace(t.Name)
	t.Prefix = strings.TrimSpace(t.Prefix)
	t.LastUsedIP = strings.TrimSpace(t.LastUsedIP)
	if t.UserID == 0 {
		return 0, errors.New("store: token user is required")
	}
	if t.Prefix == "" {
		return 0, errors.New("store: token prefix is required")
	}
	if t.Hash == "" {
		return 0, errors.New("store: token hash is required")
	}
	if t.Scope == "" {
		t.Scope = ScopeRead
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx, `
INSERT INTO api_tokens (user_id, name, prefix, hash, scope,
                        expires_at, last_used_at, last_used_ip, created_at)
VALUES (?,?,?,?,?,?,?,?,?)`,
		t.UserID, t.Name, t.Prefix, t.Hash, string(t.Scope),
		nullTS(t.ExpiresAt), nullTS(t.LastUsedAt), t.LastUsedIP, ts(t.CreatedAt))
	if err != nil {
		if isUnique(err) {
			return 0, ErrConflict
		}
		return 0, fmt.Errorf("store: create token: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: create token: %w", err)
	}
	t.ID = id
	t.Expired = tokExpired(t.ExpiresAt, time.Now())
	return id, nil
}

// ListTokens returns the credentials of one account, newest first. Tokens whose
// expiry has passed are listed too, marked Expired, so the owner can see what
// went stale and clear it out.
func (s *Store) ListTokens(ctx context.Context, userID int64) ([]*APIToken, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+tokColumns+`
FROM api_tokens WHERE user_id = ? ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list tokens for %d: %w", userID, err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]*APIToken, 0, 8)
	for rows.Next() {
		t, err := tokScan(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list tokens for %d: %w", userID, err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list tokens for %d: %w", userID, err)
	}
	return out, nil
}

// GetTokenByPrefix loads the credential a presented token opens with, reporting
// ErrNotFound when the prefix is unknown. Expiry is not evaluated here: the
// caller compares the secret first, so a wrong token and an expired one cost
// the same work.
func (s *Store) GetTokenByPrefix(ctx context.Context, prefix string) (*APIToken, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return nil, ErrNotFound
	}
	t, err := tokScan(s.db.QueryRowContext(ctx,
		`SELECT `+tokColumns+` FROM api_tokens WHERE prefix = ?`, prefix))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: get token by prefix: %w", err)
	}
	return t, nil
}

// TouchToken records that a credential was used and from where. Callers rate
// limit this rather than writing on every request, since a script can easily
// make thousands of calls a minute. A missing row reports ErrNotFound.
func (s *Store) TouchToken(ctx context.Context, id int64, at time.Time, ip string) error {
	if at.IsZero() {
		at = time.Now()
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE api_tokens SET last_used_at = ?, last_used_ip = ? WHERE id = ?`,
		at.UTC().Unix(), tokClampIP(ip), id)
	if err != nil {
		return fmt.Errorf("store: touch token %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: touch token %d: %w", id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteToken revokes one credential. The owner is part of the condition, so a
// caller can never revoke a token belonging to another account even though the
// identifiers are guessable. A token that is not there, or not theirs, reports
// ErrNotFound.
func (s *Store) DeleteToken(ctx context.Context, id, userID int64) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM api_tokens WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return fmt.Errorf("store: delete token %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete token %d: %w", id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteUserTokens revokes every credential of an account, for instance after a
// password change. It is idempotent: an account with no tokens is not an error.
func (s *Store) DeleteUserTokens(ctx context.Context, userID int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("store: delete tokens for %d: %w", userID, err)
	}
	return nil
}

// PurgeExpiredTokens removes credentials whose expiry has passed and reports
// how many went. Tokens without an expiry are left alone.
func (s *Store) PurgeExpiredTokens(ctx context.Context, now time.Time) (int64, error) {
	if now.IsZero() {
		now = time.Now()
	}
	res, err := s.db.ExecContext(ctx, `
DELETE FROM api_tokens WHERE expires_at IS NOT NULL AND expires_at > 0 AND expires_at < ?`, now.Unix())
	if err != nil {
		return 0, fmt.Errorf("store: purge expired tokens: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: purge expired tokens: %w", err)
	}
	return n, nil
}

// tokClampIP keeps a caller address inside the column, so a header carrying a
// long forwarded chain cannot bloat the row.
func tokClampIP(ip string) string {
	ip = strings.TrimSpace(ip)
	if len(ip) > 64 {
		return ip[:64]
	}
	return ip
}
