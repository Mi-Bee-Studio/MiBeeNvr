# WASM + WebCodecs 统一播放器与 AI 检测

## 什么是 WASM + WebCodecs 统一播放器与 AI 检测系统

WASM + WebCodecs 统一播放器与 AI 检测系统是一个现代的、分层的视频流架构，它使用基于 WebCodecs 的管线替换传统的浏览器视频播放器。它提供三级回退渲染（WebGPU → WebGL2 → 传统），支持使用 YOLOv11-nano 模型进行前端和后端 AI 对象检测，并在保持现代浏览器兼容性的同时，为 H.264/H.265 内容提供卓越性能。

**主要功能：**
- 三级回退渲染，自动降级
- WebSocket 二进制流协议，实现低延迟视频传输
- 前端 AI 检测，使用 ONNX Runtime WebGPU/WASM 执行提供商
- 后端 AI 检测，使用 ONNX Runtime 子进程（保持 CGO_ENABLED=0）
- 通过服务器发送事件实现实时检测事件流
- 解码和渲染的硬件加速
- 支持现代视频编解码器，包括 H.265（HEVC）

## 架构概览

该系统实现了复杂的分级回退架构，能够自动适应浏览器功能：

```
┌─ 第1级：WebGPU（零拷贝硬件） ──────────────────────────────────┐
│  WebCodecs 解码 → WebGPU 纹理（importExternalTexture）→ 渲染   │
│  ONNX Runtime WebGPU 执行提供商（AI，5-10ms/帧）               │
│  浏览器支持：Chrome 113+、Edge 113+、Safari 18+                 │
│  性能：解码 200+ FPS，AI 30+ FPS，渲染 60 FPS                   │
├───────────────────────────────────────────────────────────────────────────┤
└── ↓ WebGPU 设备丢失或不可用 → 自动回退到 WebGL2

┌─ 第2级：WebGL2 + WASM SIMD（混合模式） ─────────────────────────────┐
│  WebCodecs 解码 → WebGL2 Canvas（copyExternalImageToTexture）→ 渲染 │
│  ONNX Runtime WASM SIMD 执行提供商（AI，30-50ms/帧）              │
│  浏览器支持：Firefox 130+、Safari 16.4+、Chrome 94+（WebCodecs）  │
│  性能：解码 200+ FPS，AI 10-20 FPS，渲染 60 FPS                   │
├───────────────────────────────────────────────────────────────────────────┤
└── ↓ WebCodecs 不可用 → 自动回退到传统播放器

┌─ 第3级：传统兼容模式 ──────────────────────────────────┐
│  HTTP-FLV/HLS/WebRTC → hls.js/mpegts.js/WHEP → <video> 元素 │
│  浏览器支持：传统浏览器、移动浏览器、特定场景               │
│  性能：与当前体验相同                                    │
└───────────────────────────────────────────────────────────────────────────┘
```

### 数据流架构

```
摄像头 → RTSP 录制器 → StreamHub → WebSocket → 浏览器 Worker →
视频解码器 → 渲染器 → Canvas
                                                    ↓
前端 AI（ONNX Runtime）→ 检测事件 → SSE 流 → UI 覆盖层
                                                    ↓
后端 AI（ONNX Runtime 子进程）→ 检测结果 → 数据库 → API
```

### 分级检测算法

系统使用 `getPlaybackTier()` 动态确定最佳播放分级：

```typescript
export function getPlaybackTier(): PlaybackTier {
  if (detectWebCodecs() && detectWebGPU()) {
    return 'tier1'; // WebCodecs + WebGPU
  }
  if (detectWebCodecs() && (detectWebGL2() || detectOffscreenCanvas())) {
    return 'tier2'; // WebCodecs + WebGL2
  }
  return 'tier3'; // 传统回退
}
```

## WebSocket 协议

系统使用自定义的 WebSocket 二进制协议，通过高效的帧格式传输视频和编解码器配置数据。所有多字节整数使用大端序（网络字节序）。

