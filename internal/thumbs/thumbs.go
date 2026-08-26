// Package thumbs renders image thumbnails for the Storix file browser and
// keeps them in a disk backed cache.
//
// The cache is content addressed: a key covers the file path, its modification
// time, its size in bytes and the requested pixel size, so an edited file gets
// a new entry instead of a stale one and no invalidation pass is needed.
// Entries are sharded by the first two hex characters of the key so a busy
// server never ends up with a single directory holding a million files.
//
// Generation is guarded by a single flight, which matters because the grid
// asks for sixty thumbnails at once and a browser opens several connections
// per page. Without it the same file would be decoded once per request.
//
// Storix - modern web file manager for servers.
// Developed by X Project.
package thumbs

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	xdraw "golang.org/x/image/draw"

	// Decoder registrations. image/jpeg and image/png are imported above for
	// encoding and register themselves as a side effect of that import.
	_ "image/gif"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// Errors reported by the cache.
var (
	// ErrUnsupported means the file is not an image Storix can decode.
	ErrUnsupported = errors.New("thumbs: unsupported image format")
	// ErrTooLarge means the source exceeds the configured byte ceiling or
	// declares more pixels than the decoder is allowed to materialize.
	ErrTooLarge = errors.New("thumbs: source image is too large")
)

const (
	defaultMaxSourceBytes = 64 << 20
	defaultQuality        = 82

	// maxPixels caps the decoded pixel count. A malicious file can declare a
	// tiny compressed payload and enormous dimensions, and decoding it would
	// allocate width by height by four bytes before anything else runs.
	maxPixels = 100_000_000

	// touchInterval is how stale an entry has to be before a cache hit rewrites
	// its timestamp. Touching on every hit would turn a grid scroll into a
	// burst of metadata writes for no benefit.
	touchInterval = time.Hour

	dirPerm  fs.FileMode = 0o700
	filePerm fs.FileMode = 0o600
)

// sizes is the allowlist of thumbnail edge lengths. Requests are snapped to the
// nearest entry so a caller cannot flood the cache with arbitrary sizes.
var sizes = []int{96, 128, 192, 256, 384, 512, 768, 1024}

// decodable lists the image/format names the cache accepts. Registration alone
// is not enough: another package in the binary may register a decoder Storix
// has not vetted, and this keeps the accepted set explicit.
var decodable = map[string]bool{
	"jpeg": true,
	"png":  true,
	"gif":  true,
	"bmp":  true,
	"tiff": true,
	"webp": true,
}

// Options configure a thumbnail cache.
type Options struct {
	// Dir is the directory that holds the cache. It is created if missing.
	Dir string
	// MaxSourceBytes refuses source files above this size. Zero selects 64 MiB.
	MaxSourceBytes int64
	// MaxAge expires entries that have not been served within the window.
	// Zero keeps entries until Purge or Clear removes them.
	MaxAge time.Duration
	// Quality is the JPEG quality, 1 to 100. Zero selects 82.
	Quality int
}

// Result describes a thumbnail that is ready to be served.
type Result struct {
	// Path is the absolute path of the cached image on disk.
	Path string
	// ContentType is the media type of the cached image.
	ContentType string
	// Width and Height are the thumbnail dimensions in pixels.
	Width, Height int
	// FromCache reports that no decoding happened for this call, either because
	// the entry already existed or because another caller was generating it.
	FromCache bool
}

// call is one in flight generation shared by every caller of the same key.
type call struct {
	wg  sync.WaitGroup
	res Result
	err error
}

// Cache renders thumbnails and stores them under a directory it owns.
// A Cache is safe for concurrent use.
type Cache struct {
	dir      string
	maxBytes int64
	maxAge   time.Duration
	quality  int

	mu     sync.Mutex
	flight map[string]*call
}

// New prepares a cache rooted at opts.Dir, creating the directory if needed.
func New(opts Options) (*Cache, error) {
	dir := strings.TrimSpace(opts.Dir)
	if dir == "" {
		return nil, errors.New("thumbs: cache directory is required")
	}
	dir = filepath.Clean(dir)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, fmt.Errorf("thumbs: create cache directory: %w", err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("thumbs: resolve cache directory: %w", err)
	}
	c := &Cache{
		dir:      abs,
		maxBytes: opts.MaxSourceBytes,
		maxAge:   opts.MaxAge,
		quality:  opts.Quality,
		flight:   make(map[string]*call),
	}
	if c.maxBytes <= 0 {
		c.maxBytes = defaultMaxSourceBytes
	}
	if c.quality <= 0 {
		c.quality = defaultQuality
	}
	if c.quality > 100 {
		c.quality = 100
	}
	if c.maxAge < 0 {
		c.maxAge = 0
	}
	return c, nil
}

