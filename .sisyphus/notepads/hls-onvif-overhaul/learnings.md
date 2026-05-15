## 2026-05-11 Session Start
- Plan: hls-onvif-overhaul
- Session: ses_1eaff8215ffeOe76vwvw8s1C9q
- User note: 完成后要部署测试

## T3: ONVIF Testable Interfaces + Mock Types
- PTZ methods on `Client` take `profileToken string` but PTZController interface omits it (cleaner abstraction for testing)
- `ProbeDevice` doesn't exist in current `discovery.go` — included in Discoverer interface as forward-looking contract
- Mock call counters use `sync.Mutex` for thread safety
- MockPTZController tracks MoveHistory as append-only slice for verifying call sequences
- Test sentinel error uses custom `testError` type (not `errors.New`) to support `ErrorIs` matching

## T4: ONVIF DB Schema Migration

- Current schema version was 5 (merge config columns). New migration v5→v6 adds `onvif_endpoint` and `profile_token`.
- Migration pattern: `pragma_table_info` check → `ALTER TABLE ADD COLUMN` → bump version. Matches existing v5 pattern exactly.
- `UpsertCamera` signature extended with `onvifEndpoint, profileToken string` params. All callers across 7 files updated via ast_grep_replace.
- SELECT queries in `ListCameras` and `GetCamera` append new columns at end. Scan order must match SELECT order exactly.
- `CameraRow` gets `ONVIFEndpoint string` and `ProfileToken string` fields with json tags.
- Backward compatible: `TEXT DEFAULT ''` ensures empty strings for existing rows.
- 4 new tests: UpsertCamera_OnvifFields, OnvifFieldsEmptyDefaults, OnvifUpdateExisting, MigrationV5ToV6_OnvifColumns.
- All 67 storage tests pass. Full project builds clean.

## HLS Buffer Config + Eviction Fix (2026-05-11)

### Key Changes
- `writeBufSize` reduced from 120 to 40 (~2s at 20fps instead of ~6s)
- `SegmentMaxSize` reduced from 50MB to 10MB
- Added `NewManagerWithOpts()` for configurable buffer/segment sizes
- Added `HLSConfig` struct to config.go with `write_buffer_size` and `segment_max_size_mb`
- `startStream()` now returns `ErrMaxStreamsReached` instead of silently evicting oldest stream
- Added `EvictStream(cameraID)` and `GetActiveStreamCount()` public methods
- `stopStreamLocked()` now nil-checks `entry.mux` before `.Close()`

### Patterns
- gohlslib `Muxer.SegmentMaxSize` is `uint64`, not `int` — need explicit cast
- `NewManager` preserved as zero-arg convenience; `NewManagerWithOpts` for customization
- Config defaults use `<= 0` check (zero value = unconfigured)
- Test stream entries without real muxers need nil mux guard in `stopStreamLocked`

### Existing Handler Integration
- `api/handler.go` lines 1226,1261 already check `err == hls.ErrMaxStreamsReached`
- Now that error is actually returned, the handler will properly respond with error

## T5: Frontend Shared HLS.js Config Module

### Key Changes
- Created `web/src/lib/hls-config.ts` with `createHlsConfig()` factory function
- RPi-optimized: maxBufferLength:5, maxMaxBufferLength:10, maxBufferSize:10MB, backBufferLength:2, enableWorker:false
- Auth via `xhrSetup` with same pattern as existing Dashboard.svelte (getCredentials + btoa)
- Dashboard.svelte: replaced inline config with `createHlsConfig()` call
- LiveView.svelte: replaced inline config, removed unused `getCredentials` import
- `hls.js` default import pattern: `import Hls from 'hls.js'` (dynamic), type via `import type Hls from 'hls.js'`

### Patterns
- Dashboard.svelte still uses `getCredentials` directly in `fetchSnapshot` — kept import
- LiveView.svelte no longer uses `getCredentials` directly — cleaned up import
- Config uses `Partial<Hls.Config>` return type for type safety

