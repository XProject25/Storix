// Package jobs runs the long file operations of Storix in the background.
//
// Copy, move, delete, compress and extract can take minutes on a real server,
// far longer than an HTTP request should live. The API therefore hands the
// work to this manager, which persists every state change to the store and
// streams progress to the browser over the events hub. Each job carries its
// own cancellable context, so the user can stop an operation that is already
// halfway through a directory tree.
//
// Storix - modern web file manager for servers.
// Developed by X Project.
package jobs

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/XProject25/Storix/internal/events"
	"github.com/XProject25/Storix/internal/store"
)

// Errors returned by the manager.
var (
	// ErrNotFound means no job with that id exists.
	ErrNotFound = errors.New("jobs: job not found")
	// ErrForbidden means the caller does not own the job and is not an
	// administrator.
	ErrForbidden = errors.New("jobs: not allowed to control this job")
	// ErrFinished means the job already reached a terminal state.
	ErrFinished = errors.New("jobs: job already finished")
	// ErrNoStore means the manager was built without a database.
	ErrNoStore = errors.New("jobs: no store configured")
)

const (
	// progressInterval throttles progress traffic to five events per second
	// per job. A recursive copy can touch thousands of files a second and the
	// UI cannot use more than a handful of updates.
	progressInterval = 200 * time.Millisecond
	// flushInterval is how often a job that was throttled gets its pending
	// counters published, so the last update before a stall is never lost.
	flushInterval = 250 * time.Millisecond
	// retention is how long a finished job stays in the database.
	retention = 24 * time.Hour
	// janitorInterval is how often finished jobs are purged.
	janitorInterval = time.Hour
	// dbTimeout bounds a single persistence call.
	dbTimeout = 10 * time.Second
)

// Handler performs the actual work of a job. It must return promptly once the
// context is cancelled, and it should report progress through the job so the
// user sees something move.
type Handler func(ctx context.Context, j *Job) error

// Job is a background operation in flight. It wraps the persisted record and
// adds the live controls a handler needs: the cancellable context, the
// progress counters and the result payload.
type Job struct {
	mgr    *Manager
	fn     Handler
	ctx    context.Context
	cancel context.CancelFunc

	// canceled records an explicit cancel request, which is what tells the
	// finaliser to report "canceled" rather than "failed".
	canceled atomic.Bool
	// final flips once the terminal state has been written, so a late
	// progress call from a handler that ignored its context is discarded.
	final atomic.Bool

	mu      sync.Mutex
	rec     store.Job
	lastPub time.Time
	dirty   bool
}

// ID is the job identifier, sixteen hexadecimal characters.
func (j *Job) ID() string {
	if j == nil {
		return ""
	}
	// The identifier is assigned once at submit time and never changes.
	return j.rec.ID
}

// UserID is the account the job belongs to.
func (j *Job) UserID() int64 {
	if j == nil {
		return 0
	}
	return j.rec.UserID
}

// Context is the cancellable context of this job. Handlers must pass it into
// every blocking call they make.
func (j *Job) Context() context.Context {
	if j == nil || j.ctx == nil {
		return context.Background()
	}
	return j.ctx
}

// DecodeParams unmarshals the parameters captured at submit time into v.
func (j *Job) DecodeParams(v any) error {
	if j == nil {
		return ErrNotFound
	}
	j.mu.Lock()
	raw := j.rec.Params
	j.mu.Unlock()
	if raw == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(raw), v); err != nil {
		return fmt.Errorf("jobs: decode params: %w", err)
	}
	return nil
}

// SetTotal declares the size of the work ahead, in bytes and in items. Either
// may be zero when it is not known up front.
func (j *Job) SetTotal(bytes, items int64) {
	if j == nil {
		return
	}
	j.mu.Lock()
	j.rec.Total = bytes
	j.rec.TotalItems = items
	j.mu.Unlock()
	j.note(false)
}

