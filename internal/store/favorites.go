package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

// favoriteSelect is the projection shared by every favorites query.
const favoriteSelect = `
SELECT id, user_id, path, name, is_dir, created_at
FROM favorites`

// recentSelect is the projection shared by every recents query.
const recentSelect = `
SELECT id, user_id, path, name, is_dir, size, action, at
FROM recents`

// scanFavorite reads one row of favoriteSelect.
func scanFavorite(sc interface{ Scan(dest ...any) error }) (*Favorite, error) {
	var (
		f       Favorite
		isDir   int
		created int64
	)
	if err := sc.Scan(&f.ID, &f.UserID, &f.Path, &f.Name, &isDir, &created); err != nil {
		return nil, err
	}
	f.IsDir = isDir != 0
	f.CreatedAt = fromTS(created)
	return &f, nil
}

// scanRecent reads one row of recentSelect.
func scanRecent(sc interface{ Scan(dest ...any) error }) (*Recent, error) {
	var (
		r     Recent
		isDir int
		at    int64
	)
	if err := sc.Scan(&r.ID, &r.UserID, &r.Path, &r.Name, &isDir, &r.Size, &r.Action, &at); err != nil {
		return nil, err
	}
	r.IsDir = isDir != 0
	r.At = fromTS(at)
	return &r, nil
}

// AddFavorite pins a location for a user. It is idempotent: pinning the same
// path again keeps the original identifier and pin time, and only refreshes the
// label, which is what a rename in another window should do. The identifier and
// the stored pin time are written back into f.
func (s *Store) AddFavorite(ctx context.Context, f *Favorite) (int64, error) {
	if f == nil {
		return 0, errors.New("store: nil favorite")
	}
	if f.UserID == 0 {
		return 0, errors.New("store: favorite user is required")
	}
	if strings.TrimSpace(f.Path) == "" {
		return 0, errors.New("store: favorite path is required")
	}
	if f.Name == "" {
		f.Name = path.Base(f.Path)
	}
	if f.CreatedAt.IsZero() {
		f.CreatedAt = time.Now().UTC()
	}
	var (
		id      int64
		created int64
	)
	err := s.db.QueryRowContext(ctx, `
INSERT INTO favorites (user_id, path, name, is_dir, created_at)
VALUES (?,?,?,?,?)
ON CONFLICT(user_id, path) DO UPDATE SET name = excluded.name, is_dir = excluded.is_dir
RETURNING id, created_at`,
		f.UserID, f.Path, f.Name, boolToInt(f.IsDir), ts(f.CreatedAt)).Scan(&id, &created)
	if err != nil {
		return 0, fmt.Errorf("store: add favorite: %w", err)
	}
	f.ID = id
	f.CreatedAt = fromTS(created)
	return id, nil
}

