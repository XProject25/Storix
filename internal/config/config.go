// Package config loads and persists the Storix server configuration.
//
// Storix - modern web file manager for servers.
// Developed by X Project.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultPath is where the installer drops the configuration file.
const DefaultPath = "/etc/storix/config.yaml"

// Duration is a yaml friendly time.Duration ("30m", "168h").
type Duration time.Duration

// MarshalYAML renders the duration in its human readable form.
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// UnmarshalYAML accepts either "30m" style strings or a plain number of seconds.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var raw string
	if err := value.Decode(&raw); err != nil {
		var secs int64
		if err2 := value.Decode(&secs); err2 != nil {
			return err
		}
		*d = Duration(time.Duration(secs) * time.Second)
		return nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "0" {
		*d = 0
		return nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("config: invalid duration %q: %w", raw, err)
	}
	*d = Duration(parsed)
	return nil
}

// D unwraps the duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// Config is the full on-disk configuration.
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Storage  StorageConfig  `yaml:"storage"`
	Security SecurityConfig `yaml:"security"`
	Limits   LimitsConfig   `yaml:"limits"`
	Log      LogConfig      `yaml:"log"`
	Updates  UpdatesConfig  `yaml:"updates"`

	path string
}

// ServerConfig controls the HTTP listener.
type ServerConfig struct {
	Host           string    `yaml:"host"`
	Port           int       `yaml:"port"`
	Domain         string    `yaml:"domain"`
	BaseURL        string    `yaml:"base_url"`
	ReadTimeout    Duration  `yaml:"read_timeout"`
	WriteTimeout   Duration  `yaml:"write_timeout"`
	IdleTimeout    Duration  `yaml:"idle_timeout"`
	ShutdownGrace  Duration  `yaml:"shutdown_grace"`
	TrustedProxies []string  `yaml:"trusted_proxies"`
	TLS            TLSConfig `yaml:"tls"`
}

// TLSMode selects how certificates are obtained.
type TLSMode string

// Supported TLS modes.
const (
	TLSOff    TLSMode = "off"
	TLSACME   TLSMode = "acme"
	TLSManual TLSMode = "manual"
)

// TLSConfig configures HTTPS.
type TLSConfig struct {
	Mode      TLSMode `yaml:"mode"`
	Email     string  `yaml:"email"`
	CertFile  string  `yaml:"cert_file"`
	KeyFile   string  `yaml:"key_file"`
	CacheDir  string  `yaml:"cache_dir"`
	Redirect  bool    `yaml:"redirect_http"`
	HTTPPort  int     `yaml:"http_port"`
	HTTPSPort int     `yaml:"https_port"`
}

// StorageConfig points Storix at its own working directories.
type StorageConfig struct {
	DataDir   string `yaml:"data_dir"`
	UploadDir string `yaml:"upload_dir"`
	CacheDir  string `yaml:"cache_dir"`
	ThumbDir  string `yaml:"thumb_dir"`
	TrashDir  string `yaml:"trash_dir"`
	Database  string `yaml:"database"`
}

// SecurityConfig holds authentication and hardening knobs.
type SecurityConfig struct {
	SessionTTL      Duration `yaml:"session_ttl"`
	SessionIdle     Duration `yaml:"session_idle"`
	CookieName      string   `yaml:"cookie_name"`
	CookieSecure    bool     `yaml:"cookie_secure"`
	CookieSameSite  string   `yaml:"cookie_same_site"`
	AllowAdvanced   bool     `yaml:"allow_advanced"`
	DeniedPaths     []string `yaml:"denied_paths"`
	IPAllowlist     []string `yaml:"ip_allowlist"`
	LoginRateBurst  int      `yaml:"login_rate_burst"`
	LoginRateWindow Duration `yaml:"login_rate_window"`
	LoginLockout    Duration `yaml:"login_lockout"`
	FollowSymlinks  bool     `yaml:"follow_symlinks"`
	RunAsUser       string   `yaml:"run_as_user"`
}

// LimitsConfig bounds expensive operations.
type LimitsConfig struct {
	MaxUploadSize    int64    `yaml:"max_upload_size"`
	UploadChunkSize  int64    `yaml:"upload_chunk_size"`
	UploadExpiry     Duration `yaml:"upload_expiry"`
	SearchMaxResults int      `yaml:"search_max_results"`
	SearchTimeout    Duration `yaml:"search_timeout"`
	ZipMaxFiles      int      `yaml:"zip_max_files"`
	TextEditMaxBytes int64    `yaml:"text_edit_max_bytes"`
	ThumbMaxBytes    int64    `yaml:"thumb_max_bytes"`
	TrashRetention   Duration `yaml:"trash_retention"`
	MaxConcurrentOps int      `yaml:"max_concurrent_ops"`
	ListPageSize     int      `yaml:"list_page_size"`
}

