// Package archive creates, inspects and extracts archives inside a guarded root.
//
// Every byte read and every byte written travels through the *os.Root the
// caller resolved, so the kernel keeps the work inside that mount even when an
// archive carries a hostile entry name. Extraction additionally validates each
// name before it is used, refuses links that would point out of the
// destination, and caps how much data one archive is allowed to produce.
//
// Storix - modern web file manager for servers.
// Developed by X Project.
package archive

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

// Errors reported by this package.
var (
	// ErrUnsupportedFormat is returned when the extension names no known format.
	ErrUnsupportedFormat = errors.New("archive: unsupported format")
	// ErrReadOnlyFormat is returned when a format can be read but not written.
	ErrReadOnlyFormat = errors.New("archive: format cannot be created")
	// ErrNoSources is returned when a creation request selects nothing.
	ErrNoSources = errors.New("archive: no sources given")
	// ErrInvalidPath is returned for a source or destination outside the root.
	ErrInvalidPath = errors.New("archive: invalid path")
	// ErrNotDirectory is returned when the extraction target is not a directory.
	ErrNotDirectory = errors.New("archive: destination is not a directory")
	// ErrTooManyEntries is returned when an archive holds more entries than the
	// limits allow.
	ErrTooManyEntries = errors.New("archive: archive holds too many entries")
	// ErrBomb is returned when the extracted size is out of proportion to the
	// archive size, which is the signature of a decompression bomb.
	ErrBomb = errors.New("archive: decompressed size is out of proportion to the archive")
)

const (
	// chunkSize is the copy granularity, and therefore also the progress and
	// cancellation granularity.
	chunkSize = 1 << 20
	// maxLinkBytes caps how much of a zip entry is read while looking for a
	// symlink target.
	maxLinkBytes = 4096
	// maxInspectItems bounds the memory an unbounded Inspect call may use.
	maxInspectItems = 100000
	// maxSkippedReported bounds the skip list carried back to the browser.
	maxSkippedReported = 500
)

// Format is an archive container plus its compression.
type Format string

// Supported formats.
const (
	FormatZip    Format = "zip"
	FormatTar    Format = "tar"
	FormatTarGz  Format = "tar.gz"
	FormatTarBz2 Format = "tar.bz2"
)

// DetectFormat maps a file name onto a format. It returns an empty Format when
// the extension is not one this package handles.
func DetectFormat(name string) Format {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return FormatTarGz
	case strings.HasSuffix(lower, ".tar.bz2"), strings.HasSuffix(lower, ".tbz"), strings.HasSuffix(lower, ".tbz2"):
		return FormatTarBz2
	case strings.HasSuffix(lower, ".tar"):
		return FormatTar
	case strings.HasSuffix(lower, ".zip"):
		return FormatZip
	}
	return ""
}

// Extension is the canonical file extension of the format, empty when unknown.
func (f Format) Extension() string {
	switch f {
	case FormatZip:
		return ".zip"
	case FormatTar:
		return ".tar"
	case FormatTarGz:
		return ".tar.gz"
	case FormatTarBz2:
		return ".tar.bz2"
	}
	return ""
}

// CanCreate reports whether this package can write the format. The standard
// library ships a bzip2 decompressor only, so tar.bz2 is read only.
func (f Format) CanCreate() bool {
	switch f {
	case FormatZip, FormatTar, FormatTarGz:
		return true
	}
	return false
}

// String renders the format for logs and messages.
func (f Format) String() string { return string(f) }

// storedExts are already compressed on disk. Running deflate over them costs a
// lot of processor time and gives back a few bytes at best, so they go into a
// zip uncompressed.
var storedExts = map[string]bool{
	".zip": true, ".gz": true, ".tgz": true, ".bz2": true, ".xz": true, ".zst": true,
	".7z": true, ".rar": true, ".lz4": true, ".br": true,
	".mp4": true, ".mkv": true, ".avi": true, ".mov": true, ".webm": true, ".m4v": true,
	".mp3": true, ".m4a": true, ".aac": true, ".ogg": true, ".flac": true,
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".heic": true,
	".iso": true,
}

// stored reports whether an entry should go into a zip without compression.
func stored(name string) bool {
	return storedExts[strings.ToLower(path.Ext(name))]
}

// Progress is called while an archive operation runs. Bytes and items are
// cumulative and current is the entry being handled. It may be nil.
type Progress func(bytes, items int64, current string)

// counter turns raw byte and item counts into throttled progress callbacks.
type counter struct {
	progress Progress
	bytes    int64
	items    int64
	pending  int64
}

