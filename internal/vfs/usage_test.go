package vfs

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The measured tree, with sizes chosen so every ordering below is unambiguous:
//
//	inside/                     12700 bytes, 5 files, 3 folders
//	  photos/                    8200
//	    small.jpg                 200
//	    sub/
//	      really-big.jpg         8000
//	  docs/                      4000
//	    a.txt                    3000
//	    b.txt                    1000
//	  notes.txt                   500
//	outside/                    not mounted
const (
	usageTestTotal   = 12700
	usageTestPhotos  = 8200
	usageTestDocs    = 4000
	usageTestNotes   = 500
	usageTestBiggest = 8000
)

// usageTestVFS builds the tree above and mounts only the inside directory.
func usageTestVFS(t *testing.T) (*VFS, Scope, string) {
	t.Helper()
	base := t.TempDir()
	inside := filepath.Join(base, "inside")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{
		filepath.Join(inside, "photos", "sub"),
		filepath.Join(inside, "docs"),
		outside,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	usageWrite(t, filepath.Join(inside, "photos", "small.jpg"), 200)
	usageWrite(t, filepath.Join(inside, "photos", "sub", "really-big.jpg"), usageTestBiggest)
	usageWrite(t, filepath.Join(inside, "docs", "a.txt"), 3000)
	usageWrite(t, filepath.Join(inside, "docs", "b.txt"), 1000)
	usageWrite(t, filepath.Join(inside, "notes.txt"), usageTestNotes)
	usageWrite(t, filepath.Join(outside, "secret.txt"), 4096)

	v := New(Options{
		TrashDir:     filepath.Join(base, "trash"),
		MaxTextBytes: 1 << 20,
	})
	t.Cleanup(func() { _ = v.Close() })
	scope := Scope{Mounts: []Mount{{Path: Clean(inside), Label: "Inside"}}}
	return v, scope, base
}

// usageWrite creates a file of an exact size.
func usageWrite(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Repeat("x", size)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// usageNode finds a row by name so a test never depends on an index it is not
// the one asserting.
func usageNode(nodes []UsageNode, name string) (UsageNode, bool) {
	for _, n := range nodes {
		if n.Name == name {
			return n, true
		}
	}
	return UsageNode{}, false
}

func TestUsageTotalsAndChildOrder(t *testing.T) {
	v, scope, base := usageTestVFS(t)
	inside := Clean(filepath.Join(base, "inside"))

	report, err := v.Usage(context.Background(), scope, inside, UsageOptions{})
	if err != nil {
		t.Fatalf("Usage failed: %v", err)
	}
	if report.Path != inside {
		t.Errorf("Path = %q, want %q", report.Path, inside)
	}
	if report.Bytes != usageTestTotal {
		t.Errorf("Bytes = %d, want %d", report.Bytes, usageTestTotal)
	}
	if report.Files != 5 {
		t.Errorf("Files = %d, want 5", report.Files)
	}
	if report.Folders != 3 {
		t.Errorf("Folders = %d, want 3", report.Folders)
	}
	if report.Scanned != 8 {
		t.Errorf("Scanned = %d, want 8", report.Scanned)
	}
	if report.Truncated {
		t.Error("a small tree must not report a truncated walk")
	}

	// Only the direct entries appear, largest first.
	if len(report.Children) != 3 {
		t.Fatalf("Children = %d rows, want 3: %+v", len(report.Children), report.Children)
	}
	wantOrder := []struct {
		name  string
		bytes int64
		files int64
		isDir bool
	}{
		{"photos", usageTestPhotos, 2, true},
		{"docs", usageTestDocs, 2, true},
		{"notes.txt", usageTestNotes, 1, false},
	}
	for i, want := range wantOrder {
		got := report.Children[i]
		if got.Name != want.name {
			t.Errorf("Children[%d].Name = %q, want %q", i, got.Name, want.name)
			continue
		}
		if got.Bytes != want.bytes {
			t.Errorf("%s bytes = %d, want %d", want.name, got.Bytes, want.bytes)
		}
		if got.Files != want.files {
			t.Errorf("%s files = %d, want %d", want.name, got.Files, want.files)
		}
		if got.IsDir != want.isDir {
			t.Errorf("%s isDir = %v, want %v", want.name, got.IsDir, want.isDir)
		}
		if got.Path != Clean(filepath.Join(base, "inside", want.name)) {
			t.Errorf("%s path = %q", want.name, got.Path)
		}
	}
	if kind := report.Children[0].Kind; kind != KindFolder {
		t.Errorf("a folder row must carry the folder kind, got %q", kind)
	}
	if kind := report.Children[2].Kind; kind != KindText {
		t.Errorf("notes.txt kind = %q, want %q", kind, KindText)
	}
}

func TestUsageLargestFindsADeepFile(t *testing.T) {
	v, scope, base := usageTestVFS(t)
	inside := Clean(filepath.Join(base, "inside"))

	report, err := v.Usage(context.Background(), scope, inside, UsageOptions{})
	if err != nil {
		t.Fatalf("Usage failed: %v", err)
	}
	if len(report.Largest) != 5 {
		t.Fatalf("Largest = %d rows, want one per file: %+v", len(report.Largest), report.Largest)
	}
	top := report.Largest[0]
	if top.Name != "really-big.jpg" {
		t.Fatalf("Largest[0] = %q, want the file two levels down", top.Name)
	}
	if top.Bytes != usageTestBiggest {
		t.Errorf("Largest[0].Bytes = %d, want %d", top.Bytes, usageTestBiggest)
	}
	want := Clean(filepath.Join(base, "inside", "photos", "sub", "really-big.jpg"))
	if top.Path != want {
		t.Errorf("Largest[0].Path = %q, want %q", top.Path, want)
	}
	if top.IsDir {
		t.Error("Largest must list files, never folders")
	}
	// The list stays ordered all the way down, and holds no folders.
	for i := 1; i < len(report.Largest); i++ {
		if report.Largest[i-1].Bytes < report.Largest[i].Bytes {
			t.Fatalf("Largest is out of order at %d: %+v", i, report.Largest)
		}
		if report.Largest[i].IsDir {
			t.Errorf("Largest[%d] is a folder", i)
		}
	}
	if _, ok := usageNode(report.Largest, "photos"); ok {
		t.Error("a folder reached the Largest list")
	}
}

func TestUsageByKindAndPercentages(t *testing.T) {
	v, scope, base := usageTestVFS(t)
	inside := Clean(filepath.Join(base, "inside"))

	report, err := v.Usage(context.Background(), scope, inside, UsageOptions{})
	if err != nil {
		t.Fatalf("Usage failed: %v", err)
	}
	if len(report.ByKind) != 2 {
		t.Fatalf("ByKind = %d rows, want images and text: %+v", len(report.ByKind), report.ByKind)
	}
	if report.ByKind[0].Kind != KindImage || report.ByKind[0].Bytes != usageTestPhotos {
		t.Errorf("ByKind[0] = %+v, want %d image bytes first", report.ByKind[0], usageTestPhotos)
	}
	if report.ByKind[0].Files != 2 {
		t.Errorf("image files = %d, want 2", report.ByKind[0].Files)
	}
	if report.ByKind[1].Kind != KindText || report.ByKind[1].Bytes != 4500 {
		t.Errorf("ByKind[1] = %+v, want 4500 text bytes second", report.ByKind[1])
	}

	var childSum, kindSum float64
	for _, c := range report.Children {
		childSum += c.Percent
	}
	for _, k := range report.ByKind {
		kindSum += k.Percent
	}
	if math.Abs(childSum-100) > 0.5 {
		t.Errorf("child percentages add up to %.2f, want about 100", childSum)
	}
	if math.Abs(kindSum-100) > 0.5 {
		t.Errorf("kind percentages add up to %.2f, want about 100", kindSum)
	}
	if got := report.Children[0].Percent; math.Abs(got-64.57) > 0.01 {
		t.Errorf("photos percent = %.2f, want 64.57", got)
	}
}

func TestUsageLimitCapsEveryList(t *testing.T) {
	v, scope, base := usageTestVFS(t)
	inside := Clean(filepath.Join(base, "inside"))

	report, err := v.Usage(context.Background(), scope, inside, UsageOptions{Limit: 1})
	if err != nil {
		t.Fatalf("Usage failed: %v", err)
	}
	if len(report.Children) != 1 || report.Children[0].Name != "photos" {
		t.Errorf("Children = %+v, want the single largest entry", report.Children)
	}
	if len(report.Largest) != 1 || report.Largest[0].Name != "really-big.jpg" {
		t.Errorf("Largest = %+v, want the single largest file", report.Largest)
	}
	// The totals still cover the whole tree, only the lists are shortened.
	if report.Bytes != usageTestTotal {
		t.Errorf("Bytes = %d, want the full %d", report.Bytes, usageTestTotal)
	}
}

func TestUsageTruncatesAtTheEntryCeiling(t *testing.T) {
	v, scope, base := usageTestVFS(t)
	inside := Clean(filepath.Join(base, "inside"))

	report, err := v.Usage(context.Background(), scope, inside, UsageOptions{MaxEntries: 2})
	if err != nil {
		t.Fatalf("a capped walk must still return a report: %v", err)
	}
	if !report.Truncated {
		t.Error("Truncated = false, want true once the entry ceiling stops the walk")
	}
	if report.Scanned != 2 {
		t.Errorf("Scanned = %d, want 2", report.Scanned)
	}
	if report.Bytes >= usageTestTotal {
		t.Errorf("a partial walk reported the full %d bytes", report.Bytes)
	}
}

func TestUsageReportsProgress(t *testing.T) {
	v, scope, base := usageTestVFS(t)
	inside := Clean(filepath.Join(base, "inside"))

	calls := 0
	last := ""
	_, err := v.Usage(context.Background(), scope, inside, UsageOptions{
		Progress: func(scanned int, current string) {
			calls++
			last = current
		},
	})
	if err != nil {
		t.Fatalf("Usage failed: %v", err)
	}
	if calls == 0 {
		t.Fatal("the progress callback was never called")
	}
	if !strings.HasPrefix(last, inside) {
		t.Errorf("progress reported %q, which is not inside the mount", last)
	}
}

func TestUsageIgnoresSymlinks(t *testing.T) {
	base := t.TempDir()
	inside := filepath.Join(base, "inside")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	usageWrite(t, filepath.Join(inside, "real.bin"), 1000)
	if err := os.Symlink(filepath.Join(inside, "real.bin"), filepath.Join(inside, "link.bin")); err != nil {
		t.Skipf("this platform does not allow creating symlinks here: %v", err)
	}

	v := New(Options{MaxTextBytes: 1 << 20})
	t.Cleanup(func() { _ = v.Close() })
	scope := Scope{Mounts: []Mount{{Path: Clean(inside)}}}

	report, err := v.Usage(context.Background(), scope, Clean(inside), UsageOptions{})
	if err != nil {
		t.Fatalf("Usage failed: %v", err)
	}
	if report.Bytes != 1000 {
		t.Errorf("Bytes = %d, want 1000: a symlink is not the space its target uses", report.Bytes)
	}
	if report.Files != 1 {
		t.Errorf("Files = %d, want 1", report.Files)
	}
	if _, ok := usageNode(report.Children, "link.bin"); ok {
		t.Error("a symlink appeared as a child row")
	}
}

func TestUsageRefusesAPathOutsideTheScope(t *testing.T) {
	v, scope, base := usageTestVFS(t)

	outside := Clean(filepath.Join(base, "outside"))
	if _, err := v.Usage(context.Background(), scope, outside, UsageOptions{}); !errors.Is(err, ErrForbidden) {
		t.Errorf("Usage(%q) error = %v, want ErrForbidden", outside, err)
	}
	escape := Clean(filepath.Join(base, "inside", "..", "outside"))
	if _, err := v.Usage(context.Background(), scope, escape, UsageOptions{}); !errors.Is(err, ErrForbidden) {
		t.Errorf("a path that climbs out of the mount must be refused, got %v", err)
	}
	if _, err := v.Usage(context.Background(), scope, "", UsageOptions{}); !errors.Is(err, ErrForbidden) {
		t.Errorf("an empty path must be refused, got %v", err)
	}
}

func TestUsageRefusesAFile(t *testing.T) {
	v, scope, base := usageTestVFS(t)
	file := Clean(filepath.Join(base, "inside", "notes.txt"))

	if _, err := v.Usage(context.Background(), scope, file, UsageOptions{}); !errors.Is(err, ErrNotDir) {
		t.Errorf("Usage on a file error = %v, want ErrNotDir", err)
	}
	missing := Clean(filepath.Join(base, "inside", "nope"))
	if _, err := v.Usage(context.Background(), scope, missing, UsageOptions{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Usage on a missing folder error = %v, want ErrNotFound", err)
	}
}

func TestUsageStopsWhenTheCallerCancels(t *testing.T) {
	v, scope, base := usageTestVFS(t)
	inside := Clean(filepath.Join(base, "inside"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := v.Usage(ctx, scope, inside, UsageOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("Usage error = %v, want context.Canceled", err)
	}
}
