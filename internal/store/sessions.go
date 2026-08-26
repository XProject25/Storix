package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// sessionColumns lists the sessions table in the order scanSession expects.
const sessionColumns = `id, user_id, csrf, ip, user_agent, created_at, last_seen_at, expires_at`

// scanSession reads one sessions row. The parameter is satisfied by both
// *sql.Row and *sql.Rows.
func scanSession(sc interface{ Scan(dest ...any) error }) (*Session, error) {
	var (
		sess                             Session
		createdAt, lastSeenAt, expiresAt int64
	)
	err := sc.Scan(&sess.ID, &sess.UserID, &sess.CSRF, &sess.IP, &sess.UserAgent,
		&createdAt, &lastSeenAt, &expiresAt)
	if err != nil {
		return nil, err
	}
	sess.CreatedAt = fromTS(createdAt)
	sess.LastSeenAt = fromTS(lastSeenAt)
	sess.ExpiresAt = fromTS(expiresAt)
	return &sess, nil
}

// CreateSession stores a signed in browser session. CreatedAt and LastSeenAt
// default to the current time when left zero. A zero ExpiresAt means the
// session never expires on its own. A reused identifier returns ErrConflict.
func (s *Store) CreateSession(ctx context.Context, sess *Session) error {
	if sess == nil {
		return errors.New("store: create session: nil session")
	}
	if strings.TrimSpace(sess.ID) == "" {
		return errors.New("store: create session: empty id")
	}
	now := time.Now().UTC().Truncate(time.Second)
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = now
	}
	if sess.LastSeenAt.IsZero() {
		sess.LastSeenAt = sess.CreatedAt
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (id, user_id, csrf, ip, user_agent, created_at, last_seen_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.UserID, sess.CSRF, sess.IP, sess.UserAgent,
		ts(sess.CreatedAt), ts(sess.LastSeenAt), ts(sess.ExpiresAt),
	)
	if err != nil {
		if isUnique(err) {
			return ErrConflict
		}
		return fmt.Errorf("store: create session: %w", err)
	}
	return nil
}

// GetSession loads a live session. An unknown identifier and one that has
// already expired both return ErrNotFound, so callers cannot accidentally
// honour a stale cookie.
func (s *Store) GetSession(ctx context.Context, id string) (*Session, error) {
	if id == "" {
		return nil, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT `+sessionColumns+` FROM sessions WHERE id = ? AND (expires_at <= 0 OR expires_at > ?)`,
		id, time.Now().UTC().Unix())
	sess, err := scanSession(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: get session: %w", err)
	}
	return sess, nil
}

// TouchSession records activity on a session. When extendTo is non zero the
// expiry moves with it, which is how idle timeouts slide forward. Missing
// sessions return ErrNotFound.
func (s *Store) TouchSession(ctx context.Context, id string, at time.Time, extendTo time.Time) error {
	if id == "" {
		return ErrNotFound
	}
	if at.IsZero() {
		at = time.Now().UTC().Truncate(time.Second)
	}
	// A nil binding leaves the stored expiry untouched.
	var expires any
	if !extendTo.IsZero() {
		expires = extendTo.Unix()
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET last_seen_at = ?, expires_at = COALESCE(?, expires_at) WHERE id = ?`,
		ts(at), expires, id)
	if err != nil {
		return fmt.Errorf("store: touch session: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: touch session: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSession revokes one session. An unknown identifier returns ErrNotFound.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete session: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete session: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteUserSessions signs an account out everywhere. Deleting nothing is not
// an error.
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("store: delete sessions of %d: %w", userID, err)
	}
	return nil
}

// DeleteOtherUserSessions signs an account out everywhere except the session
// making the request, which is what a password change should do.
func (s *Store) DeleteOtherUserSessions(ctx context.Context, userID int64, keepID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id = ? AND id <> ?`, userID, keepID)
	if err != nil {
		return fmt.Errorf("store: delete other sessions of %d: %w", userID, err)
	}
	return nil
}

// ListUserSessions returns the live sessions of an account, most recently seen
// first. Expired rows are left out; PurgeExpiredSessions removes them.
func (s *Store) ListUserSessions(ctx context.Context, userID int64) ([]*Session, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+sessionColumns+` FROM sessions
		 WHERE user_id = ? AND (expires_at <= 0 OR expires_at > ?)
		 ORDER BY last_seen_at DESC`,
		userID, time.Now().UTC().Unix())
	if err != nil {
		return nil, fmt.Errorf("store: list sessions of %d: %w", userID, err)
	}
	defer func() { _ = rows.Close() }()

	sessions := make([]*Session, 0, 4)
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list sessions of %d: %w", userID, err)
		}
		sessions = append(sessions, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list sessions of %d: %w", userID, err)
	}
	return sessions, nil
}

// PurgeExpiredSessions deletes every session that expired at or before now and
// reports how many rows went. Sessions stored without an expiry are kept.
func (s *Store) PurgeExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE expires_at > 0 AND expires_at <= ?`, now.Unix())
	if err != nil {
		return 0, fmt.Errorf("store: purge sessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: purge sessions: %w", err)
	}
	return n, nil
}
