package middleware

import (
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
	authMaxFailures   = 20
	authWindowMinutes = 1
	authCacheTTL      = 5 * time.Minute
)

type rateLimitEntry struct {
	count       int
	windowStart time.Time
}

var authFailures sync.Map

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
func NewAuthMiddleware(provider AuthProvider, plaintextPassword string) (func(http.Handler) http.Handler, string) {
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
			ip := extractIP(r.RemoteAddr)

			if v, ok := authFailures.Load(ip); ok {
				entry := v.(rateLimitEntry)
				if time.Since(entry.windowStart) > time.Duration(authWindowMinutes)*time.Minute {
					authFailures.Delete(ip)
				} else if entry.count >= authMaxFailures {
					logger.Info("rate limited request", "ip", ip, "failures", entry.count)
					w.WriteHeader(http.StatusTooManyRequests)
					return
				}
			}

			// Dynamic hash: prefer provider's current value (supports setup),
			// fall back to auto-hashed value from initialization.
			currentHash := provider.GetHash()
			if strings.TrimSpace(currentHash) == "" {
				currentHash = effectiveHash
			}
			currentUsername := provider.GetUsername()

			if strings.TrimSpace(currentHash) == "" {
				// No password configured — reject all requests with setup guidance
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("WWW-Authenticate", `Basic realm="MiBee NVR"`)
				w.WriteHeader(http.StatusServiceUnavailable)
				w.Write([]byte(`{"error":"setup required","code":"SETUP_REQUIRED"}`))
				return
			}


			user, pass, ok := r.BasicAuth()
			if !ok {
				// Fallback: check ?token= query parameter (for WebSocket which cannot set headers)
				if tok := r.URL.Query().Get("token"); tok != "" {
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
				if v, ok := authFailures.Load(ip); ok {
					entry := v.(rateLimitEntry)
					if time.Since(entry.windowStart) > time.Duration(authWindowMinutes)*time.Minute {
						authFailures.Store(ip, rateLimitEntry{count: 1, windowStart: time.Now()})
					} else {
						entry.count++
						authFailures.Store(ip, entry)
					}
				} else {
					authFailures.Store(ip, rateLimitEntry{count: 1, windowStart: time.Now()})
				}

				w.WriteHeader(http.StatusUnauthorized)
				return
			}

			authFailures.Delete(ip)
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

// CheckPassword compares a plaintext password against a bcrypt hash.
// Results are cached for authCacheTTL to avoid repeated bcrypt overhead.
func CheckPassword(password, hash string) bool {
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

func ResetAuthFailures() {
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

// NewRateLimiter returns a middleware that rate limits requests per IP
// using a sliding window approach.
func NewRateLimiter(cfg RateLimiterConfig) func(http.Handler) http.Handler {
	var mu sync.Mutex
	entries := make(map[string]*rateLimitEntry)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := extractIP(r.RemoteAddr)

			mu.Lock()
			entry, ok := entries[ip]
			now := time.Now()

			if !ok || now.Sub(entry.windowStart) > cfg.Window {
				entries[ip] = &rateLimitEntry{count: 1, windowStart: now}
				mu.Unlock()
				next.ServeHTTP(w, r)
				return
			}

			entry.count++
			if entry.count > cfg.MaxRequests {
				mu.Unlock()
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			mu.Unlock()
			next.ServeHTTP(w, r)
		})
	}
}
