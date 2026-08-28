package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/XProject25/Storix/internal/build"
	"github.com/XProject25/Storix/internal/config"
	"github.com/XProject25/Storix/internal/events"
	"github.com/XProject25/Storix/internal/jobs"
	"github.com/XProject25/Storix/internal/store"
	"github.com/XProject25/Storix/internal/updater"
	"github.com/XProject25/Storix/internal/vfs"
)

// sysUpdateMinInterval is the shortest gap an operator may put between two
// update checks. It protects the host being asked, not this one.
const sysUpdateMinInterval = time.Hour

// sysDiskBudget is how long the dashboard waits for the file system to answer
// a usage question. A single unresponsive disk must never stall the home
// screen, so anything slower than this is reported as zeros.
const sysDiskBudget = 3 * time.Second

// sysUpdateTTL is how long a release check answer stays fresh. The dashboard
// polls often, and the release feed is rate limited.
const sysUpdateTTL = 10 * time.Minute

// ---- update check cache -----------------------------------------------------

// sysUpdateCacheState holds the last release check so repeated calls do not
// reach the network. The mutex is also the fetch guard: one caller performs
// the request while the others wait for its result.
type sysUpdateCacheState struct {
	mu  sync.Mutex
	rel *updater.Release
	at  time.Time
}

// sysUpdateCache is the process wide release check cache.
var sysUpdateCache sysUpdateCacheState

// peek returns the cached release without ever blocking and without touching
// the network. It is what the dashboard uses.
func (c *sysUpdateCacheState) peek() (*updater.Release, bool) {
	if !c.mu.TryLock() {
		return nil, false
	}
	defer c.mu.Unlock()
	if c.rel == nil || time.Since(c.at) > sysUpdateTTL {
		return nil, false
	}
	return c.rel, true
}

// remember stores a release the caller obtained on its own, so the next check
// answers from memory.
func (c *sysUpdateCacheState) remember(rel *updater.Release) {
	if rel == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rel = rel
	c.at = time.Now()
}

// fetch returns the cached release, asking the updater when the entry is
// missing or stale.
func (c *sysUpdateCacheState) fetch(ctx context.Context, up *updater.Updater) (*updater.Release, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rel != nil && time.Since(c.at) <= sysUpdateTTL {
		return c.rel, nil
	}
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	rel, err := up.Check(callCtx)
	if err != nil {
		return nil, err
	}
	c.rel = rel
	c.at = time.Now()
	return rel, nil
}

// ---- dashboard --------------------------------------------------------------

// sysStorage is the headline storage figure of the home screen.
type sysStorage struct {
	Total   uint64  `json:"total"`
	Used    uint64  `json:"used"`
	Free    uint64  `json:"free"`
	Percent float64 `json:"percent"`
	Path    string  `json:"path"`
}

// sysTransfers counts the uploads still in flight.
type sysTransfers struct {
	Active int   `json:"active"`
	Bytes  int64 `json:"bytes"`
}

// sysShareSummary counts the public links a user owns.
type sysShareSummary struct {
	Active int `json:"active"`
}

// sysTrashSummary is the recycle bin at a glance.
type sysTrashSummary struct {
	Count int   `json:"count"`
	Bytes int64 `json:"bytes"`
}

// sysMountView is a mount together with the volume that carries it.
type sysMountView struct {
	vfs.Mount
	Usage *vfs.DiskUsage `json:"usage"`
}

// sysDashboard is the aggregate the home screen renders in one request.
type sysDashboard struct {
	Greeting        string            `json:"greeting"`
	User            *store.User       `json:"user"`
	Storage         sysStorage        `json:"storage"`
	Recent          []*store.Recent   `json:"recent"`
	Favorites       []*store.Favorite `json:"favorites"`
	Transfers       sysTransfers      `json:"transfers"`
	Shares          sysShareSummary   `json:"shares"`
	Jobs            []*store.Job      `json:"jobs"`
	Trash           sysTrashSummary   `json:"trash"`
	Mounts          []sysMountView    `json:"mounts"`
	Version         string            `json:"version"`
	UpdateAvailable bool              `json:"updateAvailable"`
}

// handleDashboard answers GET /api/v1/dashboard.
func (a *API) handleDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(r)

	scope, err := a.scopeFor(ctx, user)
	if err != nil {
		a.fail(w, r, err)
		return
	}

	var (
		recents    []*store.Recent
		favorites  []*store.Favorite
		recentJobs []*store.Job
		transfers  sysTransfers
		shares     sysShareSummary
		trash      sysTrashSummary
	)

	// The database work runs while the disks are being measured.
	var wg sync.WaitGroup
	wg.Add(6)

	go func() {
		defer wg.Done()
		list, err := a.Store.ListRecents(ctx, user.ID, 12)
		if err != nil {
			a.Logger.Warn("dashboard recents failed", "err", err)
			return
		}
		recents = list
	}()

	go func() {
		defer wg.Done()
		list, err := a.Store.ListFavorites(ctx, user.ID)
		if err != nil {
			a.Logger.Warn("dashboard favorites failed", "err", err)
			return
		}
		favorites = list
	}()

	go func() {
		defer wg.Done()
		list, err := a.sysRecentJobs(ctx, user.ID, 5)
		if err != nil {
			a.Logger.Warn("dashboard jobs failed", "err", err)
			return
		}
		recentJobs = list
	}()

	go func() {
		defer wg.Done()
		active, bytes, err := a.Store.UploadStats(ctx, user.ID)
		if err != nil {
			a.Logger.Warn("dashboard upload stats failed", "err", err)
			return
		}
		transfers = sysTransfers{Active: active, Bytes: bytes}
	}()

	go func() {
		defer wg.Done()
		count, err := a.Store.CountShares(ctx, user.ID)
		if err != nil {
			a.Logger.Warn("dashboard share count failed", "err", err)
			return
		}
		shares = sysShareSummary{Active: count}
	}()

	go func() {
		defer wg.Done()
		count, bytes, err := a.Store.TrashStats(ctx, user.ID)
		if err != nil {
			a.Logger.Warn("dashboard trash stats failed", "err", err)
			return
		}
		trash = sysTrashSummary{Count: count, Bytes: bytes}
	}()

	mounts := a.VFS.Mounts(scope)
	usage := sysUsageBatch(ctx, sysDiskBudget, len(mounts), func(i int) *vfs.DiskUsage {
		du, err := a.VFS.Disk(scope, mounts[i].Path)
		if err != nil {
			return nil
		}
		return du
	})

	wg.Wait()

	views := make([]sysMountView, 0, len(mounts))
	for i, m := range mounts {
		views = append(views, sysMountView{Mount: m, Usage: usage[i]})
	}

	storage := sysStorage{Path: "/"}
	if len(mounts) > 0 {
		storage.Path = mounts[0].Path
		if du := usage[0]; du != nil {
			storage.Total = du.Total
			storage.Used = du.Used
			storage.Free = du.Free
			storage.Percent = du.Percent
		}
	}

	updateAvailable := false
	if rel, ok := sysUpdateCache.peek(); ok && rel != nil {
		updateAvailable = rel.Available
	}

	if recents == nil {
		recents = []*store.Recent{}
	}
	if favorites == nil {
		favorites = []*store.Favorite{}
	}
	if recentJobs == nil {
		recentJobs = []*store.Job{}
	}

	noCache(w)
	writeJSON(w, http.StatusOK, sysDashboard{
		Greeting:        sysGreeting(time.Now()),
		User:            user,
		Storage:         storage,
		Recent:          recents,
		Favorites:       favorites,
		Transfers:       transfers,
		Shares:          shares,
		Jobs:            recentJobs,
		Trash:           trash,
		Mounts:          views,
		Version:         build.Version,
		UpdateAvailable: updateAvailable,
	})
}

