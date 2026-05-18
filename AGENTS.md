# MiBee NVR — Project Context

## Overview

Lightweight NVR in Go. Single static binary with embedded Svelte 5 SPA. Supports RTSP (H.264/H.265/MJPEG), HTTP JPEG, ONVIF, and Xiaomi proprietary cameras. SQLite metadata. Targets Raspberry Pi 3B.

## Structure

```
cmd/mibee-nvr/       # Entry point — config loading, subsystem init, graceful shutdown
internal/
  api/               # REST API (chi) — recordings CRUD, cameras, stats, settings, ONVIF proxy
  camera/            # Camera manager — recorder lifecycle, create/start/stop/restart
  cleanup/           # Retention-based cleanup — time + disk threshold
  config/            # YAML config — CameraConfig, MergeConfig, per-camera overrides
  ftp/               # FTP server — read-only file access
  hls/               # HLS live streaming — on-demand, idle eviction, sub-stream fallback
  merge/             # Segment merging — streaming MP4 merge, reduces file count
  metrics/           # Prometheus metrics — recordings, cameras, errors
  middleware/        # HTTP middleware — BasicAuth (bcrypt+cache), logging, security headers
  model/             # Core interfaces — Recorder, RecorderStatus, protocol/encoding enums
  mqtt/              # MQTT trigger — message-driven recording control
  plugin/            # Protocol registry — protocol enable/disable, camera/manager.go dispatch
  recorder/          # 5 recorder impls — H264/H265/MJPEG/HTTP-JPEG/ONVIF, all auto-reconnect
  storage/           # SQLite DB + file manager — recordings CRUD, atomic segment lifecycle
  ui/                # Embedded frontend — Go embed of web/dist/
  upload/            # Upload handler — recording file upload
  webdav/            # WebDAV server — read-only or read-write
  xiaomi/            # Xiaomi camera support — MISS protocol, CS2 P2P, cloud auth, H264/H265 (migrated from plugins/)
  ai/                # AI interface definitions — Detector, AIProvider (pre-design, not implemented)

  xiaomi/            # Xiaomi camera plugin — MISS protocol, CS2 P2P, cloud auth, H264/H265
plugin/
  proto/             # Protocol Buffers — nvr.proto, generated Go code
tests/
  mock_plugin/       # Mock NVR plugin for integration testing
web/                 # Svelte 5 frontend — dark/light theme, i18n, Chart.js stats, HLS live
deploy/              # systemd service file
docs/                # EN/ZH documentation
```

## Where To Look

| Task | Location | Notes |
|------|----------|-------|
| Add camera protocol | New file in `recorder/` + `internal/xiaomi/` + case in `camera/manager.go` | Must implement `model.Recorder`; example: internal/xiaomi/ for Xiaomi cameras |
| Fix protocol registry | `internal/plugin/plugin.go` | ProtocolRegistry — protocol enable/disable, `plugin.LookupProtocol()`, camera/manager.go dispatch |
| Fix recording logic | `recorder/*.go` or `internal/xiaomi/recorder.go` | Shared pattern: `Start→run→connectAndRecord→writeFrames` |
| Fix reconnection | `run()` in any recorder | Exponential backoff + jitter, capped at `MaxBackoff` |
| Add API endpoint | `api/handler.go` → `Routes()` | Chi router, `writeJSON`/`writeError` helpers |
| Change DB schema | `storage/db.go` `Init()` | CREATE IF NOT EXISTS migrations |
| Fix time handling | `storage/db.go` `timeToDB()`/`parseTime()` | UTC storage, 5+ legacy format compat |
| Add frontend page | `web/src/routes/` | Svelte 5, `$state()`, lucide-svelte icons |
| Fix MP4 muxing | `muxer/mp4mux.go` | Used by all recorders, cgo-free |
| Fix Xiaomi camera | `internal/xiaomi/` | MISS protocol, CS2 P2P, cloud API, ChaCha20 encryption |
| Fix HLS streaming | `hls/manager.go` | On-demand, max 4 streams, 60s idle timeout |
| Fix segment merge | `merge/` | Streaming merge (1MB buffer), never loads full files |
| Config changes | `config/config.go` | YAML with per-camera overrides (merge, retention) |

## Conventions

