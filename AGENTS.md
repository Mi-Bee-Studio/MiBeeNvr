# MiBee NVR — PROJECT KNOWLEDGE BASE

**Stack**: Go 1.26 (CGO_ENABLED=0) + Svelte 5 + TailwindCSS 4 + SQLite (modernc)
**Module**: `github.com/Mi-Bee-Studio/MiBeeNvr`

## OVERVIEW

Lightweight NVR for Raspberry Pi 3B. Records RTSP (H.264/MJPEG) and HTTP JPEG cameras to MP4 segments. Provides Web UI, WebDAV (read-only), FTP, and MQTT integration. Single static binary with embedded SPA.

## STRUCTURE

```
cmd/mibee-nvr/main.go   # Entry point — wires all subsystems, graceful shutdown on SIGINT/SIGTERM
internal/
  api/handler.go         # REST API (chi router) — recordings CRUD, cameras CRUD, stats, settings, download
  camera/manager.go      # CameraManager — manages recorder lifecycle per camera, CRUD + config persistence
  camera/id.go           # GenerateCameraID — nanoid-style random IDs
  cleanup/cleanup.go     # CleanupManager — retention-based + disk-threshold-based recording deletion
  config/config.go       # YAML config load/save/validate with defaults
  ftp/server.go          # FTP server (ftpserverlib) — anonymous always rejected
  middleware/auth.go      # BasicAuth + bcrypt middleware; HashPassword()
  model/types.go         # Core interfaces (Recorder, StorageProvider) + domain types + constants
  mqtt/client.go         # MQTT client — subscribe to trigger topic
  muxer/mp4mux.go        # MP4Muxer — low-level MP4 box writing (abema/go-mp4)
  recorder/h264.go       # H264Recorder — RTSP→RTP decode→ring buffer→MP4 segment writer, auto-reconnect
  recorder/mjpeg.go      # MJPEGRecorder — RTSP MJPEG→JPEG frames to directory segments
  storage/db.go          # SQLite DB — recordings/cameras CRUD, time format handling (UTC, multi-format parse)
  storage/manager.go     # FileManager — segment create/write/close (temp→atomic rename), disk usage
  ui/embed.go            # //go:embed of built SPA from internal/ui/static/
  upload/handler.go      # HTTP multipart upload handler (100MB max)
  webdav/server.go       # WebDAV server — READ-ONLY, all writes return 403
web/                     # Svelte 5 frontend (see web/AGENTS.md)
deploy/                  # systemd services (mibee-nvr, mediamtx, rpicam-stream) + Caddyfile
tests/integration_test.go # 7 integration scenarios
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Add API endpoint | `internal/api/handler.go` | Register in `Routes()`, add handler method |
| Add camera protocol | `internal/recorder/` | Implement `model.Recorder` interface, add case in `camera/manager.go:createRecorder()` |
| Modify recording pipeline | `internal/recorder/h264.go` | Ring buffer → MP4Muxer; check `writeFrames()` for NALU handling |
| Change DB schema | `internal/storage/db.go` | Schema in `Init()`, migrations are CREATE IF NOT EXISTS |
| Add frontend page | `web/src/routes/` | Svelte component, add nav in `App.svelte` |
| Change config format | `internal/config/config.go` | Struct + YAML tags + `applyDefaults()` + `Validate()` |
| Fix time handling | `internal/storage/db.go` | `timeToDB()`/`parseTime()`/`scanTime()` — UTC, multi-format backward compat |
| Storage/file operations | `internal/storage/manager.go` | Temp file → atomic rename pattern, `CleanupTempFiles()` for crash recovery |
| Auth/password | `internal/middleware/auth.go` | `HashPassword()` exists but NO CLI subcommand wired (docs outdated) |
| MP4 muxing internals | `internal/muxer/mp4mux.go` | Raw box writing: ftyp, moov, moof, mdat |

## CODE MAP

| Symbol | Type | Location | Role |
|--------|------|----------|------|
| `model.Recorder` | interface | model/types.go | Start/Stop/Status — implemented by H264Recorder, MJPEGRecorder |
| `model.StorageProvider` | interface | model/types.go | CreateSegment/CloseSegment/ListRecordings etc. |
| `CameraManager` | struct | camera/manager.go | Thread-safe (RWMutex) recorder lifecycle, config persist |
| `H264Recorder` | struct | recorder/h264.go | RTSP client, RTP decode, ring buffer, backoff reconnect |
| `MP4Muxer` | struct | muxer/mp4mux.go | Low-level MP4 box writer (AddH264Track, WriteSample) |
| `DB` | struct | storage/db.go | SQLite WAL mode, UTC timestamps, dynamic query builder |
| `Manager` | struct | storage/manager.go | File ops: CreateSegment(temp→final), DeleteFile, GetDiskUsage |
| `Handler` | struct | api/handler.go | Chi router, all REST endpoints, JSON responses |
| `Config` | struct | config/config.go | YAML config with atomic save (temp+rename) |
| `CleanupManager` | struct | cleanup/cleanup.go | Periodic: retention expiry + disk threshold enforcement |

## CONVENTIONS

- **Error handling**: Non-fatal failures log warning and continue (e.g., file deletion after DB delete, cleanup at startup)
- **Config persistence**: `config.Save()` uses atomic write (temp file + rename) to prevent corruption
- **Timestamps**: All DB timestamps stored as UTC strings (`2006-01-02 15:04:05.999999999`), `parseTime()` handles 5+ legacy formats for backward compat
- **Test helpers**: `TestHandler()` / `TestHandlerWithAuth()` factories in api package; `t.Helper()` pattern throughout
- **Test assertions**: `testify/require` exclusively (not `assert`)
- **Logging**: `log.Printf` with `[component-name]` prefix (e.g., `[h264-recorder cam1]`, `[camera-manager]`)
- **Camera IDs**: Generated via `crypto/rand` 8-char alphanumeric (`GenerateCameraID()`)
- **Recording IDs**: `fmt.Sprintf("%d", time.Now().UnixNano())` — nanosecond timestamp
- **Segment lifecycle**: temp file → write frames → close muxer → atomic rename to final path → insert DB record → remove temp
- **WebDAV**: Intentionally read-only (403 on all write methods)
- **FTP**: Anonymous access always rejected; plaintext credentials from config
- **HTTP auth**: bcrypt hash via `middleware.HashPassword()`, checked on every protected route
- **Static build**: `CGO_ENABLED=0` always — no C dependencies, pure Go SQLite (modernc)

## ANTI-PATTERNS (THIS PROJECT)

- **DO NOT** use `time.Time.String()` for DB storage — contains monotonic clock, incompatible with SQLite `datetime()`
- **DO NOT** set `segment_duration` > 30s on RPi 3B — MP4Muxer holds all samples in RAM per segment, 2min segments = 60MB+
- **DO NOT** treat `retention_days: 0` as "keep forever" — code treats 0 as unconfigured, defaults to 30
- **DO NOT** add write operations to WebDAV — intentionally read-only for security
- **DO NOT** forget `t.Helper()` in test helper functions
- **DO NOT** use `os.O_RDONLY` in bit-flag checks — it's 0, so `flags&os.O_RDONLY != 0` is always false (known FTP bug, fixed)
- **DO NOT** forget `SampleDescriptionIndex: 1` in mp4.StscEntry — default 0 causes ffmpeg `STSC entry is invalid` error
- **DO NOT** use `duration <= 0` guard in H264Recorder — sub-millisecond durations also truncate to 0 via `.Milliseconds()`, use `duration < time.Millisecond`
- **DO NOT** embed credentials in subresource URLs (e.g. `//user:pass@host/path`) — modern browsers block this for `<video>` src and downloads
- **DO NOT** use `os.ReadFile()+w.Write()` for file downloads — no Content-Length/Accept-Ranges, breaks progress and resume; use `http.ServeFile()`
- **DO NOT** use `fetch→blob→<a>.click()` for large file downloads — no download progress, entire file loads to memory; use XHR with `onprogress` callback

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