// Progress records absolute completion counters and the item being worked on.
// An empty current leaves the previous message in place.
func (j *Job) Progress(bytes, items int64, current string) {
	if j == nil {
		return
	}
	j.mu.Lock()
	j.rec.Done = bytes
	j.rec.DoneItems = items
	if current != "" {
		j.rec.Message = current
	}
	j.mu.Unlock()
	j.note(false)
}

// Add advances the completion counters by the given deltas.
func (j *Job) Add(bytes, items int64) {
	if j == nil {
		return
	}
	j.mu.Lock()
	j.rec.Done += bytes
	j.rec.DoneItems += items
	j.mu.Unlock()
	j.note(false)
}

// SetMessage replaces the human readable status line.
func (j *Job) SetMessage(msg string) {
	if j == nil {
		return
	}
	j.mu.Lock()
	j.rec.Message = msg
	j.mu.Unlock()
	j.note(false)
}

// SetResult attaches a JSON payload the API returns once the job is done, for
// example the path of a created archive. A value that cannot be encoded is
// logged and ignored rather than lost mid operation.
func (j *Job) SetResult(v any) {
	if j == nil {
		return
	}
	raw := ""
	if v != nil {
		b, err := json.Marshal(v)
		if err != nil {
			slog.Error("storix: encode job result", "job", j.ID(), "error", err)
			return
		}
		raw = string(b)
	}
	j.mu.Lock()
	j.rec.Result = raw
	j.mu.Unlock()
}

// Snapshot returns a copy of the persisted record, safe to hand to a template,
// a JSON encoder or another goroutine.
func (j *Job) Snapshot() *store.Job {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.snapshotLocked()
}

func (j *Job) snapshotLocked() *store.Job {
	cp := j.rec
	if j.rec.StartedAt != nil {
		t := *j.rec.StartedAt
		cp.StartedAt = &t
	}
	if j.rec.FinishedAt != nil {
		t := *j.rec.FinishedAt
		cp.FinishedAt = &t
	}
	return &cp
}

// note persists and publishes the current counters. Unless force is set the
// call is throttled, and a throttled update is marked dirty so the flusher
// picks it up.
func (j *Job) note(force bool) {
	if j.final.Load() {
		return
	}
	now := time.Now()

	j.mu.Lock()
	if !force && now.Sub(j.lastPub) < progressInterval {
		j.dirty = true
		j.mu.Unlock()
		return
	}
	j.lastPub = now
	j.dirty = false
	j.rec.UpdatedAt = now.UTC()
	snap := j.snapshotLocked()
	j.mu.Unlock()

	j.mgr.persist(snap)
	j.mgr.publish(events.EventJobProgress, snap)
}

// flush emits an update that the throttle held back.
func (j *Job) flush() {
	j.mu.Lock()
	dirty := j.dirty
	j.mu.Unlock()
	if dirty {
		j.note(false)
	}
}

// finalize writes the terminal state exactly once and publishes it.
func (j *Job) finalize(status store.JobStatus, cause error) {
	if !j.final.CompareAndSwap(false, true) {
		return
	}
	j.cancel()

	now := time.Now().UTC()
	j.mu.Lock()
	j.rec.Status = status
	j.rec.UpdatedAt = now
	j.rec.FinishedAt = &now
	j.rec.Cancellable = false
	j.dirty = false
	if cause != nil {
		j.rec.Error = cause.Error()
	}
	if status == store.JobCanceled {
		j.rec.Message = "Canceled"
	}
	snap := j.snapshotLocked()
	j.mu.Unlock()

	j.mgr.persist(snap)
	j.mgr.forget(snap.ID)
	if status == store.JobFailed {
		j.mgr.publish(events.EventJobFailed, snap)
		return
	}
	j.mgr.publish(events.EventJobDone, snap)
}

// Manager owns the worker pool, the job registry and the persistence of every
// state change.
type Manager struct {
	st      *store.Store
	hub     *events.Hub
	workers int

	// baseCtx is the parent of every job context, so shutting the manager
	// down stops the handlers as well.
	baseCtx context.Context
	stop    context.CancelFunc

	queue chan *Job
	wg    sync.WaitGroup

	mu      sync.Mutex
	live    map[string]*Job
	backlog []*Job
	started bool
}