- **Recorder pattern**: All follow `New*Recorder()→Start(ctx)→run() loop→connectAndRecord()→writeFrames()` goroutine. Auto-reconnect with exponential backoff
- **Protocol Registry**: `plugin.Register()` in `init()`. `camera/manager.go` uses `plugin.LookupProtocol()` for protocol dispatch.
- **Feature Toggle**: protocol enable/disable via SQLite `feature_flags` table. `GET/PUT /api/features` API. Settings page UI.
- **Temp files**: All screenshots, test artifacts, and temporary files go in `tmp/` (gitignored). Never leave temp files in project root
- **Logger**: `slog.Default().With("component", "name")`. All packages use package-level `var logger`
- **Metrics**: Optional `*metrics.Metrics` passed to recorders. All have `incActive/decActive/recordSegmentCreated/recordBytes/recordError`
- **Segment lifecycle**: `CreateSegment(temp)` → write → `muxer.Close()` → `CloseSegment(temp→final)` atomic rename → `DB.InsertRecording()`
- **IDR sync**: New segments wait for keyframe (H264: NAL 5, H265: NAL 19/20) before creating muxer
- **Frontend i18n**: `web/src/lib/i18n/` — `en.json`/`zh.json`, keyed by route/component
- **CGO_ENABLED=0**: Pure Go, no C dependencies. `cgo-free` MP4 muxing via `abema/go-mp4`
- **Embed**: Frontend built with `npm run build` → copied to `internal/ui/static/` → `go:embed`

## Browser Testing Standards

**All frontend features MUST be tested with a headed browser before merge. No headless-only testing.**

Headed browser testing is required because:
- Visual regressions (layout, theme, i18n overflow) are invisible in headless mode
- HLS video playback and canvas rendering behave differently headless vs headed
- Interactive features (PTZ controls, drag, dropdowns) need visual verification
- Screenshot artifacts for CI review require real rendering

### Test Framework
- **Tool**: Playwright (Chromium, headed mode)
- **Location**: `e2e-tests/` (separate from Go `tests/`)
- **Config**: `e2e-tests/playwright.config.ts`
- **Commands**:
  ```bash
  cd e2e-tests && npx playwright test              # Default (headed on CI)
  cd e2e-tests && npx playwright test --headed      # Explicit headed
  cd e2e-tests && npm run test:headed               # Headed via npm script
  ```

### Test Scenarios (Required Coverage)
- Settings page: toggle switches, save, verify persistence
- Camera CRUD: add/edit/delete cameras, protocol dropdown filtering
- HLS live view: start/stop stream, multi-camera concurrent
- Monitoring dashboard: camera status, recording stats
- i18n: language switching (EN/ZH), all labels display correctly
- Theme: dark/light mode toggle
- Recording playback: MP4 video, MJPEG frame sequence, download

### Test Configuration
- Browser: Chromium **headed mode** (never headless for frontend tests)
- Base URL: http://localhost:9090 (or RPi target IP)
- Auth: Basic auth with configured credentials

### Anti-Pattern
- **DO NOT** run frontend E2E tests in headless mode — visual bugs go undetected
- **DO NOT** skip headed browser verification before merging UI changes

## Anti-Patterns

- **DO NOT** use `duration <= 0` guard — sub-ms durations truncate to 0. Use `duration < time.Millisecond`
- **DO NOT** block on channel sends — use non-blocking `select` (ring buffer pattern)
- **DO NOT** start segment without IDR frame — produces black/gray frames
- **DO NOT** load full MP4 into memory — streaming merge with 1MB buffer
- **DO NOT** set `SegmentDur` > 30s on RPi 3B — MP4Muxer holds samples in RAM
- **DO NOT** use `time.Time.String()` for DB — contains monotonic clock. Use `timeToDB()`
- **DO NOT** use `os.ReadFile()+w.Write()` for downloads — no Content-Length. Use `http.ServeFile()`
- **DO NOT** suppress type errors — no `as any`, `@ts-ignore`, `@ts-expect-error`
- **DO NOT** forget `t.Helper()` in test helpers
- **DO NOT** leave temp files in project root — all artifacts go in `tmp/`

## Commands

```bash
make build              # Local build
make cross              # Cross-compile arm64
make test               # Run tests
make lint               # go vet
make deploy RPi_HOST=user@host  # Deploy to RPi
```

## Deployment

- **Target**: Raspberry Pi 3B (`mickey@192.168.63.31`)
- **Binary path on RPi**: `/mnt/data/nvr/bin/mibee-nvr`
- **Service**: `mibee-nvr` (systemd)
- **Data dir**: `/mnt/data/nvr/`
- **Config**: `/mnt/data/nvr/mibee-nvr.yaml`
- **Architecture**: arm64 (cross-compile from x86_64 dev machine)

