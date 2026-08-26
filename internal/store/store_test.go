package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stOpen opens a fresh database inside a temporary directory. The handle is
// closed when the test finishes.
func stOpen(t *testing.T) *Store {
	t.Helper()
	return stOpenAt(t, filepath.Join(t.TempDir(), "test.db"))
}

// stOpenAt opens a database at an explicit path, which is what the reopen
// tests need.
func stOpenAt(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// stUser creates an active account and fails the test when it cannot.
func stUser(t *testing.T, s *Store, username string, role Role) *User {
	t.Helper()
	u := &User{
		Username:     username,
		DisplayName:  strings.ToUpper(username[:1]) + username[1:],
		PasswordHash: "hash:" + username,
		Role:         role,
		Active:       true,
	}
	if _, err := s.CreateUser(t.Context(), u); err != nil {
		t.Fatalf("CreateUser(%q): %v", username, err)
	}
	return u
}

// stRawColumn reads a single column straight out of the database, so a test
// can assert on the encoding rather than on the round trip alone.
func stRawColumn(t *testing.T, s *Store, query string, args ...any) string {
	t.Helper()
	var value string
	if err := s.DB().QueryRowContext(t.Context(), query, args...).Scan(&value); err != nil {
		t.Fatalf("read column: %v", err)
	}
	return value
}

func TestStoreOpensAndMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("a fresh database must open: %v", err)
	}

	t.Run("a fresh file is migrated to the current version", func(t *testing.T) {
		var version int
		if err := first.DB().QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil {
			t.Fatalf("read schema version: %v", err)
		}
		if version != len(migrations) {
			t.Fatalf("schema version = %d, want %d, so the migrations did not all run", version, len(migrations))
		}
		for _, table := range []string{"users", "sessions", "shares", "trash_items", "uploads", "jobs", "settings"} {
			var name string
			err := first.DB().QueryRowContext(t.Context(),
				"SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&name)
			if err != nil {
				t.Fatalf("table %q is missing after migration: %v", table, err)
			}
		}
	})

	t.Run("a second handle on the same file is safe", func(t *testing.T) {
		second, err := Open(path)
		if err != nil {
			t.Fatalf("a second Open on a live database must succeed: %v", err)
		}
		defer func() { _ = second.Close() }()

		var version int
		if err := second.DB().QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil {
			t.Fatalf("read schema version: %v", err)
		}
		if version != len(migrations) {
			t.Fatalf("the second Open changed the schema version to %d, want %d", version, len(migrations))
		}
	})

	stUser(t, first, "keeper", RoleAdmin)
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	t.Run("reopening keeps the data and does not re-run migrations", func(t *testing.T) {
		reopened := stOpenAt(t, path)
		var version int
		if err := reopened.DB().QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil {
			t.Fatalf("read schema version: %v", err)
		}
		if version != len(migrations) {
			t.Fatalf("schema version after reopen = %d, want %d", version, len(migrations))
		}
		if _, err := reopened.GetUserByName(t.Context(), "keeper"); err != nil {
			t.Fatalf("an account written before the reopen must still be there: %v", err)
		}
		if reopened.Path() != path {
			t.Fatalf("Path() = %q, want %q", reopened.Path(), path)
		}
		if reopened.Size() <= 0 {
			t.Fatal("Size() must report the file on disk")
		}
	})
}

