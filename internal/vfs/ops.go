package vfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

// ListOptions tune a directory read.
type ListOptions struct {
	ShowHidden   bool
	Sort         string
	Order        string
	FoldersFirst bool
	Filter       string
	Limit        int
}

// Conflict decides what happens when a destination name is taken.
type Conflict string

// Conflict policies.
const (
	ConflictRename    Conflict = "rename"
	ConflictOverwrite Conflict = "overwrite"
	ConflictSkip      Conflict = "skip"
	ConflictFail      Conflict = "fail"
)

// Progress is called while long operations run. Any field may be zero when
// it is not known yet.
type Progress func(p ProgressState)

// ProgressState is a snapshot of a running operation.
type ProgressState struct {
	Bytes      int64
	TotalBytes int64
	Items      int64
	TotalItems int64
	Current    string
}

func (p Progress) report(s ProgressState) {
	if p != nil {
		p(s)
	}
}

// OpResult summarizes a bulk operation.
type OpResult struct {
	Items   int64    `json:"items"`
	Bytes   int64    `json:"bytes"`
	Skipped int64    `json:"skipped"`
	Renamed []string `json:"renamed,omitempty"`
	Errors  []string `json:"errors,omitempty"`
}

// Stat describes a single path.
func (v *VFS) Stat(scope Scope, p string) (Entry, error) {
	loc, err := v.Resolve(scope, p)
	if err != nil {
		return Entry{}, err
	}
	info, err := loc.Root.Lstat(loc.Rel)
	if err != nil {
		return Entry{}, mapErr(err)
	}
	return v.entryFor(loc.Root, loc.Rel, loc.Virtual, info, loc.ReadOnly()), nil
}

// entryFor builds a browser row from a stat result.
func (v *VFS) entryFor(root *os.Root, rel, virtual string, info fs.FileInfo, readOnly bool) Entry {
	name := path.Base(virtual)
	if name == "/" || name == "." {
		name = virtual
	}
	e := Entry{
		Name:      name,
		Path:      virtual,
		IsDir:     info.IsDir(),
		Size:      info.Size(),
		Modified:  info.ModTime().UTC(),
		Mode:      modeString(info.Mode()),
		ModeOctal: fmt.Sprintf("%04o", info.Mode().Perm()),
		Ext:       strings.ToLower(strings.TrimPrefix(path.Ext(name), ".")),
		Hidden:    strings.HasPrefix(name, "."),
		ReadOnly:  readOnly,
	}
	e.UID, e.GID, e.Owner, e.Group = ownerOf(info)

	if info.Mode()&fs.ModeSymlink != 0 {
		e.Symlink = true
		if target, err := root.Readlink(rel); err == nil {
			e.LinkTarget = target
		}
		// Resolving through Root keeps the target inside the mount.
		if target, err := root.Stat(rel); err == nil {
			e.IsDir = target.IsDir()
			e.Size = target.Size()
			e.Modified = target.ModTime().UTC()
			e.Mode = modeString(target.Mode())
			e.ModeOctal = fmt.Sprintf("%04o", target.Mode().Perm())
		} else {
			e.Broken = true
		}
	}

	e.Kind = KindFor(name, e.IsDir)
	e.MIME = MIMEFor(name, e.Kind)
	e.Previewable = previewable(e.Kind)
	e.Editable = !e.IsDir && editable(e.Kind, e.Size, v.opts.MaxTextBytes)
	e.Thumbnail = thumbnailable(e.Kind, name)
	return e
}

