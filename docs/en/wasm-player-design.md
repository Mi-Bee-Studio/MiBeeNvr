# WASM + WebCodecs Unified Player & AI Detection

## What is the WASM + WebCodecs Unified Player & AI Detection System

The WASM + WebCodecs Unified Player & AI Detection system is a modern, tiered video streaming architecture that replaces traditional browser video players with a WebCodecs-based pipeline. It provides three-tier fallback rendering (WebGPU → WebGL2 → Legacy), supports both frontend and backend AI object detection using YOLOv11-nano models, and maintains compatibility across modern browsers while delivering superior performance for H.264/H.265 content.

**Key Features:**
- Three-tier fallback rendering with automatic degradation
- WebSocket binary streaming protocol for low-latency video transport
- Frontend AI detection with ONNX Runtime WebGPU/WASM execution providers
- Backend AI detection with ONNX Runtime subprocess (preserves CGO_ENABLED=0)
- Real-time detection event streaming via Server-Sent Events
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
Frontend AI (ONNX Runtime) → Detection Events → SSE Stream → UI Overlay
                                                    ↓
Backend AI (ONNX Runtime subprocess) → Detections → Database → API
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

- `VideoFrame.close()` called after every frame for GPU memory safety
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

**ConnectionManager handles** reconnection with exponential backoff:
- Initial: 2 seconds
- Subsequent: [2, 4, 8, 16, 32] seconds
- Maximum delay: 32 seconds to prevent excessive wait times

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

## AI Detection (Backend)

The backend AI detection system uses a subprocess pattern to preserve CGO_ENABLED=0 while providing on-device inference capabilities.

### Subprocess Architecture

The system spawns an external ONNX Runtime binary and communicates via JSON over stdin/stdout:

```
Go Process ↔ ONNX Runtime Binary (stdin/stdout JSON)
```

**Communication Protocol:**

```go
// Request (stdin)
type detectRequest struct {
  Frame  string `json:"frame"`  // base64-encoded JPEG
  Width  int    `json:"width"`  // frame width in pixels
  Height int    `json:"height"` // frame height in pixels
}

// Response (stdout)
type detectResponse struct {
  Detections []rawDetection `json:"detections"`
  Error      string         `json:"error,omitempty"`
}
```

### AiDetector Integration

The AiDetector integrates with the StreamHub system:

```go
type AiDetector interface {
  EnableCamera(camID string, hub *model.StreamHub) error
  DisableCamera(camID string)
  IsEnabled(camID string) bool
  EnabledCameras() []string
  OnDetection(cb engine.OnDetectionFunc)
  StopAll()
}
```

**Frame Processing:**
- Subscribes to StreamHub with `"ai-{camID}"` prefix
- Uses atomic counter for frame skipping (non-blocking)
- Processes frames via JPEG conversion and base64 encoding
- Results streamed to SSE clients

### Crash Recovery

The engine implements automatic restart with exponential backoff:

```go
func backoffDuration(crashes int) time.Duration {
  if crashes <= 0 {
    return 0
  }
  d := time.Duration(1<<(crashes-1)) * time.Second
  if d > defaultRestartBackoff { // 30s cap
    d = defaultRestartBackoff
  }
  return d
}
```

**Backoff Schedule:**
- 1st crash: 1 second
- 2nd crash: 2 seconds
- 3rd crash: 4 seconds
- 4th crash: 8 seconds
- 5+ crashes: 16 seconds (capped at 30s)

### Performance Considerations

- Non-blocking design won't stall recording pipeline
- Frame limiting prevents excessive CPU usage
- Atomic counters ensure thread safety
- Graceful degradation on subprocess failure

## ONNX Runtime Binary Downloader

The system automatically downloads and manages ONNX Runtime binaries for the current platform, ensuring optimal performance across different architectures.

### Download Process

**Atomic Download Pattern:**
1. Check existing binary with SHA-256 verification
2. Download to temporary file
3. Verify integrity with hash check
4. Rename to final location (atomic operation)

**Download Sources:**
- GitHub releases: `microsoft/onnxruntime`
- Platform-specific binaries: `onnxruntime-linux-x64`, `onnxruntime-linux-arm64`
- Version selection matches backend requirements

**Idempotent Operations:**
- Skip download if existing binary passes verification
- Cache binary in `{dataDir}/tools/onnxruntime`
- Preserve existing binary on download failure

### File Structure

```
{dataDir}/
├── tools/
│   └── onnxruntime          # Platform-specific binary
└── models/
    └── yolov11n.onnx        # YOLOv11-nano model
```

## Hardware Probe

The system performs comprehensive hardware checks to ensure AI capabilities are available before enabling AI detection.

### Probe Criteria

**Platform Requirements:**
- Architecture: amd64 or arm64 only (no ARMv7)
- Memory: ≥2GB RAM available
- CPU: ≥2 cores for acceptable performance

**Binary Existence:**
- Verify `{dataDir}/tools/onnxruntime` exists
- Check file is executable and has correct architecture

### Probe Implementation