// sysRecentJobs prefers the live job registry, which knows counters that have
// not been written to the database yet.
func (a *API) sysRecentJobs(ctx context.Context, userID int64, limit int) ([]*store.Job, error) {
	if a.Jobs != nil {
		return a.Jobs.List(ctx, userID, limit)
	}
	return a.Store.ListJobs(ctx, userID, limit)
}

// sysGreeting picks the salutation for the server local hour.
func sysGreeting(now time.Time) string {
	switch h := now.Hour(); {
	case h >= 5 && h < 12:
		return "Good morning"
	case h >= 12 && h < 17:
		return "Good afternoon"
	case h >= 17 && h < 22:
		return "Good evening"
	default:
		return "Good night"
	}
}

// sysUsageBatch measures n volumes in parallel and gives up on the stragglers
// once the budget is spent. Slots that did not answer stay nil, so the caller
// renders zeros instead of waiting for a disk that is not responding.
func sysUsageBatch(ctx context.Context, budget time.Duration, n int, fn func(i int) *vfs.DiskUsage) []*vfs.DiskUsage {
	out := make([]*vfs.DiskUsage, n)
	if n == 0 {
		return out
	}
	type slot struct {
		idx int
		du  *vfs.DiskUsage
	}
	// The channel is buffered for every worker, so a late answer never leaks
	// a goroutine.
	ch := make(chan slot, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer func() {
				if rec := recover(); rec != nil {
					ch <- slot{idx: i}
				}
			}()
			ch <- slot{idx: i, du: fn(i)}
		}(i)
	}
	deadline, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	for got := 0; got < n; got++ {
		select {
		case res := <-ch:
			out[res.idx] = res.du
		case <-deadline.Done():
			return out
		}
	}
	return out
}

// ---- system information -----------------------------------------------------

type sysHostInfo struct {
	Hostname   string `json:"hostname"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	CPUs       int    `json:"cpus"`
	Goroutines int    `json:"goroutines"`
}

type sysMemoryInfo struct {
	Alloc uint64 `json:"alloc"`
	Sys   uint64 `json:"sys"`
}

type sysDatabaseInfo struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

type sysCounts struct {
	Users  int `json:"users"`
	Shares int `json:"shares"`
	Jobs   int `json:"jobs"`
}

type sysInfoResponse struct {
	Build     build.Info       `json:"build"`
	Uptime    int64            `json:"uptime"`
	PublicURL string           `json:"publicUrl"`
	Host      *sysHostInfo     `json:"host,omitempty"`
	Memory    *sysMemoryInfo   `json:"memory,omitempty"`
	Database  *sysDatabaseInfo `json:"database,omitempty"`
	Counts    *sysCounts       `json:"counts,omitempty"`
}

// handleSystemInfo answers GET /api/v1/system/info. Everyone learns which
// version they are talking to; only administrators see the machine behind it.
func (a *API) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := sysInfoResponse{
		Build:     build.Current(),
		Uptime:    int64(a.Uptime().Seconds()),
		PublicURL: a.Config.PublicURL(),
	}

	if currentUser(r).IsAdmin() {
		hostname, err := os.Hostname()
		if err != nil {
			hostname = "unknown"
		}
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)

		users, err := a.Store.CountUsers(ctx)
		if err != nil {
			a.Logger.Warn("system info user count failed", "err", err)
		}
		shares, err := a.Store.CountShares(ctx, 0)
		if err != nil {
			a.Logger.Warn("system info share count failed", "err", err)
		}

		out.Host = &sysHostInfo{
			Hostname:   hostname,
			OS:         runtime.GOOS,
			Arch:       runtime.GOARCH,
			CPUs:       runtime.NumCPU(),
			Goroutines: runtime.NumGoroutine(),
		}
		out.Memory = &sysMemoryInfo{Alloc: mem.Alloc, Sys: mem.Sys}
		out.Database = &sysDatabaseInfo{Path: a.Store.Path(), Bytes: a.Store.Size()}
		out.Counts = &sysCounts{Users: users, Shares: shares, Jobs: a.sysCountJobs(ctx)}
	}

	noCache(w)
	writeJSON(w, http.StatusOK, out)
}

// sysCountJobs counts the job records. A failure reads as zero, because this
// number is informational only.
func (a *API) sysCountJobs(ctx context.Context) int {
	db := a.Store.DB()
	if db == nil {
		return 0
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs`).Scan(&n); err != nil {
		a.Logger.Warn("system info job count failed", "err", err)
		return 0
	}
	return n
}

// ---- settings ---------------------------------------------------------------

type sysSecuritySettings struct {
	AllowAdvanced   bool     `json:"allowAdvanced"`
	SessionTTLHours int      `json:"sessionTtlHours"`
	LoginRateBurst  int      `json:"loginRateBurst"`
	IPAllowlist     []string `json:"ipAllowlist"`
}

