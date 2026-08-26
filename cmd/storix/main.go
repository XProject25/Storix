// Command storix runs the Storix server and its administrative commands.
//
// Storix - modern web file manager for servers.
// Developed by X Project.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/XProject25/Storix/internal/api"
	"github.com/XProject25/Storix/internal/auth"
	"github.com/XProject25/Storix/internal/build"
	"github.com/XProject25/Storix/internal/config"
	"github.com/XProject25/Storix/internal/events"
	"github.com/XProject25/Storix/internal/jobs"
	"github.com/XProject25/Storix/internal/server"
	"github.com/XProject25/Storix/internal/store"
	"github.com/XProject25/Storix/internal/thumbs"
	"github.com/XProject25/Storix/internal/updater"
	"github.com/XProject25/Storix/internal/upload"
	"github.com/XProject25/Storix/internal/vfs"
	"github.com/XProject25/Storix/internal/web"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "storix: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	command := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command = args[0]
		args = args[1:]
	}
	switch command {
	case "serve", "run":
		return cmdServe(args)
	case "version":
		return cmdVersion()
	case "user":
		return cmdUser(args)
	case "setup-token":
		return cmdSetupToken(args)
	case "update":
		return cmdUpdate(args)
	case "doctor":
		return cmdDoctor(args)
	case "config":
		return cmdConfig(args)
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", command)
	}
}

func usage() {
	fmt.Print(`Storix - modern web file manager for servers
Developed by X Project

Usage:
  storix [command] [flags]

Commands:
  serve          Run the server (default)
  version        Print version information
  user           Manage accounts: user add|list|passwd|disable|enable|delete
  setup-token    Print the token that unlocks the first run wizard
  update         Download and install the newest release
  doctor         Check the installation and report problems
  config         Show the effective configuration

Flags:
  -config PATH   Configuration file (default /etc/storix/config.yaml)
  -port N        Override the listen port
  -data PATH     Override the data directory

`)
}

// ---- shared plumbing --------------------------------------------------------

type app struct {
	cfg      *config.Config
	store    *store.Store
	vfs      *vfs.VFS
	log      *slog.Logger
	logFile  *os.File
	sessions *auth.Manager
	events   *events.Hub
	jobs     *jobs.Manager
	thumbs   *thumbs.Cache
	uploads  *upload.Manager
	updater  *updater.Updater
}

func commonFlags(fs *flag.FlagSet) (*string, *int, *string) {
	cfgPath := fs.String("config", defaultConfigPath(), "configuration file")
	port := fs.Int("port", 0, "override the listen port")
	data := fs.String("data", "", "override the data directory")
	return cfgPath, port, data
}

func defaultConfigPath() string {
	if v := os.Getenv("STORIX_CONFIG"); v != "" {
		return v
	}
	return config.DefaultPath
}

// open loads the configuration and the database.
func open(cfgPath string, port int, data string, quiet bool) (*app, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	if port > 0 {
		cfg.Server.Port = port
	}
	if data != "" {
		cfg.Storage.DataDir = data
		cfg.Storage.Database = ""
		cfg.Storage.UploadDir = ""
		cfg.Storage.CacheDir = ""
		cfg.Storage.ThumbDir = ""
		cfg.Storage.TrashDir = ""
		cfg.Normalize()
	}
	if err := cfg.EnsureDirs(); err != nil {
		return nil, err
	}

	logger, logFile := newLogger(cfg, quiet)

	db, err := store.Open(cfg.Storage.Database)
	if err != nil {
		return nil, err
	}

	guard := vfs.New(vfs.Options{
		Denied:       cfg.Security.DeniedPaths,
		TrashDir:     cfg.Storage.TrashDir,
		MaxTextBytes: cfg.Limits.TextEditMaxBytes,
	})

	return &app{cfg: cfg, store: db, vfs: guard, log: logger, logFile: logFile}, nil
}

func (a *app) close() {
	if a.jobs != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		a.jobs.Shutdown(ctx)
		cancel()
	}
	if a.events != nil {
		a.events.Close()
	}
	if a.vfs != nil {
		_ = a.vfs.Close()
	}
	if a.store != nil {
		_ = a.store.Close()
	}
	if a.logFile != nil {
		_ = a.logFile.Close()
	}
}

