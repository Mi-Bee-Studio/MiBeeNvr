# Recording & Playback

> Applies to MiBeeNvr v0.12.0

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
| Recording directory | `/var/lib/mibee-nvr` (`storage.root_dir`) | The data volume (`/data`) in Docker |
| Segment duration | 30 seconds | `storage.segment_duration` (10s – 5m) |
| Retention | 30 days | `cleanup.retention_days`; older segments are deleted |
| Disk usage | ~1 GB/day/camera | Estimate for 1080p H.264; adaptive mode cuts it by 75%+ |

## Configuration

### Recording Settings

```yaml
storage:
  root_dir: "/var/lib/mibee-nvr"  # recording root path
  segment_duration: "30s"          # segment length (10s – 5m)

cleanup:
  retention_days: 30               # days to keep
```

### Recording Density

Every camera picks one **recording mode** (`recording_mode`):

| Mode | Behavior | Best for |
|------|----------|----------|
| `continuous` (default) | Full-rate recording | General surveillance |
| `adaptive` | Motion-aware — calm spans drop to timelapse-grade sparse writing; activity / audio / external triggers instantly restore full rate | Doors, hallways, warehouses that sit empty most of the day — 75%+ less disk |

Tuning knobs, the audio trigger, the ambient-audio layer and activity retrieval live in the dedicated **[Adaptive Recording](adaptive-recording.md)** guide.

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
2. Use the toolbar to filter by type (**All / Video / Timelapse / MJPEG**), by camera, by **AI detection** (person / vehicle), by **activity** (motion / static / scene-cut, plus a minimum activity score), or search by keyword.
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

There is no single "export a time range" endpoint; locate the segments in the range and download them one by one (a merged long segment is often a whole day):

```bash
# List recordings in a time range, then download each
curl -u admin:password \
  "http://192.168.1.50:9090/api/recordings?camera_id=front-door&start=2026-08-18T00:00:00Z&end=2026-08-18T12:00:00Z"

curl -u admin:password \
  -o segment.mp4 \
  "http://192.168.1.50:9090/api/recordings/{id}/download"
```

## Storage Statistics

Two places to look:

- **Dashboard → Storage trend**: per-camera usage / segment counts / share, plus a stacked daily-write chart (clickable legend)
- **Settings → Storage**: root path, [candidate volumes & migration](storage-management.md)

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

The NVR logs to stdout (journald under systemd):

```bash
# systemd deployments
journalctl -u mibee-nvr -f | grep -i record

# Docker deployments
docker compose logs -f mibee-nvr | grep -i record
```

## Advanced Features

### Adaptive Recording

Calm spans drop to sparse keyframe writing; activity instantly restores full rate — see the dedicated **[Adaptive Recording](adaptive-recording.md)** guide.

```yaml
cameras:
  - id: "front-door"
    recording_mode: "adaptive"   # default "continuous"
```

### Recording Schedule

Restrict recording to specific time windows (e.g. daytime only); nothing is written outside the window:

```yaml
cameras:
  - id: "office-cam"
    recording_schedule:
      time_ranges:
        - start: "09:00"
          end: "18:00"
      days_of_week: [1, 2, 3, 4, 5]   # 0=Sunday … 6=Saturday; empty = every day
```

### External-Event Triggered Recording

MQTT messages, scripts or an AI backend can pull a camera back to full rate (paired with adaptive mode = event-driven recording):

```bash
curl -u admin:password -X POST \
  http://192.168.1.50:9090/api/cameras/front-door/adaptive/trigger \
  -H "Content-Type: application/json" \
  -d '{"source": "automation", "hold": "30s"}'
```

See [MQTT Integration](mqtt-integration.md).

## FAQ

### Recording Won't Play

- Make sure your player supports H.265 (VLC recommended).
- Try converting the codec: `ffmpeg -i input.mp4 -c:v libx264 output.mp4`

### Corrupted Recording Files

- Segment writes use "temp file + atomic rename" — an abnormal shutdown mid-recording cannot corrupt segments already on disk.
- If an individual file still won't play, it is usually **source-stream** reference-frame loss at a disconnect instant — verify in the NVR web player (the frontend decodes with tolerance); to salvage, try `ffmpeg -i input.mp4 -c copy output.mp4`.

## Next Steps

- [Audio](audio.md) — audio recording and two-way talk
- [Timelapse](timelapse.md) — timelapse recording
- [Transcoding](transcoding.md) — video transcoding
