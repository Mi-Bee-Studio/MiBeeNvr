# WASM + WebCodecs Unified Player & AI Detection

## What is the WASM + WebCodecs Unified Player & AI Detection System

The WASM + WebCodecs Unified Player & AI Detection system is a modern, tiered video streaming architecture that replaces traditional browser video players with a WebCodecs-based pipeline. It provides three-tier fallback rendering (WebGPU → WebGL2 → Legacy), supports **browser-side** AI object detection using a YOLOv11-nano model (ONNX Runtime Web), and maintains compatibility across modern browsers while delivering superior performance for H.264/H.265 content.

**Key Features:**
- Three-tier fallback rendering with automatic degradation
- WebSocket binary streaming protocol for low-latency video transport
- Frontend AI detection with ONNX Runtime WebGPU/WASM execution providers
- Backend serves AI config + the ONNX model file only (no server-side inference — preserves the static `CGO_ENABLED=0` binary)
- Hardware acceleration for both decoding and rendering
- Support for modern video codecs including H.265 (HEVC)

## Architecture Overview

The system implements a sophisticated three-tier fallback architecture that automatically adapts to browser capabilities:

```
┌─ Tier 1: WebGPU (Zero-Copy Hardware) ──────────────────────────────────┐
│  WebCodecs decode → WebGPU texture (importExternalTexture) → Render    │
│  ONNX Runtime WebGPU execution provider (AI, 5-10ms/frame)               │
│  Browser Support: Chrome 113+, Edge 113+, Safari 18+                     │
│  Performance: Decode 200+ FPS, AI 30+ FPS, Render 60 FPS                │
├───────────────────────────────────────────────────────────────────────────┤
└── ↓ WebGPU device loss or unavailable → Automatic fallback to WebGL2

┌─ Tier 2: WebGL2 + WASM SIMD (Hybrid Mode) ─────────────────────────────┐
│  WebCodecs decode → WebGL2 Canvas (copyExternalImageToTexture) → Render  │
│  ONNX Runtime WASM SIMD execution provider (AI, 30-50ms/frame)          │
│  Browser Support: Firefox 130+, Safari 16.4+, Chrome 94+ (WebCodecs)    │
│  Performance: Decode 200+ FPS, AI 10-20 FPS, Render 60 FPS            │
├───────────────────────────────────────────────────────────────────────────┤
└── ↓ WebCodecs unavailable → Automatic fallback to Legacy players

┌─ Tier 3: Legacy Compatibility Mode ──────────────────────────────────┐
│  HTTP-FLV/HLS/WebRTC → hls.js/mpegts.js/WHEP → <video> element        │
│  Browser Support: Legacy browsers, mobile browsers, specific scenarios   │
│  Performance: Same as current experience                              │
└───────────────────────────────────────────────────────────────────────────┘
```

### Data Flow Architecture

```
Camera → RTSP Recorder → StreamHub → WebSocket → Browser Worker → 
VideoDecoder → Renderer → Canvas
                                                    ↓
Frontend AI (ONNX Runtime Web) → Detections → UI Overlay (in-browser, no server round-trip)
```

### Tier Detection Algorithm

The system dynamically determines the best playback tier using `getPlaybackTier()`:

```typescript
export function getPlaybackTier(): PlaybackTier {
  if (detectWebCodecs() && detectWebGPU()) {
    return 'tier1'; // WebCodecs + WebGPU
  }
  if (detectWebCodecs() && (detectWebGL2() || detectOffscreenCanvas())) {
    return 'tier2'; // WebCodecs + WebGL2
  }
  return 'tier3'; // Legacy fallback
}
```

## WebSocket Protocol

The system uses a custom WebSocket binary protocol with efficient framing for video and codec configuration data. All multi-byte integers use big-endian (network byte order).

### Protocol Overview

**Message Types:**
- `0x01` = CodecInfo (server → client)
- `0x02` = VideoFrame (server → client)
- `0x03` = AudioFrame (server → client, reserved)
- `0x04` = KeyframeReq (client → server)

### Codec Info (type 0x01)

Binary wire format for codec configuration data sent once at stream start:

```
[type:1byte][codec:1byte][profile:1byte][level:1byte][sps_len:2bytes_BE][sps:N][pps_len:2bytes_BE][pps:N][vps_len:2bytes_BE][vps:N]
```