// addBytes records copied bytes and reports once per chunk.
func (c *counter) addBytes(n int64, current string) {
	c.bytes += n
	c.pending += n
	if c.pending >= chunkSize {
		c.pending = 0
		c.emit(current)
	}
}

// addItem records a finished entry and always reports.
func (c *counter) addItem(current string) {
	c.items++
	c.pending = 0
	c.emit(current)
}

func (c *counter) emit(current string) {
	if c.progress != nil {
		c.progress(c.bytes, c.items, current)
	}
}

// Item is one entry of an archive as shown in the preview panel.
type Item struct {
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	Compressed int64     `json:"compressed"`
	IsDir      bool      `json:"isDir"`
	Mode       string    `json:"mode"`
	Modified   time.Time `json:"modified"`
}

// Create writes an archive of the given root relative sources to out.
//
// Directories are walked recursively and stored with their modes and
// modification times. Symlinks are recorded as links and are never followed,
// so a link inside the selection cannot pull in data from outside the mount.
// Sockets, devices and named pipes are ignored. The context is honoured
// between entries and between chunks.
func Create(ctx context.Context, root *os.Root, sources []string, out io.Writer, format Format, progress Progress) error {
	if root == nil {
		return fmt.Errorf("%w: no root", ErrInvalidPath)
	}
	if len(sources) == 0 {
		return ErrNoSources
	}
	if format == "" {
		return fmt.Errorf("%w: no format", ErrUnsupportedFormat)
	}
	if !format.CanCreate() {
		return fmt.Errorf("%w: %s", ErrReadOnlyFormat, format)
	}

	cr := &creator{root: root, c: &counter{progress: progress}, buf: make([]byte, chunkSize)}

	switch format {
	case FormatZip:
		zw := zip.NewWriter(out)
		cr.sink = &zipSink{w: zw}
		if err := cr.run(ctx, sources); err != nil {
			_ = zw.Close()
			return err
		}
		return zw.Close()

	case FormatTar:
		tw := tar.NewWriter(out)
		cr.sink = &tarSink{w: tw}
		if err := cr.run(ctx, sources); err != nil {
			_ = tw.Close()
			return err
		}
		return tw.Close()

	case FormatTarGz:
		gz := gzip.NewWriter(out)
		tw := tar.NewWriter(gz)
		cr.sink = &tarSink{w: tw}
		if err := cr.run(ctx, sources); err != nil {
			_ = tw.Close()
			_ = gz.Close()
			return err
		}
		if err := tw.Close(); err != nil {
			_ = gz.Close()
			return err
		}
		return gz.Close()
	}
	return fmt.Errorf("%w: %s", ErrUnsupportedFormat, format)
}

// StreamZip writes a zip of the selection straight to a response writer.
func StreamZip(ctx context.Context, root *os.Root, sources []string, out io.Writer, progress Progress) error {
	return Create(ctx, root, sources, out, FormatZip, progress)
}

// sink is the container specific half of archive creation.
type sink interface {
	dir(name string, info fs.FileInfo) error
	symlink(name, target string, info fs.FileInfo) error
	// file returns the writer for the entry body. The declared size is written
	// into the header of formats that need it up front.
	file(name string, info fs.FileInfo, size int64) (io.Writer, error)
	// fixedSize reports whether the body must match the declared size exactly.
	fixedSize() bool
}

// zipSink writes zip members.
type zipSink struct{ w *zip.Writer }

func (z *zipSink) dir(name string, info fs.FileInfo) error {
	h := &zip.FileHeader{Name: name + "/", Method: zip.Store, Modified: info.ModTime()}
	h.SetMode(info.Mode())
	_, err := z.w.CreateHeader(h)
	return err
}

func (z *zipSink) symlink(name, target string, info fs.FileInfo) error {
	h := &zip.FileHeader{Name: name, Method: zip.Store, Modified: info.ModTime()}
	h.SetMode(info.Mode())
	w, err := z.w.CreateHeader(h)
	if err != nil {
		return err
	}
	// A zip records a symlink by storing its target as the member body.
	_, err = io.WriteString(w, target)
	return err
}

func (z *zipSink) file(name string, info fs.FileInfo, size int64) (io.Writer, error) {
	h := &zip.FileHeader{Name: name, Modified: info.ModTime(), Method: zip.Deflate}
	if stored(name) {
		h.Method = zip.Store
	}
	h.SetMode(info.Mode().Perm())
	return z.w.CreateHeader(h)
}

