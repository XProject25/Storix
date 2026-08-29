// Package updater checks for and installs new Storix releases.
//
// The binary is replaced atomically by writing next to it and renaming over
// the old file, which POSIX allows even while the current process runs. When
// the binary is not writable by the service account, which is the safer
// installation layout, the update is reported to the operator instead of
// being forced through.
//
// Storix - modern web file manager for servers.
// Developed by X Project.
package updater

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/XProject25/Storix/internal/build"
)

// ErrNotWritable means the running binary cannot be replaced by this process.
var ErrNotWritable = errors.New("updater: the Storix binary is not writable by the service account")

// ErrNoAsset means the release has no build for this platform.
var ErrNoAsset = errors.New("updater: this release has no build for this platform")

// Options configure the updater.
type Options struct {
	Repo       string
	Channel    string
	Current    string
	BinaryPath string
	Client     *http.Client
	Logger     *slog.Logger

	// Endpoint is the update service asked about new versions. An empty
	// value goes straight to the GitHub release API, which is also where a
	// failed service call falls back to.
	Endpoint string
	// Instance identifies this install to the update service so two checks
	// from one server are not counted as two servers. An empty value sends
	// no identifier, and the service does not count the check.
	Instance string
	// Check reports whether Storix may ask anything at all. With it false
	// no request is made, by any caller, for any reason.
	Check bool
	// Interval is the floor between two checks. It protects the host being
	// asked, so it is enforced here rather than by whoever calls in.
	Interval time.Duration
}

// DefaultInterval is how long an answer is reused when the caller names no
// interval of its own.
const DefaultInterval = 6 * time.Hour

// Updater talks to the release feed.
type Updater struct {
	opts Options

	// mu guards the remembered answer and also serialises the request, so
	// several callers arriving together produce one call and not several.
	mu     sync.Mutex
	last   *Release
	lastAt time.Time
}

// Release describes the newest published build.
type Release struct {
	Version     string    `json:"version"`
	Current     string    `json:"current"`
	Available   bool      `json:"available"`
	Notes       string    `json:"notes"`
	URL         string    `json:"url"`
	PublishedAt time.Time `json:"publishedAt"`
	Asset       string    `json:"asset"`
	AssetURL    string    `json:"-"`
	Size        int64     `json:"size"`
	Checksum    string    `json:"-"`
	Writable    bool      `json:"writable"`
	Message     string    `json:"message,omitempty"`
}

// New builds an updater.
func New(o Options) *Updater {
	if o.Repo == "" {
		o.Repo = build.Repo
	}
	if o.Current == "" {
		o.Current = build.Version
	}
	if o.BinaryPath == "" {
		if exe, err := os.Executable(); err == nil {
			o.BinaryPath = exe
		}
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 30 * time.Second}
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Interval <= 0 {
		o.Interval = DefaultInterval
	}
	return &Updater{opts: o}
}

// BinaryPath reports the file that would be replaced.
func (u *Updater) BinaryPath() string { return u.opts.BinaryPath }

// Writable reports whether this process could install an update itself.
func (u *Updater) Writable() bool {
	if u.opts.BinaryPath == "" {
		return false
	}
	dir := filepath.Dir(u.opts.BinaryPath)
	probe := filepath.Join(dir, ".storix-write-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return false
	}
	f.Close()
	_ = os.Remove(probe)
	return true
}

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

