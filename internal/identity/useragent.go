package identity

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"opencode-free-proxy/internal/config"
)

// UARe extracts `opencode/<major>.<minor>[.patch]` from a User-Agent.
var UARe = regexp.MustCompile(`(?i)opencode/(\d+)\.(\d+)(?:\.(\d+))?`)

// TagRe pulls a semver from a GitHub release tag like "v1.19.2".
var TagRe = regexp.MustCompile(`^v?(\d+\.\d+\.\d+)`)

// HasValidVersion reports whether ua identifies an opencode client new enough
// for the free-tier gate (>= 1.17 — below that upstream answers 426/403).
// A bare "opencode" UA has no version and fails the check.
func HasValidVersion(ua string) bool {
	m := UARe.FindStringSubmatch(ua)
	if m == nil {
		return false
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	return major > config.ClientMinMajor ||
		(major == config.ClientMinMajor && minor >= config.ClientMinMinor)
}

// UserAgentCache is the fail-open version probe: it keeps the freshest
// opencode version seen from the GitHub releases API and falls back to the
// pinned constant when the probe has never succeeded. Errors still advance
// the cache clock so a broken network doesn't hammer GitHub.
type UserAgentCache struct {
	mu       sync.Mutex
	inflight bool
	version  string // "" until first successful probe
	cachedAt time.Time
	now      func() time.Time
}

func NewUserAgentCache() *UserAgentCache {
	return &UserAgentCache{now: time.Now}
}

// BuildUA renders the compound User-Agent the official CLI sends
// (config.UserAgentTail documents the observed shape): the probed (or
// pinned) opencode version followed by the pinned ai-sdk/runtime tail.
func BuildUA(version string) string {
	return "opencode/" + version + " " + config.UserAgentTail
}

// FallbackUA returns the pinned identity used when the probe is cold.
func FallbackUA() string {
	return BuildUA(config.ClientFallbackVersion)
}

// Get returns the current best User-Agent without triggering network I/O.
func (c *UserAgentCache) Get() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.uaLocked()
}

// Warm refreshes the cached version if the TTL elapsed. Concurrent callers
// are deduplicated (single-flight). Fail-open: on any error the fallback (or
// previously cached value) stays and cachedAt advances.
func (c *UserAgentCache) Warm(client *http.Client) string {
	c.mu.Lock()
	now := c.now()
	if c.version != "" && now.Sub(c.cachedAt) < config.VersionCacheTTL {
		c.mu.Unlock()
		return c.uaLocked()
	}
	if c.inflight {
		c.mu.Unlock()
		return c.Get()
	}
	c.inflight = true
	c.mu.Unlock()

	version, err := fetchLatestRelease(client)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inflight = false
	c.cachedAt = c.now()
	if err == nil && version != "" {
		c.version = version
	}
	return c.uaLocked()
}

// uaLocked renders the full User-Agent; caller must hold c.mu. (Not c.Get() —
// sync.Mutex is not reentrant.)
func (c *UserAgentCache) uaLocked() string {
	if c.version != "" {
		return BuildUA(c.version)
	}
	return FallbackUA()
}

func fetchLatestRelease(client *http.Client) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequest(http.MethodGet, config.GitHubReleasesURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "opencode-free-proxy-version-probe")
	httpClient := *client
	httpClient.Timeout = 8 * time.Second
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &statusError{code: resp.StatusCode}
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	m := TagRe.FindStringSubmatch(body.TagName)
	if m == nil {
		return "", errNoSemver
	}
	return m[1], nil
}

type statusError struct{ code int }

func (e *statusError) Error() string { return http.StatusText(e.code) }

var errNoSemver = errString("github release tag carried no semver")

type errString string

func (e errString) Error() string { return string(e) }
