# Camera Brand Compatibility Guide

MiBee NVR supports a wide range of IP cameras through various protocols including RTSP (H.264/H.265/MJPEG), HTTP JPEG, and ONVIF. This guide provides comprehensive compatibility information for popular camera brands, including supported protocols, configuration examples, and troubleshooting tips.

**ONVIF Integration**: For comprehensive ONVIF camera support, discovery methods, PTZ control, and troubleshooting, see the [ONVIF Guide](./onvif-guide.md).

## Quick Start (Top 3 Brands)

### Hikvision

```yaml
cameras:
  - id: "hikvision_front_door"
    name: "Front Door - Hikvision"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:password123@192.168.1.100:554/Streaming/Channels/101"
    enabled: true
```

1. **Access Camera**: Find camera IP address via Hikvision iVMS-4200 or web interface
2. **Enable RTSP**: Ensure RTSP streaming is enabled in camera web interface (usually under Network → Advanced)
3. **Configure**: Use the URL above with your camera's IP and credentials

### Dahua

```yaml
cameras:
  - id: "dahua_driveway"
    name: "Driveway - Dahua"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:admin@192.168.1.101:554/cam/realmonitor?channel=1&subtype=0"
    enabled: true
```

1. **Access Camera**: Find camera IP address via Dahua SmartPSS or web interface
2. **Enable RTSP**: Enable RTSP streaming in camera settings (usually under Configuration → Network → Stream)
3. **Configure**: Use the URL above with your camera's IP and credentials

### Uniview

```yaml
cameras:
  - id: "uniview_parking"
    name: "Parking - Uniview"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:123456@192.168.1.102:554/unicast/c1/s0/live"
    enabled: true
```

1. **Access Camera**: Find camera IP address via Uniview iViewer or web interface
2. **Enable RTSP**: Enable RTSP streaming in camera settings (usually under Network → Streaming)
3. **Configure**: Use the URL above with your camera's IP and credentials

## Compatibility Overview

| Tier | Support Level | Protocol | Brands | Notes |
|------|---------------|----------|--------|-------|
| **Full Support** | ✅ Auto-detect + PTZ | RTSP + ONVIF | Hikvision, Dahua, Uniview, Axis, Bosch, Vivotek, Hanwha, Amcrest, Reolink | Best experience, full features |
| **Manual Setup** | ⚠️ RTSP only, limited ONVIF | RTSP only | TP-Link VIGI, EZVIZ, ANNKE, Lorex, Swann, Speco | Manual configuration required |
| **Limited/Special** | 🔧 Special handling needed | Various | Xiaomi, BESDER, Generic, Wyze | Custom setup or limitations |

## Full Support (ONVIF + RTSP)

### 1. Hikvision

**Models**: DS-2CD2T42WDG1-I, DS-2CD2143G0-I, DS-2CD2343G0-I

#### RTSP URLs:
- Main Stream: `rtsp://user:pass@ip:554/Streaming/Channels/101`
- Sub Stream: `rtsp://user:pass@ip:554/Streaming/Channels/102`
- Audio Channel: `rtsp://user:pass@ip:554/Streaming/Channels/101_1`

#### Configuration:
```yaml
cameras:
  - id: "hikvision_main"
    name: "Hikvision Main Stream"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:password123@192.168.1.100:554/Streaming/Channels/101"
    enabled: true
```

#### Default Credentials:
- Username: `admin`
- Password: `password123` (default, may vary)

#### Known Issues:
- Some models require enabling RTSP in web interface first
- ONVIF may not work on firmware older than V5.3.x
- Audio streams sometimes separate from video

### 2. Dahua

**Models**: IPC-HFW1237S-Z, IPC-HDW2831R-ZS, IPC-HFW5442E-Z

#### RTSP URLs:
- Channel 1: `rtsp://user:pass@ip:554/cam/realmonitor?channel=1&subtype=0`
- Channel 2: `rtsp://user:pass@ip:554/cam/realmonitor?channel=2&subtype=0`
- Audio Stream: `rtsp://user:pass@ip:554/cam/realmonitor?channel=1&subtype=1`