type sysLimitSettings struct {
	MaxUploadSize        int64 `json:"maxUploadSize"`
	UploadChunkSize      int64 `json:"uploadChunkSize"`
	UploadExpiryHours    int   `json:"uploadExpiryHours"`
	SearchMaxResults     int   `json:"searchMaxResults"`
	SearchTimeoutSeconds int   `json:"searchTimeoutSeconds"`
	ZipMaxFiles          int   `json:"zipMaxFiles"`
	TextEditMaxBytes     int64 `json:"textEditMaxBytes"`
	ThumbMaxBytes        int64 `json:"thumbMaxBytes"`
	MaxConcurrentOps     int   `json:"maxConcurrentOps"`
	ListPageSize         int   `json:"listPageSize"`
}

// sysUpdateSettings is the update check as the interface reads it: whether it
// runs, where it is sent and how often. What the request carries is written
// out in docs/UPDATES.md.
type sysUpdateSettings struct {
	Channel  string `json:"channel"`
	Check    bool   `json:"check"`
	Endpoint string `json:"endpoint"`
	Interval string `json:"interval"`
}

type sysServerSettings struct {
	Domain    string `json:"domain"`
	TLSMode   string `json:"tlsMode"`
	Port      int    `json:"port"`
	PublicURL string `json:"publicUrl"`
}

type sysTrashSettings struct {
	RetentionDays int `json:"retentionDays"`
}

type sysSettings struct {
	Branding store.Branding      `json:"branding"`
	Security sysSecuritySettings `json:"security"`
	Limits   sysLimitSettings    `json:"limits"`
	Updates  sysUpdateSettings   `json:"updates"`
	Server   sysServerSettings   `json:"server"`
	Trash    sysTrashSettings    `json:"trash"`
}

// The patch types mirror the readable shape one field at a time, so a client
// may send the whole document back or only the section it changed.

type sysSecurityPatch struct {
	AllowAdvanced   *bool     `json:"allowAdvanced"`
	SessionTTLHours *int      `json:"sessionTtlHours"`
	LoginRateBurst  *int      `json:"loginRateBurst"`
	IPAllowlist     *[]string `json:"ipAllowlist"`
}

type sysLimitPatch struct {
	MaxUploadSize        *int64 `json:"maxUploadSize"`
	UploadChunkSize      *int64 `json:"uploadChunkSize"`
	UploadExpiryHours    *int   `json:"uploadExpiryHours"`
	SearchMaxResults     *int   `json:"searchMaxResults"`
	SearchTimeoutSeconds *int   `json:"searchTimeoutSeconds"`
	ZipMaxFiles          *int   `json:"zipMaxFiles"`
	TextEditMaxBytes     *int64 `json:"textEditMaxBytes"`
	ThumbMaxBytes        *int64 `json:"thumbMaxBytes"`
	MaxConcurrentOps     *int   `json:"maxConcurrentOps"`
	ListPageSize         *int   `json:"listPageSize"`
}

type sysUpdatePatch struct {
	Channel  *string `json:"channel"`
	Check    *bool   `json:"check"`
	Endpoint *string `json:"endpoint"`
	Interval *string `json:"interval"`
}

type sysServerPatch struct {
	Domain  *string `json:"domain"`
	TLSMode *string `json:"tlsMode"`
	Port    *int    `json:"port"`
	// PublicURL is derived from the fields above and ignored on save. It is
	// accepted so a client can send the document it just read.
	PublicURL *string `json:"publicUrl"`
}

type sysTrashPatch struct {
	RetentionDays *int `json:"retentionDays"`
}

type sysSettingsPatch struct {
	Branding *store.Branding   `json:"branding"`
	Security *sysSecurityPatch `json:"security"`
	Limits   *sysLimitPatch    `json:"limits"`
	Updates  *sysUpdatePatch   `json:"updates"`
	Server   *sysServerPatch   `json:"server"`
	Trash    *sysTrashPatch    `json:"trash"`
}

// handleGetSettings answers GET /api/v1/system/settings.
func (a *API) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	current, err := a.sysReadSettings(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	noCache(w)
	writeJSON(w, http.StatusOK, current)
}

// sysReadSettings renders the editable configuration.
func (a *API) sysReadSettings(ctx context.Context) (sysSettings, error) {
	cfg := a.Config

	branding := store.DefaultBranding()
	if _, err := a.Store.GetJSON(ctx, store.SettingBranding, &branding); err != nil {
		return sysSettings{}, err
	}
	channel, err := a.Store.GetSetting(ctx, store.SettingUpdateChannel)
	if err != nil {
		return sysSettings{}, err
	}
	if channel == "" {
		channel = build.Channel
	}
	if channel == "" {
		channel = "stable"
	}

	allowlist := cfg.Security.IPAllowlist
	if allowlist == nil {
		allowlist = []string{}
	}

	return sysSettings{
		Branding: branding,
		Security: sysSecuritySettings{
			AllowAdvanced:   cfg.Security.AllowAdvanced,
			SessionTTLHours: int(cfg.Security.SessionTTL.D().Hours()),
			LoginRateBurst:  cfg.Security.LoginRateBurst,
			IPAllowlist:     allowlist,
		},
		Limits: sysLimitSettings{
			MaxUploadSize:        cfg.Limits.MaxUploadSize,
			UploadChunkSize:      cfg.Limits.UploadChunkSize,
			UploadExpiryHours:    int(cfg.Limits.UploadExpiry.D().Hours()),
			SearchMaxResults:     cfg.Limits.SearchMaxResults,
			SearchTimeoutSeconds: int(cfg.Limits.SearchTimeout.D().Seconds()),
			ZipMaxFiles:          cfg.Limits.ZipMaxFiles,
			TextEditMaxBytes:     cfg.Limits.TextEditMaxBytes,
			ThumbMaxBytes:        cfg.Limits.ThumbMaxBytes,
			MaxConcurrentOps:     cfg.Limits.MaxConcurrentOps,
			ListPageSize:         cfg.Limits.ListPageSize,
		},
		Updates: sysUpdateSettings{
			Channel:  channel,
			Check:    cfg.Updates.Check,
			Endpoint: cfg.Updates.Endpoint,
			Interval: sysIntervalText(cfg.Updates.Interval.D()),
		},
		Server: sysServerSettings{
			Domain:    cfg.Server.Domain,
			TLSMode:   string(cfg.Server.TLS.Mode),
			Port:      cfg.Server.Port,
			PublicURL: cfg.PublicURL(),
		},
		Trash: sysTrashSettings{
			RetentionDays: int(cfg.Limits.TrashRetention.D().Hours() / 24),
		},
	}, nil
}

