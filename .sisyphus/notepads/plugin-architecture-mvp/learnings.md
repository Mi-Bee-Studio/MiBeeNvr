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
