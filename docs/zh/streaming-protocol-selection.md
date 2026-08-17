# 直播协议自动选择

> 监控大屏如何为**每个摄像头**自动选择最佳直播协议——基于编码、浏览器能力，并由 **Player Orchestrator** 在运行时做跨协议的降级**和**升级。

## 为什么要按摄像头自动选择？

典型的 NVR 部署是**混合机群**：一个 H.264 RTSP 摄像头、一个 H.265 ONVIF 摄像头、一个 ESP32 MJPEG 摄像头。没有单一协议能同时支持这三者：

| 协议 | H.264 | H.265 | JPEG/MJPEG | 延迟 | 浏览器支持 |
|------|-------|-------|------------|---------|-----------|
| WebRTC (WHEP) | ✅ | ❌（不能传 H.265） | ❌ | <500ms | 现代浏览器 |
| HTTP-FLV | ✅ | ❌（mpegts.js 解不了 H.265 → 黑屏） | ❌ | ~1s | Chrome/Edge/Firefox |
| HLS / LL-HLS | ✅ | ✅（原生 fMP4） | ❌ | 3-10s | 通用 |
| WebSocket (WebCodecs) | ✅ | ✅（libde265 WASM） | ❌ | <500ms | WebCodecs + HTTPS |
| MJPEG（轮询） | ❌ | ❌ | ✅ | 500ms | 通用 |

旧模式要求用户在设置中选一个"默认协议"，对某些摄像头是错的。现在大屏按摄像头自动选择，**新用户根本看不到协议选择器**——orchestrator 自动选当前浏览器+编码能支持的最低延迟协议。

**音频**（0.11.0 起）：WebRTC (WHEP) 将 G.711 与 Opus 音频直接复用进轨道（零转码）；
WebSocket 播放器走共享音频 WS 端点（G.711 全场景可用，AAC/Opus 需 WebCodecs 即
HTTPS 或 localhost）；HLS/HTTP-FLV/MJPEG 轮询不承载直播音频。录像回放的音频
（G.711/AAC/Opus）由浏览器 MP4 原生解码，不受协议选择影响。

---

## 架构（三层）

### 第 1 层 — 后端：per-camera 协议排名

`GET /api/cameras/{id}/protocols`（`internal/api/handler.go:handleCameraProtocols`）做三件事：

