package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// shareSelect is the projection shared by every share query. The owner name is
// joined in so a listing never needs a second lookup per row, and a share whose
// owner row is gone still scans cleanly.
const shareSelect = `
SELECT s.id, s.token, s.owner_id,
       COALESCE(NULLIF(u.display_name, ''), u.username, '') AS owner_name,
       s.path, s.name, s.kind, s.is_dir, s.password_hash,
       s.allow_download, s.allow_upload, s.allow_list,
       s.max_downloads, s.downloads, s.note,
       s.expires_at, s.last_access_at, s.created_at
FROM shares s
LEFT JOIN users u ON u.id = s.owner_id`

// escapeLike neutralizes the SQLite LIKE wildcards so a stored path is matched
// literally. Every query built on it declares ESCAPE '\'.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// subtreeArgs returns the two bindings that select a path and everything below
// it: the literal path, and a LIKE pattern for its children. An empty first
// result means the prefix was not usable and the caller must not run the query.
func subtreeArgs(prefix string) (exact, like string) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return "", ""
	}
	if prefix != "/" {
		prefix = strings.TrimRight(prefix, "/")
	}
	if prefix == "" || prefix == "/" {
		return "/", `/%`
	}
	return prefix, escapeLike(prefix) + `/%`
}

// scanShare reads one row of shareSelect.
func scanShare(sc interface{ Scan(dest ...any) error }) (*Share, error) {
	var (
		sh       Share
		kind     string
		isDir    int
		allowDL  int
		allowUP  int
		allowLS  int
		expires  sql.NullInt64
		accessed sql.NullInt64
		created  int64
	)
	err := sc.Scan(&sh.ID, &sh.Token, &sh.OwnerID, &sh.OwnerName,
		&sh.Path, &sh.Name, &kind, &isDir, &sh.PasswordHash,
		&allowDL, &allowUP, &allowLS,
		&sh.MaxDownloads, &sh.Downloads, &sh.Note,
		&expires, &accessed, &created)
	if err != nil {
		return nil, err
	}
	sh.Kind = ShareKind(kind)
	sh.IsDir = isDir != 0
	sh.AllowDownload = allowDL != 0
	sh.AllowUpload = allowUP != 0
	sh.AllowList = allowLS != 0
	sh.HasPassword = sh.PasswordHash != ""
	sh.ExpiresAt = fromNullTS(expires)
	sh.LastAccessAt = fromNullTS(accessed)
	sh.CreatedAt = fromTS(created)
	return &sh, nil
}

