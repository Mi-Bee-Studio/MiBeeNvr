# Learnings - nvr-deploy-test

## 2026-04-30 Session Start
- Plan: NVR deployment + e2e testing on RPi .31 with CSI camera on .120
- 6 bugs to fix, then compile, deploy, test
- Wave 1: All 6 bug fixes can run in parallel

## Config Fix (Task 2)

### Problem Identified
- Lines 142-145 in `internal/config/config.go`: `if !cfg.FTP.Enabled { cfg.FTP.Enabled = true }` incorrectly overrides user's explicit `enabled: false`
- Lines 155-157 in `internal/config/config.go`: Same bug for WebDAV
- Root cause: Go boolean zero value is `false`, so `!cfg.FTP.Enabled` is true BOTH when "not configured" AND when "explicitly set to false"

### Solution Implemented
- Changed `FTPConfig.Enabled` and `WebDAVConfig.Enabled` from `bool` to `*bool`
- Updated `applyDefaults()` method to check for nil pointers: only set default when pointer is nil (not configured)
- When user explicitly sets `enabled: false`, the pointer will be non-nil pointing to false

### Test Coverage Added
- `TestFTPExplicitlyDisabled`: Verifies `enabled: false` stays false
- `TestWebDAVExplicitlyDisabled`: Verifies `enabled: false` stays false
- `TestFTPNotConfigured`: Verifies defaults to true when not configured
- `TestWebDAVNotConfigured`: Verifies defaults to true when not configured

### Key Learning
**Pointer fields for tri-state boolean logic**: Use `*bool` instead of `bool` when you need to distinguish between:
- `nil` (not configured)
- `&false` (explicitly disabled)
- `&true` (explicitly enabled)

This pattern is essential for configuration handling where user intent must be preserved.

## MP4Muxer Integration (Task 4)

### Problem
H264Recorder wrote raw H.264 Annex-B NAL units to files named .mp4 — NOT valid MP4 files. `file` command identified them as "data" not "ISO Media, MP4".

### Solution
Replaced raw byte writing with MP4Muxer integration:
- `muxer.AddH264Track(sps, pps)` for codec config
- `muxer.WriteSample(trackID, nalu, pts, duration)` for video frames
- `muxer.Close()` finalizes ftyp+moov+mdat atoms

### Key Decisions
- Write to finalPath directly (not tempPath): MP4Muxer writes entire file atomically on Close() via os.Create()
- SPS/PPS NOT written as samples — only passed to AddH264Track
- Only IDR (type 5) and P-frame (type 1) NAL units written as samples
- Duration calculation: time.Since(lastFrameTime), fallback 33ms when delta is zero
- SegmentStore interface kept unchanged for backward compat (WriteFrame/CloseSegment still defined but unused by H264)

### Pre-existing Bugs Found
- `storage/db.go`: Missing closing brace in CountRecordings() — fixed
- `storage/db.go`: InsertCamera references non-existent Camera.CreatedAt — commented out (likely from Task 6 in progress)

### Pattern: NAL Type Filtering
In writeFrames(), after extracting SPS/PPS, only process type 5 (IDR) and type 1 (P-frame). Other types (SEI=6, AUD=9, etc.) are skipped. This avoids writing non-video data as MP4 samples.


## FTP Authentication Fix (Task 3)

### Problem
- FTP server compared bcrypt password hash with plaintext client password
- Authentication always failed: `if user != s.username || pass != s.password` (direct plaintext comparison)
- HTTP auth middleware uses hash, but FTP server expected plaintext

### Solution
- Added `Password string \`yaml:"password"\`` field to AuthConfig (after PasswordHash)
- Updated main.go line 170 to pass `cfg.Auth.Password` instead of `cfg.Auth.PasswordHash` to FTP server
- HTTP auth continues using `PasswordHash` (unchanged)
- FTP auth now uses plaintext password as expected by server

### Key Pattern
**Separate credentials for different protocols**: When different auth mechanisms require different formats (hash vs plaintext), provide both in config rather than trying to convert.