### 协议概述

**消息类型：**
- `0x01` = CodecInfo（服务器 → 客户端）
- `0x02` = VideoFrame（服务器 → 客户端）
- `0x03` = AudioFrame（服务器 → 客户端，保留）
- `0x04` = KeyframeReq（客户端 → 服务器）

### 编码器信息（类型 0x01）

在流开始时发送一次的编解码器配置数据的二进制线格式：

```
[type:1byte][codec:1byte][profile:1byte][level:1byte][sps_len:2bytes_BE][sps:N][pps_len:2bytes_BE][pps:N][vps_len:2bytes_BE][vps:N]
```

**编码器标识符：**
- `4` = H.264（AVC）
- `5` = H.265（HEVC）

**字段详情：**
- `codec`：表示编码器类型的字节（H.264 为 4，H.265 为 5）
- `profile`：H.264 profile 字节或 H.265 profile_idc
- `level`：H.264 level 或 H.265 tier/level 组合
- `sps_len`、`pps_len`、`vps_len`：相应 NAL 集的长度（大端序）
- `sps`、`pps`、`vps`：原始 NAL 单元数据（无起始码）
- `vps` 字段仅 H.265 存在

### 视频帧（类型 0x02）

带有 NAL 单元的单个视频帧的二进制线格式：

```
[type:1byte][pts:8bytes_BE][is_keyframe:1byte][nalu_count:2bytes_BE][nalu1_len:4bytes_BE][nalu1]...
```

**字段详情：**
- `type`：始终为 `0x02`
- `pts`：90kHz 时钟中的呈现时间戳（来自 StreamHub）
- `is_keyframe`：布尔标志（1=关键帧，0=帧间）
- `nalu_count`：此帧中的 NAL 单元数量（最多 65535）
- `naluX_len`：每个 NAL 单元的长度（大端序）
- `naluX`：无 Annex B 起始码的原始 NAL 单元数据

**关键实现细节：**
- 所有 NAL 单元都发送不带 Annex B 起始码（`00 00 00 01`）
- 起始码在客户端解码前添加
- PTS 时间戳与 StreamHub 的 90kHz 时钟同步
- 帧跳过和错误恢复在解码器级别处理

## WebCodecs 解码管线

解码管线在 Web Worker 中运行，以避免阻塞主线程，使用 WebCodecs VideoDecoder API 进行硬件加速视频解码。

### Worker 消息协议

**Worker 消息：**
- `codec-info`：使用 SPS/PPS/VPS 数据配置解码器
- `video-frame`：解码带有时间戳和关键帧信息的原始 NAL 单元
- `reset`：重新初始化解码器状态（处理格式变化）
- `close`：清理资源并终止 worker

### 编码器配置

**H.264 编码字符串：**
```typescript
const codecString = `avc1.${profile}${constraint}${level}`;
// 示例："avc1.42C01E" 表示 High Profile @ Level 3.1
```

**H.265 编码字符串：**
```typescript
const codecString = `hvc1.${profile_idc}.6.${tier}${level}.B0`;
// 示例："hvc1.1.6.L93.B0" 表示 Main Profile @ Level 3.1
```

### NAL 单元处理

1. **起始码添加**：为每个 NAL 添加 Annex B 起始码（`00 00 00 01`）
2. **解码器配置**：首先发送 SPS/PPS 来初始化解码器
3. **帧解码**：NAL 单元按帧分组，带有 PTS 元数据
4. **错误恢复**：使用最新的编解码器参数自动重置解码错误

### 内存管理

- 每帧后调用 `VideoFrame.close()` 确保 GPU 内存安全
- Worker 管理的帧池，最小化分配
- worker 终止或解码器重置时自动清理

