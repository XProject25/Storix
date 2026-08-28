package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XProject25/Storix/internal/config"
	"github.com/XProject25/Storix/internal/updater"
)

// TestUpdateWatchChecksWithoutAnybodyWatching is the point of the loop: a
// server nobody has signed in to still asks whether there is a new release.
func TestUpdateWatchChecksWithoutAnybodyWatching(t *testing.T) {
	var calls int64
	var got updater.CheckIn
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		_ = json.Unmarshal(body, &got)
		atomic.AddInt64(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"9.9.9"}`))
	}))
	defer srv.Close()

	old := updateFirstWait
	updateFirstWait = 10 * time.Millisecond
	defer func() { updateFirstWait = old }()

	cfg := config.Default()
	cfg.Updates.Check = true
	cfg.Updates.Endpoint = srv.URL
	cfg.Updates.Interval = config.Duration(time.Hour)

	a := &app{
		cfg: cfg,
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		updater: updater.New(updater.Options{
			Endpoint: srv.URL,
			Instance: "9f2c41ab7d5e40c8b3a16f0e2d7c8a95",
			Current:  "1.0.0",
			Check:    true,
			Interval: time.Hour,
		}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go a.updateWatch(ctx)

	deadline := time.Now().Add(4 * time.Second)
	for atomic.LoadInt64(&calls) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt64(&calls) == 0 {
		t.Fatal("nobody opened a page and no check was made")
	}
	if got.Instance != "9f2c41ab7d5e40c8b3a16f0e2d7c8a95" {
		t.Fatalf("the check carried instance %q", got.Instance)
	}
	if got.Version != "1.0.0" || got.OS == "" || got.Arch == "" {
		t.Fatalf("the check carried %+v", got)
	}
}

// TestUpdateWatchStaysSilentWhenTurnedOff is the promise in docs/UPDATES.md:
// with the check off, nothing is ever sent.
func TestUpdateWatchStaysSilentWhenTurnedOff(t *testing.T) {
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&calls, 1)
		_, _ = w.Write([]byte(`{"version":"9.9.9"}`))
	}))
	defer srv.Close()

	old := updateFirstWait
	updateFirstWait = 10 * time.Millisecond
	defer func() { updateFirstWait = old }()

	cfg := config.Default()
	cfg.Updates.Check = false
	cfg.Updates.Endpoint = srv.URL

	a := &app{
		cfg: cfg,
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		updater: updater.New(updater.Options{
			Endpoint: srv.URL, Instance: "9f2c41ab7d5e40c8b3a16f0e2d7c8a95",
			Current: "1.0.0", Check: false,
		}),
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a.updateWatch(ctx) // returns at once, it must not block
	time.Sleep(300 * time.Millisecond)
	if n := atomic.LoadInt64(&calls); n != 0 {
		t.Fatalf("an install with checking off contacted the service %d times", n)
	}
}

func TestRandomWaitStaysInBounds(t *testing.T) {
	if d := randomWait(time.Millisecond); d != 0 {
		t.Fatalf("a span too small to divide gave %s", d)
	}
	for i := 0; i < 200; i++ {
		if d := randomWait(time.Hour); d < 0 || d >= 30*time.Minute {
			t.Fatalf("randomWait(1h) gave %s", d)
		}
	}
}
