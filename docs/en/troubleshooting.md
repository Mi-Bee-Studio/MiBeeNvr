# Troubleshooting Guide

This guide helps you diagnose and resolve common issues with MiBee NVR. If you can't find a solution here, check the [Configuration Reference](configuration.md) or search existing issues on GitHub.

## Common Issues

### Health Check Fails

#### Database Connection Issues
**Symptom**: Health check shows `"database": {"status": "error", "message": "database is closed"}`
```json
{
  "status": "error",
  "checks": {
    "database": {"status": "error", "message": "database is closed"}
  }
}
```

**Solution**:
1. Check if the storage directory exists and is writable:
   ```bash
   ls -la /var/lib/mibee-nvr/
   sudo chown -R nvr:nvr /var/lib/mibee-nvr/
   ```
2. Verify the database file isn't corrupted:
   ```bash
   ls -la /var/lib/mibee-nvr/mibee-nvr.db
   file /var/lib/mibee-nvr/mibee-nvr.db
   ```
3. Try reinitializing the database:
   ```bash
   mv /var/lib/mibee-nvr/mibee-nvr.db /var/lib/mibee-nvr/mibee-nvr.db.backup
   ./mibee-nvr -config mibee-nvr.yaml
   ```

#### Storage Space Issues
**Symptom**: Health check shows `"storage": {"status": "error", "message": "disk space critically low"}`
```json
{
  "status": "error", 
  "checks": {
    "storage": {"status": "error", "message": "disk space critically low"}
  }
}
```

**Solution**:
1. Check disk usage:
   ```bash
   df -h
   du -sh /var/lib/mibee-nvr/recordings/
   ```
2. Clean up old recordings:
   ```bash
   find /var/lib/mibee-nvr/recordings/ -type f -mtime +30 -delete
   ```
3. Adjust retention days in configuration:
   ```yaml
   cleanup:
     retention_days: 7  # Reduce from 30 to 7 days
     disk_threshold_percent: 90  # Lower from 95 to 90
   ```

## FFmpeg / Transcoding

### Is FFmpeg required?

**No.** FFmpeg is an **optional dependency**, used only for H.265↔H.264 transcoding
(storage optimization + live relay transcoding). All other features (recording,
playback, live streaming, relay, timelapse, merge) are pure Go and **do not need FFmpeg**.

- **Without FFmpeg**: The NVR is fully functional; only transcoding is disabled
  (startup log shows "Transcoding disabled").
- **With FFmpeg**: Transcoding is auto-enabled (H.265→H.264 for old-device compat,
  H.264→H.265 to save storage).
- Docker images ship FFmpeg built-in for out-of-the-box transcoding.
- Bare-metal install (`install.sh`) attempts to install FFmpeg automatically;
  failure is non-blocking.

### How to install FFmpeg manually?

```bash
# Debian/Ubuntu
sudo apt-get install -y ffmpeg

# RHEL/CentOS/Fedora
sudo dnf install -y ffmpeg

# Alpine
sudo apk add ffmpeg
```

Or use the in-app downloader: **Settings → Transcoding → Download FFmpeg**
(downloads a static binary into the data directory).

## Camera Issues

### Camera Not Found

#### Authentication Failed
**Symptom**: Error `"authentication failed: invalid username or password"` or `"camera authentication failed"`

**Solution**:
1. Test camera connection manually:
   ```bash
   # For RTSP
   ffprobe -rtsp_transport tcp "rtsp://username:password@192.168.1.100:554/stream"
   
   # For HTTP JPEG
   curl -I "http://username:password@192.168.1.100/capture"
   ```
2. Verify camera credentials in configuration
3. Check if camera is accessible from the server:
   ```bash
   ping 192.168.1.100
   nc -zv 192.168.1.100 554
   ```

#### URL Format Issues
**Symptom**: Error `"camera URL has invalid format"` or connection timeouts

**Solution**:
1. Ensure URL has correct format:
   ```yaml
   # RTSP - must include port
   url: "rtsp://192.168.1.100:554/stream"
   
   # HTTP - must include path to capture
   url: "http://192.168.1.100/capture"
   url: "http://192.168.1.100:8080/cgi-bin/snapshot.cgi"
   ```