```typescript
// 示例 worker 消息处理
self.onmessage = (event) => {
  const { type, data } = event.data;
  
  if (type === 'codec-info') {
    decoder.configure(data.config);
  } else if (type === 'video-frame') {
    const frame = new VideoFrame(data.canvas, {
      timestamp: data.pts / 90000, // 将 90kHz 转换为秒
      duration: 1000 / 30 // 假设 30 FPS
    });
    decoder.decode(frame);
  }
};
```

## WebGPU 渲染器

WebGPU 渲染器提供两条渲染路径：零拷贝使用 GPUExternalTexture 和回退使用 copyExternalImageToTexture。这最大化性能同时保持兼容性。

### 双路径渲染

**零拷贝路径（优先）：**
```wgsl
@group(0) @binding(1) var ourTexture: texture_external;

@fragment
fn fs(input: VertexOutput) -> @location(0) vec4f {
  return textureSampleBaseClampToEdge(ourTexture, ourSampler, input.texcoord);
}
```

**回退路径（暂存纹理）：**
```wgsl
@group(0) @binding(1) var ourTexture: texture_2d<f32>;

@fragment
fn fs(input: VertexOutput) -> @location(0) vec4f {
  return textureSample(ourTexture, ourSampler, input.texcoord);
}
```

### 渲染管线

1. **设备初始化**：请求 WebGPU 适配器和设备
2. **资源创建**：创建渲染管线、采样器、绑定组
3. **帧处理**：导入外部纹理或从 VideoFrame 复制
4. **渲染通道**：将带纹理的四边形绘制到画布
5. **清理**：每次渲染后销毁外部纹理

### 设备丢失处理

```typescript
device.lost.then((info: GPUDeviceLostInfo) => {
  this.deviceLost = true;
  this.onDeviceLostCallback?.();
  // 在播放器级别自动回退到 WebGL2
});
```

**关键要求：**
- 外部纹理必须在每次渲染后销毁（WebGPU 规范）
- Canvas 格式使用 `navigator.gpu.getPreferredCanvasFormat()`
- Alpha 模式设置为 'opaque' 用于视频渲染
- RequestAnimationFrame 确保与垂直同步对齐的渲染

### 渲染循环

```typescript
render(videoFrame: VideoFrame): void {
  if (this.pendingFrame) {
    this.pendingFrame.close(); // 清理旧帧
  }
  this.pendingFrame = videoFrame;
  
  if (this.animationFrameId === null) {
    this.animationFrameId = requestAnimationFrame(() => this.renderLoop());
  }
}
```

## 三级回退

系统使用分级方法自动检测和适应浏览器功能，确保在保持广泛兼容性的同时获得最大性能。

### 分级检测

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

**功能检测函数：**
- `detectWebCodecs()`：检查 VideoDecoder 可用性
- `detectWebGPU()`：检查 navigator.gpu 可用性
- `detectWebGL2()`：尝试创建 WebGL2 上下文
- `detectOffscreenCanvas()`：检查 OffscreenCanvas API

### 运行时降级

当 WebGPU 设备丢失时，系统自动回退到 WebGL2：

```typescript
device.lost.then((info: GPUDeviceLostInfo) => {
  this.deviceLost = true;
  this.onDeviceLostCallback?.(); // 触发分级切换
});
```

**ConnectionManager 处理**指数退避重连：
- 初始：2 秒
- 后续：[2, 4, 8, 16, 32] 秒
- 最大延迟：32 秒，防止过度等待

### 浏览器支持矩阵

| 浏览器 | 第1级 | 第2级 | 第3级 | 说明 |
|--------|--------|--------|--------|-------|
| Chrome 113+ | ✅ | ✅ | ✅ | 完整第1级支持 |
| Firefox 130+ | ❌ | ✅ | ✅ | WebGL2 回退 |
| Safari 16.4+ | ❌ | ✅ | ✅ | WebGL2 回退 |
| Safari 18+ | ✅ | ✅ | ✅ | 第1级支持 |
| 传统浏览器 | ❌ | ❌ | ✅ | 当前播放器 |

## AI 检测（前端）