// LogConfig configures the logger.
type LogConfig struct {
	Level  string `yaml:"level"`
	File   string `yaml:"file"`
	Format string `yaml:"format"`
	Access bool   `yaml:"access_log"`
}

// DefaultUpdateEndpoint is the service Storix asks about new versions. What
// the request carries, and how to switch it off, is written down in
// docs/UPDATES.md.
const DefaultUpdateEndpoint = "https://updates.xproject.live/v1/check"

// UpdatesConfig controls the version check.
type UpdatesConfig struct {
	// Check switches the automatic version check on. With it off Storix
	// contacts nothing on its own, and a manual "storix update" still works.
	Check bool `yaml:"check"`
	// Endpoint receives the check. Pointing it at the GitHub release API
	// keeps the check while sending nothing that can be counted.
	Endpoint string `yaml:"endpoint"`
	// Channel selects the release track: stable or beta.
	Channel string `yaml:"channel"`
	// Interval is the floor between two checks. It exists to protect the
	// receiving host, not this one.
	Interval Duration `yaml:"interval"`
}

// Default returns a configuration suitable for a fresh Linux install.
func Default() *Config {
	dataDir := "/var/lib/storix"
	logFile := "/var/log/storix/storix.log"
	if runtime.GOOS == "windows" {
		dataDir = filepath.Join(os.Getenv("ProgramData"), "Storix")
		logFile = filepath.Join(dataDir, "storix.log")
	}
	return &Config{
		Server: ServerConfig{
			Host:          "0.0.0.0",
			Port:          8686,
			IdleTimeout:   Duration(120 * time.Second),
			ShutdownGrace: Duration(20 * time.Second),
			TLS: TLSConfig{
				Mode:      TLSOff,
				Redirect:  true,
				HTTPPort:  80,
				HTTPSPort: 443,
			},
		},
		Storage: StorageConfig{
			DataDir:   dataDir,
			UploadDir: filepath.Join(dataDir, "uploads"),
			CacheDir:  filepath.Join(dataDir, "cache"),
			ThumbDir:  filepath.Join(dataDir, "thumbnails"),
			TrashDir:  filepath.Join(dataDir, "trash"),
			Database:  filepath.Join(dataDir, "storix.db"),
		},
		Security: SecurityConfig{
			SessionTTL:      Duration(7 * 24 * time.Hour),
			CookieName:      "storix_session",
			CookieSameSite:  "lax",
			AllowAdvanced:   true,
			DeniedPaths:     DefaultDeniedPaths(),
			LoginRateBurst:  8,
			LoginRateWindow: Duration(5 * time.Minute),
			LoginLockout:    Duration(15 * time.Minute),
			FollowSymlinks:  false,
		},
		Limits: LimitsConfig{
			MaxUploadSize:    0,
			UploadChunkSize:  16 << 20,
			UploadExpiry:     Duration(72 * time.Hour),
			SearchMaxResults: 2000,
			SearchTimeout:    Duration(20 * time.Second),
			ZipMaxFiles:      200000,
			TextEditMaxBytes: 8 << 20,
			ThumbMaxBytes:    64 << 20,
			TrashRetention:   Duration(30 * 24 * time.Hour),
			MaxConcurrentOps: 4,
			ListPageSize:     5000,
		},
		Log: LogConfig{Level: "info", File: logFile, Format: "text"},
		Updates: UpdatesConfig{
			Check:    true,
			Endpoint: DefaultUpdateEndpoint,
			Channel:  "stable",
			Interval: Duration(6 * time.Hour),
		},
	}
}

// DefaultDeniedPaths lists locations Storix refuses to expose even when a
// parent directory is mounted. They hold credentials or kernel state.
func DefaultDeniedPaths() []string {
	return []string{
		"/etc/shadow",
		"/etc/gshadow",
		"/etc/sudoers",
		"/etc/sudoers.d",
		"/etc/ssh",
		"/root/.ssh",
		"/proc",
		"/sys",
		"/dev",
		"/run",
		"/var/lib/storix",
		"/etc/storix",
	}
}