// List reads a directory.
func (v *VFS) List(scope Scope, p string, opts ListOptions) (*Listing, error) {
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
	dir, err := loc.Root.Open(loc.Rel)
	if err != nil {
		return nil, mapErr(err)
	}
	defer dir.Close()

	names, err := dir.ReadDir(-1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, mapErr(err)
	}

	filter := strings.ToLower(strings.TrimSpace(opts.Filter))
	out := &Listing{
		Path:     loc.Virtual,
		Parent:   parentOf(loc.Virtual, loc.Mount.Path),
		Mount:    loc.Mount,
		ReadOnly: loc.ReadOnly(),
		Entries:  make([]Entry, 0, len(names)),
	}

	for _, de := range names {
		name := de.Name()
		if IsInternal(name) {
			continue
		}
		childVirtual := path.Join(loc.Virtual, name)
		if v.Denied(childVirtual) {
			continue
		}
		hidden := strings.HasPrefix(name, ".")
		if hidden {
			out.Hidden++
			if !opts.ShowHidden {
				continue
			}
		}
		if filter != "" && !strings.Contains(strings.ToLower(name), filter) {
			continue
		}
		fi, err := de.Info()
		if err != nil {
			// The file vanished between readdir and stat; skip it quietly.
			continue
		}
		childRel := name
		if loc.Rel != "." {
			childRel = loc.Rel + "/" + name
		}
		entry := v.entryFor(loc.Root, childRel, childVirtual, fi, loc.ReadOnly())
		if entry.IsDir {
			out.Folders++
		} else {
			out.Files++
			out.Size += entry.Size
		}
		out.Entries = append(out.Entries, entry)
	}

	out.Total = len(out.Entries)
	SortEntries(out.Entries, opts.Sort, opts.Order, opts.FoldersFirst || opts.Sort == "")
	if opts.Limit > 0 && len(out.Entries) > opts.Limit {
		out.Entries = out.Entries[:opts.Limit]
		out.Truncated = true
	}
	return out, nil
}

// parentOf returns the parent path, stopping at the mount boundary.
func parentOf(p, mount string) string {
	p = Clean(p)
	mount = Clean(mount)
	if p == mount || p == "/" {
		return ""
	}
	return path.Dir(p)
}

// Mkdir creates a directory inside dir.
func (v *VFS) Mkdir(scope Scope, dir, name string) (Entry, error) {
	loc, err := v.ResolveChild(scope, dir, name)
	if err != nil {
		return Entry{}, err
	}
	if err := loc.Root.Mkdir(loc.Rel, 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return Entry{}, ErrExists
		}
		return Entry{}, mapErr(err)
	}
	return v.Stat(scope, loc.Virtual)
}

// MkdirAll creates a directory and any missing parents inside a mount.
func (v *VFS) MkdirAll(scope Scope, p string) (Entry, error) {
	loc, err := v.ResolveWritable(scope, p)
	if err != nil {
		return Entry{}, err
	}
	if err := loc.Root.MkdirAll(loc.Rel, 0o755); err != nil {
		return Entry{}, mapErr(err)
	}
	return v.Stat(scope, loc.Virtual)
}

// Rename renames a file or folder in place.
func (v *VFS) Rename(scope Scope, p, newName string) (Entry, error) {
	if err := ValidName(newName); err != nil {
		return Entry{}, err
	}
	loc, err := v.ResolveWritable(scope, p)
	if err != nil {
		return Entry{}, err
	}
	if loc.Rel == "." {
		return Entry{}, ErrForbidden
	}
	target, err := v.ResolveWritable(scope, path.Join(path.Dir(loc.Virtual), newName))
	if err != nil {
		return Entry{}, err
	}
	if _, err := target.Root.Lstat(target.Rel); err == nil {
		return Entry{}, ErrExists
	}
	if err := loc.Root.Rename(loc.Rel, target.Rel); err != nil {
		return Entry{}, mapErr(err)
	}
	return v.Stat(scope, target.Virtual)
}