func TestStoreUsers(t *testing.T) {
	s := stOpen(t)
	ctx := t.Context()

	t.Run("an account is written and read back", func(t *testing.T) {
		u := &User{
			Username:     "ada",
			DisplayName:  "Ada Lovelace",
			Email:        "ada@example.com",
			PasswordHash: "hash:ada",
			Role:         RoleManager,
			Permissions:  []Permission{PermView, PermDownload},
			Active:       true,
			Quota:        1 << 30,
		}
		id, err := s.CreateUser(ctx, u)
		if err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
		if id == 0 || u.ID != id {
			t.Fatalf("CreateUser must report and record the new identifier, got %d and %d", id, u.ID)
		}

		got, err := s.GetUser(ctx, id)
		if err != nil {
			t.Fatalf("GetUser: %v", err)
		}
		if got.Username != "ada" || got.DisplayName != "Ada Lovelace" || got.Email != "ada@example.com" {
			t.Fatalf("the account did not round trip: %+v", got)
		}
		if got.Role != RoleManager || got.Quota != 1<<30 || !got.Active {
			t.Fatalf("role, quota or active state did not round trip: %+v", got)
		}
		if !got.CreatedAt.Equal(u.CreatedAt) {
			t.Fatalf("CreatedAt = %v, want %v", got.CreatedAt, u.CreatedAt)
		}

		byName, err := s.GetUserByName(ctx, "ADA")
		if err != nil {
			t.Fatalf("the username lookup must be case insensitive: %v", err)
		}
		if byName.ID != id {
			t.Fatalf("GetUserByName returned account %d, want %d", byName.ID, id)
		}

		if _, err := s.GetUser(ctx, id+9999); !errors.Is(err, ErrNotFound) {
			t.Fatalf("an unknown identifier must report ErrNotFound, got %v", err)
		}
		if _, err := s.GetUserByName(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("an unknown username must report ErrNotFound, got %v", err)
		}
	})

	t.Run("a username can only be claimed once", func(t *testing.T) {
		if _, err := s.CreateUser(ctx, &User{Username: "ada", PasswordHash: "x", Active: true}); !errors.Is(err, ErrConflict) {
			t.Fatalf("a duplicate username must report ErrConflict, got %v", err)
		}
		if _, err := s.CreateUser(ctx, &User{Username: "Ada", PasswordHash: "x", Active: true}); !errors.Is(err, ErrConflict) {
			t.Fatalf("a duplicate username differing only in case must report ErrConflict, got %v", err)
		}
	})

	t.Run("permissions survive the comma encoding", func(t *testing.T) {
		u := &User{
			Username:     "grace",
			PasswordHash: "hash:grace",
			Role:         RoleCustom,
			// Out of order, duplicated and carrying a name Storix does not know.
			Permissions: []Permission{PermDelete, PermView, PermView, "not-a-permission", PermUpload},
			Active:      true,
		}
		id, err := s.CreateUser(ctx, u)
		if err != nil {
			t.Fatalf("CreateUser: %v", err)
		}

		raw := stRawColumn(t, s, "SELECT permissions FROM users WHERE id = ?", id)
		if raw != "view,upload,delete" {
			t.Fatalf("stored permissions = %q, want %q in canonical order without the unknown entry", raw, "view,upload,delete")
		}

		got, err := s.GetUser(ctx, id)
		if err != nil {
			t.Fatalf("GetUser: %v", err)
		}
		want := []Permission{PermView, PermUpload, PermDelete}
		if len(got.Permissions) != len(want) {
			t.Fatalf("permissions = %v, want %v", got.Permissions, want)
		}
		for i, p := range want {
			if got.Permissions[i] != p {
				t.Fatalf("permissions = %v, want %v", got.Permissions, want)
			}
		}
		if !got.Can(PermDelete) || got.Can(PermSettings) {
			t.Fatalf("Can does not follow the stored set: %v", got.Permissions)
		}

		// Clearing the set has to clear the column too.
		got.Permissions = nil
		if err := s.UpdateUser(ctx, got); err != nil {
			t.Fatalf("UpdateUser: %v", err)
		}
		if raw := stRawColumn(t, s, "SELECT permissions FROM users WHERE id = ?", id); raw != "" {
			t.Fatalf("an empty permission set must store an empty column, got %q", raw)
		}
	})

	t.Run("the listing is ordered by username", func(t *testing.T) {
		stUser(t, s, "zoe", RoleUser)
		stUser(t, s, "Bob", RoleUser)

		users, err := s.ListUsers(ctx)
		if err != nil {
			t.Fatalf("ListUsers: %v", err)
		}
		got := make([]string, 0, len(users))
		for _, u := range users {
			got = append(got, u.Username)
		}
		want := []string{"ada", "Bob", "grace", "zoe"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("ListUsers order = %v, want %v ignoring case", got, want)
		}

		count, err := s.CountUsers(ctx)
		if err != nil {
			t.Fatalf("CountUsers: %v", err)
		}
		if count != len(want) {
			t.Fatalf("CountUsers = %d, want %d", count, len(want))
		}
	})

	t.Run("setting mounts replaces the whole set", func(t *testing.T) {
		u := stUser(t, s, "mounted", RoleUser)

		err := s.SetUserMounts(ctx, u.ID, []Mount{
			{Path: "/srv/media", Label: "Media"},
			{Path: "/srv/docs", Label: "Docs", ReadOnly: true},
			// An empty path and a repeat are skipped rather than stored.
			{Path: "   "},
			{Path: "/srv/media", Label: "Media again"},
		})
		if err != nil {
			t.Fatalf("SetUserMounts: %v", err)
		}

		mounts, err := s.ListUserMounts(ctx, u.ID)
		if err != nil {
			t.Fatalf("ListUserMounts: %v", err)
		}
		if len(mounts) != 2 {
			t.Fatalf("stored %d mounts, want 2 with the blank and the repeat skipped: %+v", len(mounts), mounts)
		}
		for _, m := range mounts {
			if m.UserID != u.ID || m.ID == 0 {
				t.Fatalf("a stored mount must carry its identifiers: %+v", m)
			}
			if m.Icon != "folder" {
				t.Fatalf("a blank icon must default to folder, got %q", m.Icon)
			}
		}

		// The second call is a replacement, not an addition.
		if err := s.SetUserMounts(ctx, u.ID, []Mount{{Path: "/srv/backup", Label: "Backup"}}); err != nil {
			t.Fatalf("SetUserMounts: %v", err)
		}
		mounts, err = s.ListUserMounts(ctx, u.ID)
		if err != nil {
			t.Fatalf("ListUserMounts: %v", err)
		}
		if len(mounts) != 1 || mounts[0].Path != "/srv/backup" {
			t.Fatalf("the mount set was not replaced: %+v", mounts)
		}

		loaded, err := s.GetUser(ctx, u.ID)
		if err != nil {
			t.Fatalf("GetUser: %v", err)
		}
		if len(loaded.Mounts) != 1 || loaded.Mounts[0].Path != "/srv/backup" {
			t.Fatalf("GetUser must carry the mounts: %+v", loaded.Mounts)
		}

		if err := s.SetUserMounts(ctx, u.ID+9999, nil); !errors.Is(err, ErrNotFound) {
			t.Fatalf("mounting an unknown account must report ErrNotFound, got %v", err)
		}
	})

	t.Run("only active administrators are counted", func(t *testing.T) {
		fresh := stOpen(t)
		stUser(t, fresh, "root", RoleAdmin)
		stUser(t, fresh, "helper", RoleManager)

		suspended := stUser(t, fresh, "retired", RoleAdmin)
		if err := fresh.SetUserActive(t.Context(), suspended.ID, false); err != nil {
			t.Fatalf("SetUserActive: %v", err)
		}

		n, err := fresh.CountAdmins(t.Context())
		if err != nil {
			t.Fatalf("CountAdmins: %v", err)
		}
		if n != 1 {
			t.Fatalf("CountAdmins = %d, want 1: a suspended administrator and a manager must not count", n)
		}
	})
}

func TestStoreLoginLockout(t *testing.T) {
	s := stOpen(t)
	ctx := t.Context()
	u := stUser(t, s, "locksmith", RoleUser)

	t.Run("the account locks once the threshold is reached", func(t *testing.T) {
		for attempt := 1; attempt <= 2; attempt++ {
			locked, err := s.RecordFailedLogin(ctx, u.ID, 3, time.Minute)
			if err != nil {
				t.Fatalf("RecordFailedLogin: %v", err)
			}
			if locked {
				t.Fatalf("attempt %d locked the account before the threshold of 3", attempt)
			}
		}
		locked, err := s.RecordFailedLogin(ctx, u.ID, 3, time.Minute)
		if err != nil {
			t.Fatalf("RecordFailedLogin: %v", err)
		}
		if !locked {
			t.Fatal("the third failure must lock the account at a threshold of 3")
		}

		got, err := s.GetUser(ctx, u.ID)
		if err != nil {
			t.Fatalf("GetUser: %v", err)
		}
		if got.FailedLogins != 3 {
			t.Fatalf("FailedLogins = %d, want 3", got.FailedLogins)
		}
		if got.LockedUntil == nil || !got.LockedUntil.After(time.Now()) {
			t.Fatalf("LockedUntil = %v, want an instant in the future", got.LockedUntil)
		}
	})

	t.Run("a successful sign in clears the lock", func(t *testing.T) {
		if err := s.RecordLogin(ctx, u.ID, "203.0.113.7"); err != nil {
			t.Fatalf("RecordLogin: %v", err)
		}
		got, err := s.GetUser(ctx, u.ID)
		if err != nil {
			t.Fatalf("GetUser: %v", err)
		}
		if got.FailedLogins != 0 {
			t.Fatalf("FailedLogins = %d, want 0 after a successful sign in", got.FailedLogins)
		}
		if got.LockedUntil != nil {
			t.Fatalf("LockedUntil = %v, want the lock cleared", got.LockedUntil)
		}
		if got.LastLoginAt == nil || got.LastLoginIP != "203.0.113.7" {
			t.Fatalf("the sign in was not recorded: %v from %q", got.LastLoginAt, got.LastLoginIP)
		}
	})

	t.Run("a threshold of zero only counts", func(t *testing.T) {
		other := stUser(t, s, "counter", RoleUser)
		locked, err := s.RecordFailedLogin(ctx, other.ID, 0, 0)
		if err != nil {
			t.Fatalf("RecordFailedLogin: %v", err)
		}
		if locked {
			t.Fatal("a threshold of zero must never lock an account")
		}
		got, err := s.GetUser(ctx, other.ID)
		if err != nil {
			t.Fatalf("GetUser: %v", err)
		}
		if got.FailedLogins != 1 || got.LockedUntil != nil {
			t.Fatalf("the attempt must still be counted without a lock: %d, %v", got.FailedLogins, got.LockedUntil)
		}
	})

	t.Run("an unknown account reports not found", func(t *testing.T) {
		if _, err := s.RecordFailedLogin(ctx, 999999, 3, time.Minute); !errors.Is(err, ErrNotFound) {
			t.Fatalf("RecordFailedLogin on an unknown account must report ErrNotFound, got %v", err)
		}
	})
}

