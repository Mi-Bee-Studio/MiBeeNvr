# MiBee NVR — Middleware Package

## OVERVIEW

HTTP middleware: BasicAuth + bcrypt, request logging (slog), security headers, response status recording.

## STRUCTURE

```
auth.go         # BasicAuth middleware — bcrypt verification, rate limiting, verification cache
auth_test.go    # Auth tests — rate limiting, cache, password verification
logging.go      # Request logging middleware — slog-based structured request logs
logging_test.go # Logging tests
security.go     # Security headers middleware (CSP, X-Frame-Options, etc.)
security_test.go
recorder.go     # StatusRecorder — wraps ResponseWriter to capture status code + bytes
slogutil.go     # slog utilities — custom error handler for chi
slogutil_test.go
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Change auth logic | `auth.go` `BasicAuth()` | Returns middleware function, bcrypt compare |
| Rate limiting | `rateLimitEntry` in `auth.go` | Per-IP, sliding window (5 attempts / 60s) |
| Auth cache | `authCacheEntry` in `auth.go` | Caches successful bcrypt result for 5 min |
| Password hashing | `HashPassword()` | Exported, also used by CLI `hash-password` subcommand |
| Request logging | `logging.go` | Logs method, path, status, duration, bytes, remote_addr |
| Security headers | `security.go` | CSP, X-Content-Type-Options, X-Frame-Options, Referrer-Policy |

## CONVENTIONS

- **bcrypt cost**: Default cost (10). `HashPassword()` exported for CLI usage
- **Rate limiting**: In-memory map (no Redis). Per-IP sliding window. `sync.Mutex` protected
- **Auth caching**: Caches username→bcrypt_hash match for 5 minutes. Avoids expensive bcrypt on every request
- **Structured logging**: Uses `slog` with `component` key. Request logs include `duration`, `status`, `method`, `path`
- **StatusRecorder**: Wraps `http.ResponseWriter` to capture status code and bytes written. Used by logging middleware

## ANTI-PATTERNS

- **DO NOT** store plaintext passwords — always use `HashPassword()` with bcrypt
- **DO NOT** bypass auth middleware for sensitive endpoints — public routes are only `/api/health` and `/api/metrics`