func newLogger(cfg *config.Config, quiet bool) (*slog.Logger, *os.File) {
	level := slog.LevelInfo
	switch strings.ToLower(cfg.Log.Level) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	var writers []io.Writer
	if !quiet {
		writers = append(writers, os.Stdout)
	}
	var file *os.File
	if cfg.Log.File != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.Log.File), 0o750); err == nil {
			if f, err := os.OpenFile(cfg.Log.File, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640); err == nil {
				file = f
				writers = append(writers, f)
			}
		}
	}
	if len(writers) == 0 {
		writers = append(writers, io.Discard)
	}
	out := io.MultiWriter(writers...)

	var handler slog.Handler
	opts := &slog.HandlerOptions{Level: level}
	if strings.EqualFold(cfg.Log.Format, "json") {
		handler = slog.NewJSONHandler(out, opts)
	} else {
		handler = slog.NewTextHandler(out, opts)
	}
	logger := slog.New(handler)
	slog.SetDefault(logger)
	return logger, file
}

// ---- serve ------------------------------------------------------------------

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfgPath, port, data := commonFlags(fs)
	staticDir := fs.String("web", "", "serve the interface from this directory instead of the embedded build")
	if err := fs.Parse(args); err != nil {
		return err
	}

	a, err := open(*cfgPath, *port, *data, false)
	if err != nil {
		return err
	}
	defer a.close()

	a.log.Info("starting",
		"product", build.Product,
		"version", build.Version,
		"platform", build.Platform(),
		"config", *cfgPath,
		"data", a.cfg.Storage.DataDir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// A crash leaves jobs marked running; clear them before accepting traffic.
	if n, err := a.store.FailStaleJobs(ctx, "Interrupted by a server restart"); err == nil && n > 0 {
		a.log.Info("cleared interrupted operations", "count", n)
	}

	a.events = events.NewHub()
	a.events.StartHeartbeat(ctx, 25*time.Second)

	a.jobs = jobs.NewManager(a.store, a.events, a.cfg.Limits.MaxConcurrentOps)
	a.jobs.Start(ctx)

	a.sessions = auth.NewManager(a.store, auth.Options{
		CookieName: a.cfg.Security.CookieName,
		TTL:        a.cfg.Security.SessionTTL.D(),
		Idle:       a.cfg.Security.SessionIdle.D(),
		Secure:     a.cfg.Security.CookieSecure,
		SameSite:   sameSite(a.cfg.Security.CookieSameSite),
		Path:       "/",
	})

	thumbCache, err := thumbs.New(thumbs.Options{
		Dir:            a.cfg.Storage.ThumbDir,
		MaxSourceBytes: a.cfg.Limits.ThumbMaxBytes,
		MaxAge:         30 * 24 * time.Hour,
	})
	if err != nil {
		return fmt.Errorf("thumbnail cache: %w", err)
	}
	a.thumbs = thumbCache

	// The upload engine consults the quota before a transfer starts and records
	// the bytes once it lands. Those answers live in the API, which in turn
	// needs the upload engine, so the hooks bind late through this pointer.
	var rest *api.API
	a.uploads = upload.New(upload.Deps{
		Store:   a.store,
		VFS:     a.vfs,
		Events:  a.events,
		Logger:  a.log,
		MaxSize: a.cfg.Limits.MaxUploadSize,
		Expiry:  a.cfg.Limits.UploadExpiry.D(),
		QuotaCheck: func(ctx context.Context, userID int64, size int64) (bool, int64, error) {
			if rest == nil {
				return true, -1, nil
			}
			return rest.UploadQuotaCheck()(ctx, userID, size)
		},
		QuotaAdd: func(ctx context.Context, userID int64, bytes int64) {
			if rest == nil {
				return
			}
			rest.UploadQuotaAdd()(ctx, userID, bytes)
		},
	})

	channel, _ := a.store.GetSetting(ctx, store.SettingUpdateChannel)
	a.updater = updater.New(updater.Options{Channel: channel, Logger: a.log})

	static, err := web.Handler(*staticDir)
	if err != nil {
		a.log.Warn("web interface not available", "err", err)
		static = web.Placeholder()
	}

	rest = api.New(api.Deps{
		Config:  a.cfg,
		Store:   a.store,
		VFS:     a.vfs,
		Session: a.sessions,
		Jobs:    a.jobs,
		Events:  a.events,
		Thumbs:  a.thumbs,
		Uploads: a.uploads,
		Updater: a.updater,
		Logger:  a.log,
		Static:  static,
	})
	defer rest.Close()

	if err := a.ensureSetupToken(ctx); err != nil {
		a.log.Warn("setup token", "err", err)
	}
	go a.janitor(ctx)

	srv := server.New(server.Options{Config: a.cfg, Handler: rest.Handler(), Logger: a.log})
	if err := srv.Run(ctx); err != nil {
		return err
	}
	a.log.Info("stopped")
	return nil
}

