package vfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The scanned tree. Every size below is deliberate, because the scan decides
// what to read from the size alone before it opens anything:
//
//	inside/
//	  a/
//	    alpha.bin           2048 bytes of A   one of three copies
//	    beta.bin            2048 bytes of A   one of three copies
//	  gamma.bin             2048 bytes of A   one of three copies
//	  same-size-1.bin       4096 bytes of X   same size as the next file only
//	  same-size-2.bin       4096 bytes of Y   same size as the last file only
//	  unique.bin            3072 bytes        the only file of its size
//	  tiny-1.txt              16 bytes of T   identical, but under the floor
//	  tiny-2.txt              16 bytes of T   identical, but under the floor
//	  head-1.bin           98304 bytes        same first 64 KiB, different tail
//	  head-2.bin           98304 bytes        same first 64 KiB, different tail
//	outside/
//	  copy.bin              2048 bytes of A   a fourth copy, but not mounted
const (
	// dupTestCopySize is the size of the three identical files.
	dupTestCopySize = 2048
	// dupTestWasted is what deleting every copy but one would free.
	dupTestWasted = 2 * dupTestCopySize
	// dupTestTinySize is below the default floor, so those files never open.
	dupTestTinySize = 16
	// dupTestHeadSize is larger than the 64 KiB head the first pass reads, so
	// the pair only splits apart once the whole file is hashed.
	dupTestHeadSize = duplicateHeadBytes + (32 << 10)
	// dupTestFiles is how many regular files live inside the mount.
	dupTestFiles = 10
	// dupTestHashed is how many of them have to be read: the three copies, the
	// two files that share a size, and the two that share a head.
	dupTestHashed = 7
)

