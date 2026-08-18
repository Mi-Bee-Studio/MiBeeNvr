# Web UI Tour

> For MiBeeNvr v0.11.0

The MiBee NVR web interface is organized into six pages. This page is a feature cheat sheet; step-by-step configuration lives in the dedicated guides.

![Surveillance grid](images/surveillance.webp)

## Surveillance (Home)

The default page after login — a live multi-camera grid:

- **Grid layout**: the "Configure" button picks which cameras to show
- **Per tile**: camera name, live badge, playback protocol (WebCodecs / MJPEG, etc.), and a **health score**
- **Tile controls**: unmute (cameras with audio) and fullscreen
- **AI overlay**: with [browser-side AI detection](ai-detection.md) enabled, detection boxes draw directly on the feed
- **Click a tile** to open that camera's live page

> H.265 cameras play in the browser via WASM decoding — no HTTPS or plugins needed; see [Streaming Protocol Selection](streaming.md).

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

## Recordings

The search-and-playback center with three views:

- **Timeline** (default): one track per camera for the day, AI events overlaid as markers; click / drag to position playback
- **List**: segment details (format, duration, size, merge status) with multi-select delete
- **Timelapse**: dedicated timelapse view

The toolbar filters by type (video / timelapse / MJPEG), camera, **AI detection** (person / vehicle), and keyword search. See [Recording & Playback](recording-playback.md).

## AI Events

Detection events pushed by the server-side AI backend (MiBeeVision integration): filter by camera and event type (zone entry / line crossing / loitering / object detected); each event can **jump to the matching recording moment**. Integration details in the [API Overview](api.md).

## Dashboard

Operations at a glance: camera online / recording stats, storage usage and trends — for daily inspection.

## Settings

Nine pages: **General** (timezone / port / frontend prefs), **Storage** (root path and candidate volumes), **Camera Access**, **Streaming**, **GB28181**, **AI Detection** (browser-side detection + MiBeeVision integration + per-camera config), **Recording & Processing**, **Advanced**, **About**.

- The "A new version is available" badge on the nav signals an upgrade (see the [Upgrade Guide](upgrade-faq.md))
- Most settings apply immediately; a few (like the storage path) need a restart

## Live Page (Single Camera)

Opened from the grid or a camera card at `#/live/{id}`:

- Large live view with protocol switching
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
- [Live Relay](relay.md) — push feeds to streaming platforms
