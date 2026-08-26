package vfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"path"
	"sort"
	"time"
)

// Duplicate scan defaults. Finding copies means reading file contents, so the
// work is fenced in on every side: a floor on the file size, a ceiling on how
// many files one report considers, and a deadline.
const (
	// defaultDuplicateMinSize ignores anything below one kibibyte. A server
	// always holds thousands of tiny identical files, and removing them frees
	// nothing worth the risk of deleting the wrong one.
	defaultDuplicateMinSize = 1 << 10
	// defaultDuplicateGroups caps how many groups a report carries.
	defaultDuplicateGroups = 200
	// defaultDuplicateTimeout bounds the scan when the caller sets none.
	defaultDuplicateTimeout = 45 * time.Second
	// defaultDuplicateMaxFiles bounds how many files the walk considers.
	defaultDuplicateMaxFiles = 500000
	// duplicateHeadBytes is how much of a file the first hashing pass reads.
	duplicateHeadBytes = 64 << 10
	// duplicateBufBytes is the read buffer shared by both hashing passes.
	duplicateBufBytes = 1 << 20
	// duplicateProgressEvery throttles the progress callback so a fast scan
	// does not spend its time reporting on itself.
	duplicateProgressEvery = 300 * time.Millisecond
	// duplicateDeadlineEvery is how many entries pass between deadline checks
	// inside one very large directory.
	duplicateDeadlineEvery = 4096
)

// DuplicateFile is one copy of a file that exists more than once.
type DuplicateFile struct {
	Path     string    `json:"path"`
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// DuplicateGroup is a set of files that hold exactly the same content.
type DuplicateGroup struct {
	Hash  string `json:"hash"`
	Size  int64  `json:"size"`
	Count int    `json:"count"`
	// Wasted is what deleting every copy but one would free.
	Wasted int64           `json:"wasted"`
	Files  []DuplicateFile `json:"files"`
}

// DuplicateReport answers which files below one folder are stored twice.
type DuplicateReport struct {
	Path string `json:"path"`
	// Groups are the sets of identical files, biggest waste first.
	Groups []DuplicateGroup `json:"groups"`
	// Wasted is what every group together would free, including any group
	// beyond the group ceiling that is not listed.
	Wasted int64 `json:"wasted"`
	// Scanned is how many files the walk looked at.
	Scanned int `json:"scanned"`
	// Hashed is how many files had to be read to be compared. A file read
	// twice, once for its head and once in full, still counts once.
	Hashed    int   `json:"hashed"`
	Truncated bool  `json:"truncated"`
	ElapsedMs int64 `json:"elapsedMs"`
}

// DuplicateOptions tune one duplicate scan.
type DuplicateOptions struct {
	// MinSize is the smallest file worth comparing. Zero uses the package
	// default.
	MinSize int64
	// MaxGroups caps how many groups the report carries. Zero uses the package
	// default.
	MaxGroups int
	// Timeout bounds the scan. Zero uses the package default.
	Timeout time.Duration
	// MaxFiles bounds how many files are considered. Zero uses the package
	// default.
	MaxFiles int
	// Progress is called while the scan runs, a few times a second at most.
	Progress func(scanned, hashed int)
}

// dupCandidate is one file the walk found, kept until it is either paired off
// with another file or dropped.
type dupCandidate struct {
	rel      string
	virtual  string
	name     string
	size     int64
	modified time.Time
}

// dupFrame is one directory waiting to be visited.
type dupFrame struct {
	rel     string
	virtual string
}

// dupScan is the state of one report while it is being built.
type dupScan struct {
	v      *VFS
	loc    *Location
	opts   DuplicateOptions
	report *DuplicateReport
	// buf is reused by every hash so a scan of a large tree does not allocate
	// a fresh megabyte per file.
	buf          []byte
	lastProgress time.Time
}

// Duplicates finds the files below one folder that are stored more than once.
//
// Hashing a whole volume to answer this would be unacceptable, so the work is
// done in three passes, each one far cheaper than the one after it. The tree is
// walked once and every file is bucketed by size, which costs no reads at all:
// a size held by a single file can never be a duplicate and is dropped there.
// The files that survive have the first 64 KiB read and hashed, which splits
// almost every remaining bucket apart for the price of one disk seek per file.
// Only the files that still share both their size and their head are then read
// in full and hashed with sha256. Two files that agree on size and first 64 KiB
// are nearly always identical, but nearly is not good enough when the interface
// then offers to delete one of them.
//
// Symlinks are never followed, so a link is never mistaken for a second copy
// and a loop cannot stall the scan, and all I/O runs through the mount root so
// the walk cannot leave it. A directory the account cannot read is skipped
// rather than fatal. When the deadline or the file ceiling stops the work the
// report comes back marked truncated: a partial answer about a large volume is
// far more useful than an error.
func (v *VFS) Duplicates(ctx context.Context, scope Scope, p string, opts DuplicateOptions) (*DuplicateReport, error) {
	started := time.Now()
	if opts.MinSize <= 0 {
		opts.MinSize = defaultDuplicateMinSize
	}
	if opts.MaxGroups <= 0 {
		opts.MaxGroups = defaultDuplicateGroups
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = defaultDuplicateMaxFiles
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultDuplicateTimeout
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

	scan := &dupScan{
		v:      v,
		loc:    loc,
		opts:   opts,
		report: &DuplicateReport{Path: loc.Virtual, Groups: []DuplicateGroup{}},
		buf:    make([]byte, duplicateBufBytes),
	}

	bySize, err := scan.bucket(ctx)
	if err != nil {
		return nil, err
	}
	groups, err := scan.compare(ctx, bySize)
	if err != nil {
		return nil, err
	}

	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Wasted != groups[j].Wasted {
			return groups[i].Wasted > groups[j].Wasted
		}
		if groups[i].Size != groups[j].Size {
			return groups[i].Size > groups[j].Size
		}
		// The digest settles ties so two scans of the same folder agree.
		return groups[i].Hash < groups[j].Hash
	})
	for _, g := range groups {
		scan.report.Wasted += g.Wasted
	}
	if opts.MaxGroups > 0 && len(groups) > opts.MaxGroups {
		groups = groups[:opts.MaxGroups]
	}

	scan.report.Groups = groups
	scan.report.ElapsedMs = time.Since(started).Milliseconds()
	return scan.report, nil
}