### Deploy Commands

```bash
make cross              # Cross-compile arm64 binary
make deploy             # Deploy to RPi (stop service → upload → start)
make rollback           # Rollback to previous binary
make deploy-check       # Verify service health
```

<!-- rtk-instructions v2 -->
## RTK (Rust Token Killer) - Token-Optimized Commands

### Golden Rule

**Always prefix commands with `rtk`**. If RTK has a dedicated filter, it uses it. If not, it passes through unchanged. This means RTK is always safe to use.

**Important**: Even in command chains with `&&`, use `rtk`:
```bash
# ✗ Wrong
git add . && git commit -m "msg" && git push

# ✓ Correct
rtk git add . && rtk git commit -m "msg" && rtk git push
```

### Build & Compile (80-90% savings)
```bash
rtk cargo build         # Cargo build output
rtk cargo check         # Cargo check output
rtk cargo clippy        # Clippy warnings grouped by file (80%)
rtk tsc                 # TypeScript errors grouped by file/code (83%)
rtk lint                # ESLint/Biome violations grouped (84%)
rtk prettier --check    # Files needing format only (70%)
rtk next build          # Next.js build with route metrics (87%)
```

### Test (60-99% savings)
```bash
rtk cargo test          # Cargo test failures only (90%)
rtk go test             # Go test failures only (90%)
rtk jest                # Jest failures only (99.5%)
rtk vitest              # Vitest failures only (99.5%)
rtk playwright test     # Playwright failures only (94%)
rtk pytest              # Python test failures only (90%)
rtk rake test           # Ruby test failures only (90%)
rtk rspec               # RSpec failures only (60%)
rtk test <cmd>          # Generic test wrapper - failures only
```

### Git (59-80% savings)
```bash
rtk git status          # Compact status
rtk git log             # Compact log (works with all git flags)
rtk git diff            # Compact diff (80%)
rtk git show            # Compact show (80%)
rtk git add             # Ultra-compact confirmations (59%)
rtk git commit          # Ultra-compact confirmations (59%)
rtk git push            # Ultra-compact confirmations
rtk git pull            # Ultra-compact confirmations
rtk git branch          # Compact branch list
rtk git fetch           # Compact fetch
rtk git stash           # Compact stash
rtk git worktree        # Compact worktree
```

Note: Git passthrough works for ALL subcommands, even those not explicitly listed.

### GitHub (26-87% savings)
```bash
rtk gh pr view <num>    # Compact PR view (87%)
rtk gh pr checks        # Compact PR checks (79%)
rtk gh run list         # Compact workflow runs (82%)
rtk gh issue list       # Compact issue list (80%)
rtk gh api              # Compact API responses (26%)
```

### JavaScript/TypeScript Tooling (70-90% savings)
```bash
rtk pnpm list           # Compact dependency tree (70%)
rtk pnpm outdated       # Compact outdated packages (80%)
rtk pnpm install        # Compact install output (90%)
rtk npm run <script>    # Compact npm script output
rtk npx <cmd>           # Compact npx command output
rtk prisma              # Prisma without ASCII art (88%)
```

### Files & Search (60-75% savings)
```bash
rtk ls <path>           # Tree format, compact (65%)
rtk read <file>         # Code reading with filtering (60%)
rtk grep <pattern>      # Search grouped by file (75%)
rtk find <pattern>      # Find grouped by directory (70%)
```

### Analysis & Debug (70-90% savings)
```bash
rtk err <cmd>           # Filter errors only from any command
rtk log <file>          # Deduplicated logs with counts
rtk json <file>         # JSON structure without values
rtk deps                # Dependency overview
rtk env                 # Environment variables compact
rtk summary <cmd>       # Smart summary of command output
rtk diff                # Ultra-compact diffs
```

### Infrastructure (85% savings)
```bash
rtk docker ps           # Compact container list
rtk docker images       # Compact image list
rtk docker logs <c>     # Deduplicated logs
rtk kubectl get         # Compact resource list
rtk kubectl logs        # Deduplicated pod logs
```

### Meta Commands
```bash
rtk gain                # View token savings statistics
rtk gain --history      # View command history with savings
rtk discover            # Analyze sessions for missed RTK usage
rtk proxy <cmd>         # Run command without filtering (debugging)
rtk init                # Add RTK instructions to CLAUDE.md
```

Overall average: **60-90% token reduction** on common development operations.
<!-- /rtk-instructions -->
