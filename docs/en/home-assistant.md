# Home Assistant Integration

> Applies to MiBee NVR v0.12.0+ (built-in RTSP output, MQTT triggers and status publishing)

MiBee NVR has no official Home Assistant integration, but its RTSP output, REST API, MQTT trigger, and status publishing capabilities combine into a complete setup. This guide splits the paths by purpose — each can be enabled independently.

## Overview

| Purpose | Path | Dependencies |
|---------|------|--------------|
| View (H.264/H.265 cameras) | RTSP output + Generic Camera | None (HA native) |
| View (MJPEG/JPEG cameras) | `stream.mjpeg` + MJPEG Camera | None (HA native) |
| Trigger (HA → NVR) | MQTT trigger | MQTT broker |
| State (NVR → HA) | MQTT status publishing (recommended) or REST polling | None / MQTT broker |
| Low-latency view | HACS WebRTC card | HACS |

The simplest combination is "RTSP for viewing + MQTT trigger + MQTT state" — HA-native integrations only.

## Prerequisites

- NVR REST endpoints use the web-login credentials as Basic Auth (except `/api/health`, `/api/events`, and `/api/metrics`, which are public).
- The camera ID is the `id` configured in the NVR (kebab-case, e.g. `front-door`).
- Never hardcode passwords into version-controlled YAML — use HA `secrets`.

## Option A: RTSP Output + Generic Camera (H.264/H.265 cameras)

The NVR ships a built-in RTSP output server (enabled by default), one pull URL per camera:

```text
rtsp://<NVR-IP>:8554/<camera_id>
```

- H.264/H.265 native distribution only — no transcoding, video only (no audio); **MJPEG/JPEG cameras are not served** — use Option A' instead.
- No authentication by default (open on the LAN); optional username/password:

```yaml
# mibee-nvr.yaml
server:
  rtsp:
    enabled: true
    port: 8554
    username: ""     # optional; with auth set, use rtsp://user:pass@<NVR-IP>:8554/<camera_id>
    password: ""
```

Docker deployments must publish port 8554 (the official compose file already does).

Add one Generic Camera per camera in HA's `configuration.yaml`:

```yaml
camera:
  - platform: generic
    name: "Front Door"
    still_image_url: "http://192.168.63.30:9090/api/cameras/front-door/snapshot"
    stream_source: "rtsp://192.168.63.30:8554/front-door"
    username: admin
    password: "!secret nvr_password"
```

About `still_image_url`:

