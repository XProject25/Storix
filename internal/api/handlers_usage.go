package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/XProject25/Storix/internal/vfs"
)

// Bounds for the storage report. A folder can hold a whole volume, so the
// walk behind it is always fenced in by both a deadline and an entry ceiling.
const (
	// usageTimeout is how long the walk may run before it reports what it has.
	usageTimeout = 25 * time.Second
	// usageDefaultLimit is how many rows each list carries when the caller
	// asks for no particular number.
	usageDefaultLimit = 40
	// usageMaxLimit caps what the caller may ask for.
	usageMaxLimit = 200
	// usageMaxEntries bounds how many entries one report visits.
	usageMaxEntries = 2000000
)

// usageResponse is the report plus a short note for the times the walk had to
// stop early, so the interface can say why the numbers are incomplete.
type usageResponse struct {
	*vfs.UsageReport
	Message string `json:"message,omitempty"`
}

// handleUsage answers what is taking up the space below one folder.
//
// This is a read, so it leaves no audit entry and does not join the recent
// list: opening the storage report is not something the user did to a file.
func (a *API) handleUsage(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	p, err := pathParam(r, "path")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	scope, err := a.scopeFor(r.Context(), user)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	limit := queryInt(r, "limit", usageDefaultLimit)
	if limit <= 0 || limit > usageMaxLimit {
		limit = usageDefaultLimit
	}

	ctx, cancel := context.WithTimeout(r.Context(), usageTimeout)
	defer cancel()

	report, err := a.VFS.Usage(ctx, scope, p, vfs.UsageOptions{
		Limit:      limit,
		Timeout:    usageTimeout,
		MaxEntries: usageMaxEntries,
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}

	noCache(w)
	out := usageResponse{UsageReport: report}
	if report.Truncated {
		out.Message = usageNote(report.Scanned)
	}
	writeJSON(w, http.StatusOK, out)
}

// usageNote explains in one sentence why a report stopped short.
func usageNote(scanned int) string {
	if scanned >= usageMaxEntries {
		return fmt.Sprintf("This folder is very large, so this covers the first %d million entries", usageMaxEntries/1000000)
	}
	return "This folder is very large, so the scan stopped early and some of it is not counted"
}
