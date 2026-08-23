# WASM + WebCodecs 统一播放器与 AI 检测

## 什么是 WASM + WebCodecs 统一播放器与 AI 检测系统

WASM + WebCodecs 统一播放器与 AI 检测系统是一个现代的、分层的视频流架构，它使用基于 WebCodecs 的管线替换传统的浏览器视频播放器。它提供三级回退渲染（WebGPU → WebGL2 → 传统），支持使用 YOLOv11-nano 模型进行**浏览器端** AI 对象检测（ONNX Runtime Web），并在保持现代浏览器兼容性的同时，为 H.264/H.265 内容提供卓越性能。

**主要功能：**
- 三级回退渲染，自动降级
- WebSocket 二进制流协议，实现低延迟视频传输
- 前端 AI 检测，使用 ONNX Runtime WebGPU/WASM 执行提供商
- 后端仅提供 AI 配置与 ONNX 模型文件（不做服务端推理——保持静态 `CGO_ENABLED=0` 二进制）
- 解码和渲染的硬件加速
- 支持现代视频编解码器，包括 H.265（HEVC）

## 架构概览

该系统实现了复杂的分级回退架构，能够自动适应浏览器功能：

```text
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

```text
摄像头 → RTSP 录制器 → StreamHub → WebSocket → 浏览器 Worker →
视频解码器 → 渲染器 → Canvas
                                                    ↓
前端 AI（ONNX Runtime Web）→ 检测结果 → UI 覆盖层（浏览器内完成，无服务端往返）
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

```text
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

```text
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

**重连与 WS 风暴修复：** 重连分两层：

1. **单协议内** — `ConnectionManager`（`web/src/lib/webcodecs-player/connection.ts`）持有 WebCodecs 播放器的 WebSocket 生命周期与重连逻辑。它是**幂等**的（`connect()` 若已有 OPEN/CONNECTING 的 socket 则 no-op），并用 `_intentionalClose` 标志区分主动关闭（`disconnect()`/`destroy()` 调 `close()` 不带 code → `CloseEvent.code 1005`）与真正的崩溃。这个标志正是**修复 WS 重连风暴**的关键：之前每次导航/切 tab 的关闭都被当成失败、重新排到一个已销毁的 coordinator 上，产生 "WebSocket closed before the connection is established" 日志刷屏（实测 9 万+ 行）。
2. **跨协议** — **Player Orchestrator**（`web/src/lib/player/orchestrator.svelte.ts`）决定是否整个换协议。WasmPlayer 通过 DOM `statechange` 事件上报健康度；当上报 `failed`（或 `degraded` 超 8s），orchestrator 把该摄像头切到候选链的下一档（如 wasm → hls）。见 `streaming-protocol-selection.md` 第 3 层。

重连退避（经 coordinator 协调时）：初始 1s，倍增，封顶 30s，后端 HTTP 503 压力后 10s 全局冷却。tab 可见性由 orchestrator 持有（`setTabVisible`），不再是 per-player `$effect`。

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

## 后端在 AI 检测中的职责

> **后端不执行任何 AI 推理。** 服务端没有子进程、没有 ONNX 运行时、没有硬件探测、也没有模型下载器。本节用于记录该决策以防止回退。