2. Try different transport methods for RTSP:
   ```yaml
   # Try TCP first (more reliable)
   protocol: "rtsp"
   # Try UDP if TCP fails
   ```

### Camera Recording Issues

#### Camera Shows "No Signal" Status
**Symptom**: Camera is enabled but status shows `"status": "no_signal"`

**Solution**:
1. Check camera logs for connection errors:
   ```bash
   journalctl -u mibee-nvr -f
   ```
2. Test camera stream manually:
   ```bash
   # View RTSP stream
   ffplay -rtsp_transport tcp "rtsp://username:password@192.168.1.100:554/stream"
   
   # Test HTTP JPEG
   curl -o test.jpg "http://username:password@192.168.1.100/capture"
   file test.jpg
   ```
3. Try adjusting segment duration for problematic cameras:
   ```yaml
   storage:
     segment_duration: "15s"  # Shorter segments for unstable cameras
   ```

#### Camera Shows "Disabled" Status
**Symptom**: Camera shows `"enabled": true` but status is `"disabled"`

**Solution**:
1. Check for configuration validation errors:
   ```bash
   ./mibee-nvr -config mibee-nvr.yaml --validate
   ```
2. Verify all required camera fields are present:
   ```yaml
   cameras:
     - id: "cam1"
       name: "Camera 1"
       protocol: "rtsp"
       encoding: "h264"
       url: "rtsp://..."  # Required for rtsp/http
       enabled: true
   ```
3. Check for duplicate camera IDs:
   ```bash
   grep -r "id:" mibee-nvr.yaml
   ```

#SX|## Live View Issues
#SY|
#HB|### Dashboard or Live View Shows Loading Indefinitely
#SY|
#XW|**Symptom**: Dashboard camera grid or Live View page shows "Buffering" or "Loading..." indefinitely. Video never starts playing.
#PR|
#SY|**Root Cause**: HLS.js 1.6+ uses `fetch` API by default instead of XHR. If auth headers are not injected into fetch requests, the server returns 401 Unauthorized, and the stream cannot load.
#PR|
#SY|**Solution**:
#HB|1. Ensure you are running MiBee NVR v0.6.0 or later, which includes the `fetchSetup` auth fix
#RP|2. Check browser console (F12) for 401 errors on `.m3u8` or `.ts` requests
#WW|3. Verify the camera is recording (status = "Recording") before attempting live view
#SR|4. For cameras in "Reconnecting" state, wait for the camera to reconnect — HLS requires an active recording stream
#SY|
#RM|### Stream Shows "SPS/PPS Not Available" Error (503)
#SY|
#XW|**Symptom**: HLS endpoint returns HTTP 503 with message "SPS/PPS not available yet"
#PR|
#SY|**Solution**:
#HB|1. This is normal for the first few seconds after camera starts recording — the video encoder needs to produce keyframe data
#RP|2. The frontend automatically retries with exponential backoff (5s, 10s, 20s, 40s)
#WW|3. If the error persists for more than 60 seconds, check:
#SR|   - Camera is actually streaming video (test with `ffprobe`)
#XW|   - Recording is active (check camera status via API)
#SY|   ```bash
#HB|   curl -u admin:password http://localhost:9090/api/cameras/{id}/stream/index.m3u8
#WW|   ```
#SY|
#HB|### Stream Plays in Live View but Not in Dashboard
#SY|
#XW|**Symptom**: Individual camera Live View works, but Dashboard grid shows all cameras stuck in "Buffering"
#PR|
#SY|**Solution**:
#HB|1. Dashboard loads multiple streams simultaneously — check `hls.max_streams` setting (default: 4)
#RP|2. Reduce Dashboard camera count (max 4, fewer is better on RPi)
#WW|3. Use sub-stream URLs for Dashboard to reduce bandwidth:
#SR|   ```yaml
#HB|   cameras:
#XW|     - id: "cam1"
#SY|       sub_stream_url: "rtsp://192.168.1.100:554/sub"  # Lower resolution stream
#WW|       hls_max_fps: 10  # Limit frame rate for Dashboard
#SR|   ```
#WW|4. On low-power devices (RPi 3B), limit Dashboard to 2 HLS cameras
#SY|
#RM|### Maximum Concurrent Streams Reached
#SY|
#XW|**Symptom**: Stream fails to start with error "maximum concurrent HLS streams reached"
#PR|
#SY|**Solution**:
#HB|1. Close unused Dashboard or Live View tabs
#RP|2. Increase `max_streams` if your hardware can handle it:
#WW|   ```yaml
#SR|   hls:
#HB|     max_streams: 6  # Increase from default 4
#WW|   ```
#SR|3. Use snapshot thumbnails instead of live streams for some cameras:
#SY|   ```yaml
#HB|   cameras:
#XW|     - id: "cam-low-priority"
#SY|       snapshot_url: "http://192.168.1.100/snapshot"
#WW|   ```
#PR|
#JB|
#NX|## Recording Issues
#SY|
### ONVIF Camera Issues

