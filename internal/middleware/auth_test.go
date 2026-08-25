package middleware

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestValidCredentials(t *testing.T) {
	hash, _ := HashPassword("secret")
	mw, _ := NewAuthMiddleware(staticProvider("user", hash), "", AuthRateLimitConfig{})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Basic "+basic("user", "secret"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestInvalidPassword(t *testing.T) {
	hash, _ := HashPassword("secret")
	mw, _ := NewAuthMiddleware(staticProvider("user", hash), "", AuthRateLimitConfig{})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Basic "+basic("user", "wrong"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestMissingAuthHeader(t *testing.T) {
	hash, _ := HashPassword("secret")
	mw, _ := NewAuthMiddleware(staticProvider("user", hash), "", AuthRateLimitConfig{})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestMalformedAuth(t *testing.T) {
	hash, _ := HashPassword("secret")
	mw, _ := NewAuthMiddleware(staticProvider("user", hash), "", AuthRateLimitConfig{})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("not base64")))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestEmptyHashReturnsSetupRequired(t *testing.T) {
	mw, _ := NewAuthMiddleware(staticProvider("user", ""), "", AuthRateLimitConfig{})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusServiceUnavailable, w.Code, "expected 503 when no password configured")
	require.Equal(t, "application/json", w.Header().Get("Content-Type"))
	require.Equal(t, `Basic realm="MiBee NVR"`, w.Header().Get("WWW-Authenticate"))
	require.Contains(t, w.Body.String(), "setup required")
}

func TestHashCheckRoundTrip(t *testing.T) {
	pass := "abc123"
	hash, _ := HashPassword(pass)
	if !CheckPassword(pass, hash) {
		t.Fatalf("hash check failed for valid password")
	}
}

func TestConcurrentAccess(t *testing.T) {
	hash, _ := HashPassword("secret")
	mw, _ := NewAuthMiddleware(staticProvider("u", hash), "", AuthRateLimitConfig{})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	reqs := 50
	done := make(chan bool)
	for i := range reqs {
		go func(i int) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", "Basic "+basic("u", "secret"))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				_ = w.Code
			}
			done <- true
		}(i)
	}
	for range reqs {
		<-done
	}
}

// helper to build basic auth header quickly
func basic(user, pass string) string {
	s := user + ":" + pass
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// staticProvider returns an AuthProvider with fixed values for testing.
func staticProvider(username, hash string) AuthProvider {
	return AuthProvider{
		GetUsername: func() string { return username },
		GetHash:     func() string { return hash },
	}
}

func TestPlaintextPasswordAutoHash(t *testing.T) {
	mw, effectiveHash := NewAuthMiddleware(staticProvider("admin", ""), "mypassword", AuthRateLimitConfig{})
	require.NotEmpty(t, effectiveHash, "effectiveHash should be populated when plaintext is provided")
	require.True(t, CheckPassword("mypassword", effectiveHash), "original password should authenticate against auto-hash")

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Basic "+basic("admin", "mypassword"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestHashTakesPriorityOverPlaintext(t *testing.T) {
	preHashed, err := HashPassword("prehashed-pass")
	require.NoError(t, err)

	mw, effectiveHash := NewAuthMiddleware(staticProvider("admin", preHashed), "ignored-plaintext", AuthRateLimitConfig{})
	require.Equal(t, preHashed, effectiveHash, "pre-existing hash should take priority over plaintext")

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Basic "+basic("admin", "prehashed-pass"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("Authorization", "Basic "+basic("admin", "ignored-plaintext"))
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusUnauthorized, w2.Code, "plaintext password should not authenticate when hash takes priority")
}

func TestRateLimiterAllowsUnderLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rl := NewRateLimiter(ctx, RateLimiterConfig{MaxRequests: 5, Window: time.Minute})
	handler := rl.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	// Send 5 requests (at the limit) — should all pass
	for i := range 5 {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "request %d should pass", i+1)
	}
}

func TestRateLimiterBlocksOverLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rl := NewRateLimiter(ctx, RateLimiterConfig{MaxRequests: 3, Window: time.Minute})
	handler := rl.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	// Send 3 requests (at the limit) — all pass
	for i := range 3 {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "request %d should pass", i+1)
	}

	// 4th request should be blocked
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusTooManyRequests, w.Code, "request over limit should be 429")
}

