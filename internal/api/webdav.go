// WebDAV access, the network drive side of Storix.
//
// The same folders the browser shows are served over WebDAV, so Windows
// Explorer, the macOS Finder or a Linux file manager can map Storix as a drive
// and copy a file across with a drag.
//
// The tree starts one level above the mounts. Each mount the caller owns is a
// collection named after its label, and everything below it resolves through
// the guarded layer exactly as a request from the browser does, so containment,
// protected paths and read only folders behave the same on both sides.
//
// Every request carries its own credentials, either an API token or the account
// password, because a file manager has no session cookie and no way to answer a
// CSRF challenge.
//
// Storix - modern web file manager for servers.
// Developed by X Project.
package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/webdav"

	"github.com/XProject25/Storix/internal/auth"
	"github.com/XProject25/Storix/internal/store"
	"github.com/XProject25/Storix/internal/vfs"
)

// davPrefix is the URL space the network drive lives in. router.go mounts the
// handler at davPrefix + "/".
const davPrefix = "/dav"

// davRealm is the name a file manager shows in its sign in prompt.
const davRealm = `Basic realm="Storix", charset="UTF-8"`

// davErrOverQuota is what a write reports once the storage allowance of the
// account is spent. It is worded as a size refusal because that is the only
// shape of refusal a mounted drive can explain to the person copying files.
var davErrOverQuota = fmt.Errorf("%w: storage allowance is full", vfs.ErrTooLarge)

// webdavHandler serves the network drive. It authenticates the caller, builds
// the file system that account can see and hands the request to the WebDAV
// implementation, which owns the protocol itself.
func (a *API) webdavHandler() http.Handler {
	// Locks outlive a single request: Windows and the Finder take a lock, then
	// write, then release, so the lock system is shared by the whole server.
	locks := webdav.NewMemLS()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := a.davAuthenticate(w, r)
		if user == nil {
			return
		}
		// A drive request carries its credentials rather than a session, so the
		// account it resolved to is put on the request the way the middleware
		// does for the browser. It is what names the account in the trail.
		r = withUser(r, user, nil)

		scope, err := a.scopeFor(r.Context(), user)
		if err != nil {
			a.Logger.Error("webdav scope failed", "user", user.Username, "err", err)
			http.Error(w, "Storix cannot open your folders right now", http.StatusInternalServerError)
			return
		}

		fsys := a.davFileSystem(user, scope)
		if note := fsys.davRefuseWrite(r); note != "" {
			a.audit(r, "webdav.denied", r.URL.Path, note, false)
			http.Error(w, note, http.StatusForbidden)
			return
		}

		handler := &webdav.Handler{
			Prefix:     davPrefix,
			FileSystem: fsys,
			LockSystem: locks,
			Logger: func(req *http.Request, err error) {
				if err == nil {
					return
				}
				// A mounted drive probes constantly for files that were never
				// there, so this stays at debug rather than filling the log.
				a.Logger.Debug("webdav request refused",
					"method", req.Method, "path", req.URL.Path, "err", err)
			},
		}
		handler.ServeHTTP(w, r)
	})
}

// ---- authentication ---------------------------------------------------------

// davAuthenticate resolves the caller from the credentials on the request, or
// answers the request itself and returns nil.
func (a *API) davAuthenticate(w http.ResponseWriter, r *http.Request) *store.User {
	ip := a.clientIP(r)
	key := "webdav:" + ip

	// Failed attempts are slowed down per address, exactly as the sign in form
	// does. Only a failure spends an allowance, and asking how many are left
	// spends none: a drive opens several connections at once and makes hundreds
	// of calls a minute, and every one of them carries the credentials again, so
	// counting a request rather than a failure would have a working mount lock
	// itself out. It would also mean one caller's success wiping the failures
	// another has recorded from the same address.
	if a.loginLimiter.Remaining(key) <= 0 {
		if wait := a.loginLimiter.RetryAfter(key); wait > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		}
		a.audit(r, "webdav.denied", r.URL.Path, "too many attempts", false)
		http.Error(w, "Too many attempts, try again later", http.StatusTooManyRequests)
		return nil
	}

	user := a.TokenAuthUser(r)
	if user == nil {
		user = a.davPasswordUser(r)
	}
	if user == nil {
		a.loginLimiter.Observe(key)
		a.audit(r, "webdav.denied", r.URL.Path, "credentials refused", false)
		davChallenge(w)
		return nil
	}

	if !user.Can(store.PermView) {
		a.audit(r, "webdav.denied", r.URL.Path, "account cannot browse files", false)
		http.Error(w, "This account cannot browse files", http.StatusForbidden)
		return nil
	}
	return user
}