#### ONVIF Discovery Fails
**Symptom**: Cannot discover ONVIF cameras or `"ONVIF not camera"` errors

**Solution**:
1. Test ONVIF discovery manually:
   ```bash
   onvif-discover
   ```
2. Verify camera supports ONVIF:
   - Check camera manufacturer documentation
   - Ensure ONVIF service is enabled on the camera
3. Try different discovery methods:
   ```yaml
   # Use specific IP range
   onvif:
     discover:
       timeout: 10
       target: "192.168.1.0/24"
   ```

#### ONVIF Profile Issues
**Symptom**: `"ONVIF no profiles"` or PTZ control not working

**Solution**:
1. Get available profiles:
   ```bash
   curl -u admin:password http://localhost:9090/api/cameras/cam-id/onvif/profiles
   ```
2. Manually specify profile token:
   ```yaml
   cameras:
     - id: "onvif-cam"
       protocol: "onvif"
       profile_token: "profile_1"  # Use specific profile
       stream_encoding: "H264"
   ```

### Xiaomi Camera Issues

#### Xiaomi Authentication Fails
**Symptom**: `"xiaomi authentication failed"` errors

**Solution**:
1. Test Xiaomi authentication manually:
   ```bash
   curl -X POST http://localhost:9090/api/xiaomi/auth \
     -H "Content-Type: application/json" \
     -d '{"username": "your-email@example.com", "password": "your-password"}'
   ```
2. Verify Xiaomi account:
   - Check if account has Xiaomi devices
   - Ensure account is in correct region
   - Try re-authenticating
3. Check Xiaomi device status:
   ```bash
   curl -u admin:password http://localhost:9090/api/xiaomi/devices
   ```

#### Xiaomi Device Not Found
**Symptom**: Xiaomi camera shows `"online": false` or cannot connect

**Solution**:
1. Verify Xiaomi device ID:
   ```bash
   # List devices to find correct DID
   curl -u admin:password http://localhost:9090/api/xiaomi/devices
   ```
2. Check device compatibility:
   - Only Xiaomi CS2 cameras are supported
   - Verify device model is supported
3. Try manual sync:
   ```bash
   curl -X POST -u admin:password http://localhost:9090/api/xiaomi/sync
   ```

## Recording Issues

### No Recordings Created

#### No Disk Space
**Symptom**: Recording directory empty but cameras are active

**Solution**:
1. Check disk space:
   ```bash
   df -h
   du -sh /var/lib/mibee-nvr/
   ```
2. Clean up disk space:
   ```bash
   # Find and delete old recordings
   find /var/lib/mibee-nvr/recordings/ -type f -mtime +30 -delete
   
   # Clean up snapshots
   find /var/lib/mibee-nvr/snapshots/ -type f -mtime +7 -delete
   ```
3. Lower disk threshold:
   ```yaml
   cleanup:
     disk_threshold_percent: 90  # Lower from 95
   ```

#### Permission Issues
**Symptom**: Recordings not written to disk

**Solution**:
1. Check directory permissions:
   ```bash
   ls -la /var/lib/mibee-nvr/
   ls -la /var/lib/mibee-nvr/recordings/
   ```
2. Fix permissions:
   ```bash
   sudo chown -R nvr:nvr /var/lib/mibee-nvr/
   sudo chmod -R 755 /var/lib/mibee-nvr/
   ```
