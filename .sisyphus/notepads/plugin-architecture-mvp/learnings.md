# Learnings — Plugin Architecture MVP

## 2026-05-15: gRPC Spike Validation

### CGO_ENABLED=0 Compatibility
- **HashiCorp go-plugin works perfectly with CGO_ENABLED=0**. All deps are pure Go.
- **Raw gRPC also works** with CGO_ENABLED=0. Both compile to static binaries.
- ARM64 cross-compilation (`GOOS=linux GOARCH=arm64 CGO_ENABLED=0`) works for both approaches.

### HashiCorp go-plugin specifics
- Uses `hclog.Logger` interface, not `slog.Logger`. Use `hclog.Default()` for plugin framework, `slog` for app code.
- Auto-restarts plugin on crash. Next gRPC call succeeds after restart. PID changes.
- Must embed `plugin.NetRPCUnsupportedPlugin` for gRPC-only plugins.
- `plugin.DefaultGRPCServer` is the standard server factory.
- Communication over Unix Domain Sockets on Linux (TCP on Windows).
- `plugin.Serve()` blocks — it's the plugin's `main()` entry point.

### Binary Size Impact
- gRPC runtime adds ~10-13 MB per binary (stripped).
- HashiCorp go-plugin framework adds ~1 MB on top of raw gRPC.
- Current NVR is 22 MB stripped; with gRPC plugin support: ~32-35 MB.
- Acceptable for RPi 3B (1 GB RAM, binary loaded from SD card).

### Decision: Use HashiCorp go-plugin
- Batteries included: lifecycle, crash detection, auto-restart, log forwarding, version negotiation.
- ~1 MB overhead vs raw gRPC is negligible.
- Battle-tested (Terraform, Vault, Consul).
- Saves significant code complexity vs rolling our own process management.

### Key Dependencies Added
```
google.golang.org/grpc v1.81.1
google.golang.org/protobuf v1.36.11 (already indirect)
github.com/hashicorp/go-plugin v1.8.0
github.com/hashicorp/go-hclog v1.6.3 (indirect)
github.com/hashicorp/yamux v0.1.2 (indirect)
github.com/oklog/run v1.1.0 (indirect)
github.com/jhump/protoreflect v1.17.0 (indirect)
```

## 2026-05-15: PluginsConfig Schema Added

### Changes to `internal/config/config.go`
- Added `PluginsConfig` struct with `directory` and `plugins` map fields
- Added `PluginEntryConfig` struct with `enabled`, `path`, and `config` map fields
- Added `Plugins PluginsConfig` field to root `Config` struct with `yaml:"plugins"` tag
- Default directory is `"./plugins"` (set in `applyDefaults()`)
- Plugins map defaults to empty non-nil map (avoids nil panics)

### Backward Compatibility
- When `Load()` detects non-empty `XiaomiConfig.Token` or `XiaomiConfig.UserID`, it auto-generates a `plugins.xiaomi` entry
- The auto-generated entry copies `user_id`, `token`, `region` from `XiaomiConfig` into the plugin's `Config` map
- Explicit `plugins.xiaomi` in YAML takes precedence over auto-generation
- Empty Xiaomi config (zero values) does NOT trigger auto-generation

### Tests Added (7 new, all pass)
1. `TestPluginsDefaults` — defaults applied correctly
2. `TestPluginsConfigSection` — parsing new `plugins:` YAML section
3. `TestPluginsBackwardCompatXiaomi` — old `xiaomi:` section auto-generates plugin entry
4. `TestPluginsBackwardCompatNoOverride` — explicit `plugins.xiaomi` wins over auto-gen
5. `TestPluginsNoBackwardCompatWhenXiaomiEmpty` — no auto-gen for empty xiaomi config
6. `TestPluginsSaveAndLoad` — round-trip Save/Load preserves plugins config
7. `writeTempYAML` helper for inline YAML test data