## T6: ONVIF Library Validation + Import Setup

### Key Changes
- Created `internal/onvif/onvifgo.go` with `MapDiscoveredDevice` and `MapDiscoveredDevices`
- Maps onvif-go `discovery.Device` fields: EndpointRef→UUID, GetName()→Name, XAddrs, Scopes, hardware scope→Hardware, GetDeviceEndpoint()→Endpoint
- Created `internal/onvif/onvifgo_test.go` with 5 test cases (3 subtests for single device, 2 for slice mapping)
- `go mod tidy` moved onvif-go from indirect to direct dependency
- CGO_ENABLED=0 compilation verified — onvif-go is pure Go (WS-Discovery via UDP multicast)
- `go test ./internal/onvif/... -v` — all 34 tests pass (28 existing + 6 new)

### onvif-go Discovery API
- `discovery.Device` struct: EndpointRef, XAddrs ([]string), Types ([]string), Scopes ([]string), MetadataVersion (int)
- Methods: `GetDeviceEndpoint()` (first XAddr), `GetName()` (name from scopes), `GetLocation()` (location from scopes)
- `discovery.Discover(ctx, timeout)` returns `([]*Device, error)` — pure Go, no CGO
- Scope parsing: scopes contain `/name/`, `/location/`, `/hardware/` suffixes

## T7: HLS Multi-Stream Backend Support + Tests

### Key Changes
- Added `GetStreamStatus(cameraID string) (active bool)` method to Manager — RLock-protected boolean check
- Handler already checks `err == hls.ErrMaxStreamsReached` at lines 1226, 1261 — returns 503 ServiceUnavailable
- Added 7 new HLS manager tests (concurrent start/stop, GetStreamStatus)
- Added 4 new API handler tests (nil HLS manager, stop stream active/not-active)

### Patterns
- Concurrent stream start/stop tests use real gohlslib Muxer (needs valid SPS/PPS: Baseline 16x16)
- `TestConcurrentStartStreams_AtCapacity_NoDeadlock` — pre-fills with nil-mux entries, 10 goroutines race for overflow slot
- Handler HLS tests need a real `hls.Manager` injected via `NewHandler()` — can't use `TestHandler()` which passes nil
- `handleStopHLSStream` returns `{"status": "not active"}` for non-existent streams (no error), `{"status": "stopped"}` for active ones
- All 150 tests pass across hls + api packages

## T8: Frontend Dashboard 4-HLS Grid Layout

### Key Changes
- `getCameraMode()` simplified: returns `'hls'` for ALL H264/H265/ONVIF cameras (not just expanded)
- Removed `'hls-expanded'` and `'hls-fallback'` modes — now just `'hls'`, `'snapshot'`, `'unsupported'`
- `isHlsSupported()` extended to include `onvif` protocol
- `shrinkToSnapshot()` renamed to `shrinkToGrid()` — no longer destroys HLS player on collapse (HLS stays active in all grid cells)
- `$effect` now inits HLS for all HLS-capable cameras simultaneously
- Removed "click for live" hover overlay — cameras are already live in grid
- Removed dead PTZ toggle code from `handleCellClick` (ONVIF now always expands)

### Patterns
- Snapshot mode now only applies to `http_jpeg` cameras (and as transient fallback)
- Expand/collapse is purely visual (CSS grid col-span/row-span) — HLS players stay alive
- `expandedCameraId` controls grid layout, not player lifecycle
- Build produces same chunk sizes — HLS is already code-split via dynamic import

## T9: ONVIF WS-Discovery Implementation + Tests

