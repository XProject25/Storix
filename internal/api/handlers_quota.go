// Storage allowances. An account carries a quota in bytes; this file is what
// turns that number into something real: the figure the interface shows, the
// background walk that keeps the figure honest, and the check an upload runs
// before the first byte is accepted.
//
// Measuring is never done on the request that needs the number. A stale figure
// is answered immediately and a measurement is queued, so a folder holding a
// million files never turns a quota panel into a stalled page.
//
// Storix - modern web file manager for servers.
// Developed by X Project.
package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/XProject25/Storix/internal/jobs"
	"github.com/XProject25/Storix/internal/store"
	"github.com/XProject25/Storix/internal/vfs"
)

const (
	// quotaStaleAfter is how long a measured figure is trusted. Deltas keep it
	// close between walks; the walk corrects the drift they cannot see, such as
	// files changed outside Storix.
	quotaStaleAfter = 10 * time.Minute
	// quotaSubmitTimeout bounds the database write that records the job, not
	// the walk itself, which runs under the job manager context.
	quotaSubmitTimeout = 10 * time.Second
)

// quotaInflight holds the accounts with a measurement already running, so a
// screen that polls the endpoint walks the tree once rather than once per
// request. quotaInflightMu guards it.
var (
	quotaInflightMu sync.Mutex
	quotaInflight   = make(map[int64]bool)
)

// quotaView is the payload of both quota endpoints. A limit of zero means no
// allowance was set, and then percent is zero and remaining is -1, which the
// interface reads as "unlimited" rather than "nothing left".
type quotaView struct {
	Limit      int64   `json:"limit"`
	Used       int64   `json:"used"`
	Files      int64   `json:"files"`
	Percent    float64 `json:"percent"`
	Remaining  int64   `json:"remaining"`
	ComputedAt string  `json:"computedAt"`
	Stale      bool    `json:"stale"`
}

// handleMyQuota reports the allowance of the signed in account.
// GET /api/v1/auth/quota
func (a *API) handleMyQuota(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		a.fail(w, r, errUnauthorized)
		return
	}
	a.quotaRespond(w, r, user)
}

// handleUserQuota reports the allowance of any account, for the administration
// screen. GET /api/v1/users/{id}/quota
func (a *API) handleUserQuota(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	target, err := a.Store.GetUser(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.quotaRespond(w, r, target)
}

// quotaRespond answers with the stored figure and schedules a fresh walk when
// that figure has aged out.
func (a *API) quotaRespond(w http.ResponseWriter, r *http.Request, u *store.User) {
	usage, err := a.Store.GetUsage(r.Context(), u.ID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	view := quotaBuild(u.Quota, usage)
	if view.Stale {
		a.quotaMeasure(u)
	}
	noCache(w)
	writeJSON(w, http.StatusOK, view)
}

// ---- shared with the rest of the server -------------------------------------

// QuotaFor reports the allowance of an account and the storage it currently
// holds. A limit of zero means the account is unlimited. A figure that has aged
// out also schedules a measurement, so the number heals itself over time
// without anything having to poll the endpoint.
func (a *API) QuotaFor(ctx context.Context, u *store.User) (limit, used int64, err error) {
	if u == nil || u.ID == 0 {
		return 0, 0, errUnauthorized
	}
	usage, err := a.Store.GetUsage(ctx, u.ID)
	if err != nil {
		return 0, 0, err
	}
	if quotaStale(usage.ComputedAt) {
		a.quotaMeasure(u)
	}
	return max(u.Quota, 0), usage.Bytes, nil
}

// QuotaExceeded reports whether adding extra bytes would break the allowance,
// together with what is left of it. An unlimited account is never exceeded and
// reports -1 remaining.
//
// Bytes already written by transfers still in flight count towards the figure.
// Without that, ten parallel uploads would each be measured against the same
// starting point and the allowance would only be noticed once it was long past.
func (a *API) QuotaExceeded(ctx context.Context, u *store.User, extra int64) (bool, int64, error) {
	if u == nil || u.Quota <= 0 {
		return false, -1, nil
	}
	limit, used, err := a.QuotaFor(ctx, u)
	if err != nil {
		return false, 0, err
	}
	if limit <= 0 {
		return false, -1, nil
	}
	if _, pending, err := a.Store.UploadStats(ctx, u.ID); err == nil {
		used += pending
	} else {
		a.Logger.Warn("upload stats unavailable for quota", "user", u.ID, "err", err)
	}
	remaining := max(limit-used, 0)
	return extra > remaining, remaining, nil
}

// UploadQuotaCheck returns the hook the upload engine consults before it
// accepts a transfer. main.go passes it into upload.Deps; leaving it out keeps
// uploads working exactly as they did before allowances existed.
func (a *API) UploadQuotaCheck() func(ctx context.Context, userID int64, size int64) (bool, int64, error) {
	return func(ctx context.Context, userID int64, size int64) (bool, int64, error) {
		if userID == 0 {
			return true, -1, nil
		}
		u, err := a.Store.GetUser(ctx, userID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				// The session outlived the account; the upload layer has its
				// own answer for that, and it is not a quota refusal.
				return true, -1, nil
			}
			return false, 0, err
		}
		over, remaining, err := a.QuotaExceeded(ctx, u, size)
		if err != nil {
			return false, 0, err
		}
		return !over, remaining, nil
	}
}

// UploadQuotaAdd returns the hook the upload engine calls once a transfer has
// landed, so the figure moves with the file instead of waiting for the next
// walk. main.go passes it into upload.Deps.
func (a *API) UploadQuotaAdd() func(ctx context.Context, userID int64, bytes int64) {
	return func(ctx context.Context, userID int64, bytes int64) {
		if userID == 0 || bytes == 0 {
			return
		}
		if err := a.Store.AddUsage(ctx, userID, bytes, 1); err != nil {
			a.Logger.Warn("storage figure not updated", "user", userID, "err", err)
		}
	}
}

// ---- measurement ------------------------------------------------------------

// quotaStale reports whether a measurement stamp is missing or too old to be
// trusted.
func quotaStale(computedAt time.Time) bool {
	return computedAt.IsZero() || time.Since(computedAt) > quotaStaleAfter
}

// quotaBuild turns an allowance and a stored figure into the payload.
func quotaBuild(limit int64, usage *store.Usage) quotaView {
	v := quotaView{Limit: max(limit, 0), Remaining: -1, Stale: true}
	if usage != nil {
		v.Used = max(usage.Bytes, 0)
		v.Files = max(usage.Files, 0)
		if !usage.ComputedAt.IsZero() {
			v.ComputedAt = usage.ComputedAt.UTC().Format(time.RFC3339)
		}
		v.Stale = quotaStale(usage.ComputedAt)
	}
	if v.Limit > 0 {
		v.Remaining = max(v.Limit-v.Used, 0)
		// An allowance the account has already outgrown still reports a full
		// bar rather than a figure above one hundred.
		v.Percent = min(float64(v.Used)/float64(v.Limit)*100, 100)
	}
	return v
}

// quotaMeasure walks the folders of an account in the background and stores
// what it finds. It returns at once, and does nothing when a walk for the same
// account is already running.
func (a *API) quotaMeasure(u *store.User) {
	if u == nil || u.ID == 0 || a.Jobs == nil || a.VFS == nil {
		return
	}
	if !quotaClaim(u.ID) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), quotaSubmitTimeout)
	defer cancel()

	_, err := a.Jobs.Submit(ctx, u.ID, "usage", "Measuring storage use",
		map[string]any{"userId": u.ID},
		func(ctx context.Context, j *jobs.Job) error {
			defer quotaRelease(u.ID)
			return a.quotaWalk(ctx, j, u)
		})
	if err != nil {
		quotaRelease(u.ID)
		a.Logger.Warn("storage measurement not started", "user", u.ID, "err", err)
	}
}