### Key Decisions
- Backward compat logic placed in `Load()` after `applyDefaults()`, not in `applyDefaults()` itself
- Only `Token` and `UserID` trigger auto-generation (not `Region` which has a default value "cn")
- Plugin config map keys are lowercase (`user_id`, `token`, `region`) matching YAML conventions
- No new dependencies added
- `XiaomiConfig` struct preserved unchanged (deprecated but still supported)

## 2026-05-15: Makefile Dual Binary Builds

### Targets Added
- `make proto` — Placeholder for proto generation
- `make plugin-xiaomi` — Builds Xiaomi plugin binary (native arch, graceful skip when source missing)
- `make plugins` — Meta-target depending on `plugin-xiaomi`
- `make plugin-xiaomi-arm64` — Cross-compiles Xiaomi plugin for ARM64
- `make plugins-cross` — Meta-target for cross-compiled plugins

### Existing Targets Updated
- `make build` — Now depends on `plugins` (gracefully skips if source missing)
- `make cross` — Now depends on `plugins-cross`
- `make deploy` — Now depends on `cross plugins-cross`, deploys plugin binary to RPi
- `make clean` — Removes plugin binaries

### Design Decisions
- **Graceful skip**: Plugin build targets check for source directory existence before running go build. Allows Makefile to exist before plugin main.go is created.
- **Binary naming**: `plugins/xiaomi/xiaomi-plugin` (native), `plugins/xiaomi/xiaomi-plugin-arm64` (cross) — follows main binary pattern
- **Deploy path**: `/mnt/data/nvr/plugins/xiaomi/xiaomi-plugin`
- **Dockerfiles NOT updated**: Plugin main.go doesn't exist yet, Docker builds would fail. Deferred.

### Verification
- `make test` — All tests passing
- `make lint` — go vet clean
- `make plugin-xiaomi` — Graceful skip: "Skipping xiaomi-plugin (cmd directory not found)"
- `make proto` — Placeholder message
- `make -n build` — Correct execution order: frontend → plugins → main build
- `make -n clean` — Plugin binaries included in cleanup
- `make -n deploy` — Plugin binary copy to RPi after main binary

## 2026-05-15: Proto SDK Definition (Final)

### Proto Setup
- protoc v34.1 available on dev machine; plugins installed via `go install` (protoc-gen-go v1.36.11, protoc-gen-go-grpc v1.6.2).
- Go plugins land in `~/go/bin/` — must add to PATH before running `go generate`.
- `go:generate` directive in `plugin/proto/gen.go` runs protoc from the proto directory.
- Generated code goes to `plugin/proto/gen/` (two files: `nvr.pb.go` for messages, `nvr_grpc.pb.go` for gRPC stubs).
- Generated package: `gen` (go_package resolves to `github.com/Mi-Bee-Studio/MiBeeNvr/plugin/proto/gen`).
- `plugin/proto/nvr.go` provides package doc re-exporting pattern (imports go to gen/ directly).

### Proto Design Decisions
- **Codec enum**: CODEC_UNSPECIFIED=0, H264=1, H265=2, MJPEG=3 — matches internal/model Format constants.
- **RecorderState enum**: IDLE/CONNECTING/RECORDING/ERROR/STOPPED — matches internal RecorderStatus strings.
- **Frame.is_codec_info** (field 5): Dedicated bool for SPS/PPS/VPS frames. Critical for muxer: these must be handled before track creation.
- **Frame.extra map<string,string>**: Extensible for codec params (SPS/PPS hex, NALU format) without schema churn.
- **Capabilities.auth** (field 5): Indicates plugin requires username/password — maps to PluginCapabilities.auth in spec.
- **RecorderConfig.name** (field 2): Human-readable camera name from CameraConfig.
- **RecorderConfig.encoding** (field 8): Desired encoding string ("h264", "h265", "mjpeg", "auto").
- **RecorderConfig.options map<string,string>**: Protocol-specific options (DID, vendor, region) — extensible per-plugin.
- **Nanosecond timestamps**: pts_ns, segment_duration_ns, uptime_ns — avoids float precision, matches Go time.Duration.
- **StartRecorder is server-streaming**: Plugin sends Frame stream, host receives. Host calls StopRecorder to end.
- **Empty message**: Used for PluginInfoRequest and HealthCheckRequest — cleaner than separate empty messages.

