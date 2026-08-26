package api_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/XProject25/Storix/internal/api"
	"github.com/XProject25/Storix/internal/auth"
	"github.com/XProject25/Storix/internal/config"
	"github.com/XProject25/Storix/internal/events"
	"github.com/XProject25/Storix/internal/jobs"
	"github.com/XProject25/Storix/internal/store"
	"github.com/XProject25/Storix/internal/thumbs"
	"github.com/XProject25/Storix/internal/updater"
	"github.com/XProject25/Storix/internal/upload"
	"github.com/XProject25/Storix/internal/vfs"
)

// harness is a complete Storix server backed by temporary directories.
type harness struct {
	t        *testing.T
	server   *httptest.Server
	store    *store.Store
	client   *http.Client
	filesDir string
	token    string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	filesDir := filepath.Join(base, "files")
	for _, dir := range []string{dataDir, filepath.Join(filesDir, "projects")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cfg := config.Default()
	cfg.Storage.DataDir = dataDir
	cfg.Storage.Database = ""
	cfg.Storage.UploadDir = ""
	cfg.Storage.CacheDir = ""
	cfg.Storage.ThumbDir = ""
	cfg.Storage.TrashDir = ""
	cfg.Log.File = ""
	cfg.Server.Host = "127.0.0.1"
	cfg.Normalize()
	cfg.SetPath(filepath.Join(base, "config.yaml"))
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	db, err := store.Open(cfg.Storage.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	guard := vfs.New(vfs.Options{
		Denied:       []string{"/etc/shadow"},
		TrashDir:     cfg.Storage.TrashDir,
		MaxTextBytes: cfg.Limits.TextEditMaxBytes,
	})
	t.Cleanup(func() { _ = guard.Close() })

	hub := events.NewHub()
	t.Cleanup(hub.Close)

	jobManager := jobs.NewManager(db, hub, 2)
	jobManager.Start(t.Context())

	sessions := auth.NewManager(db, auth.Options{
		CookieName: cfg.Security.CookieName,
		TTL:        cfg.Security.SessionTTL.D(),
		Path:       "/",
	})

	thumbCache, err := thumbs.New(thumbs.Options{Dir: cfg.Storage.ThumbDir, MaxSourceBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}

	uploads := upload.New(upload.Deps{
		Store:  db,
		VFS:    guard,
		Events: hub,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Expiry: time.Hour,
	})

	rest := api.New(api.Deps{
		Config:  cfg,
		Store:   db,
		VFS:     guard,
		Session: sessions,
		Jobs:    jobManager,
		Events:  hub,
		Thumbs:  thumbCache,
		Uploads: uploads,
		Updater: updater.New(updater.Options{}),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})),
	})
	t.Cleanup(rest.Close)

	server := httptest.NewServer(rest.Handler())
	t.Cleanup(server.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}

	setupToken := auth.MustToken(12)
	if err := db.SetSetting(t.Context(), "setup.token", setupToken); err != nil {
		t.Fatal(err)
	}

	return &harness{
		t:        t,
		server:   server,
		store:    db,
		client:   &http.Client{Jar: jar, Timeout: 30 * time.Second},
		filesDir: filesDir,
		token:    setupToken,
	}
}

