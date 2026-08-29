// Command storix-updates receives the Storix update check and counts the
// servers that make it.
//
// PRIVACY, stated here so anyone reading the file to check the claim finds it
// first. This service never reads, logs or stores the address of a caller.
// There is no r.RemoteAddr in this program, no X-Forwarded-For, no other
// header naming a client, and no access log of any kind. What it keeps is one
// row per instance identifier with the version and platform that identifier
// last reported, and nothing else. The identifier itself is never written to a
// log either: log lines carry counts and errors, never an identity. Rows unseen
// for the retention period are deleted. The promise this implements is in
// docs/UPDATES.md, and this file is the whole of what happens to a check.
//
// Storix - modern web file manager for servers.
// Developed by X Project.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Defaults. The listener is on loopback because this belongs behind a reverse
// proxy that terminates TLS.
const (
	defaultAddr      = "127.0.0.1:8787"
	defaultDB        = "/var/lib/storix-updates/updates.db"
	defaultRepo      = "XProject25/Storix"
	defaultRetention = 180
)

// Request handling limits.
const (
	// checkBodyLimit is generous for a document of four short fields.
	checkBodyLimit = 4 << 10
	// bodyLimit is the ceiling on any request body reaching this service.
	bodyLimit = 1 << 20
	// releaseTTL is how long a release is answered from memory, so a thousand
	// servers checking in do not become a thousand GitHub calls.
	releaseTTL = 5 * time.Minute
	// releaseRetry is the floor between two GitHub calls after a failed one.
	releaseRetry = 30 * time.Second
	// userAgent identifies this service to GitHub.
	userAgent = "storix-updates (+https://github.com/XProject25/Storix)"
)

// tokenEnv names the environment variable holding the statistics token.
const tokenEnv = "STORIX_UPDATES_TOKEN"

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "storix-updates: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("storix-updates", flag.ContinueOnError)
	addr := fs.String("addr", defaultAddr, "address to listen on, meant to sit behind a reverse proxy")
	dbPath := fs.String("db", defaultDB, "SQLite database file")
	repo := fs.String("repo", defaultRepo, "GitHub owner/name the releases are published under")
	retention := fs.Int("retention", defaultRetention, "delete instances unseen for this many days")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *retention < 1 {
		return errors.New("retention must be at least one day")
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	store, err := OpenStore(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	svc := &service{
		store:      store,
		releases:   newReleaseCache(*repo, logger),
		statsToken: strings.TrimSpace(os.Getenv(tokenEnv)),
		retention:  time.Duration(*retention) * 24 * time.Hour,
		logger:     logger,
	}
	if svc.statsToken == "" {
		logger.Warn("statistics are switched off because " + tokenEnv + " is not set")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/check", svc.handleCheck)
	mux.HandleFunc("/v1/stats", svc.handleStats)
	mux.HandleFunc("/healthz", svc.handleHealth)
	mux.HandleFunc("/", svc.handleNotFound)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           limitBody(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 16,
		ErrorLog:          slog.NewLogLogger(discardHandler{}, slog.LevelError),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		svc.maintain(ctx)
	}()

	errc := make(chan error, 1)
	go func() {
		logger.Info("update service listening", "addr", *addr, "db", store.Path(),
			"repo", *repo, "retention_days", *retention)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
			return
		}
		errc <- nil
	}()

	select {
	case err := <-errc:
		stop()
		wg.Wait()
		return err
	case <-ctx.Done():
	}

	logger.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		logger.Warn("shutdown was not clean", "err", err)
	}
	wg.Wait()
	return nil
}

// discardHandler silences the standard library server log. Its lines carry
// client addresses, which this service does not record anywhere.
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (h discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return h }
func (h discardHandler) WithGroup(string) slog.Handler           { return h }

// service holds what the handlers need.
type service struct {
	store      *Store
	releases   *releaseCache
	statsToken string
	retention  time.Duration
	logger     *slog.Logger
}

// ---- protocol ---------------------------------------------------------------

