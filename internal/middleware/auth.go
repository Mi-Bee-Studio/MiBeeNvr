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

type rateLimitEntry struct {
	count       int
	windowStart time.Time
}

var authFailures sync.Map

// NewAuthMiddleware returns a middleware that protects endpoints with HTTP Basic auth.
// If passwordHash is empty but plaintextPassword is non-empty, it is auto-hashed via bcrypt.
// Returns the middleware and the effective hash used (for config persistence).
// If both are empty, authentication is bypassed (first-time setup mode).
func NewAuthMiddleware(username, passwordHash, plaintextPassword string) (func(http.Handler) http.Handler, string) {
	effectiveHash := passwordHash
	if strings.TrimSpace(passwordHash) == "" && strings.TrimSpace(plaintextPassword) != "" {
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

			if strings.TrimSpace(effectiveHash) == "" {
				// No password configured — allow access (first-time setup mode)
				// User can set password later via Web UI settings page
				next.ServeHTTP(w, r)
				return
			}

			user, pass, ok := r.BasicAuth()
			if !ok || user != username || !CheckPassword(pass, effectiveHash) {
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
