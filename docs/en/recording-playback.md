# Recording & Playback

> Applies to MiBeeNvr v0.11.0

MiBee NVR captures camera video streams as MP4 segments and saves them to disk. A built-in web interface lets you browse, search, and download recordings.

## How Recording Works

1. **Frame capture**: fetches video frames from cameras via RTSP / ONVIF / SRT / RTMP / libcamera
2. **Muxing**: wraps frames in an MP4 container (H.264 / H.265)
3. **Segmentation**: splits the stream into MP4 files at a configurable interval
4. **Indexing**: stores metadata for each segment (timestamp, camera, file path, etc.) in SQLite
5. **Cleanup**: automatically deletes expired segments based on the retention policy

## Defaults

| Setting | Default | Description |
|---------|---------|-------------|
| Recording directory | `/var/lib/mibee-nvr/recordings` | Subdirectory under the data root |
| Segment duration | 1 minute | Configurable in the config file |
| Retention | 30 days | Segments older than this are deleted |
| Disk usage | ~1 GB/day/camera | Estimate for 1080p H.264 |

## Configuration

### Recording Settings

```yaml
recording:
  segment_duration: "1m"        # Segment length (10s – 5m)
  max_days: 30                  # Days to keep
  storage_path: "/data"         # Root storage path
```

### Choosing a Segment Duration

| Duration | Pros | Cons |
|----------|------|------|
| 10s–30s | Fast loading, low memory | Many files, high I/O |
| 1 m (recommended) | Balanced performance and resources | — |
| 2m–5m | Fewer files | Higher memory usage, slower loading |

### Retention Policy

- **By days**: `max_days: 30` keeps the most recent 30 days
- **By disk**: monitors disk usage and deletes the oldest segments first
- **Manual**: delete individual segments from the web UI, or bulk-clean by date / orphan files with [`mibee-nvr cleanup`](cli.md#cleanup-recording-cleanup)

## Web UI Playback

### Accessing Recordings

1. Open the web interface and go to the **Recordings** page.
2. Use the toolbar to filter by type (**All / Video / Timelapse / MJPEG**), by camera, by **AI detection** (person / vehicle), or search by keyword.
3. Pick a day using the month switcher and calendar.
4. In **Timeline** view, click or drag the timeline to position playback; in **List** view, click a segment's **View** to open it.

### Timeline View

The Recordings page defaults to a per-camera timeline of the day's coverage, with AI detection events overlaid as markers on the tracks:

![Recordings timeline](images/recordings.webp)

- **Track fill**: that time range has recordings
- **AI markers**: hover to see the event summary (category, target, hit count); click to jump to that moment
- **now indicator**: the current time position

### List View

Switch to **List** for a paginated table showing camera, format (MP4 (HEVC) / JPEG, etc.), duration, size, start time, and merge status, with multi-select batch delete:

![Recordings list](images/recordings-list.webp)

> Consecutive segments are automatically **merged** (marked "Merged") and load as one unit during playback.

### Playback Detail

Click **View** in the list to open the playback page: seek by dragging the progress bar, change playback speed, take snapshots, and download the file:

![Playback](images/playback.webp)

### Searching Recordings

Search by time range:

1. Select a camera.
2. Set the start and end times.
3. Click **Search**.
4. Choose a segment from the results to play.

## Downloading Recordings

### Single Segment

In the web UI:

1. Locate the segment.
2. Click the **Download** button.
3. Save the MP4 file.

### Batch Download

```bash
# Download via WebDAV
rclone copy nvr:/recordings/camera-id/2026-08-18/ ./local-folder/

# Download via FTP
wget -r ftp://admin:password@192.168.1.50:2121/recordings/camera-id/
```

### Export by Time Range

```bash
# Export via API
curl -u admin:password \
  "http://192.168.1.50:9090/api/v1/recordings/export?camera_id=front-door&start=2026-08-18T00:00:00Z&end=2026-08-18T12:00:00Z" \
  -o export.mp4
```

## Storage Statistics

View statistics on the **Settings → Storage** page:

![Storage settings](images/settings-storage.webp)

| Metric | Description |
|--------|-------------|
| Total usage | Disk space consumed by all recordings |
| Today's additions | Data recorded today |
| Per-camera breakdown | Disk usage per camera |
| Trend chart | Storage trends over the past 7 / 30 days |

## Recording Status

### Healthy Recording

In the web UI, the camera status indicator should show green (recording).

### Recording Errors

Common errors and fixes:

| Error | Cause | Fix |
|-------|-------|-----|
| Camera offline | Network issue or powered off | Check network connectivity |
| Permission denied | Recording directory not writable | Check directory permissions |
| Disk full | Insufficient storage space | Add disk or adjust the retention policy |
| Encoding unsupported | Camera codec not in the supported list | Check the `encoding` field configuration |

### Recording Logs

```bash
# View recording logs
tail -f /var/lib/mibee-nvr/logs/recorder.log

# Docker users
docker compose logs -f mibee-nvr | grep -i record
```

## Advanced Features

### Motion-Only Recording

Record only when motion is detected:

```yaml
cameras:
  - id: "front-door"
    recording:
      mode: "motion"             # Motion-triggered recording
      pre_buffer: "5s"           # Buffer before motion
      post_buffer: "10s"         # Buffer after motion
```

### Recording Schedule

Set a recording timetable:

```yaml
cameras:
  - id: "office-cam"
    recording:
      schedule:
        - days: ["mon", "tue", "wed", "thu", "fri"]
          hours: ["09:00-18:00"]
          mode: "continuous"
        - days: ["sat", "sun"]
          mode: "motion"
```

## FAQ

### Recording Won't Play

- Make sure your player supports H.265 (VLC recommended).
- Try converting the codec: `ffmpeg -i input.mp4 -c:v libx264 output.mp4`

### Corrupted Recording Files

- Usually caused by an abnormal NVR shutdown during recording.
- Enable integrity checks on startup:

```yaml
recording:
  integrity_check: true          # Check and repair corrupt segments on startup
```

## Next Steps

- [Audio](audio.md) — audio recording and two-way talk
- [Timelapse](timelapse.md) — timelapse recording
- [Transcoding](transcoding.md) — video transcoding