#### Configuration:
```yaml
cameras:
  - id: "dahua_main"
    name: "Dahua Main Stream"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:admin@192.168.1.101:554/cam/realmonitor?channel=1&subtype=0"
    enabled: true
```

#### Default Credentials:
- Username: `admin`
- Password: `admin` (default)

#### Known Issues:
- Some models use different substream URLs (`subtype=1` for substream)
- Audio may need separate configuration
- ONVIF discovery may take longer than RTSP direct

### 3. Uniview

**Models**: U-AI1208LBF, U-AI1208LBFZ, U-CV3208ERBU

#### RTSP URLs:
- Main Stream: `rtsp://user:pass@ip:554/unicast/c1/s0/live`
- Sub Stream: `rtsp://user:pass@ip:554/unicast/c1/s1/live`
- Audio Stream: `rtsp://user:pass@ip:554/unicast/c1/s0/live`

#### Configuration:
```yaml
cameras:
  - id: "uniview_main"
    name: "Uniview Main Stream"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:123456@192.168.1.102:554/unicast/c1/s0/live"
    enabled: true
```

#### Default Credentials:
- Username: `admin`
- Password: `123456` (default)

#### Known Issues:
- Some models require RTSP to be enabled in camera settings
- Audio streams may be separate from video
- ONVIF port may vary (usually 80, but check camera settings)

### 4. Axis

**Models**: M3045-V, M3054-V, M3065-V

#### RTSP URLs:
- Main Stream: `rtsp://root:pass@ip:554/axis-media/media.amp`
- High Quality: `rtsp://root:pass@ip:554/axis-media/media.amp?videoType=1`
- Audio Stream: `rtsp://root:pass@ip:554/axis-media/media.amp?videoType=3`

#### Configuration:
```yaml
cameras:
  - id: "axis_main"
    name: "Axis Main Stream"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://root:password@192.168.1.103:554/axis-media/media.amp"
    enabled: true
```

#### Default Credentials:
- Username: `root`
- Password: `pass` (default)

#### Known Issues:
- Some models require specific authentication methods
- Audio streams may need separate configuration
- Camera firmware updates can change RTSP endpoints

### 5. Bosch

**Models**: FLEXIDOME 5000I, MIC IP, DINION 6000I

#### RTSP URLs:
- Video Stream 1: `rtsp://user:pass@ip:554/rtp_video1`
- Video Stream 2: `rtsp://user:pass@ip:554/rtp_video2`
- Audio Stream: `rtsp://user:pass@ip:554/rtp_audio1`

#### Configuration:
```yaml
cameras:
  - id: "bosch_main"
    name: "Bosch Main Stream"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:12345@192.168.1.104:554/rtp_video1"
    enabled: true
```

#### Default Credentials:
- Username: `admin`
- Password: `12345` (default)

#### Known Issues:
- Some models use different port ranges for RTSP
- Audio may require separate configuration
- ONVIF configuration can be complex

### 6. Vivotek

**Models**: IB8369, IP8332-H, FD8166

#### RTSP URLs:
- Live Stream: `rtsp://user:pass@ip:554/live.sdp`
- Main Stream: `rtsp://user:pass@ip:554/h264/ch1/main/av_stream`
- Sub Stream: `rtsp://user:pass@ip:554/h264/ch1/sub/av_stream`

#### Configuration:
```yaml
cameras:
  - id: "vivotek_main"
    name: "Vivotek Main Stream"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://root:123456@192.168.1.105:554/live.sdp"
    enabled: true
```

#### Default Credentials:
- Username: `root`
- Password: `123456` (default)

#### Known Issues:
- Some models require specific RTSP paths
- Audio streams may not work with all models
- ONVIF discovery may need manual configuration

### 7. Hanwha

**Models**: XNV-6081R, XND-6081R, XNV-8081R