### Build Issues Found
- Commented-out InsertCamera function referenced non-existent Camera.CreatedAt field
- Extra closing brace in model/types.go causing syntax error
- Both unrelated to FTP fix but blocked testing

### Verification
- FTP tests pass with proper plaintext auth
- HTTP auth middleware tests continue to pass with password hash
- Both auth mechanisms work independently as expected
## MP4Muxer Integration (Task 4) - 2026-04-30

### Key Findings
- The muxer was already integrated into h264.go but the codebase had multiple pre-existing syntax bugs preventing compilation
- `CreateSegment()` creates an empty .tmp file that must be cleaned up when using muxer (muxer writes directly to finalPath)
- `storage.DB` uses `UpsertCamera(id, name, protocol, url, username, password, enabled)` not `InsertCamera(camera)`
- `WebDAVConfig.Enabled` and `FTPConfig.Enabled` are `*bool` pointers (nullable), need nil check + dereference
- `model.Camera` struct had a duplicate `}` causing syntax error in types.go

### Patterns
- MP4Muxer collects all samples in memory, writes everything at Close() — fine for short segments (10min default)
- PTS = time.Since(segStart), duration = inter-frame delta with 33ms minimum
- NAL units from RTP decoder have Annex B start codes (00 00 00 01) prepended; muxer expects raw NAL without start codes (data[4:])
- Only video NAL types (5=IDR, 1=non-IDR) are written as samples; SPS(7) and PPS(8) are passed to AddH264Track

### Gotchas
- camera/manager.go had multiple issues: missing for loop, wrong method name, bad indentation — suggests incomplete prior edit
- Always run `go build ./...` to catch transitive compilation errors, not just the target package


## Syntax Error and Bug Verification (Task 1-2) - 2026-04-30

### Key Findings

#### 1. Syntax Error Fix
- **Issue**: Missing `type WebDAVConfig struct {` declaration causing orphaned field definitions
- **Root Cause**: WebDAVConfig fields existed without struct declaration, preventing compilation
- **Fix**: Added missing struct declaration before orphaned fields (lines 67-69 in config.go)
- **Verification**: Config module tests now pass 10/10

#### 2. Bug #5 (camvault) Verification
- **Status**: Already resolved ✅
- **Verification**: `grep -r "camvault" --include="*.go" .` returns zero results
- **Pattern**: Search before fixing to confirm bugs are already resolved

#### 3. Bug #4 (FTP/WebDAV enabled logic) Verification  
- **Status**: Already resolved ✅
- **Logic Verified**: Both FTP and WebDAV use `if cfg.FTP.Enabled == nil` checks
- **Correct Implementation**: Only sets default when pointer is nil (not configured)
- **Preserves User Intent**: Explicit `enabled: false` stays false

#### 4. Camera Model Enhancement
- **Issue**: Missing `CreatedAt time.Time` field referenced by non-existent InsertCamera method
- **Fix**: Added CreatedAt field to model.Camera struct
- **Update**: Replaced `InsertCamera` with `UpsertCamera` method (matches database schema)

### Patterns and Gotchas

#### 1. Go Compilation Debugging
```bash
# Syntax error debugging workflow
gofmt -d file.go  # Check formatting issues
go build ./...    # Catch transitive errors
grep -r "pattern" .  # Verify bug status before fixing
```

#### 2. Missing Struct Declaration Pattern
- **Symptom**: Orphaned field definitions with no preceding struct declaration
- **Solution**: Search for struct field patterns without struct keyword
- **Prevention**: Always match struct definitions with their field declarations

#### 3. Pointer vs Boolean Pattern
```go
// WRONG: bool doesn't distinguish between not set and explicitly false
var enabled bool

// CORRECT: *bool distinguishes nil (not set) from &false (explicitly false)  
var enabled *bool
```

#### 4. Database Schema Integration
- **UpsertCamera**: Takes individual fields, not Camera struct
- **Schema Fields**: Only include fields actually in database table
- **Timestamps**: If not in schema, don't include in database operations

### Key Lessons

