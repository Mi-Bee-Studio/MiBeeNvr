# Remote Access

MiBee NVR serves on the LAN by default. This guide covers how to make the NVR reachable from **external networks (4G / remote WiFi / cross-region)**.

> **This page covers only the minimal working configuration.** For advanced features of each tool (subnet routing, ACLs, exit nodes, custom certs), consult the tool's own documentation.

---

## Scenario Comparison

| Method | Best for | Public IP? | Complexity | WebRTC | Notes |
|--------|----------|-----------|------------|--------|-------|
| Port forwarding / UPnP | Home with static public IP | ✅ Required | Low | ✅ Works | Simplest, but large attack surface — expose only the TLS port |
| **Tailscale** | Personal / small team | ❌ No | Very low | ⚠️ See below | Recommended: zero-config mesh VPN, UDP hole-punching |
| **Cloudflare Tunnel** | Sharing with others, no public IP | ❌ No | Medium | ❌ TCP only | Good for HLS, not WebRTC |
| Self-hosted WireGuard | Advanced users wanting full control | ✅ Required | High | ✅ Works | Best performance, higher setup bar |
| frp / ngrok | Temporary debugging, NAT traversal | Depends | Medium | Depends on mode | Not covered here — look them up |

---

## WebRTC Cross-Network Access (Must Read)

WebRTC uses **UDP** by default and needs ICE servers (STUN/TURN) to traverse NAT. MiBee NVR's WebRTC (WHEP) only gathers mDNS host candidates in the default config — **LAN-only**.

To watch video over WebRTC from outside the LAN, configure ICE servers in `mibee-nvr.yaml`:

```yaml
streaming:
  webrtc:
    enabled: true
    ice_servers:
      - urls: ["stun:stun.l.google.com:19302"]            # Public STUN (free)
      - urls: ["turn:turn.example.com:3478?transport=udp"] # TURN relay (required for symmetric NAT)
        username: "user"
        credential: "pass"
```