// RemoveFavorite unpins a location. ErrNotFound means it was not pinned, which
// a caller that only wants the end state can ignore.
func (s *Store) RemoveFavorite(ctx context.Context, userID int64, p string) error {
	res, err := s.db.ExecContext(ctx,
		"DELETE FROM favorites WHERE user_id = ? AND path = ?", userID, p)
	if err != nil {
		return fmt.Errorf("store: remove favorite: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: remove favorite: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListFavorites returns the pinned locations of a user in label order, so the
// sidebar keeps a stable arrangement while pins come and go.
func (s *Store) ListFavorites(ctx context.Context, userID int64) ([]*Favorite, error) {
	rows, err := s.db.QueryContext(ctx, favoriteSelect+`
WHERE user_id = ?
ORDER BY name COLLATE NOCASE ASC, path ASC`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list favorites: %w", err)
	}
	defer rows.Close()

	out := make([]*Favorite, 0, 16)
	for rows.Next() {
		f, err := scanFavorite(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list favorites: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list favorites: %w", err)
	}
	return out, nil
}

// IsFavorite reports whether a user pinned a path.
func (s *Store) IsFavorite(ctx context.Context, userID int64, p string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx,
		"SELECT 1 FROM favorites WHERE user_id = ? AND path = ? LIMIT 1", userID, p).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("store: is favorite: %w", err)
	}
	return true, nil
}

// RenameFavorites rewrites the pins that point at a moved or renamed location,
// including the ones below it, so a pin survives the move. A userID of 0 covers
// every user, which is what a move performed by an administrator needs.
//
// Pins already sitting at the destination are dropped first: the move replaced
// whatever lived there, so those pins are stale, and keeping them would collide
// with the unique path per user.
func (s *Store) RenameFavorites(ctx context.Context, userID int64, oldPrefix, newPrefix string) error {
	oldExact, oldLike := subtreeArgs(oldPrefix)
	newExact, newLike := subtreeArgs(newPrefix)
	if oldExact == "" || newExact == "" || oldExact == newExact {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: rename favorites: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
DELETE FROM favorites
 WHERE (? = 0 OR user_id = ?)
   AND (path = ? OR path LIKE ? ESCAPE '\')`, userID, userID, newExact, newLike); err != nil {
		return fmt.Errorf("store: rename favorites: %w", err)
	}

	// length() counts characters the same way substr() does, so the rewrite is
	// correct for paths outside ASCII as well.
	if _, err := tx.ExecContext(ctx, `
UPDATE favorites
   SET path = ? || substr(path, length(?) + 1)
 WHERE (? = 0 OR user_id = ?)
   AND (path = ? OR path LIKE ? ESCAPE '\')`,
		newExact, oldExact, userID, userID, oldExact, oldLike); err != nil {
		return fmt.Errorf("store: rename favorites: %w", err)
	}

	// The moved item itself carries a new label; the pins below it keep theirs.
	if _, err := tx.ExecContext(ctx, `
UPDATE favorites SET name = ? WHERE (? = 0 OR user_id = ?) AND path = ?`,
		path.Base(newExact), userID, userID, newExact); err != nil {
		return fmt.Errorf("store: rename favorites: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: rename favorites: %w", err)
	}
	return nil
}

// TouchRecent records that a user opened or changed a file. One row is kept per
// path, so revisiting a file moves it back to the top of the list instead of
// filling the history with duplicates. The identifier and the stored time are
// written back into r.
func (s *Store) TouchRecent(ctx context.Context, r *Recent) error {
	if r == nil {
		return errors.New("store: nil recent")
	}
	if r.UserID == 0 {
		return errors.New("store: recent user is required")
	}
	if strings.TrimSpace(r.Path) == "" {
		return errors.New("store: recent path is required")
	}
	if r.Name == "" {
		r.Name = path.Base(r.Path)
	}
	if r.Action == "" {
		r.Action = "open"
	}
	if r.At.IsZero() {
		r.At = time.Now().UTC()
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `
INSERT INTO recents (user_id, path, name, is_dir, size, action, at)
VALUES (?,?,?,?,?,?,?)
ON CONFLICT(user_id, path) DO UPDATE SET
    name = excluded.name, is_dir = excluded.is_dir, size = excluded.size,
    action = excluded.action, at = excluded.at
RETURNING id`,
		r.UserID, r.Path, r.Name, boolToInt(r.IsDir), r.Size, r.Action, ts(r.At)).Scan(&id)
	if err != nil {
		return fmt.Errorf("store: touch recent: %w", err)
	}
	r.ID = id
	return nil
}

// ListRecents returns the history of a user, newest first. A userID of 0 covers
// every user, and a limit of 0 or less returns everything.
func (s *Store) ListRecents(ctx context.Context, userID int64, limit int) ([]*Recent, error) {
	if limit <= 0 {
		// SQLite treats a negative LIMIT as no bound at all.
		limit = -1
	}
	rows, err := s.db.QueryContext(ctx, recentSelect+`
WHERE (? = 0 OR user_id = ?)
ORDER BY at DESC, id DESC
LIMIT ?`, userID, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list recents: %w", err)
	}
	defer rows.Close()

	out := make([]*Recent, 0, 16)
	for rows.Next() {
		r, err := scanRecent(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list recents: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list recents: %w", err)
	}
	return out, nil
}

// RemoveRecent drops one history entry. ErrNotFound means there was no such
// entry, which a caller that only wants the end state can ignore.
func (s *Store) RemoveRecent(ctx context.Context, userID int64, p string) error {
	res, err := s.db.ExecContext(ctx,
		"DELETE FROM recents WHERE user_id = ? AND path = ?", userID, p)
	if err != nil {
		return fmt.Errorf("store: remove recent: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: remove recent: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// TrimRecents caps the history at the newest keep entries per user and drops
// the rest. A keep of 0 or less clears the history. A userID of 0 applies the
// cap to every user, each against its own window.
func (s *Store) TrimRecents(ctx context.Context, userID int64, keep int) error {
	if keep <= 0 {
		if _, err := s.db.ExecContext(ctx,
			"DELETE FROM recents WHERE (? = 0 OR user_id = ?)", userID, userID); err != nil {
			return fmt.Errorf("store: trim recents: %w", err)
		}
		return nil
	}
	if userID != 0 {
		return s.trimRecentsFor(ctx, userID, keep)
	}
	ids, err := s.recentUserIDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.trimRecentsFor(ctx, id, keep); err != nil {
			return err
		}
	}
	return nil
}

// recentUserIDs lists the users that currently hold history rows.
func (s *Store) recentUserIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT DISTINCT user_id FROM recents")
	if err != nil {
		return nil, fmt.Errorf("store: trim recents: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: trim recents: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: trim recents: %w", err)
	}
	return ids, nil
}

// trimRecentsFor caps the history of a single user.
func (s *Store) trimRecentsFor(ctx context.Context, userID int64, keep int) error {
	_, err := s.db.ExecContext(ctx, `
DELETE FROM recents
 WHERE user_id = ?
   AND id NOT IN (
       SELECT id FROM recents WHERE user_id = ? ORDER BY at DESC, id DESC LIMIT ?
   )`, userID, userID, keep)
	if err != nil {
		return fmt.Errorf("store: trim recents: %w", err)
	}
	return nil
}
