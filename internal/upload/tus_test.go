package upload

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/XProject25/Storix/internal/store"
	"github.com/XProject25/Storix/internal/vfs"
)

// tuHarness is a tus endpoint backed by a real database and a real guarded
// file system over temporary directories, routed the way the API router routes
// it in production.
type tuHarness struct {
	t      *testing.T
	server *httptest.Server
	client *http.Client
	db     *store.Store

	// baseOS is the temporary directory holding everything below.
	baseOS string
	// filesOS is the mount directory on disk, mountVirt the same directory as
	// the API and the UI see it.
	filesOS   string
	mountVirt string
	// outsideVirt is a real directory the actor holds no mount for.
	outsideVirt string
}

func tuNewHarness(t *testing.T) *tuHarness {
	t.Helper()
	base := t.TempDir()
	files := filepath.Join(base, "files")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{files, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	db, err := store.Open(filepath.Join(base, "storix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	guard := vfs.New(vfs.Options{MaxTextBytes: 1 << 20})
	t.Cleanup(func() { _ = guard.Close() })

	manager := New(Deps{
		Store:   db,
		VFS:     guard,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		MaxSize: 64 << 20,
		Expiry:  time.Hour,
	})

	actor := Actor{
		UserID:   7,
		Username: "tester",
		Scope:    vfs.Scope{Mounts: []vfs.Mount{{Path: vfs.Clean(files), Label: "Files"}}},
		Base:     "/tus",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("OPTIONS /tus", manager.Options)
	mux.HandleFunc("POST /tus", func(w http.ResponseWriter, r *http.Request) { manager.Create(w, r, actor) })
	mux.HandleFunc("HEAD /tus/{id}", func(w http.ResponseWriter, r *http.Request) { manager.Head(w, r, actor) })
	mux.HandleFunc("PATCH /tus/{id}", func(w http.ResponseWriter, r *http.Request) { manager.Patch(w, r, actor) })
	mux.HandleFunc("DELETE /tus/{id}", func(w http.ResponseWriter, r *http.Request) { manager.Delete(w, r, actor) })

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return &tuHarness{
		t:           t,
		server:      server,
		client:      &http.Client{Timeout: 30 * time.Second},
		db:          db,
		baseOS:      base,
		filesOS:     files,
		mountVirt:   vfs.Clean(files),
		outsideVirt: vfs.Clean(outside),
	}
}

// tuMetadata renders a tus Upload-Metadata header. The pairs are sorted so a
// failing test always prints the same header.
func tuMetadata(pairs map[string]string) string {
	parts := make([]string, 0, len(pairs))
	for key, value := range pairs {
		parts = append(parts, key+" "+base64.StdEncoding.EncodeToString([]byte(value)))
	}
	slices.Sort(parts)
	return strings.Join(parts, ",")
}

// send issues a request and fails the test when the transport itself failed.
func (h *tuHarness) send(req *http.Request) *http.Response {
	h.t.Helper()
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	return resp
}

// create issues a tus POST. An empty length omits the Upload-Length header, a
// nil metadata map omits Upload-Metadata. The raw response is returned so a
// test can assert on a refusal as well as on a success.
func (h *tuHarness) create(length string, meta map[string]string) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.server.URL+"/tus", nil)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set(hdrResumable, Version)
	if length != "" {
		req.Header.Set(hdrLength, length)
	}
	if meta != nil {
		req.Header.Set(hdrMetadata, tuMetadata(meta))
	}
	return h.send(req)
}

// begin starts an upload and returns the URL from the Location header.
func (h *tuHarness) begin(length int, meta map[string]string) string {
	h.t.Helper()
	resp := h.create(strconv.Itoa(length), meta)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		h.t.Fatalf("tus create returned %d, want 201", resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	if location == "" {
		h.t.Fatal("tus create must return a Location header pointing at the new upload")
	}
	if resp.Header.Get(hdrExpires) == "" {
		h.t.Fatal("tus create must advertise when the unfinished upload expires")
	}
	return location
}

// patch sends one chunk at the given offset.
func (h *tuHarness) patch(location string, offset int64, chunk []byte) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest(http.MethodPatch, h.server.URL+location, bytes.NewReader(chunk))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set(hdrResumable, Version)
	req.Header.Set(hdrOffset, strconv.FormatInt(offset, 10))
	req.Header.Set("Content-Type", contentTypeOffset)
	return h.send(req)
}

// mustPatch sends one chunk and returns the offset the server reached.
func (h *tuHarness) mustPatch(location string, offset int64, chunk []byte) int64 {
	h.t.Helper()
	resp := h.patch(location, offset, chunk)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		h.t.Fatalf("tus patch at offset %d returned %d, want 204", offset, resp.StatusCode)
	}
	return h.offsetOf(resp)
}

// head asks the server where the transfer stands.
func (h *tuHarness) head(location string) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest(http.MethodHead, h.server.URL+location, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set(hdrResumable, Version)
	return h.send(req)
}

// remove aborts an upload.
func (h *tuHarness) remove(location string) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest(http.MethodDelete, h.server.URL+location, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set(hdrResumable, Version)
	return h.send(req)
}

// offsetOf reads the Upload-Offset header of a response.
func (h *tuHarness) offsetOf(resp *http.Response) int64 {
	h.t.Helper()
	offset, err := strconv.ParseInt(resp.Header.Get(hdrOffset), 10, 64)
	if err != nil {
		h.t.Fatalf("the response did not report an offset: %v", err)
	}
	return offset
}

// idOf extracts the upload identifier from an upload URL.
func (h *tuHarness) idOf(location string) string {
	h.t.Helper()
	id := strings.TrimPrefix(location, "/tus/")
	if id == "" || id == location {
		h.t.Fatalf("Location %q does not carry an upload identifier", location)
	}
	return id
}

// entries lists the names on disk in a directory below the mount.
func (h *tuHarness) entries(parts ...string) []string {
	h.t.Helper()
	list, err := os.ReadDir(filepath.Join(append([]string{h.filesOS}, parts...)...))
	if err != nil {
		h.t.Fatalf("read directory: %v", err)
	}
	names := make([]string, 0, len(list))
	for _, entry := range list {
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	return names
}

// scratchLeft returns the name of any Storix scratch file still sitting in a
// directory below the mount, or the empty string when the area is clean.
func (h *tuHarness) scratchLeft(parts ...string) string {
	h.t.Helper()
	for _, name := range h.entries(parts...) {
		if vfs.IsInternal(name) {
			return name
		}
	}
	return ""
}

// read returns the bytes of a file below the mount.
func (h *tuHarness) read(parts ...string) []byte {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(append([]string{h.filesOS}, parts...)...))
	if err != nil {
		h.t.Fatalf("read file: %v", err)
	}
	return data
}

func TestTusOptionsAnswersTheDiscoveryRequest(t *testing.T) {
	h := tuNewHarness(t)

	req, err := http.NewRequest(http.MethodOptions, h.server.URL+"/tus", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp := h.send(req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("OPTIONS returned %d, want 204", resp.StatusCode)
	}
	if got := resp.Header.Get(hdrResumable); got != Version {
		t.Fatalf("%s = %q, want %q", hdrResumable, got, Version)
	}
	if got := resp.Header.Get(hdrVersion); got != Version {
		t.Fatalf("%s = %q, want %q", hdrVersion, got, Version)
	}
	ext := resp.Header.Get(hdrExtension)
	for _, want := range []string{"creation", "creation-with-upload", "termination", "expiration"} {
		if !strings.Contains(ext, want) {
			t.Fatalf("%s = %q, want it to advertise %q", hdrExtension, ext, want)
		}
	}
	if got := resp.Header.Get(hdrMaxSize); got != strconv.Itoa(64<<20) {
		t.Fatalf("%s = %q, want the configured ceiling %d", hdrMaxSize, got, 64<<20)
	}
}

func TestTusCreateValidatesTheRequest(t *testing.T) {
	h := tuNewHarness(t)

	t.Run("a transfer without a declared length is refused", func(t *testing.T) {
		resp := h.create("", map[string]string{"dir": h.mountVirt, "filename": "report.bin"})
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("a missing Upload-Length returned %d, want 400", resp.StatusCode)
		}
		if resp.Header.Get("Location") != "" {
			t.Fatal("a refused create must not hand out an upload URL")
		}
	})

	t.Run("a length that is not a number is refused", func(t *testing.T) {
		resp := h.create("soon", map[string]string{"dir": h.mountVirt, "filename": "report.bin"})
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("an unparseable Upload-Length returned %d, want 400", resp.StatusCode)
		}
	})

	t.Run("a transfer over the ceiling is refused", func(t *testing.T) {
		resp := h.create(strconv.Itoa(65<<20), map[string]string{"dir": h.mountVirt, "filename": "huge.bin"})
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("a transfer above the ceiling returned %d, want 413", resp.StatusCode)
		}
	})

	t.Run("a transfer without a filename is refused", func(t *testing.T) {
		resp := h.create("10", map[string]string{"dir": h.mountVirt})
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("a missing filename returned %d, want 400", resp.StatusCode)
		}
	})

	t.Run("a transfer without a destination is refused", func(t *testing.T) {
		resp := h.create("10", map[string]string{"filename": "report.bin"})
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("a missing destination returned %d, want 400", resp.StatusCode)
		}
	})

	t.Run("a destination outside the scope is refused", func(t *testing.T) {
		resp := h.create("10", map[string]string{"dir": h.outsideVirt, "filename": "report.bin"})
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("a destination the actor holds no mount for returned %d, want 403", resp.StatusCode)
		}
		if names, err := os.ReadDir(filepath.Join(h.baseOS, "outside")); err != nil || len(names) != 0 {
			t.Fatalf("the refused destination must stay untouched, holds %d entries and %v", len(names), err)
		}
	})

	t.Run("a destination that is a file is refused", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(h.filesOS, "notes.txt"), []byte("notes"), 0o644); err != nil {
			t.Fatal(err)
		}
		resp := h.create("10", map[string]string{"dir": h.mountVirt + "/notes.txt", "filename": "report.bin"})
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("a destination that is not a folder returned %d, want 400", resp.StatusCode)
		}
	})

	t.Run("nothing was left behind by the refusals", func(t *testing.T) {
		if name := h.scratchLeft(); name != "" {
			t.Fatalf("a refused create left the scratch file %q behind", name)
		}
	})
}

