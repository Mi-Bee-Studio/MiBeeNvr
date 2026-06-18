# Relay Guide — RTMP Live Platform Streaming

> Forward camera streams to live streaming platforms (Bilibili, YouTube, Douyin, Kuaishou) with automatic encoding optimization. Pure Go implementation, no FFmpeg for H.264 passthrough.

## Quick Start

### 3-Step Setup: Camera → Bilibili Live

1. **Configure your camera in `mibee-nvr.yaml`:**
```yaml
cameras:
  - id: "front-door"
    name: "Front Door Camera"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true
```

2. **Add Bilibili relay target:**
```yaml
cameras:
  - id: "front-door"
    name: "Front Door Camera"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true
    push_targets:
      - id: "bilibili-live"
        name: "Bilibili Live"
        protocol: "rtmp"
        url: "rtmp://live-push.bilivideo.com/live-bvc/?streamkey=YOUR_STREAM_KEY"
        enabled: true
        platform: "bilibili"
        transcode_policy: "auto"
```

3. **Start and verify:**
```bash
# Start MiBee NVR
./mibee-nvr -config mibee-nvr.yaml

# Check relay status
curl -u admin:PASSWORD http://localhost:9090/api/cameras/front-door/push-status

# Verify Bilibili stream on your Bilibili dashboard
```

## Supported Platforms

| Platform | URL Pattern | Audio Codec | Video Codec | Notes |
|----------|-------------|-------------|-------------|-------|
| **Bilibili** | `rtmp://live-push.bilivideo.com/live-bvc/` | AAC required | H.264 only | 4000kbps, 1920x1080, main profile, 2 B-frames |
| **Douyin** | `rtmp://live-push.douyin.com/` | AAC required | H.264 only | 3500kbps, **1080x1920 (vertical)**, main profile, 0 B-frames |
| **YouTube** | `rtmp://a.youtube.com/live2/` | AAC required | H.264 only | 4500kbps, 1920x1080, high profile, 2 B-frames |
| **Kuaishou** | `rtmp://txyun-push.voip.yximgs.com/gifshow/` | AAC required | H.264 only | 4000kbps, 1920x1080, main profile, 2 B-frames |
| **Generic** | Custom URL | AAC or G.711 | H.264 only | 3000kbps, 1920x1080, main profile, 0 B-frames |

> **Important:** 
> - RTMP targets require H.264 source video
> - Audio: AAC preferred, G.711 μ-law/a-law accepted as fallback
> - H.265 sources automatically transcode to H.264

## Platform Presets

### 5 Built-in Presets

The system includes 5 optimized presets for major platforms:

```go
// Bilibili: Balanced quality for live streaming
{
    Name: "bilibili",
    Description: "Bilibili live streaming", 
    URLHint: "rtmp://live-push.bilivideo.com/live-bvc/",
    GopSeconds: 2,
    VideoBitrateKbps: 4000,
    AudioBitrateKbps: 128,
    Resolution: "1920x1080",
    Framerate: 30,
    Profile: "main",
    Bframes: 2,
    AudioCodecRequired: "aac"
}

// Douyin: Vertical video format
{
    Name: "douyin",
    Description: "Douyin/TikTok live",
    URLHint: "rtmp://live-push.douyin.com/", 
    GopSeconds: 2,
    VideoBitrateKbps: 3500,
    AudioBitrateKbps: 128,
    Resolution: "1080x1920", // Vertical format
    Framerate: 30,
    Profile: "main", 
    Bframes: 0, // No B-frames for lowest latency
    AudioCodecRequired: "aac"
}

// YouTube: High quality with high profile
{
    Name: "youtube",
    Description: "YouTube Live",
    URLHint: "rtmp://a.youtube.com/live2/",
    GopSeconds: 2,
    VideoBitrateKbps: 4500,
    AudioBitrateKbps: 128,
    Resolution: "1920x1080",
    Framerate: 30,
    Profile: "high", // YouTube high profile
    Bframes: 2,
    AudioCodecRequired: "aac"
}

// Kuaishou: Standard quality
{
    Name: "kuaishou", 
    Description: "Kuaishou live",
    URLHint: "rtmp://txyun-push.voip.yximgs.com/gifshow/",
    GopSeconds: 2,
    VideoBitrateKbps: 4000,
    AudioBitrateKbps: 128,
    Resolution: "1920x1080",
    Framerate: 30,
    Profile: "main",
    Bframes: 2, 
    AudioCodecRequired: "aac"
}

// Generic: Fallback for unknown platforms
{
    Name: "generic",
    Description: "Generic RTMP target",
    URLHint: "", // Must be provided in config
    GopSeconds: 2,
    VideoBitrateKbps: 3000,
    AudioBitrateKbps: 128,
    Resolution: "1920x1080",
    Framerate: 30,
    Profile: "main",
    Bframes: 0, // Conservative setting
    AudioCodecRequired: "any" // Accepts both AAC and G.711
}
```

