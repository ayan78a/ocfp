// User-Agent gate + GitHub release probe tests — ported from 9router
// tests/unit/opencode-client-version.test.js and
// open-sse/executors/opencode.js hasValidOpencodeVersion /
// open-sse/utils/opencodeClientVersion.js.
package identity

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"opencode-free-proxy/internal/config"
)

// TestHasValidVersion pins the free-tier version gate: opencode >= 1.17.0
// passes, older/bare/non-opencode UAs fail. The regex is unanchored and
// case-insensitive, the patch segment optional (JS /opencode\/(\d+)\.(\d+)
// (?:\.(\d+))?/i).
func TestHasValidVersion(t *testing.T) {
	cases := []struct {
		ua   string
		want bool
		note string
	}{
		{"", false, "empty UA"},
		{"opencode/1.16.0", false, "below the 1.17 gate"},
		{"opencode/1.15.0", false, "golden outdated version"},
		{"opencode/1.16.99", false, "just below the gate"},
		{"opencode/0.9.4", false, "ancient"},
		{"opencode/1.17.0", true, "exactly at the gate"},
		{"opencode/1.17", true, "patch segment optional"},
		{"opencode/1.18.31", true, "pinned fallback version"},
		{"opencode/1.19.0", true, "newer 1.x"},
		{"opencode/2.0.1", true, "future major"},
		{"opencode", false, "bare opencode UA has no version"},
		{"Mozilla/5.0", false, "non-opencode UA"},
		{"Claude-Code/1.0", false, "other client"},
		{"OpenCode/1.19.0", true, "case-insensitive product token"},
		{"opencode/1.18.31 ai-sdk/provider-utils/4.0.40 runtime/bun/1.3.14", true, "golden full opencode UA"},
		{"Mozilla/5.0 (Macintosh) opencode/1.18.0 something", true, "regex is unanchored"},
		{"xopencode/9.9.9", true, "JS regex has no word boundary before opencode"},
		{"opencode/1", false, "minor segment missing → no match"},
	}
	for _, tc := range cases {
		if got := HasValidVersion(tc.ua); got != tc.want {
			t.Errorf("HasValidVersion(%q) [%s] = %v, want %v", tc.ua, tc.note, got, tc.want)
		}
	}
}

// FallbackUA / BuildUA must render the COMPOUND User-Agent the official CLI
// sends: opencode/<version> + the pinned ai-sdk/runtime tail (observed live:
// "opencode/1.18.31 ai-sdk/provider-utils/4.0.40 runtime/bun/1.3.14").
func TestFallbackUA(t *testing.T) {
	if got, want := FallbackUA(), "opencode/1.18.31 ai-sdk/provider-utils/4.0.40 runtime/bun/1.3.14"; got != want {
		t.Errorf("FallbackUA() = %q, want %q", got, want)
	}
	if got, want := BuildUA("2.0.1"), "opencode/2.0.1 ai-sdk/provider-utils/4.0.40 runtime/bun/1.3.14"; got != want {
		t.Errorf("BuildUA(\"2.0.1\") = %q, want %q", got, want)
	}
}

// roundTripFunc is a stub transport so the probe never touches the network.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

// parseReleaseTagName: tag names like "v1.18.31" / "1.20.0" yield a semver,
// anything else is rejected (golden vectors).
func TestParseReleaseTagName(t *testing.T) {
	cases := []struct {
		tag  string
		want string
	}{
		{"v1.18.31", "1.18.31"},
		{"1.20.0", "1.20.0"},
		{"v2.0.1", "2.0.1"},
		{"bad", ""},
		{"", ""},
		{"v1.2", ""},
	}
	for _, tc := range cases {
		m := TagRe.FindStringSubmatch(tc.tag)
		got := ""
		if m != nil {
			got = m[1]
		}
		if got != tc.want {
			t.Errorf("TagRe(%q) = %q, want %q", tc.tag, got, tc.want)
		}
	}
}