### Dependencies
- `google.golang.org/grpc v1.81.1` — direct dep (was already in go.mod)
- `google.golang.org/protobuf v1.36.11` — already in go.mod
- `github.com/hashicorp/go-plugin` — will be pulled in when plugin host code is written (not yet imported)

### Test Coverage (19 tests, all pass)
- Codec enum values (4 subtests)
- RecorderState enum values (6 subtests)
- Frame round-trip with small payload + codec info fields
- Frame with is_codec_info=true (SPS/PPS/VPS detection)
- Frame with empty data
- Frame with 500KB payload (simulating IDR frame)
- PluginInfo round-trip (with auth capability)
- RecorderConfig round-trip (with name + encoding fields)
- RecorderStatus round-trip
- CloudConfig round-trip
- HealthCheckResponse round-trip
- Backward compat: unknown fields preserved
- Backward compat: new optional field ignored (is_codec_info defaults false)
- Frame serialized size check (~1KB + overhead)
- Empty message (0 bytes)
- CloudConfigResponse, StopRequest, StatusRequest round-trips
- Capabilities all-off and all-on (including auth)

## 2026-05-15: FrameReceiver Implementation

### Architecture
- FrameReceiver lives in `internal/plugin/frame_receiver.go` — pure Go, no CGO
- Receives `*gen.Frame` from gRPC plugin stream via `HandleFrame(ctx, frame)` method
- Manages MP4Muxer + Segment lifecycle: CreateSegment → WriteSample → Close → Rename → DB Insert
- Mutex-protected: all state access is serialized through `sync.Mutex`

### Codec Detection
- Codec info frames (`IsCodecInfo=true`) carry SPS/PPS/VPS but are NOT written to muxer
- Codec type detected from `frame.Codec` enum (CODEC_H264=1, CODEC_H265=2)
- SPS/PPS/VPS data can come from two sources:
  1. `frame.Data` — raw NAL bytes (with or without Annex B start code)
  2. `frame.Extra` map — keys `sps_hex`, `pps_hex`, `vps_hex`
- Both sources are checked and stored; NAL data is parsed for NAL type to categorize
- `skipStartCode()` helper strips 00 00 00 01 or 00 00 01 prefix

### Segment Lifecycle
- Segments only start on IDR frames (keyframe boundary) — prevents black/gray frames
- P-frames before first IDR are silently discarded
- IDR while segment active triggers: close current → start new (seamless split)
- Segment duration check after each frame write — closes if expired
- On muxer init failure: temp file removed, error returned but stream continues

### Interfaces
- `SegmentStore` interface: `CreateSegment(cameraID, format) → (tempPath, finalPath, error)` and `CloseSegment(tempPath, finalPath) error`
- `RecordingDB` interface: `InsertRecording(ctx, rec)` and `InsertRecordingWithRetry(ctx, rec, retries, backoff)`
- Matches existing `storage.Manager` and `storage.DB` signatures exactly

### Pattern Consistency with Existing Recorders
- Same closeCurrentSegment pattern as H264Recorder/H265Recorder/XiaomiRecorder
- Same Recording struct construction: ID from UnixNano, Duration in seconds, etc.
- Same metrics pattern: SegmentsCreated + RecordingBytesTotal with camera_id/codec labels
- Same minimum duration guard: `duration < time.Millisecond` (not `<= 0`)