### Custom Presets via `relay-presets.yaml`

Create `relay-presets.yaml` in your deploy directory to override built-in presets or add new ones:

```yaml
# deploy/relay-presets.yaml
presets:
  custom_platform:
    name: custom_platform
    description: "My custom streaming platform"
    url_hint: "rtmp://live.mycustomplatform.com/"
    gop_seconds: 2
    video_bitrate_kbps: 5000
    audio_bitrate_kbps: 128
    resolution: "1920x1080"
    framerate: 30
    profile: "main"
    bframes: 2
    audio_codec_required: "aac"

  low_latency:
    name: low_latency
    description: "Low latency gaming stream"
    url_hint: "rtmp://gaming-platform.com/live/"
    gop_seconds: 1  # Shorter GOP for lower latency
    video_bitrate_kbps: 3000
    audio_bitrate_kbps: 128
    resolution: "1280x720"
    framerate: 60
    profile: "baseline"  # Baseline for lower latency
    bframes: 0
    audio_codec_required: "aac"
```

> **Configuration Loading:** The system loads custom presets from `relay-presets.yaml` if present. If the file is missing, invalid, or empty, it automatically falls back to the 5 built-in presets.

## Audio Handling

### AAC Passthrough

- **Best Quality:** AAC audio from your camera is passed through unchanged
- **Compatibility:** Supported by all major platforms (Bilibili, YouTube, Douyin, Kuaishou)
- **Setup:** Automatic when camera provides AAC audio

### G.711 Passthrough (Limited Support)

- **Mu-law/A-law:** G.711 audio from some cameras (primarily ONVIF and Xiaomi cameras)
- **Best-effort:** Some platforms may accept G.711, others may reject it
- **Fallback:** If rejected, silent AAC is generated (see below)

### Silent AAC Fallback

**Purpose:** When cameras have no audio or G.711 is rejected by the platform

**How it works:**
1. **Detection:** System detects missing audio or G.711-only source
2. **Generation:** Creates silent AAC-LC frames at 48kHz stereo
3. **Adaptive Rate:** Limits to 5 frames per second (10% of audio buffer capacity)
4. **Drop Protection:** Automatically reduces output if network congestion occurs

**Configuration:** Automatic - no manual setup required

**Technical Details:**
- AudioSpecificConfig: Standard 48kHz stereo AAC-LC
- Frame size: 6 bytes per silent frame
- Sample rate: 48kHz, channels: 2 (stereo)
- Buffer management: Non-blocking, prevents audio buffer overflow

## H.265 Transcoding

### When Transcoding is Engaged

The system automatically transcodes H.265 sources to H.264 when:

1. **Source Camera:** Your camera provides H.265 video
2. **Transcode Policy:** `transcode_policy: "auto"` or `"force_sw"`
3. **Registry Available:** Platform presets are configured

```yaml
cameras:
  - id: "h265-camera"
    name: "H.265 Camera"
    protocol: "rtsp" 
    encoding: "h265"  # H.265 source
    url: "rtsp://camera/h265-stream"
    push_targets:
      - id: "youtube-transcode"
        protocol: "rtmp"
        url: "rtmp://a.youtube.com/live2/"
        enabled: true
        platform: "youtube"
        transcode_policy: "auto"  # Will transcode H.265 to H.264
```

### Hardware Requirements

**Banana Pi M5 Production Server:**
- **Hardware Acceleration:** Uses v4l2m2m codec (vendor kernel)
- **CPU Fallback:** Falls back to libx264 on mainline kernel
- **Performance:** Hardware transcode at 1080p@30fps with < 10% CPU usage
- **Memory:** ~50MB RAM overhead for transcoding

**Raspberry Pi 3B:**
- **Software Only:** Uses libx264 (no hardware acceleration available)
- **CPU Usage:** ~30-40% CPU at 1080p@30fps
- **Memory:** ~100MB RAM overhead
- **Recommendation:** Use 720p or lower resolution for reliable performance

### Thermal Management