// davPasswordUser verifies Basic credentials against the account password, so a
// drive can be mapped with the same username and password the sign in form
// takes. It returns nil for anything it cannot verify.
func (a *API) davPasswordUser(r *http.Request) *store.User {
	username, password, ok := r.BasicAuth()
	username = strings.TrimSpace(username)
	if !ok || username == "" || password == "" {
		return nil
	}

	user, err := a.Store.GetUserByName(r.Context(), username)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			a.Logger.Error("webdav account lookup failed", "user", username, "err", err)
			return nil
		}
		// Spend the same work as a real verification before answering.
		_, _ = auth.VerifyPassword(authDummyHash(), password)
		return nil
	}
	if !user.Active || user.MustChangePassword {
		return nil
	}
	// An account with two factor sign in has no way to answer the challenge
	// over Basic credentials. It mounts with an API token as the password
	// instead, which TokenAuthUser has already tried by this point.
	if user.TOTPEnabled {
		return nil
	}

	valid, err := auth.VerifyPassword(user.PasswordHash, password)
	if err != nil {
		a.Logger.Error("stored password hash is unreadable", "user", user.Username, "err", err)
		return nil
	}
	if !valid {
		return nil
	}
	return user
}

// davChallenge asks for credentials and says nothing else, so a file manager
// shows its own prompt instead of rendering a page inside the drive window.
func davChallenge(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", davRealm)
	w.WriteHeader(http.StatusUnauthorized)
}

// ---- mount naming -----------------------------------------------------------

// davMount is one mount as it appears at the top of the WebDAV tree.
type davMount struct {
	// Slug is the path segment the mount answers to.
	Slug string
	// Mount is the guarded layer mount behind it.
	Mount vfs.Mount
}

// davSlugMax is the longest mount name the drive hands out, in bytes.
const davSlugMax = 64

// davSlug turns a mount label into a path segment that survives a URL and a
// desktop file manager: lower case, spaces become dashes, and anything else
// that could be mistaken for a separator is dropped.
func davSlug(label string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(label)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	slug := strings.Trim(b.String(), "-._")
	if slug == "" {
		return "mount"
	}
	if len(slug) > davSlugMax {
		// The cut is made on a character boundary. Half a character is not text
		// and does not survive being written into a listing, so the mount would
		// be named one thing in the listing and answer to another.
		cut := slug[:davSlugMax]
		for len(cut) > 0 {
			if r, size := utf8.DecodeLastRuneInString(cut); r != utf8.RuneError || size > 1 {
				break
			}
			cut = cut[:len(cut)-1]
		}
		slug = strings.Trim(cut, "-._")
		if slug == "" {
			return "mount"
		}
	}
	return slug
}

// davMountsFor names every mount in a scope. Two labels that reduce to the same
// slug are told apart by a number, decided once from the scope so the same
// account always sees the same names in the same order.
func davMountsFor(v *vfs.VFS, scope vfs.Scope) []davMount {
	listed := v.Mounts(scope)
	out := make([]davMount, 0, len(listed))
	taken := make([]string, 0, len(listed))
	for _, m := range listed {
		base := davSlug(m.Label)
		slug := base
		for n := 2; davSlugTaken(taken, slug); n++ {
			slug = base + "-" + strconv.Itoa(n)
		}
		taken = append(taken, slug)
		out = append(out, davMount{Slug: slug, Mount: m})
	}
	return out
}

// davSlugTaken reports whether a name is already spoken for. It compares the
// way davMountBySlug matches rather than byte for byte, because a name that
// resolves to an earlier mount must never be handed to a later one: the later
// mount would be listed and then be unreachable behind the earlier one.
func davSlugTaken(taken []string, slug string) bool {
	for _, t := range taken {
		if strings.EqualFold(t, slug) {
			return true
		}
	}
	return false
}

// ---- the file system --------------------------------------------------------