### Test Coverage (22 tests, all pass)
- Full H264 sequence: SPS → PPS → IDR → P×29 → IDR (split) → Close
- Full H265 sequence: VPS → SPS → PPS → IDR → P×30 → IDR → Close
- Discard before codec detection (10 frames silently dropped)
- Close cleanup with active segment
- Close with no segment (safe)
- Metrics counter increment
- Codec info from Extra map (SPS/PPS/VPS via string keys)
- H264/H265 NAL type detection from codec info data
- P-frame before IDR discarded
- Multiple segment splits (3 segments via IDR boundaries)
- CreateSegment error propagation
- Segment duration expiry (10ms timeout)
- Default segment duration (10min when 0 passed)
- Context passed to DB operations
- Concurrent frame handling (50 goroutines)
- skipStartCode helper (5 subtests)
- Missing codec params (H264 no PPS, H265 no PPS)
- Recording metadata validation (CameraID, Format, FrameCount, timestamps)

## 2026-05-15: PluginManager (gRPC Process Lifecycle)

### Architecture
- `internal/plugin/grpc_manager.go` — manages plugin process lifecycle via HashiCorp go-plugin
- PluginManager struct: discovery → launch → health check → crash detection → auto-restart → graceful shutdown
- ManagedPlugin struct: tracks per-plugin state (gRPC client, go-plugin Client, status, restart count)
- PluginInterface struct: implements go-plugin's Plugin interface for gRPC-only transport
- Coexists with existing in-process plugin registry (plugin.go) — no conflicts

### go-plugin Integration
- Package alias `goPlugin "github.com/hashicorp/go-plugin"` needed — `plugin` name conflicts with stdlib and own package
- `goPlugin.NewClient(config)` creates process manager — NOT a gRPC client directly
- `client.Client()` returns dispense interface (the actual gRPC broker)
- `dispenseClient.Dispense("nvr_plugin")` returns the plugin interface — must type-assert to `gen.PluginServiceClient`
- `client.Exited() bool` — polls whether subprocess died (no channel API)
- `client.Kill()` — terminates subprocess
- Must embed `goPlugin.NetRPCUnsupportedPlugin` for gRPC-only transport
- Handshake: `MagicCookieKey="NVR_PLUGIN"`, `MagicCookieValue="mibee-nvr-plugin"`, `ProtocolVersion=1`

### Crash Detection & Auto-Restart
- `monitorPlugin` goroutine polls `client.Exited()` every `DefaultHealthCheckInterval` (30s)
- On crash: exponential backoff (1s → 2s → 4s → ... → max 60s) + random jitter (backoff/2)
- Max 10 restart attempts, then permanently marks as `StatusError`
- After successful restart: resets backoff to `DefaultInitBackoff`
- Restart preserves `RestartCount` across restarts (cumulative for lifetime)

### Health Checking
- `healthCheckLoop` goroutine ticks every 30s
- Calls `HealthCheck()` gRPC on each running plugin with 5s timeout
- On failure: marks plugin as `StatusError` (monitor goroutine handles restart if process died)
- Snapshot pattern: copies plugin list under RLock, then checks without holding lock

### Thread Safety
- `sync.RWMutex` protects `plugins` map
- Read operations (Get/List) use RLock
- Write operations (Start/Stop/restart) use full Lock
- Monitor goroutine uses Lock/RLock for each iteration — safe concurrent access

### Test Coverage (18 tests, all pass)
- NewPluginManager creation
- Get/List/GetClient on empty manager
- Stop with no plugins (no panic)
- Start with disabled plugins (skipped)
- Start with missing binary (graceful error, logged not returned)
- resolvePluginPath (3 subtests: explicit, empty, relative)
- PluginInterface.GRPCClient returns non-nil
- Plugin status constants
- Handshake config values
- Plugin constants (durations, limits)
- pluginMap returns expected type
- Coexistence with in-process registry
- Concurrent access (50 goroutines)
- Status tracking (Running → Error transitions)

