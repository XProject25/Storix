package vfs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"
)

// Search defaults. They keep an unbounded query from walking a whole server.
const (
	defaultSearchResults = 500
	defaultSearchTimeout = 30 * time.Second
	// searchContentLimit caps how much of a file the content grep reads.
	searchContentLimit = 2 << 20
)

// SearchOptions describe one recursive lookup.
type SearchOptions struct {
	// Query is the name fragment, or a glob when it holds * or ?.
	Query string
	// Path limits the walk to one directory. Empty means every mount in scope.
	Path string
	// Kinds keeps only the listed file families. Empty keeps all of them.
	Kinds []Kind
	// MaxResults caps the number of rows returned.
	MaxResults int
	// Timeout bounds the walk. Zero uses the package default.
	Timeout time.Duration
	// IncludeHidden also visits dot files and dot directories.
	IncludeHidden bool
	// MinSize and MaxSize bound file sizes in bytes. Zero disables the bound.
	MinSize int64
	MaxSize int64
	// ModifiedAfter and ModifiedBefore bound the modification time.
	ModifiedAfter  time.Time
	ModifiedBefore time.Time
	// MaxDepth limits how deep the walk goes below the start. Zero is unlimited.
	MaxDepth int
	// Content also greps inside small text and code files.
	Content bool
	// CaseSensitive matches the query exactly as typed.
	CaseSensitive bool
}

// SearchResult is what a lookup found.
type SearchResult struct {
	Entries   []Entry       `json:"entries"`
	Truncated bool          `json:"truncated"`
	Scanned   int           `json:"scanned"`
	Elapsed   time.Duration `json:"-"`
	ElapsedMs int64         `json:"elapsedMs"`
	Query     string        `json:"query"`
}

// searchNode is one directory waiting to be visited.
type searchNode struct {
	root    *os.Root
	rel     string
	virtual string
	mount   Mount
	depth   int
}

// Search walks the scope and returns the entries that match.
//
// The walk is breadth first, so a hit two directories down is reported before
// one buried ten levels deeper, which is what a person scanning the result
// list expects. Symlinked directories are never followed, so a loop cannot
// stall the walk.
func (v *VFS) Search(ctx context.Context, scope Scope, opts SearchOptions) (*SearchResult, error) {
	started := time.Now()
	if opts.MaxResults <= 0 {
		opts.MaxResults = defaultSearchResults
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultSearchTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	queue, err := v.searchRoots(scope, opts.Path)
	if err != nil {
		return nil, err
	}

	match := newSearchMatcher(opts.Query, opts.CaseSensitive)
	kinds := kindSet(opts.Kinds)
	out := &SearchResult{Entries: make([]Entry, 0, 32), Query: opts.Query}

walk:
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil, err
			}
			// The deadline ran out. Report what was found so far.
			out.Truncated = true
			break walk
		}
		node := queue[0]
		queue = queue[1:]

		entries, err := readDirRel(node.root, node.rel)
		if err != nil {
			// An unreadable directory is skipped, not fatal: a search across a
			// whole volume will always meet a few of them.
			continue
		}

		for _, de := range entries {
			name := de.Name()
			childVirtual := path.Join(node.virtual, name)
			if v.Denied(childVirtual) {
				continue
			}
			hidden := strings.HasPrefix(name, ".")
			if hidden && !opts.IncludeHidden {
				continue
			}
			info, err := de.Info()
			if err != nil {
				continue
			}
			out.Scanned++
			childRel := joinRel(node.rel, name)

			if match.name(name) || searchContent(node.root, childRel, name, info, opts, match) {
				entry := v.entryFor(node.root, childRel, childVirtual, info, node.mount.ReadOnly)
				if searchKeeps(entry, kinds, opts) {
					out.Entries = append(out.Entries, entry)
					if len(out.Entries) >= opts.MaxResults {
						out.Truncated = true
						break walk
					}
				}
			}

			if !de.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
				continue
			}
			if opts.MaxDepth > 0 && node.depth+1 >= opts.MaxDepth {
				continue
			}
			queue = append(queue, searchNode{
				root:    node.root,
				rel:     childRel,
				virtual: childVirtual,
				mount:   node.mount,
				depth:   node.depth + 1,
			})
		}
	}

	out.Elapsed = time.Since(started)
	out.ElapsedMs = out.Elapsed.Milliseconds()
	return out, nil
}