// davFS presents the mounts of one caller as a WebDAV tree. It is built per
// request, so the mapping from names to folders cannot drift while a request
// is being served.
type davFS struct {
	api   *API
	user  *store.User
	scope vfs.Scope

	mounts  []davMount
	entries []fs.FileInfo
	at      time.Time

	quotaCheck func(ctx context.Context, userID int64, size int64) (bool, int64, error)
	quotaAdd   func(ctx context.Context, userID int64, bytes int64)
}

// davFileSystem builds the WebDAV view of one account.
func (a *API) davFileSystem(user *store.User, scope vfs.Scope) *davFS {
	return &davFS{
		api:        a,
		user:       user,
		scope:      scope,
		mounts:     davMountsFor(a.VFS, scope),
		at:         a.startedAt,
		quotaCheck: a.UploadQuotaCheck(),
		quotaAdd:   a.UploadQuotaAdd(),
	}
}

// davTarget is a WebDAV name resolved onto the guarded layer.
type davTarget struct {
	// root marks the synthetic collection that holds the mounts.
	root bool
	// mount is the mount the name belongs to.
	mount davMount
	// loc is the guarded handle, nil for the root collection.
	loc *vfs.Location
}

// davSplitName breaks a WebDAV name into its mount segment and the rest. It
// reports false for anything that tries to climb out of the tree or reach the
// scratch files Storix keeps while an operation is in flight.
func davSplitName(name string) (slug, rest string, ok bool) {
	if strings.ContainsRune(name, 0) {
		return "", "", false
	}
	name = strings.ReplaceAll(name, "\\", "/")
	kept := make([]string, 0, 8)
	for _, part := range strings.Split(name, "/") {
		switch {
		case part == "" || part == ".":
			continue
		case part == "..":
			return "", "", false
		case vfs.IsInternal(part):
			return "", "", false
		}
		kept = append(kept, part)
	}
	if len(kept) == 0 {
		return "", "", true
	}
	return kept[0], strings.Join(kept[1:], "/"), true
}

// davMountBySlug finds a mount by its segment. The match ignores case, so a
// client that remembers the label as the person typed it still lands.
func (d *davFS) davMountBySlug(slug string) (davMount, bool) {
	for _, m := range d.mounts {
		if strings.EqualFold(m.Slug, slug) {
			return m, true
		}
	}
	return davMount{}, false
}

// davTargetFor resolves a WebDAV name. A name that leads nowhere the caller may
// go reports os.ErrNotExist, which is what the protocol turns into a 404.
func (d *davFS) davTargetFor(name string) (davTarget, error) {
	slug, rest, ok := davSplitName(name)
	if !ok {
		return davTarget{}, os.ErrNotExist
	}
	if slug == "" {
		return davTarget{root: true}, nil
	}
	mount, ok := d.davMountBySlug(slug)
	if !ok {
		return davTarget{}, os.ErrNotExist
	}
	virtual := vfs.Clean(mount.Mount.Path)
	if rest != "" {
		virtual = vfs.Clean(path.Join(virtual, rest))
	}
	loc, err := d.api.VFS.Resolve(d.scope, virtual)
	if err != nil {
		return davTarget{}, davMapErr(err)
	}
	return davTarget{mount: mount, loc: loc}, nil
}

// davMapErr turns a guarded layer refusal into the os error the WebDAV layer
// knows how to answer with.
func davMapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, vfs.ErrReadOnly):
		return os.ErrPermission
	case errors.Is(err, vfs.ErrExists):
		return os.ErrExist
	case errors.Is(err, vfs.ErrInvalidName):
		return os.ErrInvalid
	case errors.Is(err, vfs.ErrForbidden), errors.Is(err, vfs.ErrDenied), errors.Is(err, vfs.ErrNotFound):
		return os.ErrNotExist
	}
	return err
}

// Mkdir creates a collection inside a mount.
func (d *davFS) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	target, err := d.davTargetFor(name)
	if err != nil {
		return &os.PathError{Op: "mkdir", Path: name, Err: err}
	}
	if target.root || target.loc.ReadOnly() {
		return &os.PathError{Op: "mkdir", Path: name, Err: os.ErrPermission}
	}
	if target.loc.Rel == "." {
		return &os.PathError{Op: "mkdir", Path: name, Err: os.ErrExist}
	}
	return target.loc.Root.Mkdir(target.loc.Rel, 0o755)
}