**Codec Identifiers:**
- `4` = H.264 (AVC)
- `5` = H.265 (HEVC)

**Field Details:**
- `codec`: Byte indicating codec type (4 for H.264, 5 for H.265)
- `profile`: H.264 profile byte or H.265 profile_idc
- `level`: H.264 level or H.265 tier/level combination
- `sps_len`, `pps_len`, `vps_len`: Lengths of respective NAL sets (big-endian)
- `sps`, `pps`, `vps`: Raw NAL unit data (no start codes)
- `vps` field is only present for H.265

### Video Frame (type 0x02)

Binary wire format for individual video frames with NAL units:

```
[type:1byte][pts:8bytes_BE][is_keyframe:1byte][nalu_count:2bytes_BE][nalu1_len:4bytes_BE][nalu1]...
```

**Field Details:**
- `type`: Always `0x02`
- `pts`: Presentation timestamp in 90kHz clock (from StreamHub)
- `is_keyframe`: Boolean flag (1=keyframe, 0=inter-frame)
- `nalu_count`: Number of NAL units in this frame (max 65535)
- `naluX_len`: Length of each NAL unit (big-endian)
- `naluX`: Raw NAL unit data without Annex B start codes

**Key Implementation Details:**
- All NAL units are sent without Annex B start codes (`00 00 00 01`)
- Start codes are prepended client-side before decoding
- PTS timestamps are synchronized with StreamHub's 90kHz clock
- Frame skipping and error recovery handled at the decoder level

## WebCodecs Decode Pipeline

The decode pipeline runs in a Web Worker to avoid blocking the main thread, using the WebCodecs VideoDecoder API for hardware-accelerated video decoding.

### Worker Message Protocol

**Worker Messages:**
- `codec-info`: Configure decoder with SPS/PPS/VPS data
- `video-frame`: Decode raw NAL units with timestamp and keyframe info
- `reset`: Re-initialize decoder state (handle format changes)
- `close`: Clean up resources and terminate worker

### Codec Configuration

**H.264 Codec String:**
```typescript
const codecString = `avc1.${profile}${constraint}${level}`;
// Example: "avc1.42C01E" for High Profile @ Level 3.1
```

**H.265 Codec String:**
```typescript
const codecString = `hvc1.${profile_idc}.6.${tier}${level}.B0`;
// Example: "hvc1.1.6.L93.B0" for Main Profile @ Level 3.1
```

### NAL Unit Processing

1. **Start Code Addition:** Annex B start codes (`00 00 00 01`) prepended to each NAL
2. **Decoder Configuration:** SPS/PPS sent first to initialize decoder
3. **Frame Decoding:** NAL units grouped by frame with PTS metadata
4. **Error Recovery:** Auto-reset on decode errors with latest codec parameters

### Memory Management

- **Every frame path MUST `VideoFrame.close()`** — otherwise the GC warning "A VideoFrame was garbage collected without being closed" fires and stalls the main thread. WasmPlayer's `onmessage` handler wraps the render call in `try/finally`: frames that arrive during teardown (`destroyed` or `canvasEl` is null), or on a render early-return (`gl` already nulled by `cleanupWebGL2()`), are still `close()`d in `finally`. This guard is especially load-bearing on navigation away, when the worker still has in-flight frames.
- **AI-detection cloned frames must be closed** — `processAiDetection` clones a frame for the async `detect()`; the clone is `close()`d in `finally` so a throw from `detect()` can't leak it.
- Worker-managed frame pool to minimize allocations
- Automatic cleanup on worker termination or decoder reset

```typescript
// Example worker message handling
self.onmessage = (event) => {
  const { type, data } = event.data;
  
  if (type === 'codec-info') {
    decoder.configure(data.config);
  } else if (type === 'video-frame') {
    const frame = new VideoFrame(data.canvas, {
      timestamp: data.pts / 90000, // Convert 90kHz to seconds
      duration: 1000 / 30 // Assume 30 FPS
    });
    decoder.decode(frame);
  }
};
```

## WebGPU Renderer

The WebGPU renderer provides two rendering paths: zero-copy using GPUExternalTexture and fallback using copyExternalImageToTexture. This maximizes performance while maintaining compatibility.

### Two-Path Rendering