func TestTusChunkedUploadLandsAByteIdenticalFile(t *testing.T) {
	h := tuNewHarness(t)

	payload := bytes.Repeat([]byte("storix "), 3000) // 21000 bytes
	location := h.begin(len(payload), map[string]string{
		"dir":      h.mountVirt,
		"filename": "report.bin",
	})
	id := h.idOf(location)

	// The scratch file exists while the transfer is in flight, and it is
	// hidden from every listing by its internal prefix.
	if name := h.scratchLeft(); name != vfs.PartName(id) {
		t.Fatalf("the in flight upload must write into %q, found %q", vfs.PartName(id), name)
	}

	bounds := []int{0, 7000, 14000, len(payload)}
	for i := 1; i < len(bounds); i++ {
		start, end := bounds[i-1], bounds[i]
		if got := h.mustPatch(location, int64(start), payload[start:end]); got != int64(end) {
			t.Fatalf("offset after chunk %d = %d, want %d", i, got, end)
		}
		if end == len(payload) {
			break
		}
		resp := h.head(location)
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			t.Fatalf("HEAD between chunks returned %d, want 200", resp.StatusCode)
		}
		if got := h.offsetOf(resp); got != int64(end) {
			resp.Body.Close()
			t.Fatalf("HEAD reported offset %d between chunks, want %d so the client resumes there", got, end)
		}
		if got := resp.Header.Get(hdrLength); got != strconv.Itoa(len(payload)) {
			resp.Body.Close()
			t.Fatalf("HEAD reported a length of %q, want %d", got, len(payload))
		}
		resp.Body.Close()
	}

	written := h.read("report.bin")
	if !bytes.Equal(written, payload) {
		t.Fatalf("the landed file is %d bytes and does not match the %d bytes sent", len(written), len(payload))
	}
	if name := h.scratchLeft(); name != "" {
		t.Fatalf("a finished upload must leave no scratch file behind, found %q", name)
	}

	sess, err := h.db.GetUpload(t.Context(), id)
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if !sess.Completed {
		t.Fatal("the record must be marked completed once the file landed")
	}
	if sess.FinalPath != h.mountVirt+"/report.bin" {
		t.Fatalf("FinalPath = %q, want %q", sess.FinalPath, h.mountVirt+"/report.bin")
	}
	if sess.Offset != int64(len(payload)) {
		t.Fatalf("the recorded offset is %d, want %d", sess.Offset, len(payload))
	}
}

