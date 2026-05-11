# MiBee NVR — PROJECT KNOWLEDGE BASE

**Stack**: Go 1.26 (CGO_ENABLED=0) + Svelte 5 + TailwindCSS 4 + SQLite (modernc)
**Module**: `github.com/Mi-Bee-Studio/MiBeeNvr`

## OVERVIEW

Lightweight NVR for Raspberry Pi 3B. Records RTSP (H.264/H.265/MJPEG) and HTTP JPEG cameras to MP4 segments. Provides Web UI, HLS live view, WebDAV (configurable R/W), FTP, MQTT, ONVIF discovery (stub), Prometheus metrics, and segment merging. Single static binary with embedded SPA. E2E tests via Playwright.

## STRUCTURE

```
cmd/mibee-nvr/main.go   # Entry point — wires all subsystems, graceful shutdown, hash-password CLI subcommand
internal/
  api/handler.go         # REST API (chi router) — recordings/cameras CRUD, stats, settings, download, ONVIF, PTZ, stream proxy (see api/AGENTS.md)
  camera/manager.go      # CameraManager — manages recorder lifecycle per camera, CRUD + config persistence, metrics integration
  camera/id.go           # GenerateCameraID — nanoid-style random IDs via crypto/rand
  cleanup/cleanup.go     # CleanupManager — retention-based + disk-threshold-based recording deletion
  config/config.go       # YAML config load/save/validate with defaults, MergeConfig, HLS config
  ftp/server.go          # FTP server (ftpserverlib) — anonymous always rejected, upload auto-registers DB recordings
  hls/manager.go         # HLS Manager — on-demand live preview via gohlslib, async write buffers, idle eviction, sub-stream fallback (see hls/AGENTS.md)
  merge/manager.go       # MergeManager — periodic MP4/MJPEG segment merging to reduce file count (see merge/AGENTS.md)
  merge/mp4merge.go      # Streaming MP4 merge — placeholder moov, limitedWriter, sample data copy
  merge/parser.go        # MP4 segment parser — extracts sample tables, codec params, keyframe flags
  merge/mjpegmerge.go    # MJPEG segment merge — directory-based JPEG concatenation
  metrics/metrics.go     # Prometheus metrics — custom registry, Go runtime (memstats only), 9 NVR gauges/counters
  middleware/auth.go     # BasicAuth + bcrypt middleware with rate limiting and verification caching (see middleware/AGENTS.md)
  model/types.go         # Core interfaces (Recorder, StorageProvider) + domain types + constants
  mqtt/client.go         # MQTT client — subscribe to trigger topic for event-driven recording
  muxer/mp4mux.go        # MP4Muxer — low-level MP4 box writing (abema/go-mp4), SPS resolution parsing
  recorder/h264.go       # H264Recorder — RTSP→RTP decode→ring buffer→MP4 segment, SPS change detection, auto-reconnect (see recorder/AGENTS.md)
  recorder/h265.go       # H265Recorder — RTSP HEVC→RTP→ring buffer→MP4, VPS/SPS/PPS handling, IRAP sync
  recorder/mjpeg.go      # MJPEGRecorder — RTSP MJPEG→JPEG frames to directory segments, frame sampling
  recorder/http_jpeg.go  # HTTPJPEGRecorder — HTTP multipart MJPEG stream→JPEG frames, boundary parsing
  recorder/onvif.go      # ONVIFRecorder — delegate recorder via ONVIF GetStreamUri → RTSP sub-recorder
  storage/db.go          # SQLite DB — recordings/cameras CRUD, time format handling (UTC, multi-format parse) (see storage/AGENTS.md)
  storage/manager.go     # FileManager — segment create/write/close (temp→atomic rename), disk usage, crash recovery
  ui/embed.go            # //go:embed of built SPA from internal/ui/static/
  upload/handler.go      # HTTP multipart upload handler (100MB max), auto-registers DB recordings
  webdav/server.go       # WebDAV server — configurable read-only/read-write, auto-camera creation from upload path
  onvif/                 # ONVIF discovery + PTZ (STUB — all methods return errors, needs onvif-go integration)
web/                     # Svelte 5 frontend (see web/AGENTS.md)
deploy/                  # systemd services (mibee-nvr, mediamtx, rpicam-stream, onvif-simulator) + Caddyfile
tests/integration_test.go # 7 integration scenarios with shared test helpers
e2e-tests/               # Playwright E2E tests — recording playback, download, filters, pagination
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Add API endpoint | `internal/api/handler.go` | Register in `Routes()`, add handler method |
| Add camera protocol | `internal/recorder/` | Implement `model.Recorder` interface, add case in `camera/manager.go:createRecorder()` |
| Modify recording pipeline | `internal/recorder/h264.go` | Ring buffer → MP4Muxer; check `writeFrames()` for NALU handling |
| Add H.265 support | `internal/recorder/h265.go` | Mirrors H264 structure, HEVC NAL types (VPS=32, SPS=33, PPS=34, IRAP=19-20) |
| Change DB schema | `internal/storage/db.go` | Schema in `Init()`, migrations are CREATE IF NOT EXISTS |
| Add frontend page | `web/src/routes/` | Create component, add route case in `App.svelte` parseRoute() |
| Change config format | `internal/config/config.go` | Struct + YAML tags + `applyDefaults()` + `Validate()` |
| Fix time handling | `internal/storage/db.go` | `timeToDB()`/`parseTime()`/`scanTime()` — UTC, multi-format backward compat |
| Storage/file operations | `internal/storage/manager.go` | Temp file → atomic rename pattern, `CleanupTempFiles()` for crash recovery |
| Auth/password | `internal/middleware/auth.go` | `HashPassword()` + CLI: `mibee-nvr hash-password <pw>` |
| MP4 muxing internals | `internal/muxer/mp4mux.go` | Raw box writing: ftyp, moov, moof, mdat; SPS resolution parsing |
| HLS live streaming | `internal/hls/manager.go` | On-demand streams, idle timeout, sub-stream fallback, frame rate limiting |
| Segment merging | `internal/merge/` | `MergeManager.Run()`, `MergeMP4Segments()`, `ParseSegment()`, `MergeMJPEGSegments()` |
| Prometheus metrics | `internal/metrics/metrics.go` | Custom registry, `/metrics` endpoint (public, no auth) |
| ONVIF discovery | `internal/onvif/` | `Discover()`, `Client` operations — currently stub, needs onvif-go library |
| E2E tests | `e2e-tests/tests/` | Playwright, connects to live NVR at `http://192.168.63.31:9090` |
| Add test helper | `internal/api/handler.go` | `TestHandler()`/`TestHandlerWithAuth()` factories (exported, used by integration tests too) |