// NewManager builds a manager over the given store and events hub. The hub may
// be nil, in which case progress is persisted but not streamed. Fewer than two
// workers are raised to two, so one stuck operation cannot starve the queue.
func NewManager(st *store.Store, hub *events.Hub, workers int) *Manager {
	if workers < 2 {
		workers = 2
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		st:      st,
		hub:     hub,
		workers: workers,
		baseCtx: ctx,
		stop:    cancel,
		queue:   make(chan *Job, workers*4),
		live:    make(map[string]*Job),
	}
}

// Start launches the worker pool, the progress flusher and the janitor that
// purges finished jobs older than a day. Cancelling ctx shuts the manager
// down. Calling Start twice is a no-op.
func (m *Manager) Start(ctx context.Context) {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.mu.Unlock()

	// Anything the database still calls queued or running belongs to a
	// previous process; there is no worker left to finish it.
	m.reapStale()

	if ctx != nil && ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				m.stop()
			case <-m.baseCtx.Done():
			}
		}()
	}

	for i := 0; i < m.workers; i++ {
		m.wg.Add(1)
		go m.worker()
	}
	m.wg.Add(1)
	go m.flusher()
	m.wg.Add(1)
	go m.janitor()

	// Jobs submitted before Start may already be waiting in the backlog.
	m.pump()
}

// Submit records a new job and queues it. The returned record is a snapshot
// taken at submit time. ctx bounds the database write only: the job itself
// runs under the manager context, so it survives the request that created it.
// Submitting more work than the queue holds is allowed, the surplus simply
// stays queued until a worker frees up.
func (m *Manager) Submit(ctx context.Context, userID int64, typ, title string, params any, fn Handler) (*store.Job, error) {
	if m == nil || m.st == nil {
		return nil, ErrNoStore
	}
	if fn == nil {
		return nil, errors.New("jobs: handler is nil")
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	raw := ""
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("jobs: encode params: %w", err)
		}
		raw = string(b)
	}

	now := time.Now().UTC()
	rec := store.Job{
		ID:          id,
		UserID:      userID,
		Type:        typ,
		Status:      store.JobQueued,
		Title:       title,
		Params:      raw,
		Cancellable: true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := insertJob(ctx, m.st, &rec); err != nil {
		return nil, err
	}

	jctx, cancel := context.WithCancel(m.baseCtx)
	j := &Job{mgr: m, fn: fn, ctx: jctx, cancel: cancel, rec: rec}

	m.mu.Lock()
	m.live[id] = j
	m.mu.Unlock()

	snap := j.Snapshot()
	m.publish(events.EventJobCreated, snap)
	m.enqueue(j)
	return snap, nil
}

