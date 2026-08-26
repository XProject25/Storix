package vfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// newTestVFS builds a guarded layer over a temporary tree:
//
//	root/
//	  inside/keep.txt
//	  inside/sub/deep.txt
//	  outside/secret.txt      not mounted
func newTestVFS(t *testing.T) (*VFS, Scope, string) {
	t.Helper()
	base := t.TempDir()
	inside := filepath.Join(base, "inside")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(filepath.Join(inside, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(inside, "keep.txt"), "keep")
	write(t, filepath.Join(inside, "sub", "deep.txt"), "deep content here")
	write(t, filepath.Join(outside, "secret.txt"), "secret")

	v := New(Options{
		Denied:       []string{Clean(filepath.Join(base, "denied"))},
		TrashDir:     filepath.Join(base, "trash"),
		MaxTextBytes: 1 << 20,
	})
	t.Cleanup(func() { _ = v.Close() })
	scope := Scope{Mounts: []Mount{{Path: Clean(inside), Label: "Inside"}}}
	return v, scope, base
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestClean(t *testing.T) {
	cases := map[string]string{
		"/home/user":        "/home/user",
		"/home/user/":       "/home/user",
		"home/user":         "/home/user",
		"/home//user/./x":   "/home/user/x",
		"/home/user/../etc": "/home/etc",
		"/../../../etc":     "/etc",
		"":                  "",
		"   ":               "",
		"/":                 "/",
		`\home\user`:        "/home/user",
	}
	for in, want := range cases {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestContainsRespectsBoundaries(t *testing.T) {
	cases := []struct {
		parent, child string
		want          bool
	}{
		{"/home/user", "/home/user", true},
		{"/home/user", "/home/user/docs", true},
		{"/home/user", "/home/user2", false},
		{"/home/user", "/home/user2/docs", false},
		{"/home/user", "/home", false},
		{"/", "/anything", true},
		{"/home/user/", "/home/user/x", true},
		{"", "/home", false},
	}
	for _, c := range cases {
		if got := Contains(c.parent, c.child); got != c.want {
			t.Errorf("Contains(%q, %q) = %v, want %v", c.parent, c.child, got, c.want)
		}
	}
}

func TestValidName(t *testing.T) {
	bad := []string{"", ".", "..", "a/b", `a\b`, "a\x00b", "   ", strings.Repeat("x", 256)}
	for _, name := range bad {
		if err := ValidName(name); err == nil {
			t.Errorf("ValidName(%q) accepted a name it should refuse", name)
		}
	}
	good := []string{"file.txt", ".env", "a b c", "naïve.txt", strings.Repeat("x", 255)}
	for _, name := range good {
		if err := ValidName(name); err != nil {
			t.Errorf("ValidName(%q) refused a valid name: %v", name, err)
		}
	}
}

func TestResolveStaysInsideTheMount(t *testing.T) {
	v, scope, base := newTestVFS(t)
	inside := Clean(filepath.Join(base, "inside"))
	outside := Clean(filepath.Join(base, "outside"))

	if _, err := v.Resolve(scope, inside); err != nil {
		t.Fatalf("the mount itself must resolve: %v", err)
	}
	if _, err := v.Resolve(scope, inside+"/keep.txt"); err != nil {
		t.Fatalf("a file inside the mount must resolve: %v", err)
	}

	refused := []string{
		outside,
		outside + "/secret.txt",
		inside + "/../outside/secret.txt",
		Clean(base),
		"/",
		inside + "2",
	}
	for _, p := range refused {
		if _, err := v.Resolve(scope, p); err == nil {
			t.Errorf("Resolve(%q) succeeded, it must be refused", p)
		}
	}
}

func TestDeniedPathsAreRefused(t *testing.T) {
	base := t.TempDir()
	secrets := filepath.Join(base, "secrets")
	if err := os.MkdirAll(secrets, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(secrets, "keys.txt"), "x")

	v := New(Options{Denied: []string{Clean(secrets)}, TrashDir: filepath.Join(base, "trash")})
	t.Cleanup(func() { _ = v.Close() })
	scope := Scope{Mounts: []Mount{{Path: Clean(base)}}, Admin: true}

	if !v.Denied(Clean(secrets)) {
		t.Fatal("the denied folder must report as denied")
	}
	if !v.Denied(Clean(secrets) + "/keys.txt") {
		t.Fatal("a file inside a denied folder must report as denied")
	}
	if _, err := v.Resolve(scope, Clean(secrets)+"/keys.txt"); err == nil {
		t.Fatal("resolving inside a denied folder must fail even for an administrator")
	}
	// The parent still lists, but the denied child is filtered out.
	listing, err := v.List(scope, Clean(base), ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range listing.Entries {
		if e.Name == "secrets" {
			t.Fatal("a denied folder must not appear in a listing")
		}
	}
}

func TestSymlinkCannotEscapeTheMount(t *testing.T) {
	v, scope, base := newTestVFS(t)
	inside := Clean(filepath.Join(base, "inside"))
	link := filepath.Join(base, "inside", "escape")
	if err := os.Symlink(filepath.Join(base, "outside"), link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("creating a symlink needs privileges on this platform")
		}
		t.Fatal(err)
	}

	// The link may be seen, but reading through it must not leave the mount,
	// and the refusal has to arrive as a refusal rather than as an unexplained
	// server error the interface cannot phrase for the user.
	if _, err := v.Stat(scope, inside+"/escape"); err != nil {
		t.Fatalf("the link itself should be visible: %v", err)
	}
	if _, _, err := v.Open(scope, inside+"/escape/secret.txt"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("reading through an escaping symlink must report ErrForbidden, got %v", err)
	}
	if _, err := v.List(scope, inside+"/escape", ListOptions{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("listing through an escaping symlink must report ErrForbidden, got %v", err)
	}
	if _, err := v.ReadText(scope, inside+"/escape/secret.txt"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("reading text through an escaping symlink must report ErrForbidden, got %v", err)
	}
}

func TestReadOnlyMountRefusesWrites(t *testing.T) {
	v, _, base := newTestVFS(t)
	inside := Clean(filepath.Join(base, "inside"))
	scope := Scope{Mounts: []Mount{{Path: inside, ReadOnly: true}}}

	if _, err := v.Resolve(scope, inside+"/keep.txt"); err != nil {
		t.Fatalf("reading from a read only mount must still work: %v", err)
	}
	if _, err := v.ResolveWritable(scope, inside+"/keep.txt"); err != ErrReadOnly {
		t.Fatalf("writing to a read only mount must fail with ErrReadOnly, got %v", err)
	}
	if _, err := v.Mkdir(scope, inside, "new"); err != ErrReadOnly {
		t.Fatalf("Mkdir on a read only mount must fail with ErrReadOnly, got %v", err)
	}
}

func TestListHidesInternalArtifacts(t *testing.T) {
	v, scope, base := newTestVFS(t)
	inside := Clean(filepath.Join(base, "inside"))
	write(t, filepath.Join(base, "inside", PartName("abc123")), "partial upload")

	listing, err := v.List(scope, inside, ListOptions{ShowHidden: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range listing.Entries {
		if IsInternal(e.Name) {
			t.Fatalf("an in flight upload artifact leaked into the listing: %s", e.Name)
		}
	}
}

func TestFileOperationsRoundTrip(t *testing.T) {
	v, scope, base := newTestVFS(t)
	inside := Clean(filepath.Join(base, "inside"))
	ctx := context.Background()

	if _, err := v.Mkdir(scope, inside, "docs"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if _, err := v.Mkdir(scope, inside, "docs"); err != ErrExists {
		t.Fatalf("creating the same folder twice must report ErrExists, got %v", err)
	}

	if _, err := v.Copy(ctx, scope, []string{inside + "/keep.txt"}, inside+"/docs", ConflictRename, nil); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if _, err := v.Stat(scope, inside+"/docs/keep.txt"); err != nil {
		t.Fatalf("the copy should exist: %v", err)
	}

	// A second copy must not overwrite, it lands beside the first.
	if _, err := v.Copy(ctx, scope, []string{inside + "/keep.txt"}, inside+"/docs", ConflictRename, nil); err != nil {
		t.Fatalf("second Copy: %v", err)
	}
	if _, err := v.Stat(scope, inside+"/docs/keep (2).txt"); err != nil {
		t.Fatalf("the conflicting copy should have been renamed: %v", err)
	}

	if _, err := v.Rename(scope, inside+"/docs/keep.txt", "renamed.txt"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := v.Stat(scope, inside+"/docs/keep.txt"); err != ErrNotFound {
		t.Fatalf("the old name must be gone, got %v", err)
	}

	if _, err := v.Move(ctx, scope, []string{inside + "/docs/renamed.txt"}, inside+"/sub", ConflictRename, nil); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if _, err := v.Stat(scope, inside+"/sub/renamed.txt"); err != nil {
		t.Fatalf("the moved file should exist at the destination: %v", err)
	}
}

func TestTextEditKeepsTheOriginalOnFailure(t *testing.T) {
	v, scope, base := newTestVFS(t)
	inside := Clean(filepath.Join(base, "inside"))

	file, err := v.ReadText(scope, inside+"/keep.txt")
	if err != nil {
		t.Fatal(err)
	}
	if file.Content != "keep" {
		t.Fatalf("content = %q, want %q", file.Content, "keep")
	}
	if _, err := v.WriteText(scope, inside+"/keep.txt", "changed"); err != nil {
		t.Fatal(err)
	}
	again, err := v.ReadText(scope, inside+"/keep.txt")
	if err != nil {
		t.Fatal(err)
	}
	if again.Content != "changed" {
		t.Fatalf("content = %q, want %q", again.Content, "changed")
	}
	// Oversized content is refused before the file is touched.
	v.opts.MaxTextBytes = 4
	if _, err := v.WriteText(scope, inside+"/keep.txt", "far too long"); err != ErrTooLarge {
		t.Fatalf("an oversized write must fail with ErrTooLarge, got %v", err)
	}
	v.opts.MaxTextBytes = 1 << 20
	final, err := v.ReadText(scope, inside+"/keep.txt")
	if err != nil {
		t.Fatal(err)
	}
	if final.Content != "changed" {
		t.Fatalf("a refused write must leave the original intact, got %q", final.Content)
	}
}

func TestTrashRoundTrip(t *testing.T) {
	v, scope, base := newTestVFS(t)
	inside := Clean(filepath.Join(base, "inside"))
	ctx := context.Background()

	record, err := v.MoveToTrash(ctx, scope, inside+"/keep.txt")
	if err != nil {
		t.Fatalf("MoveToTrash: %v", err)
	}
	if _, err := v.Stat(scope, inside+"/keep.txt"); err != ErrNotFound {
		t.Fatalf("the file must be gone from its folder, got %v", err)
	}
	if record.Size != 4 {
		t.Fatalf("recorded size = %d, want 4", record.Size)
	}

	restored, err := v.RestoreFromTrash(ctx, scope, *record, ConflictRename)
	if err != nil {
		t.Fatalf("RestoreFromTrash: %v", err)
	}
	if _, err := v.Stat(scope, restored); err != nil {
		t.Fatalf("the restored file should exist at %s: %v", restored, err)
	}

	// The mount root itself may never be thrown away.
	if _, err := v.MoveToTrash(ctx, scope, inside); err == nil {
		t.Fatal("moving a mount root to the trash must be refused")
	}
}

func TestSearchFindsByNameAndStaysInScope(t *testing.T) {
	v, scope, base := newTestVFS(t)
	ctx := context.Background()

	result, err := v.Search(ctx, scope, SearchOptions{Query: "deep", MaxResults: 50})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(result.Entries) == 0 {
		t.Fatal("the search should have found deep.txt")
	}
	inside := Clean(filepath.Join(base, "inside"))
	for _, e := range result.Entries {
		if !Contains(inside, e.Path) {
			t.Fatalf("search returned a result outside the mount: %s", e.Path)
		}
	}

	// A file that exists only outside the mount must never surface.
	outsideHit, err := v.Search(ctx, scope, SearchOptions{Query: "secret", MaxResults: 50})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(outsideHit.Entries) != 0 {
		t.Fatalf("search reached outside the mount: %+v", outsideHit.Entries)
	}
}

func TestParseMode(t *testing.T) {
	good := map[string]uint32{"0755": 0o755, "755": 0o755, "0644": 0o644, "600": 0o600}
	for in, want := range good {
		got, err := ParseMode(in)
		if err != nil {
			t.Fatalf("ParseMode(%q): %v", in, err)
		}
		if uint32(got) != want {
			t.Errorf("ParseMode(%q) = %o, want %o", in, got, want)
		}
	}
	for _, in := range []string{"", "abc", "99999", "0888", "-1"} {
		if _, err := ParseMode(in); err == nil {
			t.Errorf("ParseMode(%q) accepted an invalid mode", in)
		}
	}
}

func TestKindClassification(t *testing.T) {
	cases := map[string]Kind{
		"photo.JPG":        KindImage,
		"clip.mkv":         KindVideo,
		"song.flac":        KindAudio,
		"manual.pdf":       KindPDF,
		"backup.tar.gz":    KindArchive,
		"main.go":          KindCode,
		"notes.md":         KindText,
		"Dockerfile":       KindText,
		"report.docx":      KindDocument,
		"ubuntu.iso":       KindDisk,
		"mystery.unknown":  KindOther,
		"deploy.sh":        KindCode,
		"nginx.conf":       KindText,
		".env":             KindText,
		"typescript.ts":    KindCode,
		"archive.zip":      KindArchive,
		"font.woff2":       KindFont,
		"spreadsheet.xlsx": KindDocument,
	}
	for name, want := range cases {
		if got := KindFor(name, false); got != want {
			t.Errorf("KindFor(%q) = %q, want %q", name, got, want)
		}
	}
	if got := KindFor("anything", true); got != KindFolder {
		t.Errorf("a directory must classify as a folder, got %q", got)
	}
}

func TestNaturalOrdering(t *testing.T) {
	entries := []Entry{
		{Name: "file10.txt"},
		{Name: "file9.txt"},
		{Name: "file1.txt"},
		{Name: "Folder", IsDir: true},
	}
	SortEntries(entries, "name", "asc", true)
	want := []string{"Folder", "file1.txt", "file9.txt", "file10.txt"}
	for i, name := range want {
		if entries[i].Name != name {
			t.Fatalf("position %d = %q, want %q (full order: %v)", i, entries[i].Name, name, names(entries))
		}
	}
}

func names(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Name
	}
	return out
}
