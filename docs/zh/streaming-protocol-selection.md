# 直播协议自动选择

> 监控大屏如何为**每个摄像头**自动选择最佳直播协议——基于编码、浏览器能力，并在运行时跨协议降级。

## 为什么要按摄像头自动选择？

典型的 NVR 部署是**混合机群**：一个 H.264 RTSP 摄像头、一个 H.265 ONVIF 摄像头、一个 ESP32 MJPEG 摄像头。没有单一协议能同时支持这三者：

| 协议 | H.264 | H.265 | JPEG/MJPEG | 延迟 | 浏览器支持 |
|------|-------|-------|------------|---------|-----------|
| WebRTC (WHEP) | ✅ | ❌（不能传 H.265） | ❌ | <500ms | 现代浏览器 |
| HTTP-FLV | ✅ | ❌（mpegts.js 解不了 H.265 → 黑屏） | ❌ | ~1s | Chrome/Edge/Firefox |
| HLS / LL-HLS | ✅ | ✅（原生 fMP4） | ❌ | 3-10s | 通用 |
| WebSocket (WebCodecs) | ✅ | ✅（libde265 WASM） | ❌ | <500ms | WebCodecs + HTTPS |
| MJPEG（轮询） | ❌ | ❌ | ✅ | 500ms | 通用 |

旧模式要求用户在设置中选一个"默认协议"，对某些摄像头是错的。现在大屏按摄像头自动选择。

---

## 四层架构

### 第 1 层 — 后端：per-camera 协议排名

`GET /api/cameras/{id}/protocols`（`internal/api/handler.go:handleCameraProtocols`）做三件事：

1. **探测真实编码** — 按以下优先级解析编码（DB 存的值可能过时或错误，例如小米摄像头存的是 h264 实际流 h265）：
   - **① 运行中 recorder 的探测值**（权威）— 经 `getCodecParams(rec)` 从真实视频包得出（ONVIF 走 RTSP DESCRIBE；小米走首包 codec ID）。但在首包到达前、或小米 P2P 重连期间（其 codec 字段会重置）返回空。
   - **② HLS muxer 冻结值兜底**（`hls.Manager.CodecFor(id)`）— HLS muxer 在流启动时把编码烤进 track，**recorder 重连期间也不丢**。所以 recorder 探测为空时用这个兜底，避免重连窗口期闪烁回 DB 的旧值导致前端误路由黑屏。仅当该摄像头有活跃 HLS 流条目时可用（从未启动 / 空闲淘汰后不可用）。
   - **③ DB 存的值** — 最后保底。
   - ONVIF 摄像头会撒谎（声明 H.264 实际流 H.265）；recorder 探测是权威。
2. **查 stream handler 能力** — 遍历已注册的 handler，每个 `CanHandle(codec)`：
   - WebRTC：仅 H.264
   - FLV：仅 H.264（mpegts.js 浏览器端解不了 H.265）
   - HLS / LL-HLS：H.264 + H.265
   - WebSocket (wasm)：H.264 + H.265（需 WebCodecs）
   - MJPEG：仅 JPEG/MJPEG
3. **算 default** — 优先用用户配的 `streaming.default_protocol`（如果该协议对此编码可用）；否则按 `webrtc → flv → ll-hls → hls → mjpeg` 取第一个可用的。

**响应：**
```json
{
  "protocols": [
    {"Protocol": "webrtc", "Available": true,  "Reason": ""},
    {"Protocol": "flv",    "Available": true,  "Reason": ""},
    {"Protocol": "hls",    "Available": true,  "Reason": ""},
    {"Protocol": "wasm",   "Available": true,  "Reason": ""},
    {"Protocol": "webrtc", "Available": false, "Reason": "WebRTC does not support H.265"}
  ],
  "encoding": "h265",
  "default": "hls"
}
```

### 第 2 层 — 前端：并行拉取 + 缓存

大屏 `onMount` 时为每个选中摄像头**并行**调 `getCameraProtocols(id)`（`Promise.allSettled`），缓存到 `Map<cameraId, ProtocolsResponse>`。**非阻塞**：摄像头先用 legacy 默认渲染，响应到了再重新解析。失败存 `null`（退回 legacy 默认，不阻塞大屏）。

浏览器能力一次性检测：
- `probeMSEH265()` / `detectMSEH265()` — MSE 能否解 H.265？**注意**：`MediaSource.isTypeSupported('hvc1')` 在 Chromium/Edge 上是**假阳性**（OS 的 HEVC 解码器为原生 `<video>` 注册，MSE 接受 hvc1 字节却永不缓冲 → 黑屏）。因此用权威探测 `probeMSEH265()`：建一个临时 hvc1 SourceBuffer、追加真实 HEVC init segment、检查 `video.buffered`。结果缓存在 sessionStorage。未探测解析前 `detectMSEH265()` 保守返回 false。（Linux 桌面、无 HEVC 扩展的 Windows 为 false）
- `detectWebCodecs()` — 有无 `VideoDecoder`？（需 HTTPS 或 localhost）
- `detectWasmH265()` — libde265 WASM 是否可用？（plain HTTP 上 H.265 的唯一可靠解码路径，Canvas2D 渲染）

