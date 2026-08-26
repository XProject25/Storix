package vfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rnFixture builds a guarded layer over a temporary mount holding the given
// files, each one written with its own name as content so a test can tell the
// files apart after they have been renamed.
func rnFixture(t *testing.T, names ...string) (*VFS, Scope, string) {
	t.Helper()
	base := t.TempDir()
	mount := filepath.Join(base, "files")
	if err := os.MkdirAll(mount, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		write(t, filepath.Join(mount, name), "content of "+name)
	}
	v := New(Options{TrashDir: filepath.Join(base, "trash"), MaxTextBytes: 1 << 20})
	t.Cleanup(func() { _ = v.Close() })
	return v, Scope{Mounts: []Mount{{Path: Clean(mount), Label: "Files"}}}, Clean(mount)
}

// rnPaths turns names into the virtual paths a request would carry.
func rnPaths(mount string, names ...string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, mount+"/"+name)
	}
	return out
}

// rnNames lists the mount, so a test can assert what is really on disk.
func rnNames(t *testing.T, mount string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.FromSlash(mount))
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestPreviewRenameCoversEveryMode(t *testing.T) {
	cases := []struct {
		name      string
		files     []string
		rule      RenameRule
		want      []string
		unchanged int
	}{
		{
			name:  "replace a plain substring",
			files: []string{"draft-one.txt", "draft-two.txt"},
			rule:  RenameRule{Mode: RenameReplace, Find: "draft", Replace: "final", CaseSensitive: true},
			want:  []string{"final-one.txt", "final-two.txt"},
		},
		{
			name:  "replace folds case by default",
			files: []string{"Draft.txt"},
			rule:  RenameRule{Mode: RenameReplace, Find: "draft", Replace: "final"},
			want:  []string{"final.txt"},
		},
		{
			name:  "replace respects case when asked",
			files: []string{"Draft.txt"},
			rule:  RenameRule{Mode: RenameReplace, Find: "draft", Replace: "final", CaseSensitive: true},
			want:  []string{"Draft.txt"},

			unchanged: 1,
		},
		{
			name:  "replace with a regular expression",
			files: []string{"img_0012.png"},
			rule:  RenameRule{Mode: RenameReplace, Find: `img_(\d+)`, Replace: "photo-$1", Regex: true},
			want:  []string{"photo-0012.png"},
		},
		{
			name:  "replace leaves the extension alone",
			files: []string{"report.report"},
			rule:  RenameRule{Mode: RenameReplace, Find: "report", Replace: "summary", KeepExtension: true},
			want:  []string{"summary.report"},
		},
		{
			name:  "prefix goes in front of the whole name",
			files: []string{"photo.jpg", ".env"},
			rule:  RenameRule{Mode: RenamePrefix, Text: "2026-"},
			want:  []string{"2026-photo.jpg", "2026-.env"},
		},
		{
			name:  "suffix lands before the extension",
			files: []string{"photo.jpg", "archive.tar.gz", ".env"},
			rule:  RenameRule{Mode: RenameSuffix, Text: "-edited", KeepExtension: true},
			want:  []string{"photo-edited.jpg", "archive.tar-edited.gz", ".env-edited"},
		},
		{
			name:  "suffix goes at the very end without keeping the extension",
			files: []string{"photo.jpg"},
			rule:  RenameRule{Mode: RenameSuffix, Text: ".bak"},
			want:  []string{"photo.jpg.bak"},
		},
		{
			name:  "number counts in the order given",
			files: []string{"a.jpg", "b.jpg", "c.jpg"},
			rule:  RenameRule{Mode: RenameNumber, Pattern: "holiday-{n}", Padding: 3, KeepExtension: true},
			want:  []string{"holiday-001.jpg", "holiday-002.jpg", "holiday-003.jpg"},
		},
		{
			name:  "number keeps the original name and starts where asked",
			files: []string{"one.txt", "two.txt"},
			rule:  RenameRule{Mode: RenameNumber, Pattern: "{name} ({n})", Start: 5, KeepExtension: true},
			want:  []string{"one (5).txt", "two (6).txt"},
		},
		{
			name:  "case lower",
			files: []string{"MiXeD.TXT"},
			rule:  RenameRule{Mode: RenameCase, Casing: "lower"},
			want:  []string{"mixed.txt"},
		},
		{
			name:  "case upper lowers the extension",
			files: []string{"MiXeD.TXT"},
			rule:  RenameRule{Mode: RenameCase, Casing: "upper"},
			want:  []string{"MIXED.txt"},
		},
		{
			name:  "case title",
			files: []string{"my holiday-photo.JPG", "don't stop.txt"},
			rule:  RenameRule{Mode: RenameCase, Casing: "title"},
			want:  []string{"My Holiday-Photo.jpg", "Don't Stop.txt"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, scope, mount := rnFixture(t, c.files...)
			preview, err := v.PreviewRename(scope, rnPaths(mount, c.files...), c.rule)
			if err != nil {
				t.Fatalf("preview failed: %v", err)
			}
			if len(preview.Changes) != len(c.want) {
				t.Fatalf("got %d rows, want %d", len(preview.Changes), len(c.want))
			}
			for i, change := range preview.Changes {
				if change.To != c.want[i] {
					t.Errorf("%q became %q, want %q", change.From, change.To, c.want[i])
				}
				if change.Conflict {
					t.Errorf("%q was refused: %s", change.From, change.Reason)
				}
			}
			if preview.Unchanged != c.unchanged {
				t.Errorf("unchanged = %d, want %d", preview.Unchanged, c.unchanged)
			}
			if want := len(c.want) - c.unchanged; preview.Valid != want {
				t.Errorf("valid = %d, want %d", preview.Valid, want)
			}
			if preview.Conflicts != 0 {
				t.Errorf("conflicts = %d, want 0", preview.Conflicts)
			}
		})
	}
}

