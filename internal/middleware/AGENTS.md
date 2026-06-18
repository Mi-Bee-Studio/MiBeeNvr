# MiBee NVR — Middleware Package

## OVERVIEW

HTTP middleware: dual auth (API Key for MiBeeVision + BasicAuth for browser users), request logging (slog), security headers, request trace_id, auth metrics, remote log shipping. API Key auth (Bearer `mbv_` tokens) runs BEFORE BasicAuth — use `IsAPIKeyAuthenticated(r)` to distinguish callers.

## STRUCTURE

```
auth.go         # BasicAuth middleware — bcrypt verification, rate limiting, verification cache, setup-required gate
auth_test.go    # Auth tests — rate limiting, cache, password verification
apikey.go       # API Key auth — MiBeeVision Bearer tokens (mbv_ prefix), constant-time compare, ?api_key= SSE/WS fallback
trace.go        # Request trace_id middleware — injects X-Request-Id, propagates via context
trace_test.go
auth_metrics.go # Auth attempt metrics recorder — success/failure counters for Prometheus
logging.go      # Request logging middleware — slog-based structured request logs
logging_test.go # Logging tests
security.go     # Security headers middleware (CSP, X-Frame-Options, etc.)
security_test.go
headers.go      # Misc response headers
headers_test.go
recorder.go     # StatusRecorder — wraps ResponseWriter to capture status code + bytes
recorder_test.go
slogutil.go     # slog utilities — custom error handler for chi
slogutil_test.go
remotelog/      # Remote log shipping subdirectory
  handler.go    # RemoteLog handler — batch ingest, rate-limited, auth-gated
  handler_test.go
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
| API Key auth | `apikey.go` `APIKeyAuthMiddleware()` | MiBeeVision Bearer tokens (mbv_), constant-time compare, runs before BasicAuth |
| API Key context | `apikey.go` `IsAPIKeyAuthenticated()` / `APIKeyNameFromContext()` | Distinguish API Key callers from browser sessions |
| Request trace_id | `trace.go` | Injects X-Request-Id header, propagates via context for log correlation |
| Auth metrics | `auth_metrics.go` `recordAuthAttempt()` | Success/failure counters for Prometheus |
| Remote log shipping | `remotelog/handler.go` | Batch log ingest from external sources, rate-limited, auth-gated |
| Generate API key | `apikey.go` `GenerateAPIKey()` | Produces mbv_ + 40-char hex (44 chars total) |

## CONVENTIONS

- **bcrypt cost**: Default cost (10). `HashPassword()` exported for CLI usage
- **Rate limiting**: In-memory map (no Redis). Per-IP sliding window. `sync.Mutex` protected
- **Auth caching**: Caches username→bcrypt_hash match for 5 minutes. Avoids expensive bcrypt on every request
- **Structured logging**: Uses `slog` with `component` key. Request logs include `duration`, `status`, `method`, `path`
- **StatusRecorder**: Wraps `http.ResponseWriter` to capture status code and bytes written. Used by logging middleware
- **Dual auth order**: API Key (Bearer mbv_) attempted FIRST, then BasicAuth. Use `IsAPIKeyAuthenticated(r)` to distinguish MiBeeVision from browser sessions
- **API Key generation**: `GenerateAPIKey()` produces `mbv_` + 40-char hex. Constant-time comparison (`subtle.ConstantTimeCompare`) prevents timing attacks
- **API Key SSE/WS fallback**: `?api_key=mbv_...` query param supported for SSE/WebSocket (can't set Authorization header)
- **Trace ID**: Every request gets an X-Request-Id via `trace.go`. Propagated through context for log correlation

## ANTI-PATTERNS

- **DO NOT** store plaintext passwords — always use `HashPassword()` with bcrypt
- **DO NOT** bypass auth middleware for sensitive endpoints — public routes are `/api/health`, `/api/metrics`, `/models/{filename}`, `/api/recordings/{id}/download` + `/merged`