**Zero-Copy Path (Preferred):**
```wgsl
@group(0) @binding(1) var ourTexture: texture_external;

@fragment
fn fs(input: VertexOutput) -> @location(0) vec4f {
  return textureSampleBaseClampToEdge(ourTexture, ourSampler, input.texcoord);
}
```

**Fallback Path (Staging Texture):**
```wgsl
@group(0) @binding(1) var ourTexture: texture_2d<f32>;

@fragment
fn fs(input: VertexOutput) -> @location(0) vec4f {
  return textureSample(ourTexture, ourSampler, input.texcoord);
}
```

### Rendering Pipeline

1. **Device Initialization:** Request WebGPU adapter and device
2. **Resource Creation:** Create render pipelines, samplers, bind groups
3. **Frame Processing:** Import external texture or copy from VideoFrame
4. **Render Pass:** Draw textured quad to canvas
5. **Cleanup:** Destroy external texture after each render

### Device Loss Handling

```typescript
device.lost.then((info: GPUDeviceLostInfo) => {
  this.deviceLost = true;
  this.onDeviceLostCallback?.();
  // Automatic fallback to WebGL2 occurs at player level
});
```

**Key Requirements:**
- External textures must be destroyed after each render (WebGPU spec)
- Canvas format uses `navigator.gpu.getPreferredCanvasFormat()`
- Alpha mode set to 'opaque' for video rendering
- RequestAnimationFrame ensures vsync-aligned rendering

### Render Loop

```typescript
render(videoFrame: VideoFrame): void {
  if (this.pendingFrame) {
    this.pendingFrame.close(); // Cleanup old frame
  }
  this.pendingFrame = videoFrame;
  
  if (this.animationFrameId === null) {
    this.animationFrameId = requestAnimationFrame(() => this.renderLoop());
  }
}
```

## Three-Tier Fallback

The system automatically detects and adapts to browser capabilities using a tiered approach that ensures maximum performance while maintaining broad compatibility.

### Tier Detection

```typescript
export function getPlaybackTier(): PlaybackTier {
  if (detectWebCodecs() && detectWebGPU()) {
    return 'tier1';
  }
  if (detectWebCodecs() && (detectWebGL2() || detectOffscreenCanvas())) {
    return 'tier2';
  }
  return 'tier3';
}
```

**Capability Detection Functions:**
- `detectWebCodecs()`: Check for VideoDecoder availability
- `detectWebGPU()`: Check for navigator.gpu availability
- `detectWebGL2()`: Try creating WebGL2 context
- `detectOffscreenCanvas()`: Check OffscreenCanvas API

### Runtime Degradation

When WebGPU device is lost, the system automatically falls back to WebGL2:

```typescript
device.lost.then((info: GPUDeviceLostInfo) => {
  this.deviceLost = true;
  this.onDeviceLostCallback?.(); // Tr tier switch
});
```

**ConnectionManager handles** reconnection (coordinated via `ReconnectCoordinator` to prevent a thundering herd when many cameras reconnect at once):
- Initial backoff: 1 second, doubling each round, capped at 30 seconds; at most 2 concurrent reconnect slots
- Backend pressure (HTTP 503) → 10s global cooldown + doubled backoff

**Intentional closes do NOT trigger reconnect** (`_intentionalClose` flag): `disconnect()`/`destroy()`/reconnect-rotation call `close()` without a code, so the resulting `CloseEvent.code` is 1005 ("no status"), which is neither 1000 nor 1001. The old logic treated this as a crash and called `_scheduleCoordinatedReconnect()` — so **navigating away from the surveillance grid made every camera auto-reconnect**, and the reconnecting socket was closed in `onopen` once destroyed ("WebSocket closed before connection is established" spam), while the coordinator reconnect slot leaked permanently. Fix: `_closeWebSocket()` sets `_intentionalClose=true`; `onclose` returns early without rescheduling when it sees it; and an `onopen` reached mid-destroy calls `completeReconnect()` to release the slot.

### Browser Support Matrix

| Browser | Tier 1 | Tier 2 | Tier 3 | Notes |
|---------|--------|--------|--------|-------|
| Chrome 113+ | ✅ | ✅ | ✅ | Full Tier 1 support |
| Firefox 130+ | ❌ | ✅ | ✅ | WebGL2 fallback |
| Safari 16.4+ | ❌ | ✅ | ✅ | WebGL2 fallback |
| Safari 18+ | ✅ | ✅ | ✅ | Tier 1 support |
| Legacy browsers | ❌ | ❌ | ✅ | Current players |