#### RTSP URLs:
- Profile 1: `rtsp://user:pass@ip:554/profile1/media.smp`
- Profile 2: `rtsp://user:pass@ip:554/profile2/media.smp`
- Audio Stream: `rtsp://user:pass@ip:554/profile1/audio`

#### Configuration:
```yaml
cameras:
  - id: "hanwha_main"
    name: "Hanwha Main Stream"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:123456@192.168.1.106:554/profile1/media.smp"
    enabled: true
```

#### Default Credentials:
- Username: `admin`
- Password: `123456` (default)

#### Known Issues:
- Some models use different profile numbers
- Audio streams may require separate configuration
- ONVIF port varies by model (check camera web interface)

### 8. Amcrest

**Models**: IP8M-T1179EW, IP5M-T1177EW, IP2M-841EB

#### RTSP URLs:
- Channel 1: `rtsp://user:pass@ip:554/cam/realmonitor?channel=1&subtype=0`
- Channel 2: `rtsp://user:pass@ip:554/cam/realmonitor?channel=2&subtype=0`
- Audio Stream: `rtsp://user:pass@ip:554/cam/realmonitor?channel=1&subtype=1`

#### Configuration:
```yaml
cameras:
  - id: "amcrest_main"
    name: "Amcrest Main Stream"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:admin@192.168.1.107:554/cam/realmonitor?channel=1&subtype=0"
    enabled: true
```

#### Default Credentials:
- Username: `admin`
- Password: `admin` (default)

#### Known Issues:
- Similar to Dahua cameras but with different firmware
- Some models use subtypes differently
- ONVIF discovery works but may be slow

### 9. Reolink

**Models**: RLC-810A, RLC-510A, RLC-823A

#### RTSP URLs:
- Main Stream: `rtsp://user:pass@ip:554/h264Preview_01_main`
- Sub Stream: `rtsp://user:pass@ip:554/h264Preview_01_sub`
- Audio Stream: `rtsp://user:pass@ip:554/h264Preview_01_main`

#### Configuration:
```yaml
cameras:
  - id: "reolink_main"
    name: "Reolink Main Stream"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:123456@192.168.1.108:554/h264Preview_01_main"
    enabled: true
```

#### Default Credentials:
- Username: `admin`
- Password: `123456` (default)

#### Known Issues:
- Some models require Reolink-specific authentication
- Audio streams may need separate configuration
- ONVIF discovery works but port may be different (8000)

## Manual Setup (RTSP Only)

### 10. TP-Link VIGI

**Models**: TC-VR303, TC-VR307, TC-V304GS

#### RTSP URLs:
- Stream 1: `rtsp://user:pass@ip:554/stream1`
- Stream 2: `rtsp://user:pass@ip:554/stream2`
- Sub Stream: `rtsp://user:pass@ip:554/stream1_sub`

#### Configuration:
```yaml
cameras:
  - id: "tplink_vigi"
    name: "TP-Link VIGI"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:admin@192.168.1.109:554/stream1"
    enabled: true
```

#### Default Credentials:
- Username: `admin`
- Password: `admin` (default)

#### Known Issues:
- Limited ONVIF support (may not discover automatically)
- Some models require manual URL configuration
- Audio streams not always available

### 11. EZVIZ

**Models**: CS-CV248, CS-CV228, CS-CV208

#### RTSP URLs:
- Channel 1: `rtsp://user:pass@ip:554/h264/ch1/main/av_stream`
- Channel 2: `rtsp://user:pass@ip:554/h264/ch2/main/av_stream`
- Audio Stream: `rtsp://user:pass@ip:554/h264/ch1/main/audio`

#### Configuration:
```yaml
cameras:
  - id: "ezviz_main"
    name: "EZVIZ Main Stream"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:verification_code@192.168.1.110:554/h264/ch1/main/av_stream"
    enabled: true
```

#### Default Credentials:
- Username: `admin`
- Password: Verification code from camera sticker

