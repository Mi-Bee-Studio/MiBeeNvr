package middleware

import (
	"context"
	"encoding/base64"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var logger = slog.Default().With("component", "auth")

const (
	authCacheTTL = 5 * time.Minute
	// authCacheCleanupInterval is how often authCacheCleanupLoop sweeps expired
	// authCache entries. Set to authCacheTTL so eviction stays ahead of churn;
	// the loop mirrors RateLimiter.cleanupLoop's 2×Window cadence in spirit.
	authCacheCleanupInterval = authCacheTTL
)

type rateLimitEntry struct {
	count       int
	windowStart time.Time
}

var authFailures sync.Map

// AuthRateLimitConfig controls auth failure rate limiting.
type AuthRateLimitConfig struct {
	Enabled       bool
	MaxFailures   int
	WindowMinutes int
}

// AuthProvider returns the current username and effective password hash.
// Used by the auth middleware to dynamically read credentials (e.g. after setup).
type AuthProvider struct {
	GetUsername func() string
	GetHash     func() string
}

// NewAuthMiddleware returns a middleware that protects endpoints with HTTP Basic auth.
// If passwordHash is empty but plaintextPassword is non-empty, it is auto-hashed via bcrypt.
// Returns the middleware and the effective hash used (for config persistence).
// If both are empty, all requests return 503 Service Unavailable with setup guidance.
// The provider is called on every request so changes (e.g. setup) take effect immediately.
// rateLimit controls auth failure rate limiting; when .Enabled is false, no limiting is applied.
func NewAuthMiddleware(provider AuthProvider, plaintextPassword string, rateLimit AuthRateLimitConfig) (func(http.Handler) http.Handler, string) {
	initialHash := provider.GetHash()
	effectiveHash := initialHash
	if strings.TrimSpace(initialHash) == "" && strings.TrimSpace(plaintextPassword) != "" {
		hash, err := HashPassword(plaintextPassword)
		if err != nil {
			logger.Error("failed to hash plaintext password", "error", err)
		} else {
			logger.Info("auto-hashed plaintext password from config")
			effectiveHash = hash
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// If already authenticated via API Key middleware, skip BasicAuth.
			if IsAPIKeyAuthenticated(r.Context()) {
				next.ServeHTTP(w, r)
				return
			}

			ip := extractIP(r.RemoteAddr)

			// Auth failure rate limiting (optional — enabled via config).
			if rateLimit.Enabled {
				maxFailures := rateLimit.MaxFailures
				windowMin := rateLimit.WindowMinutes
				if maxFailures <= 0 {
					maxFailures = 20
				}
				if windowMin <= 0 {
					windowMin = 1
				}
				if v, ok := authFailures.Load(ip); ok {
					entry := v.(rateLimitEntry)
					if time.Since(entry.windowStart) > time.Duration(windowMin)*time.Minute {
						authFailures.Delete(ip)
					} else if entry.count >= maxFailures {
						logger.Info("rate limited request", "ip", ip, "failures", entry.count)
						recordAuthRateLimited()
						w.WriteHeader(http.StatusTooManyRequests)
						return
					}
				}
			}

			// Dynamic hash: prefer provider's current value (supports setup),

			// Dynamic hash: prefer provider's current value (supports setup),
			// fall back to auto-hashed value from initialization.
			currentHash := provider.GetHash()
			if strings.TrimSpace(currentHash) == "" {
				currentHash = effectiveHash
			}
			currentUsername := provider.GetUsername()

			if strings.TrimSpace(currentHash) == "" {
				// No password configured — reject all requests with setup guidance
				recordAuthAttempt("no_password")
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("WWW-Authenticate", `Basic realm="MiBee NVR"`)
				w.WriteHeader(http.StatusServiceUnavailable)
				w.Write([]byte(`{"error":"setup required","code":"SETUP_REQUIRED"}`))
				return
			}

			// --- Signed session token (mbs_...) ---
			// Tried BEFORE BasicAuth so already-logged-in browsers (which send a
			// Bearer token) skip the bcrypt path entirely. The token is validated
			// purely by recomputing the HMAC against the current bcrypt hash, so a
			// password change naturally invalidates old tokens.
			if tok := bearerSessionToken(r); tok != "" {
				claims, err := VerifySessionToken(tok, currentHash, time.Now())
				if err != nil {
					recordAuthAttempt("failure")
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				// Username must match the configured account. (A token signed under
				// an old username after the admin renamed the account is rejected.)
				if claims.Sub != currentUsername {
					recordAuthAttempt("failure")
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				// Sliding renewal: if little lifetime remains, mint a fresh token
				// and hand it back in the response header. The frontend swaps it in.
				expiresAt := time.Unix(claims.EXP, 0)
				if NeedsRenewal(expiresAt, time.Now()) {
					// Mint a fresh token; its expiry is carried inside the token
					// itself, so we discard the returned expiresAt here.
					newTok, _ := SignSessionToken(claims.Sub, currentHash, time.Now())
					// It is safe to set the header before calling next: headers are
					// flushed only when the first Write/WriteHeader happens downstream.
					w.Header().Set(RenewedTokenHeader, newTok)
				}
				recordAuthAttempt("success")
				if rateLimit.Enabled {
					authFailures.Delete(ip)
				}
				next.ServeHTTP(w, r)
				return
			}

			user, pass, ok := r.BasicAuth()
			if !ok {
				// Fallback: check ?token= query parameter.
				// Session tokens (mbs_) are validated above via the Bearer path; the
				// ?token= variant for WS/sendBeacon is also handled by bearerSessionToken.
				// Anything left here is the legacy base64(user:pass) form, kept only
				// for migration compatibility.
				if tok := r.URL.Query().Get("token"); tok != "" && !IsSessionToken(tok) {
					decoded, err := base64.StdEncoding.DecodeString(tok)
					if err == nil {
						parts := strings.SplitN(string(decoded), ":", 2)
						if len(parts) == 2 {
							user = parts[0]
							pass = parts[1]
							ok = true
						}
					}
				}
			}
			if !ok || user != currentUsername || !CheckPassword(pass, currentHash) {
				recordAuthAttempt("failure")
				// Track auth failure only when rate limiting is enabled.
				if rateLimit.Enabled {
					windowMin := rateLimit.WindowMinutes
					if windowMin <= 0 {
						windowMin = 1
					}
					if v, ok := authFailures.Load(ip); ok {
						entry := v.(rateLimitEntry)
						if time.Since(entry.windowStart) > time.Duration(windowMin)*time.Minute {
							authFailures.Store(ip, rateLimitEntry{count: 1, windowStart: time.Now()})
						} else {
							entry.count++
							authFailures.Store(ip, entry)
						}
					} else {
						authFailures.Store(ip, rateLimitEntry{count: 1, windowStart: time.Now()})
					}
				}

				w.WriteHeader(http.StatusUnauthorized)
				return
			}

			if rateLimit.Enabled {
				authFailures.Delete(ip)
			}
			recordAuthAttempt("success")
			next.ServeHTTP(w, r)
		})
	}, effectiveHash
}

// HashPassword generates a bcrypt hash from a plaintext password.
func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

type authCacheEntry struct {
	hash       string
	verifiedAt time.Time
}

var authCache sync.Map

// authCacheCleanupStarted ensures the background eviction goroutine for
// authCache is launched at most once per process. Stale entries are otherwise
// only evicted lazily on re-lookup (CheckPassword), so cache keys that are
// never revisited (e.g. a one-time login from a transient client) would
// accumulate indefinitely. The cleanup loop mirrors RateLimiter.cleanupLoop.
var authCacheCleanupStarted sync.Once

// startAuthCacheCleanup launches a background goroutine that periodically
// evicts expired authCache entries. It is safe to call concurrently; the first
// caller wins via authCacheCleanupStarted and subsequent calls are no-ops.
// The goroutine runs for the lifetime of the process (authCache is process-
// global), so it intentionally does NOT take a cancellable context — mirroring
// the package-level authFailures rate-limiter, which is likewise never stopped.
// The interval is a multiple of authCacheTTL so eviction stays ahead of churn
// without excessive scanning.
func startAuthCacheCleanup() {
	authCacheCleanupStarted.Do(func() {
		go authCacheCleanupLoop()
	})
}

// authCacheCleanupLoop walks authCache every authCacheCleanupInterval and
// deletes entries older than authCacheTTL.
func authCacheCleanupLoop() {
	ticker := time.NewTicker(authCacheCleanupInterval)
	defer ticker.Stop()
	for range ticker.C {
		evictExpiredAuthCache(time.Now())
	}
}

// evictExpiredAuthCache deletes every authCache entry whose verifiedAt is older
// than authCacheTTL relative to now. Extracted from authCacheCleanupLoop so the
// eviction logic is unit-testable without waiting on the ticker. Returns the
// number of entries removed (useful for assertions).
func evictExpiredAuthCache(now time.Time) int {
	removed := 0
	authCache.Range(func(k, v any) bool {
		entry, ok := v.(authCacheEntry)
		if !ok {
			// Malformed entry (should never happen) — drop defensively.
			authCache.Delete(k)
			removed++
			return true
		}
		if now.Sub(entry.verifiedAt) >= authCacheTTL {
			authCache.Delete(k)
			removed++
		}
		return true
	})
	return removed
}

// CheckPassword compares a plaintext password against a bcrypt hash.
// Results are cached for authCacheTTL to avoid repeated bcrypt overhead.
func CheckPassword(password, hash string) bool {
	// Lazily start the periodic eviction goroutine on first use. Idempotent via
	// sync.Once; subsequent calls are essentially free.
	startAuthCacheCleanup()

	if strings.TrimSpace(hash) == "" {
		return false
	}

	cacheKey := password + "\x00" + hash

	if v, ok := authCache.Load(cacheKey); ok {
		entry := v.(authCacheEntry)
		if entry.hash == hash && time.Since(entry.verifiedAt) < authCacheTTL {
			return true
		}
		authCache.Delete(cacheKey)
	}

	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	if err == nil {
		authCache.Store(cacheKey, authCacheEntry{hash: hash, verifiedAt: time.Now()})
	}
	return err == nil
}

func extractIP(remoteAddr string) string {
	if idx := strings.LastIndex(remoteAddr, "]"); idx != -1 {
		return remoteAddr[:idx+1]
	}
	if idx := strings.LastIndex(remoteAddr, ":"); idx != -1 {
		return remoteAddr[:idx]
	}
	return remoteAddr
}

func resetAuthFailures() {
	authFailures.Range(func(key, _ interface{}) bool {
		authFailures.Delete(key)
		return true
	})
}

// RateLimiterConfig defines parameters for a per-IP rate limiter.
type RateLimiterConfig struct {
	MaxRequests int
	Window      time.Duration
}

// RateLimiter provides per-IP rate limiting with automatic stale entry cleanup.
type RateLimiter struct {
	mu      sync.Mutex
	entries map[string]*rateLimitEntry
	cfg     RateLimiterConfig
}

// NewRateLimiter creates a new RateLimiter and starts a background cleanup goroutine.
// The cleanup goroutine exits when ctx is cancelled.
// Every 2×Window period, stale entries (older than Window) are evicted.
func NewRateLimiter(ctx context.Context, cfg RateLimiterConfig) *RateLimiter {
	rl := &RateLimiter{
		entries: make(map[string]*rateLimitEntry),
		cfg:     cfg,
	}
	go rl.cleanupLoop(ctx)
	return rl
}

// Handler returns an HTTP middleware that rate limits per client IP.
func (rl *RateLimiter) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := extractIP(r.RemoteAddr)

		rl.mu.Lock()
		entry, ok := rl.entries[ip]
		now := time.Now()

		if !ok || now.Sub(entry.windowStart) > rl.cfg.Window {
			rl.entries[ip] = &rateLimitEntry{count: 1, windowStart: now}
			rl.mu.Unlock()
			next.ServeHTTP(w, r)
			return
		}

		entry.count++
		if entry.count > rl.cfg.MaxRequests {
			rl.mu.Unlock()
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		rl.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

// cleanupLoop runs a ticker that evicts entries with expired windows.
// It exits when ctx is cancelled.
func (rl *RateLimiter) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * rl.cfg.Window)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			rl.evictStale()
		case <-ctx.Done():
			return
		}
	}
}

// evictStale removes all entries whose window has expired.
func (rl *RateLimiter) evictStale() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	for ip, entry := range rl.entries {
		if now.Sub(entry.windowStart) > rl.cfg.Window {
			delete(rl.entries, ip)
		}
	}
}

// entryCount returns the number of tracked entries (for testing).
func (rl *RateLimiter) entryCount() int {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return len(rl.entries)
}