func TestStoreSessions(t *testing.T) {
	s := stOpen(t)
	ctx := t.Context()
	u := stUser(t, s, "browser", RoleUser)
	now := time.Now().UTC().Truncate(time.Second)

	t.Run("a session is written and read back", func(t *testing.T) {
		sess := &Session{
			ID:        "session-live",
			UserID:    u.ID,
			CSRF:      "csrf-token",
			IP:        "198.51.100.4",
			UserAgent: "Storix test",
			ExpiresAt: now.Add(time.Hour),
		}
		if err := s.CreateSession(ctx, sess); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if sess.CreatedAt.IsZero() || sess.LastSeenAt.IsZero() {
			t.Fatal("CreateSession must stamp the creation and last seen times")
		}

		got, err := s.GetSession(ctx, "session-live")
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		if got.UserID != u.ID || got.CSRF != "csrf-token" || got.IP != "198.51.100.4" {
			t.Fatalf("the session did not round trip: %+v", got)
		}

		if err := s.CreateSession(ctx, &Session{ID: "session-live", UserID: u.ID}); !errors.Is(err, ErrConflict) {
			t.Fatalf("a reused session identifier must report ErrConflict, got %v", err)
		}
	})

	t.Run("an expired session is never handed out", func(t *testing.T) {
		expired := &Session{ID: "session-expired", UserID: u.ID, ExpiresAt: now.Add(-time.Minute)}
		if err := s.CreateSession(ctx, expired); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if _, err := s.GetSession(ctx, "session-expired"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("an expired cookie must not resolve to a session, got %v", err)
		}
		if _, err := s.GetSession(ctx, "no-such-session"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("an unknown session must report ErrNotFound, got %v", err)
		}

		live, err := s.ListUserSessions(ctx, u.ID)
		if err != nil {
			t.Fatalf("ListUserSessions: %v", err)
		}
		for _, sess := range live {
			if sess.ID == "session-expired" {
				t.Fatal("the listing must leave expired sessions out")
			}
		}
	})

	t.Run("purging removes only the expired rows", func(t *testing.T) {
		n, err := s.PurgeExpiredSessions(ctx, now)
		if err != nil {
			t.Fatalf("PurgeExpiredSessions: %v", err)
		}
		if n != 1 {
			t.Fatalf("PurgeExpiredSessions removed %d rows, want 1", n)
		}
		if _, err := s.GetSession(ctx, "session-live"); err != nil {
			t.Fatalf("the live session must survive the purge: %v", err)
		}
	})

	t.Run("signing out elsewhere keeps the current session", func(t *testing.T) {
		for _, id := range []string{"session-phone", "session-tablet"} {
			if err := s.CreateSession(ctx, &Session{ID: id, UserID: u.ID, ExpiresAt: now.Add(time.Hour)}); err != nil {
				t.Fatalf("CreateSession(%q): %v", id, err)
			}
		}
		if err := s.DeleteOtherUserSessions(ctx, u.ID, "session-live"); err != nil {
			t.Fatalf("DeleteOtherUserSessions: %v", err)
		}

		live, err := s.ListUserSessions(ctx, u.ID)
		if err != nil {
			t.Fatalf("ListUserSessions: %v", err)
		}
		if len(live) != 1 || live[0].ID != "session-live" {
			t.Fatalf("only the named session may survive, got %+v", live)
		}

		if err := s.DeleteSession(ctx, "session-live"); err != nil {
			t.Fatalf("DeleteSession: %v", err)
		}
		if err := s.DeleteSession(ctx, "session-live"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("deleting a session twice must report ErrNotFound, got %v", err)
		}
	})
}

