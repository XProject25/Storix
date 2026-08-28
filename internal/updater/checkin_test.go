package updater

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testInstance is a made up identifier of the documented shape.
const testInstance = "9f2c41ab7d5e40c8b3a16f0e2d7c8a95"

// newTestUpdater builds an updater pointed at a stand in service. The binary
// path is a real temporary file name so Writable answers from a directory that
// exists rather than from wherever the test binary happens to live.
func newTestUpdater(t *testing.T, o Options) *Updater {
	t.Helper()
	if o.Current == "" {
		o.Current = "1.3.0"
	}
	if o.Instance == "" {
		o.Instance = testInstance
	}
	if o.BinaryPath == "" {
		o.BinaryPath = filepath.Join(t.TempDir(), "storix")
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return New(o)
}

// A check in carries the five documented fields and not one thing more. This
// is the promise in docs/UPDATES.md, so it is asserted on the whole key set
// rather than on the fields somebody remembered to look at.
// TestCheckInSendsOnlyTheDocumentedFields pins the payload to the list in
// docs/UPDATES.md. That page is what somebody reads when deciding whether to
// trust this, so a field added here has to be added there in the same change.
// If this fails after a field was added on purpose, fix the documentation
// first and the list below second.
func TestCheckInSendsOnlyTheDocumentedFields(t *testing.T) {
	var got map[string]any
	var contentType, method string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		contentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode check in: %v", err)
		}
		writeReply(t, w, `{"version":"1.3.0"}`)
	}))
	defer srv.Close()

	up := newTestUpdater(t, Options{Endpoint: srv.URL, Check: true, Channel: "stable"})
	if _, err := up.Check(context.Background()); err != nil {
		t.Fatalf("check: %v", err)
	}

	if method != http.MethodPost {
		t.Errorf("method = %s, want POST", method)
	}
	if contentType != "application/json" {
		t.Errorf("content type = %q, want application/json", contentType)
	}

	want := []string{"arch", "channel", "instance", "os", "version"}
	keys := make([]string, 0, len(got))
	for key := range got {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("check in fields = %v, want exactly %v", keys, want)
	}
	if got["instance"] != testInstance {
		t.Errorf("instance = %v, want the configured identifier", got["instance"])
	}
	if got["version"] != "1.3.0" {
		t.Errorf("version = %v, want 1.3.0", got["version"])
	}
	if got["channel"] != "stable" {
		t.Errorf("channel = %v, want stable", got["channel"])
	}
	for _, key := range []string{"os", "arch"} {
		if v, _ := got[key].(string); v == "" {
			t.Errorf("%s is empty", key)
		}
	}
}

