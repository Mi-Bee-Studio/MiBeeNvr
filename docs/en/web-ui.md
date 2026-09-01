# Web UI Tour

> For MiBeeNvr v0.12.0

The MiBee NVR web interface is organized into five top-level pages plus the single-camera live page. This page is a feature cheat sheet; step-by-step configuration lives in the dedicated guides.

![Surveillance grid](images/surveillance.webp)

## Surveillance (Home)

The default page after login — a live multi-camera grid:

- **Grid layout**: the "Configure" button picks which cameras to show
- **Header quality switch**: a **Smooth / HD** toggle for the whole grid — "Smooth" rides the camera's [sub-stream](sub-stream.md) (default), slashing bandwidth and decode load when viewing many cameras at once; cameras without one silently fall back to the main stream
- **Per tile**: camera name, live badge, playback protocol (WebCodecs / MJPEG, etc.), and a **health score**
- **Tile controls**: unmute (cameras with audio) and fullscreen
- **AI overlay**: with [browser-side AI detection](ai-detection-tuning.md) enabled, detection boxes draw directly on the feed
- **Click a tile** to open that camera's live page

> H.265 cameras play in the browser via WASM decoding — no HTTPS or plugins needed; see [Streaming Protocol Selection](streaming-protocol-selection.md).

## Cameras

Full camera lifecycle management:

| Action | Description |
|---------|-------------|
| Scan Devices | ONVIF / Xiaomi auto-discovery, see [ONVIF Auto-Discovery](onvif-discovery.md) |
| + Add Camera | manually add a camera of any protocol |
| Start/Stop switch | temporarily disable / re-enable a camera (config and recordings kept) |
| Restart | disconnect and reconnect the camera |
| Live | open the live page |
| Activate | some GB / ONVIF devices need activation first |
| Archived | archived old cameras (replaced devices, record keeping) |

Cards show the run state (recording / live-only / stopped / reconnecting / unreachable) plus protocol and codec tags.

The edit form is organized into collapsible sections; beyond basic access settings these include:

- **[Recording mode](adaptive-recording.md)**: continuous or adaptive (motion-aware) — the latter carries audio-trigger and ambient-audio options
- **[Sub-stream](sub-stream.md)**: ONVIF sub-stream auto-discovery or a manual sub-stream RTSP URL
- **Storage**: assign this camera its own recording root and enqueue [background migration](storage-management.md) for its history
- **GB cascade**: whether to expose this camera to the upper platform, and whether to cascade its sub-stream

## Recordings

The search-and-playback center with three views:

- **Timeline** (default): one track per camera for the day, AI events overlaid as markers; click / drag to position playback
- **List**: segment details (format, duration, size, merge status) with multi-select delete
- **Timelapse**: dedicated timelapse view

The toolbar filters by type (video / timelapse / MJPEG), camera, **AI detection** (person / vehicle), **activity** (motion / static / scene-cut, plus a minimum activity score), and keyword search. See [Recording & Playback](recording-playback.md).

## Dashboard

Operations at a glance, organized into four tabs:

- **Storage trend** (default): a stacked daily-write chart per camera (custom SVG) — **the legend doubles as a camera selector** (click to isolate / toggle / click again to restore), each bar independently sorted by that day's write volume, hover a segment for the day's breakdown, one click flips the sort direction; the "Camera Status" card below shows **segment count and storage usage per row** (sortable headers), plus a per-camera storage card (usage, segments, share bar)
- **Health history**: camera health-event timeline
- **Transcoding history**: transcode job log
- **AI Events**: detection events pushed by the server-side AI backend (MiBeeVision integration) — filter by camera and event type (zone entry / line crossing / loitering / object detected); each event can **jump to the matching recording moment**. Visible only when an external AI backend is configured

> Each row of the camera-status card expands into a **link-diagnosis tree** (capture source → hub → recording / live consumers / health / relay / sub-stream) — green = active, gray = idle, orange = anomalous, red = heavy frame loss. See [Observability](observability.md).

## Settings

Nine pages: **General** (timezone / port / frontend prefs), **Storage** (root path, [candidate volumes & migration](storage-management.md)), **Camera Access**, **Streaming**, **GB28181**, **AI Detection** (browser-side detection + MiBeeVision integration + per-camera config), **Recording & Processing** (incl. [activity-aware cleanup](adaptive-recording.md#activity-scores--retrieval)), **Advanced**, **About**.

- The "A new version is available" badge on the nav signals an upgrade (see the [Upgrade Guide](upgrade-guide.md))
- Most settings apply immediately; a few (like the storage path) need a restart

## Live Page (Single Camera)

Opened from the grid or a camera card at `#/live/{id}`:

- Large live view with protocol switching
- **Quality switcher**: switch between main / [sub-stream](sub-stream.md) when the camera has one
- **PTZ control**: pan/tilt/preset control for capable cameras (e.g. Xiaomi PTZ models)
- **Two-way audio**: push-to-talk back to the camera (see [Audio](audio.md))
- **Snapshot**: save the current frame

## Interface Preferences

- **Theme**: light / dark / follow system (toggle top-right)
- **Language**: 中文 / English (toggle top-right, applies instantly)
- **PWA**: installable as a standalone app on desktop / phone home screens; the UI opens offline

## Next Steps

- [Quick Start](quickstart.md) — add your first camera
- [CLI Reference](cli.md) — command-line administration
- [Live Relay](relay-guide.md) — push feeds to streaming platforms