// handleSaveSettings answers PUT /api/v1/system/settings.
func (a *API) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req sysSettingsPatch
	if err := decode(r, &req); err != nil {
		a.fail(w, r, err)
		return
	}

	// The runtime configuration is edited on a copy, so a rejected document
	// never leaves the server half changed.
	next := *a.Config
	changed := make([]string, 0, 4)

	if s := req.Security; s != nil {
		if s.AllowAdvanced != nil {
			next.Security.AllowAdvanced = *s.AllowAdvanced
		}
		if s.SessionTTLHours != nil {
			if *s.SessionTTLHours < 1 {
				a.fail(w, r, badRequest("The session lifetime must be at least one hour"))
				return
			}
			ttl := config.Duration(time.Duration(*s.SessionTTLHours) * time.Hour)
			if ttl != next.Security.SessionTTL {
				changed = append(changed, "security.sessionTtlHours")
			}
			next.Security.SessionTTL = ttl
		}
		if s.LoginRateBurst != nil {
			if *s.LoginRateBurst < 1 {
				a.fail(w, r, badRequest("The sign in attempt limit must be at least one"))
				return
			}
			if *s.LoginRateBurst != next.Security.LoginRateBurst {
				changed = append(changed, "security.loginRateBurst")
			}
			next.Security.LoginRateBurst = *s.LoginRateBurst
		}
		if s.IPAllowlist != nil {
			list, err := sysCleanAllowlist(*s.IPAllowlist)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			if !sysSameList(list, next.Security.IPAllowlist) {
				changed = append(changed, "security.ipAllowlist")
			}
			next.Security.IPAllowlist = list
		}
	}

	if l := req.Limits; l != nil {
		if err := sysApplyLimits(&next, l); err != nil {
			a.fail(w, r, err)
			return
		}
	}

	if s := req.Server; s != nil {
		if s.Port != nil {
			if *s.Port < 1 || *s.Port > 65535 {
				a.fail(w, r, badRequest("The port must be between 1 and 65535"))
				return
			}
			if *s.Port != next.Server.Port {
				changed = append(changed, "server.port")
			}
			next.Server.Port = *s.Port
		}
		if s.Domain != nil {
			domain := strings.TrimSpace(*s.Domain)
			if domain != "" {
				cleaned, err := sysCleanHost(domain)
				if err != nil {
					a.fail(w, r, err)
					return
				}
				domain = cleaned
			}
			if domain != next.Server.Domain {
				changed = append(changed, "server.domain")
			}
			next.Server.Domain = domain
		}
		if s.TLSMode != nil {
			mode := config.TLSMode(strings.ToLower(strings.TrimSpace(*s.TLSMode)))
			switch mode {
			case config.TLSOff, config.TLSACME, config.TLSManual:
			default:
				a.fail(w, r, badRequest("The certificate mode must be off, acme or manual"))
				return
			}
			if mode != next.Server.TLS.Mode {
				changed = append(changed, "server.tlsMode")
			}
			next.Server.TLS.Mode = mode
			if mode != config.TLSOff && next.Server.Domain != "" {
				next.Security.CookieSecure = true
			}
		}
	}

	if t := req.Trash; t != nil && t.RetentionDays != nil {
		if *t.RetentionDays < 1 {
			a.fail(w, r, badRequest("The recycle bin must keep items for at least one day"))
			return
		}
		next.Limits.TrashRetention = config.Duration(time.Duration(*t.RetentionDays) * 24 * time.Hour)
	}

	// The update check: whether it runs at all, where it is sent and how
	// often. Only the last two need a restart, because the updater is built
	// with them when the service starts.
	if u := req.Updates; u != nil {
		if u.Check != nil {
			next.Updates.Check = *u.Check
		}
		if u.Endpoint != nil {
			endpoint, err := sysCleanEndpoint(*u.Endpoint)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			if endpoint != next.Updates.Endpoint {
				changed = append(changed, "updates.endpoint")
			}
			next.Updates.Endpoint = endpoint
		}
		if u.Interval != nil {
			interval, err := sysCleanInterval(*u.Interval)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			if interval != next.Updates.Interval {
				changed = append(changed, "updates.interval")
			}
			next.Updates.Interval = interval
		}
	}

	// The settings that live in the database are checked here as well, so a
	// document that is rejected halfway can never leave the configuration file
	// ahead of the stored values.
	var branding *store.Branding
	if b := req.Branding; b != nil {
		cleaned, err := sysCleanBranding(*b)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		branding = &cleaned
	}
	channel := ""
	if u := req.Updates; u != nil && u.Channel != nil {
		channel = strings.ToLower(strings.TrimSpace(*u.Channel))
		if channel != "stable" && channel != "beta" {
			a.fail(w, r, badRequest("The update channel must be stable or beta"))
			return
		}
		// The stored value is what the updater follows. The file carries the
		// same track so it reads as the truth rather than as a leftover.
		next.Updates.Channel = channel
	}

	if err := next.Validate(); err != nil {
		a.audit(r, "settings.save", "settings", err.Error(), false)
		a.fail(w, r, badRequest(sysExplainConfig(err)))
		return
	}
	if err := a.sysSaveConfig(&next); err != nil {
		a.audit(r, "settings.save", "settings", err.Error(), false)
		a.fail(w, r, err)
		return
	}
	*a.Config = next

	if branding != nil {
		if err := a.Store.SetJSON(ctx, store.SettingBranding, branding); err != nil {
			a.fail(w, r, err)
			return
		}
	}
	if channel != "" {
		if err := a.Store.SetSetting(ctx, store.SettingUpdateChannel, channel); err != nil {
			a.fail(w, r, err)
			return
		}
	}

	saved, err := a.sysReadSettings(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}

	detail := "no restart needed"
	if len(changed) > 0 {
		detail = "restart needed for " + strings.Join(changed, ", ")
	}
	a.audit(r, "settings.save", "settings", detail, true)

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":              true,
		"restartRequired": len(changed) > 0,
		"changed":         changed,
		"settings":        saved,
	})
}