// Get returns one job, preferring the live copy over the stored one.
func (m *Manager) Get(id string) (*store.Job, error) {
	if m == nil || m.st == nil {
		return nil, ErrNoStore
	}
	m.mu.Lock()
	j := m.live[id]
	m.mu.Unlock()
	if j != nil {
		return j.Snapshot(), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	return selectJob(ctx, m.st, id)
}

// List returns the most recent jobs of one user, newest first. A userID of 0
// or below lists every user, which is what the administrator view wants.
func (m *Manager) List(ctx context.Context, userID int64, limit int) ([]*store.Job, error) {
	if m == nil || m.st == nil {
		return nil, ErrNoStore
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	stored, err := listJobs(ctx, m.st, userID, limit)
	if err != nil {
		return nil, err
	}
	// The live copy holds counters that have not been written yet.
	m.mu.Lock()
	for i, rec := range stored {
		if j := m.live[rec.ID]; j != nil {
			stored[i] = j.Snapshot()
		}
	}
	m.mu.Unlock()
	return stored, nil
}

// Cancel stops a queued or running job. Only the owner or an administrator may
// do so. A job that already finished reports ErrFinished.
func (m *Manager) Cancel(id string, userID int64, admin bool) error {
	if m == nil || m.st == nil {
		return ErrNoStore
	}
	m.mu.Lock()
	j := m.live[id]
	m.mu.Unlock()

	if j == nil {
		// Not live: either it finished or it never existed. Look it up so the
		// caller gets an accurate answer, and still check ownership so the id
		// space cannot be probed.
		rec, err := m.Get(id)
		if err != nil {
			return err
		}
		if !admin && rec.UserID != userID {
			return ErrForbidden
		}
		return ErrFinished
	}

	if !admin && j.UserID() != userID {
		return ErrForbidden
	}
	if j.final.Load() {
		return ErrFinished
	}
	j.canceled.Store(true)
	j.cancel()
	j.finalize(store.JobCanceled, nil)
	return nil
}

// Shutdown stops accepting work, cancels every running job and waits for the
// workers to return or for ctx to expire.
func (m *Manager) Shutdown(ctx context.Context) {
	if m == nil {
		return
	}
	m.stop()
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	if ctx == nil {
		<-done
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// ---- worker pool ------------------------------------------------------------

// enqueue hands the job to a worker, or parks it in the backlog when the queue
// is full. Submitting must never block an HTTP handler.
func (m *Manager) enqueue(j *Job) {
	select {
	case m.queue <- j:
	default:
		m.mu.Lock()
		m.backlog = append(m.backlog, j)
		m.mu.Unlock()
	}
}

// pump moves as many parked jobs into the queue as currently fit. The send is
// non blocking, so holding the lock here cannot deadlock.
func (m *Manager) pump() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for len(m.backlog) > 0 {
		select {
		case m.queue <- m.backlog[0]:
			m.backlog[0] = nil
			m.backlog = m.backlog[1:]
		default:
			return
		}
	}
}

func (m *Manager) worker() {
	defer m.wg.Done()
	for {
		select {
		case <-m.baseCtx.Done():
			return
		case j := <-m.queue:
			m.pump()
			m.run(j)
			m.pump()
		}
	}
}

// run executes one job and writes its terminal state.
func (m *Manager) run(j *Job) {
	if j == nil {
		return
	}
	if j.final.Load() || j.canceled.Load() {
		// Cancelled while it was still waiting for a worker.
		j.finalize(store.JobCanceled, nil)
		return
	}
	if err := j.ctx.Err(); err != nil {
		j.finalize(store.JobCanceled, nil)
		return
	}

	now := time.Now().UTC()
	j.mu.Lock()
	j.rec.Status = store.JobRunning
	j.rec.StartedAt = &now
	j.rec.UpdatedAt = now
	j.lastPub = time.Now()
	snap := j.snapshotLocked()
	j.mu.Unlock()
	m.persist(snap)
	m.publish(events.EventJobProgress, snap)

	err := m.invoke(j)
	switch {
	case j.canceled.Load() || errors.Is(err, context.Canceled):
		j.finalize(store.JobCanceled, nil)
	case err != nil:
		j.finalize(store.JobFailed, err)
	default:
		j.finalize(store.JobDone, nil)
	}
}

// invoke calls the handler with panics contained. A bug in one file operation
// must fail that operation, not the server.
func (m *Manager) invoke(j *Job) (err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("storix: job handler panic",
				"job", j.ID(), "type", j.rec.Type, "panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("jobs: handler panic: %v", r)
		}
	}()
	return j.fn(j.ctx, j)
}

// flusher publishes counters that the per job throttle held back.
func (m *Manager) flusher() {
	defer m.wg.Done()
	t := time.NewTicker(flushInterval)
	defer t.Stop()
	for {
		select {
		case <-m.baseCtx.Done():
			return
		case <-t.C:
			m.mu.Lock()
			pending := make([]*Job, 0, len(m.live))
			for _, j := range m.live {
				pending = append(pending, j)
			}
			m.mu.Unlock()
			for _, j := range pending {
				j.flush()
			}
		}
	}
}

// janitor purges finished jobs once they age out.
func (m *Manager) janitor() {
	defer m.wg.Done()
	m.purge()
	t := time.NewTicker(janitorInterval)
	defer t.Stop()
	for {
		select {
		case <-m.baseCtx.Done():
			return
		case <-t.C:
			m.purge()
		}
	}
}

func (m *Manager) purge() {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	if _, err := purgeJobs(ctx, m.st, time.Now().Add(-retention)); err != nil {
		slog.Error("storix: purge finished jobs", "error", err)
	}
}

// reapStale fails jobs left behind by a previous process run. Without this a
// restart leaves rows stuck at "running" forever.
func (m *Manager) reapStale() {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	pending, err := listPending(ctx, m.st)
	if err != nil {
		slog.Error("storix: read pending jobs", "error", err)
		return
	}
	now := time.Now().UTC()
	for _, rec := range pending {
		m.mu.Lock()
		_, live := m.live[rec.ID]
		m.mu.Unlock()
		if live {
			continue
		}
		rec.Status = store.JobFailed
		rec.Error = "Interrupted by a server restart"
		rec.Cancellable = false
		rec.UpdatedAt = now
		finished := now
		rec.FinishedAt = &finished
		if err := updateJob(ctx, m.st, rec); err != nil {
			slog.Error("storix: reap stale job", "job", rec.ID, "error", err)
		}
	}
}

// forget drops a finished job from the live registry. The store keeps the
// record, so Get and List still find it.
func (m *Manager) forget(id string) {
	m.mu.Lock()
	delete(m.live, id)
	m.mu.Unlock()
}

// persist writes a snapshot. It deliberately uses a background context so the
// final state of a cancelled job still reaches the database.
func (m *Manager) persist(rec *store.Job) {
	if m == nil || m.st == nil || rec == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	if err := updateJob(ctx, m.st, rec); err != nil {
		slog.Error("storix: persist job", "job", rec.ID, "error", err)
	}
}

// publish streams a snapshot to the owner's browser sessions.
func (m *Manager) publish(typ string, rec *store.Job) {
	if m == nil || m.hub == nil || rec == nil {
		return
	}
	m.hub.Publish(rec.UserID, events.New(typ, rec))
}

// newID returns a random sixteen character hexadecimal job id.
func newID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("jobs: generate id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// ---- persistence ------------------------------------------------------------
//
// The queries live here rather than in the store package so the job manager
// owns its own table access and stays independent of the rest of the schema.

const jobColumns = `id, user_id, type, status, title, message, error, total, done,
	total_items, done_items, params, result, cancellable, created_at, updated_at,
	started_at, finished_at`

func insertJob(ctx context.Context, st *store.Store, j *store.Job) error {
	if st == nil || st.DB() == nil {
		return ErrNoStore
	}
	const q = `INSERT INTO jobs (id, user_id, type, status, title, message, error,
		total, done, total_items, done_items, params, result, cancellable,
		created_at, updated_at, started_at, finished_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	_, err := st.DB().ExecContext(ctx, q,
		j.ID, j.UserID, j.Type, string(j.Status), j.Title, j.Message, j.Error,
		j.Total, j.Done, j.TotalItems, j.DoneItems, j.Params, j.Result,
		boolInt(j.Cancellable), unixTS(j.CreatedAt), unixTS(j.UpdatedAt),
		nullUnix(j.StartedAt), nullUnix(j.FinishedAt))
	if err != nil {
		return fmt.Errorf("jobs: insert %s: %w", j.ID, err)
	}
	return nil
}

func updateJob(ctx context.Context, st *store.Store, j *store.Job) error {
	if st == nil || st.DB() == nil {
		return ErrNoStore
	}
	const q = `UPDATE jobs SET status=?, title=?, message=?, error=?, total=?, done=?,
		total_items=?, done_items=?, result=?, cancellable=?, updated_at=?,
		started_at=?, finished_at=? WHERE id=?`
	res, err := st.DB().ExecContext(ctx, q,
		string(j.Status), j.Title, j.Message, j.Error, j.Total, j.Done,
		j.TotalItems, j.DoneItems, j.Result, boolInt(j.Cancellable),
		unixTS(j.UpdatedAt), nullUnix(j.StartedAt), nullUnix(j.FinishedAt), j.ID)
	if err != nil {
		return fmt.Errorf("jobs: update %s: %w", j.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		// Not every driver reports this; the write itself already succeeded.
		return nil
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func selectJob(ctx context.Context, st *store.Store, id string) (*store.Job, error) {
	if st == nil || st.DB() == nil {
		return nil, ErrNoStore
	}
	row := st.DB().QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id=?`, id)
	j, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("jobs: read %s: %w", id, err)
	}
	return j, nil
}

func listJobs(ctx context.Context, st *store.Store, userID int64, limit int) ([]*store.Job, error) {
	if st == nil || st.DB() == nil {
		return nil, ErrNoStore
	}
	// rowid breaks ties, because created_at only has one second resolution and
	// a burst of jobs shares the same value.
	q := `SELECT ` + jobColumns + ` FROM jobs ORDER BY created_at DESC, rowid DESC LIMIT ?`
	args := []any{limit}
	if userID > 0 {
		q = `SELECT ` + jobColumns + ` FROM jobs WHERE user_id=? ORDER BY created_at DESC, rowid DESC LIMIT ?`
		args = []any{userID, limit}
	}
	rows, err := st.DB().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("jobs: list: %w", err)
	}
	defer rows.Close()

	out := make([]*store.Job, 0, limit)
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("jobs: list: %w", err)
		}
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobs: list: %w", err)
	}
	return out, nil
}