#### Known Issues:
- No ONVIF support on most models
- Password changes frequently (sticker-based)
- Some models require firmware updates for RTSP support

### 12. ANNKE

**Models**: 8CH H.265, 4CH H.264, 16CH NVR

#### RTSP URLs:
- Channel 1: `rtsp://user:pass@ip:554/cam/realmonitor?channel=1&subtype=0`
- Channel 2: `rtsp://user:pass@ip:554/cam/realmonitor?channel=2&subtype=0`
- Audio Stream: `rtsp://user:pass@ip:554/cam/realmonitor?channel=1&subtype=1`

#### Configuration:
```yaml
cameras:
  - id: "annke_main"
    name: "ANNKE Main Stream"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:admin123@192.168.1.111:554/cam/realmonitor?channel=1&subtype=0"
    enabled: true
```

#### Default Credentials:
- Username: `admin`
- Password: `admin123` (default)

#### Known Issues:
- Dahua OEM, similar URL format to Dahua
- ONVIF support varies by model
- Some models require specific firmware versions

### 13. Lorex

**Models**: LNB4432, LNC22852B, LNE8832

#### RTSP URLs:
- Main Stream: `rtsp://user:pass@ip:554/stream`
- Sub Stream: `rtsp://user:pass@ip:554/stream_sub`
- Audio Stream: `rtsp://user:pass@ip:554/stream_audio`

#### Configuration:
```yaml
cameras:
  - id: "lorex_main"
    name: "Lorex Main Stream"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:000000@192.168.1.112:554/stream"
    enabled: true
```

#### Default Credentials:
- Username: `admin`
- Password: `000000` or `123456` (varies by model)

#### Known Issues:
- Dahua/FLIR OEM, RTSP format varies by model
- ONVIF support inconsistent across models
- Some models require specific configuration steps

### 14. Swann

**Models**: SWPRO-890MS, SWPRO-882MS, SWPRO-848MS

#### RTSP URLs:
- Main Stream: `rtsp://user:pass@ip:554/stream`
- Sub Stream: `rtsp://user:pass@ip:554/stream_sub`
- Audio Stream: `rtsp://user:pass@ip:554/stream_audio`

#### Configuration:
```yaml
cameras:
  - id: "swann_main"
    name: "Swann Main Stream"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:123456@192.168.1.113:554/stream"
    enabled: true
```

#### Default Credentials:
- Username: `admin`
- Password: `123456` (default)

#### Known Issues:
- Similar to Lorex, varies by model
- ONVIF support inconsistent
- Some models require firmware updates

### 15. Speco Technologies

**Models**: C128-HV, C828-HV, C168-HV

#### RTSP URLs:
- Main Stream: `rtsp://user:pass@ip:554/stream`
- Sub Stream: `rtsp://user:pass@ip:554/stream_sub`
- Audio Stream: `rtsp://user:pass@ip:554/stream_audio`

#### Configuration:
```yaml
cameras:
  - id: "speco_main"
    name: "Speco Main Stream"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:1234@192.168.1.114:554/stream"
    enabled: true
```

#### Default Credentials:
- Username: `admin`
- Password: `1234` (default)

#### Known Issues:
- Good ONVIF support but RTSP format varies
- Some models require manual configuration
- Documentation can be outdated

## Limited / Special Cases

### 16. Xiaomi

**Models**: C200, C300, Xiaofang, Dafang

#### Special Setup:
- Use MiBee NVR's built-in Xiaomi integration, NOT RTSP directly
- Configure via Web UI → Xiaomi Device Discovery
- Authentication via Xiaomi cloud services

#### Configuration:
```yaml
xiaomi:
  enabled: true
  user_id: "123456789"
  token: "your_passToken_here"
  region: "cn"
  
cameras:
  - id: "xiaomi_c200"
    name: "Xiaomi C200"
    protocol: "xiaomi"
    encoding: "h264"
    did: "device_id_here"
    vendor: "cs2"
    enabled: true
```