- `GET /api/cameras/{id}/latest-frame` only works for JPEG-family cameras (HTTP-JPEG/MJPEG); H.264/H.265 cameras return 404.
- For H.264/H.265 cameras use `GET /api/cameras/{id}/snapshot` (proxies the camera's own snapshot URL; requires a device with ONVIF snapshot support) or the vendor's snapshot URL directly.

## Option A': MJPEG/JPEG Cameras (e.g. ESP32 cameras)

MJPEG/JPEG cameras bypass the RTSP output — use HA's native MJPEG Camera against the NVR's MJPEG stream instead:

```yaml
camera:
  - platform: mjpeg
    name: "Yard ESP32"
    mjpeg_url: "http://192.168.63.30:9090/api/cameras/yard-esp32/stream.mjpeg"
    username: admin
    password: "!secret nvr_password"
    verify_ssl: false
```

For a static thumbnail add `still_image_url: "http://.../api/cameras/{id}/latest-frame"` (works for JPEG-family cameras, ETag 304 supported).

## Option B: MQTT Trigger (HA → NVR)

Have the NVR start recording the moment a motion sensor fires. Configure MQTT on the NVR side first (see [MQTT Integration](./mqtt-integration.md)):

```yaml
# mibee-nvr.yaml
mqtt:
  enabled: true
  broker: "tcp://192.168.63.1:1883"
  client_id: "mibee-nvr"
  topic: "mibee"
  username: "mqtt_user"
  password: "mqtt_password"
```

The NVR subscribes to `mibee/trigger/{camera_id}` and supports `record` (start recording) and `stop` (stop recording); `snapshot` is not implemented yet. HA automation example:

```yaml
automation:
  - alias: "Record front door on motion"
    trigger:
      - platform: state
        entity_id: binary_sensor.front_door_motion
        to: "on"
    action:
      - service: mqtt.publish
        data:
          topic: "mibee/trigger/front-door"
          payload: '{"action": "record"}'
```

## Option C: State Feed (NVR → HA)

### C1: MQTT Status Publishing (recommended)

Two publishing channels (see [MQTT Integration — Status Publishing](./mqtt-integration.md#status-publishing)):

- `{prefix}/health/{camera_id}`: health alerts (connection lost/freeze/restored). Requires `mqtt.enabled: true` plus `health.alerts.mqtt: true`.
- `{prefix}/event/{topic}`: recording and camera events (`segment.completed`, `camera.added`, `camera.quality`, `storage.health.changed`). Requires `mqtt.enabled: true` plus `mqtt.status_events: true`.

HA-side example — update a sensor whenever a recording segment completes:

```yaml
mqtt:
  sensor:
    - name: "Front Door last recording"
      state_topic: "mibee/event/segment.completed"
      value_template: "{{ value_json.camera_id }}"
      json_attributes_topic: "mibee/event/segment.completed"
```

> Note: `segment.completed` is one topic for the whole NVR (cameras are distinguished by the `camera_id` field). To filter per camera, use a condition template on `trigger.payload_json.camera_id` in automations.

### C2: REST Polling (no MQTT needed)

For low-frequency values, poll NVR endpoints with HA's RESTful integration. The per-camera stats endpoint returns `recording_count` and `total_size`:

```yaml
restful:
  - resource: "http://192.168.63.30:9090/api/cameras/front-door/stats"
    username: admin
    password: "!secret nvr_password"
    scan_interval: 60
    sensor:
      - name: "Front Door recording count"
        value_template: "{{ value_json.recording_count }}"
      - name: "Front Door storage used"
        value_template: "{{ value_json.total_size }}"
```

`GET /api/health` (public) can be polled for overall status and the `device_id`.

### C3: SSE Event Stream (requires middleware)

The NVR exposes SSE event streams: `GET /api/events` (whole NVR, public, rate-limited to 60 req/min) and `GET /api/cameras/{id}/events` (per camera, auth required). HA has no native SSE consumer — bridge it via Node-RED or a small script into MQTT. If C1's MQTT status publishing is already enabled, this path is usually unnecessary.

## Option D: Low-Latency View (HACS WebRTC Card)

Generic Camera over RTSP has 1–3 s latency. For sub-second latency install the community card ([AlexxIT/WebRTC](https://github.com/AlexxIT/WebRTC), via HACS):

```yaml
type: custom:webrtc-camera
url: rtsp://192.168.63.30:8554/front-door
muted: true
```

The card runs an embedded go2rtc to convert RTSP→WebRTC (it does not consume the NVR's WHEP endpoint directly) — low latency with audio support. H.264/H.265 cameras only, same as Option A.

## Known Limitations & Security Notes

- RTSP output has no authentication by default — anyone on the LAN can pull streams. Either configure `server.rtsp.username/password` or keep the NVR on a trusted network.
- The MQTT trigger's `snapshot` action is not implemented yet (logged and ignored).
- `latest-frame` only works for JPEG-family cameras; H.264/H.265 cameras use the `snapshot` endpoint or the vendor's snapshot URL instead.
- Neither the RTSP output nor the WebRTC card covers MJPEG/JPEG cameras (use Option A').
- There is no app-level push-notification loop from NVR to HA; build notification automations in HA (e.g. triggered by the health topic).

## Next Steps

- [MQTT Integration](./mqtt-integration.md) — full reference for triggers and status publishing
- [Configuration Reference](./configuration.md) — all fields including `server.rtsp`, `mqtt`, `health.alerts`
- [API Overview](./api.md) — REST endpoints and authentication
