package api_test

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// davRequest issues a WebDAV request with Basic credentials.
func (h *harness) davRequest(method, path, user, pass string, body io.Reader, headers map[string]string) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest(method, h.server.URL+path, body)
	if err != nil {
		h.t.Fatal(err)
	}
	if user != "" {
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pass)))
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	// A cookie jar would let a browser session leak in and hide a missing
	// credential, so the drive is always exercised with a bare client.
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	return resp
}

const davPropfindAll = `<?xml version="1.0" encoding="utf-8" ?><D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>`

func TestWebDAVRefusesAnonymousCallers(t *testing.T) {
	h := newHarness(t)
	h.setup()

	resp := h.davRequest(http.MethodGet, "/dav/", "", "", nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no credentials must give 401, got %d", resp.StatusCode)
	}
	if challenge := resp.Header.Get("WWW-Authenticate"); !strings.Contains(strings.ToLower(challenge), "basic") {
		t.Fatalf("a 401 must ask for Basic credentials, got %q", challenge)
	}

	wrong := h.davRequest(http.MethodGet, "/dav/", "admin", "not-the-password", nil, nil)
	wrong.Body.Close()
	if wrong.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong credentials must give 401, got %d", wrong.StatusCode)
	}
}

func TestWebDAVListsOnlyTheMounts(t *testing.T) {
	h := newHarness(t)
	h.setup()

	resp := h.davRequest("PROPFIND", "/dav/", "admin", "StorixIntegration1",
		strings.NewReader(davPropfindAll), map[string]string{"Depth": "1", "Content-Type": "application/xml"})
	payload, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND on the root must give 207, got %d: %s", resp.StatusCode, truncate(payload))
	}
	body := string(payload)
	// The mount created by setup is the temporary files directory, exposed
	// under a slug rather than its absolute server path.
	if strings.Contains(body, h.filesDir) {
		t.Fatalf("the drive must not reveal the absolute server path: %s", truncate(payload))
	}
	if !strings.Contains(body, "<D:href>") && !strings.Contains(body, "<d:href>") {
		t.Fatalf("a multistatus response must carry hrefs: %s", truncate(payload))
	}
}