func TestTusStaleOffsetIsRefusedAndKeepsTheFileIntact(t *testing.T) {
	h := tuNewHarness(t)

	payload := bytes.Repeat([]byte("abcdefghij"), 300) // 3000 bytes
	location := h.begin(len(payload), map[string]string{
		"dir":      h.mountVirt,
		"filename": "resumed.bin",
	})

	if got := h.mustPatch(location, 0, payload[:1000]); got != 1000 {
		t.Fatalf("offset after the first chunk = %d, want 1000", got)
	}

	t.Run("a chunk that repeats accepted bytes is refused", func(t *testing.T) {
		resp := h.patch(location, 0, []byte("this would corrupt the file"))
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("a stale offset returned %d, want 409", resp.StatusCode)
		}
		if got := h.offsetOf(resp); got != 1000 {
			t.Fatalf("the refusal reported offset %d, want the true offset 1000 so the client can resume", got)
		}
	})

	t.Run("a chunk that skips ahead is refused", func(t *testing.T) {
		resp := h.patch(location, 2000, []byte("this would leave a hole"))
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("an offset past the server state returned %d, want 409", resp.StatusCode)
		}
	})

	t.Run("a chunk without the tus content type is refused", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPatch, h.server.URL+location, bytes.NewReader(payload[1000:]))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(hdrResumable, Version)
		req.Header.Set(hdrOffset, "1000")
		req.Header.Set("Content-Type", "application/octet-stream")
		resp := h.send(req)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Fatalf("the wrong content type returned %d, want 415", resp.StatusCode)
		}
	})

	t.Run("the refusals did not disturb the transfer", func(t *testing.T) {
		resp := h.head(location)
		defer resp.Body.Close()
		if got := h.offsetOf(resp); got != 1000 {
			t.Fatalf("offset after the refusals = %d, want the 1000 bytes that were accepted", got)
		}
		if got := h.mustPatch(location, 1000, payload[1000:]); got != int64(len(payload)) {
			t.Fatalf("the transfer did not resume cleanly, offset = %d, want %d", got, len(payload))
		}
		if written := h.read("resumed.bin"); !bytes.Equal(written, payload) {
			t.Fatalf("the landed file is %d bytes and does not match the %d bytes sent", len(written), len(payload))
		}
	})

	t.Run("an unknown upload reports not found", func(t *testing.T) {
		resp := h.patch("/tus/00000000000000000000000000000000", 0, []byte("x"))
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("a chunk for an unknown upload returned %d, want 404", resp.StatusCode)
		}
	})
}

