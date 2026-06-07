# Timelapse Recording

Timelapse functionality creates time-lapse videos from camera recordings, compressing hours or days into minutes. MiBee NVR v2 introduces major improvements with flexible merge durations, H.264/H.265 dual-mode support, and a unified recordings interface.

## Overview

The timelapse system automatically merges video segments into compressed timelapse recordings. Key improvements in v2:

- **Flexible Merge Durations**: Support for 8h, 12h, 24h, natural-day, 7d, and 30d intervals
- **H.264/H.265 Dual-Mode**: Any RTSP camera can now generate timelapse recordings without additional hardware
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
    
    # Timelapse configuration (v2 features)
    timelapse:
      enabled: true
      merge_duration: "natural-day"  # v2: flexible merge intervals
      frame_source: "rtsp_keyframe"  # v2: dual-mode keyframe extraction
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
      merge_duration: "24h"                  # Merge every 24 hours
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
    
    timlapse:
      enabled: true
      merge_duration: "7d"                  # Weekly merges
      frame_source: "rtsp_keyframe"         # Extract from timelapse stream
      output_fps: 15                         # Lower fps for longer durations
```

## Merge Duration Options (v2)

The `merge_duration` field supports flexible intervals for different use cases:

| Duration | Description | Alignment | Use Case |
|----------|-------------|-----------|----------|
| `8h` | 8-hour merges | 00:00, 08:00, 16:00 UTC | Business hours, shift changes |
| `12h` | 12-hour merges | 00:00, 12:00 UTC | Day/night cycles, AM/PM segments |
| `24h` | 24-hour merges | 00:00 UTC daily | Daily overview, security review |
| `natural-day` | Natural day (0-24h) | Local time | User-friendly daily summary |
| `7d` | Weekly merges | Monday 00:00 UTC | Weekly reviews, pattern analysis |
| `30d` | Monthly merges | 1st of month 00:00 UTC | Monthly reports, long-term analysis |

### Configuration Examples

```yaml
# 8-hour business monitoring
timelapse:
  enabled: true
  merge_duration: "8h"
  output_fps: 30

# Daily summary with natural day
timelapse:
  enabled: true
  merge_duration: "natural-day"
  output_fps: 10

# Weekly pattern analysis
timelapse:
  enabled: true
  merge_duration: "7d"
  output_fps: 5

# Monthly reports
timelapse:
  enabled: true
  merge_duration: "30d"
  output_fps: 2
```

## Dual-Mode Timelapse (v2)

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
      merge_duration: "24h"
      frame_source: "rtsp_keyframe"
```

## Unified Recordings Interface (v2)

The v2 release merges timelapse and regular recordings into a unified interface with multiple view modes.

### View Modes

Access different views through the URL hash parameters:

- **Table View**: `#/recordings?view=table` - Detailed list with metadata
- **Gallery View**: `#/recordings?view=gallery` - Thumbnail grid layout
- **Calendar View**: `#/recordings?view=calendar` - Calendar-based navigation

### Gallery View

```bash
# URL: /#recordings?view=gallery
```

Displays timelapse recordings in a responsive grid layout with:

- Thumbnail previews
- Date/time labels
- Lazy loading for performance
- Click to view/download recordings

### Calendar View

```bash
# URL: /#recordings?view=calendar  
```

Provides calendar-based navigation with:

- Month/week/day views
- Recording density visualization
- Click dates to filter recordings
- Timeline navigation controls

### Timeline Bar

Above the view mode tabs when viewing timelapse recordings:

- Horizontal timeline showing recording density
- Time range selector (week/month/3months)
- Clickable navigation between time periods
- Visual indicators for recording availability

<!-- TODO: Add screenshot of unified Recordings page -->

## Migration Guide

### From Timelapse v1 to v2

#### 1. Update Configuration

**Before (v1):**

```yaml
timelapse:
  enabled: true
  daily_merge: true
  output_fps: 30
```

**After (v2):**

```yaml
timelapse:
  enabled: true
  merge_duration: "natural-day"  # v2 field
  frame_source: "rtsp_keyframe"   # v2 field
  output_fps: 30
```

#### 2. New Merge Duration Options

If you want different merge intervals:

```yaml
# Change from daily to 8-hour merges
timelapse:
  enabled: true
  merge_duration: "8h"            # v2: flexible intervals
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
      merge_duration: "natural-day"
      frame_source: "rtsp_keyframe"  # v2 dual-mode
      output_fps: 30
```

### Backward Compatibility

- **Existing cameras continue working** without changes
- **Legacy `daily_merge` field** still works but is deprecated
- **Existing timelapse recordings** remain accessible in the unified interface
- **API endpoints** maintain compatibility with existing integrations

### Migration Checklist

1. [ ] Review existing camera configurations
2. [ ] Add `timelapse.enabled: true` to desired RTSP cameras
3. [ ] Set appropriate `merge_duration` (default: "natural-day")
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

Valid values: `8h`, `12h`, `24h`, `natural-day`, `7d`, `30d`

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
      merge_duration: "24h"             # Set duration
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
      merge_duration: "24h"
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
  - 8h merges: ~50-100MB per hour of footage
  - 24h merges: ~200-400MB per day
  - 7d merges: ~1-2GB per week

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

List timelapse recordings. Use `view=gallery|calendar` in the web interface.

### Configuration API

Update camera timelapse configuration:

```bash
PUT /api/cameras/camera-id
{
  "timelapse": {
    "enabled": true,
    "merge_duration": "24h",
    "frame_source": "rtsp_keyframe",
    "output_fps": 30
  }
}
```

## Best Practices

### Configuration Tips

1. **Choose appropriate merge durations** based on your use case:
   - Security monitoring: 8h or 24h for daily review
   - Business analytics: 7d for weekly patterns
   - Long-term storage: 30d for monthly reports

2. **Optimize output FPS**:
   - 30 FPS: Real-time events
   - 15 FPS: Daily summaries
   - 5 FPS: Weekly overviews
   - 2 FPS: Monthly reports

3. **Use natural-day** for user-friendly daily summaries aligned with local time

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