// checkRequest is the document a Storix server posts. Fields it does not
// declare are ignored rather than stored, so a future client can add one
// without this service keeping it by accident.
type checkRequest struct {
	Instance string `json:"instance"`
	Version  string `json:"version"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Channel  string `json:"channel"`
}

// checkResponse is the answer. A caller already on the newest version gets the
// version alone, because there is nothing else worth saying.
type checkResponse struct {
	Version     string     `json:"version"`
	Notes       string     `json:"notes,omitempty"`
	URL         string     `json:"url,omitempty"`
	Asset       string     `json:"asset,omitempty"`
	AssetURL    string     `json:"assetUrl,omitempty"`
	ChecksumURL string     `json:"checksumUrl,omitempty"`
	PublishedAt *time.Time `json:"publishedAt,omitempty"`
}

// errorResponse is what every failure looks like, on every route.
type errorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// ---- handlers ---------------------------------------------------------------

// handleCheck answers POST /v1/check.
func (s *service) handleCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "This endpoint accepts POST")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, checkBodyLimit)

	var req checkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "body_too_large",
				"An update check is a few hundred bytes, this one was larger")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_body", "The request body is not a valid update check")
		return
	}

	in := CheckIn{
		Instance: strings.TrimSpace(req.Instance),
		Version:  strings.TrimPrefix(strings.TrimSpace(req.Version), "v"),
		OS:       strings.ToLower(strings.TrimSpace(req.OS)),
		Arch:     strings.ToLower(strings.TrimSpace(req.Arch)),
		Channel:  strings.ToLower(strings.TrimSpace(req.Channel)),
	}
	if in.Channel == "" {
		in.Channel = ChannelStable
	}
	if err := in.Validate(); err != nil {
		switch {
		case errors.Is(err, ErrInvalidInstance):
			writeError(w, http.StatusBadRequest, "invalid_instance",
				"The instance identifier must be 32 lower case hexadecimal characters")
		case errors.Is(err, ErrInvalidVersion):
			writeError(w, http.StatusBadRequest, "invalid_version",
				"The version is missing or is not a version number")
		case errors.Is(err, ErrInvalidPlatform):
			writeError(w, http.StatusBadRequest, "invalid_platform",
				"The os and arch values are missing or are not platform names")
		default:
			writeError(w, http.StatusBadRequest, "invalid_request",
				"The request is not a valid update check")
		}
		return
	}

	// Counting is not what the caller asked for, so a storage failure is
	// logged and the answer still goes out. The log line carries the error,
	// never the identifier that failed to store.
	if err := s.store.Record(r.Context(), in, time.Now()); err != nil {
		s.logger.Error("could not record a check in", "err", err)
	}

	rel, err := s.releases.get(r.Context(), in.Channel)
	if err != nil {
		// Answering 503 rather than "you are current" matters: the caller
		// falls back to GitHub instead of believing it has the newest build.
		writeError(w, http.StatusServiceUnavailable, "release_unavailable",
			"The release list is not available right now")
		return
	}

	writeJSON(w, http.StatusOK, rel.answer(in))
}

// handleStats answers GET /v1/stats.
func (s *service) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "This endpoint accepts GET")
		return
	}
	if s.statsToken == "" {
		// Refused entirely rather than served openly: how many servers run
		// Storix is the operator's figure, not a public one.
		writeError(w, http.StatusServiceUnavailable, "stats_disabled",
			"Statistics are not enabled on this service")
		return
	}
	if !s.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="storix-updates"`)
		writeError(w, http.StatusUnauthorized, "unauthorized", "A valid token is required")
		return
	}
	stats, err := s.store.Stats(r.Context(), time.Now())
	if err != nil {
		s.logger.Error("could not read statistics", "err", err)
		writeError(w, http.StatusInternalServerError, "stats_failed", "The statistics could not be read")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// authorized compares the presented token in constant time. Both "Bearer x"
// and a bare "x" are accepted, because both are what people type.
func (s *service) authorized(r *http.Request) bool {
	presented := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(presented) > 7 && strings.EqualFold(presented[:7], "bearer ") {
		presented = strings.TrimSpace(presented[7:])
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(s.statsToken)) == 1
}

// handleHealth answers GET /healthz for a monitor.
func (s *service) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "This endpoint accepts GET")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, "ok\n")
}

// handleNotFound answers everything else.
func (s *service) handleNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "not_found", "There is nothing at this address")
}

// limitBody caps every request body reaching the service.
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, bodyLimit)
		}
		next.ServeHTTP(w, r)
	})
}

