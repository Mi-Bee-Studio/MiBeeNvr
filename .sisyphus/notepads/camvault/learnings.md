# Learnings

## abema/go-mp4 AnyTypeBox pattern (2026-04-27)

- `VisualSampleEntry` and `AVCDecoderConfiguration` implement `IAnyType` via embedded `AnyTypeBox`
- `AnyTypeBox.GetType()` returns its `Type` field — defaults to zero `BoxType{0,0,0,0}` if not set
- `mp4.Marshal()` calls `src.GetType().getBoxDef(ctx)` — zero BoxType has no registered definition → `ErrBoxInfoNotFound`
- **Fix**: Always set `AnyTypeBox{Type: mp4.StrToBoxType("avc1")}` when constructing AnyType boxes for marshaling
- `AVCDecoderConfiguration.GetFieldLength()` reads `NumOfSequenceParameterSets` / `NumOfPictureParameterSets` to determine slice lengths — these fields MUST be set explicitly alongside the slice data
- Standard boxes (Ftyp, Mvhd, Tkhd, Stsd, etc.) use `AddBoxDef` and don't need Type set — they implement `IBox` directly
- `mp4.Context{}` (empty) is safe for writing standard MP4 boxes — context is only needed for ambiguous metadata types (ilst, wave, udta)

## rtpmjpeg testJPEG corruption (2026-04-27)

- **Root cause**: Byte array literal was truncated during copy — DQT section 0x4f fill had 7 bytes per line instead of 8
- **Symptom**: `rtpmjpeg.Encoder.Encode()` returns `"invalid image"` at `encoder.go:93` (`h0 != 0xFF`)
- **Impact**: Zero RTP packets sent → `OnPacketRTP` never called → no frames recorded
- **Fix**: Replace hardcoded byte array with `image/jpeg.Encode()` stdlib — generates guaranteed-valid JPEG
- **Lesson**: Never hand-copy binary byte arrays. Use programmatic generation for test data.
- **Lesson**: gortsplib `OnTransportSwitch` and `OnDecodeError` are struct fields on Client, set at construction time
- **Lesson**: `rtpmjpeg.Encoder` requires: valid SOI, DQT, SOF0 (width/height multiple of 8), DHT, SOS markers

## Camera Manager (Task 16) (2026-04-27)

- `recorder.SegmentStore` interface: `CreateSegment(cameraID, fmt) → (tempPath, finalPath, err)`, `WriteFrame(tempPath, data) → (int, err)`, `CloseSegment(tempPath, finalPath) → error`
- `storage.Manager` satisfies `recorder.SegmentStore` — same method signatures
- For unit tests with recorders that connect to RTSP servers, use `rtsp://127.0.0.1:1/stream` — connection refused is immediate, avoids 10s TCP timeouts
- Recorder's `Stop()` cancels internal context but `connectAndRecord` blocks on network I/O until it returns, then checks `ctx.Done()` — so Stop waits for current connection attempt to fail
- Pre-existing cleanup test failure (`SQL logic error: 9 values for 10 columns`) is unrelated to camera package

## REST API handler implementation (2026-04-27)

- chi v5 `r.Route("/{id}", ...)` creates a sub-router — paths like `/{id}/pin` work naturally inside it
- `POST /api/auth/login` needs manual auth validation since it's a public route: wrap a dummy handler with authMW and check if it passes
- `statusRecorder` pattern intercepts `WriteHeader` to detect if middleware rejected the request
- `storage.DB` fields (`db`, `path`) are private — must add public methods to db.go for any new queries
- Pre-existing test failures in `internal/camera` (TestGracefulShutdown timeout) and `internal/cleanup` (9 values for 10 columns) are NOT caused by API changes
- `http.ServeFile` handles Content-Type, Content-Disposition, and range requests automatically
- `json.NewEncoder(w).Encode(v)` appends a trailing newline — acceptable for API responses

## Cleanup Manager (Task 14) (2026-04-27)

- `storage.DeleteFile(path)` uses `os.Remove(path)` directly — NOT relative to RootDir. Store full paths in recording.FilePath.
- `modernc.org/sqlite` is strict about column/value count matching — `VALUES(?,?,NULL,?)` with wrong count fails even if the existing codebase had a similar pattern that worked (likely due to test isolation).
- For inserting records with NULL ended_at, use `VALUES(?,?,?,?,?,NULL,?,?,?,?)` — 5 args + NULL + 4 args = 10 values for 10 columns.
- Disk threshold cleanup with 0% threshold will delete ALL unpinned recordings (expected for testing).
- `CleanupConfig` already existed in config.go with defaults (30 days, 1h interval, 95% threshold).
- Added `DB()` accessor method to `storage.DB` for raw SQL access in tests.

## Integration Tests (Task 18) (2026-04-27)

- Integration tests in `tests/` dir must use package `camvault_tests` (not `main`) since they use `testing.T`
- `upload.uploadResponse` is unexported — need a local mirror struct in external test packages
- `upload.RegisterRoutes` takes `chi.Router`, not `http.ServeMux` — use `chi.NewRouter()`
- Go's zero `time.Time` marshals as `"0001-01-01T00:00:00Z"` in SQLite, NOT as SQL NULL — to insert NULL, use raw SQL via `db.DB().Exec()`
- `storage.Manager.CreateSegment()` calls `EnsureCameraDir()` which uses `os.MkdirAll()` — this recreates the entire root dir tree if removed. So `CreateSegment` can "succeed" in recreating dirs even after `os.RemoveAll(rootDir)`. Test `ListFiles`/`GetDiskUsage` BEFORE `CreateSegment` when testing storage unavailability.
- `http.ServeFile` doesn't always set `Content-Disposition: attachment` for all file types — don't rely on this header in tests
- RTSP test servers from `internal/recorder/mjpeg_test.go` cannot be reused from external packages (unexported types, gortsplib test helpers). Multi-camera tests should test at storage level, not recorder level.
- `t.TempDir()` is auto-cleaned by Go test framework; use subdirectories if you need to remove and recreate dirs during the test.

## Final Wave Review (2026-04-27)

- F1 Plan Compliance: Must Have 17/17, Must NOT Have 14/14 — APPROVE
- F2 Code Quality: 12 minor findings (all LOW), no blockers — APPROVE
- F3 Manual QA: 82/82 tests pass, all builds clean — APPROVE
- F4 Scope Fidelity: 18/19 tasks fully compliant, 1 minor gap — APPROVE
- Oracle agents hit 200 tool call limit when asked to read files — must provide all evidence inline in prompt
- `deep` category effective for scope review (4m runtime, thorough analysis)
- Key gap: `/api/recordings/{id}/play` endpoint missing but functionally compensated by `/download`
- Non-blocking issues: `interface{}` vs `any`, AI slop comments, duplicate line in ftp, magic numbers in muxer
