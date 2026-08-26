package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// tokNew stores one credential for an account and fails the test when it
// cannot. The prefix is derived from the name, so every token in a test is
// distinguishable and the uniqueness rule still applies.
func tokNew(t *testing.T, s *Store, user *User, name string, scope TokenScope, expires *time.Time) *APIToken {
	t.Helper()
	tk := &APIToken{
		UserID:    user.ID,
		Name:      name,
		Prefix:    name + "01",
		Hash:      "digest:" + name,
		Scope:     scope,
		ExpiresAt: expires,
	}
	if _, err := s.CreateToken(t.Context(), tk); err != nil {
		t.Fatalf("CreateToken(%q): %v", name, err)
	}
	if tk.ID == 0 {
		t.Fatalf("CreateToken(%q) left the identifier at zero", name)
	}
	return tk
}

// tokAt returns a pointer to a moment, which is how an expiry is carried.
func tokAt(d time.Duration) *time.Time {
	at := time.Now().Add(d).UTC().Truncate(time.Second)
	return &at
}

func TestStoreTokens(t *testing.T) {
	s := stOpen(t)
	ctx := t.Context()
	user := stUser(t, s, "scripter", RoleUser)

	// An account that has never minted anything answers with an empty list
	// rather than an error.
	empty, err := s.ListTokens(ctx, user.ID)
	if err != nil {
		t.Fatalf("ListTokens on a fresh account: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("ListTokens on a fresh account returned %d tokens, want none", len(empty))
	}

	backup := tokNew(t, s, user, "backup", ScopeWrite, tokAt(24*time.Hour))
	reader := tokNew(t, s, user, "reader", "", nil)

	// A blank scope is the narrow one, so a caller that forgets to say cannot
	// accidentally mint a credential that writes.
	if reader.Scope != ScopeRead {
		t.Fatalf("a token created without a scope has %q, want %q", reader.Scope, ScopeRead)
	}

	tokens, err := s.ListTokens(ctx, user.ID)
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	if len(tokens) != 2 {
		t.Fatalf("ListTokens returned %d tokens, want 2", len(tokens))
	}
	// Newest first, so the token just minted is the one the owner sees at the
	// top of the list.
	if tokens[0].ID != reader.ID || tokens[1].ID != backup.ID {
		t.Fatalf("ListTokens order = %d then %d, want %d then %d",
			tokens[0].ID, tokens[1].ID, reader.ID, backup.ID)
	}

	got := tokens[1]
	if got.Name != "backup" || got.Scope != ScopeWrite || got.Prefix != "backup01" {
		t.Fatalf("stored token = %+v, want the backup write token", got)
	}
	if got.Hash != "digest:backup" {
		t.Fatalf("stored digest = %q, want it read back untouched", got.Hash)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(*backup.ExpiresAt) {
		t.Fatalf("ExpiresAt = %v, want %v", got.ExpiresAt, backup.ExpiresAt)
	}
	if got.Expired {
		t.Fatal("a token expiring tomorrow reads as expired")
	}
	if got.LastUsedAt != nil || got.LastUsedIP != "" {
		t.Fatalf("a token that was never used reports %v from %q, want neither",
			got.LastUsedAt, got.LastUsedIP)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("CreatedAt was left empty, want the moment the token was minted")
	}

	// The lookup the authenticated path runs is by prefix, and it has to carry
	// the digest so the secret can be compared.
	found, err := s.GetTokenByPrefix(ctx, "backup01")
	if err != nil {
		t.Fatalf("GetTokenByPrefix: %v", err)
	}
	if found.ID != backup.ID || found.UserID != user.ID || found.Hash != "digest:backup" {
		t.Fatalf("GetTokenByPrefix = %+v, want the backup token of %d", found, user.ID)
	}
	if _, err := s.GetTokenByPrefix(ctx, "nosuch01"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetTokenByPrefix on an unknown prefix = %v, want ErrNotFound", err)
	}
	if _, err := s.GetTokenByPrefix(ctx, "   "); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetTokenByPrefix on a blank prefix = %v, want ErrNotFound", err)
	}

	// Two tokens cannot share a prefix, because the prefix is what the lookup
	// resolves.
	clash := &APIToken{UserID: user.ID, Name: "clash", Prefix: "backup01", Hash: "digest:clash"}
	if _, err := s.CreateToken(ctx, clash); !errors.Is(err, ErrConflict) {
		t.Fatalf("CreateToken with a prefix already in use = %v, want ErrConflict", err)
	}

	used := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	if err := s.TouchToken(ctx, backup.ID, used, "10.0.0.9"); err != nil {
		t.Fatalf("TouchToken: %v", err)
	}
	found, err = s.GetTokenByPrefix(ctx, "backup01")
	if err != nil {
		t.Fatalf("GetTokenByPrefix after TouchToken: %v", err)
	}
	if found.LastUsedAt == nil || !found.LastUsedAt.Equal(used) {
		t.Fatalf("LastUsedAt = %v, want %v", found.LastUsedAt, used)
	}
	if found.LastUsedIP != "10.0.0.9" {
		t.Fatalf("LastUsedIP = %q, want 10.0.0.9", found.LastUsedIP)
	}
	if err := s.TouchToken(ctx, 9999, used, "10.0.0.9"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("TouchToken on a token that is gone = %v, want ErrNotFound", err)
	}

	// A token has to belong to somebody, and it has to carry both halves.
	if _, err := s.CreateToken(ctx, nil); err == nil {
		t.Fatal("CreateToken(nil) succeeded, want an error")
	}
	if _, err := s.CreateToken(ctx, &APIToken{Prefix: "orphan01", Hash: "digest"}); err == nil {
		t.Fatal("CreateToken without an account succeeded, want an error")
	}
	if _, err := s.CreateToken(ctx, &APIToken{UserID: user.ID, Hash: "digest"}); err == nil {
		t.Fatal("CreateToken without a prefix succeeded, want an error")
	}
	if _, err := s.CreateToken(ctx, &APIToken{UserID: user.ID, Prefix: "nohash01"}); err == nil {
		t.Fatal("CreateToken without a digest succeeded, want an error")
	}
}

func TestStoreTokenExpiry(t *testing.T) {
	s := stOpen(t)
	ctx := t.Context()
	user := stUser(t, s, "runner", RoleUser)

	stale := tokNew(t, s, user, "stale", ScopeRead, tokAt(-time.Hour))
	live := tokNew(t, s, user, "live", ScopeRead, tokAt(time.Hour))
	forever := tokNew(t, s, user, "forever", ScopeWrite, nil)

	// The store marks a dead token when it reads it, so no caller has to repeat
	// the comparison and none of them can forget to.
	if !stale.Expired {
		t.Fatal("CreateToken left Expired false on a token whose expiry has passed")
	}
	found, err := s.GetTokenByPrefix(ctx, "stale01")
	if err != nil {
		t.Fatalf("GetTokenByPrefix: %v", err)
	}
	if !found.Expired {
		t.Fatal("a token whose expiry has passed reads as usable, want it marked expired")
	}

	tokens, err := s.ListTokens(ctx, user.ID)
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	for _, tk := range tokens {
		want := tk.ID == stale.ID
		if tk.Expired != want {
			t.Fatalf("token %q reports Expired %v, want %v", tk.Name, tk.Expired, want)
		}
	}

	// Purging clears the dead one and leaves both usable tokens alone, so a
	// credential without an expiry keeps working unattended.
	n, err := s.PurgeExpiredTokens(ctx, time.Now())
	if err != nil {
		t.Fatalf("PurgeExpiredTokens: %v", err)
	}
	if n != 1 {
		t.Fatalf("PurgeExpiredTokens removed %d tokens, want 1", n)
	}
	if _, err := s.GetTokenByPrefix(ctx, "stale01"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the expired token is still there: %v", err)
	}
	left, err := s.ListTokens(ctx, user.ID)
	if err != nil {
		t.Fatalf("ListTokens after purge: %v", err)
	}
	if len(left) != 2 || left[0].ID != forever.ID || left[1].ID != live.ID {
		t.Fatalf("after the purge the account holds %d tokens, want the live and the endless one", len(left))
	}

	// Purging again finds nothing to do.
	if n, err := s.PurgeExpiredTokens(ctx, time.Time{}); err != nil || n != 0 {
		t.Fatalf("PurgeExpiredTokens on a clean table = %d, %v, want 0 and no error", n, err)
	}
}

func TestStoreTokenDeleteBelongsToTheOwner(t *testing.T) {
	s := stOpen(t)
	ctx := t.Context()
	mine := stUser(t, s, "mine", RoleUser)
	theirs := stUser(t, s, "theirs", RoleUser)

	ours := tokNew(t, s, mine, "ours", ScopeWrite, nil)
	other := tokNew(t, s, theirs, "other", ScopeRead, nil)

	// One account cannot revoke the token of another, and cannot tell that
	// apart from a token that never existed.
	if err := s.DeleteToken(ctx, other.ID, mine.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteToken across accounts = %v, want ErrNotFound", err)
	}
	if _, err := s.GetTokenByPrefix(ctx, "other01"); err != nil {
		t.Fatalf("the token of another account was removed: %v", err)
	}

	if err := s.DeleteToken(ctx, ours.ID, mine.ID); err != nil {
		t.Fatalf("DeleteToken on our own token: %v", err)
	}
	if _, err := s.GetTokenByPrefix(ctx, "ours01"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetTokenByPrefix after delete = %v, want ErrNotFound", err)
	}
	if err := s.DeleteToken(ctx, ours.ID, mine.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteToken twice = %v, want ErrNotFound", err)
	}

	// Clearing an account takes only its own tokens with it, and clearing one
	// that holds none is not an error.
	tokNew(t, s, mine, "first", ScopeRead, nil)
	tokNew(t, s, mine, "second", ScopeRead, nil)
	if err := s.DeleteUserTokens(ctx, mine.ID); err != nil {
		t.Fatalf("DeleteUserTokens: %v", err)
	}
	if err := s.DeleteUserTokens(ctx, mine.ID); err != nil {
		t.Fatalf("DeleteUserTokens twice: %v", err)
	}
	left, err := s.ListTokens(ctx, mine.ID)
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("the account still holds %d tokens after clearing it", len(left))
	}
	if left, err = s.ListTokens(ctx, theirs.ID); err != nil || len(left) != 1 {
		t.Fatalf("the other account holds %d tokens, want its own 1 (err %v)", len(left), err)
	}

	// Removing the account removes what it could sign in with.
	if err := s.DeleteUser(ctx, theirs.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, err := s.GetTokenByPrefix(ctx, "other01"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a token outlived the account it belonged to: %v", err)
	}
}

func TestStoreTokenMigrationFromVersionTwo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v2.db")

	// Build the database exactly as version 1.1 left it: the original schema
	// and the storage figures, with no token table in sight.
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	if _, err := db.Exec(quotaSchemaSQL); err != nil {
		t.Fatalf("apply the quota migration: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version = 2"); err != nil {
		t.Fatalf("set version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open at version 2: %v", err)
	}
	ctx := t.Context()
	var version int
	if err := s.DB().QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != len(migrations) {
		t.Fatalf("user_version = %d, want %d", version, len(migrations))
	}

	user := stUser(t, s, "upgraded", RoleAdmin)
	tokNew(t, s, user, "afterupgrade", ScopeRead, nil)

	// The upgrade has to survive a restart without running twice.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	again := stOpenAt(t, path)
	if err := again.DB().QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read version after reopen: %v", err)
	}
	if version != len(migrations) {
		t.Fatalf("user_version after reopen = %d, want %d", version, len(migrations))
	}
	tokens, err := again.ListTokens(ctx, user.ID)
	if err != nil {
		t.Fatalf("ListTokens after reopen: %v", err)
	}
	if len(tokens) != 1 || tokens[0].Name != "afterupgrade" {
		t.Fatalf("after the reopen the account holds %d tokens, want the one minted before it", len(tokens))
	}
}
