# Audio

> Applies to MiBeeNvr v0.11.0

MiBee NVR supports audio recording, live listening, and two-way talk-back.

## Supported Audio Codecs

| Codec | Description | Recording | Live | Talk-back |
|-------|-------------|-----------|------|-----------|
| G.711 μ-law | Telephone quality | ✅ | ✅ | ✅ |
| G.711 A-law | Telephone quality | ✅ | ✅ | ✅ |
| AAC | High quality | ✅ | ✅ | — |
| Opus | High quality, low latency | ✅ | ✅ | ✅ |

## Audio Configuration

### Enable Audio Recording

Add audio parameters to the camera configuration:

```yaml
cameras:
  - id: "front-door"
    name: "Front Door"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    audio:
      enabled: true
      codec: "g711"               # g711 / aac / opus
      sample_rate: 8000           # Sample rate (Hz)
      channels: 1                 # Channel count
    enabled: true
```

### Audio Parameters

| Parameter | Default | Description |
|-----------|---------|-------------|
| `enabled` | `false` | Enable audio |
| `codec` | `g711` | Audio codec |
| `sample_rate` | `8000` | Sample rate (Hz) |
| `channels` | `1` | Channels (1 = mono, 2 = stereo) |

## Live Listening

### Web UI Listening

1. Open a camera in the web interface.
2. Click the **speaker** icon.
3. Audio plays through the browser.

> **Note**: Audio latency may be 100–300 ms higher than video.

### API Listening

```bash
# Fetch the audio stream
curl -u admin:password \
  "http://192.168.1.50:9090/api/v1/cameras/front-door/audio" \
  -o audio-stream.aac
```

## Two-Way Talk

### Enable Talk

```yaml
cameras:
  - id: "front-door"
    name: "Front Door"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    audio:
      enabled: true
      codec: "g711"
      two_way: true               # Enable two-way talk
    enabled: true
```

### Talk Features

- Click the **microphone** icon in the web UI to start talking.
- Uses the browser's microphone input.
- Supports G.711 and Opus codecs.

### Talk Parameters

| Parameter | Description |
|-----------|-------------|
| Codec | G.711 (broad compatibility) or Opus (better quality) |
| Latency | Typically 200–500 ms |
| Bandwidth | G.711: 64 kbps, Opus: 32–128 kbps |

## Audio Storage

Audio is recorded in sync with video and stored in the same MP4 segments:

```
recordings/
├── front-door/
│   ├── 2026-08-18/
│   │   ├── 00-00-00.mp4     # Contains video and audio
│   │   ├── 00-01-00.mp4
│   │   └── ...
```

## Audio Quality Tuning

### Bandwidth Control

```yaml
cameras:
  - id: "front-door"
    audio:
      enabled: true
      codec: "aac"
      bitrate: "64k"              # Audio bitrate
      sample_rate: 44100          # Higher sample rate
```

### Noise Reduction

If the camera supports noise reduction, enable it on the camera side:

- ONVIF cameras: enable noise reduction in the camera's web management interface.
- RTSP cameras: check the camera's configuration manual.

### Audio Sync

If audio and video are out of sync:

1. Check the camera's timestamp synchronization.
2. Try reconnecting the camera.
3. Enable timestamp correction in the NVR configuration.

```yaml
audio:
  enabled: true
  sync_correction: true          # Enable timestamp correction
```

## FAQ

### Audio Not Recording

1. Verify the camera has a built-in microphone (not all cameras do).
2. Confirm `audio.enabled: true` is set.
3. Check that the camera's audio codec is in the supported list.
4. Review the logs for audio-related errors.

### No Sound During Talk

1. Confirm the browser has microphone permission.
2. Verify the camera supports audio output (speaker or audio-out port).
3. Check network latency (high latency degrades the talk experience).

### Audio Latency

- Audio latency is typically higher than video.
- Use Opus encoding when strict sync is required.
- Lowering the audio sample rate can reduce latency.

## Next Steps

- [Timelapse](timelapse.md) — timelapse recording
- [Transcoding](transcoding.md) — video transcoding
- [WebDAV / FTP Storage](webdav-ftp.md) — access recording files
