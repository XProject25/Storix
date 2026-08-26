// Per account storage figures. store.User.Quota is the allowance an operator
// sets; this file holds what the allowance is measured against.
//
// Walking a tree of a few million files takes seconds, far too long to do on
// the request that wants the number, so the figure is cached: a background
// measurement writes it, and the operations that add or remove bytes adjust it
// with a delta. The stamp says when the walk happened, which is what lets a
// caller decide the number has drifted far enough to be worth measuring again.
//
// Storix - modern web file manager for servers.
// Developed by X Project.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// quotaSchemaSQL is the second migration: the table holding the measured
// storage figure of every account. Installs that predate it get the table on
// the next start, empty, which reads as "never measured" and schedules a walk.
const quotaSchemaSQL = `
CREATE TABLE IF NOT EXISTS user_usage (
    user_id     INTEGER PRIMARY KEY,
    bytes       INTEGER NOT NULL DEFAULT 0,
    files       INTEGER NOT NULL DEFAULT 0,
    computed_at INTEGER NOT NULL DEFAULT 0
);`

// Usage is how much storage one account occupies. ComputedAt is the moment the
// figure was last measured by a full walk, and stays zero while no walk has
// ever finished, even after deltas have moved the counters.
type Usage struct {
	UserID     int64     `json:"userId"`
	Bytes      int64     `json:"bytes"`
	Files      int64     `json:"files"`
	ComputedAt time.Time `json:"computedAt"`
}

// GetUsage returns the stored figure for an account. An account nobody has
// measured yet is not an error: the zero value comes back with the identifier
// filled in, which a caller reads as "nothing known yet" and answers by
// scheduling a measurement.
func (s *Store) GetUsage(ctx context.Context, userID int64) (*Usage, error) {
	u := &Usage{UserID: userID}
	if userID == 0 {
		return u, nil
	}
	var computed int64
	err := s.db.QueryRowContext(ctx,
		"SELECT bytes, files, computed_at FROM user_usage WHERE user_id = ?", userID).
		Scan(&u.Bytes, &u.Files, &computed)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return u, nil
	case err != nil:
		return nil, fmt.Errorf("store: get usage: %w", err)
	}
	u.ComputedAt = fromTS(computed)
	return u, nil
}

// SetUsage records the result of a measurement, replacing whatever was there.
// A zero ComputedAt is stamped with the current time, since a caller storing a
// figure has just measured it.
func (s *Store) SetUsage(ctx context.Context, u Usage) error {
	if u.UserID == 0 {
		return errors.New("store: usage user is required")
	}
	if u.ComputedAt.IsZero() {
		u.ComputedAt = time.Now().UTC()
	}
	u.Bytes = max(u.Bytes, 0)
	u.Files = max(u.Files, 0)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO user_usage (user_id, bytes, files, computed_at)
VALUES (?,?,?,?)
ON CONFLICT(user_id) DO UPDATE SET
    bytes = excluded.bytes, files = excluded.files, computed_at = excluded.computed_at`,
		u.UserID, u.Bytes, u.Files, ts(u.ComputedAt))
	if err != nil {
		return fmt.Errorf("store: set usage: %w", err)
	}
	return nil
}

// AddUsage moves the figure by a delta, so a finished upload or a deletion is
// reflected at once without walking the tree again. Both counters are clamped
// at zero, because a delta that arrives after a rescan already accounted for
// the same change must not push the figure negative.
//
// The measurement stamp is deliberately left alone: a delta is an adjustment,
// not a measurement, and the stamp is what eventually earns a fresh walk that
// corrects the drift deltas cannot see, such as a file replaced in place.
func (s *Store) AddUsage(ctx context.Context, userID int64, deltaBytes, deltaFiles int64) error {
	if userID == 0 {
		return errors.New("store: usage user is required")
	}
	if deltaBytes == 0 && deltaFiles == 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO user_usage (user_id, bytes, files, computed_at)
VALUES (?,?,?,0)
ON CONFLICT(user_id) DO UPDATE SET
    bytes = MAX(0, user_usage.bytes + ?),
    files = MAX(0, user_usage.files + ?)`,
		userID, max(deltaBytes, 0), max(deltaFiles, 0), deltaBytes, deltaFiles)
	if err != nil {
		return fmt.Errorf("store: add usage: %w", err)
	}
	return nil
}

// ClearUsage forgets the figure of an account, for instance when the account
// is removed or its folders change. It is idempotent: an account that was
// never measured is not an error.
func (s *Store) ClearUsage(ctx context.Context, userID int64) error {
	if userID == 0 {
		return errors.New("store: usage user is required")
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM user_usage WHERE user_id = ?", userID); err != nil {
		return fmt.Errorf("store: clear usage: %w", err)
	}
	return nil
}