func (z *zipSink) fixedSize() bool { return false }

// tarSink writes tar members.
type tarSink struct{ w *tar.Writer }

func (t *tarSink) dir(name string, info fs.FileInfo) error {
	h, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	h.Name = name + "/"
	return t.w.WriteHeader(h)
}

func (t *tarSink) symlink(name, target string, info fs.FileInfo) error {
	h, err := tar.FileInfoHeader(info, target)
	if err != nil {
		return err
	}
	h.Name = name
	h.Linkname = target
	return t.w.WriteHeader(h)
}

func (t *tarSink) file(name string, info fs.FileInfo, size int64) (io.Writer, error) {
	h, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return nil, err
	}
	h.Name = name
	h.Size = size
	if err := t.w.WriteHeader(h); err != nil {
		return nil, err
	}
	return t.w, nil
}

func (t *tarSink) fixedSize() bool { return true }

// creator walks the selection and feeds it to a sink.
type creator struct {
	root *os.Root
	sink sink
	c    *counter
	buf  []byte
}

// run archives every source, keeping the top level names unique.
func (cr *creator) run(ctx context.Context, sources []string) error {
	taken := make(map[string]bool, len(sources))
	for _, src := range sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := cleanRel(src)
		if err != nil {
			return err
		}
		info, err := cr.root.Lstat(rel)
		if err != nil {
			return fmt.Errorf("archive: %s: %w", path.Base(rel), err)
		}
		if rel == "." {
			// The mount itself was selected, so its children keep their own
			// names instead of nesting under a "." folder.
			if !info.IsDir() {
				return fmt.Errorf("%w: %q", ErrInvalidPath, src)
			}
			names, err := readDirNames(cr.root, ".")
			if err != nil {
				return err
			}
			for _, child := range names {
				ci, err := cr.root.Lstat(child)
				if err != nil {
					continue
				}
				if err := cr.walk(ctx, child, unique(taken, child), ci); err != nil {
					return err
				}
			}
			continue
		}
		if err := cr.walk(ctx, rel, unique(taken, path.Base(rel)), info); err != nil {
			return err
		}
	}
	return nil
}

// walk archives one path and everything below it.
func (cr *creator) walk(ctx context.Context, rel, name string, info fs.FileInfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := cr.root.Readlink(rel)
		if err != nil {
			return err
		}
		if err := cr.sink.symlink(name, target, info); err != nil {
			return err
		}
		cr.c.addItem(name)
		return nil

	case info.IsDir():
		if err := cr.sink.dir(name, info); err != nil {
			return err
		}
		cr.c.addItem(name)
		names, err := readDirNames(cr.root, rel)
		if err != nil {
			return err
		}
		for _, child := range names {
			childRel := joinRel(rel, child)
			ci, err := cr.root.Lstat(childRel)
			if err != nil {
				// The entry disappeared between the directory read and the
				// stat. Nothing to archive.
				continue
			}
			if err := cr.walk(ctx, childRel, path.Join(name, child), ci); err != nil {
				return err
			}
		}
		return nil

	case info.Mode().IsRegular():
		return cr.file(ctx, rel, name, info)

	default:
		// Sockets, devices and named pipes carry no content worth archiving.
		return nil
	}
}

