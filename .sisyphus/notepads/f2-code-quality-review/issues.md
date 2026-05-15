# F2 Code Quality Review Issues

## MED Severity
1. **db.go:170** — `scanTime` silently swallows parse errors (returns zero time). Should log warning for data corruption visibility.
2. **ftp/server.go:399** — DB insert error in uploadFileTransfer.Close() silently discarded with `_ =`. Recording file exists but no DB entry = data integrity gap.
3. **mp4mux.go:285-286, 447-448** — Hardcoded 1920x1080 in tkhd/avc1. RPi CSI cameras are often 1280x720. Should derive from SPS or config.

## LOW Severity
4. **h264.go:313-314** — Extra indentation on closing brace (leftover from refactoring).
5. **db.go:86-88** — Inconsistent single-line if style (rest of file uses multi-line).
6. **db.go:344-357** — 4-space indentation in UpsertCamera (should be tabs, will show in gofmt).
7. **ftp/server.go:200** — Stray indentation on Chown method.
8. **config.go:18** — Extra space before MQTT field in struct.
9. **config.go:110-114** — Meaningless AI-slop comment ("mutex: minimal validation...").
10. **main.go:32-34, 56, 139** — 4-space indentation (should be tabs).
11. **mp4mux.go** — 20 instances of `_ = bi` from mp4.StartBox() API artifact.