func TestStoreShares(t *testing.T) {
	s := stOpen(t)
	ctx := t.Context()
	owner := stUser(t, s, "publisher", RoleManager)
	now := time.Now().UTC().Truncate(time.Second)

	t.Run("a link is written and read back by token", func(t *testing.T) {
		sh := &Share{
			Token:         "tok-plain",
			OwnerID:       owner.ID,
			Path:          "/data/reports/q1.pdf",
			Name:          "q1.pdf",
			Kind:          ShareDownload,
			AllowDownload: true,
			AllowList:     true,
			Note:          "Quarterly figures",
		}
		if _, err := s.CreateShare(ctx, sh); err != nil {
			t.Fatalf("CreateShare: %v", err)
		}

		got, err := s.GetShareByToken(ctx, "tok-plain")
		if err != nil {
			t.Fatalf("GetShareByToken: %v", err)
		}
		if got.ID != sh.ID || got.Path != sh.Path || got.Note != sh.Note {
			t.Fatalf("the share did not round trip: %+v", got)
		}
		if got.OwnerName != owner.DisplayName {
			t.Fatalf("OwnerName = %q, want the owner display name %q", got.OwnerName, owner.DisplayName)
		}
		if !got.AllowDownload || !got.AllowList || got.AllowUpload {
			t.Fatalf("the permission flags did not round trip: %+v", got)
		}

		if _, err := s.CreateShare(ctx, &Share{Token: "tok-plain", OwnerID: owner.ID, Path: "/data"}); !errors.Is(err, ErrConflict) {
			t.Fatalf("a reused token must report ErrConflict, got %v", err)
		}
		if _, err := s.GetShareByToken(ctx, "tok-unknown"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("an unknown token must report ErrNotFound, got %v", err)
		}
	})

	t.Run("the password flag follows the stored hash", func(t *testing.T) {
		open, err := s.GetShareByToken(ctx, "tok-plain")
		if err != nil {
			t.Fatalf("GetShareByToken: %v", err)
		}
		if open.HasPassword {
			t.Fatal("a link without a hash must not claim to be password protected")
		}

		locked := &Share{
			Token:        "tok-locked",
			OwnerID:      owner.ID,
			Path:         "/data/private",
			Name:         "private",
			IsDir:        true,
			PasswordHash: "argon2id$fake",
		}
		if _, err := s.CreateShare(ctx, locked); err != nil {
			t.Fatalf("CreateShare: %v", err)
		}
		if !locked.HasPassword {
			t.Fatal("CreateShare must derive HasPassword from the hash it stored")
		}
		got, err := s.GetShareByToken(ctx, "tok-locked")
		if err != nil {
			t.Fatalf("GetShareByToken: %v", err)
		}
		if !got.HasPassword || !got.IsDir {
			t.Fatalf("the password flag and folder flag did not round trip: %+v", got)
		}

		// Clearing the hash lifts the flag again.
		got.PasswordHash = ""
		if err := s.UpdateShare(ctx, got); err != nil {
			t.Fatalf("UpdateShare: %v", err)
		}
		reloaded, err := s.GetShareByToken(ctx, "tok-locked")
		if err != nil {
			t.Fatalf("GetShareByToken: %v", err)
		}
		if reloaded.HasPassword {
			t.Fatal("clearing the hash must clear the password flag")
		}
	})

	t.Run("expiry is reported and purged", func(t *testing.T) {
		past := now.Add(-time.Hour)
		future := now.Add(time.Hour)
		stale := &Share{Token: "tok-stale", OwnerID: owner.ID, Path: "/data/stale.txt", ExpiresAt: &past}
		fresh := &Share{Token: "tok-fresh", OwnerID: owner.ID, Path: "/data/fresh.txt", ExpiresAt: &future}
		for _, sh := range []*Share{stale, fresh} {
			if _, err := s.CreateShare(ctx, sh); err != nil {
				t.Fatalf("CreateShare(%q): %v", sh.Token, err)
			}
		}

		// Expiry is not evaluated by the lookup: the caller decides.
		got, err := s.GetShareByToken(ctx, "tok-stale")
		if err != nil {
			t.Fatalf("an expired link must still be readable so the caller can explain it: %v", err)
		}
		if !got.Expired(now) {
			t.Fatal("a link past its expiry must report Expired")
		}
		alive, err := s.GetShareByToken(ctx, "tok-fresh")
		if err != nil {
			t.Fatalf("GetShareByToken: %v", err)
		}
		if alive.Expired(now) {
			t.Fatal("a link inside its window must not report Expired")
		}

		n, err := s.PurgeExpiredShares(ctx, now)
		if err != nil {
			t.Fatalf("PurgeExpiredShares: %v", err)
		}
		if n != 1 {
			t.Fatalf("PurgeExpiredShares removed %d links, want only the expired one", n)
		}
		if _, err := s.GetShareByToken(ctx, "tok-fresh"); err != nil {
			t.Fatalf("a live link must survive the purge: %v", err)
		}
	})

	t.Run("a visit can count a download", func(t *testing.T) {
		sh := &Share{Token: "tok-counted", OwnerID: owner.ID, Path: "/data/counted.zip", MaxDownloads: 2}
		if _, err := s.CreateShare(ctx, sh); err != nil {
			t.Fatalf("CreateShare: %v", err)
		}

		// A listing visit records the access without spending the budget.
		if err := s.TouchShare(ctx, sh.ID, false); err != nil {
			t.Fatalf("TouchShare: %v", err)
		}
		got, err := s.GetShare(ctx, sh.ID)
		if err != nil {
			t.Fatalf("GetShare: %v", err)
		}
		if got.Downloads != 0 {
			t.Fatalf("Downloads = %d, want 0 when the visit is not a download", got.Downloads)
		}
		if got.LastAccessAt == nil {
			t.Fatal("TouchShare must record the access time")
		}

		for i := 0; i < 2; i++ {
			if err := s.TouchShare(ctx, sh.ID, true); err != nil {
				t.Fatalf("TouchShare: %v", err)
			}
		}
		got, err = s.GetShare(ctx, sh.ID)
		if err != nil {
			t.Fatalf("GetShare: %v", err)
		}
		if got.Downloads != 2 {
			t.Fatalf("Downloads = %d, want 2", got.Downloads)
		}
		if !got.Expired(now) {
			t.Fatal("a link that spent its download budget must report Expired")
		}

		if err := s.TouchShare(ctx, sh.ID+9999, true); !errors.Is(err, ErrNotFound) {
			t.Fatalf("touching an unknown link must report ErrNotFound, got %v", err)
		}
	})

	t.Run("deleting a subtree revokes the links below it", func(t *testing.T) {
		fresh := stOpen(t)
		freshOwner := stUser(t, fresh, "prefix", RoleManager)
		paths := map[string]string{
			"tok-dir":     "/data/reports",
			"tok-child":   "/data/reports/q1.pdf",
			"tok-deep":    "/data/reports/2024/q2.pdf",
			"tok-sibling": "/data/reports-archive/old.pdf",
			"tok-other":   "/data/other.txt",
		}
		for token, p := range paths {
			if _, err := fresh.CreateShare(t.Context(), &Share{Token: token, OwnerID: freshOwner.ID, Path: p}); err != nil {
				t.Fatalf("CreateShare(%q): %v", token, err)
			}
		}

		n, err := fresh.DeleteSharesUnder(t.Context(), 0, "/data/reports")
		if err != nil {
			t.Fatalf("DeleteSharesUnder: %v", err)
		}
		if n != 3 {
			t.Fatalf("DeleteSharesUnder removed %d links, want the folder and its two children", n)
		}
		for _, token := range []string{"tok-sibling", "tok-other"} {
			if _, err := fresh.GetShareByToken(t.Context(), token); err != nil {
				t.Fatalf("%q sits outside the deleted subtree and must survive: %v", token, err)
			}
		}
		if _, err := fresh.GetShareByToken(t.Context(), "tok-child"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a link below the deleted folder must be gone, got %v", err)
		}

		if n, err := fresh.DeleteSharesUnder(t.Context(), 0, "   "); err != nil || n != 0 {
			t.Fatalf("an empty prefix must delete nothing, got %d and %v", n, err)
		}

		count, err := fresh.CountShares(t.Context(), freshOwner.ID)
		if err != nil {
			t.Fatalf("CountShares: %v", err)
		}
		if count != 2 {
			t.Fatalf("CountShares = %d, want 2", count)
		}
	})
}