// Dir reports the directory the cache writes to.
func (c *Cache) Dir() string { return c.dir }

// Sizes returns the allowed thumbnail edge lengths in ascending order.
func Sizes() []int { return append([]int(nil), sizes...) }

// ClampSize snaps a requested edge length to the nearest allowed size. Ties go
// to the smaller size, and any out of range request lands on an end of the list.
func ClampSize(size int) int {
	best := sizes[0]
	bestDiff := diff(size, best)
	for _, s := range sizes[1:] {
		if d := diff(size, s); d < bestDiff {
			best, bestDiff = s, d
		}
	}
	return best
}

// Supported reports whether a file name looks like an image the cache can read.
// It is a name check only, so decoding may still fail on a corrupt file.
func Supported(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".tif", ".tiff":
		return true
	}
	return false
}

// Thumbnail returns a cached thumbnail for rel inside root, generating it when
// it is missing. info may be nil, in which case the file is stat'ed first; when
// the caller already has the listing entry, passing it saves a syscall.
//
// The returned Result is owned by the caller. Errors from a broken or oversized
// source are reported as ErrUnsupported and ErrTooLarge.
func (c *Cache) Thumbnail(ctx context.Context, root *os.Root, rel string, size int, info fs.FileInfo) (*Result, error) {
	if root == nil {
		return nil, errors.New("thumbs: nil root")
	}
	rel = strings.TrimSpace(rel)
	if rel == "" || rel == "." {
		return nil, ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if info == nil {
		st, err := root.Stat(rel)
		if err != nil {
			return nil, err
		}
		info = st
	}
	if info.IsDir() {
		return nil, ErrUnsupported
	}
	if !info.Mode().IsRegular() {
		return nil, ErrUnsupported
	}
	if info.Size() > c.maxBytes {
		return nil, fmt.Errorf("%w: %d bytes", ErrTooLarge, info.Size())
	}

	px := ClampSize(size)
	key := cacheKey(rel, info.ModTime(), info.Size(), px)
	if res, ok := c.lookup(key); ok {
		return res, nil
	}
	return c.single(key, func() (Result, error) {
		return c.render(ctx, root, rel, px, key)
	})
}

// single runs fn once per key and hands every concurrent caller its own copy of
// the outcome. Followers report FromCache because they did no decoding, so
// exactly one caller per generated entry sees FromCache false.
func (c *Cache) single(key string, fn func() (Result, error)) (*Result, error) {
	c.mu.Lock()
	if existing, ok := c.flight[key]; ok {
		c.mu.Unlock()
		existing.wg.Wait()
		if existing.err != nil {
			return nil, existing.err
		}
		res := existing.res
		res.FromCache = true
		return &res, nil
	}
	cur := &call{}
	cur.wg.Add(1)
	c.flight[key] = cur
	c.mu.Unlock()

	cur.res, cur.err = fn()

	c.mu.Lock()
	delete(c.flight, key)
	c.mu.Unlock()
	cur.wg.Done()

	if cur.err != nil {
		return nil, cur.err
	}
	res := cur.res
	return &res, nil
}

// lookup returns an existing entry for the key. The extension is not part of
// the key, so both candidates are probed, cheapest first.
func (c *Cache) lookup(key string) (*Result, bool) {
	for _, ext := range [...]string{"jpg", "png"} {
		p := c.pathFor(key, ext)
		st, err := os.Stat(p)
		if err != nil || !st.Mode().IsRegular() || st.Size() == 0 {
			continue
		}
		if c.maxAge > 0 && time.Since(st.ModTime()) > c.maxAge {
			// Expired. Drop it so the next call regenerates instead of aging further.
			_ = os.Remove(p)
			continue
		}
		w, h, err := imageDimensions(p)
		if err != nil {
			// A truncated entry, most likely from a crash mid write.
			_ = os.Remove(p)
			continue
		}
		c.touch(p, st.ModTime())
		return &Result{
			Path:        p,
			ContentType: contentTypeFor(ext),
			Width:       w,
			Height:      h,
			FromCache:   true,
		}, true
	}
	return nil, false
}

// render decodes the source, scales it and writes the cache entry.
func (c *Cache) render(ctx context.Context, root *os.Root, rel string, px int, key string) (Result, error) {
	// Another caller may have finished between the fast path lookup and the
	// moment this goroutine took the single flight slot.
	if res, ok := c.lookup(key); ok {
		return *res, nil
	}

	f, err := root.Open(rel)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = f.Close() }()

	src, err := c.decode(f)
	if err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	alpha := hasAlpha(src)
	bounds := src.Bounds()
	w, h := fit(bounds.Dx(), bounds.Dy(), px)
	if w <= 0 || h <= 0 {
		return Result{}, ErrUnsupported
	}

	// Scaling happens in premultiplied RGBA, which is the correct space for
	// resampling an image with alpha: it keeps transparent pixels from bleeding
	// their color into their neighbours.
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	if w == bounds.Dx() && h == bounds.Dy() {
		xdraw.Draw(dst, dst.Bounds(), src, bounds.Min, xdraw.Src)
	} else {
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, bounds, xdraw.Src, nil)
	}

	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	data, ext, err := c.encode(dst, alpha)
	if err != nil {
		return Result{}, err
	}
	p, err := c.write(key, ext, data)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Path:        p,
		ContentType: contentTypeFor(ext),
		Width:       w,
		Height:      h,
		FromCache:   false,
	}, nil
}

