# GB/T 28181 Guide for MiBee NVR

This guide covers GB/T 28181 (Chinese national video surveillance standard) integration with MiBee NVR, including SIP platform configuration, device enrollment, PTZ control, playback, and troubleshooting.

## What is GB/T 28181?

GB/T 28181 is a Chinese national standard for video surveillance network systems, defining how IP cameras, NVRs, and platforms communicate over SIP and RTP/PS. MiBee NVR implements the **platform role** (UAS — answering side), which means:

- Devices **REGISTER** with the NVR over SIP (UDP port 5060 by default)
- Devices send **Keepalive** messages to maintain their online status
- The NVR **INVITEs** a channel to pull an RTP/PS media stream (pull model)
- The NVR demuxes MPEG-PS into H.264/H.265 NALUs and feeds them to StreamHub

Supported camera brands include Hikvision, Dahua, Uniview, and other GB28181-compliant manufacturers.

## Quick Start

### Step 1: Enable GB28181 Server

Open `mibee-nvr.yaml` and add the GB28181 server configuration:

```yaml
gb28181:
  enabled: true
  sip_listen: ":5060"
  server_id: "34020000002000000001"
  realm: "3402000000"
  password: "yourpassword"
  port_range: "30000-30050"
  heartbeat_interval: "60s"
  catalog_interval: "30m"
  tcp_mode: false
  tcp_framing: "auto"
  allowed_device_ids: []
```

**Key parameters**:
- `server_id`: Your NVR's 20-digit GB/T 28181 serial code (format: `34020000002000000001`)
- `realm`: SIP digest-auth realm (typically your 10-digit area code, e.g., `3402000000`)
- `password`: Secret for SIP digest authentication (encrypt via `mibee-nvr encrypt-config`)
- `port_range`: RTP media port pool, format `"start-end"` (default `"30000-30050"`)
- `tcp_mode`: Force TCP-passive mode for devices behind NAT (default `false`, UDP)
- `tcp_framing`: TCP framing when `tcp_mode=true` — `"rfc4571"`, `"0x24"`, or `"auto"`

### Step 2: Configure Your Camera

On your GB28181 camera (Hikvision, Dahua, etc.), configure the SIP platform:

**Hikvision example** (via web UI):
- Navigate to **Network → Advanced Platform Access**
- Set **Server Address** to your NVR's IP address
- Set **Server Port** to `5060`
- Set **Device ID** to your camera's 20-digit code (e.g., `34020000001320000001`)
- Set **Password** to match your NVR's GB28181 password
- Enable **Platform Access**

**Dahua example** (via web UI):
- Navigate to **Network → TCP/IP → 28181**
- Set **Server IP** to your NVR's IP address
- Set **Server Port** to `5060`
- Set **Device ID** to your camera's 20-digit code
- Set **Device Domain** to match your NVR's realm (e.g., `3402000000`)
- Set **Password** to match your NVR's GB28181 password
- Enable **28181**

### Step 3: Start the NVR

```bash
./mibee-nvr -config mibee-nvr.yaml
```

The GB28181 server will listen on UDP port 5060. Your camera should REGISTER within a few seconds.

### Step 4: View Registered Devices

Open the MiBee NVR web UI and navigate to **GB28181** in the sidebar. You should see:

- **Devices**: Registered GB28181 devices with online/offline status
- **Channels**: Each device's video channels (typically channel 1 = main stream, channel 2 = sub-stream)
- **PTZ**: PTZ control pad (if the channel supports PTZ)

### Step 5: Add a Camera to Record

Create a camera entry in your config that maps to a GB28181 channel:

```yaml
cameras:
  - id: "hikvision-front-door"
    name: "Hikvision Front Door"
    protocol: "gb28181"
    gb28181:
      device_id: "34020000001320000001"
      channel_id: "34020000001320000001"
      manufacturer: "Hikvision"
    recording_enabled: true
    enabled: true
```