#### Known Issues:
- Not compatible with RTSP directly
- Requires Xiaomi cloud connectivity
- P2P protocol only, no direct IP access

### 17. BESDER

**Models**: BCS-SR-505, BCS-SR-608, BCS-SR-702

#### RTSP URLs:
- Main Stream: `rtsp://user:pass@ip:554/stream`
- Sub Stream: `rtsp://user:pass@ip:554/sub`
- Audio Stream: `rtsp://user:pass@ip:554/audio`

#### Configuration:
```yaml
cameras:
  - id: "besder_main"
    name: "BESDER Main Stream"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:password@192.168.1.115:554/stream"
    enabled: true
```

#### Known Issues:
- Usually ONVIF compatible but quality varies
- RTSP format not standardized across models
- Some models have poor video quality over RTSP

### 18. Generic RTSP

**Models**: Various Chinese brands, white-label cameras

#### RTSP URLs:
- Common formats: `rtsp://user:pass@ip:554/stream`
- Alternative formats: `rtsp://user:pass@ip:554/live`
- Another option: `rtsp://user:pass@ip:554/h264`

#### Configuration:
```yaml
cameras:
  - id: "generic_rtsp"
    name: "Generic RTSP Camera"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:password@192.168.1.116:554/stream"
    enabled: true
```

#### Known Issues:
- RTSP format varies greatly by manufacturer
- Trial and error required for URL format
- ONVIF discovery may not work

### 19. Generic ONVIF

**Models**: Various ONVIF-compliant cameras

#### Configuration:
```yaml
cameras:
  - id: "generic_onvif"
    name: "Generic ONVIF Camera"
    protocol: "onvif"
    url: "http://192.168.1.117:80/onvif/device_service"
    username: "admin"
    password: "password"
    enabled: true
```

#### Known Issues:
- Use MiBee's ONVIF Scan feature for auto-discovery
- May require manual configuration if discovery fails
- ONVIF profiles may vary between models

### 20. Wyze

**Models**: Wyze Cam v3, Wyze Cam Outdoor, Wyze Cam Pan

#### Special Setup:
- Limited RTSP support via custom firmware
- Requires Wyze RTSP fork or equivalent
- Mostly cloud-based, not ideal for NVR

#### Configuration:
```yaml
cameras:
  - id: "wyze_rtsp"
    name: "Wyze RTSP Stream"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:password@192.168.1.118:554/live"
    enabled: true
```

#### Known Issues:
- Not officially supported by Wyze
- Requires specific firmware modifications
- Performance can be poor with custom firmware

## General Configuration Tips

### RTSP URL Testing

Before adding to MiBee NVR, test RTSP URLs with VLC or ffplay:

```bash
# Test RTSP connection
ffplay rtsp://admin:password@192.168.1.100:554/stream

# Test HTTP JPEG
curl http://admin:password@192.168.1.101:8080/snapshot

# Check camera network connectivity
ping 192.168.1.100

# Test ONVIF discovery
nmap -p 80 192.168.1.102
```

### Performance Optimization

For Raspberry Pi users, optimize settings for limited resources:

```yaml
cameras:
  - id: "optimized_camera"
    name: "Optimized Camera"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:password@192.168.1.100:554/stream"
    enabled: true
    # Optimize for Raspberry Pi
    segment_duration: "30s"
    hls_max_fps: 15
    sample_interval: 2
```

### Network Considerations

- Ensure cameras and MiBee NVR are on the same network
- Consider VLAN segmentation for security
- Use static IP addresses for reliable connections
- Monitor bandwidth usage for multiple cameras

### Security Best Practices

- Change default camera passwords
- Use different credentials for each camera
- Enable firewall rules to restrict camera access
- Regular firmware updates for cameras
- Consider HTTPS for camera web interfaces

## Troubleshooting

### Common RTSP Issues

**"RTSP connection failed"**:

