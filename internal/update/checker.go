// Package update implements the in-app "new version available" SENSING layer.
//
// It polls GitHub Releases for the latest tag, compares it to the running
// version (injected via -ldflags main.appVersion), and caches the result for
// the API/UI to read. It NEVER executes an upgrade — Docker containers are
// immutable, so real upgrades are performed by an external tool (Watchtower),
// the NAS app store, or `docker compose pull && up -d`. This matches the NAS
// data-safety contract: a box holding recordings must not be silently changed.
//
// GitHub rate limits: unauthenticated requests are capped at 60/hour/IP.
// Conditional requests (If-None-Match / ETag) returning 304 do NOT count
// against that quota, so a single-instance NVR polling hourly is well within
// limits. The frontend never contacts GitHub directly — it reads this cache.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"
)

// latestReleaseAPI is the GitHub endpoint for the newest non-prerelease.
const latestReleaseAPI = "https://api.github.com/repos/%s/releases/latest"

// minRecheckBackoff caps how fast the poller retries after a failure, so a
// flaky network/GitHub outage does not hammer the API.
const (
	defaultInterval    = time.Hour
	minRecheckInterval = 5 * time.Minute
	requestTimeout     = 15 * time.Second
)

// githubRelease models the subset of fields used from the GitHub API response.
type githubRelease struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
	Body        string `json:"body"` // release notes (changelog)
}

// Status is the cached result served to the API/UI. All times are RFC3339.
type Status struct {
	Current         string `json:"current"`          // running version (appVersion)
	Latest          string `json:"latest"`           // newest tag from GitHub ("" if unknown)
	UpdateAvailable bool   `json:"update_available"` // Latest strictly newer than Current
	PublishedAt     string `json:"published_at,omitempty"`
	HTMLURL         string `json:"html_url,omitempty"`
	Changelog       string `json:"changelog,omitempty"` // release notes body (already markdown)
	Deployment      string `json:"deployment"`          // "docker" | "binary" | ""
	CheckedAt       string `json:"checked_at"`          // last successful/304 check (RFC3339)
	Channel         string `json:"channel"`             // "stable" | "beta"
}

// Checker polls GitHub Releases in the background and caches the result.
// The zero value is not usable; use New.
type Checker struct {
	current    string // running version, set at construction (may be "dev")
	repo       string
	channel    string
	interval   time.Duration
	httpClient *http.Client
	// endpoint overrides the GitHub API URL when non-empty (tests only).
	endpoint string

	mu     sync.RWMutex
	cached *Status // latest known status (nil before first successful check)
	etag   string  // sent as If-None-Match on the next poll

	stop chan struct{} // closed by Stop to end the poll loop
}

// New builds a Checker. currentVersion is the value injected via
// -ldflags main.appVersion (typically a git tag like "v0.10.0" or "dev").
// repo is "owner/name". channel is "stable" (only supported value today).
// interval is the poll cadence; <=0 falls back to defaultInterval.
func New(currentVersion, repo, channel string, interval time.Duration) *Checker {
	if interval <= 0 {
		interval = defaultInterval
	}
	return &Checker{
		current:    strings.TrimSpace(currentVersion),
		repo:       strings.TrimSpace(repo),
		channel:    strings.TrimSpace(channel),
		interval:   interval,
		httpClient: &http.Client{Timeout: requestTimeout},
		stop:       make(chan struct{}),
	}
}

// Start launches the background poller until Stop is called. It performs an
// immediate first check, then ticks at the configured interval. Errors are
// logged (non-fatal) and retried with the normal cadence; the cached value
// from the last success is preserved.
func (c *Checker) Start(ctx context.Context) {
	go c.loop(ctx)
}

// Stop ends the background poller. Safe to call multiple times.
func (c *Checker) Stop() {
	select {
	case <-c.stop:
		// already closed
	default:
		close(c.stop)
	}
}

// Status returns a snapshot of the latest check. Before the first successful
// poll it returns a Status with only Current/Deployment/Channel populated
// (UpdateAvailable=false).
func (c *Checker) Status() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.cached == nil {
		return Status{
			Current:    c.current,
			Deployment: Deployment(),
			Channel:    c.channel,
		}
	}
	return *c.cached
}