// sysApplyLimits copies the limit patch onto the configuration copy. Every
// value is validated before anything is written, so a rejected document leaves
// the copy untouched.
func sysApplyLimits(next *config.Config, l *sysLimitPatch) error {
	// Zero means "no ceiling" for the upload size, and only for that one.
	if l.MaxUploadSize != nil && *l.MaxUploadSize < 0 {
		return badRequest("The maximum upload size cannot be negative")
	}
	if err := sysAtLeastOne64("The upload chunk size", l.UploadChunkSize); err != nil {
		return err
	}
	if err := sysAtLeastOne("The unfinished upload lifetime", l.UploadExpiryHours); err != nil {
		return err
	}
	if err := sysAtLeastOne("The search result ceiling", l.SearchMaxResults); err != nil {
		return err
	}
	if err := sysAtLeastOne("The search time limit", l.SearchTimeoutSeconds); err != nil {
		return err
	}
	if err := sysAtLeastOne("The archive file ceiling", l.ZipMaxFiles); err != nil {
		return err
	}
	if err := sysAtLeastOne64("The text editor size limit", l.TextEditMaxBytes); err != nil {
		return err
	}
	if err := sysAtLeastOne64("The preview size limit", l.ThumbMaxBytes); err != nil {
		return err
	}
	if err := sysAtLeastOne("The number of operations running at once", l.MaxConcurrentOps); err != nil {
		return err
	}
	if err := sysAtLeastOne("The folder page size", l.ListPageSize); err != nil {
		return err
	}

	if l.MaxUploadSize != nil {
		next.Limits.MaxUploadSize = *l.MaxUploadSize
	}
	if l.UploadChunkSize != nil {
		next.Limits.UploadChunkSize = *l.UploadChunkSize
	}
	if l.UploadExpiryHours != nil {
		next.Limits.UploadExpiry = config.Duration(time.Duration(*l.UploadExpiryHours) * time.Hour)
	}
	if l.SearchMaxResults != nil {
		next.Limits.SearchMaxResults = *l.SearchMaxResults
	}
	if l.SearchTimeoutSeconds != nil {
		next.Limits.SearchTimeout = config.Duration(time.Duration(*l.SearchTimeoutSeconds) * time.Second)
	}
	if l.ZipMaxFiles != nil {
		next.Limits.ZipMaxFiles = *l.ZipMaxFiles
	}
	if l.TextEditMaxBytes != nil {
		next.Limits.TextEditMaxBytes = *l.TextEditMaxBytes
	}
	if l.ThumbMaxBytes != nil {
		next.Limits.ThumbMaxBytes = *l.ThumbMaxBytes
	}
	if l.MaxConcurrentOps != nil {
		next.Limits.MaxConcurrentOps = *l.MaxConcurrentOps
	}
	if l.ListPageSize != nil {
		next.Limits.ListPageSize = *l.ListPageSize
	}
	return nil
}

// sysAtLeastOne refuses a limit that would switch a feature off by accident.
func sysAtLeastOne(label string, v *int) error {
	if v != nil && *v < 1 {
		return badRequest(label + " must be at least one")
	}
	return nil
}

// sysAtLeastOne64 is sysAtLeastOne for byte counts.
func sysAtLeastOne64(label string, v *int64) error {
	if v != nil && *v < 1 {
		return badRequest(label + " must be at least one")
	}
	return nil
}

// sysCleanEndpoint validates where the update check is sent. Anything that is
// not a full http or https address is refused, because a half written address
// would quietly turn the check into a failure every six hours.
func sysCleanEndpoint(raw string) (string, error) {
	endpoint := strings.TrimSpace(raw)
	invalid := badRequest("The update address must be a full http or https address, for example " +
		config.DefaultUpdateEndpoint)
	if endpoint == "" {
		return "", invalid
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return "", invalid
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", invalid
	}
	return endpoint, nil
}

// sysCleanInterval reads how often the check may run.
func sysCleanInterval(raw string) (config.Duration, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return 0, badRequest("Say how often the check may run, as a length of time such as 6h")
	}
	interval, err := time.ParseDuration(text)
	if err != nil {
		return 0, badRequest("The update check interval must be a length of time such as 6h or 24h")
	}
	if interval < sysUpdateMinInterval {
		return 0, badRequest("The update check must not run more often than once an hour")
	}
	return config.Duration(interval), nil
}

// sysIntervalText writes a duration the short way, so six hours reads as 6h
// rather than as 6h0m0s. Only trailing zero parts are dropped, so 1h30m keeps
// its minutes.
func sysIntervalText(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	text := d.String()
	if strings.HasSuffix(text, "m0s") {
		text = strings.TrimSuffix(text, "0s")
	}
	if strings.HasSuffix(text, "h0m") {
		text = strings.TrimSuffix(text, "0m")
	}
	return text
}

// sysSaveConfig writes the configuration file, turning the two failures an
// operator can actually act on into plain answers.
func (a *API) sysSaveConfig(cfg *config.Config) error {
	if cfg.Path() == "" {
		return apiError(http.StatusConflict, "no_config_file",
			"Storix does not know where its configuration file is, so this change cannot be saved")
	}
	if err := cfg.Save(); err != nil {
		a.Logger.Error("save configuration failed", "path", cfg.Path(), "err", err)
		if errors.Is(err, os.ErrPermission) {
			return apiError(http.StatusForbidden, "config_not_writable",
				"The configuration file cannot be written by the service account")
		}
		return apiError(http.StatusInternalServerError, "config_save_failed",
			"The configuration file could not be written")
	}
	return nil
}

// sysExplainConfig turns a configuration validation error into a sentence an
// operator can act on.
func sysExplainConfig(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "acme requires"):
		return "Automatic certificates need a domain name, set one first"
	case strings.Contains(msg, "manual requires"):
		return "Manual certificates need a certificate file and a key file"
	case strings.Contains(msg, "server.port"):
		return "The port must be between 1 and 65535"
	case strings.Contains(msg, "updates.interval"):
		return "The update check must not run more often than once an hour"
	}
	return "That combination of settings cannot be used"
}

// sysCleanBranding trims the identity fields and refuses anything that would
// end up as active content in the interface.
func sysCleanBranding(in store.Branding) (store.Branding, error) {
	out := store.Branding{
		Name:       strings.TrimSpace(in.Name),
		Tagline:    strings.TrimSpace(in.Tagline),
		LogoURL:    strings.TrimSpace(in.LogoURL),
		AccentFrom: strings.TrimSpace(in.AccentFrom),
		AccentTo:   strings.TrimSpace(in.AccentTo),
		Footer:     strings.TrimSpace(in.Footer),
	}
	def := store.DefaultBranding()
	if out.Name == "" {
		out.Name = def.Name
	}
	if len(out.Name) > 60 || len(out.Tagline) > 120 || len(out.Footer) > 200 {
		return out, badRequest("The branding text is too long")
	}
	if out.AccentFrom == "" {
		out.AccentFrom = def.AccentFrom
	}
	if out.AccentTo == "" {
		out.AccentTo = def.AccentTo
	}
	if !sysValidColor(out.AccentFrom) || !sysValidColor(out.AccentTo) {
		return out, badRequest("The accent colours must be hexadecimal, for example #00D4FF")
	}
	if !sysValidLogo(out.LogoURL) {
		return out, badRequest("The logo must be an address starting with https, a data image or a path inside Storix")
	}
	if out.Footer == "" {
		out.Footer = def.Footer
	}
	return out, nil
}

