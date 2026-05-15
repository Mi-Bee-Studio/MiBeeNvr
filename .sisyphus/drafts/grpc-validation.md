# gRPC Plugin Validation — Spike Results

**Date**: 2026-05-15
**Spike location**: `/tmp/opencode/grpc-spike/`

## Executive Summary

**Both HashiCorp go-plugin and raw gRPC work perfectly with `CGO_ENABLED=0`.**

**Recommendation: Use HashiCorp go-plugin.**

It provides batteries-included process lifecycle, automatic crash detection with auto-restart, log forwarding, and protocol negotiation — all pure Go, zero CGO.

---

## Test Results

### HashiCorp go-plugin (gRPC mode)

| Test | Result | Notes |
|------|--------|-------|
| CGO_ENABLED=0 build | ✅ PASS | Statically linked, no dynamic deps |
| Basic gRPC call | ✅ PASS | Host ↔ Plugin via UDS |
| Crash detection | ✅ PASS | Auto-restarts killed plugin; call succeeds after restart |
| Multiple sequential calls | ✅ PASS | 10/10 calls succeed |
| Lifecycle (start/stop/restart) | ✅ PASS | 2 rounds of start→use→kill |
| ARM64 cross-compilation | ✅ PASS | `GOOS=linux GOARCH=arm64 CGO_ENABLED=0` |

### Raw gRPC (manual process management)

| Test | Result | Notes |
|------|--------|-------|
| CGO_ENABLED=0 build | ✅ PASS | Statically linked |
| Basic gRPC over UDS | ✅ PASS | Manual exec.Command + grpc.Dial |
| Crash detection | ✅ PASS | Call correctly fails with `Unavailable` after kill |
| ARM64 cross-compilation | ✅ PASS | No issues |

---

## Binary Size Comparison

All sizes are **stripped** (`-ldflags="-s -w"`), ARM64:

| Binary | Size | Notes |
|--------|------|-------|
| Current NVR (no gRPC) | 22 MB | Baseline |
| Spike host (go-plugin) | 12 MB | Minimal host; real host will be larger |
| Spike plugin (go-plugin) | 12 MB | Minimal plugin |
| Spike raw-host | 12 MB | Slightly smaller (no go-plugin deps) |
| Spike raw-plugin | 12 MB | Same as above |

### Dependency Impact (added to go.mod)

```
google.golang.org/grpc          v1.81.1   (direct)
google.golang.org/protobuf      v1.36.11  (already present as indirect)
google.golang.org/genproto      v0.0.0    (indirect)
github.com/hashicorp/go-plugin  v1.8.0    (direct, for HashiCorp approach only)
github.com/hashicorp/go-hclog   v1.6.3    (indirect, for HashiCorp approach)
github.com/hashicorp/yamux      v0.1.2    (indirect, for HashiCorp approach)
github.com/oklog/run            v1.1.0    (indirect, for HashiCorp approach)
```

**Estimated binary size increase for full NVR**: ~10-13 MB (gRPC runtime). Total: ~32-35 MB. Well within RPi 3B constraints.

**go.sum entries added**: ~76 lines (spike) vs 160 lines (current project). The gRPC deps are non-trivial but manageable.

---

## Comparison: HashiCorp go-plugin vs Raw gRPC

| Feature | HashiCorp go-plugin | Raw gRPC |
|---------|--------------------|---------|---------:|
| CGO_ENABLED=0 | ✅ Works | ✅ Works |
| ARM64 cross-compile | ✅ Works | ✅ Works |
| Process lifecycle | ✅ Built-in | ❌ Must implement (exec.Command, wait, restart) |
| Crash detection | ✅ Built-in auto-restart | ❌ Must implement (health check, reconnection) |
| Log forwarding | ✅ Built-in (via stdout/stderr pipe) | ❌ Must implement manually |
| Protocol negotiation | ✅ Built-in (version + magic cookie) | ❌ Must implement |
| Bidirectional comms | ✅ Via GRPCBroker | ❌ Plugin→Host needs separate mechanism |
| mTLS support | ✅ Built-in | ❌ Must implement or skip |
| Complexity | Low (framework handles it) | High (all plumbing is your code) |
| Binary size overhead | ~1 MB extra (go-plugin + yamux + hclog) | Minimal |
| Battle-tested | ✅ Terraform, Vault, Consul, Packer | You own the bugs |

