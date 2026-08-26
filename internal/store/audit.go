package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// AuditFilter narrows an audit log query. Every zero valued field means
// "no restriction on this column".
type AuditFilter struct {
	// UserID keeps only entries produced by one account. Zero means any.
	UserID int64
	// Action keeps only entries with this exact action name.
	Action string
	// Query is a free text needle matched against username, target, detail
	// and IP address.
	Query string
	// Since and Until bound the entry timestamp, both inclusive.
	Since time.Time
	Until time.Time
	// Limit and Offset page the result. Limit defaults to 200 and is capped
	// at 2000.
	Limit  int
	Offset int
}

// Paging bounds applied to every audit listing so a careless caller cannot
// pull the whole table into memory.
const (
	auditDefaultLimit = 200
	auditMaxLimit     = 2000
)

const auditColumns = `id, user_id, username, action, target, detail, ip, ua, ok, at`

// Audit appends one entry to the audit log. The entry timestamp is filled in
// with the current time when the caller left it zero.
//
// Auditing is a side effect of some other operation, so callers normally treat
// a failure here as non fatal. The error is still returned rather than
// swallowed, so a caller that does care can react.
func (s *Store) Audit(ctx context.Context, e AuditEntry) error {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO audit_log (user_id, username, action, target, detail, ip, ua, ok, at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.UserID, e.Username, e.Action, e.Target, e.Detail,
		e.IP, e.UA, boolToInt(e.OK), ts(e.At))
	if err != nil {
		return fmt.Errorf("store: write audit entry: %w", err)
	}
	return nil
}

// ListAudit returns matching entries newest first.
func (s *Store) ListAudit(ctx context.Context, f AuditFilter) ([]*AuditEntry, error) {
	where, args := auditWhere(f)

	limit := f.Limit
	switch {
	case limit <= 0:
		limit = auditDefaultLimit
	case limit > auditMaxLimit:
		limit = auditMaxLimit
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(args, limit, offset)

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+auditColumns+` FROM audit_log`+where+
			` ORDER BY at DESC, id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list audit: %w", err)
	}
	defer rows.Close()

	entries := make([]*AuditEntry, 0, limit)
	for rows.Next() {
		e, err := scanAuditEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list audit: %w", err)
	}
	return entries, nil
}

// CountAudit reports how many entries match the filter. Limit and Offset are
// ignored so the result can drive pagination.
func (s *Store) CountAudit(ctx context.Context, f AuditFilter) (int, error) {
	where, args := auditWhere(f)
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_log`+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count audit: %w", err)
	}
	return n, nil
}

// PurgeAudit deletes entries older than the given instant and reports how many
// rows went away. A zero time deletes nothing.
func (s *Store) PurgeAudit(ctx context.Context, before time.Time) (int64, error) {
	if before.IsZero() {
		return 0, nil
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM audit_log WHERE at < ?`, ts(before))
	if err != nil {
		return 0, fmt.Errorf("store: purge audit: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: purge audit: %w", err)
	}
	return n, nil
}

// auditWhere renders the filter as a WHERE clause plus its bind arguments.
// The returned clause is empty when nothing is filtered.
func auditWhere(f AuditFilter) (string, []any) {
	var (
		clauses []string
		args    []any
	)
	if f.UserID > 0 {
		clauses = append(clauses, "user_id = ?")
		args = append(args, f.UserID)
	}
	if action := strings.TrimSpace(f.Action); action != "" {
		clauses = append(clauses, "action = ?")
		args = append(args, action)
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		needle := auditLike(q)
		clauses = append(clauses, `(username LIKE ? ESCAPE '\' OR target LIKE ? ESCAPE '\'`+
			` OR detail LIKE ? ESCAPE '\' OR ip LIKE ? ESCAPE '\')`)
		args = append(args, needle, needle, needle, needle)
	}
	if !f.Since.IsZero() {
		clauses = append(clauses, "at >= ?")
		args = append(args, ts(f.Since))
	}
	if !f.Until.IsZero() {
		clauses = append(clauses, "at <= ?")
		args = append(args, ts(f.Until))
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// auditLike wraps a user supplied needle in wildcards, escaping the SQL
// wildcards inside it so a typed percent or underscore matches literally.
// Pair it with an ESCAPE '\' clause.
func auditLike(q string) string {
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
	return "%" + esc + "%"
}

// auditRow is satisfied by both *sql.Row and *sql.Rows.
type auditRow interface {
	Scan(dest ...any) error
}

func scanAuditEntry(row auditRow) (*AuditEntry, error) {
	var (
		e  AuditEntry
		ok int
		at int64
	)
	err := row.Scan(&e.ID, &e.UserID, &e.Username, &e.Action, &e.Target,
		&e.Detail, &e.IP, &e.UA, &ok, &at)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("store: scan audit entry: %w", err)
	}
	e.OK = ok != 0
	e.At = fromTS(at)
	return &e, nil
}
