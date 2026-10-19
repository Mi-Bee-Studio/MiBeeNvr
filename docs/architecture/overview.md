# Overview: Layered Architecture & Data Paths

MiBee NVR is a single static Go binary (CGO disabled) with an embedded Svelte 5 SPA.
Protocol differences end at the ingest factories; everything downstream of the
`StreamHub` frame bus is protocol-agnostic.

## 1. Layered view

```mermaid
flowchart TB
    subgraph L1["Ingest (8 recorder factories)"]
        direction LR
        F1["rtsp direct"] --- F2["onvif\n(pull / Pull-Point events / connection adoption)"] --- F3["xiaomi cloud cams"] --- F4["gb28181\n(device + cascade)"]
        F5["srt"] --- F6["rtmp ingest"] --- F7["mjpeg http-jpeg"] --- F8["push cameras"]
    end
    subgraph L2["Core"]
        HUB["StreamHub (per-camera frame fan-out)"]
        EVT["Event bus (pub/sub)"]
        ADP["Adaptive gate (motion / audio / external triggers)"]
    end
    subgraph L3["Consumers"]
        REC["recorder (atomic writes / rotation / rolling merge)"]
        SUB["substream.Manager (on-demand, ref-counted)"]
        OUT["egress managers: ws / flv / hls / rtsp / webrtc (WHEP)"]
        VIS["vision pusher (SSE consumption → HTTP upload)"]
    end
    subgraph L4["Cross-cutting"]
        AUTH["auth: BasicAuth+bcrypt / mbv_ API keys / stateless tokens"]
        LIC["license verification"]
        P2P["embedded P2P device client"]
        HEALTH["health monitor + self-remediation"]
        STORAGE["storage: roots / per-camera assignment / migration"]
        CFG["config (atomic save, encrypted secrets)"]
        UI["SPA embedded in the binary"]
    end
    L1 --> HUB
    HUB --> ADP --> REC
    HUB --> OUT
    HUB --> SUB --> OUT
    REC --> EVT --> VIS
    REC --> STORAGE
    L4 -.cross-cuts everything.-> L1 & L2 & L3
```

## 2. The two main data paths

### Recording (with adaptive)

```mermaid
flowchart LR
    CAM["camera AU frames"] --> HUB["StreamHub"]
    HUB --> GATE{"adaptive gate"}
    GATE -- "spike burst / audio / external trigger" --> N["NORMAL segment (full rate)\nGOP flush on TL exit"]
    GATE -- "calm for calm_threshold" --> TL["TIMELAPSE segment\n(compressed frame pacing)"]
    N & TL --> W["atomic write: .mp4 + .vodidx sidecar"]
    W --> RM["rolling merge"]
    RM --> TM["timelapse merge\n(restart catch-up)"]
    TM --> SEG["segment.completed (event bus)"]
    SEG --> SSE["SSE feed → external AI processors"]
    SEG --> CLN["cleanup (skips ai_status=processing)"]
```

### Live streaming (multi-protocol egress + sub-streams)

```mermaid
flowchart LR
    HUB["StreamHub (main)"] --> WS & FLV & HLSQ & RTSPQ & WHEP
    SUBHUB["StreamHub (camid/sub)"] --> WS2["ws?quality=sub"] & FLV2["flv?quality=sub"] & HLS2["hls /stream/sub/"]
    SUBM["substream.Manager"] -- "ref-counted acquire/release\nidle recycle (default 30s)" --> SUBHUB
    WHEP{"webrtc"} -- "H.265 sub → fall back to main\nX-Stream-Quality header" --> SUBHUB
```

See [streaming.md](streaming.md) for the full protocol matrix.

## 3. Service orchestration

`pkg/app` starts ~19 services in dependency order and stops them in reverse.
A startup failure aborts the process, except for services explicitly marked
"log-only" (e.g. the P2P client and the RTSP server when their port is taken).
State lives in SQLite (idempotent, versioned schema migrations); recordings,
events, AI status and audit rows share one database file.

## 4. Deployment forms

```mermaid
flowchart LR
    subgraph ARM["ARM64 SBC (typical)"]
        B["single binary + yaml config\n(CGO=0, cross-compiled)"]
    end
    subgraph NAS["NAS app stores / Docker"]
        D["container with host networking"]
    end
    subgraph DESK["Windows desktop"]
        T["GUI tray build"]
    end
```

- Never compile on the target device — cross-compile from a dev machine.
- The SPA is rebuilt and embedded at build time; a binary refresh must always
  ship a freshly built SPA.
- Windows GUI builds need `-H windowsgui` to avoid a console window.