func TestPreviewRenameRefusesRulesItCannotApply(t *testing.T) {
	cases := []struct {
		name string
		rule RenameRule
	}{
		{"no mode", RenameRule{}},
		{"unknown mode", RenameRule{Mode: RenameMode("shuffle")}},
		{"broken expression", RenameRule{Mode: RenameReplace, Find: "(unclosed", Regex: true}},
		{"nothing to look for", RenameRule{Mode: RenameReplace}},
		{"nothing to add", RenameRule{Mode: RenamePrefix}},
		{"pattern without a counter", RenameRule{Mode: RenameNumber, Pattern: "holiday"}},
		{"casing not offered", RenameRule{Mode: RenameCase, Casing: "sentence"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, scope, mount := rnFixture(t, "one.txt")
			// A rule the server cannot apply is an error, never a panic.
			_, err := v.PreviewRename(scope, rnPaths(mount, "one.txt"), c.rule)
			if !errors.Is(err, ErrInvalidRule) {
				t.Fatalf("got %v, want ErrInvalidRule", err)
			}
		})
	}
}

func TestPreviewRenameReportsNamesItCannotUse(t *testing.T) {
	v, scope, mount := rnFixture(t, "one-two.txt")
	preview, err := v.PreviewRename(scope, rnPaths(mount, "one-two.txt"),
		RenameRule{Mode: RenameReplace, Find: "-", Replace: "/"})
	if err != nil {
		t.Fatalf("preview failed: %v", err)
	}
	if preview.Conflicts != 1 || len(preview.Changes) != 1 {
		t.Fatalf("conflicts = %d over %d rows, want 1 over 1", preview.Conflicts, len(preview.Changes))
	}
	if !preview.Changes[0].Conflict || preview.Changes[0].Reason == "" {
		t.Fatalf("a name with a separator in it must be refused with a reason, got %+v", preview.Changes[0])
	}
}

