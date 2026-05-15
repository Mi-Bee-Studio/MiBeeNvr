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
