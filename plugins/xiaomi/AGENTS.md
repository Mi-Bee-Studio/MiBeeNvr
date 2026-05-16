# MiBee NVR — Xiaomi Camera Plugin

## Overview

Xiaomi proprietary camera integration. MISS protocol over CS2 P2P transport with cloud auth, ChaCha20 encryption. Supports H.264/H.265 recording.

## Structure

```
plugin.go       # Plugin registration — XiaomiPlugin, NewRecorder factory, cloud config
recorder.go     # XiaomiRecorder — MISS connect, codec probe, H264/H265 NALU→MP4, auto-reconnect
miss.go         # MISSClient — protocol client, auth, media start/stop, packet read/decrypt
cs2.go          # CS2 P2P transport — UDP/TCP handshake, data channels, keepalive ping, seq reordering
cloud.go        # Cloud auth — Xiaomi account login, device list, MISS URL resolution
crypto.go       # Encryption — Curve25519 key exchange, ChaCha20 encode/decode
*_test.go       # Per-component tests
```

## Where To Look

| Task | Location | Notes |
|------|----------|-------|
| Fix video capture | `recorder.go` `connectAndRecord()` | Main packet read loop, codec probe, NALU processing |
| Fix reconnection | `recorder.go` `run()` | Re-resolves MISS URL on each attempt, exponential backoff |
| Fix CS2 connection | `cs2.go` `worker()` | Read deadline (1s TCP / 30s UDP), keepalive ping, idle timeout |
| Fix CS2 handshake | `cs2.go` `cs2Handshake()` | UDP→TCP fallback, 5s deadline |
| Fix encryption | `crypto.go` | Curve25519 shared key, ChaCha20 nonce from 8 random bytes |
| Fix cloud auth | `cloud.go` `SignInWithToken()` | Re-authenticates per MISS URL resolution |
| Fix MISS URL resolution | `cloud.go` `ResolveMISSURL()` | Gets device IP, generates keypair, calls miss_get_vendor API |
| Fix login flow | `cloud.go` `SignIn()`/`SignInWithCaptcha()` | Multi-step: captcha, 2FA, token-based |
| Fix NALU parsing | `recorder.go` `splitAnnexBNALUs()` | Start code detection (00 00 00 01 / 00 00 01) |
| Add camera model | `miss.go` model constants | Add to `ModelXxx` consts, update `StartMedia()` quality mapping |

## Conventions

- **Recorder pattern**: Same as built-in recorders — `NewXiaomiRecorder()→Start()→run()→connectAndRecord()→processNALU()`
- **MISS URL resolution**: Every reconnection re-authenticates with cloud and resolves fresh MISS URL (new keypair each time)
- **Codec probe**: First video packet determines codec (H264/H265). `codecOK` flag reset on each new connection
- **CS2 transport**: Two modes — UDP (ACK-based seq reordering, 10-entry push buffer) or TCP (ping keepalive, streaming)
- **CS2 data channels**: Channel 0 = commands, Channel 2 = media (video/audio), Channel 3 = outgoing
- **Encryption**: ChaCha20 with Curve25519 shared key. 8-byte random nonce prepended to ciphertext
- **Model-specific behavior**: `StartMedia()` uses different quality codes per model (C200/C300 use "3", others use "2")
- **Timestamp handling**: Dafang/Xiaofang/LoockV2 use local timestamp; others use packet header timestamp

## Anti-Patterns

- **DO NOT** skip MISS URL re-resolution on reconnect — cloud token/device IP may have changed
- **DO NOT** clear CS2 read deadline after handshake — causes indefinite block when camera stops sending
- **DO NOT** send TCP keepalive only when receiving data — camera silence means ping stops, connection dies
- **DO NOT** assume UDP for all cameras — handshake may negotiate TCP; `isTCP` flag controls behavior
- **DO NOT** forget to reset codec probe state (`codecOK`, `sps`, `pps`, `vps`) on each new connection
- **DO NOT** block on `Pop()` without idle timeout — use `SetReadDeadline` to detect dead connections
