package thumbs

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newCache builds a cache in a temporary directory alongside an os.Root over a
// second temporary directory that stands in for a user mount.
func newCache(t *testing.T, opts Options) (*Cache, *os.Root, string) {
	t.Helper()
	if opts.Dir == "" {
		opts.Dir = filepath.Join(t.TempDir(), "thumbs")
	}
	cache, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	files := t.TempDir()
	root, err := os.OpenRoot(files)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Errorf("close root: %v", err)
		}
	})
	return cache, root, files
}

// writePNG renders a solid image of the given size and stores it in dir.
// When alpha is set the top left pixel is fully transparent.
func writePNG(t *testing.T, dir, name string, w, h int, alpha bool) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x % 256), G: uint8(y % 256), B: 0x40, A: 0xFF})
		}
	}
	if alpha {
		img.SetNRGBA(0, 0, color.NRGBA{})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode source png: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write source png: %v", err)
	}
}

func TestThumbnailServesSecondCallFromCache(t *testing.T) {
	cache, root, files := newCache(t, Options{})
	writePNG(t, files, "photo.png", 400, 200, false)

	ctx := context.Background()
	first, err := cache.Thumbnail(ctx, root, "photo.png", 128, nil)
	if err != nil {
		t.Fatalf("first thumbnail: %v", err)
	}
	if first.FromCache {
		t.Fatal("first call reported a cache hit")
	}
	if first.ContentType != "image/jpeg" {
		t.Fatalf("content type = %q, want image/jpeg for an opaque source", first.ContentType)
	}
	if _, err := os.Stat(first.Path); err != nil {
		t.Fatalf("cached file missing: %v", err)
	}

	// The entry must live in a two character shard directory, not at the top.
	shard := filepath.Base(filepath.Dir(first.Path))
	if len(shard) != 2 {
		t.Fatalf("shard directory = %q, want two hex characters", shard)
	}
	if filepath.Dir(filepath.Dir(first.Path)) != cache.Dir() {
		t.Fatalf("entry %q is not directly under the cache directory", first.Path)
	}

	second, err := cache.Thumbnail(ctx, root, "photo.png", 128, nil)
	if err != nil {
		t.Fatalf("second thumbnail: %v", err)
	}
	if !second.FromCache {
		t.Fatal("second call did not report a cache hit")
	}
	if second.Path != first.Path {
		t.Fatalf("second path = %q, want %q", second.Path, first.Path)
	}
	if second.Width != first.Width || second.Height != first.Height {
		t.Fatalf("cached size = %dx%d, want %dx%d", second.Width, second.Height, first.Width, first.Height)
	}
}

