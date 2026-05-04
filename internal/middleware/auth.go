package middleware

import (
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

// rateLimitEntry tracks failed auth attempts per IP.
type rateLimitEntry struct {
	count    int
	windowStart time.Time
}

// authFailures tracks per-IP failed auth attempts.
var authFailures sync.Map

// NewAuthMiddleware returns a middleware that protects endpoints with HTTP Basic auth.
// If passwordHash is empty, authentication is rejected (not bypassed).
func NewAuthMiddleware(username, passwordHash string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := extractIP(r.RemoteAddr)

			// Rate limit check FIRST — expire stale entries
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

			if strings.TrimSpace(passwordHash) == "" {
			logger.Info("rejected request: no password hash configured", "ip", ip)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}

			user, pass, ok := r.BasicAuth()
			if !ok || user != username || !CheckPassword(pass, passwordHash) {
				// Increment failure counter
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

			// Successful auth — reset counter
			authFailures.Delete(ip)
			next.ServeHTTP(w, r)
		})
	}
}

// HashPassword generates a bcrypt hash from a plaintext password.
func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// authCacheEntry stores a successful bcrypt verification result.
type authCacheEntry struct {
	hash      string // the bcrypt hash that was verified
	verifiedAt time.Time
}

// authCache caches successful bcrypt verifications keyed by username+password.
// This avoids repeating the expensive (~430ms on ARM) bcrypt verify on every request.
var authCache sync.Map

// CheckPassword compares a plaintext password against a bcrypt hash.
// Results are cached for authCacheTTL to avoid repeated bcrypt overhead.
func CheckPassword(password, hash string) bool {
	if strings.TrimSpace(hash) == "" {
		return false
	}

	cacheKey := password + "\x00" + hash

	// Check cache first
	if v, ok := authCache.Load(cacheKey); ok {
		entry := v.(authCacheEntry)
		if entry.hash == hash && time.Since(entry.verifiedAt) < authCacheTTL {
			return true // cache hit
		}
		// Cache expired or hash changed, remove stale entry
		authCache.Delete(cacheKey)
	}

	// Cache miss — do expensive bcrypt verify
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	if err == nil {
		authCache.Store(cacheKey, authCacheEntry{hash: hash, verifiedAt: time.Now()})
	}
	return err == nil
}

// extractIP extracts the IP address from a RemoteAddr string (host:port).
func extractIP(remoteAddr string) string {
	// Handle IPv6 [::1]:port
	if idx := strings.LastIndex(remoteAddr, "]"); idx != -1 {
		return remoteAddr[:idx+1]
	}
	// Handle IPv4 host:port or bare host
	if idx := strings.LastIndex(remoteAddr, ":"); idx != -1 {
		return remoteAddr[:idx]
	}
	return remoteAddr
}

// ResetAuthFailures clears all rate limit entries. For testing only.
func ResetAuthFailures() {
	authFailures.Range(func(key, _ interface{}) bool {
		authFailures.Delete(key)
		return true
	})
}
