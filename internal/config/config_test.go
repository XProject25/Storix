package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A configuration that names only storage.data_dir must move everything that
// hangs off it. Getting this wrong sends the database to the built in
// directory while the operator believes it lives where they asked.
func TestDataDirMovesEverythingDerivedFromIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	dataDir := filepath.ToSlash(filepath.Join(dir, "storix-data"))
	body := "server:\n  port: 9000\nstorage:\n  data_dir: " + dataDir + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"database":   cfg.Storage.Database,
		"uploads":    cfg.Storage.UploadDir,
		"cache":      cfg.Storage.CacheDir,
		"thumbnails": cfg.Storage.ThumbDir,
		"trash":      cfg.Storage.TrashDir,
		"acme cache": cfg.Server.TLS.CacheDir,
	}
	for name, got := range cases {
		if !strings.HasPrefix(filepath.ToSlash(got), dataDir) {
			t.Errorf("%s = %q, it must live under the configured data directory %q", name, got, dataDir)
		}
	}
	if cfg.Server.Port != 9000 {
		t.Errorf("port = %d, want 9000", cfg.Server.Port)
	}
}

// An explicit path for one of the derived locations wins over the data
// directory, so an operator can put the database on a different volume.
func TestExplicitPathsWinOverTheDataDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	dataDir := filepath.ToSlash(filepath.Join(dir, "data"))
	dbPath := filepath.ToSlash(filepath.Join(dir, "elsewhere", "storix.db"))
	body := "storage:\n  data_dir: " + dataDir + "\n  database: " + dbPath + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.ToSlash(cfg.Storage.Database) != dbPath {
		t.Fatalf("database = %q, want the explicit %q", cfg.Storage.Database, dbPath)
	}
	if !strings.HasPrefix(filepath.ToSlash(cfg.Storage.TrashDir), dataDir) {
		t.Fatalf("trash = %q, it should still follow the data directory", cfg.Storage.TrashDir)
	}
}

func TestMissingFileFallsBackToDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("a missing configuration file must not be an error: %v", err)
	}
	if cfg.Server.Port != 8686 {
		t.Errorf("port = %d, want the default 8686", cfg.Server.Port)
	}
	if len(cfg.Security.DeniedPaths) == 0 {
		t.Error("the protected path list must never be empty")
	}
}

func TestDurationsParse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "security:\n  session_ttl: 24h\n  login_lockout: 30m\nlimits:\n  trash_retention: 168h\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Security.SessionTTL.D() != 24*time.Hour {
		t.Errorf("session_ttl = %s, want 24h", cfg.Security.SessionTTL.D())
	}
	if cfg.Security.LoginLockout.D() != 30*time.Minute {
		t.Errorf("login_lockout = %s, want 30m", cfg.Security.LoginLockout.D())
	}
	if cfg.Limits.TrashRetention.D() != 168*time.Hour {
		t.Errorf("trash_retention = %s, want 168h", cfg.Limits.TrashRetention.D())
	}
}

func TestValidateRejectsImpossibleSettings(t *testing.T) {
	cfg := Default()
	cfg.Server.Port = 70000
	if err := cfg.Validate(); err == nil {
		t.Error("an out of range port must be refused")
	}

	cfg = Default()
	cfg.Server.TLS.Mode = TLSACME
	if err := cfg.Validate(); err == nil {
		t.Error("automatic TLS without a domain must be refused")
	}

	cfg = Default()
	cfg.Server.TLS.Mode = TLSManual
	if err := cfg.Validate(); err == nil {
		t.Error("manual TLS without a certificate must be refused")
	}
}

func TestDomainIsNormalizedAndForcesSecureCookies(t *testing.T) {
	cfg := Default()
	cfg.Server.Domain = "https://files.example.com/"
	cfg.Server.TLS.Mode = TLSACME
	cfg.Normalize()

	if cfg.Server.Domain != "files.example.com" {
		t.Errorf("domain = %q, want files.example.com", cfg.Server.Domain)
	}
	if !cfg.Security.CookieSecure {
		t.Error("a TLS deployment must force secure cookies")
	}
	if got := cfg.PublicURL(); got != "https://files.example.com" {
		t.Errorf("public url = %q", got)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg := Default()
	cfg.SetPath(path)
	cfg.Server.Port = 9191
	cfg.Storage.DataDir = filepath.Join(dir, "data")
	cfg.Storage.Database = ""
	cfg.Normalize()
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Developed by X Project") {
		t.Error("a saved configuration should carry the project header")
	}

	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.Server.Port != 9191 {
		t.Errorf("port did not survive the round trip: %d", again.Server.Port)
	}
	if filepath.ToSlash(again.Storage.Database) != filepath.ToSlash(cfg.Storage.Database) {
		t.Errorf("database path did not survive: %q vs %q", again.Storage.Database, cfg.Storage.Database)
	}
}
