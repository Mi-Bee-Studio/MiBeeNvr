# MiBee NVR — Media Probe Package

## OVERVIEW

Pure-Go media metadata probing — the ffprobe-free replacement for
`internal/transcoding/ffprobe.go`'s `GetMediaInfo`. Reads MP4 box metadata
(`moov`/`mdhd`/`stsz`/`avcC`/`hvcC`) and parses SPS for resolution. **Never
decodes pixel data, never spawns external processes.**

This package exists to remove the ffmpeg/ffprobe binary dependency from all
non-transcoding code paths (cleanup duration repair, timelapse segment probing,
transcoding preflight). When ffprobe is available it can still be used as a
fallback for non-MP4 inputs or edge cases; mediaprobe is the fast pure-Go
default.

## STRUCTURE

```
probe.go      # ProbeMP4(), ProbeDuration() — public API
probe_test.go # Unit tests (builds real tiny MP4s via internal/muxer)
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Probe an MP4 for codec/duration/resolution/frames | `ProbeMP4(path)` | Returns `*MediaInfo` |
| Probe only duration | `ProbeDuration(path)` | Slightly cheaper but ParseSegment already skips mdat |
| Check if path is MP4 before probing | `IsLikelyMP4(path)` | Extension-based; helps decide ffprobe fallback |

## CONVENTIONS

- **Delegates to `internal/merge`**: `merge.ParseSegment()` does the box walking; `merge.ParseSPSResolution`/`ParseHEVCSPSResolution` resolve resolution. mediaprobe is a thin aggregation + codec-name mapping layer.
- **Codec name mapping**: internal codec is `"h264"`/`"h265"`; the `CodecName` field uses ffprobe-compatible names (`"h264"`/`"hevc"`) so consumers comparing against legacy ffprobe output are not broken.
- **Frame count** comes free from `stsz.SampleCount` — ffprobe needs `-count_frames` (full decode) to get this, so mediaprobe is 10–100× faster for frame counting.
- **Best-effort resolution**: SPS parse failure leaves `Width`/`Height` at 0 (non-fatal); callers needing resolution should fall back to ffprobe if `Width==0`.

## ANTI-PATTERNS

- **DO NOT** add pixel decoding to this package — it is metadata-only by design. If you need to decode frames (e.g. for thumbnails), that belongs in a separate package or behind the ffmpeg gateway.
- **DO NOT** assume `CodecName == "hevc"` everywhere internally — internal code uses `"h265"`; only `CodecName` (the ffprobe-compat field) is `"hevc"`.
- **DO NOT** spawn ffprobe from this package — it is the ffprobe-*free* layer. Fallback orchestration belongs in the caller (e.g. `transcoding.GetMediaInfo` tries mediaprobe first, ffprobe second).
