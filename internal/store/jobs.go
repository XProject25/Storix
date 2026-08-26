package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const jobColumns = `id, user_id, type, status, title, message, error, total, done,
        total_items, done_items, params, result, cancellable,
        created_at, updated_at, started_at, finished_at`

// Paging bounds for job listings.
const (
	jobsDefaultLimit = 100
	jobsMaxLimit     = 1000
)

// staleJobMessage is used when FailStaleJobs is called without one.
const staleJobMessage = "Interrupted by a server restart"

// CreateJob persists a new background operation. The caller supplies the
// identifier. An empty status defaults to JobQueued and zero timestamps are
// filled in with the current time. A reused identifier returns ErrConflict.
func (s *Store) CreateJob(ctx context.Context, j *Job) error {
	if j == nil {
		return errors.New("store: nil job")
	}
	if j.ID == "" {
		return errors.New("store: empty job id")
	}
	if j.Status == "" {
		j.Status = JobQueued
	}
	if j.CreatedAt.IsZero() {
		j.CreatedAt = time.Now()
	}
	if j.UpdatedAt.IsZero() {
		j.UpdatedAt = j.CreatedAt
	}
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO jobs (id, user_id, type, status, title, message, error, total, done,
            total_items, done_items, params, result, cancellable,
            created_at, updated_at, started_at, finished_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ID, j.UserID, j.Type, string(j.Status), j.Title, j.Message, j.Error,
		j.Total, j.Done, j.TotalItems, j.DoneItems, j.Params, j.Result,
		boolToInt(j.Cancellable), ts(j.CreatedAt), ts(j.UpdatedAt),
		nullTS(j.StartedAt), nullTS(j.FinishedAt))
	if err != nil {
		if isUnique(err) {
			return ErrConflict
		}
		return fmt.Errorf("store: create job: %w", err)
	}
	return nil
}

// UpdateJob writes the mutable fields of a job back to the database and stamps
// UpdatedAt on both the row and the passed struct. The identifier, owner, type
// and creation time are never changed.
func (s *Store) UpdateJob(ctx context.Context, j *Job) error {
	if j == nil {
		return errors.New("store: nil job")
	}
	if j.ID == "" {
		return errors.New("store: empty job id")
	}
	j.UpdatedAt = time.Now()
	res, err := s.db.ExecContext(ctx, `
        UPDATE jobs SET status = ?, title = ?, message = ?, error = ?, total = ?, done = ?,
            total_items = ?, done_items = ?, params = ?, result = ?, cancellable = ?,
            updated_at = ?, started_at = ?, finished_at = ?
        WHERE id = ?`,
		string(j.Status), j.Title, j.Message, j.Error, j.Total, j.Done,
		j.TotalItems, j.DoneItems, j.Params, j.Result, boolToInt(j.Cancellable),
		ts(j.UpdatedAt), nullTS(j.StartedAt), nullTS(j.FinishedAt), j.ID)
	if err != nil {
		return fmt.Errorf("store: update job: %w", err)
	}
	return jobAffected(res, "store: update job")
}