前端 AI 检测系统使用 ONNX Runtime Web 和 YOLOv11-nano 进行实时对象检测，完全在浏览器中运行，具有硬件加速。

### 后端选择

系统自动选择最佳执行提供商：

```typescript
// 来自 onnxruntime-web 的运行时检测
if (navigator.gpu) {
  // WebGPU 执行提供商（5-10ms/帧）
  sessionOptions.executionProviders = ['webgpu'];
} else if (detectWasmSimd()) {
  // WASM SIMD 执行提供商（30-50ms/帧）
  sessionOptions.executionProviders = ['wasm-simd'];
} else {
  // 回退到普通 WASM
  sessionOptions.executionProviders = ['wasm'];
}
```

### YOLOv11-nano 管线

**模型规格：**
- 输入：`[1, 3, 640, 640]` float32 NCHW
- 输出：`[1, 84, 8400]` 带置信度分数的边界框
- 类别：80 个 COCO 类别（人、车等）
- 模型大小：~4MB（量化）

**预处理管线：**

```typescript
async function preprocessFrame(
  videoFrame: VideoFrame,
  inputSize: number
): Promise<Float32Array> {
  // 1. 创建 ImageBitmap 用于安全绘制
  const bitmap = await createImageBitmap(videoFrame);
  
  // 2. 字母框到 640x640，灰色填充
  const { scale, padX, padY } = letterboxParams(
    bitmap.width, bitmap.height, inputSize
  );
  
  // 3. 绘制到离屏画布
  const canvas = new OffscreenCanvas(inputSize, inputSize);
  const ctx = canvas.getContext('2d');
  ctx.fillStyle = `rgb(114, 114, 114)`; // 灰色填充
  ctx.fillRect(0, 0, inputSize, inputSize);
  ctx.drawImage(bitmap, padX, padY, 
    Math.round(bitmap.width * scale), 
    Math.round(bitmap.height * scale)
  );
  
  // 4. 提取并转换像素为 Float32 CHW
  return convertToCHW(canvas);
}
```

**后处理管线：**

1. **YOLO 输出解析**：提取边界框和置信度分数
2. **非极大值抑制**：移除重叠检测（IoU 阈值 0.45）
3. **EMA 平滑**：应用指数移动平均进行跟踪（alpha 0.3）
4. **坐标映射**：从输入空间（640x640）映射到原始帧

### 性能优化

**帧跳过：**
- 可配置帧跳过（默认：每 3 帧）
- 在 30 FPS 视频中产生约 10 FPS 检测
- 平衡检测精度和性能

**模型缓存：**
- 通过 Cache API 下载并缓存模型
- 跟踪带百分比的下载进度
- 使用 SHA-256 验证的幂等下载

**内存管理：**
- 重用离屏画布防止泄漏
- 使用后关闭 ImageBitmap 对象
- 推理后输出张量清理

### 检测输出

```typescript
interface Detection {
  bbox: [number, number, number, number]; // [x1, y1, x2, y2] 在原始坐标中
  confidence: number; // [0, 1] 置信度分数
  classId: number; // COCO 类 ID（0-79）
  label: string; // 人类可读标签
}
```

## AI 检测（后端）

后端 AI 检测系统使用子进程模式来保持 CGO_ENABLED=0，同时提供设备推理能力。

### 子进程架构

系统生成外部 ONNX Runtime 二进制文件，并通过 stdin/stdout 上的 JSON 进行通信：

```
Go 进程 ↔ ONNX Runtime 二进制文件（stdin/stdout JSON）
```

**通信协议：**

```go
// 请求（stdin）
type detectRequest struct {
  Frame  string `json:"frame"`  // base64 编码的 JPEG
  Width  int    `json:"width"`  // 帧宽度（像素）
  Height int    `json:"height"` // 帧高度（像素）
}

// 响应（stdout）
type detectResponse struct {
  Detections []rawDetection `json:"detections"`
  Error      string         `json:"error,omitempty"`
}
```