// file copies one regular file into the archive.
func (cr *creator) file(ctx context.Context, rel, name string, info fs.FileInfo) error {
	f, err := cr.root.Open(rel)
	if err != nil {
		return err
	}
	defer f.Close()

	size := info.Size()
	if st, err := f.Stat(); err == nil {
		size = st.Size()
	}
	w, err := cr.sink.file(name, info, size)
	if err != nil {
		return err
	}

	var written int64
	for written < size {
		if err := ctx.Err(); err != nil {
			return err
		}
		want := size - written
		if want > int64(len(cr.buf)) {
			want = int64(len(cr.buf))
		}
		n, readErr := f.Read(cr.buf[:want])
		if n > 0 {
			if _, writeErr := w.Write(cr.buf[:n]); writeErr != nil {
				return writeErr
			}
			written += int64(n)
			cr.c.addBytes(int64(n), name)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if written < size && cr.sink.fixedSize() {
		// The file shrank while it was being read. A tar header cannot be
		// rewritten at this point, so the body is padded to stay consistent.
		if err := pad(w, size-written); err != nil {
			return err
		}
	}
	cr.c.addItem(name)
	return nil
}

// pad writes n zero bytes.
func pad(w io.Writer, n int64) error {
	zeros := make([]byte, 4096)
	for n > 0 {
		size := int64(len(zeros))
		if n < size {
			size = n
		}
		written, err := w.Write(zeros[:size])
		if err != nil {
			return err
		}
		n -= int64(written)
	}
	return nil
}

// Inspect lists the entries of an archive without extracting anything. A limit
// of zero or less means every entry, up to an internal ceiling that keeps the
// result from exhausting memory.
func Inspect(ctx context.Context, root *os.Root, rel string, limit int) ([]Item, Format, error) {
	if root == nil {
		return nil, "", fmt.Errorf("%w: no root", ErrInvalidPath)
	}
	format := DetectFormat(rel)
	if format == "" {
		return nil, "", fmt.Errorf("%w: %s", ErrUnsupportedFormat, path.Base(rel))
	}
	clean, err := cleanRel(rel)
	if err != nil {
		return nil, format, err
	}
	f, err := root.Open(clean)
	if err != nil {
		return nil, format, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, format, err
	}
	if info.IsDir() {
		return nil, format, fmt.Errorf("%w: %s", ErrInvalidPath, path.Base(clean))
	}
	if limit <= 0 || limit > maxInspectItems {
		limit = maxInspectItems
	}

	items := make([]Item, 0, 64)
	if format == FormatZip {
		zr, err := zip.NewReader(f, info.Size())
		if err != nil {
			return nil, format, fmt.Errorf("archive: read %s: %w", path.Base(clean), err)
		}
		for i, member := range zr.File {
			if i%256 == 0 {
				if err := ctx.Err(); err != nil {
					return items, format, err
				}
			}
			if len(items) >= limit {
				break
			}
			fi := member.FileInfo()
			isDir := fi.IsDir() || strings.HasSuffix(member.Name, "/")
			items = append(items, Item{
				Name:       strings.TrimSuffix(member.Name, "/"),
				Size:       int64(clampUint64(member.UncompressedSize64)),
				Compressed: int64(clampUint64(member.CompressedSize64)),
				IsDir:      isDir,
				Mode:       fi.Mode().String(),
				Modified:   member.Modified.UTC(),
			})
		}
		return items, format, nil
	}

	tr, closeFn, err := tarReader(format, f)
	if err != nil {
		return nil, format, err
	}
	defer closeFn()
	for count := 0; len(items) < limit; count++ {
		if count%256 == 0 {
			if err := ctx.Err(); err != nil {
				return items, format, err
			}
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return items, format, fmt.Errorf("archive: read %s: %w", path.Base(clean), err)
		}
		if hdr.Typeflag == tar.TypeXHeader || hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		isDir := hdr.Typeflag == tar.TypeDir
		items = append(items, Item{
			Name:     strings.TrimSuffix(hdr.Name, "/"),
			Size:     hdr.Size,
			IsDir:    isDir,
			Mode:     hdr.FileInfo().Mode().String(),
			Modified: hdr.ModTime.UTC(),
		})
	}
	return items, format, nil
}

// tarReader wraps an archive stream in the right decompressor.
func tarReader(format Format, r io.Reader) (*tar.Reader, func(), error) {
	switch format {
	case FormatTar:
		return tar.NewReader(r), func() {}, nil
	case FormatTarGz:
		gz, err := gzip.NewReader(r)
		if err != nil {
			return nil, nil, fmt.Errorf("archive: gzip: %w", err)
		}
		return tar.NewReader(gz), func() { _ = gz.Close() }, nil
	case FormatTarBz2:
		return tar.NewReader(bzip2.NewReader(r)), func() {}, nil
	}
	return nil, nil, fmt.Errorf("%w: %s", ErrUnsupportedFormat, format)
}

// Report summarizes an extraction.
type Report struct {
	Files   int64    `json:"files"`
	Dirs    int64    `json:"dirs"`
	Bytes   int64    `json:"bytes"`
	Skipped []string `json:"skipped,omitempty"`
}

// Limits bound what an extraction may produce. They matter because a small
// archive can declare an enormous payload, which is how a decompression bomb
// fills a disk.
//
// A zero field takes the value from DefaultLimits. A negative field disables
// that particular check.
type Limits struct {
	// MaxRatio is the largest declared entry size relative to the archive size.
	MaxRatio int64
	// MaxTotalRatio caps the sum of all written bytes relative to the archive
	// size. Extraction stops with ErrBomb once the budget is used up.
	MaxTotalRatio int64
	// MinEntryBytes is always allowed whatever the ratio says, so that a tiny
	// archive holding one ordinary file is not refused.
	MinEntryBytes int64
	// MinTotalBytes is the floor of the overall budget.
	MinTotalBytes int64
	// MaxEntrySize refuses any single entry larger than this.
	MaxEntrySize int64
	// MaxEntries refuses an archive with more entries than this.
	MaxEntries int64
}

// DefaultLimits are the limits Extract applies.
var DefaultLimits = Limits{
	MaxRatio:      200,
	MaxTotalRatio: 100,
	MinEntryBytes: 64 << 20,
	MinTotalBytes: 256 << 20,
	MaxEntrySize:  1 << 40,
	MaxEntries:    200000,
}

// normalize fills unset fields from the defaults.
func (l Limits) normalize() Limits {
	if l.MaxRatio == 0 {
		l.MaxRatio = DefaultLimits.MaxRatio
	}
	if l.MaxTotalRatio == 0 {
		l.MaxTotalRatio = DefaultLimits.MaxTotalRatio
	}
	if l.MinEntryBytes == 0 {
		l.MinEntryBytes = DefaultLimits.MinEntryBytes
	}
	if l.MinTotalBytes == 0 {
		l.MinTotalBytes = DefaultLimits.MinTotalBytes
	}
	if l.MaxEntrySize == 0 {
		l.MaxEntrySize = DefaultLimits.MaxEntrySize
	}
	if l.MaxEntries == 0 {
		l.MaxEntries = DefaultLimits.MaxEntries
	}
	return l
}

// Extract unpacks the archive at srcRel into dstRel using DefaultLimits.
//
// Entry names are validated before use and every write goes through dstRoot,
// so neither a traversal name nor a crafted link can place a file outside the
// destination. Entries that are refused are listed in Report.Skipped instead of
// failing the whole job.
func Extract(ctx context.Context, srcRoot *os.Root, srcRel string, dstRoot *os.Root, dstRel string, progress Progress) (*Report, error) {
	return ExtractWithLimits(ctx, srcRoot, srcRel, dstRoot, dstRel, DefaultLimits, progress)
}

// ExtractWithLimits is Extract with caller supplied safety limits.
func ExtractWithLimits(ctx context.Context, srcRoot *os.Root, srcRel string, dstRoot *os.Root, dstRel string, limits Limits, progress Progress) (*Report, error) {
	if srcRoot == nil || dstRoot == nil {
		return nil, fmt.Errorf("%w: no root", ErrInvalidPath)
	}
	format := DetectFormat(srcRel)
	if format == "" {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedFormat, path.Base(srcRel))
	}
	srcClean, err := cleanRel(srcRel)
	if err != nil {
		return nil, err
	}
	dest, err := cleanRel(dstRel)
	if err != nil {
		return nil, err
	}

	src, err := srcRoot.Open(srcClean)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	srcInfo, err := src.Stat()
	if err != nil {
		return nil, err
	}
	if srcInfo.IsDir() {
		return nil, fmt.Errorf("%w: %s", ErrInvalidPath, path.Base(srcClean))
	}

	if dest != "." {
		if err := dstRoot.MkdirAll(dest, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	if info, err := dstRoot.Stat(dest); err != nil {
		return nil, err
	} else if !info.IsDir() {
		return nil, fmt.Errorf("%w: %s", ErrNotDirectory, dstRel)
	}

	ex := &extractor{
		ctx:     ctx,
		dstRoot: dstRoot,
		dest:    dest,
		limits:  limits.normalize(),
		count:   &counter{progress: progress},
		report:  &Report{},
		buf:     make([]byte, chunkSize),
		dirs:    make(map[string]bool),
		archive: srcInfo.Size(),
	}
	ex.budget = ex.totalBudget()

	if format == FormatZip {
		err = ex.zip(src, srcInfo.Size())
	} else {
		err = ex.tar(format, src)
	}
	ex.applyModes()
	if err != nil {
		return ex.report, err
	}
	return ex.report, nil
}

// extractor holds the state of one extraction.
type extractor struct {
	ctx     context.Context
	dstRoot *os.Root
	dest    string
	limits  Limits
	budget  int64
	written int64
	count   *counter
	report  *Report
	buf     []byte
	dirs    map[string]bool
	modes   []pendingMode
	archive int64
	entries int64
}

// pendingMode is a directory permission applied once the tree is complete.
type pendingMode struct {
	rel  string
	perm fs.FileMode
}

// totalBudget is the ceiling on the bytes this archive may write.
func (e *extractor) totalBudget() int64 {
	if e.limits.MaxTotalRatio < 0 {
		return math.MaxInt64
	}
	budget := int64(math.MaxInt64)
	if e.archive > 0 && e.archive < math.MaxInt64/e.limits.MaxTotalRatio {
		budget = e.archive * e.limits.MaxTotalRatio
	}
	if floor := e.limits.MinTotalBytes; floor > 0 && budget < floor {
		budget = floor
	}
	return budget
}

// zip walks the members of a zip archive.
func (e *extractor) zip(r io.ReaderAt, size int64) error {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return fmt.Errorf("archive: read: %w", err)
	}
	for _, member := range zr.File {
		fi := member.FileInfo()
		mode := fi.Mode()
		ent := entryInfo{name: member.Name, mode: mode.Perm(), modTime: member.Modified}
		switch {
		case fi.IsDir() || strings.HasSuffix(member.Name, "/"):
			ent.isDir = true
		case mode&fs.ModeSymlink != 0:
			target, err := readZipLink(member)
			if err != nil {
				e.skip(member.Name, "unreadable link target")
				continue
			}
			ent.isSymlink = true
			ent.linkname = target
		case mode&fs.ModeType != 0:
			// Devices, sockets and pipes are never recreated.
		default:
			ent.isRegular = true
			ent.size = int64(clampUint64(member.UncompressedSize64))
			m := member
			ent.open = func() (io.ReadCloser, error) { return m.Open() }
		}
		if err := e.entry(ent); err != nil {
			return err
		}
	}
	return nil
}

// tar walks the members of a tar stream.
func (e *extractor) tar(format Format, r io.Reader) error {
	tr, closeFn, err := tarReader(format, r)
	if err != nil {
		return err
	}
	defer closeFn()
	for {
		if err := e.ctx.Err(); err != nil {
			return err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("archive: read: %w", err)
		}
		ent := entryInfo{
			name:    hdr.Name,
			mode:    hdr.FileInfo().Mode().Perm(),
			modTime: hdr.ModTime,
			size:    hdr.Size,
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			ent.isDir = true
		case tar.TypeSymlink:
			ent.isSymlink = true
			ent.linkname = hdr.Linkname
		case tar.TypeLink:
			ent.isLink = true
			ent.linkname = hdr.Linkname
		case tar.TypeReg:
			ent.isRegular = true
			ent.open = func() (io.ReadCloser, error) { return io.NopCloser(tr), nil }
		case tar.TypeXHeader, tar.TypeXGlobalHeader:
			// Metadata the reader already folded into the next header.
			continue
		default:
			// Devices, sockets and pipes are never recreated.
		}
		if err := e.entry(ent); err != nil {
			return err
		}
	}
}

// entryInfo is one archive member, normalized across containers.
type entryInfo struct {
	name      string
	size      int64
	mode      fs.FileMode
	modTime   time.Time
	isDir     bool
	isSymlink bool
	isLink    bool
	isRegular bool
	linkname  string
	open      func() (io.ReadCloser, error)
}

// entry writes one member, or records why it was refused.
func (e *extractor) entry(ent entryInfo) error {
	if err := e.ctx.Err(); err != nil {
		return err
	}
	e.entries++
	if e.limits.MaxEntries > 0 && e.entries > e.limits.MaxEntries {
		return fmt.Errorf("%w: more than %d", ErrTooManyEntries, e.limits.MaxEntries)
	}

	rel, ok := entryName(ent.name)
	if !ok {
		// Absolute names, empty names and names holding a ".." element are the
		// classic archive traversal, so they never reach the file system.
		e.skip(ent.name, "unsafe entry name")
		return nil
	}

	switch {
	case ent.isDir:
		if err := e.ensureDir(rel, permOr(ent.mode, 0o755)); err != nil {
			e.skip(ent.name, err.Error())
			return nil
		}
		e.count.addItem(rel)
		return nil

	case ent.isSymlink:
		return e.writeSymlink(rel, ent)

	case ent.isLink:
		return e.writeHardLink(rel, ent)

	case ent.isRegular:
		return e.writeFile(rel, ent)

	default:
		e.skip(ent.name, "unsupported entry type")
		return nil
	}
}

// writeSymlink recreates a link, but only when its target stays inside the
// destination.
func (e *extractor) writeSymlink(rel string, ent entryInfo) error {
	if !safeLinkTarget(rel, ent.linkname) {
		e.skip(ent.name, "link target leaves the destination")
		return nil
	}
	if dir := path.Dir(rel); dir != "." {
		if err := e.ensureDir(dir, 0o755); err != nil {
			e.skip(ent.name, err.Error())
			return nil
		}
	}
	target := joinRel(e.dest, rel)
	if err := e.dstRoot.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		e.skip(ent.name, err.Error())
		return nil
	}
	if err := e.dstRoot.Symlink(ent.linkname, target); err != nil {
		e.skip(ent.name, err.Error())
		return nil
	}
	e.count.addItem(rel)
	return nil
}

// writeHardLink recreates a hard link between two entries of the same archive.
func (e *extractor) writeHardLink(rel string, ent entryInfo) error {
	source, ok := entryName(ent.linkname)
	if !ok {
		e.skip(ent.name, "link target leaves the destination")
		return nil
	}
	if dir := path.Dir(rel); dir != "." {
		if err := e.ensureDir(dir, 0o755); err != nil {
			e.skip(ent.name, err.Error())
			return nil
		}
	}
	target := joinRel(e.dest, rel)
	if err := e.dstRoot.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		e.skip(ent.name, err.Error())
		return nil
	}
	if err := e.dstRoot.Link(joinRel(e.dest, source), target); err != nil {
		e.skip(ent.name, err.Error())
		return nil
	}
	e.count.addItem(rel)
	return nil
}

