# 已知问题：H.265 多相机宫格 —— WebCodecs 降级 + HLS 振荡

> **状态**：✅ **已修复**（issues #107、#108）。本文档保留作复盘 / 回归参考。
> **影响范围**：HTTPS 下 4 相机宫格中的 H.265 相机（小米 CS2、ONVIF H.265）。
> **影响（修复前）**：4 台相机中可能有 1 台降级到 HLS 并触发冻结控制台的
> 状态振荡循环；DB `encoding` 陈旧的 H.265 相机在实时视图无法播放。

## 问题

在一个 4 相机全部为 H.265 的监控宫格中（HTTPS，WebCodecs 可用）：

- **3/4 的相机**保持在 WebCodecs 上正常播放。
- **1 台相机**（通常是 P2P 连接最慢的那台）降级到 HLS，随后 HLS 的 hls.js
  在 `loading ↔ buffering` 之间每秒振荡数百次（观测到 55 秒内 7428 次
  statechange 事件）。每次状态变化都触发 Svelte 的 `$effect` 分发器，压垮
  响应式调度器并冻结浏览器控制台。

## 根因（最初诊断时的认识）

1. **负载下的 WebCodecs 降级**：4 条 WS 连接并发时，P2P 连接最慢的小米
   CS2 相机在启动阶段帧交付延迟。无媒体看门狗（WasmPlayer 里的
   `NO_MEDIA_TIMEOUT_MS = 30000`）可能把启动慢的相机误判为「无媒体」并
   上报失败。单相机测试证实这类相机在 WebCodecs 上是稳定的——问题是多
   相机负载造成帧交付时序竞争。（注：最终修复并未改动该超时；降级本身
   是正确行为。冻结来自未去抖的状态分发放大了由此产生的状态切换，另行
   修复。）

2. **HLS H.265 状态振荡**：降级到 HLS 后，hls.js 把 H.265 喂给 MSE，MSE
   报告 `isTypeSupported('hvc1') = true`，但实际解码停滞在
   `readyState=1`（HAVE_METADATA）。hls.js 在 `loading ↔ buffering` 间快速
   交替，`VideoPlayer.$effect(dispatchStateChange)` 在每次切换时触发 →
   Svelte 调度器过载 → 控制台冻结。

3. **调查修正（issue #107 深挖）**：对 H.265/无 MSE 的相机，链式降级实际上
   到不了 HLS（编解码门控把它排除在外），真正的触发因素是瞬时的
   `/protocols` 获取失败，使 `buildCandidateChain` 经其 `!resp` 分支塌缩成
   强制单一 `hls` 链——完全绕过了编解码门控。无论哪条路径，未去抖的状态
   分发都把随后的 HLS H.265 停滞变成了控制台冻结。

## 已应用的修复（本分支 —— PR #120，4 个提交）

### Issue #107 —— 状态振荡风暴（根因 + 级联）

1. **去抖 + 去重 + 异步状态分发**（`web/src/lib/player/dispatch.ts`）：
   每个播放器适配器（Video/Flv/Wasm/WebRTC）都把其 `statechange`
   CustomEvent 送入一个共享分发器，后者 (a) 丢弃与上一条相同的状态，(b)
   把非 `playing` 状态在 500ms 窗口内合并为一次尾随分发。**每次发射都被
   推迟出同步调用栈**——`'playing'`（恢复）在 `queueMicrotask` 上冲刷
   （近似即时但不是同步），其他状态走 `setTimeout`。这一点至关重要：在
   播放器 `$effect` 内同步发射会在 Svelte 的 effect 冲刷期间到达
   `orchestrator.reportHealth → setSlot`（一次响应式 `$state` 写入），叠加
   播放器重挂载就会产生 `effect_update_depth_exceeded`。异步推迟保证
   DOM dispatch → reportHealth → setSlot 链在新任务中运行，绝不发生在
   effect 冲刷内。hls.js buffering↔playing 振荡爆发现在塌缩为每窗口约 1
   个事件，而不是每秒数千个。

2. **健康状态未变化时 `reportHealth` 短路**（`orchestrator.svelte.ts`）：
   H80 触发 `effect_update_depth_exceeded` 的最深层根因。`reportHealth`
   此前在**每次**上报都调用 `setSlot`（它执行 `slots = { ...slots }`——
   生成新对象），即使 `(status, reason)` 与当前健康状态完全相同。由于
   `healthFromStreamState('error')` 每次调用都返回新对象（`since:
   Date.now()` 不同），引用比较无法短路。一台 WS 始终到不了稳态
   `'playing'` 的 H.265 相机（如 H80）反复上报 `'failed'` → 每次上报都翻动
   `slots` → 每个 `$derived(mode)` 重跑 → 播放器 `$effect` 重入 → 递归
   溢出。修复：当 `slot.health.{status,reason}` 与传入的
   `h.{status,reason}` 相等时，跳过响应式 `setSlot` 写入。定时器/降级逻辑
   使用非响应式的内部簿记，不受影响。同时删除了 `'ok'` 路径上冗余的尾随
   `setSlot`——它会让在 ok/degraded 边界振荡的相机重新武装该循环。