3. Check if disk is mounted correctly:
   ```bash
   mount | grep mibee-nvr
   df -h /var/lib/mibee-nvr/
   ```

### Corrupted Recordings

#### Timeline Shows Missing Recordings (duration=0 segments)
**Symptom**: The timeline shows gaps or missing recordings for certain days, even though the camera was recording continuously. The database has recording rows but they appear as zero-width blips or don't show at all.

**Cause**: A historical bug caused some recording segments to be written to the database with `duration=0` and `ended_at = started_at`. The video files on disk are intact, but the metadata is wrong, so the timeline can't render them.

**Solution**: Use the `repair duration` CLI tool to re-probe the actual video file durations and restore correct metadata:

```bash
# 1. Check how many recordings are affected (dry run, no changes)
./mibee-nvr repair duration --config mibee-nvr.yaml --dry-run

# 2. Fix them (updates duration + ended_at in the DB)
./mibee-nvr repair duration --config mibee-nvr.yaml --execute

# 3. For recordings that can't be repaired (corrupt/empty files), delete them:
./mibee-nvr repair duration --config mibee-nvr.yaml --prune --execute

# Optional: only process a specific camera or limit the count
./mibee-nvr repair duration --camera cam-front-door --limit 100 --execute
```

The tool uses pure-Go MP4 box parsing (`mediaprobe`) — no ffprobe required. For large files it uses `FastProbeDuration` which reads only the `stts` box (~100× faster than full parsing). MJPEG frame directories (ESP32 MiBeeCam) are supported via frame-count estimation.

#### MP4 Files Won't Play
**Symptom**: Recordings created but cannot be played with media players

**Solution**:
1. Check file integrity:
   ```bash
   file /var/lib/mibee-nvr/recordings/h264/cam_1704123456789012345.mp4
   ffprobe -v quiet -show_format -show_streams /var/lib/mibee-nvr/recordings/h264/cam_1704123456789012345.mp4
   ```
2. Adjust segment duration:
   ```yaml
   storage:
     segment_duration: "30s"  # Use standard duration
   ```
3. Check for segment merge issues:
   ```bash
   # Check merge status
   curl -u admin:password http://localhost:9090/api/merge/status
   
   # Check pending segments
   curl -u admin:password http://localhost:9090/api/merge/pending
   ```

### High Memory Usage

#### Camera Consumes Too Much RAM
**Symptom**: System becomes unresponsive or OOM killer activates

**Solution**:
1. Check memory usage:
   ```bash
   free -h
   ps aux | grep mibee-nvr
   ```
2. Reduce segment duration:
   ```yaml
   storage:
     segment_duration: "15s"  # Shorter segments = less RAM usage
   ```
3. Enable sub-streams for live preview:
   ```yaml
   cameras:
     - id: "cam1"
       sub_stream_url: "rtsp://192.168.1.100:554/low"  # Lower bandwidth stream
   ```
4. Limit FPS for MJPEG cameras:
   ```yaml
   cameras:
     - id: "mjpeg-cam"
       sample_interval: 2  # Sample every 2 seconds
       hls_max_fps: 15      # Limit to 15 FPS
   ```

## Network Issues

### Port Conflicts

#### Port Already in Use
**Symptom**: Cannot start server, port already bound

**Solution**:
1. Check which process is using the port:
   ```bash
   sudo netstat -tulpn | grep :9090
   sudo lsof -i :9090
   ```
2. Change port in configuration:
   ```yaml
   server:
     listen: ":8080"  # Use different port
   ```
3. Kill conflicting process:
   ```bash
   sudo kill -9 <PID>
   ```

### Firewall Issues

#### Cannot Access Web UI
**Symptom**: External connections to web UI fail

**Solution**:
1. Check firewall status:
   ```bash
   sudo ufw status
   sudo iptables -L -n
   ```
2. Open required ports:
   ```bash
   # For Ubuntu/Debian
   sudo ufw allow 9090/tcp
   sudo ufw allow 2121/tcp  # FTP
   sudo ufw allow 5005/tcp  # WebDAV (if enabled)
   
   # For CentOS/RHEL
   sudo firewall-cmd --permanent --add-port=9090/tcp
   sudo firewall-cmd --reload
   ```
