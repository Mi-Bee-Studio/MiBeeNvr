# Learnings

## limitedWriter Bug Fix (mp4merge.go)
The `limitedWriter` had 3 bugs:
1. **Type assertion failure**: `w` field was `io.Writer`, so `l.w.(io.Seeker)` always failed. Fixed by changing to `io.WriteSeeker`.
2. **No position tracking**: Original Seek returned `l.written` (cumulative bytes written) instead of delegating to underlying writer. Fixed by delegating Seek and tracking position.
3. **Wrong initial position**: `limitedWriter` was created with `pos=0` but the file was already at `moovOffset`. Fixed by initializing `pos: moovOffset`.

## Moov Size Calculation Bug (mp4merge.go)
The moov size was calculated with 0 samples (`tmpTrack.samples = nil`) but the actual write used N samples. The per-sample tables (stts, stsz, stsc) vary with sample count, causing moov overflow. Fixed by populating `tmpTrack.samples` with placeholder entries before size calculation.

## MdatOffset/MdatSize Always 0 in Parser (parser.go)
The parser checks `len(h.Path) == 0` for top-level mdat, but go-mp4's `ReadBoxStructure` calls the handler with `h.Path` of length 1 for top-level boxes. This means mdat tracking is broken. Not critical since merge uses per-sample offsets from stco/stsz.

## Test Patterns
- `createTestH264Segment`: Uses muxer.NewMP4Muxer with minimal SPS/PPS (Baseline profile, 16x16)
- `createTestH265Segment`: Uses muxer.NewMP4Muxer with minimal VPS/SPS/PPS (Main profile, 16x16)
- Manager tests use `mergeTestEnv` with real SQLite DB + storage Manager
- `insertMergeableRecording` creates real MP4 files via store and inserts DB records