func TestApplyRenameSwapsTwoNames(t *testing.T) {
	files := []string{"left-right.txt", "right-left.txt"}
	v, scope, mount := rnFixture(t, files...)
	rule := RenameRule{
		Mode:    RenameReplace,
		Find:    `^([a-z]+)-([a-z]+)\.txt$`,
		Replace: "$2-$1.txt",
		Regex:   true,
	}

	preview, err := v.PreviewRename(scope, rnPaths(mount, files...), rule)
	if err != nil {
		t.Fatalf("preview failed: %v", err)
	}
	if preview.Valid != 2 || preview.Conflicts != 0 {
		t.Fatalf("a swap must preview as two valid rows, got %+v", preview)
	}

	renamed, failures, err := v.ApplyRename(context.Background(), scope, rnPaths(mount, files...), rule)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if renamed != 2 || len(failures) != 0 {
		t.Fatalf("renamed %d with %d failures, want 2 and none: %+v", renamed, len(failures), failures)
	}

	// Both names still exist, and each one now holds what the other held, which
	// is only possible if the batch went through a temporary name.
	for _, name := range rnNames(t, mount) {
		if IsInternal(name) {
			t.Fatalf("a temporary name was left behind: %s", name)
		}
	}
	for name, want := range map[string]string{
		"left-right.txt": "content of right-left.txt",
		"right-left.txt": "content of left-right.txt",
	} {
		data, err := os.ReadFile(filepath.Join(filepath.FromSlash(mount), name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		if string(data) != want {
			t.Errorf("%s holds %q, want %q", name, data, want)
		}
	}
}

func TestApplyRenameShiftsAChainOfNames(t *testing.T) {
	files := []string{"1.txt", "2.txt", "3.txt"}
	v, scope, mount := rnFixture(t, files...)
	rule := RenameRule{Mode: RenameNumber, Pattern: "{n}", Start: 2, KeepExtension: true}

	renamed, failures, err := v.ApplyRename(context.Background(), scope, rnPaths(mount, files...), rule)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if renamed != 3 || len(failures) != 0 {
		t.Fatalf("renamed %d with %d failures, want 3 and none: %+v", renamed, len(failures), failures)
	}
	got := strings.Join(rnNames(t, mount), ",")
	if got != "2.txt,3.txt,4.txt" {
		t.Fatalf("folder holds %s, want 2.txt,3.txt,4.txt", got)
	}
	data, err := os.ReadFile(filepath.Join(filepath.FromSlash(mount), "4.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "content of 3.txt" {
		t.Fatalf("4.txt holds %q, the chain moved a file onto the wrong name", data)
	}
}

func TestApplyRenameReportsACollisionInsteadOfOverwriting(t *testing.T) {
	v, scope, mount := rnFixture(t, "notes.txt", "keep.txt")
	rule := RenameRule{Mode: RenameReplace, Find: "notes", Replace: "keep"}
	paths := rnPaths(mount, "notes.txt")

	preview, err := v.PreviewRename(scope, paths, rule)
	if err != nil {
		t.Fatalf("preview failed: %v", err)
	}
	if preview.Conflicts != 1 || preview.Valid != 0 {
		t.Fatalf("a name already on disk must preview as a conflict, got %+v", preview)
	}

	renamed, failures, err := v.ApplyRename(context.Background(), scope, paths, rule)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if renamed != 0 || len(failures) != 1 {
		t.Fatalf("renamed %d with %d failures, want 0 and 1", renamed, len(failures))
	}
	if failures[0].Reason == "" {
		t.Error("a refused row must carry a reason")
	}
	// Neither file moved, and the one that was already there is untouched.
	if got := strings.Join(rnNames(t, mount), ","); got != "keep.txt,notes.txt" {
		t.Fatalf("folder holds %s, want keep.txt,notes.txt", got)
	}
	data, err := os.ReadFile(filepath.Join(filepath.FromSlash(mount), "keep.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "content of keep.txt" {
		t.Fatalf("keep.txt holds %q, it was written over", data)
	}
}

func TestApplyRenameRefusesTwoItemsTakingOneName(t *testing.T) {
	files := []string{"a.txt", "b.txt"}
	v, scope, mount := rnFixture(t, files...)
	// Both names collapse onto the same new name, so only the first may move.
	rule := RenameRule{Mode: RenameReplace, Find: `^[ab]`, Replace: "same", Regex: true}

	renamed, failures, err := v.ApplyRename(context.Background(), scope, rnPaths(mount, files...), rule)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if renamed != 1 || len(failures) != 1 {
		t.Fatalf("renamed %d with %d failures, want 1 and 1: %+v", renamed, len(failures), failures)
	}
	if got := strings.Join(rnNames(t, mount), ","); got != "b.txt,same.txt" {
		t.Fatalf("folder holds %s, want b.txt,same.txt", got)
	}
}

func TestApplyRenameSkipsNamesThatDoNotChange(t *testing.T) {
	files := []string{"keep.txt", "draft.txt"}
	v, scope, mount := rnFixture(t, files...)
	rule := RenameRule{Mode: RenameReplace, Find: "draft", Replace: "final"}

	preview, err := v.PreviewRename(scope, rnPaths(mount, files...), rule)
	if err != nil {
		t.Fatalf("preview failed: %v", err)
	}
	if preview.Unchanged != 1 || preview.Valid != 1 {
		t.Fatalf("got %+v, want one unchanged row and one valid row", preview)
	}
	renamed, failures, err := v.ApplyRename(context.Background(), scope, rnPaths(mount, files...), rule)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if renamed != 1 || len(failures) != 0 {
		t.Fatalf("renamed %d with %d failures, want 1 and none", renamed, len(failures))
	}
	if got := strings.Join(rnNames(t, mount), ","); got != "final.txt,keep.txt" {
		t.Fatalf("folder holds %s, want final.txt,keep.txt", got)
	}
}

func TestApplyRenameStaysInsideTheMount(t *testing.T) {
	v, scope, mount := rnFixture(t, "one.txt")
	outside := Clean(filepath.Dir(filepath.FromSlash(mount)))

	if _, err := v.PreviewRename(scope, []string{outside + "/secret.txt"}, RenameRule{Mode: RenamePrefix, Text: "x-"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a path outside the mount must be refused, got %v", err)
	}
	if _, err := v.PreviewRename(scope, []string{mount}, RenameRule{Mode: RenamePrefix, Text: "x-"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the mount itself must not be renamed, got %v", err)
	}
	if _, err := v.PreviewRename(scope, rnPaths(mount, "one.txt"), RenameRule{Mode: RenamePrefix, Text: "../"}); err != nil {
		t.Fatalf("preview failed: %v", err)
	}
}

func TestApplyRenameRefusesReservedNames(t *testing.T) {
	v, scope, mount := rnFixture(t, "upload.txt")
	preview, err := v.PreviewRename(scope, rnPaths(mount, "upload.txt"),
		RenameRule{Mode: RenamePrefix, Text: InternalPrefix})
	if err != nil {
		t.Fatalf("preview failed: %v", err)
	}
	if preview.Conflicts != 1 {
		t.Fatalf("a name Storix reserves for itself must be refused, got %+v", preview.Changes)
	}
}

// Numbering names the base, and the extension has to survive it. A folder of
// photos renamed to holiday-001 with no .JPG would stop opening.
func TestNumberingKeepsTheExtension(t *testing.T) {
	v, scope, base := newTestVFS(t)
	inside := Clean(filepath.Join(base, "inside"))
	for _, name := range []string{"IMG_001.JPG", "IMG_002.JPG", "notes"} {
		write(t, filepath.Join(base, "inside", name), "x")
	}

	paths := []string{inside + "/IMG_001.JPG", inside + "/IMG_002.JPG", inside + "/notes"}
	preview, err := v.PreviewRename(scope, paths, RenameRule{
		Mode: RenameNumber, Pattern: "holiday-{n}", Start: 1, Padding: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"IMG_001.JPG": "holiday-001.JPG",
		"IMG_002.JPG": "holiday-002.JPG",
		"notes":       "holiday-003",
	}
	for _, change := range preview.Changes {
		if got := want[change.From]; got != change.To {
			t.Errorf("%s became %q, want %q", change.From, change.To, got)
		}
	}

	// A pattern that supplies its own extension is left alone.
	explicit, err := v.PreviewRename(scope, paths[:1], RenameRule{
		Mode: RenameNumber, Pattern: "shot-{n}.png", Start: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Changes[0].To != "shot-1.png" {
		t.Errorf("an explicit extension in the pattern must win, got %q", explicit.Changes[0].To)
	}
}
