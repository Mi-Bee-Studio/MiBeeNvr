# 并发架构

本文档记录 NVR 热路径的同步纪律，让贡献者既能推理正确性，**又**能避免重新
提出已被评估并否决的重构。它是确立各项决策的 GitHub issue（#219、#220、
#226、#230、#231、#234、#245、#246）的长期配套文档。

## 使用中的同步原语

| 原语 | 位置 | 原因 |
|---|---|---|
| `sync.Mutex` | `baseRecorder.mu`、`Manager.mu`（hls/cleanup/…）、`muxer.MP4Muxer.mu` | 生命周期/每分段状态的多字段一致性 |
| `sync.RWMutex` | `StreamHub.mu`、`CameraManager` 快照读 | 众多读者、稀少的写者 |
| `atomic.Pointer[T]` | `baseRecorder.codec`（#219）、`baseRecorder.audio`（#226）、`CameraManager.snapshot` | 一次性发布的不可变配置快照，被多个 goroutine 读取 |
| `atomic.Int64` | 帧计数器、码率统计 | 独立计数器，无需多字段一致性 |
| `sync.Map` | `CameraManager.lifecycleMu`（每相机生命周期守卫） | 按 key（相机）加锁，竞争稀少 |
| Go channel | `EventBus` 每订阅者投递、录像器 `frameCh` | MPSC 队列；运行时的无锁快路径在 ARM 上优化良好 |

## recorder：三层锁纪律（`internal/recorder/base.go`）

`baseRecorder` 的字段划分为三层。该划分由结构体上的文档注释约束，并由
`go test -race` 守护（见 `codec_race_test.go`、`audio_race_test.go`）。

### 第 1 层 —— 生命周期 + 每分段状态，由 `mu` 守护

```
status, cancel, done, muxer, audioTrackID, segStart
```

`mu` 串行化多字段一致性。`writeFrames` goroutine 是
`muxer`/`segStart`/`audioTrackID` 的唯一写者；锁的存在是为了**音频 RTP
回调**读者（一个独立 goroutine）和各生命周期方法。`createNewSegment` 在
同一次 `Lock`/`Unlock` 内一起发布 `muxer`+`segStart`+`audioTrackID`，使
音频回调观察到对齐的三元组（#226 修复了 `audioTrackID` 在该块之外发布
导致的撕裂视图 bug）。

### 第 2 层 —— 跨 goroutine 配置，置于 `atomic.Pointer` 之后

```
codec  atomic.Pointer[codecParams]   // SPS/PPS/VPS — #219
audio  atomic.Pointer[audioConfig]   // codec/sampleRate/channels/muxerConfig/g711* — #226
```

两者都在 `connectAndRecord` 期间**一次性**写入，被多个 goroutine 读取
（`writeFrames`、音频 RTP 回调，以及外部 HLS/WebRTC/WS/转发/状态访问
路径）。快照在 `Store` 之后不可变，这使并发读无需互斥锁即可免于数据竞争，
并保证视图连贯（不会出现 `codec`↔`muxerConfig` 撕裂配对）。字节切片
（`sps`/`pps`/`vps`、`muxerConfig`）在 `Store` 时深拷贝，因此快照独立于
写者的可复用缓冲区。

### 第 3 层 —— `writeFrames` 独占，无锁（单 goroutine 不变式）

```
trackID, curFinalPath, curTempPath, frameCount, lastFrameTime
```

只有 `writeFrames` goroutine 读写这些字段。HTTP handler 和 RTP 回调**不得**
触碰它们——这里刻意不加锁，若要在此添加跨 goroutine 访问，必须把该字段
提升到第 1 层或第 2 层。

### 为什么从两个 goroutine 调用 muxer 是安全的

`muxer.MP4Muxer` 本身是 goroutine 安全的：`WriteSample` 和
`WriteAudioSample` 各自获取 muxer 自己的 `sync.Mutex`
（`internal/muxer/mp4mux.go`）。因此视频写入（来自 `writeFrames`）与音频
写入（来自 RTP 回调）并发在构造上就是安全的——#226 关心的是音频**配置**
字段，不是 muxer。

## StreamHub：先快照后释放

`StreamHub.Broadcast` 只在快照消费者列表期间持有 `h.mu`，随后在调用每个
消费者回调之前释放锁。这是「一个生产者、多个动态订阅者」的经典模式，让
`Broadcast`（RTP 热路径，约 320 次广播/秒）不会在慢消费者回调期间持锁。
Subscribe/Unsubscribe 很罕见（播放/停止事件），所以 `h.mu` 在热路径上
无竞争。