// GetJob loads one job, returning ErrNotFound when the identifier is unknown.
func (s *Store) GetJob(ctx context.Context, id string) (*Job, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = ?`, id)
	return scanJob(row)
}

// ListJobs returns jobs newest first. A userID of zero lists every user's
// jobs, which is what the administrator view wants. A limit of zero or less
// means 100, and the limit is capped at 1000.
func (s *Store) ListJobs(ctx context.Context, userID int64, limit int) ([]*Job, error) {
	switch {
	case limit <= 0:
		limit = jobsDefaultLimit
	case limit > jobsMaxLimit:
		limit = jobsMaxLimit
	}
	query := `SELECT ` + jobColumns + ` FROM jobs`
	args := []any{}
	if userID > 0 {
		query += ` WHERE user_id = ?`
		args = append(args, userID)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list jobs: %w", err)
	}
	return collectJobs(rows)
}

// ListActiveJobs returns every queued or running job across all users, oldest
// first so the caller sees them in the order they were submitted.
func (s *Store) ListActiveJobs(ctx context.Context) ([]*Job, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+jobColumns+` FROM jobs WHERE status IN (?, ?) ORDER BY created_at, id`,
		string(JobQueued), string(JobRunning))
	if err != nil {
		return nil, fmt.Errorf("store: list active jobs: %w", err)
	}
	return collectJobs(rows)
}

// DeleteJob removes a job row, returning ErrNotFound for an unknown
// identifier.
func (s *Store) DeleteJob(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM jobs WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete job: %w", err)
	}
	return jobAffected(res, "store: delete job")
}

// PurgeOldJobs deletes finished jobs that stopped changing before the given
// instant and reports how many rows went away. Queued and running jobs are
// never touched, however old they look. A zero time deletes nothing.
func (s *Store) PurgeOldJobs(ctx context.Context, before time.Time) (int64, error) {
	if before.IsZero() {
		return 0, nil
	}
	res, err := s.db.ExecContext(ctx, `
        DELETE FROM jobs
        WHERE status IN (?, ?, ?) AND COALESCE(finished_at, updated_at) < ?`,
		string(JobDone), string(JobFailed), string(JobCanceled), ts(before))
	if err != nil {
		return 0, fmt.Errorf("store: purge jobs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: purge jobs: %w", err)
	}
	return n, nil
}

// FailStaleJobs marks every queued or running job as failed and reports how
// many were affected. Jobs live only in the memory of the process that runs
// them, so anything still marked active at startup was orphaned by a crash or
// a restart and would otherwise hang in the interface forever.
func (s *Store) FailStaleJobs(ctx context.Context, message string) (int64, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		message = staleJobMessage
	}
	now := ts(time.Now())
	res, err := s.db.ExecContext(ctx, `
        UPDATE jobs SET status = ?, error = ?, message = ?, cancellable = 0,
            updated_at = ?, finished_at = COALESCE(finished_at, ?)
        WHERE status IN (?, ?)`,
		string(JobFailed), message, message, now, now,
		string(JobQueued), string(JobRunning))
	if err != nil {
		return 0, fmt.Errorf("store: fail stale jobs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: fail stale jobs: %w", err)
	}
	return n, nil
}

func collectJobs(rows *sql.Rows) ([]*Job, error) {
	defer rows.Close()

	var out []*Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list jobs: %w", err)
	}
	return out, nil
}

// jobRow is satisfied by both *sql.Row and *sql.Rows.
type jobRow interface {
	Scan(dest ...any) error
}

func scanJob(row jobRow) (*Job, error) {
	var (
		j                     Job
		status                string
		cancellable           int
		createdAt, updatedAt  int64
		startedAt, finishedAt sql.NullInt64
	)
	err := row.Scan(&j.ID, &j.UserID, &j.Type, &status, &j.Title, &j.Message, &j.Error,
		&j.Total, &j.Done, &j.TotalItems, &j.DoneItems, &j.Params, &j.Result,
		&cancellable, &createdAt, &updatedAt, &startedAt, &finishedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("store: scan job: %w", err)
	}
	j.Status = JobStatus(status)
	j.Cancellable = cancellable != 0
	j.CreatedAt = fromTS(createdAt)
	j.UpdatedAt = fromTS(updatedAt)
	j.StartedAt = fromNullTS(startedAt)
	j.FinishedAt = fromNullTS(finishedAt)
	return &j, nil
}

// jobAffected turns "the UPDATE or DELETE matched nothing" into ErrNotFound.
func jobAffected(res sql.Result, what string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- login attempts ---------------------------------------------------------
//
// The attempt log backs the brute force throttle. It lives here because it is,
// like the job table, pure operational bookkeeping rather than user data.

// RecordLoginAttempt appends one sign in attempt for an address.
func (s *Store) RecordLoginAttempt(ctx context.Context, ip, username string, ok bool) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO login_attempts (ip, username, ok, at) VALUES (?, ?, ?, ?)`,
		ip, username, boolToInt(ok), ts(time.Now()))
	if err != nil {
		return fmt.Errorf("store: record login attempt: %w", err)
	}
	return nil
}

// CountFailedAttempts reports how many failed sign ins an address produced
// since the given instant, which is what the rate limiter compares against its
// threshold.
func (s *Store) CountFailedAttempts(ctx context.Context, ip string, since time.Time) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM login_attempts WHERE ip = ? AND ok = 0 AND at >= ?`,
		ip, ts(since)).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count failed attempts: %w", err)
	}
	return n, nil
}

// ClearLoginAttempts drops the history of an address, called after a
// successful sign in so an honest user is not held back by earlier typos.
func (s *Store) ClearLoginAttempts(ctx context.Context, ip string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM login_attempts WHERE ip = ?`, ip); err != nil {
		return fmt.Errorf("store: clear login attempts: %w", err)
	}
	return nil
}

// PurgeLoginAttempts deletes attempts older than the given instant and reports
// how many rows went away. A zero time deletes nothing.
func (s *Store) PurgeLoginAttempts(ctx context.Context, before time.Time) (int64, error) {
	if before.IsZero() {
		return 0, nil
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM login_attempts WHERE at < ?`, ts(before))
	if err != nil {
		return 0, fmt.Errorf("store: purge login attempts: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: purge login attempts: %w", err)
	}
	return n, nil
}