// Open opens a file for reading. The caller closes it.
func (v *VFS) Open(scope Scope, p string) (*os.File, fs.FileInfo, error) {
	loc, err := v.Resolve(scope, p)
	if err != nil {
		return nil, nil, err
	}
	f, err := loc.Root.Open(loc.Rel)
	if err != nil {
		return nil, nil, mapErr(err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, mapErr(err)
	}
	if info.IsDir() {
		f.Close()
		return nil, nil, ErrIsDir
	}
	return f, info, nil
}

// OpenLocation opens a file for reading from an already resolved location.
func (v *VFS) OpenLocation(loc *Location) (*os.File, fs.FileInfo, error) {
	f, err := loc.Root.Open(loc.Rel)
	if err != nil {
		return nil, nil, mapErr(err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, mapErr(err)
	}
	return f, info, nil
}

// Create makes a new file for writing.
func (v *VFS) Create(scope Scope, p string, overwrite bool) (*os.File, string, error) {
	loc, err := v.ResolveWritable(scope, p)
	if err != nil {
		return nil, "", err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if !overwrite {
		flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
	}
	f, err := loc.Root.OpenFile(loc.Rel, flags, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, "", ErrExists
		}
		return nil, "", mapErr(err)
	}
	return f, loc.Virtual, nil
}

// TextFile is an inline editor payload.
type TextFile struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Content   string `json:"content"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated"`
	Binary    bool   `json:"binary"`
	Language  string `json:"language"`
	ReadOnly  bool   `json:"readOnly"`
	Modified  string `json:"modified"`
}

// ReadText loads a file for the built in editor.
func (v *VFS) ReadText(scope Scope, p string) (*TextFile, error) {
	loc, err := v.Resolve(scope, p)
	if err != nil {
		return nil, err
	}
	f, info, err := v.OpenLocation(loc)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info.IsDir() {
		return nil, ErrIsDir
	}
	limit := v.opts.MaxTextBytes
	truncated := info.Size() > limit
	reader := io.LimitReader(f, limit)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, mapErr(err)
	}
	out := &TextFile{
		Path:      loc.Virtual,
		Name:      path.Base(loc.Virtual),
		Size:      info.Size(),
		Truncated: truncated,
		Binary:    looksBinary(data),
		Language:  LanguageFor(path.Base(loc.Virtual)),
		ReadOnly:  loc.ReadOnly(),
		Modified:  info.ModTime().UTC().Format(time.RFC3339),
	}
	if !out.Binary {
		out.Content = string(data)
	}
	return out, nil
}

