package vfs

import (
	"container/heap"
	"context"
	"errors"
	"io/fs"
	"math"
	"path"
	"sort"
	"time"
)

// Usage defaults. They keep the question "what is taking up my space" from
// turning into an unbounded walk of a whole volume.
const (
	// defaultUsageLimit caps how many rows each list of a report carries.
	defaultUsageLimit = 40
	// defaultUsageTimeout bounds the walk when the caller sets none.
	defaultUsageTimeout = 30 * time.Second
	// defaultUsageMaxEntries bounds how many entries the walk visits.
	defaultUsageMaxEntries = 2000000
	// usageProgressEvery throttles the progress callback so a fast walk does
	// not spend its time reporting on itself.
	usageProgressEvery = 300 * time.Millisecond
	// usageDeadlineEvery is how many entries pass between deadline checks
	// inside one very large directory.
	usageDeadlineEvery = 4096
)

// UsageNode is one row of a storage report: either a direct child of the
// folder being measured, or a single large file found somewhere below it.
type UsageNode struct {
	Name    string  `json:"name"`
	Path    string  `json:"path"`
	Bytes   int64   `json:"bytes"`
	Files   int64   `json:"files"`
	IsDir   bool    `json:"isDir"`
	Percent float64 `json:"percent"`
	Kind    Kind    `json:"kind"`
}

// KindTotal is everything one file family holds below the measured folder.
type KindTotal struct {
	Kind    Kind    `json:"kind"`
	Bytes   int64   `json:"bytes"`
	Files   int64   `json:"files"`
	Percent float64 `json:"percent"`
}

// UsageReport answers where the space below one folder went.
type UsageReport struct {
	Path    string `json:"path"`
	Bytes   int64  `json:"bytes"`
	Files   int64  `json:"files"`
	Folders int64  `json:"folders"`
	// Children are the direct entries, largest first.
	Children []UsageNode `json:"children"`
	// Largest are the biggest individual files anywhere below the folder, so
	// one huge file buried deep in the tree is still visible.
	Largest []UsageNode `json:"largest"`
	// ByKind groups the bytes by file family, largest first.
	ByKind    []KindTotal `json:"byKind"`
	Scanned   int         `json:"scanned"`
	Truncated bool        `json:"truncated"`
	ElapsedMs int64       `json:"elapsedMs"`
}

// UsageOptions tune one storage report.
type UsageOptions struct {
	// Limit caps each list in the report. Zero uses the package default.
	Limit int
	// Timeout bounds the walk. Zero uses the package default.
	Timeout time.Duration
	// MaxEntries bounds how many entries are visited. Zero uses the package
	// default.
	MaxEntries int
	// Progress is called while the walk runs, a few times a second at most.
	Progress func(scanned int, current string)
}

// usageFrame is one directory waiting to be visited, along with the direct
// child of the measured folder that everything below it belongs to.
type usageFrame struct {
	rel     string
	virtual string
	// owner indexes the direct child accumulator, and is negative for the
	// measured folder itself.
	owner int
}