### Key Design Decisions
- Plugin errors logged but NOT returned from Start() — one bad plugin doesn't block others
- `resolvePluginPath` checks `entry.Path` first, falls back to `directory/name`
- `launch()` helper returns (client, grpcClient, info, error) — kills client on any error
- `RestartPlugin` cancels old monitor, kills old client, then calls startPlugin (clean slate)
- Health check uses separate 5s timeout context — not the manager's lifecycle context

## 2026-05-15: FrameReceiver Interface Extraction + Test Fix

### Interface Consolidation
- Created `internal/plugin/interfaces.go` with canonical `SegmentStore` and `RecordingDB` interfaces
- Removed duplicate interface definitions from `frame_receiver.go` (were lines 22-32)
- Same interfaces exist in `internal/recorder/h264.go`, `internal/recorder/h265.go`, `plugins/xiaomi/recorder.go` — these use local copies to avoid cross-package coupling (left unchanged per task constraints)

### Existing Test Fix
- `internal/plugin/grpc_adapter_test.go` had `stream := client.setStream(frames)` at line 208 but `stream` was used later at line 246 (`close(stream.blockCh)`) — Go compiler saw unused in one code path
- No actual bug — the variable IS used. The build error was likely introduced by a prior edit that changed the surrounding test flow

### Verification Results
- `go build ./...` — clean (no errors)
- `go test ./internal/plugin/...` — 62 tests pass (22 FrameReceiver + 18 PluginManager + 14 GRPC adapter + 8 Plugin registry)
- `go vet ./internal/plugin/...` — clean

## 2026-05-15: Mock Plugin (Integration Test Infrastructure)

### Structure and Package
- Tests live in `tests/mock_plugin/` with `package main` (standalone binary)
- Test files use `package main` (internal test) — not `package main_test`, since `package main` can't be imported
- `go test` correctly handles `package main` with `func main()` in non-test files — test binary excludes the real `main()`
- Four files: `main.go` (entry), `server.go` (gRPC impl), `plugin.go` (go-plugin wrapper), `server_test.go` (13 tests)

### go-plugin v1.6.3 API
- `plugin.ServeConfig.Plugins` is `map[string]plugin.Plugin` (type alias: `plugin.PluginSet`)
- `plugin.NetRPCUnsupportedPlugin` must be embedded by value for gRPC-only plugins
- `GRPCServer(*plugin.GRPCBroker, *grpc.Server) error` registers the gRPC server
- `GRPCClient(ctx, *plugin.GRPCBroker, *grpc.ClientConn) (interface{}, error)` creates the client
- Handshake config: `ProtocolVersion: 1`, `MagicCookieKey: "NVR_PLUGIN"`, `MagicCookieValue: "nvr-plugin"`
- `plugin.DefaultGRPCServer` is the server factory
- Same HandshakeConfig values used in PluginManager (but go-plugin host uses `plugin.NewClient` not `plugin.Serve`)

### Generated Proto Usage
- Must use `*gen.Empty` (proto-defined message) NOT `*emptypb.Empty` (Google well-known type) — they're different types
- The generated `PluginServiceServer` interface uses `*gen.Empty` for zero-param RPCs
- `grpc.ServerStreamingServer[gen.Frame]` is the generic streaming server type

### Synthetic H.264 Frame Generation
- SPS: NAL type 7, `0x67` + nal_ref_idc=3, 7 bytes payload
- PPS: NAL type 8, `0x68` + nal_ref_idc=3, 4 bytes payload
- IDR: NAL type 5, `0x65` (nal_ref_idc=3), ~10KB random payload
- P-frame: NAL type 1, `0x41` (nal_ref_idc=2), ~5KB random payload
- All frames prefixed with Annex B start code `0x00 0x00 0x00 0x01`
- Frame rate: ~30fps (33ms ticker), IDR every 30 frames = ~1 second segments

### In-memory gRPC Testing (bufconn)
- `bufconn.Listen(bufSize)` creates in-memory listener (no TCP, no Unix socket)
- `grpc.NewClient("passthrough://bufnet", grpc.WithContextDialer(...))` connects via bufconn dialer
- `insecure.NewCredentials()` required for non-TLS gRPC
- gRPC server runs in a goroutine, client connects through it
- Cleanup: close conn, GracefulStop server