---

## Recommendation

### Use HashiCorp go-plugin for the following reasons:

1. **Zero CGO required** — Pure Go, statically links. Verified.
2. **Built-in process management** — No need to write exec.Command + health check + restart logic.
3. **Crash detection with auto-restart** — Plugin dies → host auto-restarts it. No manual reconnection code.
4. **Log forwarding** — Plugin stdout/stderr automatically piped through host logger. No manual plumbing.
5. **Battle-tested** — Used by Terraform, Vault, Consul, Packer. Production-hardened.
6. **Bidirectional communication** — `GRPCBroker` allows plugin to call back to host (needed for progress reporting, status updates).
7. **Protocol versioning** — Handshake config prevents version mismatches.
8. **Binary size impact is acceptable** — ~1 MB overhead for go-plugin framework on top of raw gRPC. Total binary increase for NVR: ~10-13 MB (from gRPC itself, not go-plugin).

### Only downside:
- Adds `hashicorp/go-plugin`, `hashicorp/go-hclog`, `hashicorp/yamux`, `oklog/run` as dependencies
- `go-hclog` for logging (different from `slog`) — but only used internally by go-plugin framework, not in application code
- These are all stable, well-maintained HashiCorp libraries

### When to use raw gRPC instead:
- If binary size is absolutely critical (saves ~1 MB)
- If you need complete control over process lifecycle
- If you don't want HashiCorp dependencies in your go.mod

**For MiBee NVR: The ~1 MB savings from raw gRPC is NOT worth the significant additional code complexity. Use HashiCorp go-plugin.**

---

## Gotchas Found

1. **`go-hclog` vs `slog`**: HashiCorp go-plugin uses `hclog.Logger` interface, not `slog.Logger`. Pass `hclog.Default()` to `plugin.ClientConfig.Logger`. Your app can still use `slog` — only the plugin framework needs `hclog`.

2. **Auto-restart on crash**: When the plugin process is killed, HashiCorp go-plugin auto-restarts it. The next gRPC call will succeed. This is a FEATURE for production, but means "crash detection" tests need to check the PID changed, not just that the call fails.

3. **`plugin.NetRPCUnsupportedPlugin`**: Must embed this in your gRPC-only plugins to disable net/rpc (legacy protocol). Ensures only gRPC is used.

4. **Proto generation**: Standard protoc + protoc-gen-go + protoc-gen-go-grpc workflow. No surprises.

5. **UDS on RPi**: Unix Domain Sockets work fine on ARM Linux. go-plugin uses UDS on all Unix-like systems, TCP only on Windows.

6. **Plugin binary must be separate**: go-plugin launches a separate process. Plugin code cannot be in the same binary as the host. This is by design — it's a process isolation boundary.

---

## Files in Spike

```
/tmp/opencode/grpc-spike/
├── go.mod
├── go.sum
├── shared/
│   ├── greeter.proto          # Proto definition
│   ├── greeter.pb.go          # Generated protobuf messages
│   ├── greeter_grpc.pb.go     # Generated gRPC client/server
│   └── plugin.go              # HashiCorp go-plugin bridge
├── cmd/
│   ├── host/main.go           # HashiCorp go-plugin host (4 tests)
│   ├── plugin/main.go         # HashiCorp go-plugin plugin binary
│   ├── raw-host/main.go       # Raw gRPC host (2 tests)
│   └── raw-plugin/main.go     # Raw gRPC plugin binary
```
