## Learnings

### 2026-05-09 T2: Edit Tool Invisible Corruption
- **CRITICAL**: The `edit` tool can introduce invisible character corruption that causes Go syntax errors even when the file reads correctly
- Error: `syntax error: unexpected name context in argument list` on perfectly valid-looking code
- Solution: Use `python3 -c` with string replacement for reliable Go file modifications
- Alternative: `sed -i` works for simple insertions but gets tricky with multi-line + exact indentation
- `xxd` byte inspection + `go build` are the definitive verification tools
- `go clean -cache` does NOT fix this — the corruption is in the source file, not the build cache

### 2026-05-09 T2: cam.Protocol is string, not model.Protocol
- `config.CameraConfig.Protocol` is `string` type, NOT `model.Protocol`
- Switch cases MUST use `string(model.ProtoONVIF)` cast
- Confirmed via minimal reproduction test (test_switch2)

### 2026-05-09 T2: manager.go has TWO switch statements for protocols
1. `createRecorder()` (line ~64) — factory method for creating recorders
2. `Start()` (line ~158) — startup loop that creates and starts recorders
- ONVIF case needed in BOTH: createRecorder returns nil (no ONVIF recorder), Start logs info message

## Decisions

### 2026-05-09: ONVIF case in createRecorder returns nil
- ONVIF cameras don't need their own recorder type
- They use discovered RTSP URL → existing H264/H265 recorder
- The ONVIF case in createRecorder returns nil as a placeholder
- Actual ONVIF camera creation flow (T6) will call GetStreamURI first, then create with correct protocol

## Issues
- Pre-existing: webdav TestWriteMethodsForbidden/PATCH returns 405 instead of 405 (expected 403) — NOT related to our changes, 292/294 tests pass

## Problems
(none currently)

### 2026-05-10 T5: PTZ Control Protocol Guards in Frontend
- **Dashboard.svelte**: Already had correct protocol guards
  - Line 637: `{#if ptzOpenIndex === index && camera.protocol === 'onvif'}` wraps PTZ overlay
  - Line 285 & 292: `handleCellClick` checks `camera.protocol === 'onvif'` before toggling PTZ
- **LiveView.svelte**: Had logic bug — PTZ control was nested INSIDE rtsp_h264/h265 conditional
  - Original: PTZ control at line 243 was inside `{#if camera.protocol === 'rtsp_h264' || camera.protocol === 'rtsp_h265'}` block
  - Problem: ONVIF cameras never entered this block, so PTZ was never shown for ANY camera
  - Fix: Moved PTZ control OUTSIDE the HLS player conditional, wrapped in its own `{#if camera.protocol === 'onvif'}` block (lines 257-262)
- **Verification**: `rtk npm run build` succeeds with no new errors
- **Pattern**: Protocol-specific UI components must be checked at RENDER time, not just at click handler time
- **Nested conditionals anti-pattern**: Don't nest protocol-specific UI inside unrelated protocol conditionals