func sameSite(v string) http.SameSite {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "strict":
		return http.SameSiteStrictMode
	case "none":
		return http.SameSiteNoneMode
	default:
		return http.SameSiteLaxMode
	}
}

// ensureSetupToken creates the one time token that unlocks the first run
// wizard, so a stranger who finds the port before the owner cannot claim the
// instance. It is printed on first boot and readable with storix setup-token.
func (a *app) ensureSetupToken(ctx context.Context) error {
	if a.store.SetupCompleted(ctx) {
		return nil
	}
	token, err := a.store.GetSetting(ctx, "setup.token")
	if err != nil {
		return err
	}
	if token == "" {
		token = auth.MustToken(18)
		if err := a.store.SetSetting(ctx, "setup.token", token); err != nil {
			return err
		}
	}
	tokenFile := filepath.Join(a.cfg.Storage.DataDir, "setup-token")
	_ = os.WriteFile(tokenFile, []byte(token+"\n"), 0o600)

	url := fmt.Sprintf("%s/setup?token=%s", a.cfg.PublicURL(), token)
	a.log.Info("first run wizard is waiting", "url", url)
	fmt.Printf("\n  Storix is ready. Open the setup wizard:\n\n    %s\n\n", url)
	return nil
}

// janitor performs the periodic housekeeping.
func (a *app) janitor(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.sweep(ctx)
		}
	}
}

func (a *app) sweep(ctx context.Context) {
	now := time.Now()
	if n, err := a.store.PurgeExpiredSessions(ctx, now); err == nil && n > 0 {
		a.log.Debug("expired sessions removed", "count", n)
	}
	if n, err := a.store.PurgeExpiredShares(ctx, now); err == nil && n > 0 {
		a.log.Debug("expired shares removed", "count", n)
	}
	if _, err := a.store.PurgeOldJobs(ctx, now.Add(-48*time.Hour)); err != nil {
		a.log.Debug("job cleanup failed", "err", err)
	}
	if _, err := a.store.PurgeLoginAttempts(ctx, now.Add(-24*time.Hour)); err != nil {
		a.log.Debug("login attempt cleanup failed", "err", err)
	}
	if _, err := a.store.PurgeAudit(ctx, now.Add(-90*24*time.Hour)); err != nil {
		a.log.Debug("audit cleanup failed", "err", err)
	}
	if a.thumbs != nil {
		if _, err := a.thumbs.Purge(ctx, now.Add(-30*24*time.Hour)); err != nil {
			a.log.Debug("thumbnail cleanup failed", "err", err)
		}
	}
	scope, err := a.adminScope(ctx)
	if err == nil {
		if n, err := a.uploads.Cleanup(ctx, scope); err == nil && n > 0 {
			a.log.Debug("abandoned uploads removed", "count", n)
		}
		items, err := a.store.ListExpiredTrash(ctx, now)
		if err == nil {
			for _, item := range items {
				rec := vfs.TrashRecord{Name: item.Name, OriginalPath: item.OriginalPath, StoredPath: item.StoredPath, IsDir: item.IsDir, Size: item.Size}
				if err := a.vfs.PurgeTrash(rec); err != nil {
					a.log.Debug("trash purge failed", "item", item.Name, "err", err)
					continue
				}
				_ = a.store.DeleteTrashItem(ctx, item.ID)
			}
		}
	}
}

// adminScope is the full set of configured roots, used by background work.
func (a *app) adminScope(ctx context.Context) (vfs.Scope, error) {
	roots, err := a.store.ListRoots(ctx)
	if err != nil {
		return vfs.Scope{}, err
	}
	mounts := make([]vfs.Mount, 0, len(roots))
	for _, r := range roots {
		mounts = append(mounts, vfs.Mount{Path: r.Path, Label: r.Label, Icon: r.Icon})
	}
	return vfs.Scope{Mounts: mounts, Admin: true}, nil
}