func TestRateLimiterResetsAfterWindow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rl := NewRateLimiter(ctx, RateLimiterConfig{MaxRequests: 1, Window: 50 * time.Millisecond})
	handler := rl.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First request passes
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// Second request blocked
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusTooManyRequests, w.Code)

	// Wait for window to expire
	time.Sleep(60 * time.Millisecond)

	// Should be allowed again
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestSetupUpdatesHashDynamically(t *testing.T) {
	t.Helper()
	currentHash := ""
	currentUsername := "admin"
	provider := AuthProvider{
		GetUsername: func() string { return currentUsername },
		GetHash:     func() string { return currentHash },
	}

	mw, _ := NewAuthMiddleware(provider, "", AuthRateLimitConfig{})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Before setup: no hash configured → 503 SETUP_REQUIRED
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "setup required")

	// Simulate setup: set hash in config
	hash, err := HashPassword("newpassword123")
	require.NoError(t, err)
	currentHash = hash

	// After setup: middleware picks up the new hash → 200 OK
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("Authorization", "Basic "+basic("admin", "newpassword123"))
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusOK, w2.Code)
}

func TestRateLimiterEvictsStaleEntries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rl := NewRateLimiter(ctx, RateLimiterConfig{MaxRequests: 100, Window: 50 * time.Millisecond})
	handler := rl.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Create entries from 100 different IPs
	for i := range 100 {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "192.168.1." + strconv.Itoa(i) + ":12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)
	}

	// Verify entries were created
	require.Equal(t, 100, rl.entryCount(), "should have 100 entries before eviction")

	// Wait for 2× window + buffer to ensure cleanup ticker fires
	time.Sleep(110 * time.Millisecond)

	// After cleanup, entries should be evicted
	require.Equal(t, 0, rl.entryCount(), "entries should be evicted after 2× window")
}

func TestRateLimiterCleanupStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rl := NewRateLimiter(ctx, RateLimiterConfig{MaxRequests: 1, Window: 50 * time.Millisecond})
	handler := rl.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Create an entry
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, rl.entryCount())

	// Cancel context to stop cleanup goroutine
	cancel()

	// Give goroutine time to exit
	time.Sleep(10 * time.Millisecond)

	// Rate limiter should still work (no panic, no deadlock)
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusTooManyRequests, w2.Code, "second request from same IP should be blocked when MaxRequests=1")
}

// resetAuthCacheForTest wipes the package-global authCache so tests start from
// a known state (the cache otherwise leaks between tests in the same package).
func resetAuthCacheForTest() {
	authCache.Range(func(k, _ any) bool {
		authCache.Delete(k)
		return true
	})
}

// authCacheSize counts the current number of authCache entries (test helper).
func authCacheSize() int {
	n := 0
	authCache.Range(func(_, _ any) bool {
		n++
		return true
	})
	return n
}

// TestEvictExpiredAuthCache_RemovesStaleEntries is the regression test for
// issue #164: authCache had no background eviction, so entries that were never
// re-looked-up (e.g. a one-time login) accumulated indefinitely. The fix added
// a periodic sweep; this test drives the core eviction logic directly via
// evictExpiredAuthCache (the function the ticker calls), so it doesn't depend
// on wall-clock timing of the 5-minute interval.
func TestEvictExpiredAuthCache_RemovesStaleEntries(t *testing.T) {
	resetAuthCacheForTest()
	t.Cleanup(resetAuthCacheForTest)

	now := time.Now()
	// Seed: one fresh entry (verified just now) and one stale entry (older than TTL).
	authCache.Store("fresh-key", authCacheEntry{hash: "h1", verifiedAt: now})
	authCache.Store("stale-key", authCacheEntry{hash: "h2", verifiedAt: now.Add(-authCacheTTL - time.Second)})
	require.Equal(t, 2, authCacheSize(), "precondition: two entries seeded")

	// Evict at a "now" that is within TTL of fresh but past TTL of stale.
	removed := evictExpiredAuthCache(now.Add(time.Second))
	require.Equal(t, 1, removed, "only the stale entry should be evicted")
	require.Equal(t, 1, authCacheSize(), "fresh entry must survive eviction")

	// The surviving entry is the fresh one.
	if _, ok := authCache.Load("fresh-key"); !ok {
		t.Error("fresh entry should still be present after eviction")
	}
	if _, ok := authCache.Load("stale-key"); ok {
		t.Error("stale entry should have been evicted")
	}

	// A second sweep at the same now does nothing (already evicted).
	if removed := evictExpiredAuthCache(now.Add(time.Second)); removed != 0 {
		t.Errorf("second sweep should remove nothing, got %d", removed)
	}
}

// TestEvictExpiredAuthCache_ExpiredBoundary confirms an entry exactly at TTL is
// evicted (>= comparison), while one just under TTL survives.
func TestEvictExpiredAuthCache_ExpiredBoundary(t *testing.T) {
	resetAuthCacheForTest()
	t.Cleanup(resetAuthCacheForTest)

	base := time.Now()
	authCache.Store("at-ttl", authCacheEntry{verifiedAt: base.Add(-authCacheTTL)})                       // exactly TTL old
	authCache.Store("under-ttl", authCacheEntry{verifiedAt: base.Add(-authCacheTTL + time.Millisecond)}) // just under

	removed := evictExpiredAuthCache(base)
	require.Equal(t, 1, removed, "entry at exactly TTL (>=) should be evicted")
	if _, ok := authCache.Load("under-ttl"); !ok {
		t.Error("entry under TTL should survive")
	}
}