### Issue #108 —— H.265 实时视图黑屏（编解码数据流完整性）

3. **CameraPlayer 喂给播放器的是录像器探测的编解码，而不是 DB 值**：
   H80 黑屏的直接原因。`CameraPlayer` 从 `camera.encoding`（DB 值）推导
   播放器的 `codec` prop。H80 在 DB 里存的是 `h264`，实际流出的是 `h265`；
   orchestrator 正确地基于探测到的 `h265` 构建 wasm/WebCodecs 链，但
   CameraPlayer 喂给 WasmPlayer 的是 `codec='h264'`，于是它配置了一个
   H.264 解码器，静默解码失败（数据在流动——WS 101 + 二进制帧——但解码器
   不匹配导致永久黑屏）。修复：orchestrator 暴露
   `resolvedEncoding(cameraId)`（与构建候选链所用的同一个
   `resolveEncoding(camera, resp)`），CameraPlayer 使用它并以 DB 作兜底。
   这是一个通用的数据流修复——任何 DB 编码与真实流漂移的相机都受益，不只
   是 H80。

4. **`buildCandidateChain` 的 `!resp` 分支不再把 H.265 困在 HLS 上**：当
   `/protocols` 获取失败（resp=null）且相机是 H.265 并有可用解码路径
   （WebCodecs 或 libde265 WASM）时，链现在是 `[{wasm}]` 而不是强制的
   `[{hls}]`。经 MSE 的 HLS H.265 在大多数浏览器里是黑屏 + 振荡；wasm
   总是可用。H.264 行为不变。

5. **实时视图不再冲掉自己的注册**：`listProtocols()` 的 `.then()` 过去会
   重新调用 `registerCamera(makeRegistration(camera, null, …))`，与
   `loadCamera()` 里完成的真实注册竞争。null 的 `resp` 会顶着编解码门控把
   链塌缩成强制 HLS。已移除；`listProtocols()` 现在只填充全局
   `protocolsMap`，并在结果返回时（由 `cameraRegistered` 旗标守卫）用**同
   一个**探测 resp 重新注册。

6. **实时视图 ProtocolSwitcher 读取探测到的编码**：`cameraEncoding` prop
   现在优先取 `cameraProtocolsResp.encoding`（录像器探测，权威来源），
   而非可能陈旧的 DB `camera.encoding`，因此存成 h264 的 H.265 相机仍显示
   其 H.265 徽章并隐藏 WebRTC。

### 回归覆盖

- `dispatch.test.ts`（12 项）：去重、去抖合并、**`'playing'` 不得同步发射**
  （effect_update_depth 守卫）、`'playing'` 取消待发的尾随分发、dispose
  取消两个队列。
- `orchestrator.test.ts`（+3 项）：回到 ok 的高频 buffering↔playing 振荡
  不会降级；重复的 degraded 上报只武装一个降级定时器；**链耗尽后重复的
  `'failed'` 不会重写 `slots`**（H80 回归——slot 引用在 50 次相同上报间
  保持稳定）。
- `stream-selection.test.ts`（+4 项）：`!resp` + H.265 + WebCodecs/wasm →
  `[{wasm}]`；H.264 + `!resp` 仍走 HLS（回归守卫）。

## 验证（Banana Pi M5，真实相机）

- **#107 宫格风暴**：4 相机 H.265 宫格浸泡 30 秒以上保持稳定；控制台无
  冻结；点击响应 100–200ms（修复前的风暴在 55 秒内冻结控制台）。所有
  H.265 相机保持在 WebCodecs（从未降级到 HLS）。
- **effect_update_depth_exceeded 回归**：H80（cam-608e6966）加入宫格 +
  60 秒浸泡（越过 30 秒 `noMediaTimer` + 多轮 WS 重连周期）：无冻结，
  `allAlive: true`，展开/收起/拖拽全部响应正常。
- **#108 H80 黑屏**：实时视图现在在 WebCodecs 上以正确的 H.265 解码器
  显示**「实时」**（之前是「快照模式」/黑屏）。ProtocolSwitcher 显示
  "H.265 WebCodecs"（之前是 "H.264"）。WS 握手确认正常（101 + 二进制
  H.265 帧）——黑屏纯粹是解码器配置不匹配，不是连接问题。
- 后端日志干净；9 台相机在录；健康相机无回归。

## 已修复的内容（player-orchestrator-adaptive 分支，PR #106）

- ✅ 后端编码探测：小米相机现在正确上报 h265（之前是 h264）
- ✅ 消除三重可见性冲突（ConnectionManager/WasmPlayer/orchestrator）
- ✅ WS 僵尸连接/握手/墙钟三重守卫
- ✅ 解码器初始化暂停期间也记录帧交付
- ✅ WebRTC WHEP 超时 + 连接超时 + firstFramePlayed
- ✅ 解码错误阈值提高（10→50）
- ✅ Service Worker 每次构建的缓存版本
- ✅ 固定协议的终态失败回退到 HLS