## CODE MAP

| Symbol | Type | Location | Role |
|--------|------|----------|------|
| `model.Recorder` | interface | model/types.go | Start/Stop/Status — implemented by H264Recorder, H265Recorder, MJPEGRecorder, HTTPJPEGRecorder |
| `model.StorageProvider` | interface | model/types.go | CreateSegment/CloseSegment/ListRecordings etc. |
| `CameraManager` | struct | camera/manager.go | Thread-safe (RWMutex) recorder lifecycle, config persist, recorder factory by protocol |
| `H264Recorder` | struct | recorder/h264.go | RTSP client, RTP decode, ring buffer, backoff reconnect, SPS change detection |
| `H265Recorder` | struct | recorder/h265.go | RTSP HEVC, VPS/SPS/PPS tracking, IRAP sync, mirrors H264 architecture |
| `HTTPJPEGRecorder` | struct | recorder/http_jpeg.go | HTTP multipart MJPEG stream, boundary detection, JPEG validation |
| `MP4Muxer` | struct | muxer/mp4mux.go | Low-level MP4 box writer (AddH264Track, AddH265Track, WriteSample), SPS resolution parsing |
| `Manager` (HLS) | struct | hls/manager.go | On-demand HLS muxers per camera, async write buffers (120 frames), idle eviction, sub-stream reader |
| `MergeManager` | struct | merge/manager.go | Periodic segment merge passes, groups by SPS/PPS, batch limits, disk space checks |
| `MergeMP4Segments` | func | merge/mp4merge.go | Streaming MP4 merge with placeholder moov, limitedWriter, in-place header patching |
| `ParseSegment` | func | merge/parser.go | Extract sample tables, codec params, keyframe flags from MP4 segment |
| `Metrics` | struct | metrics/metrics.go | Custom Prometheus registry with 9 NVR metrics (bytes, cameras, segments, cleanup, errors) |
| `DB` | struct | storage/db.go | SQLite WAL mode, UTC timestamps, dynamic query builder |
| `Manager` (storage) | struct | storage/manager.go | File ops: CreateSegment(temp→final), DeleteFile, GetDiskUsage |
| `Handler` | struct | api/handler.go | Chi router, all REST endpoints, JSON responses, HLS/ONVIF proxy |
| `Config` | struct | config/config.go | YAML config with atomic save (temp+rename), MergeConfig, camera HLS config |
| `CleanupManager` | struct | cleanup/cleanup.go | Periodic: retention expiry + disk threshold enforcement |
| `Client` (ONVIF) | struct | onvif/client.go | STUB — ONVIF device client, all methods return errors |