// bucket walks the tree once and files every candidate under its size.
func (s *dupScan) bucket(ctx context.Context) (map[int64][]dupCandidate, error) {
	bySize := make(map[int64][]dupCandidate, 256)
	stack := []dupFrame{{rel: s.loc.Rel, virtual: s.loc.Virtual}}
	sinceCheck := 0

walk:
	for len(stack) > 0 {
		frame := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil, err
			}
			s.report.Truncated = true
			break walk
		}

		entries, err := readDirRel(s.loc.Root, frame.rel)
		if err != nil {
			// A directory the account cannot read holds nothing this report can
			// speak for. A scan of a whole volume will always meet a few of
			// them, and stopping on the first one would make the feature useless.
			continue
		}

		for _, de := range entries {
			name := de.Name()
			if IsInternal(name) {
				continue
			}
			childVirtual := path.Join(frame.virtual, name)
			if s.v.Denied(childVirtual) {
				continue
			}
			fi, err := de.Info()
			if err != nil {
				// The entry vanished between readdir and stat; skip it.
				continue
			}
			if fi.Mode()&fs.ModeSymlink != 0 {
				// A link is a second name for a file that is already counted,
				// not a second copy of it, and following one could leave the
				// tree or run in a circle.
				continue
			}
			if fi.IsDir() {
				stack = append(stack, dupFrame{rel: joinRel(frame.rel, name), virtual: childVirtual})
				continue
			}
			if !fi.Mode().IsRegular() {
				// Sockets, pipes and device nodes hold no content to compare.
				continue
			}

			s.report.Scanned++
			sinceCheck++
			if sinceCheck >= duplicateDeadlineEvery {
				sinceCheck = 0
				if err := ctx.Err(); err != nil {
					if errors.Is(err, context.Canceled) {
						return nil, err
					}
					s.report.Truncated = true
					break walk
				}
			}
			s.tick()

			if size := fi.Size(); size >= s.opts.MinSize {
				bySize[size] = append(bySize[size], dupCandidate{
					rel:      joinRel(frame.rel, name),
					virtual:  childVirtual,
					name:     name,
					size:     size,
					modified: fi.ModTime().UTC(),
				})
			}

			if s.report.Scanned >= s.opts.MaxFiles {
				s.report.Truncated = true
				break walk
			}
		}
	}
	return bySize, nil
}

