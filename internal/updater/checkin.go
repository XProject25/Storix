package updater

// The check-in half of the updater: one request to the update service asking
// what the newest version is, and telling it just enough to count this server
// once rather than twice. The payload is fixed and small on purpose, and
// docs/UPDATES.md is the promise it has to keep, so nothing may be added here
// that an operator has not already read about there.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/XProject25/Storix/internal/build"
)

// checkInTimeout bounds the call to the update service. The answer is only
// ever advisory, so a slow service is dropped rather than waited for.
const checkInTimeout = 8 * time.Second

// checkInMaxBody caps the answer that is read. A release note is prose, not a
// payload.
const checkInMaxBody = 1 << 20

// CheckIn is the whole request sent to the update service. These fields are
// the entire payload: no account, no host name, no path and no address.
type CheckIn struct {
	Instance string `json:"instance"`
	Version  string `json:"version"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Channel  string `json:"channel"`
}

// checkInReply is the answer the update service returns. A caller that is
// already current gets back its own version and nothing else.
type checkInReply struct {
	Version     string    `json:"version"`
	Notes       string    `json:"notes"`
	URL         string    `json:"url"`
	Asset       string    `json:"asset"`
	AssetURL    string    `json:"assetUrl"`
	ChecksumURL string    `json:"checksumUrl"`
	PublishedAt time.Time `json:"publishedAt"`
}

// checkIn builds the request for this install.
func (u *Updater) checkIn() CheckIn {
	channel := strings.ToLower(strings.TrimSpace(u.opts.Channel))
	if channel == "" {
		channel = "stable"
	}
	return CheckIn{
		Instance: strings.TrimSpace(u.opts.Instance),
		Version:  strings.TrimPrefix(strings.TrimSpace(u.opts.Current), "v"),
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Channel:  channel,
	}
}

// checkService asks the update service for the newest release.
//
// Every failure is returned rather than reported, because the caller answers a
// failure by asking GitHub instead. The identifier is never logged, here or
// anywhere else.
func (u *Updater) checkService(ctx context.Context, in CheckIn) (*Release, error) {
	if strings.TrimSpace(u.opts.Endpoint) == "" {
		return nil, errors.New("updater: no update service is configured")
	}
	body, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}

	callCtx, cancel := context.WithTimeout(ctx, checkInTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, u.opts.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", build.UserAgent())

	client := u.opts.Client
	if client == nil {
		client = &http.Client{Timeout: checkInTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("updater: contact update service: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("updater: update service returned %s", resp.Status)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, checkInMaxBody))
	if err != nil {
		return nil, fmt.Errorf("updater: read the update service answer: %w", err)
	}
	var reply checkInReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, fmt.Errorf("updater: the update service answer is not a release: %w", err)
	}
	if strings.TrimSpace(reply.Version) == "" {
		return nil, errors.New("updater: the update service named no version")
	}

	out := &Release{
		Version:     strings.TrimPrefix(strings.TrimSpace(reply.Version), "v"),
		Current:     strings.TrimPrefix(strings.TrimSpace(u.opts.Current), "v"),
		Notes:       reply.Notes,
		URL:         reply.URL,
		PublishedAt: reply.PublishedAt,
		Asset:       reply.Asset,
		AssetURL:    reply.AssetURL,
		Checksum:    reply.ChecksumURL,
		Writable:    u.Writable(),
	}
	out.Available = Newer(out.Version, out.Current)
	if out.Available && out.AssetURL == "" {
		out.Message = "This release has no build for " + build.Platform()
	}
	if out.Available && !out.Writable {
		out.Message = "Run sudo storix update on the server to install this version"
	}
	return out, nil
}