The NVR will:
- Auto-detect the codec (H.264 or H.265) from the PS stream
- INVITE the channel when the camera starts recording
- Demux the MPEG-PS stream into NALUs
- Feed the video to StreamHub for recording and live streaming (HLS, WebRTC, FLV, etc.)

## Configuration Reference

### Server Configuration

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `enabled` | bool | `false` | Enable GB28181 server |
| `sip_listen` | string | `":5060"` | SIP UDP/TCP listen address |
| `server_id` | string | (required) | NVR's 20-digit GB/T 28181 serial code |
| `realm` | string | (required) | SIP digest-auth realm (10-digit area code) |
| `password` | string | (required) | SIP digest-auth secret (encrypted) |
| `port_range` | string | `"30000-30050"` | RTP media port pool (`"start-end"`) |
| `heartbeat_interval` | string | `"60s"` | Device keepalive interval |
| `catalog_interval` | string | `"30m"` | Catalog refresh interval |
| `tcp_mode` | bool | `false` | Force TCP-passive mode for NAT traversal |
| `tcp_framing` | string | `"auto"` | TCP framing: `"rfc4571"`, `"0x24"`, or `"auto"` |
| `allowed_device_ids` | `[]string` | `[]` | Restrict registration to specific device IDs (empty = allow all) |

### Camera Configuration

| Parameter | Type | Description |
|-----------|------|-------------|
| `protocol` | string | Must be `"gb28181"` |
| `gb28181.device_id` | string | Camera's 20-digit GB/T 28181 device code |
| `gb28181.channel_id` | string | Camera's 20-digit GB/T 28181 channel code |
| `gb28181.manufacturer` | string | Optional manufacturer name (e.g., `"Hikvision"`) |

## PTZ Control

GB28181 PTZ control works via SIP MESSAGE with MANSCDP DeviceControl commands.

### Supported Directions

- `up`, `down`, `left`, `right` — Pan/tilt movement
- `up-left`, `up-right`, `down-left`, `down-right` — Diagonal movement
- `zoom-in`, `zoom-out` — Zoom (requires `PTZType=2`)
- `stop` — Stop all movement

### PTZ Type

Channels report `PTZType` from the catalog:
- `0` — No PTZ support
- `1` — Pan/tilt only
- `2` — Pan/tilt + zoom

### Web UI Control

On the GB28181 devices page, click the PTZ pad for a channel with `PTZType > 0`:
- Hold a direction button to move at speed 128
- Release to stop
- Click the center stop button to halt movement
- Zoom in/out buttons appear for `PTZType=2`

### API Control

```bash
curl -X POST http://localhost:9090/api/gb28181/channels/34020000001320000001/ptz \
  -H "Content-Type: application/json" \
  -H "Authorization: Basic base64(username:password)" \
  -d '{
    "direction": "up",
    "speed": 128
  }'
```

Response:
```json
{
  "status": "ptz_sent",
  "channel_id": "34020000001320000001",
  "direction": "up",
  "speed": 128
}
```

## Playback

GB28181 supports historical playback via INVITE with `PlayMode=PlayBack` and `StartTime`/`EndTime`. This feature is not yet implemented in the MiBee NVR web UI, but you can trigger it via the API:

```bash
curl -X POST http://localhost:9090/api/gb28181/channels/34020000001320000001/invite \
  -H "Content-Type: application/json" \
  -H "Authorization: Basic base64(username:password)" \
  -d '{
    "playback": {
      "start_time": "2026-01-01T00:00:00Z",
      "end_time": "2026-01-01T01:00:00Z"
    }
  }'
```

This is a placeholder for future UI work. The underlying INVITE/BYE infrastructure already supports playback SDP negotiation.

## Troubleshooting

### Device Does Not Register

**Symptom**: Device does not appear in the GB28181 devices list.

**Solutions**:
1. **Check SIP port**: Ensure `sip_listen` is listening on `:5060` (or your configured port):
   ```bash
   sudo netstat -ulnp | grep 5060
   sudo ss -ulnp | grep 5060
   ```