# Install to RPi
rtk make install            # → /mnt/data/nvr/bin/mibee-nvr
rtk make install-service    # + systemd service setup
```

## NOTES

- **Memory budget**: RPi 3B has 905MB RAM. Default segment_duration in example is 10m but README recommends 30s for RPi 3B. Each segment holds ~15-20MB RAM. Total stable at ~300MB with 30s segments.
- **`hash-password` gap**: README documents `mibee-nvr hash-password <pw>` CLI subcommand but it's NOT implemented in main.go. `middleware.HashPassword()` exists but has no CLI entry point.
- **SQLite pragmas**: WAL mode, NORMAL sync, 5s busy timeout, 2MB cache — tuned for SD card media on RPi.
- **RTP errors**: `"invalid FU-A packet (non-starting)"` from UDP→mediamtx pipeline are non-critical, don't affect recording.
- **Web UI rebuild**: After `cd web && npm run build`, must `cp -r web/dist/* internal/ui/static/` then rebuild Go binary to embed updated assets.
- **Cross-compilation**: `GOOS=linux GOARCH=arm64` — target is always Linux ARM64 for RPi.
- **MP4 container tolerance**: Browser `<video>` 和 VLC 等 player 对 MP4 容器错误有较强容错能力——STSC SampleDescriptionIndex=0 会默认指向第一个 stsd entry，DTS 重复会被忽略。但 ffmpeg 严格模式 (`-f null -`) 会报 warning。生成 MP4 时应严格遵守规范。
- **MP4 muxer STTS 时间戳**: STTS delta 用 `duration.Milliseconds()` 转 uint32。timescale=1000 时 1ms=1 tick。必须保证每个 sample 的 duration >= 1ms (1 tick)，否则 DTS 不递增。
- **MP4 muxer segment 关闭顺序**: `MP4Muxer.Close()` 在 `CloseSegment()` (atomic rename) 之前调用。Close() 写 ftyp+moov+mdat 到 tempPath，然后 CloseSegment() 做 temp→final rename。不要改变这个顺序。
- **前端文件下载架构**: 大文件下载用 XHR (`XMLHttpRequest`) + `responseType: 'blob'` + `onprogress` 回调显示进度，完成后 `URL.createObjectURL` 触发保存。不要用 `fetch()` 因为没有进度回调。小文件 (MJPEG 单帧 JPEG) 用 `fetch→blob` 没问题。
- **前端视频播放**: `<video>` 的 src 必须用 blob URL（通过 XHR/fetch + Authorization header 获取），不能直接用带嵌入凭证的 HTTP URL（`//user:pass@host`）——现代浏览器会拦截。

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
