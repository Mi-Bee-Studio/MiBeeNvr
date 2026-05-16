# Per-Camera Retention Days Implementation

## Changes Made

### 1. DB Migration v2→v3
- Added migration after existing v1→v2 migration in `internal/storage/db.go` (around line 114)
- Follows exact pattern: check version → ALTER TABLE → update version
- Uses `retention_days INTEGER DEFAULT 0` - 0 means "use global default"

### 2. Database Schema Changes
- Added `RetentionDays int` field to `CameraRow` struct
- Updated `ListCameras()` to include `retention_days` in SELECT and Scan
- Updated `GetCamera()` to include `retention_days` in SELECT and Scan  
- Updated `UpdateCameraMetadata()` signature to add `retentionDays int` parameter
- Added new `ListExpiredRecordingsByCamera()` method for per-camera cleanup

### 3. Application Logic Changes
- Added `RetentionDays *int` to `CameraUpdate` struct in manager
- Added `intPtrOrZero()` helper function for pointer handling
- Updated all callers of `UpdateCameraMetadata()` to pass retentionDays parameter:
  - API handler: passes `0` for new cameras (use global default)
  - Camera manager: passes actual value from updates or 0

### 4. Verification
- `go vet ./...` passes with no errors
- All call sites updated consistently
- Maintains backward compatibility (default 0 = use global default)

## Key Decisions
- Did NOT add to CameraConfig YAML (as specified - DB-only metadata)
- Used `0` as default to indicate "use global retention"
- Added helper function for clean pointer handling
- Followed existing migration pattern exactly

## Files Modified
- `internal/storage/db.go` - Migration, schema changes, new method
- `internal/camera/manager.go` - CameraUpdate struct, helper function, call updates  
- `internal/api/handler.go` - API call update

## Potential Future Work
- Add retentionDays to API request/response bodies
- Add UI support for per-camera retention settings
- Consider validation for negative retention values
## Task: Per-camera retention in cleanup

- `timeBasedCleanup()` now iterates cameras via `ListCameras()`, using `ListExpiredRecordingsByCamera()` per camera
- Per-camera `retention_days=0` → falls back to global `cm.retention`
- Both 0 → skip (no cleanup for that camera)
- Tests must insert camera rows via `UpsertCamera()` since `ListCameras()` now drives cleanup
- `UpsertCamera()` doesn't set `retention_days`, so new cameras default to 0 (use global)
- SQL boundary: `ended_at < datetime('now', '-N days')` is strict less-than, so exactly N days old is NOT expired. Use N+1 for test data.

## gohlslib v2 API Research

- Import: `github.com/bluenviron/gohlslib/v2` and `github.com/bluenviron/gohlslib/v2/pkg/codecs`
- Muxer struct fields: Tracks, Variant, SegmentCount, SegmentMinDuration, PartMinDuration, SegmentMaxSize, Directory
- Track: `{ Codec: &codecs.H264{SPS: []byte{}, PPS: []byte{}}, ClockRate: 90000 }`
- WriteH264(track, ntp time.Time, pts int64, au [][]byte) error
- Handle(w, r) serves playlist + segments, can be used directly as http.Handler
- Start() error / Close()
- Variants: MuxerVariantMPEGTS, MuxerVariantFMP4, MuxerVariantLowLatency
- Directory field enables disk-based segment storage (important for RPi 3B RAM)
- Default SegmentCount=7, SegmentMinDuration=1s, SegmentMaxSize=50MB
- For RPi 3B: use Directory for disk storage, SegmentCount=3-4, SegmentMinDuration=2s

## HLS Live Streaming Implementation

### Changes Made
- Created `internal/hls/manager.go` — HLS Manager with on-demand stream lifecycle
- Created `internal/hls/errors.go` — Sentinel errors for HLS package
- Modified `internal/recorder/h264.go` — Added `OnHLSFrame` callback + `SPS()`/`PPS()` getters
- Modified `internal/camera/manager.go` — Added `GetRecorder()` method to expose recorder
- Modified `internal/api/handler.go` — Added HLS stream endpoints, `hlsMgr` field
- Modified `cmd/mibee-nvr/main.go` — Wired HLS Manager, added shutdown cleanup
- Modified `internal/api/handler_test.go` — Updated 3 test helper calls for new NewHandler signature (nil hlsMgr)

### gohlslib v2 API (verified from source)
- `Muxer.Start() error` / `Muxer.Close()` — lifecycle
- `Muxer.WriteH264(track *Track, ntp time.Time, pts int64, au [][]byte) error` — au is raw NAL units without start bytes
- `Muxer.Handle(w, r)` — serves .m3u8 + .ts segments (NOT an http.Handler interface, must wrap)
- `Track{Codec: &codecs.H264{SPS, PPS}, ClockRate: 90000}`
- `MuxerVariantMPEGTS` = iota + 1

### Key Design Decisions
- Used `MuxerVariantMPEGTS` for broader browser compatibility
- `Directory` field for disk-based segments (RPi 3B RAM constraint)
- `SegmentCount: 3`, `SegmentMinDuration: 2s` for RPi 3B
- Idle timeout 60s with watchdog goroutine per stream
- `maxStreams: 2` for RPi 3B memory budget
- `OnHLSFrame` callback in RTP receive path — must NOT block (WriteH264 is non-blocking internally)
- `Manager.Handle()` public method wraps muxer.Handle() for clean API
- `StartStream` returns `error` (not http.Handler) since gohlslib.Muxer doesn't implement http.Handler

### Test Notes
- `TestListCameras_Empty` failure is PRE-EXISTING (verified with git stash)
- WebDAV test failures are PRE-EXISTING
- Only minimal test file changes needed (3 NewHandler calls updated with nil hlsMgr)


## Task 13: i18n Translation Updates

### Changes Made

- Removed unused quick-select keys from both en.json and zh.json:
  - `recordings.last1h`
  - `recordings.last24h`
  - `recordings.last7d`
  - `recordings.last30d`
  - `recordings.quickRange`

- Added new keys for stats filters, camera status, and live view:
  - Stats filter keys: `stats.filterCameras`, `stats.selectAll`, `stats.deselectAll`
  - Camera status keys: `cameras.live`, `cameras.lastSeen`, `cameras.active`, etc.
  - Live view keys: `live.title`, `live.notSupported`, `live.loading`, etc.

### Key Points

- Both files maintained exactly the same set of keys after updates
- `npm run build` succeeded with no compilation errors
- JSON structure preserved with proper commas and formatting
- No existing key values were modified, only additions and removals
- Build warnings are unrelated to i18n changes (existing codebase issues)

### Files Modified

- `web/src/lib/i18n/en.json` - Added new keys, removed unused keys
- `web/src/lib/i18n/zh.json` - Added Chinese translations, removed unused keys