## CONVENTIONS

- **Error handling**: Non-fatal failures log warning and continue (e.g., file deletion after DB delete, cleanup at startup)
- **Config persistence**: `config.Save()` uses atomic write (temp file + rename) to prevent corruption
- **Timestamps**: All DB timestamps stored as UTC strings (`2006-01-02 15:04:05.999999999`), `parseTime()` handles 5+ legacy formats for backward compat
- **Test helpers**: `TestHandler()` / `TestHandlerWithAuth()` factories in api package; `t.Helper()` pattern throughout (MANDATORY)
- **Test assertions**: `testify/require` exclusively (not `assert`) — 1145 require vs ~50 assert occurrences
- **Logging**: `slog.Default().With("component", "pkg-name")` per package — structured logging with key-value attributes
- **Camera IDs**: Generated via `crypto/rand` 8-char alphanumeric (`GenerateCameraID()`)
- **Recording IDs**: `fmt.Sprintf("%d", time.Now().UnixNano())` — nanosecond timestamp
- **Segment lifecycle**: temp file → write frames → close muxer → atomic rename to final path → insert DB record → remove temp
- **Metrics**: Optional `opts ...*metrics.Metrics` variadic parameter pattern across recorder, camera, storage, cleanup packages
- **Dependency injection**: `NewXxx()` constructors with required deps as params, no DI framework, manual wiring in main.go
- **Static build**: `CGO_ENABLED=0` always — no C dependencies, pure Go SQLite (modernc)
- **Protocol strings**: Transport-only: `rtsp`, `http`, `onvif` — encoding is separate field (`h264`, `h265`, `mjpeg`, `jpeg`). Legacy combined format (`rtsp_h264`, `rtsp_h265`, `rtsp_mjpeg`, `http_jpeg`) auto-parsed via `model.ParseLegacyProtocol()`

## ANTI-PATTERNS (THIS PROJECT)

- **DO NOT** use `time.Time.String()` for DB storage — contains monotonic clock, incompatible with SQLite `datetime()`
- **DO NOT** set `segment_duration` > 30s on RPi 3B — MP4Muxer holds all samples in RAM per segment, 2min segments = 60MB+
- **DO NOT** treat `retention_days: 0` as "keep forever" — code treats 0 as unconfigured, defaults to 30
- **DO NOT** forget `t.Helper()` in test helper functions — strictly enforced, 188 occurrences in 17 files
- **DO NOT** forget `SampleDescriptionIndex: 1` in mp4.StscEntry — default 0 causes ffmpeg `STSC entry is invalid` error
- **DO NOT** use `duration <= 0` guard in H264Recorder — sub-millisecond durations also truncate to 0 via `.Milliseconds()`, use `duration < time.Millisecond`
- **DO NOT** embed credentials in subresource URLs (e.g. `//user:pass@host/path`) — modern browsers block this for `<video>` src and downloads
- **DO NOT** use `os.ReadFile()+w.Write()` for file downloads — no Content-Length/Accept-Ranges, breaks progress and resume; use `http.ServeFile()`
- **DO NOT** use `fetch→blob→<a>.click()` for large file downloads — no download progress, entire file loads to memory; use XHR with `onprogress` callback
- **DO NOT** use `os.O_RDONLY` in bit-flag checks — it's 0, so `flags&os.O_RDONLY != 0` is always false
- **DO NOT** assume ONVIF package is functional — all methods are stubs returning errors, needs onvif-go library integration
- **DO NOT** treat WebDAV as always read-only — configurable via `read_write: true` in config; default is read-only
- **DO NOT** hand-assemble build commands (e.g. `go build -o ...` or `GOOS=linux go build ...`) — the Makefile contains frontend build, asset copy, cleanup, version injection, and static linking logic that hand-rolled commands will miss. ALWAYS use `rtk make build` (local) or `rtk make cross` (RPi arm64). Frontend: `cd web && rtk npm run build`, then assets are embedded on next `make build`.

