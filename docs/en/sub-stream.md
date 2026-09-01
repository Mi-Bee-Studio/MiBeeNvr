# Sub-Streams (Low-Resolution Secondary Streams)

> For MiBeeNvr v0.12.0

Most IP cameras output two streams: the **main stream** (high resolution, for recording) and a **sub-stream** (low resolution/bitrate, traditionally for previews). MiBee NVR makes the sub-stream an **on-demand** first-class citizen: the surveillance grid, GB cascade, and external AI consumers ride the sub-stream while **main-stream recording is untouched**; with no viewers the sub-stream pull stops by itself — zero cost when nobody watches.

## Who Consumes the Sub-Stream

| Consumer | Behavior |
|----------|----------|
| Surveillance grid | Header **"Smooth / HD"** toggle, default Smooth — bandwidth and decode load drop sharply when viewing many cameras |
| Live page (single camera) | Per-camera quality switcher (shown when this camera has a sub-stream) |
| Live API | `stream/ws` / `stream.flv` with `?quality=sub`; HLS uses the path form `/api/cameras/{id}/stream/sub/index.m3u8` |
| WebRTC (WHEP) | True sub-stream session when the sub-stream is H.264; H.265 sub-streams fall back to main (WebRTC is H.264-only) |
| GB28181 cascade | Per-camera "cascade sub-stream" toggle — upper-platform previews stop saturating your uplink (see the [GB/T 28181 guide](gb28181-guide.md)) |
| External AI push | Sub-stream analysis layer — low-res segments decode at 1/4–1/16 the cost of main (see the [configuration reference](configuration.md#vision-push-integration-configuration)) |

**Fallback semantics**: when a camera has no sub-stream or the pull fails, every consumer falls back to the main stream, and the live response header `X-Stream-Quality: main|sub` reports what was actually served — the frontend never black-screens, scripts can decide.

## Camera-Side Configuration

The collapsible **"Sub-stream"** section of the camera edit form:

| Field | Description |
|-------|-------------|
| Sub-stream RTSP URL | Manually enter any camera's `rtsp://…` sub-stream address (Hikvision convention `…/Streaming/Channels/102`, Dahua `…/cam/realmonitor?channel=1&subtype=1`) |
| ONVIF sub-stream profile token | **Blank = auto-discover**: after connecting, the NVR picks the second-highest resolution profile besides the main one (fill-once; clear and save to re-discover) |

Either target suffices. GB28181 cameras have their own vendor-convention sub-channel probing — see the [GB/T 28181 guide](gb28181-guide.md).

> **ONVIF profile metadata can lie** (advertising H.264 while serving H.265) — the NVR trusts the actual RTSP DESCRIBE, no manual verification needed.

## Server Configuration

```yaml
server:
  substream:
    idle_timeout_s: 30   # keep an idle pull warm this long before recycling (default 30s)
    ready_timeout_s: 8   # how long a quality=sub request waits for first video (default 8s)
```

## RTSP Output (Third-Party Platforms)

The NVR embeds an RTSP output server; every camera gets one stable pull URL that third-party platforms (e.g. Synology Surveillance Station) can use directly as a camera source:

```text
rtsp://<NVR-IP>:8554/<camera_id>
```

- H.264 / H.265 served natively (no transcoding); concurrent clients each get an independent stream
- Credentials optional, open on the LAN by default; configured via `server.rtsp {enabled, port, username, password}`
- Docker deployments must publish the port (`-p 8554:8554`); a bind failure only logs an error — the rest of the NVR keeps working

## Observing & Troubleshooting

- `GET /api/cameras/{id}/protocols` reports a `sub_stream` entry with availability, source, and reason (`available` / `source` / `reason`)
- The dashboard link tree shows the sub-stream as its own dashed branch (differential fps/kbps + consumer types); it appears with consumers and disappears on recycle
- Log keywords: `sub-stream live` (pull up), `recycled reason=idle` (idle recycle), `WHEP: sub-stream not servable over WebRTC` (H.265 sub falling back to main — expected)

## FAQ

| Question | Answer |
|----------|---------|
| Grid "Smooth" changed nothing? | That camera may have no sub-stream — the fallback to main is silent; check `sub_stream` in `/protocols` |
| Sub-stream laggy? | The NVR pulls the sub-stream over its own RTSP connection; check the camera's sub-stream framerate is at least 15fps |
| Why is WHEP always main-stream? | WebRTC supports H.264 only — cameras whose sub-stream is H.265 always fall back (visible in logs) |
| Which stream gets recorded? | Recording always uses the **main stream**; the sub-stream serves preview/cascade/push consumers only |

## Next Steps

- [Streaming Protocol Selection](streaming-protocol-selection.md) — choosing WebRTC / WS / FLV / HLS / WASM
- [GB/T 28181 Guide](gb28181-guide.md) — cascading the sub-stream to an upper platform
- [Configuration Reference](configuration.md) — all configuration keys