所有目标检测都在浏览器端运行（见上方 [AI 检测（前端）](#ai-检测前端) 及 `web/src/lib/ai-detection/`）。Go 服务端与 AI 相关的职责仅限于：

1. **配置持久化** —— AI 设置与 ROI 区域，通过 `/api/ai/*`（见下方 [后端 AI API](#后端-ai-api)）。
2. **提供模型文件** —— 公开路由 `GET /models/{filename}` 提供 `{storage_root}/models/<file>`，供浏览器获取 `.onnx`。

### 为什么不在后端做推理

- NVR 以静态 `CGO_ENABLED=0` 二进制形式发布，需交叉编译到 ARM64/ARMv7。在 Go 中引入 ONNX 绑定（`libonnxruntime`）会引入 C 依赖、使二进制膨胀，并破坏 ARM 交叉编译。FFmpeg 已是最重的依赖。
- 最低支持目标（RPi 3B，1GB RAM，Cortex-A53，无 ML GPU）无法在录制/流媒体之外再承担推理开销。
- 检测针对已解码的实时流、按观看者进行，因此在浏览器端运行可避免服务器重复解码，并随观看者而非摄像头数量扩展。

如果将来重新考虑后端推理，必须以**进程外** sidecar（子进程 + IPC）方式运行，绝不链接进主二进制——以保持 `CGO_ENABLED=0` 静态构建保证。

## 后端 AI API

> **后端不执行任何 AI 推理，也不提供推理/事件 API。** 检测完全在浏览器端运行（见 `web/src/lib/ai-detection/`）。Go 后端与 AI 相关的职责仅限于 (a) 持久化配置与 ROI 区域，(b) 提供模型文件。

后端 AI 相关 API **仅为配置**：

| 端点 | 用途 |
|------|------|
| `GET /api/ai/status` | 读取全局 AI 配置 |
| `PUT /api/ai/config` | 更新配置（启用、阈值、模型 URL） |
| `GET`/`POST`/`PUT`/`DELETE /api/ai/zones[/{id}]` | ROI 区域增删改查 |
| `GET /models/{filename}` | 向浏览器提供 `.onnx` 模型（**公开**，无需认证） |

**不存在** `POST /api/ai/enable`、`POST /api/ai/disable` 或 `GET /api/ai/events` SSE 端点。完整的请求/响应契约见 [AI 检测 API](api/ai-detection.md)。

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

后端将 AI 配置存储在 YAML 的 `ai:` 块（`config.AIConfig`）中，并通过 `PUT /api/ai/config` 原子地持久化运行时更改。**AI 没有后端硬件要求**——服务端不运行推理。

- `enabled`、`confidence_threshold`、`frame_skip_rate`、`model_url` —— 全局 AI 设置
- `enabled_cameras` —— 允许在 UI 中显示 AI 的摄像头
- `zones` —— `map[cameraID][]ROI`，归一化 `[0,1]` 多边形顶点

**模型文件：** 将 `.onnx` 模型放入 `{storage_root}/models/`（`mibee-nvr download-model` CLI 会为默认的 `yolo11n.onnx` 完成此操作）。浏览器通过 `GET /models/{filename}` 获取。

### 性能调优

**前端设置（浏览器端推理）：**
- `frameSkip`：较高值减少客户端 CPU 但降低检测频率
- `confidenceThreshold`：较高值减少假阳性但可能漏检对象
- `emaAlpha`：较低值提供更平滑的跟踪但响应更慢

## 测试

浏览器端推理流水线有直接的单元测试；后端的区域/配置逻辑用 Go 测试。**没有**后端推理 / ONNX / 探测 / 下载器测试——这些代码不存在。

### 测试清单

**Go 测试（后端——仅配置与区域）：**
- `internal/ai/zones_test.go` —— 43 个测试：区域增删改查、`PointInPolygon`、`FilterDetectionsByZone`、`GetEnabledZones`、校验
- `internal/config/config_ai_test.go` —— 15 个测试：AI 配置默认值 + 校验（阈值范围、帧跳过、区域点/坐标）

**前端测试（浏览器端推理——vitest）：**
- `web/src/lib/ai-detection/inference.test.ts` —— YOLO 输出解析、NMS、IoU、sigmoid、EMA 平滑、坐标映射
- `web/src/lib/ai-detection/runtime.test.ts` —— WebGPU 检测、WASM 回退、模型缓存、URL 白名单/SSRF 校验、init/run/dispose 生命周期（ONNX 运行时已 mock）

### 缺口

- 没有端到端测试驱动完整流水线（加载模型 → 解码帧 → 检测 → 渲染叠加）。这是最大的测试缺口。
- 没有针对标注帧的准确率/召回率基准测试。
- 推理用 mock 的 ONNX 运行时测试；没有测试运行真实 `.onnx` 模型。