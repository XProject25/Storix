// Package vfs is the guarded file system layer of Storix.
//
// Every path a request can reach is resolved against the mounts the acting
// user owns, and all I/O then runs through os.Root, which pins operations
// below the mount directory at the kernel level. That closes symlink escapes
// and the classic check-then-open race that plain path string checks leave open.
//
// Storix - modern web file manager for servers.
// Developed by X Project.
package vfs

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// Errors returned by the guarded layer.
var (
	ErrForbidden   = errors.New("vfs: path is outside the allowed area")
	ErrDenied      = errors.New("vfs: path is protected")
	ErrReadOnly    = errors.New("vfs: mount is read only")
	ErrNotFound    = errors.New("vfs: not found")
	ErrExists      = errors.New("vfs: already exists")
	ErrInvalidName = errors.New("vfs: invalid name")
	ErrNotDir      = errors.New("vfs: not a directory")
	ErrIsDir       = errors.New("vfs: is a directory")
	ErrTooLarge    = errors.New("vfs: file is too large")
)

// Mount is one directory tree an actor may work in.
type Mount struct {
	Path     string `json:"path"`
	Label    string `json:"label"`
	Icon     string `json:"icon"`
	ReadOnly bool   `json:"readOnly"`
}

// Scope is the set of mounts available to the current actor.
type Scope struct {
	Mounts []Mount
	Admin  bool
}

// Options configure the guarded layer.
type Options struct {
	// Denied paths are refused even when they sit inside a mount.
	Denied []string
	// TrashDir is the Storix owned directory that holds deleted items.
	TrashDir string
	// MaxTextBytes caps inline text reads and edits.
	MaxTextBytes int64
}

// VFS resolves virtual paths and caches one os.Root handle per mount.
type VFS struct {
	opts Options

	mu    sync.RWMutex
	roots map[string]*os.Root
}

// New builds a guarded file system layer.
func New(opts Options) *VFS {
	if opts.MaxTextBytes <= 0 {
		opts.MaxTextBytes = 8 << 20
	}
	cleaned := make([]string, 0, len(opts.Denied))
	for _, d := range opts.Denied {
		if d = Clean(d); d != "" {
			cleaned = append(cleaned, d)
		}
	}
	opts.Denied = cleaned
	return &VFS{opts: opts, roots: make(map[string]*os.Root)}
}

// Options exposes the active options.
func (v *VFS) Options() Options { return v.opts }

// Close releases every cached root handle.
func (v *VFS) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	for k, r := range v.roots {
		_ = r.Close()
		delete(v.roots, k)
	}
	return nil
}

// Forget drops the cached handle for a mount, for instance after the
// administrator removes it.
func (v *VFS) Forget(mountPath string) {
	mountPath = Clean(mountPath)
	v.mu.Lock()
	defer v.mu.Unlock()
	if r, ok := v.roots[mountPath]; ok {
		_ = r.Close()
		delete(v.roots, mountPath)
	}
}

// Location is a resolved, permission checked path.
type Location struct {
	// Virtual is the cleaned absolute path as the API and UI see it.
	Virtual string
	// Mount is the mount the path belongs to.
	Mount Mount
	// Root pins all I/O below the mount directory.
	Root *os.Root
	// Rel is the path relative to the mount, "." for the mount itself.
	Rel string
}

// ReadOnly reports whether writes are refused here.
func (l *Location) ReadOnly() bool { return l.Mount.ReadOnly }

// Name is the last path element.
func (l *Location) Name() string {
	if l.Rel == "." {
		return path.Base(l.Mount.Path)
	}
	return path.Base(l.Virtual)
}

// Parent returns the containing directory as a virtual path.
func (l *Location) Parent() string { return path.Dir(l.Virtual) }

// Join returns a child virtual path.
func (l *Location) Join(name string) string { return path.Join(l.Virtual, name) }

// OSPath is the real path on disk. It is correct for display and for logging,
// but I/O must go through Root so containment stays enforced.
func (l *Location) OSPath() string {
	if l.Rel == "." {
		return l.Mount.Path
	}
	return filepath.Join(l.Mount.Path, filepath.FromSlash(l.Rel))
}