// CreateShare stores a new public link and returns its identifier. The token
// must be unique: a collision reports ErrConflict so the caller can mint a
// fresh token and retry. The new identifier is written back into sh.
func (s *Store) CreateShare(ctx context.Context, sh *Share) (int64, error) {
	if sh == nil {
		return 0, errors.New("store: nil share")
	}
	if strings.TrimSpace(sh.Token) == "" {
		return 0, errors.New("store: share token is required")
	}
	if strings.TrimSpace(sh.Path) == "" {
		return 0, errors.New("store: share path is required")
	}
	if sh.Kind == "" {
		sh.Kind = ShareDownload
	}
	if sh.CreatedAt.IsZero() {
		sh.CreatedAt = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx, `
INSERT INTO shares (token, owner_id, path, name, kind, is_dir, password_hash,
                    allow_download, allow_upload, allow_list, max_downloads,
                    downloads, note, expires_at, last_access_at, created_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		sh.Token, sh.OwnerID, sh.Path, sh.Name, string(sh.Kind), boolToInt(sh.IsDir), sh.PasswordHash,
		boolToInt(sh.AllowDownload), boolToInt(sh.AllowUpload), boolToInt(sh.AllowList), sh.MaxDownloads,
		sh.Downloads, sh.Note, nullTS(sh.ExpiresAt), nullTS(sh.LastAccessAt), ts(sh.CreatedAt))
	if err != nil {
		if isUnique(err) {
			return 0, ErrConflict
		}
		return 0, fmt.Errorf("store: create share: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: create share: %w", err)
	}
	sh.ID = id
	sh.HasPassword = sh.PasswordHash != ""
	return id, nil
}

// UpdateShare saves the editable fields of a link. The token, the owner, the
// download counter and the creation time are immutable here; the counter
// belongs to TouchShare so a concurrent public download is never lost.
// A missing row reports ErrNotFound.
func (s *Store) UpdateShare(ctx context.Context, sh *Share) error {
	if sh == nil {
		return errors.New("store: nil share")
	}
	if sh.ID == 0 {
		return errors.New("store: share id is required")
	}
	if strings.TrimSpace(sh.Path) == "" {
		return errors.New("store: share path is required")
	}
	if sh.Kind == "" {
		sh.Kind = ShareDownload
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE shares
   SET path = ?, name = ?, kind = ?, is_dir = ?, password_hash = ?,
       allow_download = ?, allow_upload = ?, allow_list = ?,
       max_downloads = ?, note = ?, expires_at = ?
 WHERE id = ?`,
		sh.Path, sh.Name, string(sh.Kind), boolToInt(sh.IsDir), sh.PasswordHash,
		boolToInt(sh.AllowDownload), boolToInt(sh.AllowUpload), boolToInt(sh.AllowList),
		sh.MaxDownloads, sh.Note, nullTS(sh.ExpiresAt), sh.ID)
	if err != nil {
		return fmt.Errorf("store: update share %d: %w", sh.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update share %d: %w", sh.ID, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	sh.HasPassword = sh.PasswordHash != ""
	return nil
}

// GetShare loads one link by identifier, reporting ErrNotFound when it is gone.
func (s *Store) GetShare(ctx context.Context, id int64) (*Share, error) {
	sh, err := scanShare(s.db.QueryRowContext(ctx, shareSelect+"\nWHERE s.id = ?", id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: get share %d: %w", id, err)
	}
	return sh, nil
}

// GetShareByToken loads the link behind a public URL, reporting ErrNotFound
// when the token is unknown. Expiry is not evaluated here: the caller decides
// what an exhausted link should look like, using Share.Expired.
func (s *Store) GetShareByToken(ctx context.Context, token string) (*Share, error) {
	sh, err := scanShare(s.db.QueryRowContext(ctx, shareSelect+"\nWHERE s.token = ?", token))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: get share by token: %w", err)
	}
	return sh, nil
}

// ListShares returns the links owned by a user, newest first. An ownerID of 0
// lists every share in the system, which is what the administration screen
// asks for.
func (s *Store) ListShares(ctx context.Context, ownerID int64) ([]*Share, error) {
	rows, err := s.db.QueryContext(ctx, shareSelect+`
WHERE (? = 0 OR s.owner_id = ?)
ORDER BY s.created_at DESC, s.id DESC`, ownerID, ownerID)
	if err != nil {
		return nil, fmt.Errorf("store: list shares: %w", err)
	}
	defer rows.Close()

	out := make([]*Share, 0, 16)
	for rows.Next() {
		sh, err := scanShare(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list shares: %w", err)
		}
		out = append(out, sh)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list shares: %w", err)
	}
	return out, nil
}

// DeleteShare revokes a link, reporting ErrNotFound when there was nothing to
// revoke.
func (s *Store) DeleteShare(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM shares WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("store: delete share %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete share %d: %w", id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSharesUnder revokes every link pointing at a path or at anything below
// it and reports how many were removed. It runs after a delete or a move so a
// public URL can never outlive the content it published. An ownerID of 0 covers
// every owner.
func (s *Store) DeleteSharesUnder(ctx context.Context, ownerID int64, prefix string) (int64, error) {
	exact, like := subtreeArgs(prefix)
	if exact == "" {
		return 0, nil
	}
	res, err := s.db.ExecContext(ctx, `
DELETE FROM shares
 WHERE (? = 0 OR owner_id = ?)
   AND (path = ? OR path LIKE ? ESCAPE '\')`, ownerID, ownerID, exact, like)
	if err != nil {
		return 0, fmt.Errorf("store: delete shares under %s: %w", prefix, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: delete shares under %s: %w", prefix, err)
	}
	return n, nil
}

// TouchShare records a visit to a public link. When countDownload is set the
// download counter moves too, which is what enforces the max downloads budget.
// A missing row reports ErrNotFound.
func (s *Store) TouchShare(ctx context.Context, id int64, countDownload bool) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE shares
   SET last_access_at = ?, downloads = downloads + ?
 WHERE id = ?`, time.Now().UTC().Unix(), boolToInt(countDownload), id)
	if err != nil {
		return fmt.Errorf("store: touch share %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: touch share %d: %w", id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// CountShares reports how many links a user owns. An ownerID of 0 counts every
// share in the system.
func (s *Store) CountShares(ctx context.Context, ownerID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM shares WHERE (? = 0 OR owner_id = ?)", ownerID, ownerID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count shares: %w", err)
	}
	return n, nil
}

// PurgeExpiredShares removes links whose expiry has passed and reports how many
// went. Links that only ran out of downloads are kept, so the owner still sees
// them in the listing and can lift the limit.
func (s *Store) PurgeExpiredShares(ctx context.Context, now time.Time) (int64, error) {
	if now.IsZero() {
		now = time.Now()
	}
	res, err := s.db.ExecContext(ctx, `
DELETE FROM shares WHERE expires_at IS NOT NULL AND expires_at > 0 AND expires_at < ?`, now.Unix())
	if err != nil {
		return 0, fmt.Errorf("store: purge expired shares: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: purge expired shares: %w", err)
	}
	return n, nil
}