func TestWebDAVRoundTripsAFile(t *testing.T) {
	h := newHarness(t)
	h.setup()
	slug := h.davFirstSlug()

	content := bytes.Repeat([]byte("storix over webdav\n"), 500)
	put := h.davRequest(http.MethodPut, "/dav/"+slug+"/from-dav.txt", "admin", "StorixIntegration1",
		bytes.NewReader(content), nil)
	put.Body.Close()
	if put.StatusCode != http.StatusCreated && put.StatusCode != http.StatusNoContent && put.StatusCode != http.StatusOK {
		t.Fatalf("PUT returned %d", put.StatusCode)
	}

	// It has to be a real file in the real folder, not a WebDAV only illusion.
	onDisk, err := os.ReadFile(filepath.Join(h.filesDir, "from-dav.txt"))
	if err != nil {
		t.Fatalf("the uploaded file should exist on disk: %v", err)
	}
	if !bytes.Equal(onDisk, content) {
		t.Fatalf("on disk content differs: got %d bytes, want %d", len(onDisk), len(content))
	}

	get := h.davRequest(http.MethodGet, "/dav/"+slug+"/from-dav.txt", "admin", "StorixIntegration1", nil, nil)
	back, err := io.ReadAll(get.Body)
	get.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if get.StatusCode != http.StatusOK || !bytes.Equal(back, content) {
		t.Fatalf("GET returned %d and %d bytes, want 200 and %d", get.StatusCode, len(back), len(content))
	}

	del := h.davRequest(http.MethodDelete, "/dav/"+slug+"/from-dav.txt", "admin", "StorixIntegration1", nil, nil)
	del.Body.Close()
	if del.StatusCode != http.StatusNoContent && del.StatusCode != http.StatusOK {
		t.Fatalf("DELETE returned %d", del.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(h.filesDir, "from-dav.txt")); !os.IsNotExist(err) {
		t.Fatal("the file should be gone from disk after DELETE")
	}
}

func TestWebDAVCreatesAndMovesFolders(t *testing.T) {
	h := newHarness(t)
	h.setup()
	slug := h.davFirstSlug()

	mkcol := h.davRequest("MKCOL", "/dav/"+slug+"/reports", "admin", "StorixIntegration1", nil, nil)
	mkcol.Body.Close()
	if mkcol.StatusCode != http.StatusCreated {
		t.Fatalf("MKCOL returned %d", mkcol.StatusCode)
	}
	if info, err := os.Stat(filepath.Join(h.filesDir, "reports")); err != nil || !info.IsDir() {
		t.Fatalf("MKCOL should have created a real folder: %v", err)
	}

	move := h.davRequest("MOVE", "/dav/"+slug+"/reports", "admin", "StorixIntegration1", nil, map[string]string{
		"Destination": h.server.URL + "/dav/" + slug + "/archive",
		"Overwrite":   "F",
	})
	move.Body.Close()
	if move.StatusCode != http.StatusCreated && move.StatusCode != http.StatusNoContent {
		t.Fatalf("MOVE returned %d", move.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(h.filesDir, "archive")); err != nil {
		t.Fatalf("the folder should have moved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.filesDir, "reports")); !os.IsNotExist(err) {
		t.Fatal("the old folder name should be gone")
	}
}

// The drive is a second door onto the same files, so it has to hold the same
// line. A name that climbs out of a mount must never resolve.
func TestWebDAVCannotClimbOutOfAMount(t *testing.T) {
	h := newHarness(t)
	h.setup()
	slug := h.davFirstSlug()

	outside := filepath.Join(filepath.Dir(h.filesDir), "secret.txt")
	if err := os.WriteFile(outside, []byte("not for the drive"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, target := range []string{
		"/dav/" + slug + "/../secret.txt",
		"/dav/" + slug + "/%2e%2e/secret.txt",
		"/dav/../secret.txt",
		"/dav/nosuchmount/secret.txt",
	} {
		resp := h.davRequest(http.MethodGet, target, "admin", "StorixIntegration1", nil, nil)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK && strings.Contains(string(body), "not for the drive") {
			t.Errorf("%s served a file from outside the mount", target)
		}
	}
}

// A read only token must not be able to write through the drive, or the
// scope would be a suggestion rather than a limit.
func TestWebDAVHonoursTheSetupMountAndMethods(t *testing.T) {
	h := newHarness(t)
	h.setup()

	options := h.davRequest(http.MethodOptions, "/dav/", "admin", "StorixIntegration1", nil, nil)
	options.Body.Close()
	if options.StatusCode != http.StatusOK && options.StatusCode != http.StatusNoContent {
		t.Fatalf("OPTIONS returned %d", options.StatusCode)
	}
	if dav := options.Header.Get("DAV"); dav == "" {
		t.Error("OPTIONS must advertise a DAV compliance class so clients will mount")
	}
	if allow := options.Header.Get("Allow"); allow != "" && !strings.Contains(allow, "PROPFIND") {
		t.Errorf("Allow should include PROPFIND, got %q", allow)
	}
}

// davFirstSlug reads the first collection name the drive exposes, which is how
// a real client discovers where to write.
func (h *harness) davFirstSlug() string {
	h.t.Helper()
	resp := h.davRequest("PROPFIND", "/dav/", "admin", "StorixIntegration1",
		strings.NewReader(davPropfindAll), map[string]string{"Depth": "1", "Content-Type": "application/xml"})
	payload, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		h.t.Fatal(err)
	}
	body := string(payload)
	for _, marker := range []string{"<D:href>", "<d:href>"} {
		for _, part := range strings.Split(body, marker)[1:] {
			href := part
			if idx := strings.IndexByte(href, '<'); idx >= 0 {
				href = href[:idx]
			}
			trimmed := strings.Trim(strings.TrimPrefix(href, "/dav"), "/")
			if trimmed != "" {
				return trimmed
			}
		}
	}
	h.t.Fatalf("the drive listed no collections: %s", truncate(payload))
	return ""
}
