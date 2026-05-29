# Transcoding Guide

## Overview

Transcoding converts video recordings between different codecs to optimize storage, improve playback compatibility, or reduce file sizes. MiBee NVR supports background transcoding of H.264 and H.265 recordings to either format with configurable quality presets.

Use transcoding when you need to:
- Reduce storage usage by converting to more efficient codecs
- Improve playback compatibility on older devices
- Convert recordings for specific use cases
- Archive footage with optimized file sizes

## Quick Start

### Step 1: Check Hardware Support

```bash
# Test if your system can handle transcoding
curl -u admin:password http://localhost:9090/api/transcoding/check
```

### Step 2: Download FFmpeg (if needed)

```bash
# Automatically download FFmpeg for your platform
curl -u admin:password \
  -X POST http://localhost:9090/api/transcoding/ffmpeg/download
```

### Step 3: Enable Transcoding

Add transcoding configuration to your config file:

```yaml
transcoding:
  enabled: true
  max_workers: 1
  replace_original: false

cameras:
  - id: "front-door"
    name: "Front Door"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true
    transcoding:
      enabled: true
      target_codec: "h264"
      preset: "medium"
      bitrate: "2M"
```

Restart MiBee NVR to apply the configuration.

## Configuration Reference

### Global Settings

The `transcoding` section in your main configuration file controls overall transcoding behavior:

```yaml
transcoding:
  enabled: true                    # Enable/disable transcoding system
  ffmpeg_path: ""                 # Auto-detected if empty
  max_workers: 1                   # Maximum concurrent transcoding jobs (1-4)
  replace_original: false          # Delete original after successful transcode
```

**Field Details**:

- **`enabled`**: `true`/`false` - Enable the entire transcoding system
- **`ffmpeg_path`**: Optional path to FFmpeg binary. Leave empty for auto-detection
- **`max_workers`**: Number of concurrent FFmpeg processes (1-4). Start with 1 on RPi 3B
- **`replace_original`**: `true`/`false` - Delete original file after successful transcode. **Use with caution**

### Per-Camera Settings

Individual cameras can have their own transcoding configuration:

```yaml
cameras:
  - id: "front-door"
    name: "Front Door"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    transcoding:
      enabled: true                 # Enable transcoding for this camera
      target_codec: "h264"         # h264 or h265
      preset: "medium"             # ultrafast, faster, medium
      bitrate: "2M"                # e.g., "1M", "2M", "5M"
```

**Field Details**:

- **`enabled`**: `true`/`false` - Override global setting for this camera
- **`target_codec`**: `"h264"` or `"h265"` - Target codec for conversion
- **`preset`**: Speed/quality tradeoff:
  - `"ultrafast"`: Fastest, largest files
  - `"faster"`: Good balance (recommended)
  - `"medium"`: Slower, smaller files
- **`bitrate`**: Target bitrate in format like `"2M"` (2 Mbps) or `"500k"` (500 kbps)

### Performance Presets

| Preset | Speed | Quality | File Size | CPU Usage | Best For |
|--------|-------|---------|-----------|-----------|----------|
| `ultrafast` | Very Fast | Good | Large | High | RT playback, temporary storage |
| `faster` | Fast | Good | Medium | Medium | General purpose, balance |
| `medium` | Medium | Good | Small | Low | Archive storage, long-term |

## Self-Check Explained

The transcoding self-check validates your hardware capabilities and FFmpeg availability:

### What it Checks

- **FFmpeg availability**: Looks for FFmpeg binary on system PATH
- **Encoder support**: Verifies H.264/H.265 encoder support
- **Hardware capabilities**: Estimates max concurrent streams and FPS
- **Memory requirements**: Warns if system has insufficient RAM
- **CPU performance**: Estimates transcoding speed and quality

### Failure Scenarios

**FFmpeg Not Available**:
- FFmpeg will be automatically downloaded on first use
- Monitor download progress via `/api/transcoding/ffmpeg/status`

**Low Memory (<512MB)**:
- Warning: System may become unstable
- Solution: Reduce `max_workers` to 1, avoid `replace_original`

**Low Estimated FPS (<5.0)**:
- Warning: Real-time transcoding may be too slow
- Solution: Use lower resolutions or reduce `max_workers`

**Encoder Not Supported**:
- Error: FFmpeg compiled without H.264/H.265 support
- Solution: Rebuild FFmpeg with encoder support

## Hardware Requirements

| Platform | Max Workers | Recommended FPS | Memory Usage | Notes |
|----------|-------------|----------------|--------------|-------|
| Raspberry Pi 3B | 1 | 1-2 FPS | 512MB+ | Start with 1 worker only |
| Raspberry Pi 4 | 2 | 2-4 FPS | 1GB+ | Suitable for light transcoding |
| Raspberry Pi 5 | 2-3 | 3-6 FPS | 1GB+ | Better performance |
| x86 Server | 4 | 5+ FPS | 2GB+ | Full performance potential |
| Docker | 1-2 | 2-4 FPS | 1GB+ | Depends on host resources |

## Performance Expectations

### Raspberry Pi 3B
- **1 worker**: ~1-2 FPS for 720p, ~1 FPS for 1080p
- **Memory**: ~300-500MB during transcoding
- **Storage**: ~15-30% faster recording speed
- **Recommended**: Use `ultrafast` preset for RT playback

### Raspberry Pi 4/5  
- **2 workers**: ~3-4 FPS for 720p, ~2-3 FPS for 1080p
- **Memory**: ~500MB-1GB per worker
- **Storage**: ~25-40% faster recording speed
- **Recommended**: `faster` preset for general use