// quotaWalk adds up every folder the account can reach and stores the total.
func (a *API) quotaWalk(ctx context.Context, j *jobs.Job, u *store.User) error {
	scope, err := a.scopeFor(ctx, u)
	if err != nil {
		return err
	}
	var bytes, files int64
	for _, p := range quotaRoots(scope) {
		b, n, err := a.VFS.DirSize(ctx, scope, p)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// A folder that was removed or cannot be read simply contributes
			// nothing; refusing to store the rest would help nobody.
			a.Logger.Debug("storage measurement skipped a folder", "path", p, "err", err)
			continue
		}
		bytes += b
		files += n
		j.Progress(bytes, files, p)
	}
	usage := store.Usage{UserID: u.ID, Bytes: bytes, Files: files, ComputedAt: time.Now().UTC()}
	if err := a.Store.SetUsage(ctx, usage); err != nil {
		return err
	}
	j.SetMessage("Storage use measured")
	return nil
}

// quotaRoots reduces a scope to the folders worth walking. A mount that sits
// inside another mount would otherwise have its contents counted twice.
func quotaRoots(scope vfs.Scope) []string {
	paths := make([]string, 0, len(scope.Mounts))
	for _, m := range scope.Mounts {
		if p := vfs.Clean(m.Path); p != "" {
			paths = append(paths, p)
		}
	}
	// Sorting puts a parent ahead of everything below it, so one pass is
	// enough to drop the folders already covered.
	sort.Strings(paths)
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		covered := false
		for _, kept := range out {
			if vfs.Contains(kept, p) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, p)
		}
	}
	return out
}

// quotaClaim marks an account as being measured. It reports false when a
// measurement is already under way, which is the caller's signal to skip.
func quotaClaim(userID int64) bool {
	quotaInflightMu.Lock()
	defer quotaInflightMu.Unlock()
	if quotaInflight[userID] {
		return false
	}
	quotaInflight[userID] = true
	return true
}

// quotaRelease clears the mark left by quotaClaim.
func quotaRelease(userID int64) {
	quotaInflightMu.Lock()
	delete(quotaInflight, userID)
	quotaInflightMu.Unlock()
}