```bash
# Check if camera is online
ping 192.168.1.100

# Test RTSP manually
ffplay rtsp://admin:password@192.168.1.100:554/stream

# Check camera port accessibility
nc -zv 192.168.1.100 554
```

**"No video stream available"**:

- Verify RTSP is enabled in camera settings
- Check if camera requires authentication
- Try different stream formats (main/sub)
- Test URL with VLC to confirm stream works

### ONVIF Discovery Issues

**Camera not discovered via ONVIF**:

```yaml
# Manual ONVIF configuration
cameras:
  - id: "manual_onvif"
    name: "Manual ONVIF Camera"
    protocol: "onvif"
    url: "http://192.168.1.100:80/onvif/device_service"
    username: "admin"
    password: "password"
    enabled: true
```

**ONVIF authentication failed**:

- Check ONVIF port (usually 80, but varies)
- Verify camera supports ONVIF
- Try different authentication methods
- Check camera firmware version for ONVIF support

### Audio Configuration

**No audio in recordings**:

```yaml
# Enable audio in camera configuration
cameras:
  - id: "camera_with_audio"
    name: "Camera with Audio"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://admin:password@192.168.1.100:554/stream"
    enabled: true
    # Audio settings
    enable_audio: true
    audio_codec: "aac"  # or pcm, g711
    audio_bitrate: "128k"
```

**Audio sync issues**:

- Check if audio stream is separate from video
- Adjust audio sync settings if available
- Some cameras don't support audio in RTSP stream
- Consider separate audio recording if needed

## Support Resources

### Documentation

- [MiBee NVR Configuration Guide](./configuration.md)
- [MiBee NVR API Reference](./api/README.md)
- [MiBee NVR Getting Started](./getting-started.md)

### Community Support