### Key Changes
- Rewrote `internal/onvif/discovery.go`: `Discover()` calls `discovery.Discover(ctx, timeout)` from onvif-go, maps via `MapDiscoveredDevices()`, returns empty slice (not error) on failure/no devices
- Added `ProbeDevice()` — direct HTTP POST to `http://{host}:{port}/onvif/device_service` with WS-Discovery SOAP probe, parses ProbeMatches response
- 12 new tests in `discovery_test.go`: Discover (3), ProbeDevice (9) — using httptest.Server for deterministic testing

### onvif-go Discovery API
- `discovery.Discover(ctx, timeout)` — UDP multicast only (239.255.255.250:3702), returns `([]*Device, error)`
- NO direct HTTP probe function in the library — had to implement manually
- `ErrNoProbeMatches` returned when no devices respond

### ProbeDevice Implementation
- SOAP XML envelope with WS-Discovery Probe action, NetworkVideoTransmitter type filter
- UUID v4 message ID generated via crypto/rand
- Response parsing: Go XML ignores namespace prefixes by default — struct tags use local names only
- 1MB response body limit via `io.LimitReader`
- Non-200 status → nil device (not error) — device may not be ONVIF
- Invalid/unparseable XML → nil device (not error)

### Patterns
- `discovery.Discover()` errors (including `ErrNoProbeMatches`) → return `[]DiscoveredDevice{}`, nil (not error)
- `ProbeDevice()` non-ONVIF responses → return nil, nil (not error)
- `ProbeDevice()` connection failures → return nil, error (caller can distinguish)
- `probeMatchEntry` struct uses local-name XML tags (no namespace URIs) — Go XML parser matches `<d:ProbeMatch>` to `xml:"ProbeMatch"`

### Test Strategy
- ProbeDevice tests use `httptest.NewServer` for deterministic SOAP responses
- `testServerAddr()` helper extracts host:port from test server for ProbeDevice calls
- Existing `mocks_test.go` already has MockDiscoverer tests — avoid duplication

## T11: ONVIF PTZ Operations + Tests

### Key Changes
- Created `PTZControllerImpl` struct in `ptz.go` — wraps `*onvifgo.Client` with `profileToken` stored internally
- Implements `PTZController` interface: ContinuousMove, AbsoluteMove, RelativeMove, Stop, GetStatus
- All methods serialized by `sync.Mutex` for concurrent safety
- Type conversion helpers: `toOnvifPTZVector`, `toOnvifPTZSpeed`, `fromOnvifPTZVector`, `fromOnvifPTZStatus`
- Kept Client PTZ stub methods (PTZContinuousMove etc.) unchanged — T13 will wire them
- 12 new PTZ tests: 5 success ops, 2 GetStatus (idle+moving), 1 concurrent, 1 error, 1 interface check, 1 SetProfileToken, 6 type conversion

### onvif-go PTZ API
- Methods on `*onvifgo.Client`: `ContinuousMove`, `AbsoluteMove`, `RelativeMove`, `Stop`, `GetStatus`
- All take `profileToken string` param — our PTZController interface omits it (stored internally)
- `PTZVector` has nested `PanTilt *Vector2D` and `Zoom *Vector1D` — need conversion to/from flat Pan/Tilt/Zoom
- `PTZStatus` has `Position *PTZVector`, `MoveStatus *PTZMoveStatus` (PanTilt/Zoom strings: IDLE/MOVING/UNKNOWN)

### Testing Patterns
- `setPTZEndpoint` uses `unsafe.Pointer` to set unexported `ptzEndpoint` field on `onvifgo.Client`
  - `reflect` can't set fields in structs containing `sync.RWMutex` (canSet=false), unsafe bypasses this
- `extractSOAPAction` uses `xml:",any"` to capture the first child element of `<s:Body>`
- onvif-go SOAP client only checks HTTP status for errors, NOT SOAP Fault elements in body
  - For error tests, must return HTTP 500 (not SOAP Fault with 200)
- SOAP mock responses need proper namespace: `xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl"`