// OpenFile opens a file or collection. All I/O runs through the mount root, so
// a symlink that points outside the mount cannot be followed out of it.
func (d *davFS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	target, err := d.davTargetFor(name)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: name, Err: err}
	}
	writing := flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0

	if target.root {
		if writing {
			return nil, &os.PathError{Op: "open", Path: name, Err: os.ErrPermission}
		}
		return &davRootDir{info: d.davRootInfo(), entries: d.davRootEntries()}, nil
	}
	if writing && target.loc.ReadOnly() {
		return nil, &os.PathError{Op: "open", Path: name, Err: os.ErrPermission}
	}

	limit := int64(-1)
	created := false
	if writing {
		existing, statErr := target.loc.Root.Lstat(target.loc.Rel)
		created = errors.Is(statErr, os.ErrNotExist)
		limit, err = d.davAllowance(ctx)
		if err != nil {
			return nil, &os.PathError{Op: "open", Path: name, Err: err}
		}
		// Replacing a file gives its current size back to the allowance,
		// otherwise an account sitting on its limit could never save again.
		if limit >= 0 && existing != nil && flag&os.O_TRUNC != 0 {
			limit += existing.Size()
		}
		if limit == 0 {
			return nil, &os.PathError{Op: "open", Path: name, Err: davErrOverQuota}
		}
	}

	f, err := target.loc.Root.OpenFile(target.loc.Rel, flag, 0o644)
	if err != nil {
		return nil, err
	}
	display := ""
	if target.loc.Rel == "." {
		display = target.mount.Slug
	}
	return &davFile{
		File:     f,
		fs:       d,
		root:     target.loc.Root,
		rel:      target.loc.Rel,
		name:     name,
		virtual:  target.loc.Virtual,
		display:  display,
		readOnly: target.loc.ReadOnly(),
		limit:    limit,
		created:  created,
	}, nil
}

// RemoveAll deletes a file or a whole folder. A mount itself is never removed,
// because that is a change to the account rather than to its files.
func (d *davFS) RemoveAll(ctx context.Context, name string) error {
	target, err := d.davTargetFor(name)
	if err != nil {
		return &os.PathError{Op: "remove", Path: name, Err: err}
	}
	if target.root || target.loc.Rel == "." || target.loc.ReadOnly() {
		return &os.PathError{Op: "remove", Path: name, Err: os.ErrPermission}
	}
	return target.loc.Root.RemoveAll(target.loc.Rel)
}

// Rename moves a file or folder inside one mount, which is what a drag inside
// a drive window asks for.
func (d *davFS) Rename(ctx context.Context, oldName, newName string) error {
	src, err := d.davTargetFor(oldName)
	if err != nil {
		return &os.PathError{Op: "rename", Path: oldName, Err: err}
	}
	dst, err := d.davTargetFor(newName)
	if err != nil {
		return &os.PathError{Op: "rename", Path: newName, Err: err}
	}
	if src.root || dst.root || src.loc.Rel == "." || dst.loc.Rel == "." {
		return &os.PathError{Op: "rename", Path: oldName, Err: os.ErrPermission}
	}
	if src.loc.ReadOnly() || dst.loc.ReadOnly() {
		return &os.PathError{Op: "rename", Path: oldName, Err: os.ErrPermission}
	}
	// Each mount is pinned by its own root handle, so a move between two of
	// them has no single safe step. A file manager can still copy the item
	// across and delete the original, which is what it does when a rename is
	// refused.
	if !strings.EqualFold(src.mount.Slug, dst.mount.Slug) {
		return &os.PathError{Op: "rename", Path: newName, Err: os.ErrPermission}
	}
	return src.loc.Root.Rename(src.loc.Rel, dst.loc.Rel)
}

// Stat describes one entry in the tree.
func (d *davFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	target, err := d.davTargetFor(name)
	if err != nil {
		return nil, &os.PathError{Op: "stat", Path: name, Err: err}
	}
	if target.root {
		return d.davRootInfo(), nil
	}
	info, err := target.loc.Root.Stat(target.loc.Rel)
	if err != nil {
		return nil, err
	}
	if target.loc.Rel == "." {
		return davInfoFor(target.mount.Slug, info, target.loc.ReadOnly()), nil
	}
	if target.loc.ReadOnly() {
		return davInfoFor("", info, true), nil
	}
	return info, nil
}