// decode reads the header first so an oversized or unknown image is refused
// before any pixel buffer is allocated, then rewinds and decodes for real.
func (c *Cache) decode(f *os.File) (image.Image, error) {
	br := bufio.NewReaderSize(io.LimitReader(f, c.maxBytes), 64<<10)
	cfg, format, err := image.DecodeConfig(br)
	if err != nil {
		if errors.Is(err, image.ErrFormat) {
			return nil, ErrUnsupported
		}
		return nil, fmt.Errorf("thumbs: read image header: %w", err)
	}
	if !decodable[format] {
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, format)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, ErrUnsupported
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return nil, fmt.Errorf("%w: %d by %d pixels", ErrTooLarge, cfg.Width, cfg.Height)
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("thumbs: rewind source: %w", err)
	}
	br.Reset(io.LimitReader(f, c.maxBytes))

	img, format, err := image.Decode(br)
	if err != nil {
		if errors.Is(err, image.ErrFormat) {
			return nil, ErrUnsupported
		}
		return nil, fmt.Errorf("thumbs: decode image: %w", err)
	}
	if !decodable[format] {
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, format)
	}
	return img, nil
}

// encode produces the bytes to cache and the file extension to store them under.
func (c *Cache) encode(img image.Image, alpha bool) ([]byte, string, error) {
	var buf bytes.Buffer
	if alpha {
		enc := png.Encoder{CompressionLevel: png.DefaultCompression}
		if err := enc.Encode(&buf, img); err != nil {
			return nil, "", fmt.Errorf("thumbs: encode png: %w", err)
		}
		return buf.Bytes(), "png", nil
	}
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: c.quality}); err != nil {
		return nil, "", fmt.Errorf("thumbs: encode jpeg: %w", err)
	}
	return buf.Bytes(), "jpg", nil
}

// write stores the entry through a temporary file and a rename, so a reader
// never observes a half written thumbnail.
func (c *Cache) write(key, ext string, data []byte) (string, error) {
	shard := filepath.Join(c.dir, key[:2])
	if err := os.MkdirAll(shard, dirPerm); err != nil {
		return "", fmt.Errorf("thumbs: create cache shard: %w", err)
	}
	tmp, err := os.CreateTemp(shard, ".storix-*")
	if err != nil {
		return "", fmt.Errorf("thumbs: create temporary entry: %w", err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return "", fmt.Errorf("thumbs: write entry: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("thumbs: close entry: %w", err)
	}
	final := c.pathFor(key, ext)
	if err := os.Rename(name, final); err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("thumbs: publish entry: %w", err)
	}
	return final, nil
}

// Purge removes entries that have not been served since before and reports how
// many bytes were freed. The count is returned even when the walk fails part
// way through, so a caller can still log the work that was done.
func (c *Cache) Purge(ctx context.Context, before time.Time) (int64, error) {
	var freed int64
	err := filepath.WalkDir(c.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// A shard removed underneath us, by Clear or by another purge, is
			// not a failure of this one.
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !info.ModTime().Before(before) {
			return nil
		}
		size := info.Size()
		if err := os.Remove(p); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		freed += size
		return nil
	})
	if err != nil {
		return freed, fmt.Errorf("thumbs: purge: %w", err)
	}
	c.pruneShards()
	return freed, nil
}