2. **Check firewall**: Allow UDP port 5060:
   ```bash
   sudo ufw allow 5060/udp
   # or
   sudo iptables -A INPUT -p udp --dport 5060 -j ACCEPT
   ```

3. **Verify camera SIP settings**:
   - Server IP matches your NVR's IP
   - Server port is `5060`
   - Device ID is 20 digits (GB/T 28181 format)
   - Password matches your NVR's `gb28181.password`
   - Platform access is enabled on the camera

4. **Check NVR logs**:
   ```bash
   journalctl -u mibee-nvr -f | grep -i gb28181
   ```

5. **Test SIP reachability**:
   ```bash
   # From the camera (if you have shell access):
   nc -uvz nvr-ip 5060
   ```

### Heartbeat Timeout

**Symptom**: Device registers but goes offline after a few minutes.

**Cause**: Device heartbeat interval does not match NVR's `heartbeat_interval` (default 60s). Most cameras send keepalives every 60 seconds, but some use different intervals.

**Solution**:
1. Adjust `heartbeat_interval` in your config:
   ```yaml
   gb28181:
     heartbeat_interval: "90s"  # or "30s", depending on your camera
   ```

2. Check camera SIP settings for its keepalive interval and match it.

### No Video (Black Screen)

**Symptom**: Channel invites successfully but live view is black.

**Solutions**:
1. **Check RTP port allocation**: Ensure `port_range` is not exhausted:
   ```bash
   # Check GB28181 devices page for "RTP Port" column
   # Verify ports are in the configured range
   ```

2. **Check firewall for RTP ports**: Allow UDP ports in `port_range`:
   ```bash
   sudo ufw allow 30000:30050/udp
   ```

3. **Verify codec**: GB28181 uses MPEG-PS with stream_type 96 (H.264) or 97 (H.265). The NVR auto-detects this from the PS stream. If the camera sends an unsupported codec, demux will fail.

4. **Check NVR logs for demux errors**:
   ```bash
   journalctl -u mibee-nvr -f | grep -i "psdemux\|demux"
   ```

5. **Test with direct RTP capture** (advanced):
   ```bash
   # Capture RTP from the camera's advertised port
   sudo tcpdump -i any -w capture.pcap udp port <camera-rtp-port>
   # Analyze with Wireshark to verify MPEG-PS structure
   ```

### Device Behind NAT (Network Address Translation)

**Symptom**: Device registers but cannot establish RTP media (video never starts).

**Cause**: RTP packets from the camera cannot reach the NVR because the camera is behind NAT and the SDP-advertised port is not forwarded.

**Solutions**:
1. **Enable TCP-passive mode** on the NVR:
   ```yaml
   gb28181:
     tcp_mode: true
     tcp_framing: "auto"
   ```
   TCP-passive mode requires the camera to initiate the TCP connection to the NVR, which works through NAT.

2. **Configure port forwarding** on your router:
   - Forward UDP ports `30000-30050` (or your `port_range`) to the NVR's IP address
   - Forward UDP port `5060` (SIP) to the NVR's IP address

3. **Use a VPN** (e.g., Tailscale, WireGuard) to bypass NAT:
   - Install the VPN on both the NVR and the camera (if supported)
   - Use VPN-assigned IPs in the camera's SIP settings

4. **Check camera NAT traversal settings**:
   - Hikvision: **Network → Advanced Platform Access → NAT Traversal**
   - Dahua: **Network → TCP/IP → 28181 → NAT Mode**

### Charset Issues (GBK vs UTF-8)

**Symptom**: Catalog response has mojibake (garbled text) for Chinese device/channel names.

**Cause**: GB/T 28181 devices often declare encoding as GB2312/GBK in the XML prolog (`<?xml ... encoding="GB2312"?>`) but may send actual UTF-8.

**Solution**: The NVR's MANSCDP codec handles this automatically:
- Strips the XML declaration before unmarshaling
- Validates UTF-8 and falls back to GB18030 → GBK decoders
- Logs a warning if charset fallback fails

