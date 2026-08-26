package archive

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// mkdirs creates the given directories below base.
func mkdirs(t *testing.T, base string, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		full := filepath.Join(base, filepath.FromSlash(dir))
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", full, err)
		}
	}
}

// writeTree drops a set of slash separated files below base.
func writeTree(t *testing.T, base string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		full := filepath.Join(base, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", full, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
}

// openRoot opens a root and closes it when the test ends.
func openRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("open root %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

// mustNotExist fails when a path exists. A stat error is enough: some of the
// names under test are not even representable on every platform.
func mustNotExist(t *testing.T, base, rel string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(base, filepath.FromSlash(rel))); err == nil {
		t.Errorf("%s exists but should never have been written", rel)
	}
}

// readFile reads a file back with slash separated components.
func readFile(t *testing.T, base, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

func TestDetectFormat(t *testing.T) {
	cases := []struct {
		name string
		want Format
	}{
		{"photos.zip", FormatZip},
		{"PHOTOS.ZIP", FormatZip},
		{"backup.tar", FormatTar},
		{"backup.tar.gz", FormatTarGz},
		{"backup.TGZ", FormatTarGz},
		{"backup.tar.bz2", FormatTarBz2},
		{"backup.tbz", FormatTarBz2},
		{"backup.tbz2", FormatTarBz2},
		{"/srv/data/site backup.tar.gz", FormatTarGz},
		{"notes.txt", ""},
		{"payload.gz", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := DetectFormat(c.name); got != c.want {
			t.Errorf("DetectFormat(%q) = %q, want %q", c.name, got, c.want)
		}
	}

	extensions := map[Format]string{
		FormatZip:      ".zip",
		FormatTar:      ".tar",
		FormatTarGz:    ".tar.gz",
		FormatTarBz2:   ".tar.bz2",
		Format("nope"): "",
	}
	for format, want := range extensions {
		if got := format.Extension(); got != want {
			t.Errorf("%q.Extension() = %q, want %q", format, got, want)
		}
	}

	creatable := map[Format]bool{
		FormatZip:    true,
		FormatTar:    true,
		FormatTarGz:  true,
		FormatTarBz2: false,
		Format(""):   false,
	}
	for format, want := range creatable {
		if got := format.CanCreate(); got != want {
			t.Errorf("%q.CanCreate() = %v, want %v", format, got, want)
		}
	}
}

func TestCreateRefusesReadOnlyFormat(t *testing.T) {
	base := t.TempDir()
	writeTree(t, base, map[string]string{"a.txt": "alpha"})
	root := openRoot(t, base)

	err := Create(context.Background(), root, []string{"a.txt"}, io.Discard, FormatTarBz2, nil)
	if !errors.Is(err, ErrReadOnlyFormat) {
		t.Fatalf("Create with bzip2 = %v, want ErrReadOnlyFormat", err)
	}
	if err := Create(context.Background(), root, nil, io.Discard, FormatZip, nil); !errors.Is(err, ErrNoSources) {
		t.Fatalf("Create with no sources = %v, want ErrNoSources", err)
	}
}

func TestRoundTrip(t *testing.T) {
	for _, format := range []Format{FormatZip, FormatTarGz} {
		t.Run(string(format), func(t *testing.T) {
			base := t.TempDir()
			srcDir := filepath.Join(base, "src")
			arcDir := filepath.Join(base, "arc")
			dstDir := filepath.Join(base, "dst")
			mkdirs(t, base, "src", "arc", "dst")
			writeTree(t, srcDir, map[string]string{
				"tree/a.txt":          "alpha",
				"tree/sub/b.txt":      "beta beta",
				"tree/sub/deep/c.bin": strings.Repeat("x", 4096),
			})
			mkdirs(t, srcDir, "tree/empty")
			const wantBytes = int64(5 + 9 + 4096)

			srcRoot := openRoot(t, srcDir)
			arcRoot := openRoot(t, arcDir)
			dstRoot := openRoot(t, dstDir)

			archiveName := "bundle" + format.Extension()
			out, err := os.Create(filepath.Join(arcDir, archiveName))
			if err != nil {
				t.Fatalf("create archive: %v", err)
			}
			var seenBytes, seenItems int64
			progress := func(b, i int64, current string) {
				seenBytes, seenItems = b, i
			}
			if err := Create(context.Background(), srcRoot, []string{"tree"}, out, format, progress); err != nil {
				out.Close()
				t.Fatalf("Create: %v", err)
			}
			if err := out.Close(); err != nil {
				t.Fatalf("close archive: %v", err)
			}
			if seenBytes != wantBytes {
				t.Errorf("progress bytes = %d, want %d", seenBytes, wantBytes)
			}
			if seenItems != 7 {
				t.Errorf("progress items = %d, want 7", seenItems)
			}

			items, gotFormat, err := Inspect(context.Background(), arcRoot, archiveName, 0)
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			if gotFormat != format {
				t.Errorf("Inspect format = %q, want %q", gotFormat, format)
			}
			listed := make(map[string]Item, len(items))
			for _, item := range items {
				listed[item.Name] = item
			}
			for _, want := range []string{"tree", "tree/a.txt", "tree/empty", "tree/sub/deep/c.bin"} {
				if _, ok := listed[want]; !ok {
					t.Fatalf("Inspect is missing %q, got %v", want, listed)
				}
			}
			if item := listed["tree/a.txt"]; item.Size != 5 || item.IsDir {
				t.Errorf("tree/a.txt = %+v, want size 5 and a file", item)
			}
			if item := listed["tree/empty"]; !item.IsDir {
				t.Errorf("tree/empty = %+v, want a directory", item)
			}

			report, err := Extract(context.Background(), arcRoot, archiveName, dstRoot, "out", nil)
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if report.Files != 3 {
				t.Errorf("report.Files = %d, want 3", report.Files)
			}
			if report.Dirs != 4 {
				t.Errorf("report.Dirs = %d, want 4", report.Dirs)
			}
			if report.Bytes != wantBytes {
				t.Errorf("report.Bytes = %d, want %d", report.Bytes, wantBytes)
			}
			if len(report.Skipped) != 0 {
				t.Errorf("report.Skipped = %v, want none", report.Skipped)
			}

			if got := readFile(t, dstDir, "out/tree/a.txt"); got != "alpha" {
				t.Errorf("a.txt = %q, want %q", got, "alpha")
			}
			if got := readFile(t, dstDir, "out/tree/sub/b.txt"); got != "beta beta" {
				t.Errorf("b.txt = %q, want %q", got, "beta beta")
			}
			if got := readFile(t, dstDir, "out/tree/sub/deep/c.bin"); len(got) != 4096 {
				t.Errorf("c.bin length = %d, want 4096", len(got))
			}
			info, err := os.Stat(filepath.Join(dstDir, "out", "tree", "empty"))
			if err != nil || !info.IsDir() {
				t.Errorf("empty directory was not restored: %v", err)
			}
		})
	}
}

func TestInspectLimit(t *testing.T) {
	base := t.TempDir()
	mkdirs(t, base, "src", "arc")
	srcDir := filepath.Join(base, "src")
	arcDir := filepath.Join(base, "arc")
	writeTree(t, srcDir, map[string]string{
		"one.txt": "1", "two.txt": "2", "three.txt": "3", "four.txt": "4",
	})
	srcRoot := openRoot(t, srcDir)
	arcRoot := openRoot(t, arcDir)

	out, err := os.Create(filepath.Join(arcDir, "all.zip"))
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	err = Create(context.Background(), srcRoot, []string{"one.txt", "two.txt", "three.txt", "four.txt"}, out, FormatZip, nil)
	if err != nil {
		out.Close()
		t.Fatalf("Create: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	items, _, err := Inspect(context.Background(), arcRoot, "all.zip", 2)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("Inspect returned %d items, want 2", len(items))
	}
	if _, _, err := Inspect(context.Background(), arcRoot, "all.rar", 0); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("Inspect of an unknown format = %v, want ErrUnsupportedFormat", err)
	}
}

// zipMember is one crafted archive entry.
type zipMember struct {
	name    string
	content string
}

// buildHostileZip writes a zip whose member names are exactly the ones given.
// The names are patched in after the writer has finished, with placeholders of
// the same length so every offset stays valid. That keeps the test independent
// of whatever the zip writer is willing to accept.
func buildHostileZip(t *testing.T, members []zipMember) []byte {
	t.Helper()
	if len(members) > 9 {
		t.Fatalf("buildHostileZip supports at most 9 members, got %d", len(members))
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	placeholders := make([]string, len(members))
	for i, m := range members {
		if len(m.name) < 2 {
			t.Fatalf("member name %q is too short to patch", m.name)
		}
		placeholders[i] = strings.Repeat("q", len(m.name)-1) + strconv.Itoa(i)
		w, err := zw.CreateHeader(&zip.FileHeader{Name: placeholders[i], Method: zip.Store})
		if err != nil {
			t.Fatalf("create member %q: %v", m.name, err)
		}
		if _, err := io.WriteString(w, m.content); err != nil {
			t.Fatalf("write member %q: %v", m.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	raw := buf.Bytes()
	for i, m := range members {
		if !bytes.Contains(raw, []byte(placeholders[i])) {
			t.Fatalf("placeholder for %q not found in the archive", m.name)
		}
		raw = bytes.ReplaceAll(raw, []byte(placeholders[i]), []byte(m.name))
	}
	return raw
}

// stageZip writes raw archive bytes into an archive directory and returns the
// roots for the archive and for a fresh destination.
func stageZip(t *testing.T, raw []byte, name string) (base string, arcRoot, dstRoot *os.Root) {
	t.Helper()
	base = t.TempDir()
	mkdirs(t, base, "arc", "dst")
	if err := os.WriteFile(filepath.Join(base, "arc", name), raw, 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	return base, openRoot(t, filepath.Join(base, "arc")), openRoot(t, filepath.Join(base, "dst"))
}

func TestExtractSkipsTraversalNames(t *testing.T) {
	raw := buildHostileZip(t, []zipMember{
		{name: "../escape.txt", content: "owned"},
		{name: "good.txt", content: "fine"},
		{name: "sub/../../evil.txt", content: "owned"},
	})
	base, arcRoot, dstRoot := stageZip(t, raw, "evil.zip")

	report, err := Extract(context.Background(), arcRoot, "evil.zip", dstRoot, ".", nil)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if report.Files != 1 {
		t.Fatalf("report.Files = %d, want 1", report.Files)
	}
	if len(report.Skipped) != 2 {
		t.Fatalf("report.Skipped = %v, want two entries", report.Skipped)
	}
	joined := strings.Join(report.Skipped, " ")
	for _, want := range []string{"escape.txt", "evil.txt"} {
		if !strings.Contains(joined, want) {
			t.Errorf("report.Skipped = %v, want it to mention %q", report.Skipped, want)
		}
	}
	if got := readFile(t, base, "dst/good.txt"); got != "fine" {
		t.Errorf("good.txt = %q, want %q", got, "fine")
	}
	for _, escaped := range []string{"escape.txt", "evil.txt", "arc/escape.txt", "dst/sub/escape.txt"} {
		mustNotExist(t, base, escaped)
	}
}

func TestExtractSkipsAbsoluteNames(t *testing.T) {
	raw := buildHostileZip(t, []zipMember{
		{name: "/etc/storix-owned.conf", content: "owned"},
		{name: "C:/owned.txt", content: "owned"},
		{name: "keep.txt", content: "fine"},
	})
	base, arcRoot, dstRoot := stageZip(t, raw, "absolute.zip")

	report, err := Extract(context.Background(), arcRoot, "absolute.zip", dstRoot, ".", nil)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if report.Files != 1 {
		t.Fatalf("report.Files = %d, want 1", report.Files)
	}
	if len(report.Skipped) != 2 {
		t.Fatalf("report.Skipped = %v, want two entries", report.Skipped)
	}
	if got := readFile(t, base, "dst/keep.txt"); got != "fine" {
		t.Errorf("keep.txt = %q, want %q", got, "fine")
	}
	// The absolute names must not be quietly turned into relative ones either.
	for _, unwanted := range []string{"dst/etc/storix-owned.conf", "dst/owned.txt", "dst/C:/owned.txt"} {
		mustNotExist(t, base, unwanted)
	}
}

func TestExtractSkipsEscapingSymlink(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	header := &zip.FileHeader{Name: "link", Method: zip.Store}
	header.SetMode(0o777 | fs.ModeSymlink)
	w, err := zw.CreateHeader(header)
	if err != nil {
		t.Fatalf("create link member: %v", err)
	}
	if _, err := io.WriteString(w, "../../etc/passwd"); err != nil {
		t.Fatalf("write link target: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	base, arcRoot, dstRoot := stageZip(t, buf.Bytes(), "link.zip")

	report, err := Extract(context.Background(), arcRoot, "link.zip", dstRoot, ".", nil)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(report.Skipped) != 1 || !strings.Contains(report.Skipped[0], "link") {
		t.Fatalf("report.Skipped = %v, want the link to be refused", report.Skipped)
	}
	mustNotExist(t, base, "dst/link")
}

func TestExtractLimits(t *testing.T) {
	base := t.TempDir()
	mkdirs(t, base, "src", "arc", "dst")
	srcDir := filepath.Join(base, "src")
	arcDir := filepath.Join(base, "arc")
	// Highly compressible content stands in for the payload of a bomb: the
	// archive stays tiny while the declared size does not.
	writeTree(t, srcDir, map[string]string{"blob.txt": strings.Repeat("A", 512<<10)})

	srcRoot := openRoot(t, srcDir)
	arcRoot := openRoot(t, arcDir)
	out, err := os.Create(filepath.Join(arcDir, "blob.zip"))
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	if err := Create(context.Background(), srcRoot, []string{"blob.txt"}, out, FormatZip, nil); err != nil {
		out.Close()
		t.Fatalf("Create: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	t.Run("entry ratio", func(t *testing.T) {
		dstRoot := openRoot(t, filepath.Join(base, "dst"))
		limits := Limits{MaxRatio: 2, MinEntryBytes: -1, MinTotalBytes: -1, MaxTotalRatio: -1}
		report, err := ExtractWithLimits(context.Background(), arcRoot, "blob.zip", dstRoot, "ratio", limits, nil)
		if err != nil {
			t.Fatalf("ExtractWithLimits: %v", err)
		}
		if report.Files != 0 || len(report.Skipped) != 1 {
			t.Fatalf("report = %+v, want the entry refused", report)
		}
	})

	t.Run("total budget", func(t *testing.T) {
		dstRoot := openRoot(t, filepath.Join(base, "dst"))
		limits := Limits{MaxRatio: -1, MinEntryBytes: -1, MinTotalBytes: -1, MaxTotalRatio: 2}
		_, err := ExtractWithLimits(context.Background(), arcRoot, "blob.zip", dstRoot, "budget", limits, nil)
		if !errors.Is(err, ErrBomb) {
			t.Fatalf("ExtractWithLimits = %v, want ErrBomb", err)
		}
	})

	t.Run("defaults allow it", func(t *testing.T) {
		dstRoot := openRoot(t, filepath.Join(base, "dst"))
		report, err := Extract(context.Background(), arcRoot, "blob.zip", dstRoot, "plain", nil)
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		if report.Files != 1 || report.Bytes != 512<<10 {
			t.Fatalf("report = %+v, want one file of %d bytes", report, 512<<10)
		}
	})
}

func TestStreamZipStoresCompressedExtensions(t *testing.T) {
	base := t.TempDir()
	writeTree(t, base, map[string]string{
		"clip.mp4":  strings.Repeat("m", 2048),
		"notes.txt": strings.Repeat("t", 2048),
	})
	root := openRoot(t, base)

	var buf bytes.Buffer
	if err := StreamZip(context.Background(), root, []string{"clip.mp4", "notes.txt"}, &buf, nil); err != nil {
		t.Fatalf("StreamZip: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	methods := make(map[string]uint16, len(zr.File))
	for _, member := range zr.File {
		methods[member.Name] = member.Method
	}
	if methods["clip.mp4"] != zip.Store {
		t.Errorf("clip.mp4 method = %d, want store", methods["clip.mp4"])
	}
	if methods["notes.txt"] != zip.Deflate {
		t.Errorf("notes.txt method = %d, want deflate", methods["notes.txt"])
	}
}

func TestCreateRecordsSymlinkWithoutFollowing(t *testing.T) {
	base := t.TempDir()
	writeTree(t, base, map[string]string{"target.txt": "payload"})
	if err := os.Symlink(filepath.Join(base, "target.txt"), filepath.Join(base, "link.txt")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	root := openRoot(t, base)

	var buf bytes.Buffer
	if err := Create(context.Background(), root, []string{"link.txt"}, &buf, FormatZip, nil); err != nil {
		t.Fatalf("Create: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	if len(zr.File) != 1 {
		t.Fatalf("archive holds %d members, want 1", len(zr.File))
	}
	member := zr.File[0]
	if member.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("member mode = %v, want a symlink", member.Mode())
	}
	rc, err := member.Open()
	if err != nil {
		t.Fatalf("open member: %v", err)
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read member: %v", err)
	}
	if string(body) == "payload" {
		t.Fatal("the link was followed instead of recorded")
	}
}

func TestEntryNameRejects(t *testing.T) {
	bad := []string{"", "/abs", "../up", "a/../../b", "C:/win", "..", "./..", "a\\..\\b"}
	for _, name := range bad {
		if got, ok := entryName(name); ok {
			t.Errorf("entryName(%q) = %q, true, want it refused", name, got)
		}
	}
	good := map[string]string{
		"a.txt":       "a.txt",
		"./a.txt":     "a.txt",
		"dir/b.txt":   "dir/b.txt",
		"dir/":        "dir",
		"dir\\b.txt":  "dir/b.txt",
		"dir//b.txt":  "dir/b.txt",
		"dir/./b.txt": "dir/b.txt",
	}
	for name, want := range good {
		got, ok := entryName(name)
		if !ok || got != want {
			t.Errorf("entryName(%q) = %q, %v, want %q, true", name, got, ok, want)
		}
	}
}