3. Check reverse proxy configuration (if used):
   ```nginx
   # Caddy example
   reverse_proxy localhost:9090
   ```

## Performance Issues

### High CPU Usage

#### Too Many Cameras
**Symptom**: High CPU usage affecting system performance

**Solution**:
1. Monitor CPU usage:
   ```bash
   top -p $(pgrep mibee-nvr)
   htop -p $(pgrep mibee-nvr)
   ```
2. Reduce concurrent camera processing:
   - Disable unnecessary cameras
   - Use sub-streams for live viewing
   - Increase sample intervals for MJPEG cameras
3. Optimize segment merging:
   ```yaml
   merge:
     batch_limit: 100  # Reduce from 200
     check_interval: "2h"  # Less frequent checks
   ```

#### Too Many Concurrent Streams
**Symptom**: High CPU during live viewing

**Solution**:
1. Limit HLS streams:
   ```yaml
   hls:
     max_streams: 2  # Reduce from 4
   ```
2. Use snapshot thumbnails instead of live streams:
   ```yaml
   cameras:
     - id: "cam1"
       snapshot_url: "http://192.168.1.100/snapshot"  # Use snapshots for thumbnails
   ```

### High Network Usage

#### Bandwidth Saturated
**Symptom**: Network interface saturated, affecting other services

**Solution**:
1. Monitor network usage:
   ```bash
   iftop -i eth0 -t
   nethogs
   ```
2. Optimize camera streams:
   ```yaml
   cameras:
     - id: "cam1"
       sub_stream_url: "rtsp://192.168.1.100:554/sub"  # Lower bandwidth sub-stream
       hls_max_fps: 15  # Limit frame rate
   ```
3. Enable snapshot caching:
   ```yaml
   cameras:
     - id: "cam1"
       snapshot_url: "http://192.168.1.100/snapshot"  # Snapshots use less bandwidth
   ```

## Docker Issues

### Container Won't Start
**Symptom**: Docker container exits immediately

**Solution**:
1. Check container logs:
   ```bash
   docker compose logs mibee-nvr
   docker logs mibee-nvr-container-id
   ```
2. Verify configuration file is mounted:
   ```yaml
   # docker-compose.yml
   volumes:
     - ./mibee-nvr.yaml:/mibee-nvr.yaml:ro
   ```
3. Check file permissions inside container:
   ```bash
   docker exec -it mibee-nvr-container ls -la /mibee-nvr.yaml
   ```

### Volume Permission Issues
**Symptom**: Cannot write recordings to mounted volume

**Solution**:
1. Set proper ownership:
   ```bash
   sudo chown -R 1000:1000 ./data  # mibee-nvr runs as UID 1000
   ```
2. Use proper user in Docker:
   ```yaml
   # docker-compose.yml
   user: "1000:1000"
   volumes:
     - ./data:/var/lib/mibee-nvr
   ```

## Error Messages and Solutions

### Common Error Codes

| Error Code | Description | Solution |
|------------|-------------|----------|
| `CAMERA_NOT_FOUND` | Camera ID doesn't exist | Check camera ID spelling, verify camera exists in config |
| `CAMERA_ALREADY_EXISTS` | Camera ID already used | Choose unique camera ID |
| `RECORDING_NOT_FOUND` | Recording file missing | Check storage directory, verify file exists |
| `STORAGE_FULL` | Disk space exhausted | Clean up recordings, increase disk space, lower retention |
| `AUTH_REQUIRED` | Authentication needed | Add valid credentials to request |
| `AUTH_FAILED` | Invalid credentials | Check username/password, verify hash generation |
| `INVALID_INPUT` | Invalid parameters | Check API request format, validate configuration |
| `PATH_TRAVERSAL` | Security violation | Fix file paths, remove suspicious characters |
| `HLS_MAX_STREAMS` | Too many concurrent streams | Reduce concurrent viewers, increase `max_streams` |
| `ONVIF_CONNECTION_FAILED` | Cannot connect to ONVIF device | Check network, verify ONVIF service is running |

### Log Analysis

#### Debug Mode
Enable debug logging for detailed troubleshooting:
```yaml
observability:
  log_level: "debug"
```