// Usage answers "what is taking up my space" for one folder.
//
// The tree is walked exactly once and every file is added to the direct child
// it belongs to on the way past, so a folder with a thousand entries costs one
// walk rather than a thousand. Symlinks are never followed and never counted:
// a link is not the space its target uses, and refusing to follow one means a
// loop cannot stall the walk. When the entry ceiling or the deadline stops the
// walk the report comes back marked truncated rather than failing, since a
// partial picture of a large volume is far more useful than an error.
func (v *VFS) Usage(ctx context.Context, scope Scope, p string, opts UsageOptions) (*UsageReport, error) {
	started := time.Now()
	if opts.Limit <= 0 {
		opts.Limit = defaultUsageLimit
	}
	if opts.MaxEntries <= 0 {
		opts.MaxEntries = defaultUsageMaxEntries
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultUsageTimeout
	}

	loc, err := v.Resolve(scope, p)
	if err != nil {
		return nil, err
	}
	info, err := loc.Root.Stat(loc.Rel)
	if err != nil {
		return nil, mapErr(err)
	}
	if !info.IsDir() {
		return nil, ErrNotDir
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	report := &UsageReport{Path: loc.Virtual}
	children := make([]UsageNode, 0, 64)
	kinds := make(map[Kind]*KindTotal, 12)
	largest := &usageHeap{}
	heap.Init(largest)

	stack := []usageFrame{{rel: loc.Rel, virtual: loc.Virtual, owner: -1}}
	sinceCheck := 0
	var lastProgress time.Time

walk:
	for len(stack) > 0 {
		frame := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil, err
			}
			report.Truncated = true
			break walk
		}

		entries, err := readDirRel(loc.Root, frame.rel)
		if err != nil {
			// A directory the account cannot read contributes nothing. A scan
			// of a whole volume will always meet a few of them, and stopping
			// on the first one would make the feature useless.
			continue
		}

		for _, de := range entries {
			name := de.Name()
			if IsInternal(name) {
				continue
			}
			childVirtual := path.Join(frame.virtual, name)
			if v.Denied(childVirtual) {
				continue
			}
			fi, err := de.Info()
			if err != nil {
				// The entry vanished between readdir and stat; skip it.
				continue
			}
			if fi.Mode()&fs.ModeSymlink != 0 {
				continue
			}
			isDir := fi.IsDir()
			if !isDir && !fi.Mode().IsRegular() {
				// Sockets, pipes and device nodes hold no space to report.
				continue
			}

			report.Scanned++
			sinceCheck++
			if sinceCheck >= usageDeadlineEvery {
				sinceCheck = 0
				if err := ctx.Err(); err != nil {
					if errors.Is(err, context.Canceled) {
						return nil, err
					}
					report.Truncated = true
					break walk
				}
			}
			if opts.Progress != nil {
				if now := time.Now(); now.Sub(lastProgress) >= usageProgressEvery {
					lastProgress = now
					opts.Progress(report.Scanned, childVirtual)
				}
			}

			owner := frame.owner
			if owner < 0 {
				// A direct entry of the measured folder opens its own bucket.
				children = append(children, UsageNode{
					Name:  name,
					Path:  childVirtual,
					IsDir: isDir,
					Kind:  KindFor(name, isDir),
				})
				owner = len(children) - 1
			}

			if isDir {
				report.Folders++
				stack = append(stack, usageFrame{
					rel:     joinRel(frame.rel, name),
					virtual: childVirtual,
					owner:   owner,
				})
			} else {
				size := fi.Size()
				kind := KindFor(name, false)
				report.Bytes += size
				report.Files++
				children[owner].Bytes += size
				children[owner].Files++
				total, ok := kinds[kind]
				if !ok {
					total = &KindTotal{Kind: kind}
					kinds[kind] = total
				}
				total.Bytes += size
				total.Files++
				usageOffer(largest, opts.Limit, UsageNode{
					Name:  name,
					Path:  childVirtual,
					Bytes: size,
					Files: 1,
					Kind:  kind,
				})
			}

			if report.Scanned >= opts.MaxEntries {
				report.Truncated = true
				break walk
			}
		}
	}

	report.Children = usageRank(children, report.Bytes, opts.Limit)
	report.Largest = usageDrain(largest, report.Bytes)
	report.ByKind = usageKinds(kinds, report.Bytes)
	report.ElapsedMs = time.Since(started).Milliseconds()
	return report, nil
}

// usageRank sorts the direct children largest first, caps the list and fills
// in each share of the total.
func usageRank(nodes []UsageNode, total int64, limit int) []UsageNode {
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].Bytes != nodes[j].Bytes {
			return nodes[i].Bytes > nodes[j].Bytes
		}
		return naturalLess(nodes[i].Name, nodes[j].Name)
	})
	if limit > 0 && len(nodes) > limit {
		nodes = nodes[:limit]
	}
	out := make([]UsageNode, 0, len(nodes))
	for _, n := range nodes {
		n.Percent = usagePercent(n.Bytes, total)
		out = append(out, n)
	}
	return out
}

// usageKinds turns the running per family totals into a sorted list.
func usageKinds(kinds map[Kind]*KindTotal, total int64) []KindTotal {
	out := make([]KindTotal, 0, len(kinds))
	for _, t := range kinds {
		t.Percent = usagePercent(t.Bytes, total)
		out = append(out, *t)
	}
	// Map order is random, so the kind name settles ties to keep two reports
	// on the same folder identical.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Bytes != out[j].Bytes {
			return out[i].Bytes > out[j].Bytes
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// usageDrain empties the heap into a list ordered largest first.
func usageDrain(h *usageHeap, total int64) []UsageNode {
	out := make([]UsageNode, h.Len())
	for i := len(out) - 1; i >= 0; i-- {
		node := heap.Pop(h).(UsageNode)
		node.Percent = usagePercent(node.Bytes, total)
		out[i] = node
	}
	return out
}

// usagePercent returns part as a share of total, rounded to two decimals so
// the numbers stay short on the wire.
func usagePercent(part, total int64) float64 {
	if total <= 0 || part <= 0 {
		return 0
	}
	return math.Round(float64(part)/float64(total)*10000) / 100
}

// usageOffer keeps the largest files seen so far. The heap holds the smallest
// of them on top, so deciding what to drop is a single comparison.
func usageOffer(h *usageHeap, limit int, node UsageNode) {
	if limit <= 0 {
		return
	}
	if h.Len() < limit {
		heap.Push(h, node)
		return
	}
	if (*h)[0].Bytes >= node.Bytes {
		return
	}
	(*h)[0] = node
	heap.Fix(h, 0)
}

// usageHeap is a min heap of the largest files found so far.
type usageHeap []UsageNode

// Len reports how many files the heap holds.
func (h usageHeap) Len() int { return len(h) }

// Less puts the smallest file on top.
func (h usageHeap) Less(i, j int) bool { return h[i].Bytes < h[j].Bytes }

// Swap exchanges two files.
func (h usageHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

// Push adds a file to the heap.
func (h *usageHeap) Push(x any) { *h = append(*h, x.(UsageNode)) }

// Pop removes the smallest file from the heap.
func (h *usageHeap) Pop() any {
	old := *h
	n := len(old)
	node := old[n-1]
	*h = old[:n-1]
	return node
}