func TestTusSecondUploadOfTheSameNameLandsBesideTheFirst(t *testing.T) {
	h := tuNewHarness(t)

	first := []byte("the first upload")
	second := []byte("the second upload, which must not replace the first")

	one := h.begin(len(first), map[string]string{"dir": h.mountVirt, "filename": "report.txt"})
	if got := h.mustPatch(one, 0, first); got != int64(len(first)) {
		t.Fatalf("offset = %d, want %d", got, len(first))
	}

	two := h.begin(len(second), map[string]string{"dir": h.mountVirt, "filename": "report.txt"})
	if got := h.mustPatch(two, 0, second); got != int64(len(second)) {
		t.Fatalf("offset = %d, want %d", got, len(second))
	}

	if got := h.read("report.txt"); !bytes.Equal(got, first) {
		t.Fatalf("the first upload was overwritten, it now reads %q", got)
	}
	names := h.entries()
	if len(names) != 2 {
		t.Fatalf("the destination holds %v, want the original and one renamed copy", names)
	}
	beside := names[0]
	if beside == "report.txt" {
		beside = names[1]
	}
	if !strings.HasPrefix(beside, "report") || !strings.HasSuffix(beside, ".txt") {
		t.Fatalf("the second upload landed as %q, want a name derived from report.txt", beside)
	}
	if got := h.read(beside); !bytes.Equal(got, second) {
		t.Fatalf("%q holds %q, want the second payload", beside, got)
	}
}