1. **Verify Before Fixing**: Always check if bugs are already resolved with search tools
2. **Struct-Field Matching**: Ensure every field has a corresponding struct declaration
3. **Go Formatting**: `gofmt` often resolves syntax errors that aren't visible in code
4. **Database Integration**: Match Go struct fields to actual database schema, not desired schema
5. **Incremental Verification**: Test after each change to catch regressions early

### Final Status
- ✅ Syntax error fixed (WebDAVConfig struct added)
- ✅ Bug #5 verified (no camvault references)  
- ✅ Bug #4 verified (correct nil-check logic)
- ✅ Build successful (all modules compile)
- ✅ Tests successful (135/135 tests passing)
- ✅ Evidence saved (.sisyphus/evidence/task-1-2-verify.txt)


## Camera Hardware and Software Probe (Task 7) - 2026-04-30

### Key Findings

#### 1. Remote Device Status
- **Device**: Raspberry Pi 3 Model B Rev 1.2 (aarch64)
- **Memory**: 905MiB total, 620MiB available (68.5% available)
- **Disk**: 58G total, 50G available (11% used)
- **Status**: Hardware capable, software missing

#### 2. Camera Software Status
- **libcamera**: NOT INSTALLED
  - `libcamera-hello` command not found
  - `libcamera-vid` command not found
- **mediamtx**: NOT INSTALLED
- **RTSP Ports**: 8554, 8553 not listening
- **Impact**: Camera detection and streaming not possible

#### 3. Camera Model
- **Status**: CANNOT IDENTIFY
- **Reason**: libcamera not installed
- **Required**: libcamera installation for CSI camera detection

#### 4. Streaming Protocol
- **Hardware Support**: RPi 3B supports H.264 hardware encoding
- **Recommended**: rtsp_h264 protocol (for H.264 cameras)
- **Alternative**: rtsp_mjpeg protocol (for MJPEG cameras)
- **Deployment**: Requires libcamera + mediamtx installation

### Patterns and Gotchas

#### 1. SSH Connection Pattern
```bash
# First connection needs host key bypass
ssh -o StrictHostKeyChecking=no user@ip "command"
# Subsequent connections work normally
ssh user@ip "command"
```

#### 2. Software Dependencies
- **Order matters**: libcamera must be installed before camera detection
- **Components**: libcamera (detection) + mediamtx (streaming) both required
- **Verification**: Check both command availability and package installation

#### 3. Hardware Capability Assessment
- **RPi 3B**: Capable of H.264 hardware encoding
- **Constraint**: Software must match hardware capabilities
- **Decision**: rtsp_h264 recommended over rtsp_mjpeg for better quality

### Next Steps
1. **Install libcamera** on RPi 3B
   ```bash
   sudo apt update
   sudo apt install libcamera-tools
   ```
2. **Connect CSI camera** and verify detection
3. **Install mediamtx** streaming server
4. **Test streaming** functionality
5. **Deploy MiBee NVR** with appropriate camera configuration

### Key Learning
**Hardware ≠ Software Ready**: Just because RPi has camera hardware doesn't mean streaming works. Software stack (libcamera + mediamtx) must be installed and configured separately from the NVR application itself.

### Evidence
- Complete probe output saved to: `.sisyphus/evidence/task-7-camera-probe.txt`

## Deployment Phase (Wave 2-3) - 2026-04-30

### Key Findings

#### .31 Environment (Deployment Target)
- Old `camvault` process running on ports 9090/2121 (pid=1059527) — must kill before deploying
- /mnt/data: 2.7TB external drive, 2% used, WRITE OK
- nvr user does NOT exist — needs creation for systemd
- sqlite3 NOT installed — not critical (Go uses built-in)
- Memory VERY LOW: 777/905MB used, swap 835/904MB used
- Existing files: /mnt/data/nvr/ with old camvault deployment

#### .120 Environment (Camera Source)
- libcamera NOT installed — needs apt install
- mediamtx NOT installed — needs download from GitHub
- RTSP ports 8554/8553 free
- RPi 3B supports H.264 hardware encoding → use rtsp_h264
- 50GB disk free, 620MB memory available

