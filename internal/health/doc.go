// Package health implements the camera health monitoring system.
//
// # Architecture
//
// The health monitoring system operates in three layers:
//   - Layer 1: Connection health — detects camera online/offline/reconnecting state changes
//   - Layer 2: Stream stats — monitors bitrate, FPS, and IDR interval anomalies
//   - Layer 2.5: Freeze detection — detects frozen video via timestamp monitoring
//
// Events are dispatched through an alert pipeline with cooldown deduplication,
// published via MQTT and stored in SQLite for historical analysis.
package health