## AI Detection (Frontend)

The frontend AI detection system uses ONNX Runtime Web with YOLOv11-nano for real-time object detection, running entirely in the browser with hardware acceleration.

### Backend Selection

The system automatically selects the optimal execution provider:

```typescript
// Runtime detection from onnxruntime-web
if (navigator.gpu) {
  // WebGPU execution provider (5-10ms/frame)
  sessionOptions.executionProviders = ['webgpu'];
} else if (detectWasmSimd()) {
  // WASM SIMD execution provider (30-50ms/frame)
  sessionOptions.executionProviders = ['wasm-simd'];
} else {
  // Fallback to plain WASM
  sessionOptions.executionProviders = ['wasm'];
}
```

### YOLOv11-nano Pipeline

**Model Specifications:**
- Input: `[1, 3, 640, 640]` float32 NCHW
- Output: `[1, 84, 8400]` bounding boxes with confidence scores
- Classes: 80 COCO categories (person, car, etc.)
- Model size: ~4MB (quantized)

**Preprocessing Pipeline:**

```typescript
async function preprocessFrame(
  videoFrame: VideoFrame,
  inputSize: number
): Promise<Float32Array> {
  // 1. Create ImageBitmap for safe drawing
  const bitmap = await createImageBitmap(videoFrame);
  
  // 2. Letterbox to 640x640 with gray padding
  const { scale, padX, padY } = letterboxParams(
    bitmap.width, bitmap.height, inputSize
  );
  
  // 3. Draw to OffscreenCanvas
  const canvas = new OffscreenCanvas(inputSize, inputSize);
  const ctx = canvas.getContext('2d');
  ctx.fillStyle = `rgb(114, 114, 114)`; // Gray padding
  ctx.fillRect(0, 0, inputSize, inputSize);
  ctx.drawImage(bitmap, padX, padY, 
    Math.round(bitmap.width * scale), 
    Math.round(bitmap.height * scale)
  );
  
  // 4. Extract and convert pixels to Float32 CHW
  return convertToCHW(canvas);
}
```

**Postprocessing Pipeline:**

1. **YOLO Output Parsing:** Extract bounding boxes and confidence scores
2. **Non-Maximum Suppression:** Remove overlapping detections (IoU threshold 0.45)
3. **EMA Smoothing:** Apply exponential moving average for tracking (alpha 0.3)
4. **Coordinate Mapping:** Map from input space (640x640) to original frame

### Performance Optimization

**Frame Skipping:**
- Configurable frame skip (default: every 3rd frame)
- Results in ~10 FPS detection at 30 FPS video
- Balances detection accuracy with performance

**Model Caching:**
- Downloads and caches models via Cache API
- Tracks download progress with percentage
- Idempotent download with SHA-256 verification

**Memory Management:**
- OffscreenCanvas reused to prevent leaks
- ImageBitmap objects closed after use
- Output tensors disposed after inference

### Detection Output

```typescript
interface Detection {
  bbox: [number, number, number, number]; // [x1, y1, x2, y2] in original coordinates
  confidence: number; // [0, 1] confidence score
  classId: number; // COCO class ID (0-79)
  label: string; // Human-readable label
}
```

## Backend Role in AI Detection

> **The backend performs NO AI inference.** There is no subprocess, no ONNX runtime, no hardware probe, and no model downloader on the server side. This section documents that decision to prevent regressions.