// Check reports the newest published version.
//
// It prefers the update service, falls back to the GitHub release API when
// that service answers badly or not at all, and answers from the last result
// until the interval has passed, so a busy dashboard cannot turn a page load
// into a request. With checking switched off it asks nobody.
func (u *Updater) Check(ctx context.Context) (*Release, error) {
	if !u.opts.Check {
		return &Release{
			Current:  strings.TrimPrefix(u.opts.Current, "v"),
			Version:  strings.TrimPrefix(u.opts.Current, "v"),
			Writable: u.Writable(),
			Message:  "Update checking is switched off",
		}, nil
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	if u.last != nil && time.Since(u.lastAt) < u.opts.Interval {
		return u.last, nil
	}

	if serviceEndpoint(u.opts.Endpoint) {
		rel, err := u.checkService(ctx, u.checkIn())
		if err == nil {
			u.last, u.lastAt = rel, time.Now()
			return rel, nil
		}
		// One host being down must not cost anyone their update check.
		u.opts.Logger.Debug("update service unavailable, using the release feed", "err", err)
	}

	rel, err := u.checkGitHub(ctx)
	if err != nil {
		return nil, err
	}
	u.last, u.lastAt = rel, time.Now()
	return rel, nil
}

// serviceEndpoint reports whether an endpoint expects the check-in document.
// An address on GitHub does not: the documentation offers the GitHub release
// API as the way to keep the update check while sending nothing countable, so
// an install configured that way reaches GitHub the GitHub way and no
// identifier ever leaves the server.
func serviceEndpoint(endpoint string) bool {
	if strings.TrimSpace(endpoint) == "" {
		return false
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "github.com", "api.github.com", "objects.githubusercontent.com":
		return false
	}
	return true
}

// checkGitHub asks the GitHub release API what the newest version is. It is
// the fallback, and it is what an install that names no update service uses.
func (u *Updater) checkGitHub(ctx context.Context) (*Release, error) {
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", u.opts.Repo)
	if u.opts.Channel == "beta" {
		endpoint = fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=5", u.opts.Repo)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", build.UserAgent())

	resp, err := u.opts.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("updater: contact release feed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return &Release{Current: u.opts.Current, Version: u.opts.Current, Writable: u.Writable(),
			Message: "No published releases yet"}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("updater: release feed returned %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}

	var rel ghRelease
	if u.opts.Channel == "beta" {
		var list []ghRelease
		if err := json.Unmarshal(body, &list); err != nil {
			return nil, err
		}
		for _, candidate := range list {
			if candidate.Draft {
				continue
			}
			rel = candidate
			break
		}
	} else if err := json.Unmarshal(body, &rel); err != nil {
		return nil, err
	}
	if rel.TagName == "" {
		return &Release{Current: u.opts.Current, Version: u.opts.Current, Writable: u.Writable(),
			Message: "No published releases yet"}, nil
	}

	out := &Release{
		Version:     strings.TrimPrefix(rel.TagName, "v"),
		Current:     strings.TrimPrefix(u.opts.Current, "v"),
		Notes:       rel.Body,
		URL:         rel.HTMLURL,
		PublishedAt: rel.PublishedAt,
		Writable:    u.Writable(),
	}
	out.Available = Newer(out.Version, out.Current)

	wanted := AssetName(out.Version)
	for _, asset := range rel.Assets {
		if asset.Name == wanted {
			out.Asset = asset.Name
			out.AssetURL = asset.URL
			out.Size = asset.Size
		}
		if asset.Name == "checksums.txt" {
			out.Checksum = asset.URL
		}
	}
	if out.Available && out.AssetURL == "" {
		out.Message = "This release has no build for " + build.Platform()
	}
	if out.Available && !out.Writable {
		out.Message = "Run sudo storix update on the server to install this version"
	}
	return out, nil
}

// AssetName is the release artifact for this platform.
func AssetName(version string) string {
	return fmt.Sprintf("storix_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
}

// ErrUntrustedAsset is returned when a release names a download somewhere
// other than the project's own release hosting.
var ErrUntrustedAsset = errors.New("updater: that release points its download somewhere untrusted, refusing to install")

// downloadHosts is where a Storix build may be fetched from. Nothing else is
// accepted, whoever says otherwise.
//
// The update service answers with the address of the build to install, and the
// checksum it is verified against comes from the same answer, so a checksum
// alone only proves the download matches what that answer asked for. This is
// what makes the answer unable to point anywhere it likes: the binary is
// installed as root, so the one thing an update must never be is a way to run
// somebody else's code.
var downloadHosts = map[string]bool{
	"github.com":                           true,
	"api.github.com":                       true,
	"objects.githubusercontent.com":        true,
	"release-assets.githubusercontent.com": true,
	"codeload.github.com":                  true,
}

// trustedDownload reports whether a release asset may be fetched from here.
func trustedDownload(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return false
	}
	return downloadHosts[strings.ToLower(parsed.Hostname())]
}

// Apply downloads and installs a release.
func (u *Updater) Apply(ctx context.Context, rel *Release, progress func(done, total int64)) error {
	if rel == nil || !rel.Available {
		return errors.New("updater: nothing to install")
	}
	if rel.AssetURL == "" {
		return ErrNoAsset
	}
	if !u.Writable() {
		return ErrNotWritable
	}
	// Where the build comes from is not the answering service's decision.
	if !trustedDownload(rel.AssetURL) || !trustedDownload(rel.Checksum) {
		return ErrUntrustedAsset
	}

	sums, err := u.fetchChecksums(ctx, rel.Checksum)
	if err != nil {
		u.opts.Logger.Warn("checksum list unavailable", "err", err)
	}
	want := sums[rel.Asset]
	if want == "" {
		return errors.New("updater: the release is missing a checksum for this platform, refusing to install")
	}

	dir := filepath.Dir(u.opts.BinaryPath)
	tmp, err := os.CreateTemp(dir, ".storix-download-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := u.download(ctx, rel.AssetURL, tmp, rel.Size, progress); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		tmp.Close()
		return err
	}

	sum := sha256.New()
	if _, err := io.Copy(sum, tmp); err != nil {
		tmp.Close()
		return err
	}
	got := hex.EncodeToString(sum.Sum(nil))
	if !strings.EqualFold(got, want) {
		tmp.Close()
		return fmt.Errorf("updater: checksum mismatch, expected %s got %s", want, got)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		tmp.Close()
		return err
	}

	binaryTmp := filepath.Join(dir, ".storix-new")
	if err := extractBinary(tmp, binaryTmp); err != nil {
		tmp.Close()
		_ = os.Remove(binaryTmp)
		return err
	}
	tmp.Close()

	if err := os.Chmod(binaryTmp, 0o755); err != nil {
		_ = os.Remove(binaryTmp)
		return err
	}
	// Keep a copy of the running version so a failed rollout can be undone.
	backup := u.opts.BinaryPath + ".previous"
	_ = os.Remove(backup)
	if err := copyFile(u.opts.BinaryPath, backup); err != nil {
		u.opts.Logger.Warn("could not keep a rollback copy", "err", err)
	}
	if err := os.Rename(binaryTmp, u.opts.BinaryPath); err != nil {
		_ = os.Remove(binaryTmp)
		return fmt.Errorf("updater: install: %w", err)
	}
	u.opts.Logger.Info("update installed", "version", rel.Version, "path", u.opts.BinaryPath)
	return nil
}

func (u *Updater) download(ctx context.Context, url string, dst io.Writer, total int64, progress func(done, total int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", build.UserAgent())
	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("updater: download returned %s", resp.Status)
	}
	if total <= 0 {
		total = resp.ContentLength
	}
	buf := make([]byte, 512<<10)
	var done int64
	last := time.Now()
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := dst.Write(buf[:n]); err != nil {
				return err
			}
			done += int64(n)
			if progress != nil && time.Since(last) > 200*time.Millisecond {
				last = time.Now()
				progress(done, total)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if progress != nil {
		progress(done, total)
	}
	return nil
}

// fetchChecksums reads the sha256 manifest published with the release.
func (u *Updater) fetchChecksums(ctx context.Context, url string) (map[string]string, error) {
	out := map[string]string{}
	if url == "" {
		return out, errors.New("updater: no checksum manifest in this release")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("User-Agent", build.UserAgent())
	resp, err := u.opts.Client.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("updater: checksum manifest returned %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return out, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 {
			continue
		}
		out[strings.TrimPrefix(fields[1], "*")] = fields[0]
	}
	return out, nil
}

// extractBinary pulls the storix executable out of a release tarball.
func extractBinary(src io.Reader, dst string) error {
	gz, err := gzip.NewReader(src)
	if err != nil {
		return fmt.Errorf("updater: open archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("updater: the archive does not contain a storix binary")
		}
		if err != nil {
			return err
		}
		name := filepath.Base(header.Name)
		if header.Typeflag != tar.TypeReg || (name != "storix" && name != "storix.exe") {
			continue
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		// The binary is a known size, so a bounded copy is enough of a guard.
		if _, err := io.CopyN(out, tr, header.Size); err != nil && !errors.Is(err, io.EOF) {
			out.Close()
			return err
		}
		return out.Close()
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Newer reports whether version a is newer than version b using a simple
// dotted numeric comparison, with any suffix treated as a prerelease.
func Newer(a, b string) bool {
	pa, sa := splitVersion(a)
	pb, sb := splitVersion(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	// Equal numbers: a release beats a prerelease of the same number.
	if sa == "" && sb != "" {
		return true
	}
	return false
}

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

// Protection describes how well the installed binary is guarded against being
// rewritten by the service itself. Reporting this from the file rather than
// from the caller identity matters, because a check run under sudo would
// otherwise always claim the binary is writable.
type Protection struct {
	Path        string `json:"path"`
	OwnedByRoot bool   `json:"ownedByRoot"`
	OthersWrite bool   `json:"othersWrite"`
	Known       bool   `json:"known"`
}

// Protection inspects the installed binary.
func (u *Updater) Protection() Protection {
	out := Protection{Path: u.opts.BinaryPath}
	info, err := os.Stat(u.opts.BinaryPath)
	if err != nil {
		return out
	}
	out.OthersWrite = info.Mode().Perm()&0o022 != 0
	if uid, ok := binaryOwner(u.opts.BinaryPath); ok {
		out.OwnedByRoot = uid == 0
		out.Known = true
	}
	return out
}