func TestThumbnailPreservesAspectRatio(t *testing.T) {
	cache, root, files := newCache(t, Options{})
	writePNG(t, files, "wide.png", 400, 200, false)
	writePNG(t, files, "tall.png", 200, 400, false)
	writePNG(t, files, "odd.png", 333, 100, false)

	cases := []struct {
		name       string
		size       int
		wantW      int
		wantH      int
		sourceW    int
		sourceH    int
		aspectOnly bool
	}{
		{name: "wide.png", size: 128, wantW: 128, wantH: 64, sourceW: 400, sourceH: 200},
		{name: "tall.png", size: 128, wantW: 64, wantH: 128, sourceW: 200, sourceH: 400},
		{name: "odd.png", size: 256, wantW: 256, wantH: 77, sourceW: 333, sourceH: 100},
	}
	for _, tc := range cases {
		res, err := cache.Thumbnail(context.Background(), root, tc.name, tc.size, nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if res.Width != tc.wantW || res.Height != tc.wantH {
			t.Errorf("%s: size = %dx%d, want %dx%d", tc.name, res.Width, res.Height, tc.wantW, tc.wantH)
		}
		if longest(res.Width, res.Height) != tc.size {
			t.Errorf("%s: longest edge = %d, want %d", tc.name, longest(res.Width, res.Height), tc.size)
		}
		// The rendered ratio must stay within one pixel of the source ratio.
		want := float64(tc.sourceW) / float64(tc.sourceH)
		got := float64(res.Width) / float64(res.Height)
		tolerance := want / float64(min(res.Width, res.Height))
		if got < want-tolerance || got > want+tolerance {
			t.Errorf("%s: ratio = %.4f, want %.4f", tc.name, got, want)
		}
	}
}

func TestThumbnailNeverUpscales(t *testing.T) {
	cache, root, files := newCache(t, Options{})
	writePNG(t, files, "tiny.png", 10, 5, false)

	res, err := cache.Thumbnail(context.Background(), root, "tiny.png", 512, nil)
	if err != nil {
		t.Fatalf("thumbnail: %v", err)
	}
	if res.Width != 10 || res.Height != 5 {
		t.Fatalf("size = %dx%d, want the source 10x5", res.Width, res.Height)
	}
}

func TestThumbnailAlphaStaysPNG(t *testing.T) {
	cache, root, files := newCache(t, Options{})
	writePNG(t, files, "logo.png", 300, 300, true)

	res, err := cache.Thumbnail(context.Background(), root, "logo.png", 192, nil)
	if err != nil {
		t.Fatalf("thumbnail: %v", err)
	}
	if res.ContentType != "image/png" {
		t.Fatalf("content type = %q, want image/png for a source with alpha", res.ContentType)
	}
	if filepath.Ext(res.Path) != ".png" {
		t.Fatalf("cached file = %q, want a .png extension", res.Path)
	}
}

func TestThumbnailUnsupported(t *testing.T) {
	cache, root, files := newCache(t, Options{})
	if err := os.WriteFile(filepath.Join(files, "notes.txt"), []byte("this is not an image at all"), 0o600); err != nil {
		t.Fatalf("write text file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(files, "folder"), 0o700); err != nil {
		t.Fatalf("make directory: %v", err)
	}

	if _, err := cache.Thumbnail(context.Background(), root, "notes.txt", 128, nil); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("text file error = %v, want ErrUnsupported", err)
	}
	if _, err := cache.Thumbnail(context.Background(), root, "folder", 128, nil); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("directory error = %v, want ErrUnsupported", err)
	}
}

func TestThumbnailRefusesLargeSource(t *testing.T) {
	cache, root, files := newCache(t, Options{MaxSourceBytes: 64})
	writePNG(t, files, "big.png", 200, 200, false)

	_, err := cache.Thumbnail(context.Background(), root, "big.png", 128, nil)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("error = %v, want ErrTooLarge", err)
	}
}

