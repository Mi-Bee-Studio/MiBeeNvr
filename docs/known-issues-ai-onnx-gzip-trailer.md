# Known Issue: AI Detection `INVALID_PROTOBUF` — gzip trailer corrupting binary responses

> **Status**: ✅ **Fixed** (issue #109). This document is retained as a
> postmortem / regression reference.
> **Affected**: Browser-side AI detection (ONNX Runtime Web) loading any model
> from `/models/*.onnx` through the streaming-gzip middleware.
> **Impact (pre-fix)**: Every ONNX model received by the browser was the raw
> bytes + a spurious ~20-byte gzip trailer, so ONNX Runtime Web's C++ protobuf
> parser (compiled to WASM) rejected it with `ERROR_CODE: 7, ERROR_MESSAGE:
> Failed to load model because protobuf parsing failed.` AI detection was
> permanently unusable from any browser, on every model, on every ORT version.
> **Symptom appeared identical to**: truncated download, wrong model opset,
> ORT version mismatch, browser-engine incompatibility — all of which were
> investigated and ruled out before the real cause was found.

## Problem

After deploying ONNX Runtime Web (browser-side AI inference), every camera
LiveView showed:

```
AI ✗ Can't create a session. ERROR_CODE: 7, ERROR_MESSAGE:
       Failed to load model because protobuf parsing failed.
```

This was filed as issue #109 with the title "AI detection fails to load model:
protobuf parsing failed". The error came from ORT's C++ core
(`Model::Load` → protobuf deserialize) compiled into the
`ort-wasm-simd-threaded.jsep.wasm` binary.

## Why this was extraordinarily hard to diagnose

The error message (`INVALID_PROTOBUF`) points squarely at "the model file is
corrupt". But every obvious check passed:

| Check | Result | (Mis)leading conclusion |
|-------|--------|-------------------------|
| `onnx.checker.check_model()` in Python | ✅ PASS | "Model is valid" |
| Python `onnxruntime` 1.28 `InferenceSession(model)` | ✅ PASS | "Model loads fine" |
| `curl /models/yolo11n.onnx` first 16 bytes vs disk | ✅ MATCH | "Bytes are correct" |
| ORT Web JS layer (`new ort.Tensor(...)`) | ✅ WORKS | "ORT is fine" |
| `WebAssembly.compile(26 MB wasm)` | ✅ 139 exports | "WASM is fine" |
| ORT 1.26 **and** 1.27 (downgrade test) | ❌ both fail | "Not a version issue" |
| UMD bundle vs ESM bundle vs CDN | ❌ all fail | "Not a bundle issue" |
| `ArrayBuffer` vs `Uint8Array` vs URL-string arg | ❌ all fail | "Not a buffer issue" |
| In-app-browser (Electron) vs real Edge/Chrome | ❌ both fail | "Not a browser-engine issue" |
| 5 different models (tiny 86 B, yolo11n, official yolo, squeezenet, Add op) | ❌ all fail | "Not a model issue" |

The first 16 bytes of the browser-fetched buffer matched the disk file
**exactly** — which is the check everyone (including ORT's own maintainers in
issue [microsoft/onnxruntime#13117](https://github.com/microsoft/onnxruntime/issues/13117))
suggest first. That check is **necessary but not sufficient**: the corruption
was at the **end** of the file, not the start.

## Root Cause

The streaming-gzip middleware (`internal/middleware/compress.go`) wraps every
response with a `gzip.Writer` and pre-sets `Content-Encoding: gzip` before the
handler runs. The intended flow for binary content (`application/octet-stream`,
e.g. `/models/*.onnx`) is:

1. Middleware sets `Content-Encoding: gzip`, creates `gz`, `defer cw.Close()`.
2. `http.ServeFile` sets `Content-Type: application/octet-stream`.
3. `WriteHeader` detects octet-stream → `shouldSkipCompression=true` → deletes
   `Content-Encoding`, calls the underlying `WriteHeader`.
4. `Write` detects octet-stream → **bypasses** `gz`, writes raw bytes directly
   to the underlying `ResponseWriter`. ✓
5. **`defer cw.Close()` runs → calls `gz.Close()`** on a gzip writer that was
   **never written to**.

**The bug lives in step 5.** `gzip.Writer.Close()` on an untouched writer
still emits a complete, valid, **empty** gzip member to the underlying writer:
the `1f 8b 08 00 ...` magic header + an empty deflate stream + an 8-byte
trailer (CRC32 + ISIZE), totalling ~20 bytes. Because the raw response body
was already flushed in step 4, this empty gzip frame is **appended** to the
response — silently corrupting every binary download.

The browser always sends `Accept-Encoding: gzip`, so every binary fetch
(video segment download, ONNX model load, snapshot, recording download) was
silently getting 20 bytes of gzip garbage appended. For media containers
(MP4/MPEG-TS) the extra 20 bytes are usually tolerated (players stop at the
moov/mdat end); for ONNX, which is a single protobuf message with a strict
length prefix, the trailer makes the protobuf parser read past the real end
of the message and fail.

### The smoking gun

Comparing the **full md5** (not just the head) of the browser-fetched bytes
against the on-disk bytes revealed the corruption:

```
browser fetched md5 : ee7b0660585856f7e37a10b8c5b29edd   ← 10741417 bytes (corrupted)
disk md5 (correct)  : df8e6fccb7797fd340b6bb1358a53394   ← 10741397 bytes
                                                                       ↑ +20 bytes
```

Hex dump of the **last 32 bytes** of the corrupted file:

```
... 46 61 6c 73 65 [1f 8b 08 00 00 09 6e 88 00 ff 03 00 00 00 00 00 00 00 00 00]
                    ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
                    empty gzip member appended by gz.Close() on an unused writer
```

`curl -H "Accept-Encoding: gzip"` reproduced the corruption from the server
side (no browser involved), confirming it was a server bug, not a client bug.

## Fix

`internal/middleware/compress.go`: track whether the gzip writer was actually
used (`wroteGzip` flag, set on the first `gz.Write`). Only call `gz.Close()`
and `gz.Flush()` when `wroteGzip == true`.

```go
type compressWriter struct {
    w             http.ResponseWriter
    gz            *gzip.Writer
    contentType   string
    headerWritten bool
    wroteGzip     bool  // NEW: did we actually write any bytes through gz?
}

func (cw *compressWriter) Write(b []byte) (int, error) {
    if shouldSkipCompression(cw.contentType) {
        return cw.w.Write(b)
    }
    cw.wroteGzip = true
    return cw.gz.Write(b)
}

func (cw *compressWriter) Flush() {
    if !shouldSkipCompression(cw.contentType) && cw.wroteGzip {
        _ = cw.gz.Flush()
    }
    // ...flush underlying writer...
}

func (cw *compressWriter) Close() error {
    if !cw.wroteGzip {
        return nil  // do NOT call gz.Close() — it would emit an empty gzip member
    }
    return cw.gz.Close()
}
```

This is a **one-time finalization guard** — once any bytes go through `gz`,
the flag stays true for the rest of the response, so legitimate compressed
responses (JSON, HTML, SSE) still flush and finalize correctly.

### Regression coverage

`internal/middleware/compress_issue_test.go` —
`TestStreamingGzip_BinaryNoGzipTrailer`: serves an
`application/octet-stream` payload through the middleware with
`Accept-Encoding: gzip`, and asserts (a) the body equals the raw payload
exactly (no extra bytes), (b) `Content-Encoding` is empty, and (c) the last
20 bytes are not a gzip frame (`1f 8b ...`). This is the exact signature of
the bug and will catch any regression that re-introduces the unconditional
`gz.Close()`.

The existing tests (`TestStreamingGzip_JSONResponse`, `TestStreamingGzip_SSEFlush`,
`TestStreamingGzip_SkipsVideo`) all still pass — the fix only changes behavior
for the "gzip writer allocated but never used" path, which no existing test
exercised end-to-end (they checked `Content-Encoding` header presence but not
body byte-equality).

## Lessons learned

1. **`gzip.Writer.Close()` is not free.** Calling `Close()` on a writer that
   was never `Write()`n to still produces output — a complete empty gzip
   member (~20 bytes). Any middleware that allocates a gzip writer
   optimistically and conditionally bypasses it MUST also conditionally skip
   `Close()`. This applies to `deflate`, `zlib`, `brotli`, and `zstd` writers
   too (all of them emit headers/footers on `Close()`/`Flush()`).

2. **Head-byte equality is necessary but not sufficient.** When verifying a
   binary transfer, compare the **full content hash (md5/sha256)** of the
   received bytes against the source — not just the first N bytes. Corruption
   can be anywhere in the stream, and HTTP-level issues (chunked encoding
   bugs, compression trailers, range-join errors) frequently corrupt the
   **tail** while leaving the head intact.

3. **Error messages from compiled-WASM C++ are not debuggable.** ORT Web's
   `INVALID_PROTOBUF` comes from C++ compiled to WASM; there is no source-level
   stack trace and the only diagnostic is the error code + a generic message.
   When the runtime says "the input is corrupt", **believe it** — and
   byte-verify the input end-to-end (browser context → server response → disk
   file) rather than trusting any single layer.

4. **Use a real browser (CDP) to debug browser-side failures.** The in-app
   browser (Electron webview) and a real Chrome/Edge can both reproduce a
   server-side bug, but only a real browser driven over CDP lets you run
   arbitrary `Runtime.evaluate` to compute md5s, inspect buffers, and compare
   bytes — the IAB's `evaluate` was locked down by a side-effect guard that
   blocked even read-only `localStorage` access. For any "works in Python,
   fails in browser" bug, drive a real browser via
   `msedge.exe --remote-debugging-port=9222` + `chrome-remote-interface` and
   inspect the actual bytes the page received.

5. **The middleware pipeline is a global transformer.** Anything applied via
   `r.Use(...)` runs on **every** route, including public binary-download
   routes (`/models/*`, `/api/recordings/{id}/download`). Compression
   middleware in particular must be tested against binary responses, not just
   JSON/HTML. The existing `TestStreamingGzip_SkipsVideo` test checked the
   `Content-Encoding` header was cleared for video, but did not check the
   response **body** for stray bytes — a gap that let this bug ship.

## Verification (Banana Pi M5, real Edge browser)

Driven a real Edge 150 instance over CDP (`msedge.exe
--remote-debugging-port=9222`) against the M5 deployment:

- **md5 parity**: `curl -H "Accept-Encoding: gzip" --compressed
  /models/yolo11n.onnx | md5sum` = `df8e6fcc...` = disk md5 (was
  `ee7b0660...` before the fix).
- **ORT session create**: `ort.InferenceSession.create(buf, {executionProviders:
  ['wasm']})` → `SESSION OK inputs=["images"]` (was `ERROR_CODE: 7`).
- **LiveView AI indicator**: 视通 camera (H.265, online) shows **"AI 就绪"**
  in the status bar (was "AI ✗ ERROR_CODE 7"). Screenshot confirms the video
  is playing and the green AI-ready chip is visible.
- All existing `TestStreamingGzip_*` tests still pass; new regression test
  added.

## Related

- Issue #109 — original report ("AI detection fails to load model: protobuf
  parsing failed").
- `internal/middleware/compress.go` — the fix.
- `internal/middleware/compress_issue_test.go` — regression test.
- [microsoft/onnxruntime#13117](https://github.com/microsoft/onnxruntime/issues/13117)
  — ORT maintainers' (correct, but incomplete) guidance: "check that the
  server serves the file correctly". This postmortem is the concrete version
  of that guidance for this codebase.
- `docs/en/troubleshooting.md` / `docs/zh/troubleshooting.md` →
  "AI Detection" section — user-facing symptom + fix steps.