// searchRoots turns the requested start point into the initial queue.
func (v *VFS) searchRoots(scope Scope, start string) ([]searchNode, error) {
	if strings.TrimSpace(start) != "" {
		loc, err := v.Resolve(scope, start)
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
		return []searchNode{{root: loc.Root, rel: loc.Rel, virtual: loc.Virtual, mount: loc.Mount}}, nil
	}

	mounts := v.Mounts(scope)
	nodes := make([]searchNode, 0, len(mounts))
	for _, m := range mounts {
		// An overlapping mount would be walked twice, once per entry point.
		if coveredByAnother(m, mounts) {
			continue
		}
		loc, err := v.Resolve(scope, m.Path)
		if err != nil {
			// A mount that is missing or protected simply contributes nothing.
			continue
		}
		nodes = append(nodes, searchNode{root: loc.Root, rel: loc.Rel, virtual: loc.Virtual, mount: loc.Mount})
	}
	if len(nodes) == 0 {
		return nil, ErrForbidden
	}
	return nodes, nil
}

// coveredByAnother reports whether a mount already sits inside another one.
func coveredByAnother(m Mount, all []Mount) bool {
	for _, other := range all {
		if other.Path == m.Path {
			continue
		}
		if Contains(other.Path, m.Path) {
			return true
		}
	}
	return false
}

// readDirRel reads one directory through a guarded root.
func readDirRel(root *os.Root, rel string) ([]fs.DirEntry, error) {
	dir, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return entries, nil
}

// searchKeeps applies the non name filters to a candidate row.
func searchKeeps(e Entry, kinds map[Kind]bool, opts SearchOptions) bool {
	if len(kinds) > 0 && !kinds[e.Kind] {
		return false
	}
	if !e.IsDir {
		if opts.MinSize > 0 && e.Size < opts.MinSize {
			return false
		}
		if opts.MaxSize > 0 && e.Size > opts.MaxSize {
			return false
		}
	}
	if !opts.ModifiedAfter.IsZero() && e.Modified.Before(opts.ModifiedAfter) {
		return false
	}
	if !opts.ModifiedBefore.IsZero() && e.Modified.After(opts.ModifiedBefore) {
		return false
	}
	return true
}

// searchContent greps inside a small text or code file.
func searchContent(root *os.Root, rel, name string, info fs.FileInfo, opts SearchOptions, match searchMatcher) bool {
	if !opts.Content || match.empty {
		return false
	}
	if info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false
	}
	if info.Size() == 0 || info.Size() > searchContentLimit {
		return false
	}
	switch KindFor(name, false) {
	case KindText, KindCode:
	default:
		return false
	}
	f, err := root.Open(rel)
	if err != nil {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(f, searchContentLimit))
	f.Close()
	if err != nil || looksBinary(data) {
		return false
	}
	return match.content(data)
}

// kindSet turns the requested kinds into a lookup table.
func kindSet(kinds []Kind) map[Kind]bool {
	if len(kinds) == 0 {
		return nil
	}
	set := make(map[Kind]bool, len(kinds))
	for _, k := range kinds {
		if k != "" {
			set[k] = true
		}
	}
	return set
}

// searchMatcher decides whether a name or a buffer matches the query.
type searchMatcher struct {
	raw           string
	lower         string
	glob          bool
	caseSensitive bool
	empty         bool
}

// newSearchMatcher prepares the comparison once per search instead of once
// per candidate.
func newSearchMatcher(query string, caseSensitive bool) searchMatcher {
	query = strings.TrimSpace(query)
	return searchMatcher{
		raw:           query,
		lower:         strings.ToLower(query),
		glob:          strings.ContainsAny(query, "*?"),
		caseSensitive: caseSensitive,
		empty:         query == "",
	}
}

// name reports whether a file name matches. An empty query matches everything
// so the other filters can stand on their own, for instance to list every
// image below a directory.
func (m searchMatcher) name(name string) bool {
	if m.empty {
		return true
	}
	if m.glob {
		pattern, subject := m.raw, name
		if !m.caseSensitive {
			pattern, subject = m.lower, strings.ToLower(name)
		}
		if ok, err := path.Match(pattern, subject); err == nil {
			return ok
		}
		// A malformed pattern falls back to a plain substring match.
	}
	if m.caseSensitive {
		return strings.Contains(name, m.raw)
	}
	return strings.Contains(strings.ToLower(name), m.lower)
}

// content reports whether a file body contains the query. Glob syntax is not
// applied here, since a wildcard has no meaning inside a line of text.
func (m searchMatcher) content(data []byte) bool {
	if m.empty {
		return false
	}
	if m.caseSensitive {
		return bytes.Contains(data, []byte(m.raw))
	}
	return bytes.Contains(bytes.ToLower(data), []byte(m.lower))
}