// do issues a request carrying the CSRF token from the cookie jar.
func (h *harness) do(method, path string, body any) *http.Response {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, h.server.URL+path, reader)
	if err != nil {
		h.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token := h.csrf(); token != "" {
		req.Header.Set(auth.CSRFHeader, token)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	return resp
}

func (h *harness) csrf() string {
	parsed, err := neturl.Parse(h.server.URL)
	if err != nil {
		return ""
	}
	for _, cookie := range h.client.Jar.Cookies(parsed) {
		if cookie.Name == auth.CSRFCookie {
			return cookie.Value
		}
	}
	return ""
}

// decode reads a JSON response and asserts the status.
func (h *harness) decode(resp *http.Response, want int, dst any) {
	h.t.Helper()
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	if resp.StatusCode != want {
		h.t.Fatalf("%s: status %d, want %d, body: %s", resp.Request.URL.Path, resp.StatusCode, want, truncate(payload))
	}
	if dst == nil {
		return
	}
	if err := json.Unmarshal(payload, dst); err != nil {
		h.t.Fatalf("%s: decode: %v, body: %s", resp.Request.URL.Path, err, truncate(payload))
	}
}

func truncate(b []byte) string {
	if len(b) > 400 {
		return string(b[:400]) + "..."
	}
	return string(b)
}

// setup completes the first run wizard and signs the administrator in.
func (h *harness) setup() {
	h.t.Helper()
	resp := h.do(http.MethodPost, "/api/v1/setup", map[string]any{
		"token":       h.token,
		"username":    "admin",
		"password":    "StorixIntegration1",
		"displayName": "Storix Admin",
		"folders":     []string{vfs.Clean(h.filesDir)},
	})
	var out struct {
		OK   bool `json:"ok"`
		User struct {
			Username string `json:"username"`
			Role     string `json:"role"`
		} `json:"user"`
	}
	h.decode(resp, http.StatusOK, &out)
	if !out.OK || out.User.Role != "admin" {
		h.t.Fatalf("setup did not create an administrator: %+v", out)
	}
}

func TestSetupIsGatedByToken(t *testing.T) {
	h := newHarness(t)

	status := struct {
		SetupRequired bool   `json:"setupRequired"`
		Product       string `json:"product"`
		Developer     string `json:"developer"`
	}{}
	h.decode(h.do(http.MethodGet, "/api/v1/system/status", nil), http.StatusOK, &status)
	if !status.SetupRequired {
		t.Fatal("a fresh install must report that setup is required")
	}
	if status.Developer != "X Project" {
		t.Fatalf("developer = %q, want %q", status.Developer, "X Project")
	}

	// Everything else stays shut before setup.
	resp := h.do(http.MethodGet, "/api/v1/fs/list?path=/", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("the API must be closed before setup, got %d", resp.StatusCode)
	}

	// A wrong token cannot claim the instance.
	resp = h.do(http.MethodPost, "/api/v1/setup", map[string]any{
		"token": "not-the-real-token", "username": "intruder", "password": "Password12345",
		"folders": []string{vfs.Clean(h.filesDir)},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a wrong setup token must be refused with 403, got %d", resp.StatusCode)
	}

	h.setup()

	// The wizard cannot run twice.
	resp = h.do(http.MethodPost, "/api/v1/setup", map[string]any{
		"token": h.token, "username": "second", "password": "Password12345",
		"folders": []string{vfs.Clean(h.filesDir)},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("a completed setup must refuse a second run, got %d", resp.StatusCode)
	}
}

func TestBrowseCreateAndDeleteRoundTrip(t *testing.T) {
	h := newHarness(t)
	h.setup()
	root := vfs.Clean(h.filesDir)

	var listing struct {
		Entries []struct {
			Name  string `json:"name"`
			IsDir bool   `json:"isDir"`
		} `json:"entries"`
		Breadcrumbs []struct {
			Name string `json:"name"`
		} `json:"breadcrumbs"`
	}
	h.decode(h.do(http.MethodGet, "/api/v1/fs/list?path="+root, nil), http.StatusOK, &listing)
	if len(listing.Entries) != 1 || listing.Entries[0].Name != "projects" {
		t.Fatalf("unexpected listing: %+v", listing.Entries)
	}
	if len(listing.Breadcrumbs) == 0 {
		t.Fatal("a listing must carry breadcrumbs")
	}

	h.decode(h.do(http.MethodPost, "/api/v1/fs/mkdir", map[string]any{"path": root, "name": "Reports"}), http.StatusOK, nil)
	// Creating the same folder twice is a conflict, not a silent success.
	conflict := h.do(http.MethodPost, "/api/v1/fs/mkdir", map[string]any{"path": root, "name": "Reports"})
	conflict.Body.Close()
	if conflict.StatusCode != http.StatusConflict {
		t.Fatalf("a duplicate folder must conflict, got %d", conflict.StatusCode)
	}

	h.decode(h.do(http.MethodPost, "/api/v1/fs/touch", map[string]any{
		"path": root + "/Reports", "name": "notes.md",
	}), http.StatusOK, nil)
	h.decode(h.do(http.MethodPut, "/api/v1/fs/text", map[string]any{
		"path": root + "/Reports/notes.md", "content": "# Storix\nDeveloped by X Project\n",
	}), http.StatusOK, nil)

	var text struct {
		Content  string `json:"content"`
		Language string `json:"language"`
	}
	h.decode(h.do(http.MethodGet, "/api/v1/fs/text?path="+root+"/Reports/notes.md", nil), http.StatusOK, &text)
	if !strings.Contains(text.Content, "Developed by X Project") {
		t.Fatalf("content did not round trip: %q", text.Content)
	}
	if text.Language != "markdown" {
		t.Fatalf("language = %q, want markdown", text.Language)
	}

	// Delete moves to the recycle bin, and restoring brings it back.
	h.decode(h.do(http.MethodPost, "/api/v1/fs/delete", map[string]any{
		"paths": []string{root + "/Reports/notes.md"},
	}), http.StatusOK, nil)

	var bin struct {
		Items []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}
	h.decode(h.do(http.MethodGet, "/api/v1/trash", nil), http.StatusOK, &bin)
	if len(bin.Items) != 1 || bin.Items[0].Name != "notes.md" {
		t.Fatalf("the recycle bin should hold notes.md: %+v", bin.Items)
	}

	h.decode(h.do(http.MethodPost, "/api/v1/trash/restore", map[string]any{
		"ids": []int64{bin.Items[0].ID},
	}), http.StatusOK, nil)
	h.decode(h.do(http.MethodGet, "/api/v1/fs/stat?path="+root+"/Reports/notes.md", nil), http.StatusOK, nil)
}

func TestResumableUploadSurvivesAnInterruption(t *testing.T) {
	h := newHarness(t)
	h.setup()
	root := vfs.Clean(h.filesDir)

	payload := bytes.Repeat([]byte("storix-"), 40000) // 280000 bytes
	meta := "filename " + base64.StdEncoding.EncodeToString([]byte("archive.bin")) +
		",dir " + base64.StdEncoding.EncodeToString([]byte(root+"/projects"))

	req, err := http.NewRequest(http.MethodPost, h.server.URL+"/api/v1/tus", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", strconv.Itoa(len(payload)))
	req.Header.Set("Upload-Metadata", meta)
	req.Header.Set(auth.CSRFHeader, h.csrf())
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("tus create returned %d", resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	if location == "" {
		t.Fatal("tus create must return a Location header")
	}

	// First half, then a deliberate pause, then the rest.
	half := len(payload) / 2
	if got := h.patch(location, 0, payload[:half]); got != int64(half) {
		t.Fatalf("offset after the first chunk = %d, want %d", got, half)
	}
	if got := h.head(location); got != int64(half) {
		t.Fatalf("the server must remember the offset across requests, got %d", got)
	}
	if got := h.patch(location, int64(half), payload[half:]); got != int64(len(payload)) {
		t.Fatalf("offset after the final chunk = %d, want %d", got, len(payload))
	}

	// A stale offset must be refused rather than corrupting the file.
	stale, err := http.NewRequest(http.MethodPatch, h.server.URL+location, bytes.NewReader([]byte("x")))
	if err != nil {
		t.Fatal(err)
	}
	stale.Header.Set("Tus-Resumable", "1.0.0")
	stale.Header.Set("Upload-Offset", "5")
	stale.Header.Set("Content-Type", "application/offset+octet-stream")
	stale.Header.Set(auth.CSRFHeader, h.csrf())
	staleResp, err := h.client.Do(stale)
	if err != nil {
		t.Fatal(err)
	}
	staleResp.Body.Close()
	if staleResp.StatusCode != http.StatusConflict && staleResp.StatusCode != http.StatusNoContent {
		t.Fatalf("a stale offset should conflict, got %d", staleResp.StatusCode)
	}

	written, err := os.ReadFile(filepath.Join(h.filesDir, "projects", "archive.bin"))
	if err != nil {
		t.Fatalf("the finished upload should exist on disk: %v", err)
	}
	if !bytes.Equal(written, payload) {
		t.Fatalf("the uploaded file does not match: got %d bytes, want %d", len(written), len(payload))
	}

	// Nothing partial should be left behind in the destination.
	entries, err := os.ReadDir(filepath.Join(h.filesDir, "projects"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if vfs.IsInternal(entry.Name()) {
			t.Fatalf("a scratch file was left behind: %s", entry.Name())
		}
	}
}

func (h *harness) patch(location string, offset int64, chunk []byte) int64 {
	h.t.Helper()
	req, err := http.NewRequest(http.MethodPatch, h.server.URL+location, bytes.NewReader(chunk))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Offset", strconv.FormatInt(offset, 10))
	req.Header.Set("Content-Type", "application/offset+octet-stream")
	req.Header.Set(auth.CSRFHeader, h.csrf())
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		h.t.Fatalf("tus patch returned %d", resp.StatusCode)
	}
	got, err := strconv.ParseInt(resp.Header.Get("Upload-Offset"), 10, 64)
	if err != nil {
		h.t.Fatalf("tus patch did not report an offset: %v", err)
	}
	return got
}

func (h *harness) head(location string) int64 {
	h.t.Helper()
	req, err := http.NewRequest(http.MethodHead, h.server.URL+location, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set(auth.CSRFHeader, h.csrf())
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	resp.Body.Close()
	got, err := strconv.ParseInt(resp.Header.Get("Upload-Offset"), 10, 64)
	if err != nil {
		h.t.Fatalf("tus head did not report an offset: %v", err)
	}
	return got
}

func TestDownloadsAreRangedAndHardened(t *testing.T) {
	h := newHarness(t)
	h.setup()
	root := vfs.Clean(h.filesDir)

	content := bytes.Repeat([]byte("0123456789"), 1000)
	if err := os.WriteFile(filepath.Join(h.filesDir, "page.html"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodGet, h.server.URL+"/api/v1/fs/download?path="+root+"/page.html", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=0-99")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("a range request must return 206, got %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 100 {
		t.Fatalf("range body = %d bytes, want 100", len(body))
	}
	// Stored HTML must never be able to run in the application origin.
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("a download must carry a nosniff header")
	}
	if policy := resp.Header.Get("Content-Security-Policy"); !strings.Contains(policy, "sandbox") {
		t.Errorf("a download must carry a sandbox policy, got %q", policy)
	}
	if disposition := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(disposition, "attachment") {
		t.Errorf("a download must be an attachment, got %q", disposition)
	}
	if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "text/html") {
		t.Errorf("a download must not be served as html, got %q", ct)
	}
}

func TestPublicShareHidesTheServerPath(t *testing.T) {
	h := newHarness(t)
	h.setup()
	root := vfs.Clean(h.filesDir)
	if err := os.WriteFile(filepath.Join(h.filesDir, "public.txt"), []byte("shared"), 0o644); err != nil {
		t.Fatal(err)
	}

	var share struct {
		Token string `json:"token"`
		URL   string `json:"url"`
	}
	h.decode(h.do(http.MethodPost, "/api/v1/shares", map[string]any{
		"path": root + "/public.txt", "kind": "download", "expiresIn": "7d", "allowDownload": true,
	}), http.StatusCreated, &share)
	if share.Token == "" {
		t.Fatal("a share must carry a token")
	}

	// A visitor with no session and no cookies.
	anonymous := &http.Client{Timeout: 10 * time.Second}
	resp, err := anonymous.Get(h.server.URL + "/api/v1/public/" + share.Token)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a public share must be readable without a session, got %d: %s", resp.StatusCode, truncate(payload))
	}
	if strings.Contains(string(payload), h.filesDir) {
		t.Fatalf("a public share must never reveal the server path: %s", truncate(payload))
	}

	resp, err = anonymous.Get(h.server.URL + "/api/v1/public/" + share.Token + "/download")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "shared" {
		t.Fatalf("public download returned %q", string(body))
	}

	// An unknown token gives nothing away.
	resp, err = anonymous.Get(h.server.URL + "/api/v1/public/doesnotexist")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("an unknown share token must return 404, got %d", resp.StatusCode)
	}
}

func TestRestrictedAccountCannotLeaveItsFolder(t *testing.T) {
	h := newHarness(t)
	h.setup()
	root := vfs.Clean(h.filesDir)

	h.decode(h.do(http.MethodPost, "/api/v1/users", map[string]any{
		"username": "john", "password": "JohnStorix12345", "displayName": "John", "role": "user",
		"mounts": []map[string]any{{"path": root + "/projects", "label": "Projects"}},
	}), http.StatusCreated, nil)

	// Sign in as john in a separate jar.
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	john := &harness{t: t, server: h.server, store: h.store, client: &http.Client{Jar: jar, Timeout: 10 * time.Second}, filesDir: h.filesDir}
	john.decode(john.do(http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "john", "password": "JohnStorix12345",
	}), http.StatusOK, nil)

	john.decode(john.do(http.MethodGet, "/api/v1/fs/list?path="+root+"/projects", nil), http.StatusOK, nil)

	refused := []string{
		"/api/v1/fs/list?path=" + root,
		"/api/v1/fs/list?path=" + root + "/projects/../",
		"/api/v1/fs/stat?path=" + root + "/public.txt",
	}
	for _, path := range refused {
		resp := john.do(http.MethodGet, path, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d, a restricted account must not reach outside its folder", path, resp.StatusCode)
		}
	}

	for _, path := range []string{"/api/v1/users", "/api/v1/system/settings", "/api/v1/system/roots"} {
		resp := john.do(http.MethodGet, path, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", path, resp.StatusCode)
		}
	}
}

func TestWritesRequireTheCSRFToken(t *testing.T) {
	h := newHarness(t)
	h.setup()
	root := vfs.Clean(h.filesDir)

	body, err := json.Marshal(map[string]any{"path": root, "name": "sneaky"})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, h.server.URL+"/api/v1/fs/mkdir", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	// The session cookie rides along, the CSRF header does not.
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a write without the CSRF header must be refused, got %d", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(h.filesDir, "sneaky")); err == nil {
		t.Fatal("the refused request still created the folder")
	}
}

func TestSignInFailuresAreIndistinguishable(t *testing.T) {
	h := newHarness(t)
	h.setup()

	unknown := h.do(http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "nobody", "password": "whatever12345",
	})
	wrong := h.do(http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "admin", "password": "definitelywrong",
	})
	var a, b struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	h.decode(unknown, http.StatusUnauthorized, &a)
	h.decode(wrong, http.StatusUnauthorized, &b)
	if a.Error.Message != b.Error.Message || a.Error.Code != b.Error.Code {
		t.Fatalf("an unknown user and a wrong password must look identical: %+v vs %+v", a.Error, b.Error)
	}
}