// sysValidColor accepts an empty value or a three or six digit hex colour.
func sysValidColor(v string) bool {
	if v == "" {
		return true
	}
	if v[0] != '#' || (len(v) != 4 && len(v) != 7) {
		return false
	}
	for i := 1; i < len(v); i++ {
		c := v[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			continue
		}
		return false
	}
	return true
}

// sysValidLogo keeps script bearing URLs out of the interface.
func sysValidLogo(v string) bool {
	if v == "" {
		return true
	}
	if len(v) > 2048 {
		return false
	}
	lower := strings.ToLower(v)
	switch {
	case strings.HasPrefix(lower, "https://"), strings.HasPrefix(lower, "http://"):
		return true
	case strings.HasPrefix(lower, "data:image/"):
		return true
	case strings.HasPrefix(v, "/") && !strings.HasPrefix(v, "//"):
		return true
	}
	return false
}

// sysCleanAllowlist validates every entry as an address or a network.
func sysCleanAllowlist(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, raw := range in {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(entry); err == nil {
			out = append(out, entry)
			continue
		}
		if ip := net.ParseIP(entry); ip != nil {
			out = append(out, entry)
			continue
		}
		return nil, badRequest("This is not an address or a network: " + truncate(entry, 60))
	}
	return out, nil
}

// sysSameList reports whether two string lists hold the same entries in the
// same order.
func sysSameList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---- roots ------------------------------------------------------------------

// sysRootView is a configured tree with the state of the folder behind it.
type sysRootView struct {
	*store.Root
	Exists bool           `json:"exists"`
	Usage  *vfs.DiskUsage `json:"usage"`
}

// handleListRootsAdmin answers GET /api/v1/system/roots.
func (a *API) handleListRootsAdmin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	roots, err := a.Store.ListRoots(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}

	usage := sysUsageBatch(ctx, sysDiskBudget, len(roots), func(i int) *vfs.DiskUsage {
		du, err := a.sysRootUsage(roots[i].Path)
		if err != nil {
			return nil
		}
		return du
	})

	views := make([]sysRootView, 0, len(roots))
	for i, root := range roots {
		info, err := os.Stat(filepath.FromSlash(root.Path))
		views = append(views, sysRootView{
			Root:   root,
			Exists: err == nil && info.IsDir(),
			Usage:  usage[i],
		})
	}

	noCache(w)
	writeJSON(w, http.StatusOK, map[string]any{"roots": views, "total": len(views)})
}

// sysRootUsage measures a configured tree. Roots are not necessarily inside
// the caller scope, so the measurement runs against the tree itself.
func (a *API) sysRootUsage(p string) (*vfs.DiskUsage, error) {
	scope := vfs.Scope{Mounts: []vfs.Mount{{Path: vfs.Clean(p)}}, Admin: true}
	return a.VFS.Disk(scope, p)
}

type sysRootRequest struct {
	Path     string `json:"path"`
	Label    string `json:"label"`
	Icon     string `json:"icon"`
	ReadOnly bool   `json:"readOnly"`
}

