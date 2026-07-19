# Timelapse Recording

Timelapse functionality creates time-lapse videos from camera recordings, compressing hours or days into minutes. MiBee NVR supports flexible merge durations, H.264/H.265 dual-mode support, and a unified recordings interface.

## Overview

The timelapse system automatically merges video segments into compressed timelapse recordings. Key features:

- **Merge Durations up to 1h**: Hourly merge intervals keep timezone behavior predictable (see the note below on the 1h cap)
- **H.264/H.265 Dual-Mode**: Any RTSP camera can generate timelapse recordings without additional hardware
- **Unified Interface**: Integrated recordings page with table, gallery, and calendar view modes
- **Keyframe Extraction**: Zero-overhead timelapse generation using existing RTSP streams

## Configuration

### Basic Timelapse Setup

Enable timelapse recording for a camera in the configuration:

```yaml
cameras:
  - name: "Front Door"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true
    
    # Timelapse configuration
    timelapse:
      enabled: true
      merge_duration: "1h"             # merge interval, capped at 1h (see below)
      frame_source: "rtsp_keyframe"  # dual-mode keyframe extraction
      output_fps: 30
```

### Dual-Mode Configuration (RTSP Cameras)

For existing RTSP cameras, enable dual-mode timelapse without changing the camera protocol:

```yaml
cameras:
  - name: "Living Room Camera"
    protocol: "rtsp"
    encoding: "h265"
    url: "rtsp://192.168.1.101:554/stream"
    enabled: true

    timelapse:
      enabled: true                          # Enable timelapse
      merge_duration: "30m"                  # Merge every 30 minutes
      frame_source: "rtsp_keyframe"         # Extract from RTSP stream
      output_fps: 30
```

### Standalone Timelapse Configuration

Create dedicated timelapse cameras with separate RTSP sources:

```yaml
cameras:
  - name: "Timelapse-Only Camera"
    protocol: "timelapse"
    encoding: "h264"
    url: "rtsp://backup-camera.example.com:554/stream"
    enabled: true

    timelapse:
      enabled: true
      merge_duration: "1h"                  # Maximum merge interval
      frame_source: "rtsp_keyframe"         # Extract from timelapse stream
      output_fps: 15                         # Lower fps for longer durations
```

## Merge Duration Options

The `merge_duration` field controls how often captured keyframes are merged into a single timelapse file.

**Important: merge_duration is capped at 1h.** Any value greater than 1h is not honored — the merge window is capped at 1h. The cap was added because multi-hour windows align to local midnight and therefore cross a UTC day boundary; once storage/queries mix UTC and the user's timezone, a multi-hour window both amplifies IO (it touches two UTC day-partitions) and mis-buckets recordings across the boundary. A 1h window never crosses a natural-day boundary regardless of timezone. "Watch a whole day" is handled by client-side continuous playback (the recordings detail view auto-advances to the next segment), not by synthesizing a large file server-side.

Concretely:

- Valid values are any Go duration string up to and including `1h` (e.g. `"5m"`, `"10m"`, `"15m"`, `"30m"`, `"1h"`). The empty string defaults to `"1h"`.
- The legacy multi-hour strings (`"8h"`, `"12h"`, `"24h"`, `"natural-day"`, `"7d"`, `"30d"`) are accepted for backward compatibility but are **silently clamped to 1h** with a warning in the logs. Existing configs upgrade without breaking.
- Any other Go duration string greater than `1h` (e.g. `"2h"`, `"90m"`) is rejected at config validation with an error.

### Configuration Examples

```yaml
# Hourly merges (the maximum)
timelapse:
  enabled: true
  merge_duration: "1h"
  output_fps: 30

# Half-hourly merges
timelapse:
  enabled: true
  merge_duration: "30m"
  output_fps: 10

# 15-minute merges for finer-grained clips
timelapse:
  enabled: true
  merge_duration: "15m"
  output_fps: 5
```

## Dual-Mode Timelapse

Dual-mode timelapse allows any RTSP camera to generate timelapse recordings without additional hardware requirements.

### How It Works

1. **Primary RTSP Stream**: Camera records normal video segments as usual
2. **Keyframe Extraction**: KeyframeExtractor subscribes to the RTSP StreamHub
3. **Frame Processing**: Extracts IDR frames (H.264 type 5, H.265 type 19/20) from the stream
4. **Timelapse Generation**: Extracted frames are processed into compressed timelapse videos

### Supported Camera Types

- **RTSP H.264**: Standard IP cameras with H.264 encoding
- **RTSP H.265**: Modern cameras with H.265 encoding for better efficiency
- **ONVIF**: Auto-discovered cameras, both H.264 and H.265 streams supported

### H.265 Support