func TestTusDeleteRemovesTheScratchFileAndTheRecord(t *testing.T) {
	h := tuNewHarness(t)

	location := h.begin(4000, map[string]string{"dir": h.mountVirt, "filename": "abandoned.bin"})
	id := h.idOf(location)
	if got := h.mustPatch(location, 0, bytes.Repeat([]byte("x"), 500)); got != 500 {
		t.Fatalf("offset = %d, want 500", got)
	}
	if name := h.scratchLeft(); name == "" {
		t.Fatal("an unfinished upload must hold a scratch file")
	}

	resp := h.remove(location)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE returned %d, want 204", resp.StatusCode)
	}

	if name := h.scratchLeft(); name != "" {
		t.Fatalf("aborting an upload must remove its partial data, found %q", name)
	}
	if names := h.entries(); len(names) != 0 {
		t.Fatalf("aborting an upload must leave the destination empty, found %v", names)
	}
	if _, err := h.db.GetUpload(t.Context(), id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("aborting an upload must drop its record, GetUpload returned %v", err)
	}

	after := h.remove(location)
	after.Body.Close()
	if after.StatusCode != http.StatusNotFound {
		t.Fatalf("aborting the same upload twice returned %d, want 404", after.StatusCode)
	}
}

func TestTusFolderUploadRecreatesTheSubdirectory(t *testing.T) {
	h := tuNewHarness(t)

	payload := []byte("# Storix\nDeveloped by X Project\n")
	location := h.begin(len(payload), map[string]string{
		"dir":          h.mountVirt,
		"filename":     "readme.md",
		"relativePath": "project/docs/readme.md",
	})
	if got := h.mustPatch(location, 0, payload); got != int64(len(payload)) {
		t.Fatalf("offset = %d, want %d", got, len(payload))
	}

	if got := h.read("project", "docs", "readme.md"); !bytes.Equal(got, payload) {
		t.Fatalf("the file did not land inside the recreated tree, it reads %q", got)
	}
	if names := h.entries(); len(names) != 1 || names[0] != "project" {
		t.Fatalf("the destination holds %v, want only the recreated folder", names)
	}
	if name := h.scratchLeft("project", "docs"); name != "" {
		t.Fatalf("a finished folder upload must leave no scratch file behind, found %q", name)
	}

	sess, err := h.db.GetUpload(t.Context(), h.idOf(location))
	if err != nil {
		t.Fatalf("GetUpload: %v", err)
	}
	if sess.RelPath != "project/docs" {
		t.Fatalf("RelPath = %q, want the folder part of the dropped tree", sess.RelPath)
	}
	if sess.FinalPath != h.mountVirt+"/project/docs/readme.md" {
		t.Fatalf("FinalPath = %q, want the file inside the recreated tree", sess.FinalPath)
	}
}