// Load reads a configuration file, filling in defaults for absent keys.
// A missing file is not an error: defaults are returned.
func Load(path string) (*Config, error) {
	cfg := Default()
	cfg.path = path
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			cfg.Normalize()
			return cfg, nil
		}
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	// Parse once into a bare struct to learn which keys the file actually set.
	// Without this, the defaults already carry derived paths such as the
	// database location, and a file that only sets storage.data_dir would move
	// nothing: the database, uploads, cache, thumbnails and trash would all
	// silently stay under the built in directory.
	var provided Config
	if err := yaml.Unmarshal(raw, &provided); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if provided.Storage.DataDir != "" {
		derived := []struct {
			given string
			slot  *string
		}{
			{provided.Storage.UploadDir, &cfg.Storage.UploadDir},
			{provided.Storage.CacheDir, &cfg.Storage.CacheDir},
			{provided.Storage.ThumbDir, &cfg.Storage.ThumbDir},
			{provided.Storage.TrashDir, &cfg.Storage.TrashDir},
			{provided.Storage.Database, &cfg.Storage.Database},
			{provided.Server.TLS.CacheDir, &cfg.Server.TLS.CacheDir},
		}
		for _, d := range derived {
			if d.given == "" {
				// Clear the default so Normalize rebuilds it from data_dir.
				*d.slot = ""
			}
		}
	}

	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	cfg.Normalize()
	return cfg, cfg.Validate()
}

// Path reports where the config was loaded from.
func (c *Config) Path() string { return c.path }

// SetPath overrides the persistence target.
func (c *Config) SetPath(p string) { c.path = p }

// Normalize fills derived values and repairs empty fields.
func (c *Config) Normalize() {
	d := Default()
	if c.Server.Host == "" {
		c.Server.Host = d.Server.Host
	}
	if c.Server.Port == 0 {
		c.Server.Port = d.Server.Port
	}
	if c.Server.ShutdownGrace == 0 {
		c.Server.ShutdownGrace = d.Server.ShutdownGrace
	}
	if c.Server.IdleTimeout == 0 {
		c.Server.IdleTimeout = d.Server.IdleTimeout
	}
	if c.Server.TLS.Mode == "" {
		c.Server.TLS.Mode = TLSOff
	}
	if c.Server.TLS.HTTPPort == 0 {
		c.Server.TLS.HTTPPort = 80
	}
	if c.Server.TLS.HTTPSPort == 0 {
		c.Server.TLS.HTTPSPort = 443
	}
	if c.Storage.DataDir == "" {
		c.Storage.DataDir = d.Storage.DataDir
	}
	if c.Storage.UploadDir == "" {
		c.Storage.UploadDir = filepath.Join(c.Storage.DataDir, "uploads")
	}
	if c.Storage.CacheDir == "" {
		c.Storage.CacheDir = filepath.Join(c.Storage.DataDir, "cache")
	}
	if c.Storage.ThumbDir == "" {
		c.Storage.ThumbDir = filepath.Join(c.Storage.DataDir, "thumbnails")
	}
	if c.Storage.TrashDir == "" {
		c.Storage.TrashDir = filepath.Join(c.Storage.DataDir, "trash")
	}
	if c.Storage.Database == "" {
		c.Storage.Database = filepath.Join(c.Storage.DataDir, "storix.db")
	}
	if c.Server.TLS.CacheDir == "" {
		c.Server.TLS.CacheDir = filepath.Join(c.Storage.DataDir, "acme")
	}
	if c.Security.CookieName == "" {
		c.Security.CookieName = d.Security.CookieName
	}
	if c.Security.CookieSameSite == "" {
		c.Security.CookieSameSite = "lax"
	}
	if c.Security.SessionTTL == 0 {
		c.Security.SessionTTL = d.Security.SessionTTL
	}
	if c.Security.LoginRateBurst == 0 {
		c.Security.LoginRateBurst = d.Security.LoginRateBurst
	}
	if c.Security.LoginRateWindow == 0 {
		c.Security.LoginRateWindow = d.Security.LoginRateWindow
	}
	if c.Security.LoginLockout == 0 {
		c.Security.LoginLockout = d.Security.LoginLockout
	}
	if len(c.Security.DeniedPaths) == 0 {
		c.Security.DeniedPaths = DefaultDeniedPaths()
	}
	if c.Limits.UploadChunkSize <= 0 {
		c.Limits.UploadChunkSize = d.Limits.UploadChunkSize
	}
	if c.Limits.UploadExpiry == 0 {
		c.Limits.UploadExpiry = d.Limits.UploadExpiry
	}
	if c.Limits.SearchMaxResults <= 0 {
		c.Limits.SearchMaxResults = d.Limits.SearchMaxResults
	}
	if c.Limits.SearchTimeout == 0 {
		c.Limits.SearchTimeout = d.Limits.SearchTimeout
	}
	if c.Limits.ZipMaxFiles <= 0 {
		c.Limits.ZipMaxFiles = d.Limits.ZipMaxFiles
	}
	if c.Limits.TextEditMaxBytes <= 0 {
		c.Limits.TextEditMaxBytes = d.Limits.TextEditMaxBytes
	}
	if c.Limits.ThumbMaxBytes <= 0 {
		c.Limits.ThumbMaxBytes = d.Limits.ThumbMaxBytes
	}
	if c.Limits.TrashRetention == 0 {
		c.Limits.TrashRetention = d.Limits.TrashRetention
	}
	if c.Limits.MaxConcurrentOps <= 0 {
		c.Limits.MaxConcurrentOps = d.Limits.MaxConcurrentOps
	}
	if c.Limits.ListPageSize <= 0 {
		c.Limits.ListPageSize = d.Limits.ListPageSize
	}
	// Updates.Check is left exactly as it was read. It is the one field here
	// that has a meaningful false, and an operator who switched the check off
	// must not have it switched back on by a later repair.
	if c.Updates.Endpoint == "" {
		c.Updates.Endpoint = d.Updates.Endpoint
	}
	if c.Updates.Channel == "" {
		c.Updates.Channel = d.Updates.Channel
	}
	if c.Updates.Interval == 0 {
		c.Updates.Interval = d.Updates.Interval
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.Log.Format == "" {
		c.Log.Format = "text"
	}
	domain := strings.TrimSpace(c.Server.Domain)
	domain = strings.TrimPrefix(domain, "https://")
	domain = strings.TrimPrefix(domain, "http://")
	c.Server.Domain = strings.TrimSuffix(domain, "/")
	if c.Server.Domain != "" && c.Server.TLS.Mode != TLSOff {
		c.Security.CookieSecure = true
	}
}

// Validate reports configuration that cannot work.
func (c *Config) Validate() error {
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("config: server.port %d out of range", c.Server.Port)
	}
	switch c.Server.TLS.Mode {
	case TLSOff, TLSACME, TLSManual:
	default:
		return fmt.Errorf("config: unknown tls.mode %q", c.Server.TLS.Mode)
	}
	if c.Server.TLS.Mode == TLSACME && c.Server.Domain == "" {
		return errors.New("config: tls.mode acme requires server.domain")
	}
	if c.Server.TLS.Mode == TLSManual && (c.Server.TLS.CertFile == "" || c.Server.TLS.KeyFile == "") {
		return errors.New("config: tls.mode manual requires cert_file and key_file")
	}
	// A short interval is always a mistake, and the machine it would punish
	// belongs to somebody else.
	if c.Updates.Interval != 0 && c.Updates.Interval.D() < time.Hour {
		return fmt.Errorf("config: updates.interval %s is shorter than one hour", c.Updates.Interval.D())
	}
	return nil
}