### Key Gotchas
- `package main` can't be imported by other packages — test must be in same package (`package main` not `package main_test`)
- File tree must be flat (no `package main` + `package mock_plugin` in same directory)
- `go test` correctly excludes `func main()` from test binary — no conflict
- `go build -o tests/mock_plugin/mock-plugin ./tests/mock_plugin/` works because all non-test files are `package main`
- Ticker pattern vs context-cancel: StartRecorder uses `time.NewTicker(33ms)` + `select` for both tick and ctx.Done()
- Atomic counters (`atomic.Int64`) for bytesWritten and segments — safe concurrent access


## 2026-05-15: gRPCRecorderAdapter (Task 7)

### Design
- `FrameHandler` interface decouples adapter from FrameReceiver implementation. Two methods: `HandleFrame(ctx, *gen.Frame) error` and `Close() error`.
- `gRPCRecorderAdapter` implements `model.Recorder` by proxying to `gen.PluginServiceClient`.
- Compile-time check: `var _ model.Recorder = (*gRPCRecorderAdapter)(nil)`
- Constructor: `NewGRPCRecorderAdapter(client, handler, cfg, segmentDur)` — segmentDur defaults to 30s if <= 0.

### Key Patterns
- `Start()`: Creates child context, builds `RecorderConfig` proto from `config.CameraConfig`, calls `client.StartRecorder()`, starts receive goroutine.
- `Stop()`: Cancels context, calls `client.StopRecorder()` (best-effort, 5s timeout), waits for done channel (10s timeout), closes handler.
- `receiveLoop()`: Reads frames from stream, forwards to handler. On context cancel → StatusStopped. On stream/handler error → StatusError.
- **No deferred setStatus**: The receiveLoop does NOT use `defer a.setStatus(model.StatusStopped)` — that would override error statuses. Instead, each exit path sets status explicitly.
- `buildRecorderConfig()`: Maps `config.CameraConfig` fields to proto `RecorderConfig`, including DID/vendor/ONVIF fields in the `Options` map.

### RecorderConfig Options Mapping
- `cfg.DID` → `options["did"]`
- `cfg.Vendor` → `options["vendor"]`
- `cfg.ONVIFEndpoint` → `options["onvif_endpoint"]`
- `cfg.ProfileToken` → `options["profile_token"]`
- `cfg.StreamEncoding` → `options["stream_encoding"]`

### Test Infrastructure
- `mockStream` uses a `blockCh` channel to simulate blocking on `Recv()` after frames are exhausted. Tests must `close(stream.blockCh)` before `Stop()` to prevent 10s timeout.
- `mockPluginClient` wraps `gen.PluginServiceClient` with configurable stream, stop error, and atomic stop counter.
- `mockFrameHandler` records frames, has configurable error injection, and tracks closed state.

### Test Coverage (11 tests, all pass)
1. DefaultSegmentDur — zero duration → 30s default
2. StartStop — full lifecycle, frames forwarded, status transitions
3. StartTwiceFails — double start returns error
4. StreamError — plugin crash → StatusError
5. HandlerError — handler failure → StatusError
6. ContextCancellation — ctx cancel → StatusStopped
7. StartRPCFailure — StartRecorder failure → StatusError
8. BuildRecorderConfig — CameraConfig→proto mapping
9. StopWithoutStart — no panic on unstarted adapter
10. StopRPCFailure — StopRecorder RPC fail still succeeds
11. ManyFrames — 50 frames throughput
12. FrameFields — H265 frame with extra metadata preserved

### Parallel Task Conflict
- Task 5 (FrameReceiver) created duplicate `SegmentStore`/`RecordingDB` interfaces in `frame_receiver.go`. Task 2 also created `interfaces.go` with canonical definitions. The `frame_receiver.go` was updated to remove its local copies.