// handleAddRoot answers POST /api/v1/system/roots.
func (a *API) handleAddRoot(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req sysRootRequest
	if err := decode(r, &req); err != nil {
		a.fail(w, r, err)
		return
	}

	target := vfs.Clean(req.Path)
	if target == "" || !sysAbsolute(target) {
		a.fail(w, r, badRequest("Enter the full path of the folder, starting at the root of the server"))
		return
	}
	info, err := os.Stat(filepath.FromSlash(target))
	switch {
	case errors.Is(err, os.ErrNotExist):
		a.audit(r, "root.add", target, "missing", false)
		a.fail(w, r, badRequest("There is no folder at that path"))
		return
	case errors.Is(err, os.ErrPermission):
		a.audit(r, "root.add", target, "unreadable", false)
		a.fail(w, r, apiError(http.StatusForbidden, "denied", "Storix cannot read that folder"))
		return
	case err != nil:
		a.fail(w, r, err)
		return
	case !info.IsDir():
		a.fail(w, r, badRequest("That path is a file, choose a folder"))
		return
	}
	if a.VFS.Denied(target) {
		a.audit(r, "root.add", target, "protected", false)
		a.fail(w, r, apiError(http.StatusForbidden, "denied", "That path is protected and cannot be added"))
		return
	}
	if dataDir := vfs.Clean(a.Config.Storage.DataDir); dataDir != "" && vfs.Contains(target, dataDir) {
		a.audit(r, "root.add", target, "contains data directory", false)
		a.fail(w, r, badRequest("That folder holds the Storix data directory, choose a different one"))
		return
	}

	existing, err := a.Store.ListRoots(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	for _, root := range existing {
		if vfs.Clean(root.Path) == target {
			a.fail(w, r, conflict("That folder is already available in Storix"))
			return
		}
	}

	label := strings.TrimSpace(req.Label)
	if label == "" {
		label = path.Base(target)
		if target == "/" {
			label = "Root volume"
		}
	}
	icon := strings.TrimSpace(req.Icon)
	if icon == "" {
		icon = "drive"
	}

	root := &store.Root{
		Path:      target,
		Label:     truncate(label, 60),
		Icon:      truncate(icon, 40),
		ReadOnly:  req.ReadOnly,
		SortOrder: len(existing),
	}
	id, err := a.Store.CreateRoot(ctx, root)
	if err != nil {
		a.audit(r, "root.add", target, err.Error(), false)
		a.fail(w, r, err)
		return
	}
	root.ID = id
	// Drop any handle left over from an earlier life of this path.
	a.VFS.Forget(target)
	a.audit(r, "root.add", target, root.Label, true)

	writeJSON(w, http.StatusCreated, map[string]any{
		"root":    root,
		"message": "The folder is now available in Storix",
	})
}

type sysRootPatch struct {
	Label    *string `json:"label"`
	Icon     *string `json:"icon"`
	ReadOnly *bool   `json:"readOnly"`
}

// handleUpdateRoot answers PATCH /api/v1/system/roots/{id}. The path of a tree
// never changes: an operator who wants another folder adds it and removes this
// one, which keeps user mounts honest.
func (a *API) handleUpdateRoot(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := idParam(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var req sysRootPatch
	if err := decode(r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	root, err := a.Store.GetRoot(ctx, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}

	dirty := false
	if req.Label != nil {
		label := truncate(strings.TrimSpace(*req.Label), 60)
		if label == "" {
			label = path.Base(root.Path)
		}
		if label != root.Label {
			root.Label = label
			dirty = true
		}
	}
	if req.Icon != nil {
		icon := truncate(strings.TrimSpace(*req.Icon), 40)
		if icon == "" {
			icon = "folder"
		}
		if icon != root.Icon {
			root.Icon = icon
			dirty = true
		}
	}
	if req.ReadOnly != nil && *req.ReadOnly != root.ReadOnly {
		root.ReadOnly = *req.ReadOnly
		dirty = true
	}

	if !dirty {
		writeJSON(w, http.StatusOK, map[string]any{"root": root, "changed": false})
		return
	}
	if err := a.Store.UpdateRoot(ctx, root); err != nil {
		a.audit(r, "root.update", root.Path, err.Error(), false)
		a.fail(w, r, err)
		return
	}
	// The cached handle carries the old flags with it.
	a.VFS.Forget(root.Path)
	a.audit(r, "root.update", root.Path, root.Label, true)

	writeJSON(w, http.StatusOK, map[string]any{"root": root, "changed": true})
}

// handleDeleteRoot answers DELETE /api/v1/system/roots/{id}.
func (a *API) handleDeleteRoot(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := idParam(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	root, err := a.Store.GetRoot(ctx, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	count, err := a.Store.CountRoots(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if count <= 1 {
		a.fail(w, r, conflict("This is the only folder Storix can reach, add another one before removing it"))
		return
	}
	if err := a.Store.DeleteRoot(ctx, id); err != nil {
		a.audit(r, "root.remove", root.Path, err.Error(), false)
		a.fail(w, r, err)
		return
	}
	a.VFS.Forget(root.Path)
	a.audit(r, "root.remove", root.Path, root.Label, true)

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": "The folder was removed from Storix. Nothing on disk was deleted.",
	})
}

// ---- server directory picker ------------------------------------------------

type sysBrowseDir struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Readable bool   `json:"readable"`
}

// sysBrowseLimit caps one page of the picker.
const sysBrowseLimit = 500

// handleBrowseServer answers GET /api/v1/system/browse. It deliberately reads
// outside the mount scope, because the operator is choosing a folder that is
// not mounted yet, so it is restricted to administrators and to folder names.
func (a *API) handleBrowseServer(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if !user.IsAdmin() {
		a.audit(r, "admin.denied", r.URL.Path, "browse", false)
		a.fail(w, r, errForbidden)
		return
	}

	target := vfs.Clean(r.URL.Query().Get("path"))
	if target == "" {
		target = "/"
	}
	if a.VFS.Denied(target) {
		a.fail(w, r, apiError(http.StatusForbidden, "denied", "That path is protected"))
		return
	}

	osPath := filepath.FromSlash(target)
	info, err := os.Stat(osPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		a.fail(w, r, errNotFound)
		return
	case errors.Is(err, os.ErrPermission):
		a.fail(w, r, apiError(http.StatusForbidden, "denied", "Storix cannot read that folder"))
		return
	case err != nil:
		a.fail(w, r, err)
		return
	case !info.IsDir():
		a.fail(w, r, badRequest("That path is not a folder"))
		return
	}

	entries, err := os.ReadDir(osPath)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			a.fail(w, r, apiError(http.StatusForbidden, "denied", "Storix cannot read that folder"))
			return
		}
		a.fail(w, r, err)
		return
	}

	dirs := make([]sysBrowseDir, 0, 32)
	truncated := false
	for _, entry := range entries {
		if len(dirs) >= sysBrowseLimit {
			truncated = true
			break
		}
		child := path.Join(target, entry.Name())
		if !sysIsDir(entry, osPath) {
			continue
		}
		if a.VFS.Denied(child) {
			continue
		}
		dirs = append(dirs, sysBrowseDir{
			Name:     entry.Name(),
			Path:     child,
			Readable: sysReadable(filepath.Join(osPath, entry.Name())),
		})
	}
	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name) })

	parent := path.Dir(target)
	if target == "/" {
		parent = "/"
	}

	noCache(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"path":      target,
		"parent":    parent,
		"dirs":      dirs,
		"truncated": truncated,
	})
}

// sysIsDir reports whether a directory entry leads to a folder, following a
// symbolic link once so linked volumes can be picked.
func sysIsDir(entry os.DirEntry, parent string) bool {
	if entry.IsDir() {
		return true
	}
	if entry.Type()&os.ModeSymlink == 0 {
		return false
	}
	info, err := os.Stat(filepath.Join(parent, entry.Name()))
	return err == nil && info.IsDir()
}

// sysReadable reports whether the service account may list a folder.
func sysReadable(osPath string) bool {
	f, err := os.Open(osPath)
	if err != nil {
		return false
	}
	defer f.Close()
	// An empty folder answers with io.EOF, which still counts as readable.
	if _, err := f.ReadDir(1); err != nil && !errors.Is(err, io.EOF) {
		return false
	}
	return true
}

// ---- audit log --------------------------------------------------------------

// handleAudit answers GET /api/v1/system/audit.
func (a *API) handleAudit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	limit := queryInt(r, "limit", 100)
	if limit < 1 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	offset := queryInt(r, "offset", 0)
	if offset < 0 {
		offset = 0
	}

	filter := store.AuditFilter{
		Action: strings.TrimSpace(r.URL.Query().Get("action")),
		Query:  strings.TrimSpace(r.URL.Query().Get("q")),
		Limit:  limit,
		Offset: offset,
	}

	if who := strings.TrimSpace(r.URL.Query().Get("user")); who != "" {
		if id, err := strconv.ParseInt(who, 10, 64); err == nil && id > 0 {
			filter.UserID = id
		} else {
			user, err := a.Store.GetUserByName(ctx, who)
			switch {
			case errors.Is(err, store.ErrNotFound):
				writeJSON(w, http.StatusOK, map[string]any{
					"entries": []*store.AuditEntry{}, "total": 0, "limit": limit, "offset": offset,
				})
				return
			case err != nil:
				a.fail(w, r, err)
				return
			}
			filter.UserID = user.ID
		}
	}

	entries, err := a.Store.ListAudit(ctx, filter)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	total, err := a.Store.CountAudit(ctx, filter)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if entries == nil {
		entries = []*store.AuditEntry{}
	}

	noCache(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries,
		"total":   total,
		"limit":   limit,
		"offset":  offset,
	})
}