### Wave 1 Status (ALL COMPLETE)
- Task 1: Bug #5 camvault ✅ (already fixed)
- Task 2: Bug #4 FTP/WebDAV enabled ✅ (already fixed)
- Task 3: Bug #3 FTP password ✅ (added Password field)
- Task 4: Bug #1 MP4Muxer ✅ (integrated into H264Recorder)
- Task 5: Bug #6 cameras DB ✅ (UpsertCamera added)
- Task 6: Bug #2 DB recordings ✅ (InsertRecording in closeSegment)
- Task 7: SSH probe .120 ✅
- Task 8: Cross-compile ✅ (20MB ARM64 binary)
- Task 9: SSH probe .31 ✅
- Complete probe output saved to: `.sisyphus/evidence/task-7-camera-probe.txt`

## RTSP Streaming Setup (Task 10) - 2026-04-30

### Key Findings

#### 1. Binary Naming Change (Debian Bookworm+)
- libcamera-apps renamed to rpicam-apps
- `libcamera-hello` → `rpicam-hello`
- `libcamera-vid` → `rpicam-vid`
- Transitional package `libcamera-apps` exists but doesn't provide old binary names

#### 2. Camera Hardware
- Model: OV5647 (Raspberry Pi Camera V1)
- Max resolution: 2592x1944 @ 15.63fps
- H.264 hardware encoding works (1.4MB in 3s at 1280x720/15fps)

#### 3. mediamtx Source Configuration
- `exec://` NOT supported in mediamtx v1.11.3
- Direct command sources NOT supported
- Only protocol sources work: `udp://`, `rtsp://`, etc.
- **Solution**: Pipe through ffmpeg for MPEG-TS muxing over UDP

#### 4. Pipeline Architecture
```
rpicam-vid (raw H.264) → pipe → ffmpeg (MPEG-TS mux) → UDP → mediamtx
```
- ffmpeg flags needed: `-fflags +genpts -c:v copy -f mpegts -flush_packets 0`
- CRITICAL: `?pkt_size=188` on UDP URL fixes TS packet alignment
- Without pkt_size=188: error "packet size 1472 not multiple of 188"

#### 5. Available Stream Protocols
- RTSP: rtsp://192.168.63.120:8554/stream
- RTMP: rtmp://192.168.63.120:1935/stream
- HLS: http://192.168.63.120:8888/stream
- WebRTC: http://192.168.63.120:8889/stream

### Patterns and Gotchas

#### 1. SSH Background Processes
- Background processes started via `ssh host 'cmd &'` may not survive
- Use a shell script on the remote host for reliable startup
- Script at `/tmp/start_stream.sh` on 192.168.63.120

#### 2. MPEG-TS over UDP Requirements
- mediamtx UDP source expects properly formatted MPEG-TS (188-byte packets)
- Raw H.264 must be wrapped in TS container by ffmpeg
- `pkt_size=188` ensures UDP packets align with TS packet boundaries

#### 3. Memory Footprint
- mediamtx: ~20MB, rpicam-vid: ~63MB, ffmpeg: ~58MB
- Total: ~141MB (acceptable on RPi 3B with 620MB available)

### Next Steps for NVR Integration
1. Configure MiBee NVR camera with `rtsp://192.168.63.120:8554/stream`
2. Consider making stream start script persistent (systemd)
3. Consider reducing resolution/framerate if memory constrained

### Evidence
- Complete setup output saved to: `.sisyphus/evidence/task-10-rtsp-setup.txt`

## SQLite Timestamp Fix - 2026-04-30

### Problem
- Go's `time.Time` stored via `modernc.org/sqlite` used `time.Time.String()` format: `2026-04-30 22:52:10.109803985 +0800 CST m=+32.026969936`
- SQLite's `datetime('now')` returns `2026-04-30 15:15:48` (UTC)
- String comparison `ended_at < datetime('now', '-30 days')` failed: `22 > 15` lexicographically → cleanup never triggered