// writeJSON sends a document with the usual headers.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError sends a failure in the same shape everywhere.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Error: code, Message: message})
}

// ---- maintenance ------------------------------------------------------------

// maintain runs the janitor daily and logs a summary now and then. The summary
// is the only periodic line, and it holds counts only.
func (s *service) maintain(ctx context.Context) {
	s.prune(ctx)
	s.summarise(ctx)

	janitor := time.NewTicker(24 * time.Hour)
	defer janitor.Stop()
	heartbeat := time.NewTicker(6 * time.Hour)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-janitor.C:
			s.prune(ctx)
		case <-heartbeat.C:
			s.summarise(ctx)
		}
	}
}

// prune deletes what is past retention.
func (s *service) prune(ctx context.Context) {
	callCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	removed, err := s.store.Prune(callCtx, s.retention, time.Now())
	if err != nil {
		s.logger.Error("could not delete old instances", "err", err)
		return
	}
	if removed > 0 {
		s.logger.Info("deleted instances past retention", "removed", removed,
			"retention_days", int(s.retention.Hours()/24))
	}
}

// summarise logs the counts. No identifier appears in it.
func (s *service) summarise(ctx context.Context) {
	callCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	stats, err := s.store.Stats(callCtx, time.Now())
	if err != nil {
		s.logger.Error("could not read statistics", "err", err)
		return
	}
	s.logger.Info("instances", "total", stats.Total, "active24h", stats.Active24h,
		"active7d", stats.Active7d, "active30d", stats.Active30d)
}

// ---- releases ---------------------------------------------------------------

// release is the newest published build of one channel, as this service
// remembers it.
type release struct {
	Version     string
	Notes       string
	URL         string
	PublishedAt time.Time
	Assets      map[string]asset
	ChecksumURL string
}

// asset is one downloadable file of a release.
type asset struct {
	Name string
	URL  string
}

// answer shapes the release for one caller. A caller on the newest version is
// told the version and nothing more.
func (r *release) answer(in CheckIn) checkResponse {
	out := checkResponse{Version: r.Version}
	if !newerVersion(r.Version, in.Version) {
		return out
	}
	out.Notes = r.Notes
	out.URL = r.URL
	out.ChecksumURL = r.ChecksumURL
	if !r.PublishedAt.IsZero() {
		at := r.PublishedAt.UTC()
		out.PublishedAt = &at
	}
	if a, ok := r.Assets[assetName(r.Version, in.OS, in.Arch)]; ok {
		out.Asset = a.Name
		out.AssetURL = a.URL
	}
	return out
}

// assetName is the release artifact for one platform, the name the Storix
// release workflow publishes.
func assetName(version, goos, goarch string) string {
	return fmt.Sprintf("storix_%s_%s_%s.tar.gz", version, goos, goarch)
}

// releaseCache keeps the newest release of each channel in memory. The mutex
// is also the fetch guard: one caller performs the GitHub request while the
// others wait for its result, so a burst of check ins is one call.
type releaseCache struct {
	repo string
	// base is where the release feed lives. It is a field only so a test can
	// point it at a server it controls instead of at GitHub.
	base   string
	client *http.Client
	logger *slog.Logger

	mu      sync.Mutex
	entries map[string]*cachedRelease
}

// cachedRelease is what is known about one channel.
type cachedRelease struct {
	mu      sync.Mutex
	rel     *release
	fetched time.Time
	lastTry time.Time
	err     error
}

// newReleaseCache builds the cache for one repository.
func newReleaseCache(repo string, logger *slog.Logger) *releaseCache {
	return &releaseCache{
		repo:    repo,
		base:    "https://api.github.com",
		client:  &http.Client{Timeout: 20 * time.Second},
		logger:  logger,
		entries: map[string]*cachedRelease{},
	}
}