All object detection runs in the browser (see [AI Detection (Frontend)](#ai-detection-frontend) above and `web/src/lib/ai-detection/`). The Go server's only AI-related duties are:

1. **Config persistence** — AI settings + ROI zones via `/api/ai/*` (see [Backend AI API](#backend-ai-api) below).
2. **Model file serving** — the public `GET /models/{filename}` route serves `{storage_root}/models/<file>` so the browser can fetch the `.onnx`.

### Why no backend inference

- The NVR ships as a static `CGO_ENABLED=0` binary cross-compiled to ARM64/ARMv7. A Go ONNX binding (`libonnxruntime`) would introduce a C dependency, bloat the binary, and break ARM cross-compilation. FFmpeg is already the heaviest dependency.
- The lowest supported target (RPi 3B, 1 GB RAM, Cortex-A53, no ML GPU) cannot spare cycles for inference alongside recording/streaming.
- Detection is per-viewer over an already-decoded live stream, so running it in the browser avoids re-decoding on the server and scales with viewers, not cameras.

If backend inference is ever reconsidered, it must run as an **out-of-process** sidecar (subprocess + IPC), never linked into the main binary — preserving the `CGO_ENABLED=0` static-build guarantee.

## Backend AI API

> **The backend performs NO AI inference and exposes no inference/event API.** Detection runs entirely in the browser (see `web/src/lib/ai-detection/`). The Go backend's only AI-related duties are (a) persisting config + ROI zones and (b) serving the model file.

The backend API for AI is **config-only**:

| Endpoint | Purpose |
|----------|---------|
| `GET /api/ai/status` | Read global AI config |
| `PUT /api/ai/config` | Update config (enabled, thresholds, model URL) |
| `GET`/`POST`/`PUT`/`DELETE /api/ai/zones[/{id}]` | ROI zone CRUD |
| `GET /models/{filename}` | Serve the `.onnx` model to the browser (**public**, no auth) |

There is **no** `POST /api/ai/enable`, `POST /api/ai/disable`, or `GET /api/ai/events` SSE endpoint. For the full request/response contract, see [AI Detection API](api/ai-detection.md).

## Configuration

The AI detection system is configurable both on the frontend and backend to adapt to different hardware capabilities and requirements.

### Frontend Configuration

**localStorage Settings:**
```typescript
interface AIConfig {
  enabled: boolean;
  confidenceThreshold: number; // 0.1-0.9
  frameSkip: number; // 1-10 frames
  emaAlpha: number; // 0.1-1.0
}

// Default values
const defaultConfig: AIConfig = {
  enabled: true,
  confidenceThreshold: 0.5,
  frameSkip: 3,
  emaAlpha: 0.3
};
```

**Runtime Updates:**
- Configuration changes applied immediately
- No page restart required
- Changes persisted automatically

### Backend Configuration

The backend stores AI config in the YAML `ai:` block (`config.AIConfig`) and persists runtime changes from `PUT /api/ai/config` atomically. There is **no backend hardware requirement for AI** — no inference runs server-side.

- `enabled`, `confidence_threshold`, `frame_skip_rate`, `model_url` — global AI settings
- `enabled_cameras` — cameras permitted to show AI in the UI
- `zones` — `map[cameraID][]ROI`, normalized `[0,1]` polygon vertices

**Model file:** place `.onnx` models in `{storage_root}/models/` (the `mibee-nvr download-model` CLI does this for the default `yolo11n.onnx`). The browser fetches them via `GET /models/{filename}`.

### Performance Tuning

**Frontend Settings (browser-side inference):**
- `frameSkip`: higher values reduce client CPU but lower detection frequency
- `confidenceThreshold`: higher values reduce false positives but may miss objects
- `emaAlpha`: lower values give smoother tracking but slower response

## Testing

The browser-side inference pipeline is unit-tested directly; the backend zone/config logic is tested in Go. There are **no** backend inference / ONNX / probe / downloader tests — no such code exists.

### Test Inventory

**Go tests (backend — config + zones only):**
- `internal/ai/zones_test.go` — 43 tests: zone CRUD, `PointInPolygon`, `FilterDetectionsByZone`, `GetEnabledZones`, validation
- `internal/config/config_ai_test.go` — 15 tests: AI config defaults + validation (threshold range, frame skip, zone points/coordinates)

**Frontend tests (browser-side inference — vitest):**
- `web/src/lib/ai-detection/inference.test.ts` — YOLO output parsing, NMS, IoU, sigmoid, EMA smoothing, coordinate mapping
- `web/src/lib/ai-detection/runtime.test.ts` — WebGPU detection, WASM fallback, model caching, URL whitelist/SSRF validation, init/run/dispose lifecycle (ONNX runtime mocked)

### Gaps

- No E2E test drives the full pipeline (load model → decode frame → detect → render overlay). Largest open testing gap.
- No accuracy/recall benchmarking against labeled frames.
- Inference is tested with a mocked ONNX runtime; no test runs a real `.onnx` model.