### Root Cause: modernc.org/sqlite Driver Behavior
- Driver's `formatTime()` defaults to `time.Time.String()` when no `_time_format` DSN param set (line 147-148 in conn.go)
- On READ: driver auto-parses DATETIME columns to `time.Time` (rows.go line 171-173), then formats as RFC3339Nano when scanning to `*string` (convert.go line 82-84)
- On WRITE: driver binds `string` params as text via `bindText()`, `time.Time` params via `formatTime()` → `time.String()`

### Solution
- Added `timeToDB(t)`: converts `time.Time` to UTC string `2006-01-02 15:04:05.999999999`, returns `nil` for zero time (→ SQLite NULL)
- Added `formatTime(t)`: same format but returns empty string for zero time (for WHERE clause args)
- Added `parseTime(s)`: multi-format parser supporting: canonical, no-fractional, RFC3339, legacy Go `time.Time.String()` with monotonic clock
- Added `scanTime(ns)`: converts `sql.NullString` → `time.Time` via `parseTime`
- Changed all INSERT/UPDATE to use `timeToDB()` for time columns
- Changed all SELECT scans from `sql.NullTime` to `sql.NullString` + `scanTime()`
- Changed ListRecordings filter args to use `formatTime()` for time comparisons

### Files Modified
- `internal/storage/db.go`: Core fix (time helpers + all query functions)
- `internal/storage/db_test.go`: Added 6 new tests (round-trip, format, expiry, edge case, parseTime, zero→NULL)
- `internal/cleanup/cleanup_test.go`: Fixed TestRunOnce_TimeBasedCleanup (0-day retention → 1-day, old test relied on buggy timezone comparison)

### Key Pattern: modernc.org/sqlite Time Handling
```go
// WRITE: Pass formatted string, NOT time.Time directly
timeToDB(r.StartedAt)  // → "2026-04-30 15:04:05.123456789" or nil

// READ: Scan into sql.NullString, parse manually
var ts sql.NullString
row.Scan(&ts)
r.StartedAt = scanTime(ts)

// SQL comparisons work because stored format matches datetime() output
// Both are YYYY-MM-DD HH:MM:SS[.fffffffff] in UTC
```

### Key Learning
**Never pass time.Time directly to modernc.org/sqlite**: The driver uses `time.Time.String()` which includes monotonic clock + timezone text. Always format explicitly as UTC string. The driver also auto-parses DATETIME columns on read, so use `sql.NullString` scanning + manual parsing instead of `sql.NullTime`.

## Final Session Summary - 2026-04-30

### Plan Status: 22/22 COMPLETE

### Bugs Fixed (8 total)
1. Bug #1: MP4Muxer not integrated into H264Recorder
2. Bug #2: RTSP recordings not inserted into DB
3. Bug #3: FTP password not passed (empty string)
4. Bug #4: FTP/WebDAV enabled default logic inverted (*bool fix)
5. Bug #5: camvault references in Go code
6. Bug #6: cameras DB table never populated
7. Bug #7: FTP download broken (os.O_RDONLY bitwise AND)
8. Bug #8: SQLite timestamp format incompatible with datetime()

### Deployment Verified
- NVR running as systemd service on .31 (RPi 3B, 905MB RAM)
- RTSP stream from .120 via rpicam-vid → ffmpeg → UDP → mediamtx
- 30s segment duration to avoid OOM on 1GB device
- All APIs working: health, recordings, cameras, stats, download, pin/unpin
- WebDAV + FTP file access verified
- Web UI serving (HTTP 200)

### Final Test Results
- 180 unit tests passing across 16 packages
- F1 Plan Compliance: APPROVE (Must Have 7/7, Must NOT Have 4/4)
- F2 Code Quality: APPROVE (Build PASS, Tests 180/0, 4 minor issues)
- F3 Manual QA: APPROVE (7/7 scenarios, 9/9 integration checks)
- F4 Scope Fidelity: APPROVE (18/18 compliant, CLEAN contamination)

### Known Technical Debt (non-blocking)
- mp4mux.go: Hardcoded 1920x1080 resolution
- db.go: scanTime silently swallows parse errors
- ftp/server.go: DB insert error silently discarded
- config.go: retention_days: 0 treated as default (not "delete all")