- GitHub Issues: [MiBee NVR Issues](https://github.com/Mi-Bee-Studio/MiBeeNvr/issues)
- Discussions: [MiBee NVR Discussions](https://github.com/Mi-Bee-Studio/MiBeeNvr/discussions)
- Discord: Join our community server for help

### Professional Support

- Commercial support available for enterprise users
- Contact us for custom camera integration
- On-site consultation available for large deployments

## Camera Discovery Tools

### ONVIF Device Manager

Download and install [ONVIF Device Manager](https://www.onvif.org/) to:

- Discover ONVIF cameras on your network
- Get detailed camera information
- Test ONVIF compliance
- Download camera capabilities

### iSpy

[iSpy](https://www.ispyconnect.com/) can help discover cameras and generate RTSP URLs:

- Automatic camera discovery
- RTSP URL generation
- Testing stream compatibility
- Camera configuration management

### FFMPEG RTSP URL Tester

Create a simple script to test RTSP URLs:

```bash
#!/bin/bash

# Test RTSP URL function
test_rtsp_url() {
    local url="$1"
    local timeout=10
    
    echo "Testing RTSP URL: $url"
    
    # Test with timeout
    timeout $timeout ffmpeg -i "$url" -t 1 -f null - 2>/dev/null && {
        echo "✅ RTSP URL works: $url"
        return 0
    } || {
        echo "❌ RTSP URL failed: $url"
        return 1
    }
}

# Example usage
test_rtsp_url "rtsp://admin:password@192.168.1.100:554/stream"

# Batch test multiple URLs
for ip in 192.168.1.{100..120}; do
    test_rtsp_url "rtsp://admin:password@$ip:554/stream"
done
```

### Network Scanning Tools

Use these tools to find cameras on your network:

```bash

# Find ONVIF cameras
nmap -p 80 --open 192.168.1.0/24 | grep -v "Nmap scan"

# Find RTSP servers
nmap -p 554 --open 192.168.1.0/24 | grep -v "Nmap scan"

# Find HTTP cameras
nmap -p 80,8080 --open 192.168.1.0/24 | grep -v "Nmap scan"

# Quick camera discovery
sudo nmap -sV -p 554,80,8080 --open 192.168.1.0/24
```

## Push Cameras (SRT / RTMP) — Cross-Network Ingest

Unlike the pull protocols above (RTSP/ONVIF/HTTP), where the NVR dials **out** to the camera, **push** cameras work the other way around: a remote publisher (ffmpeg, OBS, a phone, or another NVR) pushes a stream **into** the NVR. This lets you record cameras on a **different network** (another LAN, across the internet, behind NAT) without a VPN — as long as the publisher can reach the NVR's public IP (or a port-forwarded port).

This is the recommended way to connect cameras that the NVR cannot dial directly.

### How it works

1. The NVR runs an **SRT listener** (UDP, default port `9000`) and/or an **RTMP server** (TCP, default port `1935`).
2. You add a camera with `protocol: srt` or `protocol: rtmp`. The NVR creates an `IngestRecorder` for it and waits (status shows as *reconnecting* / idle).
3. When the publisher connects and starts streaming, the recorder flips to *recording*, fans the frames out to live HLS/WebRTC/FLV, **and** writes rolling MP4 segments to disk — exactly like an RTSP camera.

### Prerequisites

- The NVR's SRT (`UDP 9000`) and/or RTMP (`TCP 1935`) port must be reachable from the publisher's network. If the NVR is behind NAT/router, set up **port forwarding** for that port to the NVR host.
- The publisher needs ffmpeg, OBS, GStreamer, or any tool that can push RTMP/SRT.

### Enable the ingest servers

In `mibee-nvr.yaml` (or via Settings):

```yaml
srt:
  enabled: true
  port: 9000

rtmp:
  enabled: true
  port: 1935
```

### Add a push camera (RTMP)

```yaml
cameras:
  - id: "remote-shop"
    name: "Remote Shop Camera"
    protocol: "rtmp"
    encoding: "h264"
    stream_key: "remote-shop"   # the last path segment of the publish URL
    enabled: true
```

Publish from the remote site (ffmpeg example, replacing `NVR_IP`):

```bash
# From an RTSP camera on the remote network, re-publish as RTMP to the NVR
ffmpeg -rtsp_transport tcp -i "rtsp://admin:pass@192.168.1.50:554/stream" \
  -c copy -f flv "rtmp://NVR_IP:1935/live/remote-shop"
```

The `stream_key` (`remote-shop`) maps the publish URL's last segment to this camera. You can also set the key in the camera form; the form shows the full publish address.

### Add a push camera (SRT)

```yaml
cameras:
  - id: "garage-cam"
    name: "Garage Camera"
    protocol: "srt"
    encoding: "h264"
    srt_stream_id: "garage-cam"     # maps the SRT streamid to this camera
    srt_passphrase: "my-secret"      # optional AES encryption
    enabled: true
```

Publish from the remote site:

```bash
ffmpeg -rtsp_transport tcp -i "rtsp://admin:pass@192.168.1.60:554/stream" \
  -c copy -f mpegts "srt://NVR_IP:9000?streamid=garage-cam&passphrase=my-secret"
```

SRT uses UDP, which some home ISPs block or make hard to forward. If SRT doesn't connect, use RTMP (TCP) instead — it traverses consumer NAT/firewalls more reliably.

### Notes & limitations

- **H.264 only over SRT/RTMP today.** The SRT MPEG-TS demuxer and classic RTMP both carry H.264. H.265 over these transports is a follow-up. (For H.265 cameras, transcode to H.264 at the publisher, or use MediaMTX as a bridge.)
- **The camera shows *offline* until a publisher connects** — this is expected for push cameras. There is no source to "dial" until the publisher arrives.
- **Port forwarding / public IP required.** Push ingest does not traverse NAT on its own (no STUN/TURN). If neither side has a reachable port, use an overlay network (Tailscale/WireGuard) or MediaMTX as an intermediary.
- **Audio**: RTMP/SRT push currently record video only (no audio track wiring on the ingest path yet).
- **Legacy global config** (`srt.streams[]`, `rtmp.stream_keys`) still works and is merged with per-camera fields; per-camera fields take precedence.

## Push-Out (Relay) — Forward a Camera to Remote Destinations

Push-out (relay) is the reverse of push-in: the NVR forwards a camera's live stream OUT to remote destinations — another NVR's RTMP/SRT ingest, a live streaming platform, or a backup server. **Native Go relay by default** (FFmpeg optional for compatibility) — the relay uses the existing `gortsplib`/`gortmplib` libraries in-process.

This is ideal for: sending a camera's feed to a remote NVR across the internet, mirroring to a backup site, or publishing to a live platform — all without an external process.

### How it works

Each push-out target subscribes to the camera's existing `StreamHub` (the same frame bus used by recording and live view). Frames are remuxed (not re-encoded) and written to the target over a dedicated RTMP or RTSP connection. Each target runs in its own goroutine with independent reconnect and bitrate accounting.

- **Zero-copy source**: no re-pull, no decode — adding a relay target adds one goroutine + one outbound socket (~5-10 MB on RPi 3B).
- **H.264 remux only**: RTMP targets require an H.264 source (RTMP doesn't carry H.265). RTSP targets are also H.264 currently. Transcoding is NOT supported in the relay (use the transcoding feature for H.265→H.264 if needed, then relay the transcoded stream).
- **Multiple targets per camera**: one camera can push to several destinations simultaneously.

### Configure push-out targets

Add `push_targets` to ANY camera (RTSP, ONVIF, Xiaomi, even push-in cameras):

```yaml
cameras:
  - id: "front-door"
    protocol: "rtsp"
    url: "rtsp://admin:pass@192.168.1.50/stream"
    push_targets:
      - id: "backup-nvr"
        name: "Backup NVR"
        protocol: "rtmp"
        url: "rtmp://backup.example.com:1935/live/front-door"
        enabled: true
      - id: "live-platform"
        name: "Live Platform"
        protocol: "rtsp"
        url: "rtsp://live.example.com:8554/front-door"
        enabled: false
    enabled: true
```

Or configure via the Web UI: edit a camera → expand the "Push-Out (Relay)" section → add targets (name, protocol, URL, enable toggle).

### Monitoring relay status

The camera card shows a badge with active/total targets (e.g. `↑ 2/2`). In the edit form, each target shows a live status pill (streaming + kbps / connecting / reconnecting / error). The API endpoint `GET /api/cameras/{id}/push-status` returns per-target runtime status.

### Cross-network NVR-to-NVR example

The most common use case — sending a camera from one NVR to another across the internet, with NO FFmpeg:

```text
[Camera] ──RTSP──▶ [NVR-A] ──push-out relay (RTMP)──▶ [NVR-B (push-in ingest)] ──▶ records + live
```

1. On **NVR-B** (the receiver): enable RTMP ingest, add a push-in camera with a stream key (e.g. `front-door-relay`).
2. On **NVR-A** (the sender): add a `push_target` to the source camera pointing to `rtmp://NVR-B-IP:1935/live/front-door-relay`.
3. NVR-A's relay engine connects to NVR-B and forwards frames. NVR-B records and serves the stream like a local camera.

### Notes & limitations

- **H.264 preferred for RTMP targets**. H.265 sources are automatically transcoded to H.264 when `transcode_policy: "auto"` is set (see [Relay Guide](./relay-guide.md)); H.264 sources are forwarded directly with zero transcode overhead.
- **Audio is forwarded**: AAC audio is passed through; G.711-only sources are handled per target platform requirements (see Relay Guide).
- **Target must be reachable**: the relay dials OUT to the target, so the target's RTMP/RTSP port must be accessible (port-forward if behind NAT).
- **Resilient reconnect**: if the source camera or target connection drops, the relay retries with exponential backoff and resumes automatically.

Through comprehensive camera brand compatibility information, this guide helps you successfully integrate various IP cameras with MiBee NVR, ensuring optimal recording performance and reliability for your surveillance system.