If you still see garbled text, check the camera's GB28181 configuration for encoding settings. Some older devices require explicit UTF-8 declaration.

### Clock Sync Issues

**Symptom**: Device rejects REGISTER with authentication failure even though credentials are correct.

**Cause**: GB28181 digest authentication uses the SIP `Date` header for nonce freshness. If the NVR and device clocks are not synchronized, the device may reject the challenge.

**Solution**:
1. **Sync NVR clock** to NTP:
   ```bash
   sudo timedatectl set-ntp true
   # or
   sudo ntpdate pool.ntp.org
   ```

2. **Sync camera clock** to NTP (via camera web UI):
   - Hikvision: **System → Time Configuration**
   - Dahua: **System → General → Time**

3. **Verify clock drift**:
   ```bash
   # On NVR
   date
   # On camera (if shell access)
   date
   ```

### TCP Framing Issues

**Symptom**: TCP-passive mode (`tcp_mode=true`) shows "read error" or "invalid length".

**Cause**: `tcp_framing` setting does not match the camera's wire format.

**Solutions**:
1. Try each framing option:
   ```yaml
   gb28181:
     tcp_mode: true
     tcp_framing: "rfc4571"   # 2-byte big-endian length prefix
     # or
     tcp_framing: "0x24"      # RTSP interleaved ($ + channel + 2-byte length)
     # or
     tcp_framing: "auto"      # Detect from first bytes (default)
   ```

2. Check NVR logs for framing detection errors:
   ```bash
   journalctl -u mibee-nvr -f | grep -i "tcp\|framing\|0x24\|rfc4571"
   ```

3. **Note**: GB/T 28181-2016 and -2022 specify RFC4571 for RTP over TCP. The `0x24` mode models a vendor extension (RTSP interleaving). Use `"auto"` for mixed deployments.

### PTZ Not Working

**Symptom**: PTZ pad is disabled or PTZ commands have no effect.

**Solutions**:
1. **Check PTZType**: Verify the channel's `PTZType` from the catalog:
   - `0` = No PTZ support (device firmware limitation)
   - `1` = Pan/tilt only
   - `2` = Pan/tilt + zoom

2. **Check device online status**: PTZ commands fail with 409 if the device is offline.

3. **Verify manufacturer support**: Not all GB28181 devices support PTZ via MANSCDP DeviceControl. Some use proprietary SIP extensions.

4. **Check NVR logs for PTZ errors**:
   ```bash
   journalctl -u mibee-nvr -f | grep -i "ptz\|devicecontrol"
   ```

## API Reference

### Device and Channel Endpoints

- `GET /api/gb28181/devices` — List registered devices (ETag support)
- `GET /api/gb28181/channels` — List all channels across devices
- `GET /api/gb28181/channels/{id}` — Get channel details (including PTZType)
- `POST /api/gb28181/catalog/refresh` — Trigger catalog refresh (stub, 202 accepted)

### Media Session Endpoints

- `POST /api/gb28181/channels/{id}/invite` — Invite a channel (stub, 202 accepted)
- `POST /api/gb28181/channels/{id}/bye` — Send BYE to stop streaming (calls SessionManager.Bye)

### PTZ Endpoints

- `POST /api/gb28181/channels/{id}/ptz` — Send PTZ command

PTZ request body:
```json
{
  "direction": "up|down|left|right|up-left|up-right|down-left|down-right|zoom-in|zoom-out|stop",
  "speed": 0-255
}
```

PTZ directions:
- `up`, `down`, `left`, `right` — Pan/tilt
- `up-left`, `up-right`, `down-left`, `down-right` — Diagonal
- `zoom-in`, `zoom-out` — Zoom (requires `PTZType=2`)
- `stop` — Stop all movement

### Error Responses

| Status | Error | Description |
|--------|-------|-------------|
| 400 | Invalid body | Missing or malformed PTZ body |
| 404 | Channel not found | `ErrChannelNotFound` |
| 409 | Device offline | `ErrDeviceOffline` |
| 400 | PTZ unsupported | `ErrPTZUnsupported` (PTZType=0) |
| 400 | Zoom unsupported | `ErrZoomUnsupported` (PTZType=1) |
| 500 | Internal error | Failed to encode MANSCDP or send MESSAGE |