func TestStoreTrash(t *testing.T) {
	s := stOpen(t)
	ctx := t.Context()
	u := stUser(t, s, "deleter", RoleUser)
	now := time.Now().UTC().Truncate(time.Second)

	t.Run("entries are listed newest first", func(t *testing.T) {
		for i, name := range []string{"oldest.txt", "middle.txt", "newest.txt"} {
			item := &TrashItem{
				UserID:       u.ID,
				Name:         name,
				OriginalPath: "/data/" + name,
				StoredPath:   "/var/lib/storix/trash/" + name,
				Size:         int64(100 * (i + 1)),
				DeletedAt:    now.Add(time.Duration(i) * time.Minute),
				ExpiresAt:    now.Add(24 * time.Hour),
			}
			if _, err := s.AddTrashItem(ctx, item); err != nil {
				t.Fatalf("AddTrashItem(%q): %v", name, err)
			}
			if item.ID == 0 {
				t.Fatalf("AddTrashItem must write the identifier back into the item")
			}
		}

		items, err := s.ListTrash(ctx, u.ID, 0)
		if err != nil {
			t.Fatalf("ListTrash: %v", err)
		}
		if len(items) != 3 {
			t.Fatalf("the bin holds %d entries, want 3", len(items))
		}
		want := []string{"newest.txt", "middle.txt", "oldest.txt"}
		for i, name := range want {
			if items[i].Name != name {
				t.Fatalf("the bin must list the most recent deletion first, got %q at position %d, want %q",
					items[i].Name, i, name)
			}
		}

		got, err := s.GetTrashItem(ctx, items[0].ID)
		if err != nil {
			t.Fatalf("GetTrashItem: %v", err)
		}
		if got.StoredPath != "/var/lib/storix/trash/newest.txt" || got.OriginalPath != "/data/newest.txt" {
			t.Fatalf("the entry did not round trip: %+v", got)
		}

		count, bytes, err := s.TrashStats(ctx, u.ID)
		if err != nil {
			t.Fatalf("TrashStats: %v", err)
		}
		if count != 3 || bytes != 600 {
			t.Fatalf("TrashStats = %d entries and %d bytes, want 3 and 600", count, bytes)
		}
	})

	t.Run("emptying the bin claims and removes in one step", func(t *testing.T) {
		claimed, err := s.ClaimTrash(ctx, u.ID)
		if err != nil {
			t.Fatalf("ClaimTrash: %v", err)
		}
		if len(claimed) != 3 {
			t.Fatalf("ClaimTrash returned %d entries, want all 3 so the caller can erase them", len(claimed))
		}
		for _, item := range claimed {
			if item.StoredPath == "" {
				t.Fatalf("a claimed entry must carry the location of its data: %+v", item)
			}
		}

		again, err := s.ClaimTrash(ctx, u.ID)
		if err != nil {
			t.Fatalf("ClaimTrash: %v", err)
		}
		if len(again) != 0 {
			t.Fatalf("an emptied bin must not be claimable twice, got %d entries", len(again))
		}

		count, bytes, err := s.TrashStats(ctx, u.ID)
		if err != nil {
			t.Fatalf("TrashStats: %v", err)
		}
		if count != 0 || bytes != 0 {
			t.Fatalf("TrashStats = %d entries and %d bytes, want an empty bin", count, bytes)
		}
	})

	t.Run("expired entries are offered to the housekeeper", func(t *testing.T) {
		keep := &TrashItem{UserID: u.ID, Name: "keep.txt", OriginalPath: "/data/keep.txt",
			StoredPath: "/trash/keep.txt", DeletedAt: now, ExpiresAt: now.Add(time.Hour)}
		gone := &TrashItem{UserID: u.ID, Name: "gone.txt", OriginalPath: "/data/gone.txt",
			StoredPath: "/trash/gone.txt", DeletedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour)}
		for _, item := range []*TrashItem{keep, gone} {
			if _, err := s.AddTrashItem(ctx, item); err != nil {
				t.Fatalf("AddTrashItem: %v", err)
			}
		}

		expired, err := s.ListExpiredTrash(ctx, now)
		if err != nil {
			t.Fatalf("ListExpiredTrash: %v", err)
		}
		if len(expired) != 1 || expired[0].Name != "gone.txt" {
			t.Fatalf("only the entry past its retention window may be listed, got %+v", expired)
		}

		if err := s.DeleteTrashItem(ctx, gone.ID); err != nil {
			t.Fatalf("DeleteTrashItem: %v", err)
		}
		if err := s.DeleteTrashItem(ctx, gone.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("deleting the same entry twice must report ErrNotFound, got %v", err)
		}
	})
}