// WriteText saves editor content, preserving the existing file mode.
func (v *VFS) WriteText(scope Scope, p, content string) (Entry, error) {
	loc, err := v.ResolveWritable(scope, p)
	if err != nil {
		return Entry{}, err
	}
	if int64(len(content)) > v.opts.MaxTextBytes {
		return Entry{}, ErrTooLarge
	}
	mode := fs.FileMode(0o644)
	if info, err := loc.Root.Stat(loc.Rel); err == nil {
		if info.IsDir() {
			return Entry{}, ErrIsDir
		}
		mode = info.Mode().Perm()
	}
	// Write through a sibling temp file so a failed save cannot truncate the
	// original, then swap it into place.
	tmpRel := loc.Rel + ".storix-tmp"
	f, err := loc.Root.OpenFile(tmpRel, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return Entry{}, mapErr(err)
	}
	if _, err := io.WriteString(f, content); err != nil {
		f.Close()
		_ = loc.Root.Remove(tmpRel)
		return Entry{}, mapErr(err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = loc.Root.Remove(tmpRel)
		return Entry{}, mapErr(err)
	}
	if err := f.Close(); err != nil {
		_ = loc.Root.Remove(tmpRel)
		return Entry{}, mapErr(err)
	}
	if err := loc.Root.Rename(tmpRel, loc.Rel); err != nil {
		_ = loc.Root.Remove(tmpRel)
		return Entry{}, mapErr(err)
	}
	return v.Stat(scope, loc.Virtual)
}

// Copy duplicates sources into dest.
func (v *VFS) Copy(ctx context.Context, scope Scope, sources []string, dest string, policy Conflict, progress Progress) (*OpResult, error) {
	return v.transfer(ctx, scope, sources, dest, policy, progress, false)
}

// Move relocates sources into dest, using a rename when both sides share a mount.
func (v *VFS) Move(ctx context.Context, scope Scope, sources []string, dest string, policy Conflict, progress Progress) (*OpResult, error) {
	return v.transfer(ctx, scope, sources, dest, policy, progress, true)
}

func (v *VFS) transfer(ctx context.Context, scope Scope, sources []string, dest string, policy Conflict, progress Progress, move bool) (*OpResult, error) {
	destLoc, err := v.ResolveWritable(scope, dest)
	if err != nil {
		return nil, err
	}
	if info, err := destLoc.Root.Stat(destLoc.Rel); err != nil {
		return nil, mapErr(err)
	} else if !info.IsDir() {
		return nil, ErrNotDir
	}
	if policy == "" {
		policy = ConflictRename
	}

	result := &OpResult{}
	var totalBytes, totalItems int64
	plans := make([]*transferPlan, 0, len(sources))

	for _, src := range sources {
		srcLoc, err := v.Resolve(scope, src)
		if err != nil {
			return nil, err
		}
		if move && srcLoc.ReadOnly() {
			return nil, ErrReadOnly
		}
		if srcLoc.Rel == "." {
			return nil, ErrForbidden
		}
		if Contains(srcLoc.Virtual, destLoc.Virtual) {
			return nil, fmt.Errorf("%w: cannot copy a folder into itself", ErrForbidden)
		}
		info, err := srcLoc.Root.Lstat(srcLoc.Rel)
		if err != nil {
			return nil, mapErr(err)
		}
		bytes, items := measure(srcLoc.Root, srcLoc.Rel, info)
		totalBytes += bytes
		totalItems += items
		plans = append(plans, &transferPlan{src: srcLoc, info: info})
	}

	progress.report(ProgressState{TotalBytes: totalBytes, TotalItems: totalItems})

	var doneBytes, doneItems int64
	for _, plan := range plans {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		name := path.Base(plan.src.Virtual)
		targetName, skip, err := v.resolveConflict(destLoc, name, policy)
		if err != nil {
			return result, err
		}
		if skip {
			result.Skipped++
			continue
		}
		if targetName != name {
			result.Renamed = append(result.Renamed, path.Join(destLoc.Virtual, targetName))
		}
		targetRel := joinRel(destLoc.Rel, targetName)

		sameMount := plan.src.Mount.Path == destLoc.Mount.Path
		if move && sameMount {
			if err := plan.src.Root.Rename(plan.src.Rel, targetRel); err == nil {
				bytes, items := measure(destLoc.Root, targetRel, plan.info)
				doneBytes += bytes
				doneItems += items
				result.Items += items
				result.Bytes += bytes
				progress.report(ProgressState{Bytes: doneBytes, TotalBytes: totalBytes, Items: doneItems, TotalItems: totalItems, Current: name})
				continue
			}
			// Rename can still fail across bind mounts; fall through to a copy.
		}

		counter := &progressCounter{
			progress:   progress,
			baseBytes:  doneBytes,
			baseItems:  doneItems,
			totalBytes: totalBytes,
			totalItems: totalItems,
		}
		if err := copyTree(ctx, plan.src.Root, plan.src.Rel, destLoc.Root, targetRel, plan.info, counter); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", name, err))
			if errors.Is(err, context.Canceled) {
				return result, err
			}
			continue
		}
		doneBytes = counter.baseBytes + counter.bytes
		doneItems = counter.baseItems + counter.items
		result.Bytes += counter.bytes
		result.Items += counter.items

		if move {
			if err := plan.src.Root.RemoveAll(plan.src.Rel); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", name, err))
			}
		}
	}
	return result, nil
}

type transferPlan struct {
	src  *Location
	info fs.FileInfo
}

type progressCounter struct {
	progress   Progress
	baseBytes  int64
	baseItems  int64
	totalBytes int64
	totalItems int64
	bytes      int64
	items      int64
	last       time.Time
}

func (c *progressCounter) addBytes(n int64, current string) {
	c.bytes += n
	now := time.Now()
	if now.Sub(c.last) < 120*time.Millisecond {
		return
	}
	c.last = now
	c.progress.report(ProgressState{
		Bytes:      c.baseBytes + c.bytes,
		TotalBytes: c.totalBytes,
		Items:      c.baseItems + c.items,
		TotalItems: c.totalItems,
		Current:    current,
	})
}

func (c *progressCounter) addItem(current string) {
	c.items++
	c.progress.report(ProgressState{
		Bytes:      c.baseBytes + c.bytes,
		TotalBytes: c.totalBytes,
		Items:      c.baseItems + c.items,
		TotalItems: c.totalItems,
		Current:    current,
	})
}

