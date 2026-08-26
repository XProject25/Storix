package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/XProject25/Storix/internal/events"
	"github.com/XProject25/Storix/internal/jobs"
	"github.com/XProject25/Storix/internal/store"
)

// Background operations and the live event stream.

const (
	// jobHeartbeat is how long the event stream may stay silent before a
	// comment line is sent. Proxies and load balancers close connections that
	// produce nothing, so an idle stream needs traffic of its own.
	jobHeartbeat = 25 * time.Second
	// jobHelloLimit is how many recent operations a reconnecting page receives
	// so it can catch up without a second request.
	jobHelloLimit = 50
	// jobHelloTimeout bounds the single database read the stream performs.
	// Nothing else touches the database while the connection is open.
	jobHelloTimeout = 5 * time.Second
)

// handleListJobs returns the operations of the caller, newest first.
// Administrators may ask for every account with all=1.
func (a *API) handleListJobs(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	noCache(w)
	if a.Jobs == nil {
		writeJSON(w, http.StatusOK, map[string]any{"jobs": []*store.Job{}, "count": 0})
		return
	}
	owner := user.ID
	if queryBool(r, "all") && user.IsAdmin() {
		owner = 0
	}
	list, err := a.Jobs.List(r.Context(), owner, queryInt(r, "limit", jobHelloLimit))
	if err != nil {
		a.fail(w, r, jobMapError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": list, "count": len(list)})
}

// handleGetJob returns one operation. An operation belonging to somebody else
// reads as missing, so the identifier space cannot be probed.
func (a *API) handleGetJob(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		a.fail(w, r, badRequest("Invalid identifier"))
		return
	}
	if a.Jobs == nil {
		a.fail(w, r, errNotFound)
		return
	}
	rec, err := a.Jobs.Get(id)
	if err != nil {
		a.fail(w, r, jobMapError(err))
		return
	}
	if rec.UserID != user.ID && !user.IsAdmin() {
		a.fail(w, r, errNotFound)
		return
	}
	noCache(w)
	writeJSON(w, http.StatusOK, rec)
}

// handleCancelJob stops a queued or running operation.
func (a *API) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		a.fail(w, r, badRequest("Invalid identifier"))
		return
	}
	if a.Jobs == nil {
		a.fail(w, r, errNotFound)
		return
	}
	if err := a.Jobs.Cancel(id, user.ID, user.IsAdmin()); err != nil {
		a.fail(w, r, jobMapError(err))
		return
	}
	a.audit(r, "job.cancel", id, "", true)
	writeOK(w)
}

// handleEvents is the server sent event stream that carries job progress,
// upload progress and file system change notices to the open page.
func (a *API) handleEvents(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if a.Events == nil {
		a.fail(w, r, apiError(http.StatusServiceUnavailable, "unavailable", "Live updates are not available"))
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	// Without this nginx buffers the whole stream and the page sees nothing.
	h.Set("X-Accel-Buffering", "no")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	ctrl := http.NewResponseController(w)
	// The stream outlives an ordinary request by design, so the write deadline
	// the server applies to normal responses has to be lifted.
	_ = ctrl.SetWriteDeadline(time.Time{})

	// Subscribe before the catch up read, so an event raised while the job
	// list is being fetched is queued rather than lost.
	stream, unsubscribe := a.Events.Subscribe(user.ID)
	defer unsubscribe()

	if _, err := io.WriteString(w, ": storix event stream\n\n"); err != nil {
		return
	}
	if err := ctrl.Flush(); err != nil {
		return
	}

	if !jobWriteEvent(w, ctrl, events.New("hello", map[string]any{
		"userId": user.ID,
		"jobs":   a.jobCatchUp(r, user.ID),
	})) {
		return
	}

	beat := time.NewTicker(jobHeartbeat)
	defer beat.Stop()
	done := r.Context().Done()

	for {
		select {
		case <-done:
			return
		case e, ok := <-stream:
			if !ok {
				// The hub is shutting down.
				return
			}
			if !jobWriteEvent(w, ctrl, e) {
				return
			}
			beat.Reset(jobHeartbeat)
		case <-beat.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			if err := ctrl.Flush(); err != nil {
				return
			}
		}
	}
}

// ---- helpers ----------------------------------------------------------------

// jobCatchUp reads the recent operations a reconnecting page needs. It runs
// once, before the stream starts, and never while the connection is idle.
func (a *API) jobCatchUp(r *http.Request, userID int64) []*store.Job {
	empty := []*store.Job{}
	if a.Jobs == nil {
		return empty
	}
	ctx, cancel := context.WithTimeout(r.Context(), jobHelloTimeout)
	defer cancel()
	list, err := a.Jobs.List(ctx, userID, jobHelloLimit)
	if err != nil {
		a.Logger.Warn("event stream catch up failed", "user", userID, "err", err)
		return empty
	}
	if list == nil {
		return empty
	}
	return list
}

// jobWriteEvent sends one event and flushes it. It reports whether the stream
// is still usable.
func jobWriteEvent(w http.ResponseWriter, ctrl *http.ResponseController, e events.Event) bool {
	payload, err := json.Marshal(e)
	if err != nil {
		// One unencodable payload must not take the whole stream down.
		return true
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", jobEventName(e.Type), payload); err != nil {
		return false
	}
	return ctrl.Flush() == nil
}

// jobEventName keeps the event name on a single line, whatever produced it.
func jobEventName(typ string) string {
	name := strings.TrimSpace(strings.NewReplacer("\r", "", "\n", "").Replace(typ))
	if name == "" {
		return "message"
	}
	return name
}

// jobMapError translates the job manager sentinels into the JSON envelope.
func jobMapError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, jobs.ErrNotFound), errors.Is(err, jobs.ErrForbidden):
		// An operation owned by somebody else reads as missing.
		return errNotFound
	case errors.Is(err, jobs.ErrFinished):
		return conflict("That operation has already finished")
	case errors.Is(err, jobs.ErrNoStore):
		return apiError(http.StatusServiceUnavailable, "unavailable", "Background operations are not available")
	}
	return err
}
