# Learnings

## 2026-05-13 Session Start
- SegmentCount is hardcoded at 3 (lines 140, 153 in manager.go), NOT in config
- defaultWriteBufSize is 40 in code (AGENTS.md says 120 — docs are wrong)
- HLSConfig in config.go only has WriteBufferSize and SegmentMaxSizeMB
- User views from modern desktop browsers (Chrome/Firefox/Safari), NOT RPi browser
- enableWorker: true is safe for this user
- Metis review confirmed all references valid
- Momus review: OKAY — plan is executable as-is

## HLS E2E Test Infrastructure (Wave 1)

### Key Findings

- **JSDoc `*/` in comments**: Avoid `**/stream/**` inside JSDoc comments — `**/` terminates the comment block. Use escaped form or reword.
- **Playwright esbuild transpilation**: Complex inline generics like `Promise<Array<{ id: string; ... }>>` can confuse esbuild. Use type aliases instead (`interface CameraInfo` + `Promise<CameraInfo[]>`).
- **page.evaluate closing**: `page.evaluate(async () => { ... })` needs `});` not just `}` — easy to miss the paren.
- **Auth localStorage key**: Frontend uses `mibee_nvr_auth` (not `nvr_credentials`). Stores `btoa(username:password)`.
- **No data-testid on Dashboard**: Camera cards have no `data-testid` or `data-camera` attributes. Must use structural selectors (CSS class combinations).
- **Stream state indicators**: Colored `<span>` dots with `title` attributes: "Live" (green), "Buffering" (yellow+animate), "Error" (red), "Snapshot mode" (gray).
- **HLS protocols**: `rtsp_h264`, `rtsp_h265`, `onvif`, `rtsp` are HLS-capable. `http_jpeg` uses snapshot mode.
- **Existing test patterns**: `recording-playback.spec.ts` uses no auth (navigates directly), `recordings.spec.ts` logs in via form. Both use `@playwright/test` imports.
- **No TypeScript installed in e2e-tests**: Tests are plain TS transpiled by Playwright's esbuild at runtime. No `tsconfig.json`.

## 2026-05-13 HLS Configurability + Frame Drop Observability
- HLSConfig now has SegmentCount field (yaml: `segment_count`, default 7, valid [3,10])
- defaultWriteBufSize changed from 40→100 for smoother HLS playback
- Prometheus counter `nvr_hls_frames_dropped_total` (label: camera_id) tracks buffer-full drops
- NewManagerWithOpts signature: `(dataDir string, writeBufSize, segmentMaxSize, segmentCount int, opts ...*metrics.Metrics)` — follows `opts ...*metrics.Metrics` pattern from cleanup.go
- streamEntry has atomic `drops uint64` field for per-stream drop tracking
- Drop logging: aggregate warn every 100 drops via `atomic.AddUint64 + %100`
- Prometheus CounterVec only appears in Registry.Gather() after first use (lazy registration) — must increment in tests
