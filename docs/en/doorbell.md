# SIP Doorbell / Intercom

Standard SIP door stations (Doorbird, Akuvox, Hikvision intercoms — anything
that speaks SIP as a UA) ring the NVR directly: press the bell and the web UI
shows an incoming-call popup with the caller, an optional snapshot of the
linked camera, and **answer / reject**. Answering opens a genuine two-way
G.711 intercom (browser mic ↔ doorbell speaker). Deliberately decoupled from
[GB28181](gb28181-guide.md) — no shared numbering or config; the two systems
can run side by side.

Default **off**.

## Mode A — doorbells register to the NVR

The NVR runs a small SIP registrar; point each door station at it:

```yaml
doorbell:
  enabled: true
  listen: ":5070"          # SIP listener (UDP by default)
  realm: "mibee-nvr"
  users:
    - username: "front-door"
      secret: "a-long-random-password"
      name: "Front door"
      camera_id: "cam-xxxx"   # optional: snapshotted on ring
```

On the doorbell's side configure: registrar/NVR address `sip:<nvr-ip>:5070`,
the SIP user (`front-door`), the password, G.711 audio, and — if it supports
it — "call" the NVR on press (any request URI works; the NVR accepts the
INVITE from a registered user).

## Mode B — through a PBX (Asterisk / FreeSWITCH)

If doorbells already register to an existing PBX, the NVR instead registers
to it as one more UA and receives the INVITEs:

```yaml
doorbell:
  enabled: true
  outbound:
    server: "192.168.1.5:5060"   # the PBX
    username: "nvr"
    secret: "pbx-password"
  users:                         # door stations whose From user may ring
    - username: "front-door"
      name: "Front door"
```

Route the doorbells' calls to extension `nvr` on the PBX. In this mode the
PBX is the trust anchor: INVITEs arriving through it are accepted when their
From user matches a configured entry — doorbell secrets are not re-verified
per INVITE (that is how SIP through a PBX works). An INVITE's **source
address must match the `outbound.server` host**; anything else gets a 403.

## Security posture

- **Default off**; the section does nothing until `enabled: true`.
- `lan_only` (default **true**) refuses SIP from non-private source
  addresses outright — a port-forward alone never exposes the server.
- Registrar mode requires **Digest auth** (single-use nonces); wrong
  credentials never register, and an INVITE from an unregistered source is
  refused with 403 — **anonymous INVITE is impossible in mode A**.
- Doorbell and PBX secrets are **encrypted at rest** (same `ENC:` mechanism
  as `mqtt.password`; set `NVR_ENCRYPTION_KEY` to activate).
- Transports: `udp` (default), `tcp`, `tls` (needs `tls_cert`/`tls_key`).

## Using it

When the bell rings, every open web UI shows the popup. **Answer** opens the
talk channel: your mic goes to the doorbell speaker, the doorbell mic plays
in your browser (echo cancellation on by default). **Reject** declines,
**Hang up** ends an answered call. The popup also appears on other clients —
whoever answers first wins; the rest dismiss automatically.

Every ring captures a snapshot of the configured `camera_id` (via the shared
snapshot pipeline) — shown in the popup and kept under
`{storage}/snapshots/<camera>/`.

Ring/answer/reject/end also flow to the event stream (`/api/events`, filter
`doorbell.`) and — with `mqtt.status_events` enabled — to
`{prefix}/event/doorbell.ring` etc. for home-automation reactions.

REST surface (for integrations): `GET /api/doorbell/calls`,
`POST /api/doorbell/calls/{id}/answer|reject|bye`, and the
`/api/doorbell/calls/{id}/talk` WebSocket (binary G.711 frames both ways —
the same contract as the GB28181 talk socket).

## Notes and limits

- Audio is **G.711 A-law/μ-law only** — the codec every SIP intercom
  speaks; INVITEs offering anything else are declined with 488.
- The talk path rides the browser WebSocket (mic → A-law → WS → RTP), the
  same proven pipeline as GB28181 two-way talk, rather than WebRTC; a WHEP
  upgrade can follow if latency demands it.
- Video-in-INVITE (Doorbird sends H.264 RTP alongside audio) is not
  bridged in this version — the audio intercom and the linked camera's
  existing streams cover the use case.
- A ring auto-terminates after `ring_timeout` (default 60s); answered calls
  are capped at 10 minutes.

## Troubleshooting

- **Doorbell won't register** — check it uses Digest auth (not plain), the
  secret matches, and `listen`/firewall allow UDP 5070; the log component
  is `doorbell`.
- **Popup never appears** — the web UI needs the events SSE reachable
  (same origin, authenticated session).
- **Talk is one-way** — some intercoms need `a=sendrecv` forced on (most
  do this by default); check the doorbell's audio direction setting.

## Next steps

- [Events & automation](api/README.md) — the `doorbell.*` event topics
- [GB28181](gb28181-guide.md) — the platform-side protocol for cameras
