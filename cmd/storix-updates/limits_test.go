package main

// Tests for the limits that make this service safe to expose. It answers
// anonymous callers on the public internet, from a machine that also runs
// unrelated sites, so what an attacker can spend has to be bounded and stay
// bounded.

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestKnownServerIsNeverRefused is the property the budget must not break. A
// server that already has a row keeps being counted no matter how much noise
// somebody else is making.
func TestKnownServerIsNeverRefused(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	known := CheckIn{Instance: instanceID(1), Version: "1.4.0", OS: "linux", Arch: "amd64", Channel: ChannelStable}
	if err := st.Record(ctx, known, now); err != nil {
		t.Fatalf("first check in: %v", err)
	}

	// Drain the budget, exactly as a flood would.
	st.gate.Lock()
	st.filled, st.tokens = now, 0
	st.gate.Unlock()

	for i := 0; i < 5; i++ {
		if err := st.Record(ctx, known, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("known server refused: %v", err)
		}
	}
	got := readRow(t, st, known.Instance)
	if got.checks != 6 {
		t.Fatalf("checks = %d, want 6, the known server was gated", got.checks)
	}
	if n := st.refused.Load(); n != 0 {
		t.Fatalf("refused = %d, want 0 for a server that already had a row", n)
	}
}

// TestForgedIdentifiersAreBounded is the reason the budget exists: an
// anonymous caller inventing identifiers cannot grow the table freely.
func TestForgedIdentifiersAreBounded(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	const forged = newInstanceBurst + 250
	for i := 1; i <= forged; i++ {
		in := CheckIn{Instance: instanceID(i), Version: "1.4.0", OS: "linux", Arch: "amd64", Channel: ChannelStable}
		// Every one of these is answered, none of them is an error: a caller
		// that is not counted still gets told about the newest release.
		if err := st.Record(ctx, in, now); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	rows := countRows(t, st)
	if rows > newInstanceBurst {
		t.Fatalf("%d rows were created from %d forged identifiers, the burst is %d", rows, forged, newInstanceBurst)
	}
	if st.refused.Load() == 0 {
		t.Fatal("nothing was refused, so nothing was bounded")
	}
	if int64(rows)+st.refused.Load() != forged {
		t.Fatalf("%d rows plus %d refused does not account for %d attempts", rows, st.refused.Load(), forged)
	}
	stats, err := st.Stats(ctx, now)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.RefusedNew != st.refused.Load() {
		t.Fatalf("the statistics hide the refusals: %d vs %d", stats.RefusedNew, st.refused.Load())
	}
}

// TestBudgetRefills keeps the bound from turning into a wall. Real growth
// must never be blocked for long.
func TestBudgetRefills(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	st.gate.Lock()
	st.filled, st.tokens = now, 0
	st.gate.Unlock()

	first := CheckIn{Instance: instanceID(9001), Version: "1.4.0", OS: "linux", Arch: "amd64", Channel: ChannelStable}
	if err := st.Record(ctx, first, now); err != nil {
		t.Fatalf("record: %v", err)
	}
	if countRows(t, st) != 0 {
		t.Fatal("a new server was counted while the budget was empty")
	}

	later := now.Add(time.Hour)
	second := CheckIn{Instance: instanceID(9002), Version: "1.4.0", OS: "linux", Arch: "amd64", Channel: ChannelStable}
	if err := st.Record(ctx, second, later); err != nil {
		t.Fatalf("record after an hour: %v", err)
	}
	if countRows(t, st) != 1 {
		t.Fatal("the budget did not refill, a real install would never be counted")
	}
}

// TestPruneGivesTheRoomBack checks that forgetting rows makes room again,
// rather than leaving the ceiling permanently spent.
func TestPruneGivesTheRoomBack(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for i := 1; i <= 10; i++ {
		in := CheckIn{Instance: instanceID(i), Version: "1.4.0", OS: "linux", Arch: "amd64", Channel: ChannelStable}
		if err := st.Record(ctx, in, now.Add(-200*24*time.Hour)); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	if st.rows.Load() != 10 {
		t.Fatalf("counter says %d rows, the table has 10", st.rows.Load())
	}
	n, err := st.Prune(ctx, 180*24*time.Hour, now)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 10 {
		t.Fatalf("prune removed %d rows, want 10", n)
	}
	if st.rows.Load() != 0 {
		t.Fatalf("counter still says %d rows after everything was forgotten", st.rows.Load())
	}
}

// TestChannelIsAnAllowlist is what keeps the release cache and the calls to
// the release feed bounded: the channel becomes a key, so it cannot be an
// arbitrary word.
func TestChannelIsAnAllowlist(t *testing.T) {
	for _, ok := range []string{ChannelStable, ChannelBeta} {
		if !validChannel(ok) {
			t.Fatalf("%q should be a channel", ok)
		}
	}
	for _, bad := range []string{"", "nightly", "aaaa", "stablex", "STABLE", "stable "} {
		if validChannel(bad) {
			t.Fatalf("%q was accepted as a channel, so it would become a cache key", bad)
		}
	}
	st := testStore(t)
	in := CheckIn{Instance: instanceID(1), Version: "1.4.0", OS: "linux", Arch: "amd64", Channel: "aaaa"}
	if err := st.Record(context.Background(), in, time.Now()); err == nil {
		t.Fatal("a made up channel was recorded")
	}
}

// TestDatabaseIsNotWorldReadable matters because this service shares a machine
// with unrelated sites.
func TestDatabaseIsNotWorldReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes mean something else here, and this service runs on Linux")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "updates.db")
	st, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer st.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode&0o007 != 0 {
		t.Fatalf("the database is mode %04o, readable by anybody on the machine", mode)
	}
}

// newTestService builds the service with its release feed pointed at a server
// the test controls, so nothing here talks to GitHub.
func newTestService(t *testing.T, feed http.Handler) (*service, *Store) {
	t.Helper()
	origin := httptest.NewServer(feed)
	t.Cleanup(origin.Close)

	st := testStore(t)
	cache := newReleaseCache("XProject25/Storix", slog.New(slog.NewTextHandler(io.Discard, nil)))
	cache.base = origin.URL
	return &service{
		store:      st,
		releases:   cache,
		statsToken: "",
		retention:  180 * 24 * time.Hour,
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, st
}

func releaseFeed() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v1.4.0","name":"Storix 1.4.0","draft":false,"assets":[]}`))
	})
}

