# MiBee NVR for Home Assistant

[中文说明](./README.zh-CN.md)

Custom integration for [Home Assistant](https://www.home-assistant.io/), bundled with
[MiBee NVR](https://github.com/Mi-Bee-Studio/MiBeeNvr) — the lightweight, self-hosted
network video recorder. It discovers your NVR on the local network via mDNS and creates
a camera entity, connectivity/recording sensors, and a recording switch for every
configured camera — no YAML required.

## Features

- **Zero-config discovery** — picks up the NVR's `_mibee-nvr._tcp` mDNS
  advertisement; you only enter the web login credentials.
- **Camera entities**
  - H.264/H.265 cameras → live streaming via the NVR's built-in RTSP output
    (Home Assistant `stream`, 1–3 s latency).
  - MJPEG/JPEG cameras (e.g. ESP32 boards) → HA-native MJPEG camera via the
    NVR's `stream.mjpeg` passthrough.
- **Binary sensors per camera** — *Connected* (connectivity device class) and
  *Recording* (segment writer active).
- **Switch per camera** — toggles the camera's `recording_enabled` flag.
- Device registry grouping: one hub device (the NVR) + one sub-device per
  camera.

## Installation

Manual copy — the integration lives in a subdirectory of the NVR repository,
so HACS cannot install it directly from there:

```bash
git clone https://github.com/Mi-Bee-Studio/MiBeeNvr.git /tmp/mibee-nvr
mkdir -p <HA-config-dir>/custom_components
cp -r /tmp/mibee-nvr/deploy/home-assistant/custom_components/mibee_nvr \
      <HA-config-dir>/custom_components/
```

Restart Home Assistant afterwards. To update, `git pull` in the clone and copy
again.

## Setup

Settings → Devices & Services → Add Integration → **MiBee NVR**.

- NVRs on the LAN are auto-discovered; manual host/port entry also works.
- Credentials are the NVR **web UI** login (BasicAuth).
- Options (configure on the entry): RTSP output port (default `8554`) and
  polling interval (default `30 s`).

## Requirements on the NVR side

- MiBee NVR **v0.12.0+** with the built-in RTSP output enabled (default) for
  H.264/H.265 streaming.
- `latest-frame` thumbnails work out of the box for JPEG-family cameras; for
  H.264/H.265 cameras they additionally require the optional FFmpeg on the NVR
  host (see the [NVR Home Assistant doc](../../docs/en/home-assistant.md)).

## Alternatives

The NVR also integrates without any custom code — RTSP generic cameras, MQTT
triggers and MQTT status publishing are all documented in the
[NVR manual](../../docs/en/home-assistant.md).
Use this integration when you want discovery, per-camera entities, and push
state without maintaining YAML.

## Development

```bash
cd deploy/home-assistant
python3 -m venv .venv && . .venv/bin/activate
pip install -r requirements_test.txt
ruff check custom_components tests
ruff format --check custom_components tests
pytest -v
```

The suite pins `homeassistant==2025.1.4` and includes an import-validation
test, so the component is exercised against real Home Assistant, not just
stubs. CI runs the same steps in the `home-assistant` job.

## License

Part of the MiBeeNvr repository — AGPL-3.0-only, see the
[repository license](../../LICENSE).