// get returns the newest release of a channel, from memory when it is fresh.
// When GitHub cannot be reached it answers with the last known release, and
// only reports an error when there has never been one.
func (c *releaseCache) get(ctx context.Context, channel string) (*release, error) {
	// The map lock is held only long enough to find the entry. Holding it
	// across the fetch below would put every caller, including the ones whose
	// answer is already cached, behind one network round trip.
	c.mu.Lock()
	entry := c.entries[channel]
	if entry == nil {
		entry = &cachedRelease{}
		c.entries[channel] = entry
	}
	c.mu.Unlock()

	// Callers asking for the same channel still wait for each other, which is
	// the point: one fetch serves all of them rather than each making its own.
	entry.mu.Lock()
	defer entry.mu.Unlock()

	now := time.Now()
	if entry.rel != nil && now.Sub(entry.fetched) <= releaseTTL {
		return entry.rel, nil
	}
	if !entry.lastTry.IsZero() && now.Sub(entry.lastTry) < releaseRetry && entry.err != nil {
		// A call failed moments ago. Do not make every caller wait for
		// another one that is likely to fail the same way.
		if entry.rel != nil {
			return entry.rel, nil
		}
		return nil, entry.err
	}

	// The fetch outlives the request that triggered it, so one caller hanging
	// up does not cost everyone else the answer.
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	rel, err := c.fetch(callCtx, channel)
	entry.lastTry = time.Now()
	if err != nil {
		entry.err = err
		if entry.rel != nil {
			c.logger.Warn("release feed unavailable, answering from the last known release", "err", err)
			return entry.rel, nil
		}
		c.logger.Error("release feed unavailable and nothing is known yet", "err", err)
		return nil, err
	}
	entry.rel, entry.fetched, entry.err = rel, time.Now(), nil
	return rel, nil
}

// ghRelease is the part of the GitHub release document this service reads.
type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// fetch asks GitHub for the newest release of a channel. The stable channel
// takes the published latest release, any other channel takes the newest
// release that is not a draft, which is how a prerelease is offered.
func (c *releaseCache) fetch(ctx context.Context, channel string) (*release, error) {
	base := c.base
	if base == "" {
		base = "https://api.github.com"
	}
	endpoint := base + "/repos/" + c.repo + "/releases/latest"
	if channel != ChannelStable {
		endpoint = base + "/repos/" + c.repo + "/releases?per_page=10"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("updates: contact release feed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("updates: release feed returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("updates: read release feed: %w", err)
	}

	var found ghRelease
	if channel == ChannelStable {
		if err := json.Unmarshal(body, &found); err != nil {
			return nil, fmt.Errorf("updates: decode release feed: %w", err)
		}
	} else {
		var list []ghRelease
		if err := json.Unmarshal(body, &list); err != nil {
			return nil, fmt.Errorf("updates: decode release feed: %w", err)
		}
		for _, candidate := range list {
			if candidate.Draft {
				continue
			}
			found = candidate
			break
		}
	}
	if found.TagName == "" {
		return nil, errors.New("updates: the repository has no published release")
	}

	out := &release{
		Version:     strings.TrimPrefix(found.TagName, "v"),
		Notes:       found.Body,
		URL:         found.HTMLURL,
		PublishedAt: found.PublishedAt,
		Assets:      map[string]asset{},
	}
	for _, a := range found.Assets {
		if a.Name == "checksums.txt" {
			out.ChecksumURL = a.URL
			continue
		}
		out.Assets[a.Name] = asset{Name: a.Name, URL: a.URL}
	}
	return out, nil
}

// ---- versions ---------------------------------------------------------------

// newerVersion reports whether version a is newer than version b, comparing the
// dotted numbers and treating any suffix as a prerelease. It matches what the
// Storix client does, so both sides agree on what "newer" means.
func newerVersion(a, b string) bool {
	na, sa := splitVersion(a)
	nb, sb := splitVersion(b)
	for i := 0; i < 3; i++ {
		if na[i] != nb[i] {
			return na[i] > nb[i]
		}
	}
	// Equal numbers: a release beats a prerelease of the same number.
	return sa == "" && sb != ""
}

// splitVersion breaks a version into its three numbers and any suffix.
func splitVersion(v string) ([3]int, string) {
	v = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "v"))
	suffix := ""
	if idx := strings.IndexAny(v, "-+"); idx >= 0 {
		suffix = v[idx+1:]
		v = v[:idx]
	}
	var out [3]int
	for i, part := range strings.SplitN(v, ".", 3) {
		if i > 2 {
			break
		}
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		out[i] = n
	}
	return out, suffix
}