## COMMANDS

```bash
# Build (static binary, CGO_ENABLED=0)
rtk make build              # → ./mibee-nvr (local arch)
rtk make cross              # → ./mibee-nvr-arm64 (RPi)

# Test
rtk go test ./... -v        # All tests
rtk go test ./internal/storage/... -v  # Package-specific

# Lint
rtk go vet ./...

# Frontend
cd web && rtk npm run build # Build SPA → web/dist/ (then re-build Go binary to embed)
cd web && rtk npm run dev   # Dev server with HMR

# Deploy to NVR RPi (cross-compile + SSH + restart)
rtk make deploy RPi_HOST=mickey@192.168.63.31
rtk make rollback RPi_HOST=mickey@192.168.63.31  # Restore previous binary
rtk make deploy-check RPi_HOST=mickey@192.168.63.31  # Verify service + health

# E2E tests (against live NVR)
cd e2e-tests && npm test    # Playwright, connects to http://192.168.63.31:9090
```

## DEPLOYMENT & TESTING

### NVR RPi — `ssh mickey@192.168.63.31`
- **Hostname**: rpi3b-storeage, Debian 13 (trixie), aarch64, 905MB RAM
- **Storage**: 2.7TB USB disk at `/mnt/data` (ext4) — recordings, DB, config all here
- **Binary**: `/mnt/data/nvr/bin/mibee-nvr` (20MB, version 0.1.0-dev)
- **Config**: `/mnt/data/nvr/mibee-nvr.yaml`
- **DB**: `/mnt/data/nvr/mibee-nvr.db` (SQLite, ~14MB)
- **Ports**: 9090 (NVR API+UI), 2121 (FTP), 80 (Caddy — currently NOT proxying NVR)
- **Service**: `systemctl start/stop/restart mibee-nvr`
- **No Go installed** — must cross-compile from dev machine (`make cross`)
- **Active cameras** (from config):
  - RPi CSI Camera (`rtsp_h264` via 192.168.63.162) — always on
  - 3× Xiaomi phones (`rtsp_h265` via 192.168.62.138 RTSP proxy)
  - LUATOS ESP32-S3 (`http_jpeg`, currently disabled)
- **Live Web UI**: `http://192.168.63.31:9090` (auth: admin/admin)

### Camera RPi — `ssh mickey@192.168.63.162`
- **Hostname**: rpi3b-cam, Debian 13 (trixie), aarch64, 905MB RAM
- **Camera**: ov5647 (RPi Camera V1, 5MP, 2592x1944)
- **Pipeline**: `rpicam-vid -n --codec h264 -w 1280 -h 720 -fps 15 -t 0 -o - | ffmpeg -c:v copy -f mpegts udp://127.0.0.1:8555`
- **Streaming**: mediamtx v1.11.3 reads UDP:8555, exposes:
  - RTSP: `rtsp://192.168.63.162:8554/stream` (used by NVR)
  - HLS: `:8888`, WebRTC: `:8889`, RTMP: `:1935`
- **Services**: `mediamtx.service`, `rpicam-stream.service` (depends on mediamtx)
- **Uses WiFi** (wlan0) — may have higher latency than Ethernet
- **Restart camera**: `sudo systemctl restart rpicam-stream`

## NOTES

