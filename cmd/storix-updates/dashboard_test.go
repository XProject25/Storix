package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"strings"
	"testing"
	"time"
)

// dashboardRequest asks the page for a key.
func dashboardRequest(t *testing.T, svc *service, key string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	target := "/dashboard"
	if key != "" {
		target += "?k=" + neturl.QueryEscape(key)
	}
	svc.handleDashboard(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestDashboardNeedsItsKey(t *testing.T) {
	svc, _ := newTestService(t, releaseFeed())
	svc.viewKey = "the-right-key"

	for _, key := range []string{"", "wrong", "the-right-ke", "the-right-keyy", "THE-RIGHT-KEY"} {
		rec := dashboardRequest(t, svc, key)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("key %q answered %d, want 404", key, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "servers running") {
			t.Fatalf("key %q was shown the figures", key)
		}
	}

	// A key pasted with whitespace around it still works. That is deliberate:
	// it costs nothing, since the key itself still has to be exact, and it
	// saves somebody puzzling over a link that looks right.
	if rec := dashboardRequest(t, svc, "  the-right-key  "); rec.Code != http.StatusOK {
		t.Fatalf("a pasted key with spaces answered %d, want 200", rec.Code)
	}
}

func TestDashboardIsOffWithoutAKey(t *testing.T) {
	svc, _ := newTestService(t, releaseFeed())
	svc.viewKey = ""
	rec := dashboardRequest(t, svc, "anything")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("answered %d, want 503 when the page is not enabled", rec.Code)
	}
}

func TestDashboardShowsTheCount(t *testing.T) {
	svc, st := newTestService(t, releaseFeed())
	svc.viewKey = "k"
	now := time.Now().UTC()
	for i := 1; i <= 3; i++ {
		in := CheckIn{Instance: instanceID(i), Version: "1.4.2", OS: "linux", Arch: "amd64", Channel: ChannelStable}
		if err := st.Record(context.Background(), in, now); err != nil {
			t.Fatal(err)
		}
	}
	rec := dashboardRequest(t, svc, "k")
	if rec.Code != http.StatusOK {
		t.Fatalf("answered %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"servers running Storix", "1.4.2", "linux/amd64", "Developed by X Project"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the page does not mention %q", want)
		}
	}
	if !strings.Contains(body, `<div class="big">3</div>`) {
		t.Fatal("the page does not show the count")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content type is %q", ct)
	}
	if rec.Header().Get("X-Robots-Tag") == "" {
		t.Fatal("the figures are the operator's, the page should not invite indexing")
	}
}

// TestDashboardEscapesWhatCallersSend matters because the version and platform
// labels on the page come from anonymous callers. The validator already
// narrows them, but the page must not depend on that being perfect.
func TestDashboardEscapesWhatCallersSend(t *testing.T) {
	svc, st := newTestService(t, releaseFeed())
	svc.viewKey = "k"

	// Written straight to the table, going around the validator on purpose.
	hostile := `<script>alert(1)</script>`
	now := time.Now().UTC().Unix()
	if _, err := st.db.Exec(`
        INSERT INTO instances (instance, version, os, arch, channel, first_seen, last_seen, checks)
        VALUES (?, ?, ?, ?, ?, ?, ?, 1)`,
		instanceID(1), hostile, "linux", "amd64", ChannelStable, now, now); err != nil {
		t.Fatal(err)
	}
	rec := dashboardRequest(t, svc, "k")
	if rec.Code != http.StatusOK {
		t.Fatalf("answered %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, hostile) {
		t.Fatal("a caller's text was written into the page unescaped")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatal("the hostile label is not on the page at all, so this proves nothing")
	}
}

func TestDashboardRefusesOtherMethods(t *testing.T) {
	svc, _ := newTestService(t, releaseFeed())
	svc.viewKey = "k"
	rec := httptest.NewRecorder()
	svc.handleDashboard(rec, httptest.NewRequest(http.MethodPost, "/dashboard?k=k", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST answered %d, want 405", rec.Code)
	}
	if rec.Header().Get("Allow") == "" {
		t.Fatal("405 without an Allow header")
	}
}
