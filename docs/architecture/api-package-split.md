# internal/api 包拆分方案（迁移路径 · 2026-10-23 定案）

**现状**：`internal/api` 70 文件 / 20.7k 行产品码，62 个文件挂单一 `Handler` 结构体
（每域一个 `handlers_*.go` 文件，文件级已有纪律），消费者面广（app 层直接引用
`api.Handler`/`api.NewHandler`/各流 handler 构造器）。

**为什么不现在拆**：完整拆包 = Handler 结构体按域分体 + 共享设施
（`WriteError`/`writeJSON`/authMW/依赖字段）下沉叶子包 + 70 文件迁移 + 全量回归，
是一个「机械化脚本 + 专门会话」的活；混在功能分支里做会造成半拆状态，回归风险
大于收益。当前文件级组织对 20k 规模仍是可维护的。

**目标结构**（分四步、每步独立可合入、绿灯后进下一步）：

```
internal/api/            # Handler 聚合骨架 + 路由注册 + NewHandler
internal/api/httpx/      # 叶子包：WriteError/writeJSON/apiRequest 错误族/中间件
internal/api/cameras/    # cameras+camera_create+camera_audio*（含 GB 桥接入）
internal/api/streams/    # stream+flow+ws+webrtc+hls+flv（流媒体域）
internal/api/recordings/ # recording+merge+timelapse+audio 审计
internal/api/system/     # settings+system+update+transcode
internal/api/gb/         # gb28181+onvif 域（与库适配层同处）
```

**步骤**：
1. 抽 `httpx`（零依赖叶子，~500 行）——风险最低，验证构建面。
2. 逐域迁移（建议顺序 streams → recordings → cameras → system → gb），每域一个
   PR：域包暴露 `Register(r chi.Router, deps Deps)`，`Handler` 只留聚合与依赖注入。
3. 测试随域文件走（`*_test.go` 同迁）。
4. 最后清 `handler.go`（目标 <400 行纯装配）。

**验收门槛**：每步 CI 四绿 + `go vet`/lint 零告警 + 路由行为零变化
（golden 路由表测试先行：把现有 `Routes()` 的路径-方法快照钉进测试再动手）。

**触发条件**（何时启动）：① api 包再增 ~5k 行；② 域间出现相互引用（当前为零）；
③ 下个大功能落在 api 层需要独立子团队/独立迭代节奏时。
