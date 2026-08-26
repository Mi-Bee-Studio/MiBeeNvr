# Observability Guide

MiBee NVR ships a complete troubleshooting and monitoring toolkit: the flow-path view, end-to-end latency display, health stability stats, per-camera frame-trace sampling, Prometheus metrics and Grafana dashboards. This page is the entry point; full metric reference lives in [metrics.md](metrics.md).

## Flow View

The per-camera flow tree on the Dashboard shows the full journey of a video frame: **producer → StreamHub → recording / live-protocol consumers / health / relay**.

### Column reference

| Column | Meaning | Troubleshooting |
|--------|---------|-----------------|
| sends | Frames delivered to this consumer | Stopped growing = that branch is dead |
| drops | Frames dropped (consumer buffer full) | Growing fast = consumer can't keep up |
| IDR drops | Keyframes dropped | >0 = viewers may wait long for a keyframe |
| drop_rate | drops / sends | >1% orange, >5% red |
| buffer | Current queue depth / capacity | Near capacity = drops imminent |
| dwell avg/max | Frame queue→dispatch latency in the hub | Growing max = a slow consumer drags fan-out |

The **fps / kbps** on each node are derived by the frontend diffing cumulative counters across ~2s polls — the backend hot path never computes rates, so a few seconds of refresh lag is expected.

### Recording branch

The recording node additionally shows (H.264/H.265 cameras):

- **Segment progress**: current MP4 segment elapsed vs target (`segment_duration`) and frame count — near target means a rotation is imminent
- **Ring buffer level**: the recorder's internal frame ring occupancy / capacity. Yellow above 50% means disk writes are falling behind the frame rate; sustained saturation leads to drops (`nvr_recorder_ring_buffer_drops_total`)
- **Pending merges**: segments waiting for the rolling merge (`nvr_merge_pending_segments`). Continuous growth = merging falls behind production

### Last frame

How long ago the hub last saw a video frame. This is the single most direct "is the stream alive" signal — after a camera dies, cumulative counters stay (historical totals) while this field keeps growing.

## End-to-end latency badge

Players that support live playback show a latency badge at the top-left (hover for the tooltip):

- **< 1s** green, **< 3s** yellow, **≥ 3s** red
- WebCodecs (WS) and HTTP-FLV are **exact**: frames are wallclock-stamped at hub entry and the stamp travels with the frame to the browser
- Values prefixed with **≈** are approximations: HLS derives from buffered segments (segment granularity limits precision); WebRTC uses the camera's hub-ingest time (browsers don't expose RTP timestamps)
- Samples are reported every 10s via telemetry (`live_latency`, tagged with `protocol`) and can be cross-checked against `nvr_playback_live_latency_ms`

## Per-camera frame tracing

The fastest way to answer "why is this camera stuttering": start a 30-second sampling window and that camera's per-keyframe breadcrumb logs escalate to Info level in one centralized stream — you see exactly which hop drops or delays frames:

```bash
# Start (default 30s, max 5m)
curl -X POST 'http://nvr:9090/api/cameras/front-door/trace?duration=30s'

# Check remaining window
curl http://nvr:9090/api/cameras/front-door/trace

# Stop early
curl -X DELETE http://nvr:9090/api/cameras/front-door/trace
```

Filter logs by `component=frame-trace` (or `journalctl -u mibee-nvr | grep frame-trace`). The chain:

```text
ingest (recorder received the frame)
  → streamhub_in / streamhub_drop (hub entry / consumer drop)
  → ws_recv / flv_recv / hls_* / webrtc_recv|drop (per-protocol branches)
```

`trace_id` is `cameraID-PTS` (keyframes only) — the same frame carries the same ID across hops. The window auto-expires; never leave per-frame logging on for all cameras permanently (volume = frame rate × hops × ~250B/line).

## Health stability (Health page)

Beyond the live score, the Health page provides stability statistics:

- **Uptime %**: share of the window the camera spent recordable
- **MTBF**: mean time between failures — average interval between dropouts
- **Trend chart**: score over time, for spotting intermittent degradation (weak Wi-Fi, pre-DHCP-change symptoms, …)

Deduction reasons are listed in plain language (e.g. "3.2% frame drops in the last 5 minutes") — no rule-table lookup needed.

## Prometheus + Grafana

### Scraping

`/metrics` is public by default (optional BasicAuth via `metrics_auth`); Prometheus scrape config in [metrics.md](metrics.md).

### Import dashboards

`deploy/grafana/` ships 5 dashboards + alert rules — import into Grafana as-is:

| File | Content |
|------|---------|
| `mibee-nvr-flow.json` | Flow metrics: per-camera inbound fps/bitrate, consumer drop rates, hub dwell |
| `camera-health-dashboard.json` | Health scores, uptime, blacklist/auto-reconnect |
| `streaming-quality-dashboard.json` | Per-protocol viewers, frames sent/dropped, e2e latency |
| `video-playback-dashboard.json` | Recording fps, segment durations, merge queue depth |
| `system-overview-dashboard.json` | CPU/memory/goroutines/disk usage |
| `alerts.yaml` | Common alert rules (camera offline, drop-rate threshold, disk filling) |

> Deploy dashboards and alerts on a **separate monitoring box**, not on the NVR itself — leave Raspberry-Pi-class hardware to recording.

### Latency metric

`nvr_playback_live_latency_ms` (label `protocol` = ws/flv/hls/webrtc) comes from browser telemetry and reflects **what real users see** — complementary to hub-side metrics.

## pprof profiling

Enable in config (off by default):

```yaml
observability:
  enable_pprof: true
```

This exposes `/debug/pprof/*` (CPU/heap/goroutine profiles). The endpoint sits behind the auth middleware — login or an API key is required — but prefer enabling it only during troubleshooting and turning it off afterwards.

## Related docs

- [metrics.md](metrics.md) — full Prometheus metric reference
- [configuration.md](configuration.md) — `observability` settings
- [troubleshooting.md](troubleshooting.md) — common problems