// Refresh forces an immediate check (used by the UI "check now" button).
// Returns the resulting Status. It respects ctx for cancellation/timeout.
func (c *Checker) Refresh(ctx context.Context) (Status, error) {
	_, err := c.fetchAndCache(ctx)
	if err != nil {
		slog.Warn("update: refresh failed", "repo", c.repo, "error", err)
	}
	return c.Status(), err
}

func (c *Checker) loop(ctx context.Context) {
	poll := func() {
		if _, err := c.fetchAndCache(ctx); err != nil {
			slog.Warn("update: poll failed", "repo", c.repo, "error", err)
		}
	}
	// First check immediately so the UI has data on startup.
	poll()
	t := time.NewTicker(c.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stop:
			return
		case <-t.C:
			poll()
		}
	}
}

// fetchAndCache performs one GitHub API request (with ETag) and updates the
// cache on success. A 304 keeps the existing cache but refreshes CheckedAt.
func (c *Checker) fetchAndCache(ctx context.Context) (Status, error) {
	if c.repo == "" {
		return c.Status(), fmt.Errorf("update: repo not configured")
	}
	url := c.endpoint
	if url == "" {
		url = fmt.Sprintf(latestReleaseAPI, c.repo)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return c.Status(), err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	c.mu.RLock()
	etag := c.etag
	c.mu.RUnlock()
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return c.Status(), err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		// Not modified: keep cached payload, refresh check timestamp.
		c.mu.Lock()
		if c.cached != nil {
			c.cached.CheckedAt = time.Now().UTC().Format(time.RFC3339)
		}
		c.mu.Unlock()
		return c.Status(), nil
	}
	if resp.StatusCode != http.StatusOK {
		return c.Status(), fmt.Errorf("update: github returned %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB cap
	if err != nil {
		return c.Status(), err
	}
	var rel githubRelease
	if err := json.Unmarshal(body, &rel); err != nil {
		return c.Status(), err
	}

	status := Status{
		Current:         c.current,
		Latest:          rel.TagName,
		UpdateAvailable: isNewer(c.current, rel.TagName),
		PublishedAt:     rel.PublishedAt,
		HTMLURL:         rel.HTMLURL,
		Changelog:       rel.Body,
		Deployment:      Deployment(),
		CheckedAt:       time.Now().UTC().Format(time.RFC3339),
		Channel:         c.channel,
	}
	c.mu.Lock()
	c.cached = &status
	c.etag = resp.Header.Get("ETag")
	c.mu.Unlock()
	return status, nil
}

// isNewer reports whether candidate is strictly newer than current using Go's
// canonical semver comparison. Handles:
//   - "dev" / "" / non-semver local builds  → false (never claims an update;
//     avoids false positives on local/dev builds)
//   - "v" prefix is optional (semver.Compare normalizes it)
//   - prereleases: per semver, v1.0.0-beta < v1.0.0
//   - equal versions → false (no downgrades either)
func isNewer(current, candidate string) bool {
	cur := ensureV(strings.TrimSpace(current))
	cand := ensureV(strings.TrimSpace(candidate))
	if !semver.IsValid(cur) || !semver.IsValid(cand) {
		return false
	}
	return semver.Compare(cand, cur) > 0
}

// ensureV adds the "v" prefix that golang.org/x/mod/semver requires for
// IsValid. This lets appVersion be either "v0.10.0" (ldflags from a git tag)
// or "0.10.0" and still compare correctly. Non-semver values (e.g. "dev")
// remain invalid → isNewer returns false.
func ensureV(v string) string {
	if v == "" {
		return ""
	}
	if v[0] != 'v' && v[0] != 'V' {
		return "v" + v
	}
	return v
}

// Deployment reports where the app is running. It prefers the NVR_DEPLOYMENT
// env var (set in the Dockerfile), then falls back to /.dockerenv detection.
// The value drives different upgrade instructions in the UI.
func Deployment() string {
	if d := strings.TrimSpace(os.Getenv("NVR_DEPLOYMENT")); d != "" {
		return d
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return "docker"
	}
	return "binary"
}