// A newer version is offered, with the release details the service sent.
func TestCheckServiceReportsANewerVersion(t *testing.T) {
	body := `{"version":"1.3.1","notes":"Fixes","url":"https://github.com/XProject25/Storix/releases/tag/v1.3.1",
	          "asset":"storix_1.3.1_linux_amd64.tar.gz","assetUrl":"https://example.test/storix.tar.gz",
	          "checksumUrl":"https://example.test/checksums.txt","publishedAt":"2026-08-26T12:00:00Z"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeReply(t, w, body)
	}))
	defer srv.Close()

	up := newTestUpdater(t, Options{Endpoint: srv.URL, Check: true})
	rel, err := up.Check(context.Background())
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !rel.Available {
		t.Fatal("1.3.1 must be offered to a server running 1.3.0")
	}
	if rel.Version != "1.3.1" || rel.Current != "1.3.0" {
		t.Errorf("version = %q, current = %q", rel.Version, rel.Current)
	}
	if rel.Asset != "storix_1.3.1_linux_amd64.tar.gz" || rel.AssetURL == "" || rel.Checksum == "" {
		t.Errorf("release is missing its download details: %+v", rel)
	}
	if rel.PublishedAt.IsZero() {
		t.Error("published date was not decoded")
	}
	if rel.Notes != "Fixes" {
		t.Errorf("notes = %q", rel.Notes)
	}
}

// The service answers a current caller with its own version, and that is not
// an update.
func TestCheckServiceReportsNothingWhenCurrent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeReply(t, w, `{"version":"1.3.0"}`)
	}))
	defer srv.Close()

	up := newTestUpdater(t, Options{Endpoint: srv.URL, Check: true})
	rel, err := up.Check(context.Background())
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if rel.Available {
		t.Error("1.3.0 must not be offered to a server already running it")
	}
}

// Every way the service can let us down returns an error, because an error is
// what makes the caller fall back to the release feed. Each case calls
// checkService directly so a failure cannot quietly reach the network.
func TestCheckServiceFailuresAreReported(t *testing.T) {
	t.Run("server error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "no", http.StatusInternalServerError)
		}))
		defer srv.Close()

		up := newTestUpdater(t, Options{Endpoint: srv.URL, Check: true})
		if _, err := up.checkService(context.Background(), up.checkIn()); err == nil {
			t.Fatal("a 500 must be an error so the caller falls back")
		}
	})

	t.Run("malformed body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeReply(t, w, `{"version": `)
		}))
		defer srv.Close()

		up := newTestUpdater(t, Options{Endpoint: srv.URL, Check: true})
		if _, err := up.checkService(context.Background(), up.checkIn()); err == nil {
			t.Fatal("a body that is not a release must be an error")
		}
	})

	t.Run("timeout", func(t *testing.T) {
		// The handler waits until the test is done, so the client is the one
		// that gives up. Closing the gate before the server keeps Close from
		// waiting on a handler that is still parked.
		gate := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-gate:
			case <-r.Context().Done():
			}
		}))
		defer srv.Close()
		defer close(gate)

		up := newTestUpdater(t, Options{
			Endpoint: srv.URL,
			Check:    true,
			Client:   &http.Client{Timeout: 150 * time.Millisecond},
		})
		if _, err := up.checkService(context.Background(), up.checkIn()); err == nil {
			t.Fatal("a service that does not answer must be an error")
		}
	})
}

// With checking switched off nothing is asked of anybody.
func TestCheckOffContactsNobody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the update service was contacted although checking is switched off")
	}))
	defer srv.Close()

	up := newTestUpdater(t, Options{Endpoint: srv.URL, Check: false})
	rel, err := up.Check(context.Background())
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if rel.Available {
		t.Error("a switched off check must not offer an update")
	}
	if rel.Message != "Update checking is switched off" {
		t.Errorf("message = %q", rel.Message)
	}
	if rel.Version != "1.3.0" {
		t.Errorf("version = %q, want the running version", rel.Version)
	}
}

// The floor between checks belongs to the updater, so it holds however many
// callers arrive and whichever of them asks first.
func TestIntervalFloorAllowsOneRequest(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeReply(t, w, `{"version":"1.3.1","assetUrl":"https://example.test/storix.tar.gz"}`)
	}))
	defer srv.Close()

	up := newTestUpdater(t, Options{Endpoint: srv.URL, Check: true, Interval: time.Hour})
	first, err := up.Check(context.Background())
	if err != nil {
		t.Fatalf("first check: %v", err)
	}
	second, err := up.Check(context.Background())
	if err != nil {
		t.Fatalf("second check: %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("the service was asked %d times inside the interval, want 1", n)
	}
	if first.Version != second.Version || !second.Available {
		t.Errorf("the second answer differs from the first: %+v vs %+v", first, second)
	}
}

// An endpoint on GitHub is not a check in service, so no identifier is sent to
// it. That is the documented way to keep the update check while staying
// uncounted.
func TestGitHubEndpointIsNotACheckInService(t *testing.T) {
	if serviceEndpoint("https://api.github.com/repos/XProject25/Storix/releases/latest") {
		t.Error("a GitHub address must never receive the check in document")
	}
	if serviceEndpoint("") {
		t.Error("an empty endpoint is not a service")
	}
	if !serviceEndpoint("https://updates.xproject.live/v1/check") {
		t.Error("the update service must receive the check in document")
	}
}

// writeReply sends a service answer.
func writeReply(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if _, err := io.WriteString(w, body); err != nil {
		t.Errorf("write reply: %v", err)
	}
}