```go
type ProbeInfo struct {
  Available bool   `json:"available"`
  Reason    string `json:"reason"`
}

func (p *Probe) Probe() ProbeInfo {
  if p.platform != "amd64" && p.platform != "arm64" {
    return ProbeInfo{Available: false, Reason: "unsupported_platform"}
  }
  
  if p.memory < 2*1024*1024*1024 { // 2GB
    return ProbeInfo{Available: false, Reason: "insufficient_memory"}
  }
  
  if p.cores < 2 {
    return ProbeInfo{Available: false, Reason: "insufficient_cpu"}
  }
  
  if _, err := os.Stat(p.binaryPath); err != nil {
    return ProbeInfo{Available: false, Reason: "binary_not_found"}
  }
  
  return ProbeInfo{Available: true, Reason: ""}
}
```

### Probe Behavior

**If Probe Fails:**
- AI detection routes are disabled
- Warning message logged to system
- Frontend shows AI as unavailable
- No impact on core recording functionality

**Success Metrics:**
- 100% coverage on supported platforms
- Minimal overhead (<1ms per probe)
- Thread-safe operation
- Graceful degradation

## Backend AI API

The backend provides a REST API for AI detection management and real-time event streaming.

### API Endpoints

**GET /api/ai/status**
Returns AI engine availability and system status:

```json
{
  "available": true,
  "engine_status": "running",
  "model": "/data/models/yolov11n.onnx",
  "probe": {
    "available": true,
    "reason": ""
  }
}
```

**POST /api/ai/enable**
Enable AI detection for a specific camera:

```json
{
  "camera_id": "front-door-camera"
}
```

**POST /api/ai/disable**
Disable AI detection for a specific camera:

```json
{
  "camera_id": "front-door-camera"
}
```

**GET /api/ai/events**
Server-Sent Events stream for real-time detection events.

### SSE Event Stream

**Event Format:**
```json
{
  "camera_id": "front-door-camera",
  "timestamp": "2024-01-15T10:30:00Z",
  "detections": [
    {
      "label": "person",
      "confidence": 0.95,
      "bbox": [100, 200, 300, 400]
    }
  ]
}
```

**Heartbeat Events:**
- Every 15 seconds: `: ping\n\n` (keep-alive)
- Detection events: `data: {...}\n\n`
- Connection remains open until client disconnect

### Authentication

All AI API endpoints require authentication:
- Uses existing BasicAuth middleware
- Camera ID validation against database
- Permission checks for camera access

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

**Hardware Requirements:**
- Minimum: 2GB RAM, 2 CPU cores, amd64/arm64
- Recommended: 4GB RAM, 4+ CPU cores

**Model Configuration:**
- Default model: `yolov11n.onnx` (4MB, 80 classes)
- Alternative models: `yolov8s.onnx` (~70MB, higher accuracy)
- Model path: `{dataDir}/models/{model_name}.onnx`

**StreamHub Integration:**
- AI detection subscribes to `"ai-{camera_id}"` events
- Frame skipping via atomic counter
- Non-blocking processing to prevent recording stalls

### Performance Tuning

**Frontend Settings:**
- `frameSkip`: Higher values reduce CPU usage but decrease detection frequency
- `confidenceThreshold`: Higher values reduce false positives but may miss objects
- `emaAlpha`: Lower values create smoother tracking but slower response

**Backend Settings:**
- Frame processing rate limited to 30 FPS maximum
- Batch processing for multiple cameras
- Graceful degradation on resource constraints

## Testing

The system has comprehensive testing coverage across both frontend and backend components, ensuring reliability across different hardware and browser configurations.

### Test Statistics

**Go Tests:**
- Total: 384 tests
- Engine tests: 28 (subprocess management, health checks)
- ONNX tests: 27 (model loading, inference)
- Probe tests: 19 (hardware detection)
- Downloader tests: 37 (download verification, caching)
- AI handler tests: 16 (API endpoints, SSE streaming)

**Frontend Tests:**
- Total: 289 tests
- Renderer tests: 30 (WebGPU/WebGL2 rendering, device loss)
- Runtime tests: 23 (AI execution provider detection)
- Inference tests: 35 (YOLO preprocessing/postprocessing)
- Connection tests: 66 (WebSocket protocol, reconnection)
- AI detection tests: 52 (frontend AI pipeline)

**Combined Total: 673 tests** (all passing)

### Hardware Test Targets

**RPi 3B (Minimal Resource Validation):**
- Tests Tier 3 fallback (no WebCodecs/WebGPU)
- Validates minimal resource constraints
- Ensures basic functionality on lowest-end hardware
- 1GB RAM, Cortex-A53 CPU, SD card storage

**Banana Pi M5 (Feature Testing + AI):**
- Tests full Tier 1 with WebGPU acceleration
- Validates backend AI inference
- Hardware transcode testing
- 4GB RAM, Amlogic S905X3, 1.8TB storage

**Docker VM (Clean-State Testing):**
- Integration testing with fresh state
- UI/UX validation across browsers
- DB migration experiments
- Ephemeral data for testing

### Test Categories

**Unit Tests:**
- Individual component isolation
- Mock dependencies for predictable testing
- Edge case validation (malformed data, error conditions)

**Integration Tests:**
- End-to-end pipeline testing
- Real video stream processing
- AI inference with actual camera feeds

**Performance Tests:**
- Frame rate validation (decode/render/ai)
- Memory usage monitoring
- CPU utilization tracking

**Browser Compatibility:**
- Cross-browser rendering pipeline
- Fallback mechanism validation
- WebGL2/WebGPU feature detection

The testing strategy follows the priority: Docker VM iterations → Banana Pi M5 validation → RPi 3B gates, ensuring the system runs reliably across the full spectrum of supported hardware configurations.