// writeFile unpacks one regular file.
func (e *extractor) writeFile(rel string, ent entryInfo) error {
	if !e.allowSize(ent.size) {
		e.skip(ent.name, "declared size is out of proportion to the archive")
		return nil
	}
	if dir := path.Dir(rel); dir != "." {
		if err := e.ensureDir(dir, 0o755); err != nil {
			e.skip(ent.name, err.Error())
			return nil
		}
	}
	perm := permOr(ent.mode, 0o644)
	target := joinRel(e.dest, rel)

	// The file is opened through the destination root, so the kernel refuses
	// the write if the resolved path would leave it even when the name checks
	// above were somehow fooled. It is created writable and put back to its
	// recorded mode afterwards.
	f, err := e.dstRoot.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm|0o200)
	if err != nil {
		e.skip(ent.name, err.Error())
		return nil
	}
	rc, err := ent.open()
	if err != nil {
		_ = f.Close()
		e.skip(ent.name, err.Error())
		return nil
	}
	written, copyErr := e.copy(f, rc, rel)
	closeErr := rc.Close()
	if err := f.Close(); err != nil && copyErr == nil {
		copyErr = err
	}
	if copyErr != nil {
		// A cancelled or refused entry leaves no half written file behind.
		_ = e.dstRoot.Remove(target)
		return copyErr
	}
	if closeErr != nil {
		e.skip(ent.name, closeErr.Error())
		return nil
	}
	if perm != perm|0o200 {
		_ = e.dstRoot.Chmod(target, perm)
	}
	if !ent.modTime.IsZero() {
		_ = e.dstRoot.Chtimes(target, time.Now(), ent.modTime)
	}
	e.report.Files++
	e.report.Bytes += written
	e.count.addItem(rel)
	return nil
}