// TestCheckRejectsAnInventedChannel proves the allowlist holds through the
// real route, not only in the validator.
func TestCheckRejectsAnInventedChannel(t *testing.T) {
	svc, st := newTestService(t, releaseFeed())
	body := `{"instance":"` + instanceID(1) + `","version":"1.4.0","os":"linux","arch":"amd64","channel":"aaaa"}`
	rec := httptest.NewRecorder()
	svc.handleCheck(rec, httptest.NewRequest(http.MethodPost, "/v1/check", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an invented channel answered %d, want 400", rec.Code)
	}
	if n := countRows(t, st); n != 0 {
		t.Fatalf("%d rows were written for a rejected check in", n)
	}
	if len(svc.releases.entries) != 0 {
		t.Fatalf("the invented channel became a cache key: %v", svc.releases.entries)
	}
}

// TestMethodNotAllowedSaysWhatIsAllowed is plain HTTP correctness.
func TestMethodNotAllowedSaysWhatIsAllowed(t *testing.T) {
	svc, _ := newTestService(t, releaseFeed())
	rec := httptest.NewRecorder()
	svc.handleCheck(rec, httptest.NewRequest(http.MethodGet, "/v1/check", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET answered %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodPost {
		t.Fatalf("Allow = %q, want POST", got)
	}
}

// TestOneSlowFeedDoesNotBlockEveryCaller is the lock fix. A fetch that hangs
// must not park callers whose answer is already cached: before the fix the map
// lock was held across the network call and every request queued behind it.
func TestOneSlowFeedDoesNotBlockEveryCaller(t *testing.T) {
	release := make(chan struct{})
	var slow atomic.Bool
	slow.Store(true)

	svc, _ := newTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "per_page") && slow.Load() {
			<-release // the beta feed hangs until the test lets it go
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.RawQuery, "per_page") {
			_, _ = w.Write([]byte(`[{"tag_name":"v1.5.0","draft":false,"assets":[]}]`))
			return
		}
		_, _ = w.Write([]byte(`{"tag_name":"v1.4.0","draft":false,"assets":[]}`))
	}))

	ctx := context.Background()
	// Warm the stable answer, so the second caller needs no network at all.
	if _, err := svc.releases.get(ctx, ChannelStable); err != nil {
		t.Fatalf("warm the cache: %v", err)
	}

	hanging := make(chan struct{})
	go func() {
		defer close(hanging)
		_, _ = svc.releases.get(ctx, ChannelBeta)
	}()

	// Give the hanging fetch time to be underway and holding whatever it holds.
	time.Sleep(200 * time.Millisecond)

	done := make(chan error, 1)
	go func() {
		_, err := svc.releases.get(ctx, ChannelStable)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the cached answer failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("a cached answer waited on an unrelated fetch, the lock is held across the network call")
	}
	slow.Store(false)
	close(release)
	<-hanging
}