// compare runs the two hashing passes over the buckets that hold more than one
// file and returns the sets that proved identical.
func (s *dupScan) compare(ctx context.Context, bySize map[int64][]dupCandidate) ([]DuplicateGroup, error) {
	sizes := make([]int64, 0, len(bySize))
	for size, files := range bySize {
		// One file of a given size has nothing to be identical to, so it is
		// dropped without a byte being read.
		if len(files) > 1 {
			sizes = append(sizes, size)
		}
	}
	// Largest first, so a scan that runs out of time has already looked at the
	// files that would free the most space.
	sort.Slice(sizes, func(i, j int) bool { return sizes[i] > sizes[j] })

	groups := make([]DuplicateGroup, 0, 16)

compare:
	for _, size := range sizes {
		heads := make(map[string][]dupCandidate, len(bySize[size]))
		for _, c := range bySize[size] {
			if err := ctx.Err(); err != nil {
				if errors.Is(err, context.Canceled) {
					return nil, err
				}
				s.report.Truncated = true
				break compare
			}
			sum, ok := s.digest(c.rel, duplicateHeadBytes)
			if !ok {
				continue
			}
			s.report.Hashed++
			s.tick()
			heads[sum] = append(heads[sum], c)
		}

		for head, sharing := range heads {
			if len(sharing) < 2 {
				continue
			}
			if size <= duplicateHeadBytes {
				// The first pass already read the whole file, so its digest is
				// the digest of the content and there is nothing left to prove.
				groups = append(groups, dupGroupFor(head, size, sharing))
				continue
			}
			full := make(map[string][]dupCandidate, len(sharing))
			for _, c := range sharing {
				if err := ctx.Err(); err != nil {
					if errors.Is(err, context.Canceled) {
						return nil, err
					}
					s.report.Truncated = true
					break compare
				}
				sum, ok := s.digest(c.rel, 0)
				if !ok {
					continue
				}
				s.tick()
				full[sum] = append(full[sum], c)
			}
			for sum, same := range full {
				if len(same) > 1 {
					groups = append(groups, dupGroupFor(sum, size, same))
				}
			}
		}
	}
	return groups, nil
}

// digest hashes a file through the guarded root, reading at most limit bytes,
// or the whole file when limit is zero. A file that cannot be opened or read is
// reported as unusable rather than as an error: one locked file must not sink
// a report about a thousand others.
func (s *dupScan) digest(rel string, limit int64) (string, bool) {
	f, err := s.loc.Root.Open(rel)
	if err != nil {
		return "", false
	}
	defer f.Close()

	var src io.Reader = f
	if limit > 0 {
		src = io.LimitReader(f, limit)
	}
	h := sha256.New()
	if _, err := io.CopyBuffer(h, src, s.buf); err != nil {
		return "", false
	}
	return hex.EncodeToString(h.Sum(nil)), true
}

// tick reports progress a few times a second at most.
func (s *dupScan) tick() {
	if s.opts.Progress == nil {
		return
	}
	now := time.Now()
	if now.Sub(s.lastProgress) < duplicateProgressEvery {
		return
	}
	s.lastProgress = now
	s.opts.Progress(s.report.Scanned, s.report.Hashed)
}

// dupGroupFor turns a set of files that share content into one report row.
// Wasted counts every copy but one, because one copy is the one you keep.
func dupGroupFor(hash string, size int64, files []dupCandidate) DuplicateGroup {
	group := DuplicateGroup{
		Hash:   hash,
		Size:   size,
		Count:  len(files),
		Wasted: int64(len(files)-1) * size,
		Files:  make([]DuplicateFile, 0, len(files)),
	}
	for _, c := range files {
		group.Files = append(group.Files, DuplicateFile{
			Path:     c.virtual,
			Name:     c.name,
			Size:     c.size,
			Modified: c.modified,
		})
	}
	// The path orders the copies, so the same group always reads the same way.
	sort.Slice(group.Files, func(i, j int) bool { return group.Files[i].Path < group.Files[j].Path })
	return group
}