// copy moves the body of one entry, checking the budget and the context on
// every chunk.
func (e *extractor) copy(dst io.Writer, src io.Reader, name string) (int64, error) {
	var total int64
	for {
		if err := e.ctx.Err(); err != nil {
			return total, err
		}
		n, readErr := src.Read(e.buf)
		if n > 0 {
			if e.written+int64(n) > e.budget {
				return total, fmt.Errorf("%w: over %d bytes from a %d byte archive", ErrBomb, e.budget, e.archive)
			}
			if _, writeErr := dst.Write(e.buf[:n]); writeErr != nil {
				return total, writeErr
			}
			e.written += int64(n)
			total += int64(n)
			e.count.addBytes(int64(n), name)
		}
		if errors.Is(readErr, io.EOF) {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}

// allowSize reports whether a declared entry size is plausible for this
// archive.
func (e *extractor) allowSize(declared int64) bool {
	if declared <= 0 {
		return true
	}
	if e.limits.MinEntryBytes > 0 && declared <= e.limits.MinEntryBytes {
		return true
	}
	if e.limits.MaxEntrySize > 0 && declared > e.limits.MaxEntrySize {
		return false
	}
	if e.limits.MaxRatio > 0 && e.archive > 0 && declared/e.archive > e.limits.MaxRatio {
		return false
	}
	return true
}

// ensureDir creates a directory below the destination once.
func (e *extractor) ensureDir(rel string, perm fs.FileMode) error {
	if rel == "" || rel == "." {
		return nil
	}
	if e.dirs[rel] {
		return nil
	}
	// Directories are created writable so their children can be unpacked, then
	// put back to the recorded mode once the tree is complete.
	if err := e.dstRoot.MkdirAll(joinRel(e.dest, rel), perm|0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	e.dirs[rel] = true
	e.report.Dirs++
	if perm != perm|0o700 {
		e.modes = append(e.modes, pendingMode{rel: joinRel(e.dest, rel), perm: perm})
	}
	return nil
}

// applyModes restores directory permissions, deepest first so a parent that
// loses its write bit cannot block a child.
func (e *extractor) applyModes() {
	for i := len(e.modes) - 1; i >= 0; i-- {
		_ = e.dstRoot.Chmod(e.modes[i].rel, e.modes[i].perm)
	}
	e.modes = nil
}

// skip records a refused entry for the report.
func (e *extractor) skip(name, reason string) {
	switch {
	case len(e.report.Skipped) < maxSkippedReported:
		e.report.Skipped = append(e.report.Skipped, label(name)+": "+reason)
	case len(e.report.Skipped) == maxSkippedReported:
		e.report.Skipped = append(e.report.Skipped, "more entries were skipped")
	}
}

// readZipLink reads the target a zip member records for a symlink.
func readZipLink(member *zip.File) (string, error) {
	rc, err := member.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxLinkBytes))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// entryName validates an archive entry name and returns it as a clean root
// relative path. The boolean is false when the entry must not be written.
func entryName(raw string) (string, bool) {
	name := strings.ReplaceAll(raw, "\\", "/")
	if name == "" || strings.ContainsRune(name, 0) {
		return "", false
	}
	if strings.HasPrefix(name, "/") {
		return "", false
	}
	// A volume prefix such as "C:" is absolute as well, and has no business in
	// an archive entry name on any platform.
	if len(name) > 1 && name[1] == ':' {
		return "", false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", false
		}
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return "", false
	}
	return clean, true
}

// safeLinkTarget reports whether a symlink recorded at entryRel may point at
// target without leaving the destination.
func safeLinkTarget(entryRel, target string) bool {
	link := strings.ReplaceAll(target, "\\", "/")
	if link == "" || strings.ContainsRune(link, 0) {
		return false
	}
	if strings.HasPrefix(link, "/") {
		return false
	}
	if len(link) > 1 && link[1] == ':' {
		return false
	}
	resolved := path.Join(path.Dir(entryRel), link)
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return false
	}
	return true
}

