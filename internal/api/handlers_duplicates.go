package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/XProject25/Storix/internal/vfs"
)

// Bounds for the duplicate scan. Comparing files means reading them, so the
// work behind this endpoint is fenced in by a deadline, a floor on the file
// size and a ceiling on how many files one report considers.
const (
	// duplicatesTimeout is how long the scan may run before it reports what it
	// has found so far.
	duplicatesTimeout = 45 * time.Second
	// duplicatesMinSize is the smallest file worth comparing when the caller
	// asks for no particular floor.
	duplicatesMinSize = 1 << 10
	// duplicatesMaxGroups caps how many groups one report carries.
	duplicatesMaxGroups = 200
	// duplicatesMaxFiles bounds how many files one report considers.
	duplicatesMaxFiles = 500000
)

// duplicatesResponse is the report plus a short note for the times the scan had
// to stop early, so the interface can say why the picture is incomplete.
type duplicatesResponse struct {
	*vfs.DuplicateReport
	Message string `json:"message,omitempty"`
}

// handleDuplicates answers which files below one folder are stored more than
// once, so the storage screen can say not only what is big but what is wasted.
//
// This is a read, so it leaves no audit entry and does not join the recent
// list: looking for wasted space is not something the user did to a file.
func (a *API) handleDuplicates(w http.ResponseWriter, r *http.Request) {
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
	minSize := queryInt64(r, "min", duplicatesMinSize)
	if minSize <= 0 {
		minSize = duplicatesMinSize
	}

	ctx, cancel := context.WithTimeout(r.Context(), duplicatesTimeout)
	defer cancel()

	report, err := a.VFS.Duplicates(ctx, scope, p, vfs.DuplicateOptions{
		MinSize:   minSize,
		MaxGroups: duplicatesMaxGroups,
		Timeout:   duplicatesTimeout,
		MaxFiles:  duplicatesMaxFiles,
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}

	noCache(w)
	out := duplicatesResponse{DuplicateReport: report}
	if report.Truncated {
		out.Message = duplicatesNote(report.Scanned)
	}
	writeJSON(w, http.StatusOK, out)
}

// duplicatesNote explains in one sentence why a scan stopped short.
func duplicatesNote(scanned int) string {
	if scanned >= duplicatesMaxFiles {
		return fmt.Sprintf("This folder holds a great many files, so only the first %d were compared", duplicatesMaxFiles)
	}
	return "This folder is very large, so the scan stopped early, largest files first"
}