// Clean normalizes a virtual path to an absolute slash separated form.
func Clean(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = strings.ReplaceAll(p, "\\", "/")
	// A Windows style drive prefix is kept so the server can also run locally.
	if runtime.GOOS == "windows" && len(p) > 1 && p[1] == ':' {
		drive := p[:2]
		rest := path.Clean("/" + strings.TrimPrefix(p[2:], "/"))
		return drive + rest
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	out := path.Clean(p)
	return out
}

// InternalPrefix marks the scratch files Storix creates while an operation is
// in flight, such as a resumable upload that has not landed yet. They live in
// the destination directory so the final step is an atomic same volume rename,
// and they are hidden from every listing and search.
const InternalPrefix = ".storix-"

// IsInternal reports whether a name belongs to Storix rather than the user.
func IsInternal(name string) bool { return strings.HasPrefix(name, InternalPrefix) }

// PartName is the scratch file name for an in flight upload.
func PartName(id string) string { return InternalPrefix + id + ".part" }

// ValidName rejects names that would break out of their directory.
func ValidName(name string) error {
	if name == "" || name == "." || name == ".." {
		return ErrInvalidName
	}
	if strings.ContainsAny(name, "/\\") || strings.ContainsRune(name, 0) {
		return ErrInvalidName
	}
	if len(name) > 255 {
		return ErrInvalidName
	}
	if strings.TrimSpace(name) == "" {
		return ErrInvalidName
	}
	return nil
}

// Contains reports whether child is parent itself or lives below it.
func Contains(parent, child string) bool {
	parent = Clean(parent)
	child = Clean(child)
	if parent == "" || child == "" {
		return false
	}
	if parent == child {
		return true
	}
	if parent == "/" {
		return strings.HasPrefix(child, "/")
	}
	return strings.HasPrefix(child, parent+"/")
}

// Denied reports whether a path is on the protected list.
func (v *VFS) Denied(p string) bool {
	p = Clean(p)
	if p == "" {
		return true
	}
	for _, d := range v.opts.Denied {
		if Contains(d, p) {
			return true
		}
	}
	// The Storix trash is managed through the trash API, never browsed raw.
	if v.opts.TrashDir != "" && Contains(Clean(v.opts.TrashDir), p) {
		return true
	}
	return false
}

// mountFor picks the most specific mount containing the path.
func mountFor(scope Scope, p string) (Mount, bool) {
	best := Mount{}
	found := false
	for _, m := range scope.Mounts {
		mp := Clean(m.Path)
		if mp == "" {
			continue
		}
		if Contains(mp, p) {
			if !found || len(mp) > len(Clean(best.Path)) {
				best = m
				best.Path = mp
				found = true
			}
		}
	}
	return best, found
}

// rootFor returns a cached os.Root for a mount directory.
func (v *VFS) rootFor(mountPath string) (*os.Root, error) {
	v.mu.RLock()
	r, ok := v.roots[mountPath]
	v.mu.RUnlock()
	if ok {
		return r, nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if r, ok := v.roots[mountPath]; ok {
		return r, nil
	}
	opened, err := os.OpenRoot(filepath.FromSlash(mountPath))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, mountPath)
		}
		return nil, err
	}
	v.roots[mountPath] = opened
	return opened, nil
}

// Resolve maps a virtual path onto a mount and returns a guarded handle.
func (v *VFS) Resolve(scope Scope, p string) (*Location, error) {
	cleaned := Clean(p)
	if cleaned == "" {
		return nil, ErrForbidden
	}
	if strings.ContainsRune(cleaned, 0) {
		return nil, ErrInvalidName
	}
	if v.Denied(cleaned) {
		return nil, ErrDenied
	}
	mount, ok := mountFor(scope, cleaned)
	if !ok {
		return nil, ErrForbidden
	}
	root, err := v.rootFor(mount.Path)
	if err != nil {
		return nil, err
	}
	rel := "."
	if cleaned != mount.Path {
		rel = strings.TrimPrefix(cleaned, strings.TrimSuffix(mount.Path, "/"))
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			rel = "."
		}
	}
	return &Location{Virtual: cleaned, Mount: mount, Root: root, Rel: rel}, nil
}

// ResolveWritable resolves a path and refuses read only mounts.
func (v *VFS) ResolveWritable(scope Scope, p string) (*Location, error) {
	loc, err := v.Resolve(scope, p)
	if err != nil {
		return nil, err
	}
	if loc.ReadOnly() {
		return nil, ErrReadOnly
	}
	return loc, nil
}

// ResolveChild resolves a new child inside a directory, validating the name.
func (v *VFS) ResolveChild(scope Scope, dir, name string) (*Location, error) {
	if err := ValidName(name); err != nil {
		return nil, err
	}
	return v.ResolveWritable(scope, path.Join(Clean(dir), name))
}

// Mounts returns the scope mounts sorted for display, with unreachable ones
// flagged by an empty label so the UI can grey them out.
func (v *VFS) Mounts(scope Scope) []Mount {
	out := make([]Mount, 0, len(scope.Mounts))
	for _, m := range scope.Mounts {
		m.Path = Clean(m.Path)
		if m.Label == "" {
			m.Label = path.Base(m.Path)
			if m.Path == "/" {
				m.Label = "Root volume"
			}
		}
		if m.Icon == "" {
			m.Icon = "folder"
		}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// TrashDir reports the Storix owned trash directory.
func (v *VFS) TrashDir() string { return v.opts.TrashDir }

// MaxTextBytes reports the inline edit ceiling.
func (v *VFS) MaxTextBytes() int64 { return v.opts.MaxTextBytes }
