# Learnings — Plugin System + Xiaomi

## Conventions
- All tests use `testify/require` (not assert)
- `t.Helper()` mandatory in all test helpers
- Structured logging: `slog.Default().With("component", "pkg-name")`
- Config uses atomic write (temp file + rename)
- Timestamps in UTC format `2006-01-02 15:04:05.999999999`
- Camera IDs: crypto/rand 8-char alphanumeric
- Recording IDs: `fmt.Sprintf("%d", time.Now().UnixNano())`
- Segment lifecycle: temp file → write → close muxer → atomic rename → insert DB
- CGO_ENABLED=0 always, single static binary
- Protocol strings: transport-only (`rtsp`, `http`, `onvif`)

## 2026-05-13 Session Start
- Plan file: 996 lines, 15 tasks + 4 verification tasks
- Branch: feature/plugin-system-xiaomi
- go2rtc source at /tmp/go2rtc/ (MIT license)
- Zero new deps needed: golang.org/x/crypto + pion/rtp already in go.mod