func TestTusMetadataCannotEscapeTheDestination(t *testing.T) {
	t.Run("a filename keeps only its last element", func(t *testing.T) {
		cases := map[string]string{
			"../../etc/passwd":         "passwd",
			`..\..\windows\hosts`:      "hosts",
			"/etc/shadow":              "shadow",
			"  spaced.txt  ":           "spaced.txt",
			"folder/sub/report.bin":    "report.bin",
			"..":                       "",
			".":                        "",
			"":                         "",
			"/":                        "",
			"C:/Windows/System32/x.db": "x.db",
		}
		for in, want := range cases {
			if got := sanitizeName(in); got != want {
				t.Errorf("sanitizeName(%q) = %q, want %q", in, got, want)
			}
		}
	})

	t.Run("a relative folder path cannot climb out", func(t *testing.T) {
		refused := []string{
			"../../etc/passwd",
			`..\..\etc\passwd`,
			"tree/../../escape/file.txt",
			"tree/../../../file.txt",
		}
		for _, in := range refused {
			if got, err := safeRelativeDir(in, "file.txt"); err == nil {
				t.Errorf("safeRelativeDir(%q) returned %q, want a refusal", in, got)
			}
		}
		accepted := map[string]string{
			"":                        "",
			"file.txt":                "",
			"tree/file.txt":           "tree",
			"/tree/sub/file.txt":      "tree/sub",
			`tree\sub\deep\file.txt`:  "tree/sub/deep",
			"tree/./sub/./file.txt":   "tree/sub",
			"tree/sub/../alt/f.txt":   "tree/alt",
			"tree with spaces/f.txt":  "tree with spaces",
			"tree/sub/another/f.json": "tree/sub/another",
		}
		for in, want := range accepted {
			got, err := safeRelativeDir(in, "file.txt")
			if err != nil {
				t.Errorf("safeRelativeDir(%q) refused a safe path: %v", in, err)
				continue
			}
			if got != want {
				t.Errorf("safeRelativeDir(%q) = %q, want %q", in, got, want)
			}
		}
	})

	t.Run("the header decoder skips pairs it cannot read", func(t *testing.T) {
		raw := "filename " + base64.StdEncoding.EncodeToString([]byte("report.bin")) +
			",dir " + base64.StdEncoding.EncodeToString([]byte("/srv/files")) +
			",broken not-base64!,flag"
		meta := parseMetadata(raw)
		if meta["filename"] != "report.bin" || meta["dir"] != "/srv/files" {
			t.Fatalf("the readable pairs did not decode: %v", meta)
		}
		if _, ok := meta["broken"]; ok {
			t.Fatalf("an undecodable pair must be skipped: %v", meta)
		}
		if value, ok := meta["flag"]; !ok || value != "" {
			t.Fatalf("a valueless key must decode to an empty string: %v", meta)
		}
	})

	t.Run("a traversal filename lands inside the destination", func(t *testing.T) {
		h := tuNewHarness(t)
		payload := []byte("root:x:0:0:root:/root:/bin/bash\n")

		location := h.begin(len(payload), map[string]string{
			"dir":      h.mountVirt,
			"filename": "../../etc/passwd",
		})
		if got := h.mustPatch(location, 0, payload); got != int64(len(payload)) {
			t.Fatalf("offset = %d, want %d", got, len(payload))
		}

		if names := h.entries(); len(names) != 1 || names[0] != "passwd" {
			t.Fatalf("the destination holds %v, want only the stripped name passwd", names)
		}
		if got := h.read("passwd"); !bytes.Equal(got, payload) {
			t.Fatalf("the file did not land intact, it reads %q", got)
		}

		// Nothing may have appeared above the mount.
		outer, err := os.ReadDir(h.baseOS)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range outer {
			if entry.Name() == "etc" || entry.Name() == "passwd" {
				t.Fatalf("the upload escaped the mount and created %q next to it", entry.Name())
			}
		}
	})

	t.Run("a filename that is only a traversal is refused", func(t *testing.T) {
		h := tuNewHarness(t)
		resp := h.create("10", map[string]string{"dir": h.mountVirt, "filename": "../.."})
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("a filename that strips to nothing returned %d, want 400", resp.StatusCode)
		}
		if names := h.entries(); len(names) != 0 {
			t.Fatalf("the refused create left %v behind", names)
		}
	})

	t.Run("a relative path that climbs out is refused", func(t *testing.T) {
		h := tuNewHarness(t)
		resp := h.create("10", map[string]string{
			"dir":          h.mountVirt,
			"filename":     "passwd",
			"relativePath": "../../etc/passwd",
		})
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("a relative path climbing out of the destination returned %d, want 400", resp.StatusCode)
		}
		if names := h.entries(); len(names) != 0 {
			t.Fatalf("the refused create left %v behind", names)
		}
	})
}