1. **探测真实编码** — 读取**运行中 recorder** 的实际编码，不是 DB 存的值。ONVIF 摄像头会撒谎（声明 H.264 实际流 H.265）；recorder 的 `detectEncoding`（RTSP DESCRIBE）是权威。
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
    {"protocol": "webrtc", "available": true,  "reason": ""},
    {"protocol": "flv",    "available": true,  "reason": ""},
    {"protocol": "hls",    "available": true,  "reason": ""},
    {"protocol": "wasm",   "available": true,  "reason": ""},
    {"protocol": "webrtc", "available": false, "reason": "WebRTC does not support H.265"}
  ],
  "encoding": "h265",
  "default": "hls"
}
```

### 第 2 层 — 前端：能力探测 + per-camera 候选链构建

每个路由（`Surveillance.svelte`、`LiveView.svelte`）在挂载时：

1. **一次性探测设备能力**，通过 `probeCaps()`（`web/src/lib/player/capabilities-cache.ts`）——`webCodecs`、`mseH265`、`wasmH265`、`webgpu`、`webgl2`、`hevcDecode`。结果缓存到 `sessionStorage`（tab 会话级），因此绝不在反应式路径里重复探测（在 Svelte `$effect` 里重复探测正是 WS 重连风暴的根因——见 `wasm-player-design.md`）。
2. **并行拉取** `/api/cameras/{id}/protocols`（`Promise.allSettled`），非阻塞。
3. **构建有序候选链**，通过 `buildCandidateChain(camera, resp, caps, opts)`（`web/src/lib/stream-selection.ts`）。返回该摄像头的编码+浏览器能力下**所有**可播放模式，延迟最优在前：
   ```
   PREFERENCE_ORDER = [wasm, webrtc, flv, hls, mjpeg]
   wasm   ← 仅前端模式，门控：WebCodecs（任意编码）或 libde265 WASM（HTTP 上的 H.265）
   webrtc ← 后端 available + 仅 H.264（H.265 被编码门控排除）
   flv    ← 后端 available +（H.264，或 H.265 仅当有 MSE H.265）
   hls    ← 后端 available（通用兜底）
   mjpeg  ← 仅 JPEG/MJPEG 摄像头（单元素链）
   ```
   链首是大屏初始渲染的协议；后续条目是降级目标。用户覆盖会把链**钉死**为单元素（见下"用户手动覆盖"）。

### 第 3 层 — Player Orchestrator：自适应降级 + 升级（运行时）

**Player Orchestrator**（`web/src/lib/player/orchestrator.svelte.ts`）持有 per-camera 状态机。每个路由创建一次，通过 Svelte context 提供给 `CameraPlayer`，内部持有：

- 候选链 + 当前激活索引（响应式 `$state`），
- 激活播放器上报的最新健康度（响应式 `$state`），
- 重连协调器（雷群防护，见下）。

每个播放器通过它已经在冒泡的 DOM `statechange` 事件上报标准化的 **`HealthState`**（`web/src/lib/player/health.ts`）——`ok | degraded | failed`。`CameraPlayer` 把它桥接到 `orchestrator.reportHealth()`。orchestrator 据此决策：

| 健康度 | 动作 |
|--------|------|
| `failed` | **立即降级**到链的下一档（如 webrtc → flv）。弹一次提示。 |
| `degraded` 超过 8s | **降级**（防抖——短暂 rebuffer 绝不能触发切换）。 |
| `ok` 持续 30s + 触发 | **尝试升级**到更低延迟的档。触发：tab 重新可见，或手动点"重试高清"。 |
| 升级后 5s 内未到 `ok` | **回退** + 该档冷却 60s（防震荡）。 |

**防震荡保证：**
- 升级仅在显式触发时发起（绝不靠定时器/轮询），所以抖动网络不会在两个协议间反复横跳。
- 被回退的升级会让该档冷却 60s。
- 用户钉死的覆盖会完全禁用自动降级/升级。

示例级联：WebRTC WHEP 失败 → 自动切 FLV（弹提示）→ FLV 失败 → 切 HLS → HLS 失败 → 才掉快照。之后若 WebRTC 恢复且切换了 tab，orchestrator 会升回 WebRTC（带 5s 探针保护）。

**可见性：** orchestrator 持有 tab 可见性（`setTabVisible`）。隐藏时暂停所有播放器（释放 WS / `RTCPeerConnection`）；可见时恢复，并给稳定的摄像头一次升回低延迟的机会。这取代了原来导致 WS 风暴的 per-player 可见性 `$effect`。

---

## 用户手动覆盖（"自动" vs 钉死）

**ProtocolSwitcher**（LiveView + 监控大屏）默认是 **"自动（推荐）"**——orchestrator 驱动选择。选某个具体协议会通过 `setCameraProtocolOverride()`（`web/src/lib/preferences.ts`，存 localStorage `mibee_nvr_prefs_proto_<cameraId>`）把链**钉死**为该单元素，orchestrator 视其为单元素钉死链：**不自动降级、不自动升级**——尊重用户的显式选择。再选"自动"会清除覆盖。

覆盖在链构建时校验：如果摄像头编码变了（如 H.264 → H.265）且钉死协议无法服务，覆盖被忽略，自动选择接管。

---

## `default_protocol` 设置

`streaming.default_protocol` 是**仅为后端保留的字段**，用于向后兼容。前端**不再暴露它的 UI**（"备用直播协议"选择器已移除——orchestrator 自动选）。当 `/protocols` 不可达时，`buildCandidateChain` 自行回落到通用 HLS 候选，所以该设置在前端实际已不用。它仍保留在 config/API 供旧部署。

---

## 关键文件

| 文件 | 职责 |
|------|------|
| `internal/api/handler.go` `handleCameraProtocols` | 后端：探测编码、查 handler、算 default |
| `internal/api/handlers_stream.go` `getCodecParams` + `CanHandle` | 每个 handler 的编码门控 |
| `web/src/lib/player/orchestrator.svelte.ts` `createPlayerOrchestrator` | **运行时状态机**：候选链、降级/升级、可见性、持有重连协调器 |
| `web/src/lib/player/health.ts` | 标准化 `HealthState` + `healthFromStreamState` / `healthFromConnectionState` 映射 |
| `web/src/lib/player/capabilities-cache.ts` `probeCaps` / `getCaps` | 一次性设备能力探测，缓存到 sessionStorage |
| `web/src/lib/stream-selection.ts` `buildCandidateChain` / `pickCameraMode` | 纯链构建 + 单点决策（有单测）。`pickCameraMode` 是 `buildCandidateChain[0]` 的薄包装，为旧调用者保留。 |
| `web/src/components/CameraPlayer.svelte` | 单一派发器：读 `orchestrator.activeMode(id)`，渲染对应播放器，通过 DOM `statechange` 桥接健康度 |
| `web/src/components/ProtocolSwitcher.svelte` | per-camera "自动" + 手动钉死（唯一覆盖写入者） |
| `web/src/lib/preferences.ts` `setCameraProtocolOverride` / `clearCameraProtocolOverride` | localStorage per-camera 覆盖存储 |
| `web/src/lib/webcodecs-player/capabilities.ts` `detectMSEH265` / `detectWebCodecs` / `getPlaybackTier` | 底层浏览器能力探测（被 capabilities-cache 消费） |

> 另见：`wasm-player-design.md`（WebCodecs 解码管线 + WS 重连风暴修复细节）。
