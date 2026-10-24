# Documentation Index

> Every page exists in both English (`docs/en/`) and Chinese (`docs/zh/`), always under the same filename. New to MiBee NVR? Follow the ★ pages in order.

## Getting Started

| Page | What it covers |
|---|---|
| ★ [Product Introduction](intro.md) | What MiBee NVR is, in one page |
| ★ [Feature Overview](features.md) | Everything it can do, each section linking to its guide |
| ★ [Getting Started](getting-started.md) | Install, first-run setup, add your first camera (with a first-steps troubleshooting section) |
| [Setup Wizard](wizard.md) | The first-boot wizard: account, language, storage in one page |
| [Web UI Tour](web-ui.md) | The dashboard, live view, and recordings pages |
| [Scenario Playbooks](scenarios.md) | Six "zero to working" paths (NAS, Raspberry Pi, remote access, …) |
| [Comparison Notes](comparison.md) | How MiBee NVR's scope differs from MediaMTX / Frigate / Shinobi |
| [Desktop Edition](desktop.md) | Windows / macOS desktop installers and trust model |

## Installation & Deployment

| Page | What it covers |
|---|---|
| [Deployment Overview](deployment.md) | systemd, reverse proxy, cross-compile, and links to every platform guide |
| [Linux Packages](install-linux.md) | Native deb / rpm install, upgrade, removal |
| [Docker Install](install-docker.md) | Compose setup, volumes, upgrades |
| [NAS Install](install-nas.md) | Choosing the NAS path for your model |
| [unRAID](deployment-unraid.md) · [fnOS](deployment-fnos.md) · [iStoreOS](deployment-istoreos.md) · [Synology](deployment-synology.md) · [QNAP](deployment-qnap.md) · [ZSpace](deployment-zspace.md) | Per-platform guides |
| [Docker Auto-update](deployment-autoupdate.md) | Watchtower auto-updates and rollback |
| [Raspberry Pi](raspberrypi.md) | Pi-specific setup and tuning |
| [MediaMTX Guide](mediamtx-guide.md) | CSI cameras (Pi Camera) via MediaMTX |

## Camera Onboarding

| Page | What it covers |
|---|---|
| [Camera Guide](camera-guide.md) | Camera setup, brand compatibility, troubleshooting |
| [ONVIF Guide](onvif-guide.md) | ONVIF setup, PTZ control, imaging |
| [ONVIF Discovery](onvif-discovery.md) | Auto-discovery, plug-and-play enrollment, IP self-healing |
| [GB/T 28181](gb28181-guide.md) | National-standard SIP platform (Hikvision/Dahua/Uniview devices) |
| [Xiaomi Setup](xiaomi-setup.md) | Xiaomi CS2 + legacy TUTK cloud cameras |
| [SRT / RTMP Ingest](srt-rtmp.md) | Cross-network push-in cameras |
| [WHIP Ingest](whip-ingest.md) | WebRTC publishing from browsers, phones, OBS |
| [Sub-streams](sub-stream.md) | Recording/viewing the low-resolution stream |
| [SIP Doorbell / Intercom](doorbell.md) | Standard SIP door stations ringing the NVR |

## Recording & Storage

| Page | What it covers |
|---|---|
| [Recording & Playback](recording-playback.md) | Segments, timelines, continuous playback, VOD scrubbing |
| [Adaptive Recording](adaptive-recording.md) | Timelapse baseline that resumes full rate on motion/audio |
| [Timelapse](timelapse.md) | Periodic timelapse recording and merging |
| [Audio](audio.md) | Audio capture (AAC / G.711 / Opus), live preview |
| [Standalone Audio Sources](audio-sources.md) | Enrolling audio-only sources like network microphones |
| [Storage Management](storage-management.md) | Multi-disk roots, per-camera storage, retention |
| [Storage Optimization](storage-optimization.md) | Disk tuning, fsync tiers, merge policies |
| [Object-Storage Offload](storage-offload.md) | S3-compatible cold backup, local eviction, presigned playback |

## Streaming & Remote Access

| Page | What it covers |
|---|---|
| [Protocol Auto-Selection](streaming-protocol-selection.md) | How the grid picks the best protocol per camera, runtime fallback |
| [Relay Guide](relay-guide.md) | Pushing streams out to RTMP platforms |
| [Remote Access](remote-access.md) | Tailscale / Cloudflare Tunnel / ICE-STUN-TURN configuration |
| [DLNA Media Server](dlna.md) | TVs and network players discovering the NVR via SSDP |

## Smart Features & Integrations

| Page | What it covers |
|---|---|
| [AI Detection Tuning](ai-detection-tuning.md) | Browser-side YOLO detection: sensitivity, classes, performance |
| [MQTT Integration](mqtt-integration.md) | Trigger recording from smart-home events |
| [Webhook Integration](webhook-integration.md) | HMAC-signed trigger endpoint |
| [Home Assistant](home-assistant.md) | HA camera/rest/sensor integration |
| [WebDAV / FTP](webdav-ftp.md) | File access over WebDAV and FTP — comparison and quick setup |
| [WebDAV Integration](webdav-integration.md) | The built-in WebDAV server in depth |
| [FTP Integration](ftp-integration.md) | The built-in FTP server in depth |
| [Transcoding](transcoding.md) | FFmpeg transcoding setup |
| [Docker HW Transcoding](docker-hardware-transcoding.md) | VAAPI / NVENC / V4L2 device passthrough |

## Operations & Reference

| Page | What it covers |
|---|---|
| [Configuration Reference](configuration.md) | Every config key, in full |
| [Config Cheat Sheet](config.md) | Top-level keys and where to change settings |
| [CLI Reference](cli.md) | All subcommands and flags |
| [API Reference](api/README.md) | REST API, endpoint by endpoint |
| [Upgrade Guide](upgrade-guide.md) | Breaking changes and migration steps per version |
| [Troubleshooting](troubleshooting.md) | Common issues and solutions |
| [Performance Tuning](performance.md) | Memory tiers, GOMEMLIMIT, background IO budget |
| [Performance Benchmark](performance-benchmark.md) | The 2026-09 self-test report: capacity and resource curves across three hardware tiers |
| [Prometheus Metrics](metrics.md) | Metric reference with types, labels, examples |
| [Observability](observability.md) | Logs, health, and event streams |

## Developer & Internals

| Page | What it covers |
|---|---|
| [Architecture Overview](architecture.md) | Package layout and data flow |
| [Concurrency Architecture](concurrency-architecture.md) | Threading model, synchronization primitives, and the decisions behind them |
| [WASM Player Design](wasm-player-design.md) | The in-browser H.265 player design |
| [Storage Layer Research](storage-research.md) | Historical research doc from the v0.9.0 storage overhaul |
| [Known Issues (deep dives)](known-issues/ai-logit-false-positives.md) | Root-cause analyses: [AI logit false positives](known-issues/ai-logit-false-positives.md) · [ONNX gzip trailer](known-issues/ai-onnx-gzip-trailer.md) · [H.265 multi-camera](known-issues/h265-multicam.md) |