## Supported Protocols and Features

### SIP (Session Initiation Protocol)
- **REGISTER**: Device registration with digest authentication
- **Keepalive**: Heartbeat messages for online/offline tracking
- **INVITE**: Pull a live or playback media stream
- **BYE**: Tear down a media session
- **MESSAGE**: Send MANSCDP commands (catalog request, device control)

### Media (RTP/PS)
- **RTP over UDP**: Default transport (port range configurable)
- **RTP over TCP**: Optional TCP-passive mode for NAT traversal
  - Framing: RFC4571 (standard), 0x24 (vendor extension), auto-detect
- **MPEG-PS demuxing**: Extract H.264 (stream_type 96) or H.265 (stream_type 97) NALUs
- **StreamHub integration**: Non-blocking frame fan-out to HLS, WebRTC, FLV, etc.

### MANSCDP (Management and Control)
- **Catalog**: Device/channel list with PTZType, manufacturer, model
- **DeviceInfo**: Device firmware/hardware details
- **DeviceControl**: PTZ commands (direction, speed)

## Limitations and Known Issues

### Playback UI
Playback INVITE is supported in the session manager but not yet exposed in the web UI. Use the API endpoint `POST /api/gb28181/channels/{id}/invite` with `playback.start_time` and `playback.end_time` as a workaround.

### TCP 0x24 Mode
The `0x24` TCP framing mode models a vendor extension (RTSP interleaved). GB/T 28181-2016 and -2022 specify RFC4571 as the standard. Use `"auto"` for mixed deployments; use `"rfc4571"` for strict compliance.

### Device-Specific Behavior
- **Hikvision**: Supports full catalog, PTZ, and playback. Some older models may require GBK charset fallback.
- **Dahua**: Supports full catalog, PTZ, and playback. NAT traversal settings vary by firmware.
- **Uniview**: Similar to Hikvision, but may have different PTZ command semantics.
- **Other brands**: Support varies. Minimal devices implement only REGISTER/keepalive/INVITE.

### Port Exhaustion on RPi 3B
The default `port_range` is `"30000-30050"` (51 ports). If you have many concurrent GB28181 streams, expand the range:
```yaml
gb28181:
  port_range: "30000-30100"  # 101 ports
```

On RPi 3B, keep the pool under 200 ports to avoid ephemeral port exhaustion.

## Security Considerations

- **Digest authentication**: GB28181 uses SIP digest auth (like HTTP Basic but with nonce hashing). The `password` field is encrypted when `NVR_ENCRYPTION_KEY` is set.
- **Device ID restriction**: Use `allowed_device_ids` to whitelist specific 20-digit device codes. Empty list = allow all.
- **Network exposure**: GB28181 runs on UDP port 5060 by default. If your NVR is exposed to the internet, use a firewall or VPN to restrict access.
- **No commercial content**: This implementation is open-source and free. No Pro/P2P features are included or referenced.

## Support Resources

### Documentation
- [MiBee NVR Getting Started](./getting-started.md)
- [MiBee NVR Configuration Guide](./configuration.md)
- [MiBee NVR API Reference](./api/README.md)

### GB/T 28181 References
- GB/T 28181-2016: Information technology — Technical requirements for video surveillance networking system
- GB/T 28181-2022: Latest revision (adds TCP-passive, improved catalog)

### Community Support
- GitHub Issues: [MiBee NVR Issues](https://github.com/Mi-Bee-Studio/MiBeeNvr/issues)
- Discussions: [MiBee NVR Discussions](https://github.com/Mi-Bee-Studio/MiBeeNvr/discussions)

---

This guide provides comprehensive coverage of GB/T 28181 integration with MiBee NVR. For specific camera models, check manufacturer documentation for GB28181 capabilities and limitations.