// ---- write refusals ---------------------------------------------------------

// davIsWriteMethod reports whether a request wants to change something. LOCK
// belongs here because a lock on a name that does not exist yet creates the
// file, which is how Windows and the Finder begin a copy.
func davIsWriteMethod(method string) bool {
	switch method {
	case http.MethodPut, http.MethodDelete, "MKCOL", "MOVE", "COPY", "PROPPATCH", "LOCK":
		return true
	}
	return false
}

// davMethodPerm is the permission a write method needs, mirroring the one the
// same operation needs in the browser, or an empty permission for a method that
// stands on the read only check alone.
//
// A MOVE that keeps the name in its folder is a rename and a MOVE that does not
// is a move, exactly as the two buttons in the interface are.
func davMethodPerm(method, from, to string) store.Permission {
	switch method {
	case http.MethodPut:
		return store.PermUpload
	case http.MethodDelete:
		return store.PermDelete
	case "MKCOL":
		return store.PermCreate
	case "COPY":
		return store.PermCopy
	case "MOVE":
		if from != "" && to != "" && path.Dir(path.Clean(from)) == path.Dir(path.Clean(to)) {
			return store.PermRename
		}
		return store.PermMove
	}
	return ""
}

// davPermRefusal is the sentence a missing permission is answered with.
func davPermRefusal(p store.Permission) string {
	switch p {
	case store.PermUpload:
		return "This account is not allowed to add files"
	case store.PermCreate:
		return "This account is not allowed to create folders"
	case store.PermDelete:
		return "This account is not allowed to delete"
	case store.PermRename:
		return "This account is not allowed to rename"
	case store.PermMove:
		return "This account is not allowed to move items"
	case store.PermCopy:
		return "This account is not allowed to copy"
	}
	return "This account is not allowed to do that"
}

// davRefuseWrite reports the sentence to answer a write with when it names a
// place that cannot take one or asks for something the account may not do, or
// an empty string to let the request through.
//
// The place is decided first and the permission second, so a person reading the
// answer is told the most specific reason: a read only folder is read only for
// everyone, whatever their account holds.
//
// The file system refuses a forbidden place anyway; catching it here is what
// turns a puzzling status code into something the person at the keyboard can
// act on. The permission is a different matter. The guarded layer knows nothing
// of who is asking, so a method that the same account would be refused in the
// browser has to be refused here, or the drive would be a way around the
// account's own limits.
func (d *davFS) davRefuseWrite(r *http.Request) string {
	if !davIsWriteMethod(r.Method) {
		return ""
	}
	from := davStripPrefix(r.URL.Path)
	to := davDestination(r)

	var names []string
	switch r.Method {
	case "COPY":
		// A copy only writes at the destination.
		names = []string{to}
	case "MOVE":
		names = []string{from, to}
	default:
		names = []string{from}
	}

	for _, name := range names {
		if name == "" {
			continue
		}
		target, err := d.davTargetFor(name)
		if err != nil {
			// A name that leads nowhere is the protocol's answer to give, not
			// ours; it has its own status for that.
			continue
		}
		if target.root {
			return "The top of the drive lists your folders and cannot be changed"
		}
		if target.loc.ReadOnly() {
			return "This folder is read only"
		}
	}

	if perm := davMethodPerm(r.Method, from, to); perm != "" && !d.user.Can(perm) {
		return davPermRefusal(perm)
	}
	return ""
}

// davStripPrefix reduces a request path to a WebDAV name, or an empty string
// when the path does not belong to the drive at all.
func davStripPrefix(p string) string {
	rest := strings.TrimPrefix(p, davPrefix)
	if len(rest) == len(p) {
		return ""
	}
	if rest == "" {
		return "/"
	}
	return rest
}

// davDestination reads the target of a MOVE or COPY.
func davDestination(r *http.Request) string {
	raw := strings.TrimSpace(r.Header.Get("Destination"))
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return davStripPrefix(parsed.Path)
}

// ---- allowances -------------------------------------------------------------