// Clear empties the cache while keeping the directory itself in place.
func (c *Cache) Clear() error {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if mkErr := os.MkdirAll(c.dir, dirPerm); mkErr != nil {
				return fmt.Errorf("thumbs: recreate cache directory: %w", mkErr)
			}
			return nil
		}
		return fmt.Errorf("thumbs: read cache directory: %w", err)
	}
	var firstErr error
	for _, e := range entries {
		if rmErr := os.RemoveAll(filepath.Join(c.dir, e.Name())); rmErr != nil && firstErr == nil {
			firstErr = fmt.Errorf("thumbs: clear cache: %w", rmErr)
		}
	}
	return firstErr
}

// Size reports the bytes on disk and the number of cached entries.
func (c *Cache) Size() (int64, int, error) {
	var bytesUsed int64
	var count int
	err := filepath.WalkDir(c.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		bytesUsed += info.Size()
		count++
		return nil
	})
	if err != nil {
		return bytesUsed, count, fmt.Errorf("thumbs: measure cache: %w", err)
	}
	return bytesUsed, count, nil
}

// pruneShards drops shard directories that a purge emptied. Failures are
// expected under concurrency and are not worth reporting.
func (c *Cache) pruneShards() {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		_ = os.Remove(filepath.Join(c.dir, e.Name()))
	}
}

// touch refreshes the last used timestamp of an entry, but only once the
// recorded time is old enough to be worth a syscall.
func (c *Cache) touch(p string, mod time.Time) {
	now := time.Now()
	if now.Sub(mod) < touchInterval {
		return
	}
	// Best effort: a cache that has gone read only should still serve browsing.
	_ = os.Chtimes(p, now, now)
}

// pathFor builds the on disk location of an entry.
func (c *Cache) pathFor(key, ext string) string {
	return filepath.Join(c.dir, key[:2], key+"."+ext)
}

// cacheKey identifies a thumbnail by source identity and requested size. The
// modification time and byte size stand in for the file contents, which is what
// makes an edited file miss the cache instead of serving the old preview.
func cacheKey(rel string, mod time.Time, size int64, px int) string {
	var b strings.Builder
	b.WriteString(path.Clean(filepath.ToSlash(rel)))
	// A separator that cannot appear in a path keeps "a" plus "1" from hashing
	// the same as "a1" plus an empty field.
	b.WriteByte(0)
	b.WriteString(strconv.FormatInt(mod.Unix(), 10))
	b.WriteByte(0)
	b.WriteString(strconv.FormatInt(size, 10))
	b.WriteByte(0)
	b.WriteString(strconv.Itoa(px))
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// imageDimensions reads only the header of a cached entry.
func imageDimensions(p string) (int, int, error) {
	f, err := os.Open(p)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = f.Close() }()
	cfg, _, err := image.DecodeConfig(bufio.NewReaderSize(f, 4<<10))
	if err != nil {
		return 0, 0, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, ErrUnsupported
	}
	return cfg.Width, cfg.Height, nil
}

// fit scales width and height so the longest edge becomes longest, preserving
// the aspect ratio. Images already at or below the target are left alone,
// because upscaling only wastes bytes.
func fit(w, h, longest int) (int, int) {
	if w <= 0 || h <= 0 || longest <= 0 {
		return 0, 0
	}
	if w <= longest && h <= longest {
		return w, h
	}
	// Integer math keeps the long edge exactly on the requested size and cannot
	// drift the way a float multiply can.
	fw, fh, fl := int64(w), int64(h), int64(longest)
	if fw >= fh {
		return longest, int(max((fh*fl+fw/2)/fw, 1))
	}
	return int(max((fw*fl+fh/2)/fh, 1)), longest
}

// hasAlpha reports whether the thumbnail has to keep transparency. A color
// model without alpha settles it outright, and for the rest an image that turns
// out to be fully opaque is encoded as JPEG, which is far smaller in the grid.
func hasAlpha(img image.Image) bool {
	switch img.ColorModel() {
	case color.YCbCrModel, color.GrayModel, color.Gray16Model, color.CMYKModel:
		return false
	}
	if o, ok := img.(interface{ Opaque() bool }); ok {
		return !o.Opaque()
	}
	return true
}

// contentTypeFor maps a stored extension to its media type.
func contentTypeFor(ext string) string {
	if ext == "png" {
		return "image/png"
	}
	return "image/jpeg"
}

// diff is the absolute distance between two sizes.
func diff(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}