// listPending returns every job the database still considers unfinished.
func listPending(ctx context.Context, st *store.Store) ([]*store.Job, error) {
	if st == nil || st.DB() == nil {
		return nil, ErrNoStore
	}
	q := `SELECT ` + jobColumns + ` FROM jobs WHERE status IN (?,?)`
	rows, err := st.DB().QueryContext(ctx, q, string(store.JobQueued), string(store.JobRunning))
	if err != nil {
		return nil, fmt.Errorf("jobs: list pending: %w", err)
	}
	defer rows.Close()

	var out []*store.Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("jobs: list pending: %w", err)
		}
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobs: list pending: %w", err)
	}
	return out, nil
}

// purgeJobs deletes finished jobs that ended before the cutoff.
func purgeJobs(ctx context.Context, st *store.Store, before time.Time) (int64, error) {
	if st == nil || st.DB() == nil {
		return 0, ErrNoStore
	}
	const q = `DELETE FROM jobs WHERE status IN (?,?,?) AND COALESCE(finished_at, updated_at) < ?`
	res, err := st.DB().ExecContext(ctx, q,
		string(store.JobDone), string(store.JobFailed), string(store.JobCanceled), before.Unix())
	if err != nil {
		return 0, fmt.Errorf("jobs: purge: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return n, nil
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanJob(s scanner) (*store.Job, error) {
	var (
		j                  store.Job
		status             string
		cancellable        int64
		created, updated   int64
		started, finished  sql.NullInt64
		params, resultJSON string
	)
	err := s.Scan(&j.ID, &j.UserID, &j.Type, &status, &j.Title, &j.Message, &j.Error,
		&j.Total, &j.Done, &j.TotalItems, &j.DoneItems, &params, &resultJSON,
		&cancellable, &created, &updated, &started, &finished)
	if err != nil {
		return nil, err
	}
	j.Status = store.JobStatus(status)
	j.Params = params
	j.Result = resultJSON
	j.Cancellable = cancellable != 0
	j.CreatedAt = fromUnix(created)
	j.UpdatedAt = fromUnix(updated)
	j.StartedAt = fromNullUnix(started)
	j.FinishedAt = fromNullUnix(finished)
	return &j, nil
}

// ---- small local helpers ----------------------------------------------------

func unixTS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func fromUnix(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.Unix(v, 0).UTC()
}

func nullUnix(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.Unix()
}

func fromNullUnix(v sql.NullInt64) *time.Time {
	if !v.Valid || v.Int64 == 0 {
		return nil
	}
	t := time.Unix(v.Int64, 0).UTC()
	return &t
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