The system automatically detects H.265 streams and configures the KeyframeExtractor appropriately:

```yaml
# ONVIF camera with H.265 stream
cameras:
  - name: "Security Camera 1"
    protocol: "onvif"
    encoding: "h265"                    # Primary encoding
    stream_encoding: "H265"            # ONVIF-specific field
    url: "onvif://192.168.1.102"
    enabled: true

    timelapse:
      enabled: true
      merge_duration: "1h"
      frame_source: "rtsp_keyframe"
```

## Unified Recordings Interface

MiBee NVR merges timelapse and regular recordings into a unified Library page with enhanced navigation and filtering capabilities.

### View Modes

Access different views through the URL hash parameters:

- **Table View**: `#/recordings?view=table` - Detailed list with metadata
- **Gallery View**: `#/recordings?view=gallery` - Thumbnail grid layout
- **List View**: `#/recordings?view=list` - Compact list layout

### Format Filters

Filter recordings by format using the `format` parameter:

- **All Formats**: `format=all` - Show all recording types
- **Video Only**: `format=video` - Show regular video recordings
- **Timelapse Only**: `format=timelapse` - Show timelapse recordings only
- **MJPEG Only**: `format=mjpeg` - Show MJPEG recordings only

Primary navigation format filter pills are always visible in the interface, allowing quick switching between recording formats.

### Gallery View

```bash
# URL: /#recordings?view=gallery&format=all
```

Displays recordings in a responsive grid layout with:

- Thumbnail previews
- Date/time labels
- Format badges (video/timelapse/mjpeg)
- Lazy loading for performance
- Click to view/download recordings

### List View

```bash
# URL: /#recordings?view=list&format=all
```

Provides a compact list view with:

- Recording metadata
- Duration and file size information
- Format indicators
- Quick download buttons
- Search and filter capabilities

### Calendar View

```bash
# URL: /#recordings?view=calendar&format=all
```

Provides calendar-based navigation with:

- Month/week/day views
- Recording density visualization
- Format-specific filtering
- Click dates to filter recordings
- Timeline navigation controls

### Timeline Bar

Above the view mode tabs, the timeline bar is always visible and provides:

- Horizontal timeline showing recording density
- Time range selector (week/month/3months)
- Format filter integration
- Clickable navigation between time periods
- Visual indicators for recording availability

## Migration Guide

### From the legacy `daily_merge` field

#### 1. Update Configuration

**Before:**

```yaml
timelapse:
  enabled: true
  daily_merge: true
  output_fps: 30
```

**After:**

```yaml
timelapse:
  enabled: true
  merge_duration: "1h"             # capped at 1h (was daily_merge)
  frame_source: "rtsp_keyframe"
  output_fps: 30
```

#### 2. Merge Duration Options

If you want different merge intervals:

```yaml
# Half-hourly merges
timelapse:
  enabled: true
  merge_duration: "30m"
  frame_source: "rtsp_keyframe"
  output_fps: 30
```

#### 3. Dual-Mode Migration for Existing RTSP Cameras

Enable timelapse on existing RTSP cameras without changing their configuration:

```yaml
# Before: Only regular recording
cameras:
  - name: "Existing Camera"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true

# After: Add timelapse to existing camera
cameras:
  - name: "Existing Camera"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true

    timelapse:                     # Add this section
      enabled: true
      merge_duration: "1h"
      frame_source: "rtsp_keyframe"  # dual-mode
      output_fps: 30
```

### Backward Compatibility

- **Existing cameras continue working** without changes
- **Legacy `daily_merge` field** still works but is deprecated
- **Legacy multi-hour `merge_duration` values** (`8h`/`12h`/`24h`/`natural-day`/`7d`/`30d`) are silently clamped to 1h
- **Existing timelapse recordings** remain accessible in the unified interface
- **API endpoints** maintain compatibility with existing integrations

### Migration Checklist

1. [ ] Review existing camera configurations
2. [ ] Add `timelapse.enabled: true` to desired RTSP cameras
3. [ ] Set appropriate `merge_duration` (default: "1h", max: "1h")
4. [ ] Test dual-mode functionality with sample cameras
5. [ ] Verify unified recordings interface works
6. [ ] Check that existing recordings are still accessible

## Troubleshooting

### Common Issues

#### 1. Keyframe Extraction Not Working

**Symptom**: Timelapse recordings empty or missing frames

**Solution**: Verify camera encoding and stream configuration:

```bash
# Check if camera supports keyframe extraction
curl -u admin:password "http://localhost:9090/api/cameras/camera-id/status"
```

Ensure H.264/H.265 encoding is correctly specified in the camera configuration.

#### 2. Merge Duration Issues

**Symptom**: Merges not running at expected intervals

**Solution**: Check merge logs and verify duration format:

```bash
# Check merge manager status
curl -u admin:password "http://localhost:9090/api/timelapse/status"

# Verify duration format in config
grep "merge_duration" /path/to/config.yaml
```

Valid values: any Go duration up to and including `1h` (e.g. `5m`, `15m`, `30m`, `1h`). The legacy strings `8h`/`12h`/`24h`/`natural-day`/`7d`/`30d` are silently clamped to `1h`; any other value greater than `1h` is rejected.

#### 3. Dual-Mode Camera Setup

**Symptom**: Dual-mode camera not generating timelapse recordings

**Solution**: Verify the dual-mode configuration:

```yaml
# Correct dual-mode setup
cameras:
  - name: "Dual-Mode Camera"
    protocol: "rtsp"                    # Must be rtsp/onvif
    encoding: "h264"                    or "h265"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true
    
    timelapse:
      enabled: true                      # Must be enabled
      merge_duration: "1h"              # Set duration (max 1h)
      frame_source: "rtsp_keyframe"       # Keyframe source
      output_fps: 30
```

#### 4. ONVIF Stream Encoding

**Symptom**: ONVIF camera H.265 timelapse not working

**Solution**: Check both `encoding` and `stream_encoding` fields:

```yaml
cameras:
  - name: "ONVIF H.265"
    protocol: "onvif"
    encoding: "h265"
    stream_encoding: "H265"  # ONVIF-specific field
    url: "onvif://192.168.1.102"
    enabled: true
    
    timelapse:
      enabled: true
      merge_duration: "1h"
      frame_source: "rtsp_keyframe"
```

### Debug Commands

```bash
# Check timelapse manager status
curl -u admin:password "http://localhost:9090/api/timelapse/status"

# List all recordings (timelapse + regular)
curl -u admin:password "http://localhost:9090/api/recordings"

# Check camera timelapse configuration  
curl -u admin:password "http://localhost:9090/api/cameras/camera-id"

# View merge logs (if available)
journalctl -u mibee-nvr -f | grep merge
```

## Performance Considerations

### Memory Usage

- **Keyframe extraction** uses minimal memory (no video decoding)
- **Merge operations** use temporary files with 1MB buffer
- **RPi 3B compatible**: Max 512MB memory budget

### Storage Requirements

- **Timelapse files** are typically 90-95% smaller than original footage
- **Merge duration** affects file sizes:
  - 30m merges: smaller, more frequent clips
  - 1h merges (the maximum): larger hourly clips

### Network Impact

- **Dual-mode** uses no additional network bandwidth
- **Keyframe extraction** works with existing RTSP streams
- **Web interface** loads efficiently with lazy loading

## API Reference

### Timelapse Endpoints

#### Get Timelapse Status

```bash
GET /api/timelapse/status
```

Response includes global timelapse settings and merge status.

#### Trigger Manual Merge

```bash
POST /api/timelapse/merge
```

Optional query parameter `duration` for specific time windows.

#### List Recordings

```bash
GET /api/recordings?format=timelapse
```

List timelapse recordings. Use `view=gallery|list&format=timelapse` in the web interface, or access the unified Library page at `#/recordings?format=timelapse`.

### Configuration API

Update camera timelapse configuration:

```bash
PUT /api/cameras/camera-id
{
  "timelapse": {
    "enabled": true,
    "merge_duration": "1h",
    "frame_source": "rtsp_keyframe",
    "output_fps": 30
  }
}
```

## Best Practices

### Configuration Tips

1. **Choose appropriate merge durations** based on your use case (remember the 1h cap):
   - Security monitoring: `1h` for frequent reviewable clips
   - Finer-grained clips: `30m` or `15m`
   - Lower output FPS to keep longer-interval clips small

2. **Optimize output FPS**:
   - 30 FPS: Real-time events
   - 15 FPS: Frequent summaries
   - 5 FPS: Compact overviews

3. **For "watch a whole day" use cases**, rely on client-side continuous playback in the recordings detail view (it auto-advances to the next segment) rather than synthesizing a single multi-hour file.

### Dual-Mode Setup

1. **Test with one camera** first before enabling on all cameras
2. **Monitor storage** usage for the increased recording volume
3. **Verify camera encoding** is correctly specified (H.264/H.265)
4. **Check stream encoding** for ONVIF cameras

### Performance Monitoring

1. **Regular maintenance**: Clean up old timelapse recordings based on retention policies
2. **Storage monitoring**: Watch for available disk space, especially with long-duration merges
3. **System resources**: Monitor memory usage during merge operations on resource-constrained devices

## Related Documentation

- [Configuration Reference](configuration.md)
- [Camera Guide](camera-guide.md)
- [API Reference](api-reference.md)
- [Troubleshooting](troubleshooting.md)
