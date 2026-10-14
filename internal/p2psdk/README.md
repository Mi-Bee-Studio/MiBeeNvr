# MiBee P2P Go SDK

Connect devices and clients over WebRTC with a MiBee P2P signaling server.
Pure Go (CGO-free), built on [pion/webrtc](https://github.com/pion/webrtc).

Protocol version: **1.0.0** (wire-compatible with MiBee server `internal/protocol`
v1). See [`docs/en/protocol/v1.md`](../../docs/en/protocol/v1.md) for the full
wire spec.

## Install

```bash
(vendored internally — import from the NVR module)
```

The SDK is a separate Go module (`sdk/go/go.mod`); it does not pull in the
server's dependencies.

## Quick start

A **device** registers a tunnel service; a **client** dials it.

```go
package main

import (
    "context"
    "log/slog"
    "os"

    peer "github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk"
    "github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/tunnel"
)

func main() {
    logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

    // ── Device: accept inbound tunnels to a local service ───────────────
    sess, err := peer.Dial(context.Background(), peer.SignalingConfig{
        ServerURL: "wss://signal.example.com:8443/signal",
        JWT:       os.Getenv("MIBEE_JWT"),       // OIDC JWT, aud "p2p-signaling"
        ProductID: "default",
        PeerID:    "my-device",
        Role:      peer.RoleDevice,
        Logger:    logger,
    })
    if err != nil {
        logger.Error("dial failed", "error", err); os.Exit(1)
    }
    defer sess.Close()

    sess.OnDataChannel = func(dc peer.DataChannel) {
        tun := tunnel.New(peer.NewDataChannelTransport(dc))
        tun.RegisterService("http", "127.0.0.1:8080") // local HTTP server
        go tun.Serve()
    }

    // ── Client: dial a device's service over the tunnel ─────────────────
    // (in a separate process; shown here for API reference)
    //
    //   clientSess.OnDataChannel = func(dc peer.DataChannel) {
    //       tun := tunnel.New(peer.NewDataChannelTransport(dc))
    //       conn, _ := tun.Dial("http")
    //       conn.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"))
    //       // read response from conn...
    //   }
    //   clientSess.Connect(ctx, "my-device")

    select {} // block
}
```

See [`demo/`](../../demo/) for complete runnable device + client binaries.

## Configuration

`SignalingConfig` fields:

| Field | Required | Description |
|---|---|---|
| `ServerURL` | yes | WebSocket URL, e.g. `wss://host:8443/signal` |
| `JWT` | yes | OIDC JWT; audience must be `p2p-signaling` |
| `ProductID` | yes | Tenant/product namespace for isolation |
| `PeerID` | yes | Unique peer identifier within the product |
| `Role` | yes | `peer.RoleDevice` (`"device"`) or `peer.RoleClient` (`"client"`) |
| `Logger` | no | `*slog.Logger`; defaults to a discard logger |
| `TLSConfig` | no | Custom TLS config for the WebSocket connection |
| `HandshakeTimeout` | no | WS handshake + hello/welcome deadline (default 15s) |
| `ReadTimeout` | no | Max time between WS reads (default 30s) |
| `TransportTimeout` | no | ICE-wait before relay fallback (default 15s) |
| `OnDisconnect` | no | Called when the signaling link drops |
| `OnReconnect` | no | Called after the link recovers |

## Callbacks

Set these on the `Session` (before `Connect` for the client side):

| Callback | When | Payload |
|---|---|---|
| `OnDataChannel` | A P2P DataChannel opens | `DataChannel` (Label, OnMessage, Send, Close) |
| `OnSession` | A P2P session is established | — |
| `OnReject` | The server rejects a connect attempt | `(RejectCode, message)` |
| `OnError` | The server sends an error message | `(code, message)` |
| `OnStats` | Periodic ICE/DataChannel stats (5s) | `StatsSnapshot` |

## Typed errors

Branch on failure cause via `errors.Is` — no string matching:

```go
sess, err := peer.Dial(ctx, cfg)
switch {
case errors.Is(err, peer.ErrInvalidConfig):
    // fix your SignalingConfig
case errors.Is(err, peer.ErrAuthFailed):
    // JWT rejected — refresh credentials
case errors.Is(err, peer.ErrProtocolMismatch):
    // server protocol version unsupported
}
```

Sentinels: `ErrInvalidConfig`, `ErrAuthFailed`, `ErrSignalingDisconnected`,
`ErrICETimeout`, `ErrRelayUnavailable`, `ErrSessionNotFound`,
`ErrProtocolMismatch`.

## Tunnel subpackage

[`tunnel`](./tunnel) multiplexes stream-oriented connections (like `net.Conn`)
over a single P2P DataChannel, with fragmentation (>16 KiB payloads),
backpressure, and concurrent streams.

```go
tun := tunnel.New(peer.NewDataChannelTransport(dc))
tun.RegisterService("ssh", "127.0.0.1:22")
go tun.Serve()
// On the client side:
conn, _ := tun.Dial("ssh") // returns a net.Conn
```

## Relay subpackage

[`relay`](./relay) is the DERP-like fallback transport. The SDK connects to it
automatically when the server advertises one (`Welcome.relay_url`); if direct
ICE fails within `TransportTimeout`, traffic flows over the relay. You rarely
interact with it directly.

## Protocol

JSON messages over WebSocket. See the
[protocol reference](../../docs/en/protocol/v1.md) for the full message catalog
and sequence diagrams.