// EnsureDirs creates every directory Storix needs to run.
func (c *Config) EnsureDirs() error {
	dirs := []string{c.Storage.DataDir, c.Storage.UploadDir, c.Storage.CacheDir, c.Storage.ThumbDir, c.Storage.TrashDir}
	if c.Server.TLS.CacheDir != "" {
		dirs = append(dirs, c.Server.TLS.CacheDir)
	}
	if c.Log.File != "" {
		dirs = append(dirs, filepath.Dir(c.Log.File))
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("config: create %s: %w", dir, err)
		}
	}
	return nil
}

// Save writes the configuration back to disk atomically.
func (c *Config) Save() error {
	if c.path == "" {
		return errors.New("config: no path set")
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o750); err != nil {
		return err
	}
	out, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	header := "# Storix configuration\n# Developed by X Project - https://github.com/XProject25/Storix\n"
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, append([]byte(header), out...), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// Addr is the listen address for the plain HTTP server.
func (c *Config) Addr() string { return fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port) }

// PublicURL is the address users should open.
func (c *Config) PublicURL() string {
	if c.Server.BaseURL != "" {
		return strings.TrimSuffix(c.Server.BaseURL, "/")
	}
	if c.Server.Domain != "" {
		if c.Server.TLS.Mode != TLSOff {
			return "https://" + c.Server.Domain
		}
		return "http://" + c.Server.Domain
	}
	host := c.Server.Host
	if host == "0.0.0.0" || host == "::" || host == "" {
		host = "localhost"
	}
	return fmt.Sprintf("http://%s:%d", host, c.Server.Port)
}