// davAllowance reports how many bytes the account may still write, or -1 when
// it has no allowance set.
func (d *davFS) davAllowance(ctx context.Context) (int64, error) {
	if d.quotaCheck == nil || d.user == nil {
		return -1, nil
	}
	ok, remaining, err := d.quotaCheck(ctx, d.user.ID, 0)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}
	return remaining, nil
}

// davRecordUsage moves the stored storage figure with a file that has just
// landed, the same way a browser upload does. The background walk corrects
// whatever this misses.
func (d *davFS) davRecordUsage(bytes int64) {
	if d.quotaAdd == nil || d.user == nil || bytes <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), quotaSubmitTimeout)
	defer cancel()
	d.quotaAdd(ctx, d.user.ID, bytes)
}

// ---- files ------------------------------------------------------------------

// davFile is a real file or folder inside a mount. The embedded handle already
// carries the reading, seeking and closing the protocol needs; what is added
// here is the allowance ceiling on writes and the hiding of the scratch files
// Storix owns.
type davFile struct {
	*os.File

	fs       *davFS
	root     *os.Root
	rel      string
	name     string
	virtual  string
	display  string
	readOnly bool
	limit    int64
	written  int64
	created  bool
	// stopped marks a transfer the allowance cut short.
	stopped bool
}

// Write stores bytes, refusing once the storage allowance is spent rather than
// filling the disk and failing halfway through a large copy.
func (f *davFile) Write(p []byte) (int, error) {
	if f.limit >= 0 && f.written+int64(len(p)) > f.limit {
		f.stopped = true
		return 0, &os.PathError{Op: "write", Path: f.name, Err: davErrOverQuota}
	}
	n, err := f.File.Write(p)
	f.written += int64(n)
	return n, err
}

// davWriteOnly carries the write half of a file and nothing else.
type davWriteOnly struct{ to *davFile }

// Write hands the bytes to the file, allowance ceiling and all.
func (w davWriteOnly) Write(p []byte) (int, error) { return w.to.Write(p) }

// ReadFrom is how a transfer actually lands: the protocol copies the request
// body into the file with io.Copy, which prefers this method over Write when
// the destination offers it. The embedded handle offers it, so without this the
// bytes would go straight to the disk and every ceiling in Write, and the count
// the allowance is moved by afterwards, would be stepped over.
func (f *davFile) ReadFrom(r io.Reader) (int64, error) {
	// The wrapper is what keeps io.Copy from finding ReadFrom again and
	// calling this method for ever.
	return io.Copy(davWriteOnly{to: f}, r)
}

// Close finishes the file and records what it added to the account.
//
// A transfer the allowance cut short leaves a file holding the first part of
// what was being copied. If this open is what created it, it is taken away
// again: a fragment under the name of the whole is worse than nothing, and the
// client has already been told the copy failed.
func (f *davFile) Close() error {
	err := f.File.Close()
	if f.stopped && f.created && f.root != nil && f.rel != "." {
		if rmErr := f.root.Remove(f.rel); rmErr != nil && f.fs != nil {
			f.fs.api.Logger.Debug("part file left behind", "path", f.virtual, "err", rmErr)
		}
		return err
	}
	if f.created && f.written > 0 && f.fs != nil {
		f.fs.davRecordUsage(f.written)
	}
	return err
}

// Stat describes the file under the name the tree knows it by.
func (f *davFile) Stat() (fs.FileInfo, error) {
	info, err := f.File.Stat()
	if err != nil {
		return nil, err
	}
	if f.display == "" && !f.readOnly {
		return info, nil
	}
	return davInfoFor(f.display, info, f.readOnly), nil
}

// Readdir lists a folder, leaving out the scratch files of transfers still in
// flight and anything on the protected list.
func (f *davFile) Readdir(count int) ([]fs.FileInfo, error) {
	infos, err := f.File.Readdir(count)
	if len(infos) == 0 {
		return infos, err
	}
	kept := infos[:0]
	for _, info := range infos {
		if vfs.IsInternal(info.Name()) {
			continue
		}
		if f.fs != nil && f.fs.api.VFS.Denied(path.Join(f.virtual, info.Name())) {
			continue
		}
		kept = append(kept, info)
	}
	return kept, err
}

// ---- the collection above the mounts ----------------------------------------