### 第 3 层 — `getCameraMode(camera)` 决策级联

每格渲染时调用，优先级从高到低：

```
① runtimeFallback[camera.id]          ← 运行时降级（第 4 层）
② pickCameraMode(camera, resp, caps, opts):
   a. JPEG/MJPEG 编码 → 'mjpeg'       ← 早短路（ONVIF JPEG delegate 报 protocol=onvif
                                          但流 JPEG；走 HLS 会黑屏）
   b. 协议不支持 HLS（rtmp/srt ingest）→ 'snapshot' 或 'unsupported'
   c. 用户覆盖（localStorage per-camera）→ 若对该编码仍可用则用它
   d. 后端 default → 叠加浏览器能力修正：
        webrtc + H.265           → 'hls'  （WebRTC 不能传 H.265）
        flv + H.265 + 无 MSE     → 'hls'  （FLV 无 H.265 MSE 会黑屏）
        wasm + 无 WebCodecs      → 'hls'  （WASM 播放器需 WebCodecs）
        ll-hls / hls             → 'hls'  （hls.js 处理低延迟）
   e. 后端响应为 null（不可达）→ legacy 全局 default_protocol（最终保底）
```

**关键**：不是"一个全局协议套所有摄像头"。混合机群每格自动选不同协议。

### 第 4 层 — 运行时跨协议降级

当播放器耗尽重连次数（原本会掉到静态快照），现在先调 `onProtocolFailed`：

```
handleProtocolFailed(cameraId, currentMode):
  从后端响应构建降级链：[webrtc, flv, hls, mjpeg] 过滤出 Available 的
  找 currentMode 在链中的位置
  如果有下一个协议：
    设 runtimeFallback[cameraId] = next
    用新播放器重挂该格
    弹提示："为提升稳定性，已自动切换到 FLV 播放"
    返回 true（播放器：先别掉快照）
  否则（链尽）：
    返回 false（播放器：掉到快照）
```

示例级联：WebRTC WHEP 失败 → 自动切 FLV → FLV 失败 → 切 HLS → HLS 失败 → 才掉快照。

`runtimeFallback` 在切换摄像头选择时清空（重新从自动选开始）。

---

## 用户手动覆盖

用户可在 **LiveView 页**（`#/live/{id}`）的 ProtocolSwitcher 里为某个摄像头固定协议。存储在 `localStorage`（`mibee_nvr_prefs_proto_<cameraId>`），大屏的 `getCameraMode` 会读它（第 3 层 c）。

**重要**：只有**手动**选择才写覆盖。运行时自动降级（第 4 层）不写——否则一次临时故障会永久钉死更差的协议。覆盖也会校验：如果摄像头编码变了（如 H.264 → H.265）且固定协议无法服务，覆盖被忽略，自动选择接管。

---

## `default_protocol` 设置

配置中的 `streaming.default_protocol` 现在是**备用**——仅在无法查询摄像头能力时（摄像头正在连接、端点不可达）使用。不再是主要协议选择。设置页标注为"备用直播协议"以反映此变化。

---

## 关键文件

| 文件 | 职责 |
|------|------|
| `internal/api/handler.go` `handleCameraProtocols` | 后端：探测编码（recorder → HLS muxer 兜底 → DB）、查 handler、算 default |
| `internal/api/handlers_stream.go` `getCodecParams` + `CanHandle` | 每个 handler 的编码门控；`getCodecParams` 经 `model.HLSProvider.CodecParams()` 读 recorder 真实编码 |
| `internal/hls/manager.go` `CodecFor` | HLS muxer 冻结的编码查询（recorder 重连期间保真的兜底来源） |
| `web/src/lib/stream-selection.ts` `pickCameraMode` / `fallbackChain` / `nextAfter` | 纯决策逻辑（有单测） |
| `web/src/routes/Surveillance.svelte` `getCameraMode` / `handleProtocolFailed` | 大屏集成：拉取、缓存、解析、降级 |
| `web/src/components/ProtocolSwitcher.svelte` | LiveView per-camera 覆盖（唯一覆盖写入者） |
| `web/src/lib/preferences.ts` `getCameraProtocolOverride` | localStorage per-camera 覆盖存储 |
| `web/src/lib/webcodecs-player/capabilities.ts` `detectMSEH265` / `detectWebCodecs` | 浏览器能力检测 |
