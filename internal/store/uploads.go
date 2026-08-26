package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const uploadColumns = `id, user_id, share_token, target_dir, filename, rel_path, size,
        offset_bytes, temp_path, metadata, overwrite, completed, final_path,
        created_at, updated_at, expires_at`

// CreateUpload persists a new resumable upload session. The caller supplies
// the identifier. Timestamps left zero are filled in with the current time.
// A reused identifier returns ErrConflict.
func (s *Store) CreateUpload(ctx context.Context, u *UploadSession) error {
	if u == nil {
		return errors.New("store: nil upload session")
	}
	if u.ID == "" {
		return errors.New("store: empty upload id")
	}
	now := time.Now()
	if u.CreatedAt.IsZero() {
		u.CreatedAt = now
	}
	if u.UpdatedAt.IsZero() {
		u.UpdatedAt = u.CreatedAt
	}
	if u.Offset < 0 {
		u.Offset = 0
	}
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO uploads (id, user_id, share_token, target_dir, filename, rel_path, size,
            offset_bytes, temp_path, metadata, overwrite, completed, final_path,
            created_at, updated_at, expires_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.UserID, u.ShareToken, u.TargetDir, u.Filename, u.RelPath, u.Size,
		u.Offset, u.TempPath, u.Metadata, boolToInt(u.Overwrite), boolToInt(u.Completed),
		u.FinalPath, ts(u.CreatedAt), ts(u.UpdatedAt), ts(u.ExpiresAt))
	if err != nil {
		if isUnique(err) {
			return ErrConflict
		}
		return fmt.Errorf("store: create upload: %w", err)
	}
	return nil
}

// GetUpload loads one session, returning ErrNotFound when the identifier is
// unknown.
func (s *Store) GetUpload(ctx context.Context, id string) (*UploadSession, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+uploadColumns+` FROM uploads WHERE id = ?`, id)
	return scanUpload(row)
}

// SetUploadOffset records how many bytes of the session have been received.
// This runs after every accepted chunk, so it touches only the two columns
// that actually change.
func (s *Store) SetUploadOffset(ctx context.Context, id string, offset int64) error {
	if offset < 0 {
		offset = 0
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE uploads SET offset_bytes = ?, updated_at = ? WHERE id = ?`,
		offset, ts(time.Now()), id)
	if err != nil {
		return fmt.Errorf("store: set upload offset: %w", err)
	}
	return uploadAffected(res, "store: set upload offset")
}

// CompleteUpload marks a session finished and records where the assembled file
// landed.
func (s *Store) CompleteUpload(ctx context.Context, id, finalPath string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE uploads SET completed = 1, final_path = ?, updated_at = ? WHERE id = ?`,
		finalPath, ts(time.Now()), id)
	if err != nil {
		return fmt.Errorf("store: complete upload: %w", err)
	}
	return uploadAffected(res, "store: complete upload")
}

// DeleteUpload removes a session row. It does not touch the temporary file on
// disk, which the upload package owns. Returns ErrNotFound for an unknown
// identifier.
func (s *Store) DeleteUpload(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM uploads WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete upload: %w", err)
	}
	return uploadAffected(res, "store: delete upload")
}

// ListUserUploads returns one user's sessions, newest first. Finished sessions
// are left out unless includeCompleted is set.
func (s *Store) ListUserUploads(ctx context.Context, userID int64, includeCompleted bool) ([]*UploadSession, error) {
	query := `SELECT ` + uploadColumns + ` FROM uploads WHERE user_id = ?`
	if !includeCompleted {
		query += ` AND completed = 0`
	}
	query += ` ORDER BY created_at DESC, id`

	rows, err := s.db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list uploads: %w", err)
	}
	return collectUploads(rows)
}

// ListExpiredUploads returns every session whose expiry has passed, oldest
// first, so a janitor can drop the temporary files and the rows. Completed
// sessions are included: their rows are still garbage once expired. Sessions
// stored without an expiry are never returned.
func (s *Store) ListExpiredUploads(ctx context.Context, now time.Time) ([]*UploadSession, error) {
	if now.IsZero() {
		now = time.Now()
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+uploadColumns+` FROM uploads
         WHERE expires_at > 0 AND expires_at <= ? ORDER BY expires_at`, ts(now))
	if err != nil {
		return nil, fmt.Errorf("store: list expired uploads: %w", err)
	}
	return collectUploads(rows)
}

// UploadStats reports the number of unfinished sessions and how many bytes
// they have already written to the temporary area, which is what a quota check
// has to account for on top of the files already in place. A userID of zero
// covers every user.
func (s *Store) UploadStats(ctx context.Context, userID int64) (active int, bytes int64, err error) {
	query := `SELECT COUNT(*), COALESCE(SUM(offset_bytes), 0) FROM uploads WHERE completed = 0`
	args := []any{}
	if userID > 0 {
		query += ` AND user_id = ?`
		args = append(args, userID)
	}
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&active, &bytes); err != nil {
		return 0, 0, fmt.Errorf("store: upload stats: %w", err)
	}
	return active, bytes, nil
}

func collectUploads(rows *sql.Rows) ([]*UploadSession, error) {
	defer rows.Close()

	var out []*UploadSession
	for rows.Next() {
		u, err := scanUpload(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list uploads: %w", err)
	}
	return out, nil
}

// uploadRow is satisfied by both *sql.Row and *sql.Rows.
type uploadRow interface {
	Scan(dest ...any) error
}

func scanUpload(row uploadRow) (*UploadSession, error) {
	var (
		u                             UploadSession
		overwrite, completed          int
		createdAt, updatedAt, expires int64
	)
	err := row.Scan(&u.ID, &u.UserID, &u.ShareToken, &u.TargetDir, &u.Filename, &u.RelPath,
		&u.Size, &u.Offset, &u.TempPath, &u.Metadata, &overwrite, &completed, &u.FinalPath,
		&createdAt, &updatedAt, &expires)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("store: scan upload: %w", err)
	}
	u.Overwrite = overwrite != 0
	u.Completed = completed != 0
	u.CreatedAt = fromTS(createdAt)
	u.UpdatedAt = fromTS(updatedAt)
	u.ExpiresAt = fromTS(expires)
	return &u, nil
}

// uploadAffected turns "the UPDATE or DELETE matched nothing" into ErrNotFound.
func uploadAffected(res sql.Result, what string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
