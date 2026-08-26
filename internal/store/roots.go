package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// rootColumns lists the roots table in the order scanRoot expects.
const rootColumns = `id, path, label, icon, read_only, sort_order, created_at`

// scanRoot reads one roots row. The parameter is satisfied by both *sql.Row
// and *sql.Rows.
func scanRoot(sc interface{ Scan(dest ...any) error }) (*Root, error) {
	var (
		r         Root
		readOnly  int
		createdAt int64
	)
	if err := sc.Scan(&r.ID, &r.Path, &r.Label, &r.Icon, &readOnly, &r.SortOrder, &createdAt); err != nil {
		return nil, err
	}
	r.ReadOnly = readOnly != 0
	r.CreatedAt = fromTS(createdAt)
	return &r, nil
}

// normalizeRoot trims free text and applies the defaults the schema declares.
func normalizeRoot(r *Root) {
	r.Path = strings.TrimSpace(r.Path)
	r.Label = strings.TrimSpace(r.Label)
	r.Icon = strings.TrimSpace(r.Icon)
	if r.Icon == "" {
		r.Icon = "folder"
	}
}

// CreateRoot exposes a directory tree to Storix and returns its identifier.
// CreatedAt is set when left zero. A path already exposed returns ErrConflict.
func (s *Store) CreateRoot(ctx context.Context, r *Root) (int64, error) {
	if r == nil {
		return 0, errors.New("store: create root: nil root")
	}
	normalizeRoot(r)
	if r.Path == "" {
		return 0, errors.New("store: create root: empty path")
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC().Truncate(time.Second)
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO roots (path, label, icon, read_only, sort_order, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		r.Path, r.Label, r.Icon, boolToInt(r.ReadOnly), r.SortOrder, ts(r.CreatedAt))
	if err != nil {
		if isUnique(err) {
			return 0, ErrConflict
		}
		return 0, fmt.Errorf("store: create root: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: create root: id: %w", err)
	}
	r.ID = id
	return id, nil
}

// UpdateRoot writes every mutable column of an exposed tree. It returns
// ErrNotFound when the row is gone and ErrConflict when another root already
// holds the path.
func (s *Store) UpdateRoot(ctx context.Context, r *Root) error {
	if r == nil {
		return errors.New("store: update root: nil root")
	}
	if r.ID <= 0 {
		return ErrNotFound
	}
	normalizeRoot(r)
	if r.Path == "" {
		return errors.New("store: update root: empty path")
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE roots SET path = ?, label = ?, icon = ?, read_only = ?, sort_order = ?
		WHERE id = ?`,
		r.Path, r.Label, r.Icon, boolToInt(r.ReadOnly), r.SortOrder, r.ID)
	if err != nil {
		if isUnique(err) {
			return ErrConflict
		}
		return fmt.Errorf("store: update root %d: %w", r.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update root %d: %w", r.ID, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetRoot loads one exposed tree, or ErrNotFound.
func (s *Store) GetRoot(ctx context.Context, id int64) (*Root, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+rootColumns+` FROM roots WHERE id = ?`, id)
	r, err := scanRoot(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: get root %d: %w", id, err)
	}
	return r, nil
}

// ListRoots returns every exposed tree ordered by sort order, then path.
func (s *Store) ListRoots(ctx context.Context) ([]*Root, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+rootColumns+` FROM roots ORDER BY sort_order, path`)
	if err != nil {
		return nil, fmt.Errorf("store: list roots: %w", err)
	}
	defer func() { _ = rows.Close() }()

	roots := make([]*Root, 0, 8)
	for rows.Next() {
		r, err := scanRoot(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list roots: %w", err)
		}
		roots = append(roots, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list roots: %w", err)
	}
	return roots, nil
}

// DeleteRoot stops exposing a tree. User mounts are not touched, so callers
// that enforce the "mounts live inside a root" rule have to re-check them.
// Missing rows return ErrNotFound.
func (s *Store) DeleteRoot(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM roots WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete root %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete root %d: %w", id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// CountRoots reports how many trees are exposed. A count of zero means the
// first run wizard has not finished yet.
func (s *Store) CountRoots(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM roots`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count roots: %w", err)
	}
	return n, nil
}