func TestThumbnailSingleFlight(t *testing.T) {
	cache, root, files := newCache(t, Options{})
	writePNG(t, files, "grid.png", 600, 400, false)

	const callers = 24
	var generated atomic.Int64
	var wg sync.WaitGroup
	errs := make([]error, callers)
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			res, err := cache.Thumbnail(context.Background(), root, "grid.png", 256, nil)
			if err != nil {
				errs[idx] = err
				return
			}
			if !res.FromCache {
				generated.Add(1)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
	if got := generated.Load(); got != 1 {
		t.Fatalf("decoded %d times, want exactly 1", got)
	}
}

func TestClampSize(t *testing.T) {
	cases := map[int]int{
		-40:   96,
		0:     96,
		100:   96,
		128:   128,
		160:   128, // A tie resolves to the smaller size.
		200:   192,
		500:   512,
		99999: 1024,
	}
	for in, want := range cases {
		if got := ClampSize(in); got != want {
			t.Errorf("ClampSize(%d) = %d, want %d", in, got, want)
		}
	}
	if got := Sizes(); len(got) != len(sizes) {
		t.Fatalf("Sizes() length = %d, want %d", len(got), len(sizes))
	}
	// Sizes must hand back a copy, not the package level slice.
	got := Sizes()
	got[0] = -1
	if sizes[0] == -1 {
		t.Fatal("Sizes() exposed the package level slice")
	}
}

func TestClampedSizeSharesOneEntry(t *testing.T) {
	cache, root, files := newCache(t, Options{})
	writePNG(t, files, "shared.png", 800, 400, false)

	ctx := context.Background()
	first, err := cache.Thumbnail(ctx, root, "shared.png", 250, nil)
	if err != nil {
		t.Fatalf("first thumbnail: %v", err)
	}
	// 250 and 260 both clamp to 256, so the second request must reuse the entry.
	second, err := cache.Thumbnail(ctx, root, "shared.png", 260, nil)
	if err != nil {
		t.Fatalf("second thumbnail: %v", err)
	}
	if !second.FromCache || second.Path != first.Path {
		t.Fatalf("clamped request produced a new entry %q, want %q", second.Path, first.Path)
	}
}

func TestSizePurgeAndClear(t *testing.T) {
	cache, root, files := newCache(t, Options{})
	writePNG(t, files, "one.png", 300, 200, false)
	writePNG(t, files, "two.png", 200, 300, false)

	ctx := context.Background()
	for _, name := range []string{"one.png", "two.png"} {
		if _, err := cache.Thumbnail(ctx, root, name, 128, nil); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}

	bytesUsed, count, err := cache.Size()
	if err != nil {
		t.Fatalf("Size: %v", err)
	}
	if count != 2 || bytesUsed <= 0 {
		t.Fatalf("Size = %d bytes in %d entries, want 2 non empty entries", bytesUsed, count)
	}

	// Nothing is older than the epoch, so this purge must be a no-op.
	freed, err := cache.Purge(ctx, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if freed != 0 {
		t.Fatalf("purge freed %d bytes, want 0", freed)
	}

	freed, err = cache.Purge(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if freed != bytesUsed {
		t.Fatalf("purge freed %d bytes, want %d", freed, bytesUsed)
	}
	if _, count, err = cache.Size(); err != nil || count != 0 {
		t.Fatalf("after purge Size = %d entries (err %v), want 0", count, err)
	}

	if _, err := cache.Thumbnail(ctx, root, "one.png", 128, nil); err != nil {
		t.Fatalf("regenerate after purge: %v", err)
	}
	if err := cache.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	bytesUsed, count, err = cache.Size()
	if err != nil {
		t.Fatalf("Size after clear: %v", err)
	}
	if bytesUsed != 0 || count != 0 {
		t.Fatalf("after clear Size = %d bytes in %d entries, want 0", bytesUsed, count)
	}
	if _, err := os.Stat(cache.Dir()); err != nil {
		t.Fatalf("cache directory gone after clear: %v", err)
	}
}

func TestMaxAgeExpiresEntry(t *testing.T) {
	cache, root, files := newCache(t, Options{MaxAge: time.Millisecond})
	writePNG(t, files, "aging.png", 240, 120, false)

	ctx := context.Background()
	first, err := cache.Thumbnail(ctx, root, "aging.png", 128, nil)
	if err != nil {
		t.Fatalf("first thumbnail: %v", err)
	}
	// Age the entry past the window instead of sleeping for it.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(first.Path, old, old); err != nil {
		t.Fatalf("age entry: %v", err)
	}
	second, err := cache.Thumbnail(ctx, root, "aging.png", 128, nil)
	if err != nil {
		t.Fatalf("second thumbnail: %v", err)
	}
	if second.FromCache {
		t.Fatal("expired entry was served from cache")
	}
}

func TestModifiedSourceGetsNewEntry(t *testing.T) {
	cache, root, files := newCache(t, Options{})
	writePNG(t, files, "edit.png", 300, 150, false)

	ctx := context.Background()
	first, err := cache.Thumbnail(ctx, root, "edit.png", 128, nil)
	if err != nil {
		t.Fatalf("first thumbnail: %v", err)
	}

	// Rewrite the file with different dimensions and a distinct timestamp.
	writePNG(t, files, "edit.png", 150, 300, false)
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(filepath.Join(files, "edit.png"), later, later); err != nil {
		t.Fatalf("touch source: %v", err)
	}

	second, err := cache.Thumbnail(ctx, root, "edit.png", 128, nil)
	if err != nil {
		t.Fatalf("second thumbnail: %v", err)
	}
	if second.Path == first.Path {
		t.Fatal("edited source reused the previous cache entry")
	}
	if second.Width != 64 || second.Height != 128 {
		t.Fatalf("size = %dx%d, want 64x128", second.Width, second.Height)
	}
}

func TestSupported(t *testing.T) {
	for _, name := range []string{"a.JPG", "b.jpeg", "c.png", "d.gif", "e.webp", "f.bmp", "g.tif", "h.tiff"} {
		if !Supported(name) {
			t.Errorf("Supported(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"a.svg", "b.mp4", "c.txt", "d", "e.avif"} {
		if Supported(name) {
			t.Errorf("Supported(%q) = true, want false", name)
		}
	}
}

func TestNewRejectsEmptyDir(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("New with no directory returned no error")
	}
}

// longest returns the larger of two edge lengths.
func longest(w, h int) int {
	if w > h {
		return w
	}
	return h
}