// cleanRel normalizes a root relative path for os.Root calls.
func cleanRel(rel string) (string, error) {
	out := strings.ReplaceAll(rel, "\\", "/")
	if strings.ContainsRune(out, 0) {
		return "", fmt.Errorf("%w: %q", ErrInvalidPath, rel)
	}
	out = strings.TrimPrefix(out, "/")
	if out == "" {
		return ".", nil
	}
	out = path.Clean(out)
	if out == ".." || strings.HasPrefix(out, "../") {
		return "", fmt.Errorf("%w: %q", ErrInvalidPath, rel)
	}
	return out, nil
}

// joinRel appends a name to a root relative base.
func joinRel(base, name string) string {
	if base == "." || base == "" {
		return name
	}
	return base + "/" + name
}

// readDirNames lists a directory in a stable order.
func readDirNames(root *os.Root, rel string) ([]string, error) {
	dir, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	entries, err := dir.ReadDir(-1)
	closeErr := dir.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	names := make([]string, 0, len(entries))
	for _, de := range entries {
		names = append(names, de.Name())
	}
	sort.Strings(names)
	return names, nil
}

// unique keeps the top level names of an archive free of collisions, which can
// happen when a selection spans several folders.
func unique(taken map[string]bool, name string) string {
	if name == "" || name == "." || name == "/" {
		name = "root"
	}
	if !taken[name] {
		taken[name] = true
		return name
	}
	ext := path.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 2; i < 10000; i++ {
		candidate := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if !taken[candidate] {
			taken[candidate] = true
			return candidate
		}
	}
	return name
}

// permOr falls back to a default when an archive records no permission bits.
func permOr(mode, fallback fs.FileMode) fs.FileMode {
	if perm := mode.Perm(); perm != 0 {
		return perm
	}
	return fallback
}

// clampUint64 keeps a declared size inside the signed range.
func clampUint64(v uint64) uint64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return v
}

// label makes an untrusted entry name safe to show in a report.
func label(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
		if b.Len() > 200 {
			b.WriteString("...")
			break
		}
	}
	if b.Len() == 0 {
		return "(unnamed entry)"
	}
	return b.String()
}