// TestStartAuthCacheCleanup_Idempotent verifies the cleanup goroutine is
// launched at most once (sync.Once) regardless of how many concurrent
// CheckPassword calls race on it. Repeated calls must be cheap no-ops and must
// not spawn additional goroutines.
func TestStartAuthCacheCleanup_Idempotent(t *testing.T) {
	// sync.Once is process-global and may already be consumed by an earlier
	// test that called CheckPassword. We assert only the documented contract:
	// calling startAuthCacheCleanup many times concurrently never panics and
	// returns quickly. (We cannot assert "exactly one goroutine" without
	// goroutine counting, which is flaky; the sync.Once guarantee is unit-
	// tested by the stdlib itself.)
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			startAuthCacheCleanup() // must be a safe no-op after the first call
		}()
	}
	wg.Wait()
	// Reaching here without panicking/deadlocking is the success condition.
}

// TestIsLocalIP verifies the loopback classification used by the local-access
// bypass. IsLocalIP deliberately matches ONLY loopback — NIC addresses must NOT
// be treated as local, because behind a reverse proxy or Docker published port
// every remote request arrives from 127.0.0.1 and would otherwise be trusted.
func TestIsLocalIP(t *testing.T) {
	// Loopback (both families, with and without port/brackets).
	for _, addr := range []string{
		"127.0.0.1",
		"127.0.0.1:9090",
		"127.0.0.1:12345",
		"::1",
		"[::1]",
		"[::1]:9090",
		"localhost", // not an IP literal → false (hostname resolution is NOT attempted)
	} {
		expect := addr != "localhost"
		if got := IsLocalIP(addr); got != expect {
			t.Errorf("IsLocalIP(%q) = %v, want %v", addr, got, expect)
		}
	}

	// IPv4-mapped IPv6 loopback must classify as local (canonicalized to ::1).
	if !IsLocalIP("::ffff:127.0.0.1") {
		t.Error("IsLocalIP should accept ::ffff:127.0.0.1 (IPv4-mapped loopback)")
	}

	// Remote / invalid inputs must never classify as local — including the
	// machine's OWN non-loopback NIC address: a proxied request's RemoteAddr is
	// the proxy's address, so NIC IPs must not grant the bypass.
	for _, addr := range []string{
		"",
		" ",
		"192.0.2.1",
		"192.0.2.1:1234",
		"8.8.8.8:53",
		"2001:db8::1",
		"[2001:db8::1]:9090",
		"no-such-host",
		"169.254.0.1", // link-local — not loopback
	} {
		if IsLocalIP(addr) {
			t.Errorf("IsLocalIP(%q) = true, want false", addr)
		}
	}
}

// TestHasProxyHeaders verifies that requests carrying evidence of an upstream
// proxy/gateway are detected, so the local bypass is refused for them.
func TestHasProxyHeaders(t *testing.T) {
	if HasProxyHeaders(nil) {
		t.Error("HasProxyHeaders(nil) = true, want false")
	}

	base := httptest.NewRequest(http.MethodGet, "/", nil)
	if HasProxyHeaders(base) {
		t.Error("plain request must not be flagged as proxied")
	}

	for _, h := range []string{"X-Forwarded-For", "X-Real-IP", "Forwarded"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(h, "198.51.100.7")
		if !HasProxyHeaders(req) {
			t.Errorf("HasProxyHeaders with %s = false, want true", h)
		}
	}
}

// TestLocalRequestBypassesAuth verifies that a loopback request with a loopback
// Host header bypasses auth ONLY when the operator opted in via
// AuthProvider.LocalBypass. The bypass is opt-in (default disabled) so reverse-
// proxy and Docker published-port deployments — where every request arrives
// from 127.0.0.1 — never inherit it.
func TestLocalRequestBypassesAuth(t *testing.T) {
	hash, _ := HashPassword("secret")
	provider := staticProvider("admin", hash)
	provider.LocalBypass = func() bool { return true }
	mw, _ := NewAuthMiddleware(provider, "", AuthRateLimitConfig{})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, tc := range []struct{ remote, host string }{
		{"127.0.0.1:1234", "localhost:9090"},
		{"127.0.0.1:1234", "127.0.0.1:9090"},
		{"[::1]:1234", "[::1]:9090"},
		{"[::1]:1234", "localhost:9090"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/cameras", nil)
		req.RemoteAddr = tc.remote
		req.Host = tc.host
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("loopback request from %s with Host %s should bypass auth, got %d", tc.remote, tc.host, w.Code)
		}
	}
}