// resolveConflict applies the conflict policy and returns the name to use.
func (v *VFS) resolveConflict(dest *Location, name string, policy Conflict) (string, bool, error) {
	rel := joinRel(dest.Rel, name)
	if _, err := dest.Root.Lstat(rel); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return name, false, nil
		}
		return "", false, mapErr(err)
	}
	switch policy {
	case ConflictSkip:
		return "", true, nil
	case ConflictOverwrite:
		return name, false, nil
	case ConflictFail:
		return "", false, ErrExists
	default:
		ext := path.Ext(name)
		base := strings.TrimSuffix(name, ext)
		for i := 2; i < 10000; i++ {
			candidate := fmt.Sprintf("%s (%d)%s", base, i, ext)
			if _, err := dest.Root.Lstat(joinRel(dest.Rel, candidate)); errors.Is(err, os.ErrNotExist) {
				return candidate, false, nil
			}
		}
		return "", false, ErrExists
	}
}

// UniqueName returns a free name inside a directory.
func (v *VFS) UniqueName(scope Scope, dir, name string) (string, error) {
	loc, err := v.Resolve(scope, dir)
	if err != nil {
		return "", err
	}
	out, _, err := v.resolveConflict(loc, name, ConflictRename)
	return out, err
}

func joinRel(base, name string) string {
	if base == "." || base == "" {
		return name
	}
	return base + "/" + name
}

// measure walks a tree and returns its byte size and item count.
func measure(root *os.Root, rel string, info fs.FileInfo) (int64, int64) {
	if info.Mode()&fs.ModeSymlink != 0 {
		return 0, 1
	}
	if !info.IsDir() {
		return info.Size(), 1
	}
	var bytes, items int64
	items++
	dir, err := root.Open(rel)
	if err != nil {
		return bytes, items
	}
	entries, _ := dir.ReadDir(-1)
	dir.Close()
	for _, de := range entries {
		fi, err := de.Info()
		if err != nil {
			continue
		}
		b, i := measure(root, joinRel(rel, de.Name()), fi)
		bytes += b
		items += i
	}
	return bytes, items
}

// copyTree copies a file, directory or symlink between two guarded roots.
func copyTree(ctx context.Context, srcRoot *os.Root, srcRel string, dstRoot *os.Root, dstRel string, info fs.FileInfo, counter *progressCounter) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := srcRoot.Readlink(srcRel)
		if err != nil {
			return err
		}
		_ = dstRoot.Remove(dstRel)
		if err := dstRoot.Symlink(target, dstRel); err != nil {
			return err
		}
		counter.addItem(path.Base(srcRel))
		return nil

	case info.IsDir():
		if err := dstRoot.MkdirAll(dstRel, info.Mode().Perm()); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		counter.addItem(path.Base(srcRel))
		dir, err := srcRoot.Open(srcRel)
		if err != nil {
			return err
		}
		entries, err := dir.ReadDir(-1)
		dir.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		for _, de := range entries {
			fi, err := de.Info()
			if err != nil {
				continue
			}
			if err := copyTree(ctx, srcRoot, joinRel(srcRel, de.Name()), dstRoot, joinRel(dstRel, de.Name()), fi, counter); err != nil {
				return err
			}
		}
		return nil

	default:
		return copyFile(ctx, srcRoot, srcRel, dstRoot, dstRel, info, counter)
	}
}

