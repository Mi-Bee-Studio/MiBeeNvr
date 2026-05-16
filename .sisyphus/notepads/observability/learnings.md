# Learnings

## Stats Page Runtime Metrics Implementation

### API Data Sources
- `GET /api/health` (public, no auth) returns `HealthResponse`: `{ status, checks: { database, storage, goroutines }, uptime }`
- `GET /api/cameras` (auth required) returns `Camera[]` — already fetched in Stats.svelte
- Health check statuses: "ok" | "degraded" | "unhealthy" for overall; individual checks use "ok" | "warning" | "error"

### Pattern: Adding new API types to api.ts
- Added `HealthCheck` and `HealthResponse` interfaces after `ApiError`
- Updated `healthCheck()` return type from `Promise<{ status: string }>` to `Promise<HealthResponse>`
- Be careful with edit tool append position — appending after an interface closing brace is fine, but need to ensure the previous interface's `}` exists

### Svelte Template: Single quotes in class attributes
- Svelte handles single-quoted strings inside `{...}` expressions within double-quoted attributes correctly
- `{camera.enabled ? 'bg-[var(--color-success)]' : 'bg-[var(--color-danger)]'}` works fine in `class="..."`

### i18n Pattern
- Both en.json and zh.json must have matching keys
- Must include trailing commas between entries — missing comma breaks JSON parse

### Design System Conventions
- Use `th-*` utility classes for theme-aware styling (th-bg-secondary, th-border, th-text-primary, etc.)
- Use CSS variable references for colors: `bg-[var(--color-success)]`, `bg-[var(--color-danger)]`
- Cards use `.card` class with `border th-border` 
- Inner metric boxes: `p-4 rounded-[var(--radius-sm)] th-bg-secondary border th-border`
- Lucide icons imported: `Activity, Clock, Cpu, Server` for runtime metrics
- Badge classes: `badge-success`, `badge-warning`, `badge-error`

## Observability Unit Tests

### Prometheus Testing Patterns
- `prometheus/client_model` `GetValue()` returns `float64` (not `*float64`) — no dereference needed
- CounterVec metrics with zero-value label combinations are NOT emitted by `Registry.Gather()` — must Inc/Add at least once to verify registration
- `NewGoCollector(WithGoCollections(GoRuntimeMemStatsCollection))` registers metrics prefixed with `go_`
- `NewProcessCollector(ProcessCollectorOpts{Namespace: "nvr"})` registers metrics prefixed with `nvr_process_`

### slog Testing Patterns
- `SetupLogger()` creates logger with handler writing to `os.Stdout` — cannot capture output via buffer
- Use `logger.Enabled(ctx, level)` to verify level configuration without needing to capture stdout
- `ComponentLogger()` uses `slog.Default().With(...)` — writes to whatever slog.Default is set to, so `slog.SetDefault()` works for testing
- `slog.SetDefault()` only affects future `slog.Default()` calls; loggers created before the change keep their original handler

### Middleware Testing Patterns
- `RequestLogger` returns `func(next http.Handler) http.Handler` — test by wrapping a `httptest.NewRecorder()` handler
- `StatusRecorder` is in `middleware/recorder.go` (same package as logging_test.go) — accessible directly
- Path normalization in RequestLogger: `/api/recordings/123456789` → `/api/recordings/{id}`

### Health/Readyz Testing
- `TestHandler(nil, nil)` creates handler with nil db/store — useful for testing error paths
- `/api/readyz` returns 503 with `{status: "not ready", checks: {...}}` when unhealthy
- `/api/health` always returns 200 with full check details regardless of health status