func TestStoreFavoritesAndRecents(t *testing.T) {
	s := stOpen(t)
	ctx := t.Context()
	u := stUser(t, s, "pinner", RoleUser)
	now := time.Now().UTC().Truncate(time.Second)

	t.Run("pinning the same path twice keeps the original pin", func(t *testing.T) {
		first := &Favorite{UserID: u.ID, Path: "/data/reports", Name: "Reports", IsDir: true,
			CreatedAt: now.Add(-time.Hour)}
		id, err := s.AddFavorite(ctx, first)
		if err != nil {
			t.Fatalf("AddFavorite: %v", err)
		}

		second := &Favorite{UserID: u.ID, Path: "/data/reports", Name: "Reports 2024", IsDir: true,
			CreatedAt: now}
		againID, err := s.AddFavorite(ctx, second)
		if err != nil {
			t.Fatalf("AddFavorite: %v", err)
		}
		if againID != id {
			t.Fatalf("pinning the same path must keep identifier %d, got %d", id, againID)
		}
		if !second.CreatedAt.Equal(first.CreatedAt) {
			t.Fatalf("the original pin time %v must survive, got %v", first.CreatedAt, second.CreatedAt)
		}

		pins, err := s.ListFavorites(ctx, u.ID)
		if err != nil {
			t.Fatalf("ListFavorites: %v", err)
		}
		if len(pins) != 1 || pins[0].Name != "Reports 2024" {
			t.Fatalf("the label must be refreshed without adding a second pin: %+v", pins)
		}

		yes, err := s.IsFavorite(ctx, u.ID, "/data/reports")
		if err != nil || !yes {
			t.Fatalf("IsFavorite = %v, %v, want true", yes, err)
		}
		no, err := s.IsFavorite(ctx, u.ID, "/data/nothing")
		if err != nil || no {
			t.Fatalf("IsFavorite = %v, %v, want false for an unpinned path", no, err)
		}

		if err := s.RemoveFavorite(ctx, u.ID, "/data/reports"); err != nil {
			t.Fatalf("RemoveFavorite: %v", err)
		}
		if err := s.RemoveFavorite(ctx, u.ID, "/data/reports"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unpinning twice must report ErrNotFound, got %v", err)
		}
	})

	t.Run("revisiting a file moves it to the top instead of duplicating it", func(t *testing.T) {
		open := &Recent{UserID: u.ID, Path: "/data/a.txt", Name: "a.txt", Action: "open", At: now.Add(-2 * time.Minute)}
		if err := s.TouchRecent(ctx, open); err != nil {
			t.Fatalf("TouchRecent: %v", err)
		}
		other := &Recent{UserID: u.ID, Path: "/data/b.txt", Name: "b.txt", Action: "open", At: now.Add(-time.Minute)}
		if err := s.TouchRecent(ctx, other); err != nil {
			t.Fatalf("TouchRecent: %v", err)
		}
		again := &Recent{UserID: u.ID, Path: "/data/a.txt", Name: "a.txt", Action: "edit", Size: 42, At: now}
		if err := s.TouchRecent(ctx, again); err != nil {
			t.Fatalf("TouchRecent: %v", err)
		}
		if again.ID != open.ID {
			t.Fatalf("revisiting a path must reuse row %d, got %d", open.ID, again.ID)
		}

		history, err := s.ListRecents(ctx, u.ID, 0)
		if err != nil {
			t.Fatalf("ListRecents: %v", err)
		}
		if len(history) != 2 {
			t.Fatalf("the history holds %d entries, want one row per path", len(history))
		}
		if history[0].Path != "/data/a.txt" || history[0].Action != "edit" || history[0].Size != 42 {
			t.Fatalf("the revisited file must lead the history with its new details: %+v", history[0])
		}

		if err := s.RemoveRecent(ctx, u.ID, "/data/b.txt"); err != nil {
			t.Fatalf("RemoveRecent: %v", err)
		}
		if err := s.RemoveRecent(ctx, u.ID, "/data/b.txt"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("removing the same entry twice must report ErrNotFound, got %v", err)
		}
	})

	t.Run("trimming keeps the newest entries", func(t *testing.T) {
		fresh := stOpen(t)
		other := stUser(t, fresh, "historian", RoleUser)
		for i := 0; i < 5; i++ {
			r := &Recent{
				UserID: other.ID,
				Path:   "/data/file-" + string(rune('a'+i)) + ".txt",
				At:     now.Add(time.Duration(i) * time.Minute),
			}
			if err := fresh.TouchRecent(t.Context(), r); err != nil {
				t.Fatalf("TouchRecent: %v", err)
			}
		}

		if err := fresh.TrimRecents(t.Context(), other.ID, 2); err != nil {
			t.Fatalf("TrimRecents: %v", err)
		}
		history, err := fresh.ListRecents(t.Context(), other.ID, 0)
		if err != nil {
			t.Fatalf("ListRecents: %v", err)
		}
		if len(history) != 2 {
			t.Fatalf("the history holds %d entries after trimming to 2", len(history))
		}
		if history[0].Path != "/data/file-e.txt" || history[1].Path != "/data/file-d.txt" {
			t.Fatalf("trimming must keep the two newest entries, got %q and %q",
				history[0].Path, history[1].Path)
		}

		if err := fresh.TrimRecents(t.Context(), other.ID, 0); err != nil {
			t.Fatalf("TrimRecents: %v", err)
		}
		history, err = fresh.ListRecents(t.Context(), other.ID, 0)
		if err != nil {
			t.Fatalf("ListRecents: %v", err)
		}
		if len(history) != 0 {
			t.Fatalf("trimming to zero must clear the history, %d entries left", len(history))
		}
	})

	t.Run("a pin follows the folder it points at", func(t *testing.T) {
		fresh := stOpen(t)
		mover := stUser(t, fresh, "mover", RoleUser)
		pins := []*Favorite{
			{UserID: mover.ID, Path: "/data/old", Name: "old", IsDir: true},
			{UserID: mover.ID, Path: "/data/old/deep", Name: "deep", IsDir: true},
			// A stale pin already sitting at the destination.
			{UserID: mover.ID, Path: "/data/new", Name: "stale", IsDir: true},
			// An unrelated pin that shares a prefix by text only.
			{UserID: mover.ID, Path: "/data/older", Name: "older", IsDir: true},
		}
		for _, f := range pins {
			if _, err := fresh.AddFavorite(t.Context(), f); err != nil {
				t.Fatalf("AddFavorite(%q): %v", f.Path, err)
			}
		}

		if err := fresh.RenameFavorites(t.Context(), 0, "/data/old", "/data/new"); err != nil {
			t.Fatalf("RenameFavorites: %v", err)
		}

		got, err := fresh.ListFavorites(t.Context(), mover.ID)
		if err != nil {
			t.Fatalf("ListFavorites: %v", err)
		}
		byPath := make(map[string]string, len(got))
		for _, f := range got {
			byPath[f.Path] = f.Name
		}
		if len(got) != 3 {
			t.Fatalf("the move must leave 3 pins, got %d: %v", len(got), byPath)
		}
		if name, ok := byPath["/data/new"]; !ok || name != "new" {
			t.Fatalf("the moved folder must be pinned at its new path with its new label, got %q", name)
		}
		if _, ok := byPath["/data/new/deep"]; !ok {
			t.Fatalf("a pin below the moved folder must follow it, got %v", byPath)
		}
		if _, ok := byPath["/data/older"]; !ok {
			t.Fatalf("a path that only shares the prefix text must not move, got %v", byPath)
		}
	})
}