### x86 Systems
- **4 workers**: Real-time performance for multiple streams
- **Memory**: ~200-500MB per worker
- **Storage**: Near-real-time performance
- **Recommended**: `medium` preset for file size optimization

## Stored File Transcoding

### Manual Trigger via API

Transcode existing recordings manually:

```bash
# Queue a transcoding job for a specific recording
curl -u admin:password \
  -X POST http://localhost:9090/api/transcoding/tasks \
  -H "Content-Type: application/json" \
  -d '{
    "camera_id": "front-door",
    "recording_id": "1704123456789012345",
    "target_codec": "h264",
    "replace_original": false
  }'
```

### Automatic Transcoding

Enable per-camera transcoding to automatically process new recordings:

```yaml
cameras:
  - id: "front-door"
    name: "Front Door"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    transcoding:
      enabled: true
      target_codec: "h264"
      preset: "faster"
      bitrate: "2M"
```

### Monitor Progress

Check transcoding status:

```bash
# View system status
curl -u admin:password http://localhost:9090/api/transcoding/status

# List all transcoding tasks
curl -u admin:password http://localhost:9090/api/transcoding/tasks

# View FFmpeg download progress
curl -u admin:password http://localhost:9090/api/transcoding/ffmpeg/status
```

## Replace Mode

### How It Works

When `replace_original: true`, the system:

1. Transcodes the original file to a new file
2. Verifies the transcoded file is valid
3. Deletes the original file
4. Updates database references

### Trade-offs

**Benefits**:
- **Storage Savings**: 30-50% reduction in file size
- **Single Source**: Only one version of each recording
- **Cleaner Storage**: No mixed quality files

**Risks**:
- **Data Loss**: Original recording permanently deleted
- **Verification Required**: Must validate transcoded files work
- **No Recovery**: Cannot revert to original if transcoding fails

### Safety Recommendations

1. **Always backup original recordings** before enabling replace mode
2. **Test transcoding** with a single camera first
3. **Monitor task success rates** via API
4. **Keep `replace_original: false`** for production systems
5. **Use `replace_original: true`** only for temporary storage optimization

```yaml
# Safe configuration (recommended for production)
transcoding:
  enabled: true
  max_workers: 1
  replace_original: false  # Keep originals for safety

cameras:
  - id: "front-door"
    transcoding:
      enabled: true
      target_codec: "h264"
      preset: "medium"
      bitrate: "2M"
```

## Monitoring

### Prometheus Metrics

Transcoding metrics are exposed at `/api/metrics`:
- `mibee_nvr_transcoding_queue_length`: Current queue size
- `mibee_nvr_transcoding_active_jobs`: Running FFmpeg processes
- `mibee_nvr_transcoding_total_completed`: Total completed tasks
- `mibee_nvr_transcoding_failed_total`: Failed tasks count

### Web UI Status

Check transcoding status in the Web UI:
- **Settings → Transcoding**: System status and FFmpeg download progress
- **Recordings → Actions**: Transcode button for individual files
- **Queue Status**: Active and pending jobs count

### API Endpoints

- `GET /api/transcoding/check` - Hardware self-check
- `GET /api/transcoding/status` - System status
- `GET /api/transcoding/ffmpeg/status` - FFmpeg progress
- `POST /api/transcoding/ffmpeg/download` - Start download
- `POST /api/transcoding/ffmpeg/download/retry` - Retry download
- `GET /api/transcoding/tasks` - List tasks with pagination
- `POST /api/transcoding/tasks` - Create manual task
- `DELETE /api/transcoding/tasks/{id}` - Cancel task
- `GET /api/transcoding/cameras` - Per-camera configs

## FAQ

### General Questions

**Q: Will transcoding affect recording performance?**
A: Yes, transcoding uses additional CPU and memory. Start with 1 worker and monitor system load.

**Q: Can I transcode while the camera is recording?**
A: Yes, transcoding runs in background processes and won't interrupt recording.

**Q: What happens if transcoding fails?**
A: Failed tasks remain in the queue. Check logs for FFmpeg error details. Retry manually or fix the issue.

### Technical Questions

**Q: Why do I need to download FFmpeg?**
A: FFmpeg provides the video processing capabilities. MiBee NVR includes a downloader to get the appropriate version for your platform.

**Q: What's the difference between transcoding and merging?**
A: Transcoding changes video codec/format, while merging combines multiple segments into fewer files.

**Q: Can I transcode H.265 to H.264 on Raspberry Pi?**
A: Yes, but performance will be limited. Use `ultrafast` preset and 1 worker only.

### Performance Questions

**Q: How much storage space will transcoding save?**
A: Typically 30-50% for H.265→H.264 conversion, depending on source quality and bitrate settings.

**Q: What's the optimal number of workers?**
A: Start with 1 worker and increase only if CPU usage stays below 80% during transcoding.

**Q: Will transcoding work on a Raspberry Pi 3B?**
A: Yes, but expect reduced performance. Use 1 worker and simple presets for best results.

### Troubleshooting

**FFmpeg Issues**:
- Check download progress: `GET /api/transcoding/ffmpeg/status`
- Retry failed downloads: `POST /api/transcoding/ffmpeg/download/retry`
- Verify FFmpeg path: Check `ffmpeg_path` in config

**Performance Issues**:
- Reduce `max_workers` if CPU usage >80%
- Use `ultrafast` preset for RPi systems
- Disable `replace_original` if disk space is critical

**Queue Issues**:
- Check task status: `GET /api/transcoding/tasks`
- Cancel stuck tasks: `DELETE /api/transcoding/tasks/{id}`
- Verify camera has transcoding enabled

For more detailed troubleshooting, check the [Configuration](configuration.md) and [API Reference](api-reference.md) documents.