# F2 Decisions

- **APPROVED**: All 3 MED issues are non-blocking. No correctness bugs, no resource leaks, 180 tests pass.
- `*bool` for enabled fields in config is a valid Go pattern for YAML nil-detection.
- 20× `_ = bi` in mp4mux is an API artifact from abema/go-mp4, not slop.
- The repeated box-writing pattern in mp4mux is inherent to MP4 structure (deeply nested atoms).