func TestStoreSettings(t *testing.T) {
	s := stOpen(t)
	ctx := t.Context()

	t.Run("strings round trip", func(t *testing.T) {
		if value, err := s.GetSetting(ctx, "never.set"); err != nil || value != "" {
			t.Fatalf("an unset key must read as empty with no error, got %q and %v", value, err)
		}
		if err := s.SetSetting(ctx, SettingUpdateChannel, "stable"); err != nil {
			t.Fatalf("SetSetting: %v", err)
		}
		if value, err := s.GetSetting(ctx, SettingUpdateChannel); err != nil || value != "stable" {
			t.Fatalf("GetSetting = %q, %v, want stable", value, err)
		}
		// Writing again replaces rather than duplicating.
		if err := s.SetSetting(ctx, SettingUpdateChannel, "beta"); err != nil {
			t.Fatalf("SetSetting: %v", err)
		}
		if value, err := s.GetSetting(ctx, SettingUpdateChannel); err != nil || value != "beta" {
			t.Fatalf("GetSetting = %q, %v, want beta", value, err)
		}

		all, err := s.AllSettings(ctx)
		if err != nil {
			t.Fatalf("AllSettings: %v", err)
		}
		if all[SettingUpdateChannel] != "beta" {
			t.Fatalf("AllSettings must carry every stored key: %v", all)
		}

		if err := s.DeleteSetting(ctx, SettingUpdateChannel); err != nil {
			t.Fatalf("DeleteSetting: %v", err)
		}
		if err := s.DeleteSetting(ctx, SettingUpdateChannel); err != nil {
			t.Fatalf("deleting a key that is already gone must not be an error: %v", err)
		}
		if err := s.SetSetting(ctx, "   ", "value"); err == nil {
			t.Fatal("a blank key must be refused")
		}
	})

	t.Run("booleans round trip", func(t *testing.T) {
		if s.SetupCompleted(ctx) {
			t.Fatal("a fresh install must report that setup is still required")
		}
		if err := s.MarkSetupCompleted(ctx); err != nil {
			t.Fatalf("MarkSetupCompleted: %v", err)
		}
		if !s.SetupCompleted(ctx) {
			t.Fatal("setup must stay marked as completed")
		}
		if raw := stRawColumn(t, s, "SELECT value FROM settings WHERE key = ?", SettingSetupCompleted); raw != "1" {
			t.Fatalf("a true boolean is stored as %q, want \"1\"", raw)
		}

		if err := s.SetBool(ctx, "feature.flag", false); err != nil {
			t.Fatalf("SetBool: %v", err)
		}
		if s.GetBool(ctx, "feature.flag", true) {
			t.Fatal("a stored false must beat the default")
		}
		if !s.GetBool(ctx, "feature.missing", true) {
			t.Fatal("an unset key must fall back to the default")
		}
		if err := s.SetSetting(ctx, "feature.garbled", "perhaps"); err != nil {
			t.Fatalf("SetSetting: %v", err)
		}
		if !s.GetBool(ctx, "feature.garbled", true) {
			t.Fatal("a value that is not a boolean must fall back to the default")
		}
	})

	t.Run("json round trips", func(t *testing.T) {
		found, err := s.GetJSON(ctx, SettingBranding, &Branding{})
		if err != nil {
			t.Fatalf("GetJSON: %v", err)
		}
		if found {
			t.Fatal("an unset key must report that nothing was decoded")
		}

		want := DefaultBranding()
		want.Tagline = "Fast. Secure. Powerful."
		if err := s.SetJSON(ctx, SettingBranding, want); err != nil {
			t.Fatalf("SetJSON: %v", err)
		}
		var got Branding
		found, err = s.GetJSON(ctx, SettingBranding, &got)
		if err != nil || !found {
			t.Fatalf("GetJSON = %v, %v, want a decoded value", found, err)
		}
		if got != want {
			t.Fatalf("branding did not round trip: %+v, want %+v", got, want)
		}
		if got.Footer != "Developed by X Project" {
			t.Fatalf("Footer = %q, want the X Project attribution", got.Footer)
		}

		if err := s.SetSetting(ctx, "broken.json", "{not json"); err != nil {
			t.Fatalf("SetSetting: %v", err)
		}
		if _, err := s.GetJSON(ctx, "broken.json", &got); err == nil {
			t.Fatal("undecodable JSON must be reported rather than silently ignored")
		}
	})

	t.Run("the instance identifier is generated once", func(t *testing.T) {
		id, err := s.InstanceID(ctx)
		if err != nil {
			t.Fatalf("InstanceID: %v", err)
		}
		if len(id) != 32 {
			t.Fatalf("InstanceID = %q, want 32 hexadecimal characters", id)
		}
		for _, c := range id {
			if !strings.ContainsRune("0123456789abcdef", c) {
				t.Fatalf("InstanceID = %q, want hexadecimal only", id)
			}
		}
		again, err := s.InstanceID(ctx)
		if err != nil {
			t.Fatalf("InstanceID: %v", err)
		}
		if again != id {
			t.Fatalf("the instance identifier must be stable, got %q then %q", id, again)
		}
	})
}