**Banana Pi M5 Thermal Protection:**
- **Throttle Threshold:** 85°C → Automatically downgrades to 480p resolution
- **Shutdown Threshold:** 95°C → Stops transcoding, triggers reconnection
- **Monitoring:** Real-time thermal stats available via Prometheus metrics
- **Cooling:** Ensure adequate cooling for sustained transcoding

**Configuration:**
```yaml
# System-wide thermal limit (optional override)
relay:
  thermal_limit: 85  # Celsius throttle threshold (default: 85)
```

### Transcode Policies

| Policy | Description | Use Case |
|--------|-------------|----------|
| `"auto"` | Use hardware if available, fallback to software | Best balance of performance and quality |
| `"force_sw"` | Always use software transcoding | Testing, or when hardware has bugs |
| `"off"` | Reject H.265 sources with permanent error | Only for H.264-only workflows |

## Manual Platform Testing

### Step-by-Step Bilibili Testing

1. **Create Bilibili Live Account:**
   - Go to [Bilibili Live Creator Center](https://link.bilibili.com/p/center/index)
   - Create a live streaming room
   - Copy your stream key (starts with "live-bvc-")

2. **Configure NVR:**
```yaml
cameras:
  - id: "test-camera"
    name: "Test Camera for Bilibili"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://your-camera-ip:554/stream"
    enabled: true
    push_targets:
      - id: "bilibili-test"
        name: "Bilibili Test Stream"
        protocol: "rtmp"
        url: "rtmp://live-push.bilivideo.com/live-bvc/?streamkey=YOUR_STREAM_KEY"
        enabled: true
        platform: "bilibili"
        transcode_policy: "auto"
```

3. **Start Stream:**
```bash
# Start the NVR
./mibee-nvr -config mibee-nvr.yaml

# Wait 30 seconds for camera connection
```

4. **Verify Stream:**
   - Check Bilibili dashboard for live preview
   - Verify stream appears in your live room
   - Check that both video and audio are working

5. **Check Analytics:**
   - Monitor viewer count and engagement
   - Check stream quality metrics on Bilibili
   - Monitor NVR logs for relay status

6. **Stop Test:**
```bash
# Disable target to stop streaming
# Edit mibee-nvr.yaml to set enabled: false for bilibili-test
# Restart NVR or reload config
```

### Verification Commands

```bash
# Check camera status
curl -s http://localhost:9090/api/cameras/test-camera | jq '.status.recording_status'

# Check relay status  
curl -s http://localhost:9090/api/cameras/test-camera | jq '.push_targets[]'

# View NVR logs
tail -f /var/log/mibee-nvr.log | grep relay

# Check stream key authentication (if applicable)
curl -H "Authorization: Bearer YOUR_KEY" -X GET https://api.bilibili.com/x/web-interface/nav
```

## Troubleshooting

### "Connection Refused" Errors

**Problem:** RTMP/RTSP connection fails

**Solutions:**
```bash
# 1. Check target URL accessibility
curl -I "rtmp://live-push.bilivideo.com/live-bvc/"

# 2. Verify camera stream locally
ffplay "rtsp://your-camera-ip:554/stream"

# 3. Check NVR logs for connection errors
journalctl -u mibee-nvr -f --since="5 minutes ago"

# 4. Verify network connectivity
ping live-push.bilivideo.com
netstat -tlnp | grep 1935  # RTMP port
```

**Configuration Fixes:**
- Ensure URL scheme is correct (`rtmp://` or `rtsp://`)
- Check port numbers and stream key format
- Verify platform preset name matches exactly

### "Codec Rejected" Errors

**Problem:** Audio/video codec not supported by target

**Solutions:**
```bash
# Check codec information from camera
curl -s http://localhost:9090/api/cameras/test-camera | jq '.recording_codec_info'

# If H.265 source and transcode not enabled:
# Add transcode_policy: "auto" to your target config
```

**Common Issues:**
- **H.265 source without transcode:** Add `transcode_policy: "auto"`
- **G.711 audio rejected:** System will fall back to silent AAC
- **Wrong audio format:** Ensure camera provides AAC audio

### "Thermal Shutdown" Errors

**Problem:** Transcoding overheats on Banana Pi M5

**Solutions:**
```bash
# Check current temperature
curl -u admin:PASSWORD -s http://localhost:9090/api/cameras/front-door/push-status | jq '.targets[0].temperature_c'

# Monitor thermal metrics
curl -s http://localhost:9090/api/metrics | grep 'nvr_relay_transcoder_temperature_c'

# Reduce transcode resolution or bitrate
# Or add cooling to the system
```

**Prevention:**
- Ensure adequate ventilation
- Monitor ambient temperature
- Use `transcode_policy: "force_sw"` to reduce GPU load

### "A/V Drift" Issues

**Problem:** Audio and video become out of sync

**Symptoms:**
- Audio leads/lags video by several seconds
- Continuous drift that worsens over time

**Solutions:**
```bash
# Check drift monitoring logs
tail -f /var/log/mibee-nvr.log | grep 'AV drift'

# System will automatically reconnect if drift exceeds 1 second sustained
# No manual intervention usually required
```

**System Behavior:**
- Monitors A/V sync in real-time
- Automatically reconnects if drift > 1 second for > 5 seconds
- No user configuration needed

### Common Error Messages

| Error Message | Solution |
|---------------|----------|
| `"source stream not ready"` | Camera disconnected or not streaming yet |
| `"H.265 source with transcode_policy=off"` | Add `transcode_policy: "auto"` or switch to H.264 camera |
| `"preset registry not configured"` | Ensure PresetRegistry is wired to relay manager in main.go |
| `"failed to parse AudioSpecificConfig"` | Camera provides invalid AAC audio, system will fallback to silent AAC |
| `"thermal limit exceeded"` | Reduce resolution/bitrate or improve cooling |

### "timed out after 15s waiting for transcoder SPS/PPS"

**Cause:** The transcoder (FFmpeg) starts but receives no H.265 input frames before the 15-second SPS/PPS timeout. This was a bug where the camera hub subscription was set up after the parameter wait instead of before.

**Fixed in:** Current version. The hub subscription now starts feeding H.265 frames to FFmpeg immediately after the transcoder starts, before waiting for output parameters.

**If you still see this:** Verify the camera is actively streaming (check `GET /api/cameras/{id}` → `status` should be `"recording"`). The transcoder can only produce output if it receives input frames.

### `temperature_c` always shows 0

**Cause:** The thermal monitor was not wired into the relay engine. The `latestTemperatureC` field was declared but never updated.

**Fixed in:** Current version. A `ThermalMonitor` is now started alongside each transcoder instance, reading `/sys/class/thermal/thermal_zone*/temp` every 30 seconds.

**Note:** When the RTMP target is unreachable, the transcoder restarts every ~5-10 seconds (connection retry cycle). Since the thermal check interval is 30 seconds, the temperature may not update during rapid reconnection cycles. Once the RTMP connection is stable, temperature reporting works normally.
## Configuration Reference

### PushTargetConfig Fields

```go
type PushTargetConfig struct {
    ID                  string                `yaml:"id" json:"id"`                             // Unique target ID within camera
    Name                string                `yaml:"name,omitempty" json:"name,omitempty"`    // Display name
    Protocol            string                `yaml:"protocol" json:"protocol"`                 // "rtmp" or "rtsp"
    URL                 string                `yaml:"url" json:"url"`                           // Target URL
    Enabled             bool                  `yaml:"enabled" json:"enabled"`                   // Whether target is active
    Platform            string                `yaml:"platform,omitempty" json:"platform,omitempty"`                         // Preset name (bilibili/dougin/youtube/kuaishou/generic)
    TranscodePolicy     string                `yaml:"transcode_policy,omitempty" json:"transcode_policy,omitempty"`         // "auto", "force_sw", "off"
    VideoPresetOverride *VideoPresetOverrides `yaml:"video_preset_override,omitempty" json:"video_preset_override,omitempty"`
}
```

### VideoPresetOverrides Fields

```go
type VideoPresetOverrides struct {
    Resolution       string `yaml:"resolution,omitempty" json:"resolution,omitempty"`       // "1920x1080", "1280x720", etc.
    Framerate        int    `yaml:"framerate,omitempty" json:"framerate,omitempty"`        // 1-120 FPS
    VideoBitrateKbps int    `yaml:"video_bitrate_kbps,omitempty" json:"video_bitrate_kbps,omitempty"` // 100-50000 kbps
    GopSeconds       int    `yaml:"gop_seconds,omitempty" json:"gop_seconds,omitempty"`       // 1-10 seconds
    Profile          string `yaml:"profile,omitempty" json:"profile,omitempty"`          // "baseline", "main", "high"
    Bframes          int    `yaml:"bframes,omitempty" json:"bframes,omitempty"`          // 0-2 (0=none, 1=1 B-frame, 2=2 B-frames)
}
```

### Global Configuration

```yaml
# mibee-nvr.yaml

relay:
  # Path to custom presets file (optional)
  presets_path: "/etc/mibee-nvr/relay-presets.yaml"
  
  # Thermal throttle threshold in Celsius (optional)
  thermal_limit: 85
```

### Validation Rules

- **Platform:** Must match `^[a-zA-Z0-9_]+$` (alphanumeric + underscores) or be empty
- **TranscodePolicy:** Must be "auto", "force_sw", "off", or empty
- **Resolution:** Must be in format WxH (e.g., "1920x1080")
- **Framerate:** If specified, must be 1-120
- **VideoBitrateKbps:** If specified, must be 100-50000
- **GopSeconds:** If specified, must be 1-10
- **Profile:** If specified, must be "baseline", "main", or "high"
- **Bframes:** Must be 0-2

### Complete Configuration Example

```yaml
# mibee-nvr.yaml - Full Relay Configuration

cameras:
  - id: "front-door"
    name: "Front Door Camera"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true
    push_targets:
      # Bilibili Live Stream
      - id: "bilibili-stream"
        name: "Bilibili Live"
        protocol: "rtmp"
        url: "rtmp://live-push.bilivideo.com/live-bvc/?streamkey=bvc_live_your_key"
        enabled: true
        platform: "bilibili"
        transcode_policy: "auto"
        video_preset_override:
          resolution: "1920x1080"
          framerate: 30
          video_bitrate_kbps: 4000
          gop_seconds: 2
          profile: "main"
          bframes: 2
      
      # YouTube Backup Stream
      - id: "youtube-backup"
        name: "YouTube Backup"
        protocol: "rtmp"
        url: "rtmp://a.youtube.com/live2/your_youtube_key"
        enabled: false  # Disabled by default
        platform: "youtube"
        transcode_policy: "auto"

  - id: "backyard-camera"
    name: "Backyard Camera"
    protocol: "rtsp"
    encoding: "h265"  # Will be transcoded automatically
    url: "rtsp://192.168.1.101:554/stream"
    enabled: true
    push_targets:
      - id: "douyin-stream"
        name: "Douyin Live"
        protocol: "rtmp"
        url: "rtmp://live-push.douyin.com/stream_key"
        enabled: true
        platform: "douyin"
        transcode_policy: "auto"
        # No override needed - uses douyin preset (1080x1920 vertical)

# Optional global relay configuration
relay:
  presets_path: "/etc/mibee-nvr/relay-presets.yaml"
  thermal_limit: 85
```

### Command-Line Examples

```bash
# Check relay status for all cameras
curl http://localhost:9090/api/cameras | jq '.[].push_targets[]'

# Check specific camera relay status
curl http://localhost:9090/api/cameras/front-door | jq '.push_targets[]'

# Monitor relay metrics
curl http://localhost:9090/api/metrics | grep 'nvr_relay_'

# Force restart camera relay (by toggling enabled flag)
# Edit config, set enabled: false → true, reload config

# Test connection to target platform
ffmpeg -re -i test-video.mp4 -c:v copy -c:a aac -f flv "rtmp://live-push.bilivideo.com/live-bvc/?test_key"
```

### System Integration

**Prometheus Metrics:**
- `nvr_relay_target_status` - Target streaming status
- `nvr_relay_bitrate_kbps` - Current bit rate
- `nvr_relay_reconnects_total` - Reconnection count
- `nvr_relay_transcoder_temperature_c` - Transcoder temperature (Banana Pi M5)
- `nvr_relay_transcoder_restarts_total` - Transcoder restarts
- `nvr_relay_transcoder_thermal_throttles_total` - Thermal throttle events

**Systemd Service:**
- Service: `mibee-nvr.service`
- Logs: `journalctl -u mibee-nvr`
- Status: `systemctl status mibee-nvr`

**Health Checks:**
```bash
# Overall system health
curl http://localhost:9090/api/health

# Camera health including relay status
curl http://localhost:9090/api/cameras/front-door/health
```

## Screenshots

*Relay configuration interface in web UI*
![Relay Configuration](docs/images/relay-config.png)

*Real-time relay status dashboard*
![Relay Status](docs/images/relay-dashboard.png)

*Platform preset management*
![Preset Management](docs/images/relay-presets.png)

---

**Next Steps:**
- [Configuration Reference](docs/en/configuration.md) - Complete config guide
- [API Reference](docs/en/api/README.md) - REST API documentation
- [Troubleshooting](docs/en/troubleshooting.md) - Common issues and solutions