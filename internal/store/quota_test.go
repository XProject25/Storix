package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// quotaTestStore opens a throwaway database for one test.
func quotaTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "quota.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return st
}

func TestUsageUpsert(t *testing.T) {
	st := quotaTestStore(t)
	ctx := context.Background()

	// An account nobody measured yet answers with the zero value, not an error.
	got, err := st.GetUsage(ctx, 7)
	if err != nil {
		t.Fatalf("GetUsage on empty table: %v", err)
	}
	if got.UserID != 7 || got.Bytes != 0 || got.Files != 0 || !got.ComputedAt.IsZero() {
		t.Fatalf("GetUsage on empty table = %+v, want the zero figure for user 7", got)
	}

	if err := st.SetUsage(ctx, Usage{UserID: 7, Bytes: 4096, Files: 12}); err != nil {
		t.Fatalf("SetUsage: %v", err)
	}
	got, err = st.GetUsage(ctx, 7)
	if err != nil {
		t.Fatalf("GetUsage: %v", err)
	}
	if got.Bytes != 4096 || got.Files != 12 {
		t.Fatalf("GetUsage = %d bytes in %d files, want 4096 in 12", got.Bytes, got.Files)
	}
	// A missing stamp is filled in, so the figure does not read as unmeasured.
	if got.ComputedAt.IsZero() {
		t.Fatal("SetUsage left ComputedAt empty, want the current time")
	}

	// A second write replaces the figure rather than adding a row.
	stamp := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	if err := st.SetUsage(ctx, Usage{UserID: 7, Bytes: 100, Files: 1, ComputedAt: stamp}); err != nil {
		t.Fatalf("SetUsage replace: %v", err)
	}
	got, err = st.GetUsage(ctx, 7)
	if err != nil {
		t.Fatalf("GetUsage after replace: %v", err)
	}
	if got.Bytes != 100 || got.Files != 1 {
		t.Fatalf("GetUsage after replace = %d bytes in %d files, want 100 in 1", got.Bytes, got.Files)
	}
	if !got.ComputedAt.Equal(stamp) {
		t.Fatalf("ComputedAt = %s, want %s", got.ComputedAt, stamp)
	}

	var rows int
	if err := st.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM user_usage").Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("user_usage holds %d rows, want 1", rows)
	}

	if err := st.ClearUsage(ctx, 7); err != nil {
		t.Fatalf("ClearUsage: %v", err)
	}
	if err := st.ClearUsage(ctx, 7); err != nil {
		t.Fatalf("ClearUsage twice: %v", err)
	}
	got, err = st.GetUsage(ctx, 7)
	if err != nil {
		t.Fatalf("GetUsage after clear: %v", err)
	}
	if got.Bytes != 0 || got.Files != 0 {
		t.Fatalf("GetUsage after clear = %+v, want the zero figure", got)
	}
}

func TestAddUsageClampsAtZero(t *testing.T) {
	st := quotaTestStore(t)
	ctx := context.Background()

	// The first delta creates the row and leaves it unmeasured, so a walk is
	// still scheduled later.
	if err := st.AddUsage(ctx, 3, 500, 1); err != nil {
		t.Fatalf("AddUsage insert: %v", err)
	}
	got, err := st.GetUsage(ctx, 3)
	if err != nil {
		t.Fatalf("GetUsage: %v", err)
	}
	if got.Bytes != 500 || got.Files != 1 {
		t.Fatalf("GetUsage = %d bytes in %d files, want 500 in 1", got.Bytes, got.Files)
	}
	if !got.ComputedAt.IsZero() {
		t.Fatalf("ComputedAt = %s, want a delta to leave the figure unmeasured", got.ComputedAt)
	}

	// A negative delta that creates the row must not store a negative figure.
	if err := st.AddUsage(ctx, 4, -900, -3); err != nil {
		t.Fatalf("AddUsage negative insert: %v", err)
	}
	got, err = st.GetUsage(ctx, 4)
	if err != nil {
		t.Fatalf("GetUsage: %v", err)
	}
	if got.Bytes != 0 || got.Files != 0 {
		t.Fatalf("GetUsage = %d bytes in %d files, want 0 in 0", got.Bytes, got.Files)
	}

	// Subtracting more than is there leaves the figure at zero, not below it.
	stamp := time.Now().UTC().Truncate(time.Second)
	if err := st.SetUsage(ctx, Usage{UserID: 3, Bytes: 500, Files: 1, ComputedAt: stamp}); err != nil {
		t.Fatalf("SetUsage: %v", err)
	}
	if err := st.AddUsage(ctx, 3, -5000, -50); err != nil {
		t.Fatalf("AddUsage subtract: %v", err)
	}
	got, err = st.GetUsage(ctx, 3)
	if err != nil {
		t.Fatalf("GetUsage after subtract: %v", err)
	}
	if got.Bytes != 0 || got.Files != 0 {
		t.Fatalf("GetUsage after subtract = %d bytes in %d files, want 0 in 0", got.Bytes, got.Files)
	}
	// An adjustment must not pass itself off as a measurement.
	if !got.ComputedAt.Equal(stamp) {
		t.Fatalf("ComputedAt = %s, want the untouched %s", got.ComputedAt, stamp)
	}

	// A delta of nothing is a no operation.
	if err := st.AddUsage(ctx, 3, 0, 0); err != nil {
		t.Fatalf("AddUsage zero: %v", err)
	}
	if err := st.AddUsage(ctx, 0, 10, 1); err == nil {
		t.Fatal("AddUsage without a user succeeded, want an error")
	}
}

func TestQuotaMigrationOnExistingInstall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")

	// Build the database exactly as version 1.0.1 left it: the original schema
	// and nothing else.
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatalf("set version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open at version 1: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	var version int
	if err := st.DB().QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != len(migrations) {
		t.Fatalf("user_version = %d, want %d", version, len(migrations))
	}
	if err := st.SetUsage(ctx, Usage{UserID: 1, Bytes: 10, Files: 2}); err != nil {
		t.Fatalf("SetUsage after upgrade: %v", err)
	}

	// The upgrade has to survive a restart without running twice.
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer again.Close()
	got, err := again.GetUsage(ctx, 1)
	if err != nil {
		t.Fatalf("GetUsage after reopen: %v", err)
	}
	if got.Bytes != 10 || got.Files != 2 {
		t.Fatalf("GetUsage after reopen = %d bytes in %d files, want 10 in 2", got.Bytes, got.Files)
	}
}
