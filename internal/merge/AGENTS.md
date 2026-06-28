# MiBee NVR — Segment Merge Package

## OVERVIEW

Periodic MP4/MJPEG segment merging to reduce file count. Streaming merge (1MB fixed buffer) — never loads full files into memory.

## STRUCTURE

```
manager.go       # MergeManager — periodic merge loop, camera grouping, disk space checks
mp4merge.go      # MergeMP4Segments() — placeholder moov, limitedWriter, in-place header patching
parser.go        # ParseSegment() — extracts sample tables, codec params, keyframe flags from MP4
mjpegmerge.go    # MergeMJPEGSegments() — directory-based JPEG file moves
*_test.go        # Tests for each component
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Change merge interval/params | `config/config.go` `MergeConfig` | CheckInterval, WindowSize, BatchLimit, MinSegmentAge, MinSegmentsToMerge |
| Fix MP4 merge logic | `mp4merge.go` | Two-pass: placeholder moov → stream samples → patch headers |
| Fix segment parsing | `parser.go` | Reads moov/stbl boxes, skips mdat. Uses abema/go-mp4 |
| Fix MJPEG merge | `mjpegmerge.go` | Moves JPEG files from segment dirs into merged dir |
| Add merge trigger | `manager.go` `RunOnce()` | Called periodically by `Run()`, can be called directly |

## CONVENTIONS

- **Streaming merge**: Fixed 1MB buffer (`mergeBufferSize = 1 << 20`). Sample data never fully loaded
- **Grouping**: Segments grouped by codec + SPS/PPS (H.264) or VPS/SPS/PPS (H.265) byte equality. Incompatible groups skipped
- **Disk space check**: Requires 1.1x estimated merged size free. Skips camera if insufficient
- **Batch limits**: `BatchLimit` (default 200) prevents runaway merges per pass
- **Min segment age**: `MinSegmentAge` (default 10m) prevents merging segments still being written
- **Atomic output**: Uses `store.CreateSegment()`/`CloseSegment()` for temp→final rename
- **DB transactions**: Inserts merged recording, batch-deletes originals in transaction
- **Placeholder moov**: Writes moov with dummy data first, calculates real size, rewrites with limitedWriter to prevent overflow

## ANTI-PATTERNS

- **DO NOT** load full MP4 files into memory — use streaming copy with fixed buffer
- **DO NOT** merge segments with different SPS/PPS — will produce unplayable output
- **DO NOT** construct stsc entries without `SampleDescriptionIndex: 1` — merge builds new moov from scratch, same rule as MP4Muxer applies
- **DO NOT** merge segments younger than `MinSegmentAge` — recorder may still be writing
- **DO NOT** assume merge always succeeds — disk full, permission errors, corrupt segments all handled gracefully with warnings
- **DO NOT** use `stco` for files with chunk offsets > 4GB — use `co64`
- **Audio merge**: supports AAC (mp4a+esds), G.711 (ulaw/alaw), and Opus (Opus+dOps) sample entries. Parser detects codec from sample entry box type.

## METRICS


| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| nvr_merge_attempts_total | Counter | — | Total merge attempts |
| nvr_merge_successes_total | Counter | — | Successful merges |
| nvr_merge_failures_total | CounterVec | reason | Failed merges by reason (sps_pps_mismatch, parse_error, db_error, disk_space, timeout, audio_mismatch, zero_resolution, io_error) |
| nvr_merge_duration_seconds | Histogram | — | Merge operation duration |
| nvr_merge_size_bytes | Histogram | — | Merged file size |
| nvr_merge_pending_segments | GaugeVec | camera_id | Pending segments per camera |


- **DO NOT** use null-byte string concatenation for SPS/PPS grouping — use SHA-256 hash (embedded null bytes cause false matches)
- **DO NOT** silently ignore SPS parse failures — always return error and reject segment
- **DO NOT** delete MJPEG source dirs before DB commit — data loss on DB failure
- **DO NOT** use `readBit()`/`readBits()` return of 0 as valid data — may be bounds overflow
- **DO NOT** use `stco` for files with chunk offsets > 4GB — use `co64`


- **Per-camera merge mutex**: `sync.Map` with try-lock pattern prevents concurrent merges for same camera
- **retryOnBusy on all DB ops**: All merge DB operations wrapped with `storage.RetryOnBusy()` for SQLITE_BUSY resilience
- **SHA-256 hash for SPS/PPS grouping**: Replacing null-byte string concatenation with `fmt.Sprintf("%x", sha256.Sum256(sps+pps+vps))`
- **co64 atom support**: For merged files >4GB, uses `co64` box instead of `stco`
- **stts compression**: Run-length encoded consecutive same-duration samples to reduce moov size
- **SPS/PPS error returns**: `parseSPSResolution` and `parseHEVCSPSResolution` now return `(int, int, error)` instead of silently returning (0, 0)
- **BitReader bounds checking**: `readBit()`, `readBits()`, `readUE()`, `readSE()` return errors on overflow instead of corrupted zero values
- **Deferred MJPEG deletion**: Source dirs deleted AFTER successful DB commit, not before
- **Context cancellation**: `MergeMP4Segments` accepts `ctx context.Context` for cancellation support