// ---- updates ----------------------------------------------------------------

// handleUpdateCheck answers GET /api/v1/system/update/check.
func (a *API) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if a.Updater == nil {
		a.fail(w, r, apiError(http.StatusServiceUnavailable, "updates_unavailable",
			"This installation does not manage its own updates"))
		return
	}
	rel, err := sysUpdateCache.fetch(r.Context(), a.Updater)
	if err != nil {
		a.Logger.Warn("update check failed", "err", err)
		a.fail(w, r, apiError(http.StatusBadGateway, "update_check_failed",
			"Storix could not reach the release service"))
		return
	}
	noCache(w)
	writeJSON(w, http.StatusOK, rel)
}

// handleUpdateApply answers POST /api/v1/system/update.
func (a *API) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if a.Updater == nil {
		a.fail(w, r, apiError(http.StatusServiceUnavailable, "updates_unavailable",
			"This installation does not manage its own updates"))
		return
	}
	if a.Jobs == nil {
		a.fail(w, r, apiError(http.StatusServiceUnavailable, "jobs_unavailable",
			"Background operations are not available right now"))
		return
	}
	if !a.Updater.Writable() {
		a.audit(r, "system.update", "update", "binary not writable", false)
		a.fail(w, r, apiError(http.StatusConflict, "not_writable",
			"Run sudo storix update on the server to install this version"))
		return
	}

	userID := currentUser(r).ID
	up := a.Updater

	job, err := a.Jobs.Submit(r.Context(), userID, "update", "Install update", nil,
		func(ctx context.Context, j *jobs.Job) error {
			j.SetMessage("Checking for a new version")
			checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			rel, err := up.Check(checkCtx)
			cancel()
			if err != nil {
				return err
			}
			if rel == nil || !rel.Available {
				j.SetMessage("Storix is already the newest version")
				j.SetResult(rel)
				return nil
			}
			if !rel.Writable {
				return errors.New("Run sudo storix update on the server to install this version")
			}
			sysUpdateCache.remember(rel)

			j.SetTotal(rel.Size, 0)
			j.SetMessage("Downloading version " + rel.Version)
			if err := up.Apply(ctx, rel, func(done, total int64) {
				j.Progress(done, 0, "")
			}); err != nil {
				return err
			}
			j.SetResult(rel)
			j.SetMessage("Update installed, restart the service to run it")
			a.publish(userID, events.EventSystemNotice, map[string]any{
				"level":   "success",
				"message": "Update installed, restart the service to run it",
				"version": rel.Version,
			})
			return nil
		})
	if err != nil {
		a.audit(r, "system.update", "update", err.Error(), false)
		a.fail(w, r, err)
		return
	}
	a.audit(r, "system.update", "update", "started", true)

	writeJSON(w, http.StatusAccepted, map[string]any{
		"job":     job,
		"message": "The update is running in the background",
	})
}

// ---- domain and certificates ------------------------------------------------

type sysDomainRequest struct {
	Domain string `json:"domain"`
	Email  string `json:"email"`
	Enable bool   `json:"enable"`
}

// handleSetDomain answers POST /api/v1/system/domain.
func (a *API) handleSetDomain(w http.ResponseWriter, r *http.Request) {
	var req sysDomainRequest
	if err := decode(r, &req); err != nil {
		a.fail(w, r, err)
		return
	}
	domain, err := sysCleanHost(req.Domain)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	email := strings.TrimSpace(req.Email)
	if email != "" && !sysValidEmail(email) {
		a.fail(w, r, badRequest("That is not a valid email address"))
		return
	}

	next := *a.Config
	next.Server.Domain = domain
	if email != "" {
		next.Server.TLS.Email = email
	}
	if req.Enable {
		next.Server.TLS.Mode = config.TLSACME
		next.Security.CookieSecure = true
	} else {
		next.Server.TLS.Mode = config.TLSOff
		next.Security.CookieSecure = false
	}

	if err := next.Validate(); err != nil {
		a.fail(w, r, badRequest(sysExplainConfig(err)))
		return
	}
	if err := a.sysSaveConfig(&next); err != nil {
		a.audit(r, "system.domain", domain, err.Error(), false)
		a.fail(w, r, err)
		return
	}
	*a.Config = next

	scheme := "http"
	message := "The domain was saved. Storix will keep serving plain HTTP until certificates are turned on."
	if req.Enable {
		scheme = "https"
		message = "The domain was saved. Point the DNS record for " + domain +
			" at this server and make sure ports 80 and 443 can be reached from the internet, then restart Storix so it can request the certificate."
	}
	a.audit(r, "system.domain", domain, fmt.Sprintf("tls=%s", next.Server.TLS.Mode), true)

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":              true,
		"restartRequired": true,
		"url":             scheme + "://" + domain,
		"message":         message,
	})
}

// sysCleanHost validates a bare host name: letters, digits, dots and dashes,
// at least one dot, no scheme and no path.
func sysCleanHost(raw string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	invalid := badRequest("Enter the domain name on its own, for example files.example.com")
	if name == "" {
		return "", invalid
	}
	if strings.Contains(name, "://") || strings.ContainsAny(name, "/\\ :?#@") {
		return "", invalid
	}
	name = strings.TrimSuffix(name, ".")
	if len(name) > 253 || !strings.Contains(name, ".") {
		return "", invalid
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 {
			return "", invalid
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return "", invalid
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			switch {
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
			default:
				return "", invalid
			}
		}
	}
	return name, nil
}

// sysValidEmail is a deliberately forgiving check: the address only has to be
// something a certificate authority can send a notice to.
func sysValidEmail(v string) bool {
	if len(v) > 254 || strings.ContainsAny(v, " \t\r\n") {
		return false
	}
	at := strings.LastIndex(v, "@")
	if at <= 0 || at == len(v)-1 {
		return false
	}
	return strings.Contains(v[at+1:], ".")
}

// sysAbsolute reports whether a cleaned virtual path starts at a volume root.
func sysAbsolute(p string) bool {
	if strings.HasPrefix(p, "/") {
		return true
	}
	// A Windows drive prefix, so the server can also be run locally.
	return len(p) > 2 && p[1] == ':' && p[2] == '/'
}
