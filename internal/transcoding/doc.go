// Package transcoding provides hardware-aware video transcoding for stored recordings.
//
// It supports converting between H.264, H.265, and MJPEG formats using FFmpeg as the
// backend transcoder. The package performs automatic hardware capability detection on
// startup, checking for available encoders (V4L2M2M, VAAPI, NVENC) and falling back
// to software encoding (libx264/libx265) when appropriate.
//
// Key features:
//   - Hardware capability self-check (detects available encoders, cores, memory)
//   - Async task queue with progress tracking
//   - Multi-format support (H.264 ↔ H.265, MJPEG → H.264)
//   - Per-camera transcoding configuration
//   - FFmpeg download management (static binary for ARM)
//
// Architecture overview:
//
//	Manager (singleton) → Hardware probe → Task queue → Worker pool → FFmpeg exec
//	         ↕                           ↕
//	  REST API handler              DB (SQLite)
//
// The Manager coordinates probe results, queues tasks, and manages worker goroutines.
// Each worker invokes FFmpeg as a subprocess with appropriate encoder flags based on
// the hardware capabilities detected at startup.
//
// Module path: github.com/Mi-Bee-Studio/MiBeeNvr
package transcoding