// fetchLatestRelease returns the parsed semver for a 200 GitHub payload.
func TestFetchLatestReleaseParsesTag(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != config.GitHubReleasesURL {
			t.Errorf("probe URL = %q, want %q", r.URL.String(), config.GitHubReleasesURL)
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept header = %q", got)
		}
		return jsonResponse(http.StatusOK, `{"tag_name":"v1.18.31"}`), nil
	})}
	version, err := fetchLatestRelease(client)
	if err != nil {
		t.Fatalf("fetchLatestRelease() error = %v", err)
	}
	if version != "1.18.31" {
		t.Errorf("fetchLatestRelease() = %q, want %q", version, "1.18.31")
	}
}

// A release tag without a semver and a non-200 response are both probe
// failures (the caller stays fail-open).
func TestFetchLatestReleaseFailureModes(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"tag_name":"bad"}`), nil
	})}
	if _, err := fetchLatestRelease(client); !errors.Is(err, errNoSemver) {
		t.Errorf("tag without semver: err = %v, want errNoSemver", err)
	}

	client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusServiceUnavailable, "nope"), nil
	})}
	if _, err := fetchLatestRelease(client); err == nil {
		t.Error("503 response must be a probe error")
	}

	client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network down")
	})}
	if _, err := fetchLatestRelease(client); err == nil {
		t.Error("transport error must surface")
	}
}

// warmWithTimeout runs Warm on a goroutine and reports whether it returned at
// all — Warm currently deadlocks on the fetch path (see
// TestUserAgentCacheWarmDeadlockBUG), and a blocked Warm must not hang the
// whole suite.
func warmWithTimeout(c *UserAgentCache, client *http.Client) (string, bool) {
	done := make(chan string, 1)
	go func() { done <- c.Warm(client) }()
	select {
	case ua := <-done:
		return ua, true
	case <-time.After(750 * time.Millisecond):
		return "", false
	}
}

// warmOnce skips the test when the Warm deadlock is still present; the
// behavioral assertions below activate automatically once it is fixed.
func warmOnce(t *testing.T, c *UserAgentCache, client *http.Client) string {
	t.Helper()
	ua, ok := warmWithTimeout(c, client)
	if !ok {
		t.Skip("Warm() deadlocks (internal/identity/useragent.go:90 calls c.Get() while holding c.mu) — see TestUserAgentCacheWarmDeadlockBUG")
	}
	return ua
}

// PARITY/IMPLEMENTATION BUG repro (expected to FAIL — see task report):
// internal/identity/useragent.go:90 ends Warm with `return c.Get()` while the
// mutex grabbed at :83 is still held (deferred unlock). sync.Mutex is not
// reentrant, so EVERY Warm that reaches the fetch phase deadlocks. In the live
// server the startup warm (cmd/server/main.go:30) blocks forever holding c.mu,
// and the first request that reads the UA (internal/router/handler.go:173,
// s.UA.Get()) then hangs too. JS has no equivalent hazard —
// warmOpencodeUserAgentCache resolves to "opencode/<version>".
func TestUserAgentCacheWarmDeadlockBUG(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"tag_name":"v2.0.1"}`), nil
	})}
	c := NewUserAgentCache()

	done := make(chan string, 1)
	go func() { done <- c.Warm(client) }()
	select {
	case ua := <-done:
		if ua != BuildUA("2.0.1") {
			t.Errorf("Warm() = %q, want %q", ua, BuildUA("2.0.1"))
		}
	case <-time.After(750 * time.Millisecond):
		t.Fatal("Warm() deadlocked: useragent.go:90 calls c.Get() while c.mu is held (deferred unlock at :84); JS warmOpencodeUserAgentCache returns \"opencode/2.0.1\"")
	}
}