func copyFile(ctx context.Context, srcRoot *os.Root, srcRel string, dstRoot *os.Root, dstRel string, info fs.FileInfo, counter *progressCounter) error {
	in, err := srcRoot.Open(srcRel)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := dstRoot.OpenFile(dstRel, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	name := path.Base(srcRel)
	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			out.Close()
			_ = dstRoot.Remove(dstRel)
			return err
		}
		n, readErr := in.Read(buf)
		if n > 0 {
			if _, writeErr := out.Write(buf[:n]); writeErr != nil {
				out.Close()
				return writeErr
			}
			counter.addBytes(int64(n), name)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			out.Close()
			return readErr
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	_ = dstRoot.Chtimes(dstRel, time.Now(), info.ModTime())
	counter.addItem(name)
	return nil
}

// Chmod changes permissions, optionally through a whole tree.
func (v *VFS) Chmod(ctx context.Context, scope Scope, p string, mode fs.FileMode, recursive bool) error {
	loc, err := v.ResolveWritable(scope, p)
	if err != nil {
		return err
	}
	if !recursive {
		return mapErr(loc.Root.Chmod(loc.Rel, mode.Perm()))
	}
	return walkRel(ctx, loc.Root, loc.Rel, func(rel string, info fs.FileInfo) error {
		return loc.Root.Chmod(rel, mode.Perm())
	})
}

// Chown changes the owner, optionally through a whole tree.
func (v *VFS) Chown(ctx context.Context, scope Scope, p string, uid, gid int, recursive bool) error {
	loc, err := v.ResolveWritable(scope, p)
	if err != nil {
		return err
	}
	if !recursive {
		return mapErr(loc.Root.Lchown(loc.Rel, uid, gid))
	}
	return walkRel(ctx, loc.Root, loc.Rel, func(rel string, info fs.FileInfo) error {
		return loc.Root.Lchown(rel, uid, gid)
	})
}

// walkRel visits a path and everything under it.
func walkRel(ctx context.Context, root *os.Root, rel string, fn func(string, fs.FileInfo) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := root.Lstat(rel)
	if err != nil {
		return mapErr(err)
	}
	if err := fn(rel, info); err != nil {
		return mapErr(err)
	}
	if !info.IsDir() {
		return nil
	}
	dir, err := root.Open(rel)
	if err != nil {
		return mapErr(err)
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return mapErr(err)
	}
	for _, de := range entries {
		if err := walkRel(ctx, root, joinRel(rel, de.Name()), fn); err != nil {
			return err
		}
	}
	return nil
}

// DirSize computes the recursive size of a directory.
func (v *VFS) DirSize(ctx context.Context, scope Scope, p string) (int64, int64, error) {
	loc, err := v.Resolve(scope, p)
	if err != nil {
		return 0, 0, err
	}
	info, err := loc.Root.Lstat(loc.Rel)
	if err != nil {
		return 0, 0, mapErr(err)
	}
	if !info.IsDir() {
		return info.Size(), 1, nil
	}
	var bytes, items int64
	err = walkRel(ctx, loc.Root, loc.Rel, func(rel string, fi fs.FileInfo) error {
		if rel == loc.Rel {
			return nil
		}
		items++
		if !fi.IsDir() && fi.Mode()&fs.ModeSymlink == 0 {
			bytes += fi.Size()
		}
		return nil
	})
	return bytes, items, err
}

// ParseMode reads an octal permission string such as "0755" or "755".
func ParseMode(raw string) (fs.FileMode, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, ErrInvalidName
	}
	n, err := strconv.ParseUint(raw, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("vfs: invalid mode %q", raw)
	}
	if n > 0o7777 {
		return 0, fmt.Errorf("vfs: invalid mode %q", raw)
	}
	return fs.FileMode(n), nil
}

// looksBinary reports whether a buffer is unlikely to be text.
func looksBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	limit := min(len(data), 8000)
	for i := 0; i < limit; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}

// mapErr converts os errors into the package level sentinels.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, os.ErrNotExist):
		return ErrNotFound
	case errors.Is(err, os.ErrExist):
		return ErrExists
	case errors.Is(err, os.ErrPermission):
		return fmt.Errorf("%w: permission denied", ErrDenied)
	}
	// os.Root refuses a path that would leave the mount, which is exactly the
	// containment guarantee doing its job. Report it as a refusal rather than
	// letting it surface as an unexplained server error. The sentinel behind
	// it is unexported, so the message is the only thing to match on.
	if strings.Contains(err.Error(), "path escapes from parent") {
		return fmt.Errorf("%w: that link points outside this folder", ErrForbidden)
	}
	if strings.Contains(err.Error(), "too many levels of symbolic links") {
		return fmt.Errorf("%w: that link points to itself", ErrForbidden)
	}
	return err
}