### AiDetector 集成

AiDetector 与 StreamHub 系统集成：

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

**帧处理：**
- 使用 `"ai-{camID}"` 前缀订阅 StreamHub
- 使用原子计数器进行帧跳过（非阻塞）
- 通过 JPEG 转换和 base64 编码处理帧
- 结果流式传输到 SSE 客户端

### 崩溃恢复

引擎实现自动重启和指数退避：

```go
func backoffDuration(crashes int) time.Duration {
  if crashes <= 0 {
    return 0
  }
  d := time.Duration(1<<(crashes-1)) * time.Second
  if d > defaultRestartBackoff { // 30s 限制
    d = defaultRestartBackoff
  }
  return d
}
```

**退避时间表：**
- 第1次崩溃：1 秒
- 第2次崩溃：2 秒
- 第3次崩溃：4 秒
- 第4次崩溃：8 秒
- 5+ 次崩溃：16 秒（限制在 30 秒）

### 性能考虑

- 非阻塞设计不会阻塞录制管线
- 帧限制防止过度 CPU 使用
- 原子计数器确保线程安全
- 子进程故障时的优雅降级

## ONNX Runtime 二进制文件下载器

系统自动下载并管理当前平台的 ONNX Runtime 二进制文件，确保在不同架构上的最佳性能。

### 下载过程

**原子下载模式：**
1. 使用 SHA-256 验证检查现有二进制文件
2. 下载到临时文件
3. 使用哈希检查验证完整性
4. 重命名为最终位置（原子操作）

**下载源：**
- GitHub 发布：`microsoft/onnxruntime`
- 平台特定二进制文件：`onnxruntime-linux-x64`、`onnxruntime-linux-arm64`
- 版本选择匹配后端要求

**幂等操作：**
- 如果现有二进制文件通过验证则跳过下载
- 将二进制文件缓存在 `{dataDir}/tools/onnxruntime`
- 下载失败时保留现有二进制文件

### 文件结构

```
{dataDir}/
├── tools/
│   └── onnxruntime          # 平台特定二进制文件
└── models/
    └── yolov11n.onnx        # YOLOv11-nano 模型
```

## 硬件检测

系统进行全面的硬件检查，确保在启用 AI 检测之前 AI 功能可用。

### 检测标准

**平台要求：**
- 架构：仅 amd64 或 arm64（无 ARMv7）
- 内存：≥2GB RAM 可用
- CPU：≥2 核心以获得可接受性能

**二进制文件存在性：**
- 验证 `{dataDir}/tools/onnxruntime` 存在
- 检查文件可执行且具有正确架构

### 检测实现

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

### 检测行为

**如果检测失败：**
- AI 检测路由被禁用
- 警告消息记录到系统
- 前端显示 AI 不可用
- 不影响核心录制功能

**成功指标：**
- 支持平台上 100% 覆盖
- 最小开销（<1ms 每次检测）
- 线程安全操作
- 优雅降级

## 后端 AI API

后端提供 AI 检测管理和实时事件流的 REST API。

### API 端点

**GET /api/ai/status**
返回 AI 引擎可用性和系统状态：

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
为特定摄像头启用 AI 检测：

```json
{
  "camera_id": "front-door-camera"
}
```

**POST /api/ai/disable**
为特定摄像头禁用 AI 检测：

```json
{
  "camera_id": "front-door-camera"
}
```

**GET /api/ai/events**
用于实时检测事件的服务器发送事件流。

### SSE 事件流

**事件格式：**
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

**心跳事件：**
- 每 15 秒：`: ping\n\n`（保持连接）
- 检测事件：`data: {...}\n\n`
- 连接保持打开，直到客户端断开

### 身份验证

所有 AI API 端点都需要身份验证：
- 使用现有的 BasicAuth 中间件
- 摄像头 ID 与数据库的验证
- 摄像机访问权限检查

## 配置

