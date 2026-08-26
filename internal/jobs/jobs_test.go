// Storix - modern web file manager for servers.
// Developed by X Project.
package jobs

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XProject25/Storix/internal/events"
	"github.com/XProject25/Storix/internal/store"
)

// newManager builds a started manager backed by a throwaway database.
func newManager(t *testing.T, workers int) (*Manager, *events.Hub) {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	hub := events.NewHub()
	m := NewManager(st, hub, workers)

	ctx, cancel := context.WithCancel(context.Background())
	m.Start(ctx)

	t.Cleanup(func() {
		shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		m.Shutdown(shutdown)
		cancel()
		hub.Close()
		if err := st.Close(); err != nil {
			t.Errorf("store.Close: %v", err)
		}
	})
	return m, hub
}

// waitFor polls until cond holds or the timeout expires.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// status reads the current status of a job.
func status(t *testing.T, m *Manager, id string) store.JobStatus {
	t.Helper()
	rec, err := m.Get(id)
	if err != nil {
		t.Fatalf("Get(%s): %v", id, err)
	}
	return rec.Status
}

func TestJobRunsAndReportsDone(t *testing.T) {
	m, _ := newManager(t, 2)

	type params struct {
		Src string `json:"src"`
	}
	ran := make(chan struct{})

	rec, err := m.Submit(context.Background(), 42, "copy", "Copy files", params{Src: "/srv/data"},
		func(ctx context.Context, j *Job) error {
			defer close(ran)
			var p params
			if err := j.DecodeParams(&p); err != nil {
				return err
			}
			if p.Src != "/srv/data" {
				return errors.New("parameters were not carried into the handler")
			}
			j.SetTotal(100, 2)
			j.Progress(100, 2, "archive.tar")
			j.SetResult(map[string]string{"path": "/srv/data/archive.tar"})
			return nil
		})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if len(rec.ID) != 16 {
		t.Fatalf("job id %q, want 16 hexadecimal characters", rec.ID)
	}
	if rec.Status != store.JobQueued {
		t.Fatalf("Status = %q, want %q", rec.Status, store.JobQueued)
	}

	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never ran")
	}
	waitFor(t, 5*time.Second, "the job to finish", func() bool {
		return status(t, m, rec.ID) == store.JobDone
	})

	final, err := m.Get(rec.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if final.Done != 100 || final.DoneItems != 2 {
		t.Fatalf("counters = %d bytes / %d items, want 100 / 2", final.Done, final.DoneItems)
	}
	if final.Percent() != 100 {
		t.Fatalf("Percent = %v, want 100", final.Percent())
	}
	if final.FinishedAt == nil || final.StartedAt == nil {
		t.Fatal("started and finished timestamps must be set")
	}
	if final.Cancellable {
		t.Fatal("a finished job must not advertise itself as cancellable")
	}
	if !strings.Contains(final.Result, "archive.tar") {
		t.Fatalf("Result = %q, want the payload set by the handler", final.Result)
	}
	if final.Error != "" {
		t.Fatalf("Error = %q, want empty", final.Error)
	}
}

func TestProgressEventsArePublished(t *testing.T) {
	m, hub := newManager(t, 2)

	const user = int64(7)
	ch, unsubscribe := hub.Subscribe(user)
	defer unsubscribe()

	rec, err := m.Submit(context.Background(), user, "copy", "Copy tree", nil,
		func(ctx context.Context, j *Job) error {
			j.SetTotal(500, 5)
			for i := 0; i < 5; i++ {
				j.Add(100, 1)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(120 * time.Millisecond):
				}
			}
			return nil
		})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	var created, progressed, done bool
	deadline := time.After(10 * time.Second)
	for !done {
		select {
		case e := <-ch:
			payload, ok := e.Data.(*store.Job)
			if !ok {
				continue
			}
			if payload.ID != rec.ID {
				continue
			}
			switch e.Type {
			case events.EventJobCreated:
				created = true
			case events.EventJobProgress:
				if payload.Done > 0 {
					progressed = true
				}
			case events.EventJobDone:
				done = true
			case events.EventJobFailed:
				t.Fatalf("job failed: %s", payload.Error)
			}
		case <-deadline:
			t.Fatal("timed out waiting for the job event stream")
		}
	}
	if !created {
		t.Fatal("no job.created event")
	}
	if !progressed {
		t.Fatal("no job.progress event carrying advanced counters")
	}
}

func TestCancelStopsLongRunningJob(t *testing.T) {
	m, _ := newManager(t, 2)

	started := make(chan struct{})
	stopped := make(chan struct{})

	rec, err := m.Submit(context.Background(), 5, "delete", "Delete tree", nil,
		func(ctx context.Context, j *Job) error {
			close(started)
			<-ctx.Done()
			close(stopped)
			return ctx.Err()
		})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never started")
	}

	if err := m.Cancel(rec.ID, 5, false); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling did not stop the handler")
	}

	waitFor(t, 5*time.Second, "the job to report cancelled", func() bool {
		return status(t, m, rec.ID) == store.JobCanceled
	})

	// Cancelling a second time reports that it is over rather than failing.
	if err := m.Cancel(rec.ID, 5, false); !errors.Is(err, ErrFinished) {
		t.Fatalf("second Cancel = %v, want %v", err, ErrFinished)
	}
}