// Golden: caches a successful GitHub release lookup and throttles the second
// warm inside the TTL to zero extra calls.
func TestUserAgentCacheWarmCachesLookup(t *testing.T) {
	var calls atomic.Int64
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return jsonResponse(http.StatusOK, `{"tag_name":"v2.0.1"}`), nil
	})}
	c := NewUserAgentCache()

	if got := warmOnce(t, c, client); got != BuildUA("2.0.1") {
		t.Errorf("Warm() = %q, want %q", got, BuildUA("2.0.1"))
	}
	if got := c.Get(); got != BuildUA("2.0.1") {
		t.Errorf("Get() = %q, want cached %q", got, BuildUA("2.0.1"))
	}
	warmOnce(t, c, client) // inside the TTL → served from cache
	if got := calls.Load(); got != 1 {
		t.Errorf("GitHub calls = %d, want 1 (second warm must be TTL-throttled)", got)
	}
}

// Get before any warm returns the pinned fallback (cold cache).
func TestUserAgentCacheColdGet(t *testing.T) {
	if got := NewUserAgentCache().Get(); got != FallbackUA() {
		t.Errorf("cold Get() = %q, want %q", got, FallbackUA())
	}
}

// Golden: falls back when the GitHub lookup fails.
func TestUserAgentCacheWarmFailsOpen(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusServiceUnavailable, "nope"), nil
	})}
	c := NewUserAgentCache()
	if got := warmOnce(t, c, client); got != FallbackUA() {
		t.Errorf("Warm() on 503 = %q, want fallback %q", got, FallbackUA())
	}
	if got := c.Get(); got != FallbackUA() {
		t.Errorf("Get() after failure = %q, want fallback %q", got, FallbackUA())
	}
}

// A successful cache entry expires after the 12h TTL and is refreshed.
func TestUserAgentCacheTTLExpiry(t *testing.T) {
	var calls atomic.Int64
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return jsonResponse(http.StatusOK, `{"tag_name":"v2.0.1"}`), nil
	})}
	c := NewUserAgentCache()
	base := time.Now()
	now := base
	c.now = func() time.Time { return now }

	warmOnce(t, c, client)
	now = base.Add(config.VersionCacheTTL - time.Minute)
	warmOnce(t, c, client)
	if got := calls.Load(); got != 1 {
		t.Errorf("calls just inside TTL = %d, want 1", got)
	}
	now = base.Add(config.VersionCacheTTL + time.Minute)
	if got := warmOnce(t, c, client); got != BuildUA("2.0.1") {
		t.Errorf("Warm() after TTL = %q, want refreshed %q", got, BuildUA("2.0.1"))
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("calls after TTL expiry = %d, want 2", got)
	}
}

// Concurrent warms are deduplicated (single-flight): exactly one GitHub call.
func TestUserAgentCacheSingleFlight(t *testing.T) {
	// Skip while the Warm deadlock is present: a deadlocked warm holds c.mu
	// forever and this test's goroutines would hang behind it.
	if _, ok := warmWithTimeout(NewUserAgentCache(), &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"tag_name":"v2.0.1"}`), nil
	})}); !ok {
		t.Skip("Warm() deadlocks (internal/identity/useragent.go:90 calls c.Get() while holding c.mu) — see TestUserAgentCacheWarmDeadlockBUG")
	}

	release := make(chan struct{})
	var calls atomic.Int64
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		<-release // hold the probe open so the others must hit the inflight gate
		return jsonResponse(http.StatusOK, `{"tag_name":"v2.0.1"}`), nil
	})}
	c := NewUserAgentCache()

	var wg sync.WaitGroup
	results := make([]string, 5)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = c.Warm(client)
		}(i)
	}
	time.Sleep(100 * time.Millisecond) // let every goroutine reach Warm
	close(release)
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Errorf("GitHub calls = %d, want 1 (concurrent warms must be deduplicated)", got)
	}
	for i, r := range results {
		if r == "" {
			t.Errorf("Warm() goroutine %d returned empty UA", i)
		}
	}
}