## EventBus：快照模式 + MPSC channel

`Publish` 只为快照订阅者列表获取 `b.mu`，发送前即释放。每个订阅者的投递
是一条 **MPSC**（多生产者、单消费者）Go channel——许多相机/goroutine 并发
调用 `Publish`。Go channel 在这里是正确的原语（运行时的无锁快路径在 ARM
上优化良好）；在 ARM 弱内存模型下，手写 SPSC 环在多生产者场景是不安全
的。

---

# 已否决的重构（勿再提议）

三个无锁 / 低锁重构在并发审计 + Oracle 咨询期间被评估并**明确否决**。在此
记录，以免未来的贡献者重新翻案。（来源：#246。）

## ❌ 1. StreamHub 消费者 → COW `atomic.Pointer[[]*consumerEntry]`

**提案**：用写时复制的不可变消费者切片替换 `h.mu`；Subscribe/Unsubscribe
执行 append/filter + CAS。

**否决理由**：
- `h.mu` **在热路径上无竞争**——每个 hub 一个 RTP 生产者 goroutine；
  Subscribe/Unsubscribe 罕见（播放/停止事件）。
- N=20 个消费者时，锁内切片拷贝约 160 字节——在 Cortex-A53 上低于 1
  微秒。
- COW 反正也消灭不了 `h.mu`：`idrCache`（由 `h.mu` 保护）仍然需要它 →
  最终会同时存在两套同步机制。
- 每次 Subscribe/Unsubscribe 都增加 CAS 重试循环 + 分配。

**结论**：在 RPi 3B 上 320 次广播/秒的量级不值得。Mutex→RWMutex 微调
（#245）已是正当变更的上限。

## ❌ 2. StreamHub hooks → `atomic.Pointer[HubHooks]`

**提案**：把 8 个回调钩子（`OnDrop`、`OnBroadcast`、`OnBufferDepth`、…）
提取为一个不可变结构体，经 `atomic.Pointer.Store` 设置。

**否决理由**：
- 全部 8 个钩子都是在 `initStreamHub` 里**一次性**设置的普通字段，而它
  先于 `Start()` / 首次 `Broadcast` 运行。
- 经 goroutine 生成顺序（构造器 → `initStreamHub` → `Start`）存在
  happens-before 边。
- **今天不存在数据竞争。** 每次 `Broadcast` 加 `Load()` 属于零正确性收益
  的徒劳动作。

**替代方案**：结构体上的文档注释：钩子必须在 `Start()` 之前设置；首次
`Broadcast` 之后不得改动。（若将来加入运行时钩子热切换——如调试插桩——
届时再提升为 `atomic.Pointer[HubHooks]`；约 10 分钟的改动。）

## ❌ 3. EventBus → 每订阅者 SPSC 环形缓冲

**提案**：用手工实现的 SPSC（单生产者、单消费者）环形缓冲替换每个订阅者
的带缓冲 channel；消灭 `s.mu`。

**否决理由**：
- 每个订阅者 channel 上总线是 **MPSC**：许多相机/goroutine 并发调用
  `Publish`（已验证：`Publish` 在发送前释放 `b.mu`，发布者们会重叠）。
  手写 SPSC 环在**多生产者下不安全**——在 ARM 弱内存模型上会出现写丢失
  或内存损坏。
- Go channel 本来就是正确的原语，且在 ARM 上优化良好。
- 320 事件/秒 × 20 订阅者 = 6400 次发送/秒，channel 发送成本（A53 上约
  50ns）合计约 0.3ms/秒——单核的 0.03%。不是热点。

**结论**：拓扑就错了（是 MPSC，不是 SPSC）+ 不值得（channel 成本可忽略）。

---

## 已批准并落地的改动

| Issue | 改动 |
|---|---|
| #219 | `baseRecorder` SPS/PPS/VPS → `atomic.Pointer[codecParams]` |
| #220 | EventBus 关停时的排空守卫 |
| #226 | `baseRecorder` 音频配置 → `atomic.Pointer[audioConfig]`；三层纪律成文；`audioTrackID` 发布顺序修复 |
| #230 | HLS `m.mu`/`entry.mu` 锁序审计（无环）+ `writeLoop`/`idleWatchdog` WaitGroup 汇合 |
| #231 / #234 | 小米 `cs2.mu`/`cmdMu` + HLS 锁序审计（无环；已记录） |
| #245 | StreamHub `Mutex`→`RWMutex`（正当变更的上限） |
