package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// testStore opens a throwaway database for one test.
func testStore(t *testing.T) *Store {
	t.Helper()
	st, err := OpenStore(filepath.Join(t.TempDir(), "updates.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return st
}

// row is one stored instance, read back for assertions.
type row struct {
	version   string
	os        string
	arch      string
	channel   string
	firstSeen int64
	lastSeen  int64
	checks    int64
}

// readRow fetches one instance straight from the table.
func readRow(t *testing.T, st *Store, instance string) row {
	t.Helper()
	var got row
	err := st.db.QueryRow(`
        SELECT version, os, arch, channel, first_seen, last_seen, checks
        FROM instances WHERE instance = ?`, instance).
		Scan(&got.version, &got.os, &got.arch, &got.channel, &got.firstSeen, &got.lastSeen, &got.checks)
	if err != nil {
		t.Fatalf("read instance: %v", err)
	}
	return got
}

// countRows reports how many instances the table holds.
func countRows(t *testing.T, st *Store) int {
	t.Helper()
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM instances`).Scan(&n); err != nil {
		t.Fatalf("count instances: %v", err)
	}
	return n
}

// instanceID builds a well formed identifier for a test.
func instanceID(n int) string { return fmt.Sprintf("%032x", n) }

func TestRecordCreatesRow(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	in := CheckIn{Instance: instanceID(1), Version: "1.3.0", OS: "linux", Arch: "amd64", Channel: "stable"}
	if err := st.Record(ctx, in, now); err != nil {
		t.Fatalf("Record: %v", err)
	}

	if n := countRows(t, st); n != 1 {
		t.Fatalf("table holds %d rows after one check in, want 1", n)
	}
	got := readRow(t, st, in.Instance)
	if got.checks != 1 {
		t.Fatalf("checks = %d after a first check in, want 1", got.checks)
	}
	if got.firstSeen != got.lastSeen {
		t.Fatalf("first_seen %d and last_seen %d differ on a first check in", got.firstSeen, got.lastSeen)
	}
	if got.firstSeen != now.Unix() {
		t.Fatalf("first_seen = %d, want %d", got.firstSeen, now.Unix())
	}
	if got.version != "1.3.0" || got.os != "linux" || got.arch != "amd64" || got.channel != "stable" {
		t.Fatalf("stored %+v, want the reported version, platform and channel", got)
	}
}

func TestRecordUpdatesInPlace(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	first := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	second := first.Add(30 * time.Hour)

	in := CheckIn{Instance: instanceID(2), Version: "1.3.0", OS: "linux", Arch: "amd64", Channel: "stable"}
	if err := st.Record(ctx, in, first); err != nil {
		t.Fatalf("Record: %v", err)
	}
	// The same server, later, on a version it installed in the meantime.
	in.Version = "1.3.1"
	if err := st.Record(ctx, in, second); err != nil {
		t.Fatalf("Record again: %v", err)
	}

	if n := countRows(t, st); n != 1 {
		t.Fatalf("table holds %d rows after two check ins from one server, want 1", n)
	}
	got := readRow(t, st, in.Instance)
	if got.version != "1.3.1" {
		t.Fatalf("version = %q after the second check in, want 1.3.1", got.version)
	}
	if got.firstSeen != first.Unix() {
		t.Fatalf("first_seen = %d, want it left at %d", got.firstSeen, first.Unix())
	}
	if got.lastSeen != second.Unix() {
		t.Fatalf("last_seen = %d, want it moved to %d", got.lastSeen, second.Unix())
	}
	if got.checks != 2 {
		t.Fatalf("checks = %d after two check ins, want 2", got.checks)
	}
}

func TestStatsCountsWindows(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	seen := []struct {
		age     time.Duration
		version string
		arch    string
	}{
		{1 * time.Hour, "1.3.0", "amd64"},
		{10 * time.Hour, "1.3.0", "amd64"},
		{3 * 24 * time.Hour, "1.3.0", "amd64"},
		{10 * 24 * time.Hour, "1.2.0", "amd64"},
		{40 * 24 * time.Hour, "1.1.0", "arm64"},
	}
	for i, s := range seen {
		in := CheckIn{Instance: instanceID(i + 1), Version: s.version, OS: "linux", Arch: s.arch, Channel: "stable"}
		if err := st.Record(ctx, in, now.Add(-s.age)); err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
	}

	stats, err := st.Stats(ctx, now)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Total != 5 {
		t.Fatalf("Total = %d, want 5", stats.Total)
	}
	if stats.Active24h != 2 {
		t.Fatalf("Active24h = %d, want 2", stats.Active24h)
	}
	if stats.Active7d != 3 {
		t.Fatalf("Active7d = %d, want 3", stats.Active7d)
	}
	if stats.Active30d != 4 {
		t.Fatalf("Active30d = %d, want 4", stats.Active30d)
	}
	if stats.Checks != 5 {
		t.Fatalf("Checks = %d, want 5", stats.Checks)
	}

	wantVersions := map[string]int{"1.3.0": 3, "1.2.0": 1, "1.1.0": 1}
	for version, want := range wantVersions {
		if stats.Versions[version] != want {
			t.Fatalf("Versions[%q] = %d, want %d", version, stats.Versions[version], want)
		}
	}
	if len(stats.Versions) != len(wantVersions) {
		t.Fatalf("Versions = %v, want %d entries", stats.Versions, len(wantVersions))
	}
	if stats.Platforms["linux/amd64"] != 4 || stats.Platforms["linux/arm64"] != 1 {
		t.Fatalf("Platforms = %v, want 4 on linux/amd64 and 1 on linux/arm64", stats.Platforms)
	}

	if stats.FirstSeen == nil || stats.LastSeen == nil {
		t.Fatal("Stats left FirstSeen or LastSeen empty, want the oldest and newest sighting")
	}
	if stats.FirstSeen.Unix() != now.Add(-40*24*time.Hour).Unix() {
		t.Fatalf("FirstSeen = %s, want the 40 day old sighting", stats.FirstSeen)
	}
	if stats.LastSeen.Unix() != now.Add(-1*time.Hour).Unix() {
		t.Fatalf("LastSeen = %s, want the one hour old sighting", stats.LastSeen)
	}
}

func TestStatsOnEmptyTable(t *testing.T) {
	st := testStore(t)
	stats, err := st.Stats(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Total != 0 || stats.Active24h != 0 || stats.Checks != 0 {
		t.Fatalf("Stats on an empty table = %+v, want zeroes", stats)
	}
	if stats.FirstSeen != nil || stats.LastSeen != nil {
		t.Fatal("Stats on an empty table named a first or last sighting")
	}
	if len(stats.Versions) != 0 || len(stats.Platforms) != 0 {
		t.Fatalf("Stats on an empty table = %v and %v, want no breakdown", stats.Versions, stats.Platforms)
	}
}

func TestPruneRemovesOnlyPastRetention(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	retention := 180 * 24 * time.Hour

	ages := []time.Duration{
		1 * time.Hour,        // current
		179 * 24 * time.Hour, // inside retention
		retention,            // exactly at the cutoff, kept
		181 * 24 * time.Hour, // past retention
		400 * 24 * time.Hour, // long past retention
	}
	for i, age := range ages {
		in := CheckIn{Instance: instanceID(i + 1), Version: "1.3.0", OS: "linux", Arch: "amd64", Channel: "stable"}
		if err := st.Record(ctx, in, now.Add(-age)); err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
	}

	removed, err := st.Prune(ctx, retention, now)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 2 {
		t.Fatalf("Prune removed %d rows, want 2", removed)
	}
	if n := countRows(t, st); n != 3 {
		t.Fatalf("table holds %d rows after pruning, want 3", n)
	}
	for _, keep := range []int{1, 2, 3} {
		readRow(t, st, instanceID(keep))
	}

	// A second run has nothing left to do.
	removed, err = st.Prune(ctx, retention, now)
	if err != nil {
		t.Fatalf("Prune again: %v", err)
	}
	if removed != 0 {
		t.Fatalf("Prune removed %d rows on a second run, want 0", removed)
	}
}

func TestRecordRefusesMalformedCheckIn(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	now := time.Now()
	good := CheckIn{Instance: instanceID(1), Version: "1.3.0", OS: "linux", Arch: "amd64", Channel: "stable"}

	cases := []struct {
		name string
		in   CheckIn
		want error
	}{
		{"empty instance", CheckIn{Instance: "", Version: "1.3.0", OS: "linux", Arch: "amd64", Channel: "stable"}, ErrInvalidInstance},
		{"too short", CheckIn{Instance: "9f2c41ab", Version: "1.3.0", OS: "linux", Arch: "amd64", Channel: "stable"}, ErrInvalidInstance},
		{"too long", CheckIn{Instance: instanceID(1) + "aa", Version: "1.3.0", OS: "linux", Arch: "amd64", Channel: "stable"}, ErrInvalidInstance},
		{"upper case", CheckIn{Instance: "9F2C41AB7D5E40C8B3A16F0E2D7C8A95", Version: "1.3.0", OS: "linux", Arch: "amd64", Channel: "stable"}, ErrInvalidInstance},
		{"not hexadecimal", CheckIn{Instance: "zzzz41ab7d5e40c8b3a16f0e2d7c8a95", Version: "1.3.0", OS: "linux", Arch: "amd64", Channel: "stable"}, ErrInvalidInstance},
		{"path in the identifier", CheckIn{Instance: "../../etc/passwd0000000000000000", Version: "1.3.0", OS: "linux", Arch: "amd64", Channel: "stable"}, ErrInvalidInstance},
		{"no version", CheckIn{Instance: instanceID(2), Version: "", OS: "linux", Arch: "amd64", Channel: "stable"}, ErrInvalidVersion},
		{"version is a paragraph", CheckIn{Instance: instanceID(3), Version: "1.3.0 and here is a much longer story", OS: "linux", Arch: "amd64", Channel: "stable"}, ErrInvalidVersion},
		{"no os", CheckIn{Instance: instanceID(4), Version: "1.3.0", OS: "", Arch: "amd64", Channel: "stable"}, ErrInvalidPlatform},
		{"os is a sentence", CheckIn{Instance: instanceID(5), Version: "1.3.0", OS: "linux server 24.04", Arch: "amd64", Channel: "stable"}, ErrInvalidPlatform},
		{"no channel", CheckIn{Instance: instanceID(6), Version: "1.3.0", OS: "linux", Arch: "amd64", Channel: ""}, ErrInvalidChannel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := st.Record(ctx, tc.in, now)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Record(%q) = %v, want %v", tc.in.Instance, err, tc.want)
			}
		})
	}
	if n := countRows(t, st); n != 0 {
		t.Fatalf("table holds %d rows after only malformed check ins, want 0", n)
	}

	// The same store still accepts a well formed check in.
	if err := st.Record(ctx, good, now); err != nil {
		t.Fatalf("Record on a well formed check in: %v", err)
	}
	if n := countRows(t, st); n != 1 {
		t.Fatalf("table holds %d rows, want 1", n)
	}
}