### Parallel Task Safety
- Created standalone `PTZControllerImpl` to avoid modifying `client.go` (T10 running in parallel)
- No changes to `interfaces.go`, `types.go`, `mocks.go`
- Fixed stray duplicate `}` in `client_test.go` that T10 left (was breaking build)

## T10: ONVIF Client Operations

### onvif-go library API patterns
- `onvifgo.NewClient(endpoint, onvifgo.WithCredentials(user, pass))` returns `(*onvifgo.Client, error)`
- `Initialize(ctx)` calls `GetCapabilities` internally to discover service endpoints (media, PTZ, etc.)
- `GetCapabilities` response: PTZ support = `caps.PTZ != nil`, Streaming = `caps.Media != nil`
- `GetProfiles` returns `[]*Profile` with `VideoEncoderConfiguration.Resolution.Width/Height`
- `GetStreamURI` returns `*MediaURI` with `URI` field
- `GetDeviceInformation` returns `*DeviceInformation` with `FirmwareVersion` (not `Firmware`)
- Import alias `onvifgo` needed since our package is also `package onvif`

### Mock server pattern for onvif-go tests
- onvif-go sends SOAP XML requests — mock server needs to parse SOAP body to determine action
- Use `xml:",any"` struct tag to extract inner SOAP action element from Body
- `clientExtractSOAPAction` uses `xml.Unmarshal` with nested struct pattern
- Must handle GetCapabilities during Initialize (called by Connect)

### Pre-existing issues discovered
- `ptz_test.go` had `newConnectedClient()` that called `Connect()` without mock server — updated to use httptest mock
- `extractSOAPAction` in ptz_test.go uses `(t *testing.T, body []byte)` signature — different from client_test.go version
- `webdav/server_test.go` has 2 pre-existing PATCH method failures (403 vs 405) — unrelated to onvif changes

### Client field access
- PTZ methods already existed on `*Client` in `ptz.go` (from T9 or T11) — no need to add stubs
- `Client.client` field holds `*onvifgo.Client` — private, used internally for method delegation

## T12: ONVIFRecorder Implementation
- ONVIFRecorder wraps H264Recorder/H265Recorder — does NOT implement its own RTSP pipeline
- Key pattern: `newRecorder` function field on the struct, overridable in tests to inject mock recorder
- This avoids needing a real RTSP server for unit tests while testing the ONVIF resolution flow
- `errorStreamURIClient` wrapper needed its own counter field because `MockDeviceClient.mu` is unexported
- `newTestONVIFRecorder` accepts `onvif.DeviceClient` interface (not concrete type) to support wrappers
- detectEncoding() prefers H264 > H265 > first profile's encoding > "H264" default
- Full suite: 509 passed, 2 failed (pre-existing webdav PATCH/405 vs 403 issue)

## T13: ONVIF Camera Manager Integration + API Handlers + Tests

### Key Changes
- `internal/onvif/client.go`: Added `NewPTZController(profileToken)` method — mutex-protected, returns nil if not connected
- `internal/camera/manager.go`: ONVIF case in `createRecorder()` now creates `ONVIFRecorder` with ONVIFEndpoint fallback to URL
- `internal/camera/manager.go`: Added `GetONVIFPTZController()` — validates camera is ONVIF, connects, gets profiles, creates PTZController
- `internal/api/handler.go`: `handleONVIFDeviceDetail` now connects to real ONVIF device, returns device_info + profiles
- `internal/api/handler.go`: `handlePTZMove` dispatches continuous/absolute/relative via PTZController
- `internal/api/handler.go`: `handlePTZStop` calls `ptz.Stop(true, true)`
- `internal/api/handler.go`: `handlePTZStatus` calls `ptz.GetStatus()`, returns pan/tilt/zoom/moving
- `internal/api/handler.go`: Added nil guard for `h.db` in `requireONVIF` to prevent panics in tests

