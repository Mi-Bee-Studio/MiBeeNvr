# Architecture Guides (contributor docs)

Deep-dive architecture documentation for contributors. These guides describe how the
major subsystems are designed and why. For the user-facing manual, see `docs/zh/` /
`docs/en/`; for a quick product overview, see the repository `README.md`.

| Guide | Scope |
|---|---|
| [overview.md](overview.md) | Layered architecture, the two main data paths (recording & live streaming), service orchestration, deployment forms |
| [recording.md](recording.md) | Recording pipeline, adaptive recording (NORMAL ↔ TIMELAPSE), timelapse merge |
| [streaming.md](streaming.md) | Multi-protocol egress (WS/FLV/HLS/RTSP/WebRTC), on-demand sub-streams, quality negotiation |
| [ingest.md](ingest.md) | The 8 ingest factories, camera auto-discovery, GB28181 device & cascade modes |
| [audio-recording.md](audio-recording.md) | The independent audio recording line and audio-triggered adaptive recording |
| [p2p.md](p2p.md) | Embedded P2P remote access (device side): tokens, session lifecycle, resilience |
| [platform.md](platform.md) | Cross-cutting platform concerns: auth chain, event bus, storage management, config, health, SPA delivery |

All diagrams are [Mermaid](https://mermaid.js.org/) and render on GitHub.

> These guides describe design intent at a point in time; when code and docs
> disagree, the code wins — and a PR fixing the docs is welcome.
