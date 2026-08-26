package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// trashSelect is the projection shared by every recycle bin query.
const trashSelect = `
SELECT id, user_id, name, original_path, stored_path, is_dir, size, deleted_at, expires_at
FROM trash_items`

// scanTrashItem reads one row of trashSelect.
func scanTrashItem(sc interface{ Scan(dest ...any) error }) (*TrashItem, error) {
	var (
		it      TrashItem
		isDir   int
		deleted int64
		expires int64
	)
	err := sc.Scan(&it.ID, &it.UserID, &it.Name, &it.OriginalPath, &it.StoredPath,
		&isDir, &it.Size, &deleted, &expires)
	if err != nil {
		return nil, err
	}
	it.IsDir = isDir != 0
	it.DeletedAt = fromTS(deleted)
	it.ExpiresAt = fromTS(expires)
	return &it, nil
}

// collectTrash drains a result set of trashSelect rows. The caller owns rows
// and has to close them.
func collectTrash(rows *sql.Rows) ([]*TrashItem, error) {
	out := make([]*TrashItem, 0, 16)
	for rows.Next() {
		it, err := scanTrashItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// AddTrashItem records a file or folder that moved into the recycle bin and
// returns its identifier, which is also written back into item. StoredPath is
// the location under the Storix trash directory that holds the moved data.
func (s *Store) AddTrashItem(ctx context.Context, item *TrashItem) (int64, error) {
	if item == nil {
		return 0, errors.New("store: nil trash item")
	}
	if strings.TrimSpace(item.StoredPath) == "" {
		return 0, errors.New("store: trash item stored path is required")
	}
	if item.DeletedAt.IsZero() {
		item.DeletedAt = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx, `
INSERT INTO trash_items (user_id, name, original_path, stored_path, is_dir, size, deleted_at, expires_at)
VALUES (?,?,?,?,?,?,?,?)`,
		item.UserID, item.Name, item.OriginalPath, item.StoredPath,
		boolToInt(item.IsDir), item.Size, ts(item.DeletedAt), ts(item.ExpiresAt))
	if err != nil {
		return 0, fmt.Errorf("store: add trash item: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: add trash item: %w", err)
	}
	item.ID = id
	return id, nil
}

// GetTrashItem loads one bin entry, reporting ErrNotFound when it is gone.
func (s *Store) GetTrashItem(ctx context.Context, id int64) (*TrashItem, error) {
	it, err := scanTrashItem(s.db.QueryRowContext(ctx, trashSelect+"\nWHERE id = ?", id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: get trash item %d: %w", id, err)
	}
	return it, nil
}

// ListTrash returns bin entries newest first. A userID of 0 covers every user,
// and a limit of 0 or less returns everything.
func (s *Store) ListTrash(ctx context.Context, userID int64, limit int) ([]*TrashItem, error) {
	if limit <= 0 {
		// SQLite treats a negative LIMIT as no bound at all.
		limit = -1
	}
	rows, err := s.db.QueryContext(ctx, trashSelect+`
WHERE (? = 0 OR user_id = ?)
ORDER BY deleted_at DESC, id DESC
LIMIT ?`, userID, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list trash: %w", err)
	}
	defer rows.Close()

	items, err := collectTrash(rows)
	if err != nil {
		return nil, fmt.Errorf("store: list trash: %w", err)
	}
	return items, nil
}

// DeleteTrashItem drops one bin row, reporting ErrNotFound when there was
// nothing to drop. Removing the stored data on disk is the caller's job.
func (s *Store) DeleteTrashItem(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM trash_items WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("store: delete trash item %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete trash item %d: %w", id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListExpiredTrash returns the entries whose retention window closed, oldest
// first, so the housekeeping pass can erase them. Entries stored without an
// expiry are kept forever and never show up here.
func (s *Store) ListExpiredTrash(ctx context.Context, now time.Time) ([]*TrashItem, error) {
	if now.IsZero() {
		now = time.Now()
	}
	rows, err := s.db.QueryContext(ctx, trashSelect+`
WHERE expires_at > 0 AND expires_at < ?
ORDER BY expires_at ASC, id ASC`, now.Unix())
	if err != nil {
		return nil, fmt.Errorf("store: list expired trash: %w", err)
	}
	defer rows.Close()

	items, err := collectTrash(rows)
	if err != nil {
		return nil, fmt.Errorf("store: list expired trash: %w", err)
	}
	return items, nil
}

// TrashStats reports how many entries a user has in the bin and how much space
// they hold. A userID of 0 covers every user.
func (s *Store) TrashStats(ctx context.Context, userID int64) (count int, bytes int64, err error) {
	err = s.db.QueryRowContext(ctx, `
SELECT COUNT(*), COALESCE(SUM(size), 0) FROM trash_items WHERE (? = 0 OR user_id = ?)`,
		userID, userID).Scan(&count, &bytes)
	if err != nil {
		return 0, 0, fmt.Errorf("store: trash stats: %w", err)
	}
	return count, bytes, nil
}

// ClaimTrash returns every bin entry of a user and removes the rows in the same
// transaction, so an emptied bin can never be claimed twice by two concurrent
// requests. The caller then erases the returned StoredPath locations. A userID
// of 0 claims the whole bin.
func (s *Store) ClaimTrash(ctx context.Context, userID int64) ([]*TrashItem, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: claim trash: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, trashSelect+`
WHERE (? = 0 OR user_id = ?)
ORDER BY deleted_at DESC, id DESC`, userID, userID)
	if err != nil {
		return nil, fmt.Errorf("store: claim trash: %w", err)
	}
	items, err := collectTrash(rows)
	// The rows have to be closed before the same transaction can execute the
	// delete, so this cannot be deferred.
	if cerr := rows.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, fmt.Errorf("store: claim trash: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		"DELETE FROM trash_items WHERE (? = 0 OR user_id = ?)", userID, userID); err != nil {
		return nil, fmt.Errorf("store: claim trash: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: claim trash: %w", err)
	}
	return items, nil
}
