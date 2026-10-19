# Ingest Protocols, Discovery & GB28181

Eight ingest factories feed one frame bus. "Protocol differences stay inside the
factory; nothing downstream of the hub knows about protocols" is the layering
rule of this area.

## 1. Factory overview

```mermaid
flowchart LR
    subgraph FACTORIES["recorder factories (internal/*)"]
        RTSP["rtsp direct"]
        ONVIF["onvif (service-facade client)\npull + Pull-Point events + connection adoption"]
        XIAOMI["xiaomi cloud cameras"]
        GBD["gb28181\nSIP + PS/RTP demux → hub"]
        SRTQ["srt ingest"]
        RTMPQ["rtmp ingest :1935"]
        MJPEG["mjpeg http-jpeg polling\n(backoff floor ≥5s)"]
    end
    FACTORIES --> HUB["StreamHub"]
    ONVIF -. "MotionAlarm → adaptive trigger" .-> ADP["adaptive gate"]
    GBD -. "snapshot sessions / manual record" .-> EVT["event bus"]
```

Notes:

- ONVIF: `GetServices` resolves the media XAddr (ver10 → ver20, stale host
  rewrite); a successful probe connection can be **adopted** by the recorder
  (no duplicate dial). Passive WS-Discovery Hello listening filters non-ONVIF
  signals so Windows/WSD hosts never become zombie cameras.
- Camera grouping (schema v36–v38): a group registry with ordering, drag & drop
  moves, inline renames — a pure UI-organization field that recorders never read.
- Sub-stream capable cameras feed the on-demand layer (see
  [streaming.md](streaming.md)).

## 2. Auto-discovery & IP self-healing

```mermaid
flowchart LR
    WS["WS-Discovery active probing\n(5min cycle)"] --> DB[(cameras table)]
    HELLO["passive Hello listener"] --> FILT{"ONVIF signal?"} -- yes --> DB
    FILT -- no --> X["discard"]
    DB --> HEAL["IP self-healing: re-resolve / re-probe"]
    ENR["enrich: GetDeviceInformation\nGetProfiles / StreamUri / SnapshotUri"] --> DB
```

## 3. GB28181: device mode and cascade mode

```mermaid
flowchart TB
    subgraph DOWN["device side (server mode)"]
        D1["GB cameras\nDigest register → INVITE → RTP"]
    end
    subgraph SIP["SIP server"]
        REG["register / keepalive"]
        CAT["catalog subscription"]
        SS["snapshot session manager\n(terminal states, TTL sweeper)"]
        INV["INVITE sessions (on-demand; optional cascade sub-stream)"]
    end
    subgraph UP["cascade uplink (client mode)"]
        CL["register to the upper platform (TCP transport)\nreport catalog → upper platform INVITEs"]
    end
    D1 --> SIP
    SIP --> PSMUX["psmux → RTP → jitter buffer → PS demuxer → hub"]
    SIP --> CL
```

- The media plane uses TCP transport for cascade (UDP lost catalog updates in
  the field); archived cameras are filtered out of the cascade catalog
  (live DB rows only, fail-open).
- For media-plane corruption triage (four independent root-cause families:
  muxer races, marker truncation, UDP burst loss, half-drained buffers), see
  the troubleshooting methodology kept alongside the PR history of #440–#445.