AI 检测系统在前端和后端都可配置，以适应不同的硬件能力和需求。

### 前端配置

**localStorage 设置：**
```typescript
interface AIConfig {
  enabled: boolean;
  confidenceThreshold: number; // 0.1-0.9
  frameSkip: number; // 1-10 帧
  emaAlpha: number; // 0.1-1.0
}

// 默认值
const defaultConfig: AIConfig = {
  enabled: true,
  confidenceThreshold: 0.5,
  frameSkip: 3,
  emaAlpha: 0.3
};
```

**运行时更新：**
- 配置更改立即应用
- 无需页面重启
- 更改自动持久化

### 后端配置

**硬件要求：**
- 最低：2GB RAM，2 CPU 核心，amd64/arm64
- 推荐：4GB RAM，4+ CPU 核心

**模型配置：**
- 默认模型：`yolov11n.onnx`（4MB，80 个类别）
- 替代模型：`yolov8s.onnx`（~70MB，更高精度）
- 模型路径：`{dataDir}/models/{model_name}.onnx`

**StreamHub 集成：**
- AI 检测订阅 `"ai-{camera_id}"` 事件
- 通过原子计数器进行帧跳过
- 非阻塞处理，防止录制停滞

### 性能调优

**前端设置：**
- `frameSkip`：较高值减少 CPU 使用但降低检测频率
- `confidenceThreshold`：较高值减少假阳性但可能漏检对象
- `emaAlpha`：较低值创建更平滑的跟踪但响应更慢

**后端设置：**
- 帧处理率限制为最大 30 FPS
- 多摄像头批处理
- 资源约束时的优雅降级

## 测试

系统在前端和后端组件上具有全面的测试覆盖率，确保在不同硬件和浏览器配置下的可靠性。

### 测试统计

**Go 测试：**
- 总计：384 个测试
- 引擎测试：28 个（子进程管理、健康检查）
- ONNX 测试：27 个（模型加载、推理）
- 检测测试：19 个（硬件检测）
- 下载器测试：37 个（下载验证、缓存）
- AI 处理器测试：16 个（API 端点、SSE 流）

**前端测试：**
- 总计：289 个测试
- 渲染器测试：30 个（WebGPU/WebGL2 渲染、设备丢失）
- 运行时测试：23 个（AI 执行提供商检测）
- 推理测试：35 个（YOLO 预处理/后处理）
- 连接测试：66 个（WebSocket 协议、重连）
- AI 检测测试：52 个（前端 AI 管线）

**总计：673 个测试**（全部通过）

### 硬件测试目标

**RPi 3B（最低资源验证）：**
- 测试第3级回退（无 WebCodecs/WebGPU）
- 验证最低资源限制
- 确保在最低端硬件上的基本功能
- 1GB RAM，Cortex-A53 CPU，SD 卡存储

**Banana Pi M5（功能测试 + AI）：**
- 测试完整的第1级，带 WebGPU 加速
- 验证后端 AI 推理
- 硬件转码测试
- 4GB RAM，Amlogic S905X3，1.8TB 存储

**Docker VM（清洁状态测试）：**
- 与清洁状态的集成测试
- 跨浏览器 UI/UX 验证
- DB 迁移实验
- 用于测试的临时数据

### 测试类别

**单元测试：**
- 单个组件隔离
- 模拟依赖以进行可预测测试
- 边缘情况验证（畸形数据、错误条件）

**集成测试：**
- 端到端管线测试
- 真实视频流处理
- 使用实际摄像头源的 AI 推理

**性能测试：**
- 帧率验证（解码/渲染/AI）
- 内存使用监控
- CPU 利用率跟踪

**浏览器兼容性：**
- 跨浏览器渲染管线
- 回退机制验证
- WebGL2/WebGPU 功能检测

测试策略遵循优先级：Docker VM 迭代 → Banana Pi M5 验证 → RPi 3B 准入，确保系统在支持的硬件配置全范围内可靠运行。