- **STUN**: any free public server works for most NAT types (cone NAT).
- **TURN**: required for symmetric NAT (common with carrier-grade NAT). TURN traffic consumes server bandwidth — consider self-hosting [coturn](https://github.com/coturn/coturn).
- **TCP-only tunnels (e.g. Cloudflare Tunnel) cannot carry WebRTC UDP**: in that case select HLS manually in the camera player (the protocol switcher defaults to Auto and degrades to HTTP transports when WebRTC is unavailable) — HLS runs over HTTP/TCP and is tunnel-friendly.

---

## Option A: Tailscale (Recommended: Personal Use)

[Tailscale](https://tailscale.com/) is a zero-config mesh VPN built on WireGuard. Once the NVR joins your tailnet, it's reachable at its assigned `100.x.x.x` address from anywhere — **no public IP, no port forwarding needed**.

### Docker Compose Example

Add a tailscale sidecar next to the NVR in your `docker-compose.yml`:

```yaml
services:
  mibee-nvr:
    image: ghcr.io/mi-bee-studio/mibeenvr:latest
    volumes:
      - ./data:/data
    network_mode: "service:tailscale"   # Key: share the tailscale network stack

  tailscale:
    image: tailscale/tailscale:latest
    hostname: mibee-nvr                  # Device name in your tailnet
    environment:
      - TS_AUTHKEY=tskey-auth-xxxxx      # Get from https://login.tailscale.com/admin/settings/keys
      - TS_STATE_DIR=/var/lib/tailscale
    volumes:
      - tailscale-state:/var/lib/tailscale
    cap_add:
      - NET_ADMIN
      - SYS_MODULE

volumes:
  tailscale-state:
```

After starting, the `mibee-nvr` device appears in the Tailscale admin console. Reach it from outside at `http://mibee-nvr:9090` (or its assigned IP).

### Notes

- **WebRTC + Tailscale**: Tailscale's UDP hole-punching usually lets WebRTC connect directly without STUN. But if all tailnet nodes sit behind symmetric NAT, you still need TURN.
- **Auth key expiry**: free-tier auth keys expire after 90 days by default. For production, use a **preauthorized** + reusable key, or set the key to non-expiring in the admin console.
- **HTTPS**: browser WebRTC (WHEP) needs a Secure Context. Tailscale ships HTTPS via MagicDNS + certs — enable it in the admin console to use `https://mibee-nvr.tail-xxxx.ts.net`.

---

## Option B: Cloudflare Tunnel (Recommended: No Public IP, Sharing)

[Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/) reverse-proxies a local service to Cloudflare's edge via `cloudflared` — **no public IP, no open ports, HTTPS and DDoS protection included**. Ideal for sharing the NVR with non-technical users (they just visit a domain).

### Docker Compose Example

```yaml
services:
  mibee-nvr:
    image: ghcr.io/mi-bee-studio/mibeenvr:latest
    volumes:
      - ./data:/data
    ports:
      - "127.0.0.1:9090:9090"            # Bind localhost only — cloudflared proxies it

  cloudflared:
    image: cloudflare/cloudflared:latest
    command: tunnel run
    environment:
      - TUNNEL_TOKEN=eyJhZxxxxx           # Create a tunnel in Cloudflare Zero Trust dashboard
    depends_on:
      - mibee-nvr
```

In the Cloudflare Zero Trust dashboard, point a subdomain (e.g. `nvr.example.com`) of the tunnel to `http://mibee-nvr:9090`.

### ⚠️ Important Limitation: WebRTC Does Not Work

Cloudflare Tunnel is **TCP-only** and cannot relay WebRTC's UDP media. Under this deployment:

- ❌ WebRTC (WHEP) does not work
- ✅ HLS / LL-HLS / HTTP-FLV / WebSocket streams (over HTTP) work normally
- ✅ Playback, management API, and all non-WebRTC features work normally

**Recommendation**: select HLS in the camera player when deploying behind Cloudflare
Tunnel (the switcher defaults to Auto and degrades to HTTP transports automatically).
The `streaming.default_protocol` setting was removed in 0.11.0; stale values are
silently ignored.

### Notes

- **Auth**: Cloudflare Access Policies (zero-trust login) can be layered on top of the tunnel for stronger auth than BasicAuth.
- **Bandwidth**: Cloudflare's free tier has bandwidth limits. Sustained multi-camera video may trip the ToS — evaluate a paid plan for production.

---

## Other Options

These are not covered in detail here — see their official docs:

- **WireGuard / OpenVPN**: self-hosted VPN, requires a public IP or VPS, best performance but higher setup bar.
- **frp / ngrok**: NAT traversal, good for temporary debugging. frp needs a self-hosted frps; ngrok's free tier is limited.
- **ZeroTier**: mesh VPN similar to Tailscale, supports self-hosted controllers.

---

## HLS playback on iOS / AVPlayer

When an HLS playlist is requested with a session token, the NVR also sets a
scoped `mbs_session` cookie so that players which cannot set per-request headers
(iOS AVPlayer, some native players) can fetch media segments. In remote-access
setups, simply open the Web UI in Safari or paste the HLS URL into a player such
as infuse — no extra configuration needed. Versions before 0.11.0 returned 401
on protected HLS segments in iOS players; upgrading fixes it.

## Limitations

Each approach above requires the user to **maintain a third-party account, client, or server**, with its own trade-offs:

- Tailscale's free tier caps device count (100)
- Cloudflare Tunnel does not support WebRTC UDP
- Self-hosted VPN requires a public IP and ops effort

If these trade-offs are unacceptable for your use case, watch for future MiBee releases that improve the remote-access experience.

---

## Next Steps

- After configuring ICE servers, verify STUN/TURN reachability at the [WebRTC trickle-ICE test page](https://webrtc.github.io/samples/src/content/peerconnection/trickle-ice/).
- After deployment, change the default admin password immediately and enable TLS (`server.tls_listen`).