func TestCancelChecksOwnership(t *testing.T) {
	m, _ := newManager(t, 2)

	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })

	rec, err := m.Submit(context.Background(), 5, "move", "Move tree", nil,
		func(ctx context.Context, j *Job) error {
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if err := m.Cancel(rec.ID, 9, false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Cancel by a stranger = %v, want %v", err, ErrForbidden)
	}
	if err := m.Cancel(rec.ID, 9, true); err != nil {
		t.Fatalf("Cancel by an administrator: %v", err)
	}
	once.Do(func() { close(release) })

	waitFor(t, 5*time.Second, "the job to report cancelled", func() bool {
		return status(t, m, rec.ID) == store.JobCanceled
	})
}

func TestPanicInHandlerMarksJobFailed(t *testing.T) {
	m, _ := newManager(t, 2)

	rec, err := m.Submit(context.Background(), 3, "extract", "Extract archive", nil,
		func(ctx context.Context, j *Job) error {
			var broken []string
			// Index out of range, the kind of bug a real handler can hide.
			_ = broken[2]
			return nil
		})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	waitFor(t, 5*time.Second, "the job to report failed", func() bool {
		return status(t, m, rec.ID) == store.JobFailed
	})

	final, err := m.Get(rec.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !strings.Contains(final.Error, "panic") {
		t.Fatalf("Error = %q, want it to mention the panic", final.Error)
	}
	if final.FinishedAt == nil {
		t.Fatal("a failed job must carry a finish timestamp")
	}

	// The pool has to survive the panic and keep taking work.
	ran := make(chan struct{})
	if _, err := m.Submit(context.Background(), 3, "copy", "After the panic", nil,
		func(ctx context.Context, j *Job) error {
			close(ran)
			return nil
		}); err != nil {
		t.Fatalf("Submit after panic: %v", err)
	}
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker pool stopped taking work after a panic")
	}
}

func TestHandlerErrorMarksJobFailed(t *testing.T) {
	m, _ := newManager(t, 2)

	rec, err := m.Submit(context.Background(), 4, "compress", "Compress", nil,
		func(ctx context.Context, j *Job) error {
			return errors.New("disk is full")
		})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitFor(t, 5*time.Second, "the job to report failed", func() bool {
		return status(t, m, rec.ID) == store.JobFailed
	})
	final, err := m.Get(rec.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if final.Error != "disk is full" {
		t.Fatalf("Error = %q, want %q", final.Error, "disk is full")
	}
}

func TestListReturnsNewestFirst(t *testing.T) {
	m, _ := newManager(t, 2)

	const user = int64(21)
	titles := []string{"first", "second", "third"}
	ids := make([]string, 0, len(titles))
	for _, title := range titles {
		rec, err := m.Submit(context.Background(), user, "copy", title, nil,
			func(ctx context.Context, j *Job) error { return nil })
		if err != nil {
			t.Fatalf("Submit %s: %v", title, err)
		}
		ids = append(ids, rec.ID)
		waitFor(t, 5*time.Second, "job "+title+" to finish", func() bool {
			return status(t, m, rec.ID) == store.JobDone
		})
	}

	// A job owned by somebody else must not show up in this user's list.
	if _, err := m.Submit(context.Background(), user+1, "copy", "someone else", nil,
		func(ctx context.Context, j *Job) error { return nil }); err != nil {
		t.Fatalf("Submit for another user: %v", err)
	}

	list, err := m.List(context.Background(), user, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != len(titles) {
		t.Fatalf("List returned %d jobs, want %d", len(list), len(titles))
	}
	for i, want := range []string{"third", "second", "first"} {
		if list[i].Title != want {
			t.Fatalf("List[%d].Title = %q, want %q", i, list[i].Title, want)
		}
	}
	if list[0].ID != ids[len(ids)-1] {
		t.Fatalf("List[0].ID = %q, want the most recent job %q", list[0].ID, ids[len(ids)-1])
	}
}

func TestSubmitBeyondQueueCapacity(t *testing.T) {
	m, _ := newManager(t, 2)

	const total = 30
	var wg sync.WaitGroup
	wg.Add(total)

	ids := make([]string, 0, total)
	for i := 0; i < total; i++ {
		rec, err := m.Submit(context.Background(), 8, "copy", "bulk", nil,
			func(ctx context.Context, j *Job) error {
				defer wg.Done()
				return nil
			})
		if err != nil {
			t.Fatalf("Submit %d: %v", i, err)
		}
		ids = append(ids, rec.ID)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("queued work past the queue capacity never ran")
	}

	for _, id := range ids {
		waitFor(t, 10*time.Second, "job "+id+" to finish", func() bool {
			return status(t, m, id) == store.JobDone
		})
	}
}

func TestGetUnknownJob(t *testing.T) {
	m, _ := newManager(t, 2)
	if _, err := m.Get("0123456789abcdef"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get unknown = %v, want %v", err, ErrNotFound)
	}
	if err := m.Cancel("0123456789abcdef", 1, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Cancel unknown = %v, want %v", err, ErrNotFound)
	}
}

func TestNewIDIsRandomHex(t *testing.T) {
	seen := make(map[string]bool, 64)
	for i := 0; i < 64; i++ {
		id, err := newID()
		if err != nil {
			t.Fatalf("newID: %v", err)
		}
		if len(id) != 16 {
			t.Fatalf("newID = %q, want 16 characters", id)
		}
		if strings.Trim(id, "0123456789abcdef") != "" {
			t.Fatalf("newID = %q, want hexadecimal only", id)
		}
		if seen[id] {
			t.Fatalf("newID repeated %q", id)
		}
		seen[id] = true
	}
}
