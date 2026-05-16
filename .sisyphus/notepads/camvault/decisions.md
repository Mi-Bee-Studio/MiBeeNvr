## 2026-04-26 Architectural Decisions
- Dual recording pipeline: H.264→MP4 + MJPEG→JPEG sequence (ESP32-CAM outputs JPEG only)
- Pure Go MP4 muxing via abema/go-mp4 (no ffmpeg dependency)
- modernc.org/sqlite for zero-CGO cross-compilation
- NVR process RSS hard cap: 300MB
- WebDAV read-only in v1 (prevent metadata desync)
- FTP needs independent port (cannot proxy through Caddy)
- Server-side timestamps only (never trust camera clocks)
- Atomic file writes: .tmp + os.Rename()