- **Memory budget**: RPi 3B has 905MB RAM. Default segment_duration in example is 10m but README recommends 30s for RPi 3B. Each segment holds ~15-20MB RAM. Total stable at ~300MB with 30s segments.
- **SQLite pragmas**: WAL mode, NORMAL sync, 5s busy timeout, 2MB cache — tuned for SD card media on RPi.
- **RTP errors**: `"invalid FU-A packet (non-starting)"` from UDP→mediamtx pipeline are non-critical, don't affect recording.
- **Web UI rebuild**: After `cd web && npm run build`, must `cp -r web/dist/* internal/ui/static/` then rebuild Go binary to embed updated assets.
- **Cross-compilation**: `GOOS=linux GOARCH=arm64` — target is always Linux ARM64 for RPi.
- **MP4 container tolerance**: Browser `<video>` and VLC tolerate STSC SampleDescriptionIndex=0, DTS duplicates. But ffmpeg strict mode warns. Generate per-spec.
- **MP4 muxer STTS**: STTS delta uses `duration.Milliseconds()` → uint32. timescale=1000: 1ms=1 tick. Each sample duration MUST be ≥1ms or DTS won't increment.
- **MP4 muxer close order**: `MP4Muxer.Close()` writes ftyp+moov+mdat to tempPath, THEN `CloseSegment()` does temp→final rename. Never reverse.
- **Frontend file download**: Large files use XHR + `responseType: 'blob'` + `onprogress`. Small files (MJPEG JPEG) can use `fetch→blob`.
- **Frontend video playback**: `<video>` src must use blob URL via XHR/fetch with Authorization header. Modern browsers block embedded credentials in URLs.
- **Frontend HLS**: hls.js with `xhrSetup` for auth header injection. `enableWorker: false` for RPi browser compat.
- **Prometheus metrics**: Custom registry (not default). `/metrics` endpoint is public (no auth). Go runtime limited to memstats for RPi 3B.
- **ONVIF stub status**: All 14 methods in onvif/ return errors. Frontend has "Scan ONVIF Devices" UI but backend needs `onvif-go` library.
- **Segment merge**: Streaming merge (1MB fixed buffer) — no full file load. Groups by SPS/PPS. Requires 1.1x estimated size free disk. Configurable batch limit.
- **HLS live view**: On-demand muxers, max 4 concurrent streams (evicts oldest), 60s idle timeout, 120-frame write buffer, sub-stream fallback for bandwidth.
- **E2E tests**: Playwright against live NVR instance. Base URL configured in `e2e-tests/playwright.config.ts`. 6/7 tests passing.
- **Docker**: Two methods — multi-stage build (amd64, distroless) or host cross-compile (arm64, scratch). Version from git short SHA.
- **CI/CD**: GitHub Actions — CI on push/PR to main (lint+test+build), Release on tag `v*` (multi-arch binaries + GitHub Release).
- **Logging migration**: Codebase migrated from `log.Printf` to `slog` structured logging. All packages use `slog.Default().With("component", "name")`.
<!-- rtk-instructions v2 -->
# RTK (Rust Token Killer) - Token-Optimized Commands

## Golden Rule

**Always prefix commands with `rtk`**. If RTK has a dedicated filter, it uses it. If not, it passes through unchanged. This means RTK is always safe to use.

**Important**: Even in command chains with `&&`, use `rtk`:
```bash
# ❌ Wrong
git add . && git commit -m "msg" && git push

# ✅ Correct
rtk git add . && rtk git commit -m "msg" && rtk git push
```

## RTK Commands by Workflow

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
rtk rspec               # RSpec test failures only (60%)
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

### Network (65-70% savings)
```bash
rtk curl <url>          # Compact HTTP responses (70%)
rtk wget <url>          # Compact download output (65%)
```

### Meta Commands
```bash
rtk gain                # View token savings statistics
rtk gain --history      # View command history with savings
rtk discover            # Analyze Claude Code sessions for missed RTK usage
rtk proxy <cmd>         # Run command without filtering (for debugging)
rtk init                # Add RTK instructions to CLAUDE.md
rtk init --global       # Add RTK to ~/.claude/CLAUDE.md
```

## Token Savings Overview

| Category | Commands | Typical Savings |
|----------|----------|-----------------|
| Tests | vitest, playwright, cargo test | 90-99% |
| Build | next, tsc, lint, prettier | 70-87% |
| Git | status, log, diff, add, commit | 59-80% |
| GitHub | gh pr, gh run, gh issue | 26-87% |
| Package Managers | pnpm, npm, npx | 70-90% |
| Files | ls, read, grep, find | 60-75% |
| Infrastructure | docker, kubectl | 85% |
| Network | curl, wget | 65-70% |

Overall average: **60-90% token reduction** on common development operations.
<!-- /rtk-instructions -->