// davRootEntries lists the mounts as the children of the top collection. The
// real folder is measured where it can be reached, so a drive window shows the
// dates it would show anywhere else.
func (d *davFS) davRootEntries() []fs.FileInfo {
	if d.entries != nil {
		return d.entries
	}
	out := make([]fs.FileInfo, 0, len(d.mounts))
	for _, m := range d.mounts {
		info := fs.FileInfo(davInfo{name: m.Slug, mode: fs.ModeDir | 0o555, mod: d.at})
		if loc, err := d.api.VFS.Resolve(d.scope, m.Mount.Path); err == nil {
			if real, err := loc.Root.Stat("."); err == nil {
				info = davInfoFor(m.Slug, real, m.Mount.ReadOnly)
			}
		}
		out = append(out, info)
	}
	d.entries = out
	return out
}

// davRootInfo describes the collection above the mounts. Its date follows the
// newest mount so the entry does not look different on every request.
func (d *davFS) davRootInfo() fs.FileInfo {
	mod := d.at
	for _, e := range d.davRootEntries() {
		if e.ModTime().After(mod) {
			mod = e.ModTime()
		}
	}
	return davInfo{name: "/", mode: fs.ModeDir | 0o555, mod: mod}
}

// davRootDir is the collection above the mounts. It exists only in memory and
// holds one entry per mount, so a drive opens on the same folder list the
// sidebar shows.
type davRootDir struct {
	info    fs.FileInfo
	entries []fs.FileInfo
	offset  int
}

// Close releases nothing, the collection holds no handle.
func (f *davRootDir) Close() error { return nil }

// Read refuses, a collection has no content of its own.
func (f *davRootDir) Read([]byte) (int, error) {
	return 0, &os.PathError{Op: "read", Path: "/", Err: os.ErrInvalid}
}

// Write refuses, the mount list is decided by the account rather than by a
// file manager.
func (f *davRootDir) Write([]byte) (int, error) {
	return 0, &os.PathError{Op: "write", Path: "/", Err: os.ErrPermission}
}

// Seek only accepts a rewind, which is all a listing needs.
func (f *davRootDir) Seek(offset int64, whence int) (int64, error) {
	if offset == 0 && whence == io.SeekStart {
		f.offset = 0
		return 0, nil
	}
	return 0, &os.PathError{Op: "seek", Path: "/", Err: os.ErrInvalid}
}

// Stat describes the collection itself.
func (f *davRootDir) Stat() (fs.FileInfo, error) { return f.info, nil }

// Readdir walks the mounts, following the same contract as a real directory:
// a count of zero or less returns everything at once.
func (f *davRootDir) Readdir(count int) ([]fs.FileInfo, error) {
	if count <= 0 {
		rest := f.entries[f.offset:]
		f.offset = len(f.entries)
		return rest, nil
	}
	if f.offset >= len(f.entries) {
		return nil, io.EOF
	}
	end := min(f.offset+count, len(f.entries))
	rest := f.entries[f.offset:end]
	f.offset = end
	return rest, nil
}

// ---- entry descriptions -----------------------------------------------------

// davInfo describes an entry under the name the WebDAV tree uses for it, which
// is not always the name it has on disk.
type davInfo struct {
	name string
	size int64
	mode fs.FileMode
	mod  time.Time
}

// Name is the entry name inside its collection.
func (i davInfo) Name() string { return i.name }

// Size is the length in bytes, zero for a collection.
func (i davInfo) Size() int64 { return i.size }

// Mode is the file mode.
func (i davInfo) Mode() fs.FileMode { return i.mode }

// ModTime is when the entry last changed.
func (i davInfo) ModTime() time.Time { return i.mod }

// IsDir reports whether the entry is a collection.
func (i davInfo) IsDir() bool { return i.mode.IsDir() }

// Sys carries nothing, the underlying entry is not exposed here.
func (i davInfo) Sys() any { return nil }

// davInfoFor restates a real entry under the name the tree uses for it and
// takes the write bits off a read only mount, so a drive window shows the
// folder as read only before a copy into it is attempted.
func davInfoFor(name string, info fs.FileInfo, readOnly bool) fs.FileInfo {
	if name == "" {
		name = info.Name()
	}
	mode := info.Mode()
	if readOnly {
		mode &^= 0o222
	}
	return davInfo{name: name, size: info.Size(), mode: mode, mod: info.ModTime()}
}
