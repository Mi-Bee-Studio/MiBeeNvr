## OVERVIEW
Xiaomi camera module — MISS protocol, CS2 P2P transport, cloud auth, ChaCha20 encryption. Implements model.Recorder + model.HLSProvider. Registered via plugin.Register() in init().

## STRUCTURE
```
plugin.go (72)    # Plugin registration — init() → plugin.Register(&XiaomiPlugin{})
recorder.go (677) # XiaomiRecorder — model.Recorder + HLSProvider, codec probing, segment lifecycle
miss.go (231)     # MISS protocol client — login, StartMedia/StopMedia, ReadPacket
cs2.go (552)      # CS2 P2P transport — UDP/TCP, channel mux (CH0/CH2/CH3), punch-through
cloud.go (953)    # Cloud auth — api.io.mi.com, RC4+MD5/SHA signatures, captcha, 2FA
crypto.go (66)    # ChaCha20 encryption, X25519 ECDH key exchange
doc.go (4)        # Package documentation
*_test.go (3)     # recorder/miss/cs2/crypto tests with MISSConn mock
```

## PROTOCOL STACK (bottom-up)
```
Config: xiaomi://<DID> → extractDID() → ResolveMISSURL()
  → Cloud API (cloud.go): RC4+MD5 auth, captcha/2FA, device list → miss:// URL
    → CS2 P2P (cs2.go): UDP/TCP transport, port 32108, NAT punch-through
      → MISS (miss.go): X25519 key exchange, ChaCha20 encryption, login → StartMedia
        → Codec (recorder.go): H264/H265 NALU parsing → MP4 muxer → segment lifecycle
```

## WHERE TO LOOK
| Task | Location | Notes |
|------|----------|-------|
| Add cloud auth flow | cloud.go | SignIn, captcha, 2FA, device list |
| Fix P2P connection | cs2.go | CS2Dial, UDP/TCP modes, punch-through |
| Fix MISS protocol | miss.go | Login, StartMedia, ReadPacket |
| Fix codec handling | recorder.go processH264NALU/processH265NALU | NALU types, SPS/PPS extraction |
| Fix encryption | crypto.go | ChaCha20 encode/decode, X25519 key exchange |
| Fix reconnection | recorder.go run() | Exponential backoff, codec re-probe |
| Add camera model | recorder.go | Model-specific timestamp/quality quirks |
| Fix HLS streaming | recorder.go forwardHLS() | OnHLSFrame callback, 90kHz PTS |
| API endpoints | api/handler.go /api/xiaomi/* | Auth, captcha, verify, devices, sync |

## CONVENTIONS
- **Plugin registration**: init() in plugin.go → plugin.Register(&XiaomiPlugin{}). Blank import in main.go
- **MISSConn interface**: Abstracts transport layer — enables mock testing without real P2P
- **Codec probing**: First video packet determines codec (codecID=4→H264, codecID=5→H265)
- **Annex B parsing**: splitAnnexBNALUs() splits on 00 00 00 01 / 00 00 01 start codes
- **Dual transport**: CS2 supports UDP (seq-based ACKs) and TCP (keepalive) modes
- **Channel multiplexing**: CH0=commands, CH2=media, CH3=writes
- **Cloud regions**: cn/cn2 (China), us (US), de (Germany), ru (Russia), sg (Singapore), i2 (India)

## ENCRYPTION
- **Cloud API**: RC4 stream cipher + MD5/SHA1/SHA256 signatures
- **Key Exchange**: X25519 ECDH via golang.org/x/crypto/nacl/box
- **Media Stream**: ChaCha20 (golang.org/x/crypto/chacha20) with 8-byte random nonce

## ANTI-PATTERNS
- **DO NOT** call StartMedia before successful missLogin — connection not established
- **DO NOT** assume codec from config — always probe from first video packet (codecID)
- **DO NOT** forget ChaCha20 decryption on ReadPacket — raw packets are encrypted
- **DO NOT** hardcode camera model IDs — use constants from cloud.go device list
- **DO NOT** skip X25519 key exchange — shared key is required for MISS login
- **DO NOT** block on CS2 reads — use channels with timeouts (15s read deadline)