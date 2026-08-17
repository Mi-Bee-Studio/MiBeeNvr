# WHIP Push-In Ingest (WebRTC)

WHIP (WebRTC-HTTP Ingest Protocol) lets a browser, phone, or OBS 30+ **push**
a camera stream into the NVR over WebRTC — H.264 video + Opus audio, zero
transcoding. Unlike SRT/RTMP ingest, the publisher needs no direct network
reachability: ICE/STUN/TURN traversal means a remote contributor can push from
another network without port exposure, frp, or a VPN.

| | SRT / RTMP | WHIP |
|---|---|---|
| Browser as source | ✗ (browsers can't push SRT/RTMP) | ✓ `getUserMedia` |
| OBS built-in client | ✓ (manual URL setup) | ✓ (Settings → Stream → WHIP) |
| Audio | — | ✓ Opus recorded + live |
| NAT traversal | direct connection required | ✓ ICE/STUN/TURN |

## Enable

```yaml
whip:
  enabled: true
```

The endpoint rides the main HTTP listener — no extra port.

## Add a push camera

Cameras → Add → protocol **WHIP (WebRTC push)**, set a **stream key** (e.g.
`door-cam`). The form shows the endpoint URL:

```
http://<nvr-ip>:9090/whip/door-cam
```

The stream key is the credential — same threat model as RTMP push keys and
SRT `streamid`. Anyone who knows the key can publish; anyone who doesn't
gets a 404.

## Publish

**OBS 30+**: Settings → Stream → Service: *WHIP* → Server:
`http://<nvr-ip>:9090/whip/door-cam` → Start Streaming. Video must be H.264;
audio is Opus.

**Browser / phone**: any WHIP client library pointed at the same URL, using
`getUserMedia`. The recorded/live pipeline is identical to OBS.

While a publisher is connected the camera behaves like any other: segments
are recorded (H.264 + Opus in MP4), live preview works across all protocols,
and the push-out relay/retention settings apply. One publisher per camera —
a second concurrent publisher is rejected with 409.

## Notes

- **Remote publishers (cross-network)**: for WAN access put the NVR behind
  TLS (see `remote-access.md`) and configure `streaming.webrtc.ice_servers`
  (STUN/TURN) — the same infrastructure WHEP viewers use.
- **H.264 only**, matching WHEP egress (browser WebRTC H.265 support is
  still fragmented).
- **Idle publishers** are reaped: a session that never sends RTP within 30s,
  or stalls for 60s, is torn down automatically.
- The recorder is the same `IngestRecorder` the SRT/RTMP listeners use —
  segment rotation, SPS/PPS handling, and the StreamHub fan-out are shared.

## API

- `POST /whip/{streamKey}` — SDP offer (Content-Type `application/sdp`) →
  `201 Created` + SDP answer + `Location` header (session URL)
- `DELETE /whip/{streamKey}/{session}` — tear the session down
- `GET /api/capabilities` → `ingest.whip.enabled` reflects the config