// dupTestVFS builds the tree above and mounts only the inside directory.
func dupTestVFS(t *testing.T) (*VFS, Scope, string) {
	t.Helper()
	base := t.TempDir()
	inside := filepath.Join(base, "inside")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{filepath.Join(inside, "a"), outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	copies := strings.Repeat("A", dupTestCopySize)
	dupTestWrite(t, filepath.Join(inside, "a", "alpha.bin"), copies)
	dupTestWrite(t, filepath.Join(inside, "a", "beta.bin"), copies)
	dupTestWrite(t, filepath.Join(inside, "gamma.bin"), copies)
	dupTestWrite(t, filepath.Join(inside, "same-size-1.bin"), strings.Repeat("X", 4096))
	dupTestWrite(t, filepath.Join(inside, "same-size-2.bin"), strings.Repeat("Y", 4096))
	dupTestWrite(t, filepath.Join(inside, "unique.bin"), strings.Repeat("U", 3072))
	dupTestWrite(t, filepath.Join(inside, "tiny-1.txt"), strings.Repeat("T", dupTestTinySize))
	dupTestWrite(t, filepath.Join(inside, "tiny-2.txt"), strings.Repeat("T", dupTestTinySize))

	head := strings.Repeat("H", duplicateHeadBytes)
	tail := dupTestHeadSize - duplicateHeadBytes
	dupTestWrite(t, filepath.Join(inside, "head-1.bin"), head+strings.Repeat("a", tail))
	dupTestWrite(t, filepath.Join(inside, "head-2.bin"), head+strings.Repeat("b", tail))

	dupTestWrite(t, filepath.Join(outside, "copy.bin"), copies)

	v := New(Options{
		TrashDir:     filepath.Join(base, "trash"),
		MaxTextBytes: 1 << 20,
	})
	t.Cleanup(func() { _ = v.Close() })
	scope := Scope{Mounts: []Mount{{Path: Clean(inside), Label: "Inside"}}}
	return v, scope, base
}

// dupTestWrite creates a file with exact content.
func dupTestWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// dupTestGroupWith finds the group holding a named file, so a test never
// depends on an ordering it is not the one asserting.
func dupTestGroupWith(groups []DuplicateGroup, name string) (DuplicateGroup, bool) {
	for _, g := range groups {
		for _, f := range g.Files {
			if f.Name == name {
				return g, true
			}
		}
	}
	return DuplicateGroup{}, false
}

func TestDuplicatesGroupsIdenticalFiles(t *testing.T) {
	v, scope, base := dupTestVFS(t)
	inside := Clean(filepath.Join(base, "inside"))

	report, err := v.Duplicates(context.Background(), scope, inside, DuplicateOptions{})
	if err != nil {
		t.Fatalf("Duplicates failed: %v", err)
	}
	if report.Path != inside {
		t.Errorf("Path = %q, want %q", report.Path, inside)
	}
	if report.Truncated {
		t.Error("a small tree must not report a truncated scan")
	}
	if len(report.Groups) != 1 {
		t.Fatalf("Groups = %d, want exactly one: %+v", len(report.Groups), report.Groups)
	}

	group := report.Groups[0]
	if group.Count != 3 || len(group.Files) != 3 {
		t.Fatalf("group holds %d files, want the three copies: %+v", len(group.Files), group.Files)
	}
	if group.Size != dupTestCopySize {
		t.Errorf("group size = %d, want %d", group.Size, dupTestCopySize)
	}
	if group.Wasted != dupTestWasted {
		t.Errorf("Wasted = %d, want %d: one copy is the one you keep", group.Wasted, dupTestWasted)
	}
	if report.Wasted != dupTestWasted {
		t.Errorf("report Wasted = %d, want %d", report.Wasted, dupTestWasted)
	}
	if len(group.Hash) != 64 {
		t.Errorf("Hash = %q, want a sha256 digest", group.Hash)
	}

	// The copies are listed by path, and each one carries its own details.
	wantPaths := []string{
		Clean(filepath.Join(base, "inside", "a", "alpha.bin")),
		Clean(filepath.Join(base, "inside", "a", "beta.bin")),
		Clean(filepath.Join(base, "inside", "gamma.bin")),
	}
	for i, want := range wantPaths {
		if group.Files[i].Path != want {
			t.Errorf("Files[%d].Path = %q, want %q", i, group.Files[i].Path, want)
		}
		if group.Files[i].Size != dupTestCopySize {
			t.Errorf("Files[%d].Size = %d, want %d", i, group.Files[i].Size, dupTestCopySize)
		}
		if group.Files[i].Modified.IsZero() {
			t.Errorf("Files[%d] carries no modification time", i)
		}
	}
	if group.Files[2].Name != "gamma.bin" {
		t.Errorf("Files[2].Name = %q, want gamma.bin", group.Files[2].Name)
	}

	// Only the files that could possibly pair off were opened.
	if report.Scanned != dupTestFiles {
		t.Errorf("Scanned = %d, want %d", report.Scanned, dupTestFiles)
	}
	if report.Hashed != dupTestHashed {
		t.Errorf("Hashed = %d, want %d: a size held by one file is never read", report.Hashed, dupTestHashed)
	}
}

func TestDuplicatesIgnoresSameSizeDifferentContent(t *testing.T) {
	v, scope, base := dupTestVFS(t)
	inside := Clean(filepath.Join(base, "inside"))

	report, err := v.Duplicates(context.Background(), scope, inside, DuplicateOptions{})
	if err != nil {
		t.Fatalf("Duplicates failed: %v", err)
	}
	for _, name := range []string{"same-size-1.bin", "same-size-2.bin", "unique.bin"} {
		if g, ok := dupTestGroupWith(report.Groups, name); ok {
			t.Errorf("%s was grouped as a duplicate: %+v", name, g)
		}
	}
}

func TestDuplicatesReadsPastTheHeadBeforeItDecides(t *testing.T) {
	v, scope, base := dupTestVFS(t)
	inside := Clean(filepath.Join(base, "inside"))

	report, err := v.Duplicates(context.Background(), scope, inside, DuplicateOptions{})
	if err != nil {
		t.Fatalf("Duplicates failed: %v", err)
	}
	// These two agree on their size and on their first 64 KiB, so only the full
	// hash can tell them apart. Grouping them would offer to delete a file that
	// is not a copy of anything.
	for _, name := range []string{"head-1.bin", "head-2.bin"} {
		if g, ok := dupTestGroupWith(report.Groups, name); ok {
			t.Errorf("%s was grouped on its head alone: %+v", name, g)
		}
	}
}

func TestDuplicatesMinSizeExcludesSmallFiles(t *testing.T) {
	v, scope, base := dupTestVFS(t)
	inside := Clean(filepath.Join(base, "inside"))

	report, err := v.Duplicates(context.Background(), scope, inside, DuplicateOptions{})
	if err != nil {
		t.Fatalf("Duplicates failed: %v", err)
	}
	if g, ok := dupTestGroupWith(report.Groups, "tiny-1.txt"); ok {
		t.Errorf("a file under the default floor was grouped: %+v", g)
	}

	// The same pair is found once the floor is lowered, which proves the floor
	// is what excluded it rather than the comparison.
	lowered, err := v.Duplicates(context.Background(), scope, inside, DuplicateOptions{MinSize: 1})
	if err != nil {
		t.Fatalf("Duplicates failed: %v", err)
	}
	tiny, ok := dupTestGroupWith(lowered.Groups, "tiny-1.txt")
	if !ok {
		t.Fatalf("the identical small files were not grouped with a floor of one byte: %+v", lowered.Groups)
	}
	if tiny.Count != 2 || tiny.Wasted != dupTestTinySize {
		t.Errorf("small group = %+v, want two files wasting %d bytes", tiny, dupTestTinySize)
	}

	// A floor above the copies leaves nothing worth reporting.
	raised, err := v.Duplicates(context.Background(), scope, inside, DuplicateOptions{MinSize: dupTestCopySize + 1})
	if err != nil {
		t.Fatalf("Duplicates failed: %v", err)
	}
	if len(raised.Groups) != 0 || raised.Wasted != 0 {
		t.Errorf("Groups = %+v, want none once the floor is above every copy", raised.Groups)
	}
}

func TestDuplicatesStaysInsideTheScope(t *testing.T) {
	v, scope, base := dupTestVFS(t)
	inside := Clean(filepath.Join(base, "inside"))

	report, err := v.Duplicates(context.Background(), scope, inside, DuplicateOptions{})
	if err != nil {
		t.Fatalf("Duplicates failed: %v", err)
	}
	// An identical file sits in outside/, which is not mounted. It must neither
	// join the group nor appear on its own.
	if g, ok := dupTestGroupWith(report.Groups, "copy.bin"); ok {
		t.Errorf("a file outside the mount was reported: %+v", g)
	}
	for _, g := range report.Groups {
		for _, f := range g.Files {
			if !strings.HasPrefix(f.Path, inside) {
				t.Errorf("group holds %q, which is outside the mount", f.Path)
			}
		}
	}

	outside := Clean(filepath.Join(base, "outside"))
	if _, err := v.Duplicates(context.Background(), scope, outside, DuplicateOptions{}); !errors.Is(err, ErrForbidden) {
		t.Errorf("Duplicates(%q) error = %v, want ErrForbidden", outside, err)
	}
	escape := Clean(filepath.Join(base, "inside", "..", "outside"))
	if _, err := v.Duplicates(context.Background(), scope, escape, DuplicateOptions{}); !errors.Is(err, ErrForbidden) {
		t.Errorf("a path that climbs out of the mount must be refused, got %v", err)
	}
	if _, err := v.Duplicates(context.Background(), scope, "", DuplicateOptions{}); !errors.Is(err, ErrForbidden) {
		t.Errorf("an empty path must be refused, got %v", err)
	}
}

func TestDuplicatesIgnoresSymlinks(t *testing.T) {
	base := t.TempDir()
	inside := filepath.Join(base, "inside")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("A", dupTestCopySize)
	dupTestWrite(t, filepath.Join(inside, "real.bin"), body)
	if err := os.Symlink(filepath.Join(inside, "real.bin"), filepath.Join(inside, "link.bin")); err != nil {
		t.Skipf("this platform does not allow creating symlinks here: %v", err)
	}

	v := New(Options{MaxTextBytes: 1 << 20})
	t.Cleanup(func() { _ = v.Close() })
	scope := Scope{Mounts: []Mount{{Path: Clean(inside)}}}

	report, err := v.Duplicates(context.Background(), scope, Clean(inside), DuplicateOptions{})
	if err != nil {
		t.Fatalf("Duplicates failed: %v", err)
	}
	if len(report.Groups) != 0 {
		t.Errorf("Groups = %+v, want none: a link is another name for a file, not a copy of it", report.Groups)
	}
	if report.Scanned != 1 {
		t.Errorf("Scanned = %d, want 1", report.Scanned)
	}
}

func TestDuplicatesTruncatesAtTheFileCeiling(t *testing.T) {
	v, scope, base := dupTestVFS(t)
	inside := Clean(filepath.Join(base, "inside"))

	report, err := v.Duplicates(context.Background(), scope, inside, DuplicateOptions{MaxFiles: 2})
	if err != nil {
		t.Fatalf("a capped scan must still return a report: %v", err)
	}
	if !report.Truncated {
		t.Error("Truncated = false, want true once the file ceiling stops the scan")
	}
	if report.Scanned != 2 {
		t.Errorf("Scanned = %d, want 2", report.Scanned)
	}
}

func TestDuplicatesCapsTheGroupList(t *testing.T) {
	v, scope, base := dupTestVFS(t)
	inside := Clean(filepath.Join(base, "inside"))

	report, err := v.Duplicates(context.Background(), scope, inside, DuplicateOptions{MinSize: 1, MaxGroups: 1})
	if err != nil {
		t.Fatalf("Duplicates failed: %v", err)
	}
	if len(report.Groups) != 1 {
		t.Fatalf("Groups = %d rows, want the single largest: %+v", len(report.Groups), report.Groups)
	}
	if report.Groups[0].Size != dupTestCopySize {
		t.Errorf("the listed group wastes %d bytes each, want the biggest waste first", report.Groups[0].Size)
	}
	// The headline figure still covers the group that was left off the list.
	if report.Wasted != dupTestWasted+dupTestTinySize {
		t.Errorf("Wasted = %d, want every group counted", report.Wasted)
	}
}

func TestDuplicatesReportsProgress(t *testing.T) {
	v, scope, base := dupTestVFS(t)
	inside := Clean(filepath.Join(base, "inside"))

	calls := 0
	seen := 0
	_, err := v.Duplicates(context.Background(), scope, inside, DuplicateOptions{
		Progress: func(scanned, hashed int) {
			calls++
			seen = scanned
		},
	})
	if err != nil {
		t.Fatalf("Duplicates failed: %v", err)
	}
	if calls == 0 {
		t.Fatal("the progress callback was never called")
	}
	if seen <= 0 || seen > dupTestFiles {
		t.Errorf("progress reported %d scanned files, want between 1 and %d", seen, dupTestFiles)
	}
}

func TestDuplicatesRefusesAFile(t *testing.T) {
	v, scope, base := dupTestVFS(t)

	file := Clean(filepath.Join(base, "inside", "gamma.bin"))
	if _, err := v.Duplicates(context.Background(), scope, file, DuplicateOptions{}); !errors.Is(err, ErrNotDir) {
		t.Errorf("Duplicates on a file error = %v, want ErrNotDir", err)
	}
	missing := Clean(filepath.Join(base, "inside", "nope"))
	if _, err := v.Duplicates(context.Background(), scope, missing, DuplicateOptions{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Duplicates on a missing folder error = %v, want ErrNotFound", err)
	}
}

func TestDuplicatesStopsWhenTheCallerCancels(t *testing.T) {
	v, scope, base := dupTestVFS(t)
	inside := Clean(filepath.Join(base, "inside"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := v.Duplicates(ctx, scope, inside, DuplicateOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("Duplicates error = %v, want context.Canceled", err)
	}
}