func TestStoreJobs(t *testing.T) {
	s := stOpen(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	t.Run("a job is written and updated", func(t *testing.T) {
		j := &Job{
			ID:          "job-copy",
			UserID:      7,
			Type:        "copy",
			Title:       "Copying 3 items",
			Total:       900,
			TotalItems:  3,
			Cancellable: true,
			CreatedAt:   now,
		}
		if err := s.CreateJob(ctx, j); err != nil {
			t.Fatalf("CreateJob: %v", err)
		}
		if j.Status != JobQueued {
			t.Fatalf("Status = %q, want a new job to start queued", j.Status)
		}
		if err := s.CreateJob(ctx, &Job{ID: "job-copy", Type: "copy"}); !errors.Is(err, ErrConflict) {
			t.Fatalf("a reused job identifier must report ErrConflict, got %v", err)
		}

		started := now.Add(time.Second)
		j.Status = JobRunning
		j.Message = "Copying reports"
		j.Done = 450
		j.DoneItems = 1
		j.StartedAt = &started
		if err := s.UpdateJob(ctx, j); err != nil {
			t.Fatalf("UpdateJob: %v", err)
		}

		got, err := s.GetJob(ctx, "job-copy")
		if err != nil {
			t.Fatalf("GetJob: %v", err)
		}
		if got.Status != JobRunning || got.Done != 450 || got.Message != "Copying reports" {
			t.Fatalf("the update did not round trip: %+v", got)
		}
		if got.StartedAt == nil || !got.StartedAt.Equal(started) {
			t.Fatalf("StartedAt = %v, want %v", got.StartedAt, started)
		}
		if got.Percent() != 50 {
			t.Fatalf("Percent() = %v, want 50", got.Percent())
		}

		if _, err := s.GetJob(ctx, "job-unknown"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("an unknown job must report ErrNotFound, got %v", err)
		}
		if err := s.UpdateJob(ctx, &Job{ID: "job-unknown", Type: "copy"}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("updating an unknown job must report ErrNotFound, got %v", err)
		}
	})

	t.Run("the listing is newest first", func(t *testing.T) {
		for i, id := range []string{"job-older", "job-newer"} {
			j := &Job{ID: id, UserID: 7, Type: "move", CreatedAt: now.Add(time.Duration(i+1) * time.Minute)}
			if err := s.CreateJob(ctx, j); err != nil {
				t.Fatalf("CreateJob(%q): %v", id, err)
			}
		}
		// Another user's job must not leak into a per user listing.
		if err := s.CreateJob(ctx, &Job{ID: "job-stranger", UserID: 99, Type: "move", CreatedAt: now.Add(time.Hour)}); err != nil {
			t.Fatalf("CreateJob: %v", err)
		}

		jobs, err := s.ListJobs(ctx, 7, 0)
		if err != nil {
			t.Fatalf("ListJobs: %v", err)
		}
		if len(jobs) != 3 {
			t.Fatalf("ListJobs returned %d jobs for the owner, want 3", len(jobs))
		}
		if jobs[0].ID != "job-newer" || jobs[1].ID != "job-older" || jobs[2].ID != "job-copy" {
			t.Fatalf("jobs must be listed newest first, got %q, %q, %q",
				jobs[0].ID, jobs[1].ID, jobs[2].ID)
		}

		all, err := s.ListJobs(ctx, 0, 0)
		if err != nil {
			t.Fatalf("ListJobs: %v", err)
		}
		if len(all) != 4 {
			t.Fatalf("a userID of zero must list every job, got %d", len(all))
		}
		if limited, err := s.ListJobs(ctx, 0, 2); err != nil || len(limited) != 2 {
			t.Fatalf("ListJobs honoured the limit as %d jobs and %v, want 2", len(limited), err)
		}
	})

	t.Run("jobs orphaned by a restart are failed", func(t *testing.T) {
		fresh := stOpen(t)
		finished := now.Add(-time.Hour)
		seed := []*Job{
			{ID: "queued", Type: "copy", Status: JobQueued, Cancellable: true, CreatedAt: now},
			{ID: "running", Type: "move", Status: JobRunning, Cancellable: true, CreatedAt: now},
			{ID: "done", Type: "zip", Status: JobDone, CreatedAt: now, FinishedAt: &finished},
		}
		for _, j := range seed {
			if err := fresh.CreateJob(t.Context(), j); err != nil {
				t.Fatalf("CreateJob(%q): %v", j.ID, err)
			}
		}

		n, err := fresh.FailStaleJobs(t.Context(), "")
		if err != nil {
			t.Fatalf("FailStaleJobs: %v", err)
		}
		if n != 2 {
			t.Fatalf("FailStaleJobs marked %d jobs, want the queued one and the running one", n)
		}

		for _, id := range []string{"queued", "running"} {
			got, err := fresh.GetJob(t.Context(), id)
			if err != nil {
				t.Fatalf("GetJob(%q): %v", id, err)
			}
			if got.Status != JobFailed {
				t.Fatalf("job %q has status %q, want failed after a restart", id, got.Status)
			}
			if got.Error != staleJobMessage {
				t.Fatalf("job %q has error %q, want the default explanation", id, got.Error)
			}
			if got.Cancellable {
				t.Fatalf("job %q must no longer be cancellable once it failed", id)
			}
			if got.FinishedAt == nil {
				t.Fatalf("job %q must be stamped as finished", id)
			}
		}

		done, err := fresh.GetJob(t.Context(), "done")
		if err != nil {
			t.Fatalf("GetJob: %v", err)
		}
		if done.Status != JobDone || !done.FinishedAt.Equal(finished) {
			t.Fatalf("a finished job must be left alone: %+v", done)
		}

		if err := fresh.DeleteJob(t.Context(), "done"); err != nil {
			t.Fatalf("DeleteJob: %v", err)
		}
		if err := fresh.DeleteJob(t.Context(), "done"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("deleting a job twice must report ErrNotFound, got %v", err)
		}
	})
}

func TestStoreAudit(t *testing.T) {
	s := stOpen(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	entries := []AuditEntry{
		{UserID: 1, Username: "ada", Action: "auth.login", Target: "", IP: "198.51.100.1", OK: true, At: now.Add(-3 * time.Hour)},
		{UserID: 1, Username: "ada", Action: "fs.delete", Target: "/data/old.txt", IP: "198.51.100.1", OK: true, At: now.Add(-2 * time.Hour)},
		{UserID: 2, Username: "bob", Action: "auth.login", Target: "", IP: "198.51.100.2", OK: false, At: now.Add(-time.Hour)},
		{UserID: 2, Username: "bob", Action: "share.create", Target: "/data/reports", IP: "198.51.100.2", OK: true, At: now},
	}
	for _, e := range entries {
		if err := s.Audit(ctx, e); err != nil {
			t.Fatalf("Audit(%q): %v", e.Action, err)
		}
	}

	t.Run("entries are read back newest first", func(t *testing.T) {
		got, err := s.ListAudit(ctx, AuditFilter{})
		if err != nil {
			t.Fatalf("ListAudit: %v", err)
		}
		if len(got) != len(entries) {
			t.Fatalf("ListAudit returned %d entries, want %d", len(got), len(entries))
		}
		if got[0].Action != "share.create" {
			t.Fatalf("the newest entry must come first, got %q", got[0].Action)
		}
		if got[0].Target != "/data/reports" || !got[0].OK || got[0].Username != "bob" {
			t.Fatalf("the entry did not round trip: %+v", got[0])
		}
		if !got[0].At.Equal(now) {
			t.Fatalf("At = %v, want %v", got[0].At, now)
		}
		for _, e := range got {
			if e.Action == "auth.login" && e.Username == "bob" && e.OK {
				t.Fatalf("a failed attempt must stay recorded as failed: %+v", e)
			}
		}
	})

	t.Run("filtering by action narrows the log", func(t *testing.T) {
		got, err := s.ListAudit(ctx, AuditFilter{Action: "auth.login"})
		if err != nil {
			t.Fatalf("ListAudit: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("filtering by auth.login returned %d entries, want 2", len(got))
		}
		for _, e := range got {
			if e.Action != "auth.login" {
				t.Fatalf("the filter let %q through", e.Action)
			}
		}

		n, err := s.CountAudit(ctx, AuditFilter{Action: "auth.login"})
		if err != nil {
			t.Fatalf("CountAudit: %v", err)
		}
		if n != 2 {
			t.Fatalf("CountAudit = %d, want 2 so paging can be built on it", n)
		}

		byUser, err := s.ListAudit(ctx, AuditFilter{UserID: 2})
		if err != nil {
			t.Fatalf("ListAudit: %v", err)
		}
		if len(byUser) != 2 {
			t.Fatalf("filtering by account returned %d entries, want 2", len(byUser))
		}

		search, err := s.ListAudit(ctx, AuditFilter{Query: "old.txt"})
		if err != nil {
			t.Fatalf("ListAudit: %v", err)
		}
		if len(search) != 1 || search[0].Action != "fs.delete" {
			t.Fatalf("the free text search must match the target, got %+v", search)
		}
	})

	t.Run("filtering by a time window narrows the log", func(t *testing.T) {
		got, err := s.ListAudit(ctx, AuditFilter{
			Since: now.Add(-2 * time.Hour),
			Until: now.Add(-time.Hour),
		})
		if err != nil {
			t.Fatalf("ListAudit: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("the window returned %d entries, want the 2 inside it inclusive", len(got))
		}
		for _, e := range got {
			if e.At.Before(now.Add(-2*time.Hour)) || e.At.After(now.Add(-time.Hour)) {
				t.Fatalf("an entry outside the window came through: %v", e.At)
			}
		}

		empty, err := s.ListAudit(ctx, AuditFilter{Since: now.Add(time.Hour)})
		if err != nil {
			t.Fatalf("ListAudit: %v", err)
		}
		if len(empty) != 0 {
			t.Fatalf("a window in the future must be empty, got %d entries", len(empty))
		}
	})

	t.Run("purging drops only the older entries", func(t *testing.T) {
		n, err := s.PurgeAudit(ctx, time.Time{})
		if err != nil {
			t.Fatalf("PurgeAudit: %v", err)
		}
		if n != 0 {
			t.Fatalf("a zero instant must delete nothing, %d rows went", n)
		}

		n, err = s.PurgeAudit(ctx, now.Add(-90*time.Minute))
		if err != nil {
			t.Fatalf("PurgeAudit: %v", err)
		}
		if n != 2 {
			t.Fatalf("PurgeAudit removed %d entries, want the 2 older than the cutoff", n)
		}
		left, err := s.CountAudit(ctx, AuditFilter{})
		if err != nil {
			t.Fatalf("CountAudit: %v", err)
		}
		if left != 2 {
			t.Fatalf("CountAudit = %d after the purge, want 2", left)
		}
	})
}