#### Log Locations
**Systemd Service**:
```bash
journalctl -u mibee-nvr -f
```

**Docker Container**:
```bash
docker logs -f mibee-nvr-container
```

**Binary Run Directly**:
```bash
./mibee-nvr -config mibee-nvr.yaml 2>&1 | tee mibee-nvr.log
```

#### Common Log Patterns

**Camera Connection Issues**:
```
WARN: camera connection failed: rtsp://...: connection refused
WARN: camera authentication failed for camera_id
ERROR: camera stream error: read timeout
```

**Storage Issues**:
```
WARN: storage directory not writable: /var/lib/mibee-nvr
ERROR: cannot write recording file: no space left on device
```

**Configuration Issues**:
```
ERROR: validation failed: camera[].url has invalid format
ERROR: validation failed: cleanup.retention_days must be between 1 and 3650
```

## Performance Optimization

### For Raspberry Pi 3B
```yaml
# Optimized for RPi 3B constraints
storage:
  segment_duration: "15s"  # Shorter segments = less RAM
hls:
  max_streams: 2          # RPi constraint: max 4, but 2 is safer
  segment_count: 5        # Fewer segments = less I/O
cleanup:
  check_interval: "30m"   # Less frequent checks
  retention_days: 7        # Shorter retention
merge:
  enabled: false          # Disable merging on RPi 3B
```

### For High-Performance Systems
```yaml
# Optimized for performance
storage:
  segment_duration: "60s"  # Longer segments = fewer files
hls:
  max_streams: 10          # Allow more concurrent streams
  segment_count: 10        # More segments for smoother playback
merge:
  enabled: true
  batch_limit: 500        # Larger batches for efficiency
cleanup:
  check_interval: "15m"    # More frequent cleanup
  retention_days: 90       # Longer retention
```

## Getting Help

### Before Reporting Issues
1. Check this troubleshooting guide
2. Review [Configuration Reference](configuration.md)
3. Search existing GitHub issues
4. Check logs for error messages

### Creating a Bug Report
When creating a GitHub issue, include:

1. **System Information**:
   ```bash
   uname -a
   lsb_release -a
   ```

2. **MiBee NVR Version**:
   ```bash
   ./mibee-nvr --version
   ```

3. **Configuration** (remove sensitive data):
   ```bash
   grep -v password mibee-nvr.yaml
   ```

4. **Logs** (last 50 lines):
   ```bash
   journalctl -u mibee-nvr --since "1 hour ago"
   ```

5. **Reproduction Steps**:
   - What you tried to do
   - What happened instead
   - Expected behavior

### Community Support
- Join our Discord community for real-time help
- Check the wiki for additional guides
- Review closed issues for similar problems

## Emergency Procedures

### System Becomes Unresponsive
1. Stop the service:
   ```bash
   sudo systemctl stop mibee-nvr
   ```
2. Kill any remaining processes:
   ```bash
   sudo pkill -f mibee-nvr
   ```
3. Check system resources:
   ```bash
   free -h
   df -h
   top
   ```
4. Restart with reduced configuration:
   ```bash
   # Use minimal config
   cp mibee-nvr.yaml mibee-nvr.yaml.backup
   # Edit to enable only essential cameras
   sudo systemctl start mibee-nvr
   ```

### Corrupted Configuration
1. Restore from backup:
   ```bash
   cp mibee-nvr.yaml.backup mibee-nvr.yaml
   ```
2. Or create minimal configuration:
   ```yaml
   server:
     listen: ":9090"
   storage:
     root_dir: "/var/lib/mibee-nvr"
     segment_duration: "30s"
   auth:
     username: "admin"
     password: "temp-password"
   ```
3. Restart service and reconfigure

### Database Corruption
1. Backup database:
   ```bash
   cp /var/lib/mibee-nvr/mibee-nvr.db /var/lib/mibee-nvr/mibee-nvr.db.backup
   ```
2. Remove corrupted database:
   ```bash
   rm /var/lib/mibee-nvr/mibee-nvr.db
   ```
3. Restart service (database will be recreated):
   ```bash
   sudo systemctl restart mibee-nvr
   ```
4. Reconfigure all cameras