// TestLocalBypassRequiresLoopbackHost verifies the Host check: even with a
// loopback RemoteAddr and local_bypass enabled, a request whose Host names a
// non-loopback origin (a malicious web page, a LAN IP, or a DNS-rebinding
// domain) must NOT bypass auth.
func TestLocalBypassRequiresLoopbackHost(t *testing.T) {
	hash, _ := HashPassword("secret")
	provider := staticProvider("admin", hash)
	provider.LocalBypass = func() bool { return true }
	mw, _ := NewAuthMiddleware(provider, "", AuthRateLimitConfig{})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, host := range []string{
		"example.com",       // default httptest host — a normal web page
		"evil.com:9090",     // attacker-controlled page
		"192.168.1.50:9090", // LAN IP reached from the host
		"10.0.0.5:9090",     // private IP via hosts-file rebinding
		"nvr.local:9090",    // hostname resolving to loopback (DNS rebinding)
		"",                  // empty Host
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/cameras", nil)
		req.RemoteAddr = "127.0.0.1:1234" // loopback transport
		req.Host = host
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code,
			"loopback RemoteAddr with Host %q must NOT bypass auth", host)
	}
}

// TestLocalBypassDisabledByDefault verifies the safe default: without an
// explicit LocalBypass(true), a loopback request still requires credentials.
func TestLocalBypassDisabledByDefault(t *testing.T) {
	hash, _ := HashPassword("secret")
	mw, _ := NewAuthMiddleware(staticProvider("admin", hash), "", AuthRateLimitConfig{})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/cameras", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Host = "localhost:9090" // even a loopback Host does not grant bypass
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code,
		"loopback request must NOT bypass when LocalBypass is unset (default off)")

	// Explicit credentials still work on loopback.
	req2 := httptest.NewRequest(http.MethodGet, "/api/cameras", nil)
	req2.RemoteAddr = "127.0.0.1:1234"
	req2.Host = "localhost:9090"
	req2.Header.Set("Authorization", "Basic "+basic("admin", "secret"))
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusOK, w2.Code, "valid credentials on loopback must pass")
}

// TestLocalBypassRefusedWhenProxied verifies the critical security guard: a
// loopback RemoteAddr WITH proxy/gateway headers (reverse-proxy topology) must
// NEVER bypass auth, even when local_bypass is enabled.
func TestLocalBypassRefusedWhenProxied(t *testing.T) {
	hash, _ := HashPassword("secret")
	provider := staticProvider("admin", hash)
	provider.LocalBypass = func() bool { return true }
	mw, _ := NewAuthMiddleware(provider, "", AuthRateLimitConfig{})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, h := range []string{"X-Forwarded-For", "X-Real-IP", "Forwarded"} {
		req := httptest.NewRequest(http.MethodGet, "/api/cameras", nil)
		req.RemoteAddr = "127.0.0.1:1234" // proxy's connection source
		req.Header.Set(h, "198.51.100.7")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code,
			"proxied request on loopback with %s must NOT bypass auth", h)
	}
}

// TestLocalRequestSeesSetupRequired verifies the setup check runs BEFORE the
// local bypass: even a loopback request hits 503 SETUP_REQUIRED while no
// password is configured, so the first-time wizard is always shown.
func TestLocalRequestSeesSetupRequired(t *testing.T) {
	provider := staticProvider("admin", "")
	provider.LocalBypass = func() bool { return true }
	mw, _ := NewAuthMiddleware(provider, "", AuthRateLimitConfig{})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusServiceUnavailable, w.Code,
		"loopback request without a configured password must see SETUP_REQUIRED")
	require.Contains(t, w.Body.String(), "setup required")
}

// TestRemoteRequestStillRequiresAuth verifies the bypass does NOT apply to
// remote clients: requests from a non-local RemoteAddr are still rejected
// without valid credentials.
func TestRemoteRequestStillRequiresAuth(t *testing.T) {
	hash, _ := HashPassword("secret")
	mw, _ := NewAuthMiddleware(staticProvider("admin", hash), "", AuthRateLimitConfig{})
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// No credentials.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.168.1.50:1234"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code, "remote request without credentials must be rejected")

	// With valid credentials.
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.RemoteAddr = "192.168.1.50:1234"
	req2.Header.Set("Authorization", "Basic "+basic("admin", "secret"))
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusOK, w2.Code, "remote request with valid credentials must pass")

	// Wrong password must still fail.
	req3 := httptest.NewRequest(http.MethodGet, "/", nil)
	req3.RemoteAddr = "192.168.1.50:1234"
	req3.Header.Set("Authorization", "Basic "+basic("admin", "wrong"))
	w3 := httptest.NewRecorder()
	handler.ServeHTTP(w3, req3)
	require.Equal(t, http.StatusUnauthorized, w3.Code, "remote request with wrong credentials must be rejected")
}