### Patterns
- `GetCameraConfig()` is the thread-safe way to look up camera config from CameraManager (uses RLock)
- ONVIF endpoint resolution: `cam.ONVIFEndpoint` with fallback to `cam.URL` — same pattern in createRecorder and GetONVIFPTZController
- `NewPTZController` must be called after `Connect()` — method checks `c.client != nil`
- Pre-existing `ptz_test.go` had tests expecting stub 200 responses — updated to expect 500 (nil camMgr) now that real logic runs
- chi `{ip}` route parameter requires at least 1 char — `/api/onvif/discover/` returns 404, not matched

### Test Results
- 518 passed, 2 failed (pre-existing webdav PATCH/405 vs 403 — NOT our bug)
- New tests: TestCreateRecorder_ONVIF, TestCreateRecorder_ONVIF_WithEndpoint, TestGetONVIFPTZController_NotFound, TestGetONVIFPTZController_NotONVIF
- Updated tests: TestONVIFDeviceDetailEndpoint (now 502), TestPTZMoveEndpoint/Stop/Status (now 500 with nil camMgr)

## T15: HLS Frontend Error Recovery + Graceful Degradation

### Key Changes
- Created `web/src/lib/hls-errors.ts` — shared error handling module with `setupHlsErrorHandling()` and `checkStreamAvailable()`
- Dashboard.svelte: replaced inline error handler with `setupHlsErrorHandling()`, added `streamStates` per-camera tracking
- LiveView.svelte: same pattern, single-camera `streamState` variable
- Both components show colored dot indicators: green (playing), yellow pulsing (buffering), red (error), gray (snapshot)
- HTTP 429 pre-check via `checkStreamAvailable()` HEAD request before HLS init
- Network errors: auto-retry 3x with exponential backoff (2s/4s/8s)
- Media errors: `hls.recoverMediaError()` with 3 retries
- Fatal errors after retries: snapshot fallback (Dashboard) or error message with Retry button (LiveView)

### Patterns
- `setupHlsErrorHandling(hls, Hls, config)` takes Hls constructor as 2nd param to avoid static import of hls.js (keeps code splitting working)
- `hls-errors.ts` uses `any` types for hls parameter since it avoids static hls.js import entirely
- Dashboard `$effect` skips re-init for cameras in 'snapshot' state (error recovery put them there)
- `checkStreamAvailable()` uses HEAD request with auth headers — returns true on network failure (optimistic)
- Retry count resets to 0 on successful FRAG_LOADED and MANIFEST_PARSED events

## T16: HLS + ONVIF End-to-End Integration Tests

### Patterns Discovered
- `api.TestHandler()` creates handler with all managers nil (camMgr, hlsMgr, mergeMgr, config)
- `api.NewHandler()` takes all deps explicitly — use for tests that need specific managers
- HLS handler checks `h.hlsMgr == nil || h.camMgr == nil` BEFORE protocol check — returns 500 not 400
- ONVIF discovery calls `onvif.Discover()` directly (no DI) — can't mock, test tolerates both 200/500
- `camera.createRecorder()` is unexported — integration tests (external package) must create recorders directly
- `recorder.NewONVIFRecorder()` accepts `onvif.DeviceClient` interface — can pass real client (won't connect until Start)

### Test Design Decisions
- TestMultiStreamHLS: Tests HLS endpoint behavior without running streams (no RTSP available in CI)
- TestONVIFDiscoveryWithMock: Tolerates both 200 (empty list) and 500 (no network) since Discovery is hardcoded
- TestONVIFCameraCreation: Creates ONVIFRecorder directly since createRecorder is unexported
- TestPTZLifecycle: Tests mock PTZ controller directly + API endpoint error paths (no camMgr)
- TestHLSWithONVIFCamera: Tests both nil camMgr and present-but-no-recorder scenarios

### Pre-existing Failures
- webdav: TestWriteMethodsForbidden/PATCH — 403 vs 405 (NOT our bug)
