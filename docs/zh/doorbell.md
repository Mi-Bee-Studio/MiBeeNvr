# SIP 门铃 / 门口机

标准 SIP 门禁设备（Doorbird、Akuvox、海康门口机——一切以 SIP UA 身份说话
的设备）可以直接呼叫 NVR：按铃后 Web UI 弹出来电框，显示呼叫者、关联相机
的抓拍（可选），并可**接听 / 拒绝**。接听后是真正的双向 G.711 对讲（浏览器
麦克风 ↔ 门铃喇叭）。与 [GB28181](gb28181-guide.md) 刻意解耦——编号、配置
互不相干，两套系统可并存。

默认**关闭**。

## 模式 A——门铃直接注册到 NVR

NVR 内置一个小型 SIP 注册服务器，把每台门禁指向它：

```yaml
doorbell:
  enabled: true
  listen: ":5070"          # SIP 监听（默认 UDP）
  realm: "mibee-nvr"
  users:
    - username: "front-door"
      secret: "一个足够长的随机密码"
      name: "前门"
      camera_id: "cam-xxxx"   # 可选：按铃时抓拍该相机
```

门铃侧配置：注册服务器 `sip:<nvr-ip>:5070`、SIP 用户名（`front-door`）、
密码、G.711 音频；若设备支持"按键呼叫"，呼叫目标随意（NVR 接受来自已注册
用户的 INVITE）。

## 模式 B——经 PBX 中转（Asterisk / FreeSWITCH）

门铃已经注册在现有 PBX 上时，NVR 改为作为一台 UA 注册到 PBX，INVITE 经
PBX 到达：

```yaml
doorbell:
  enabled: true
  outbound:
    server: "192.168.1.5:5060"   # PBX
    username: "nvr"
    secret: "pbx-password"
  users:                         # 允许按铃的门禁（按 From 用户匹配）
    - username: "front-door"
      name: "前门"
```

在 PBX 上把门铃的呼叫路由到分机 `nvr`。此模式下 PBX 是信任锚：经它到达的
INVITE 只要 From 用户在配置列表中即被接受——不对每条 INVITE 重复校验门铃
密码（SIP 经 PBX 的标准行为）。INVITE 的**来源地址必须与 `outbound.server`
的主机一致**，其它来源一律 403。

## 安全姿态

- **默认关闭**；不写 `enabled: true` 该节完全不生效。
- `lan_only`（默认**开**）直接拒绝来自非私网源地址的 SIP——仅做端口映射
  绝不会把服务暴露到公网。
- 注册模式强制 **Digest 鉴权**（nonce 一次性）；密码错误永远注册不上，
  未注册来源的 INVITE 一律 403——**模式 A 下匿名 INVITE 不可能成功**。
- 门铃与 PBX 密码**落盘加密**（与 `mqtt.password` 同一套 `ENC:` 机制；设置
  `NVR_ENCRYPTION_KEY` 启用）。
- 传输：`udp`（默认）、`tcp`、`tls`（需 `tls_cert`/`tls_key`）。

## 使用

铃响时每个打开的 Web UI 都会弹窗。**接听**打开对讲通道：你的麦克风进
门铃喇叭，门铃麦克风在浏览器里播放（默认开启回声消除）。**拒绝**挂断
来电，**挂断**结束通话。弹窗会同步出现在所有客户端——谁先接听谁通话，
其余自动收起。

每次按铃都会经共享快照管线抓拍配置的 `camera_id`——弹窗里展示，文件
保留在 `{storage}/snapshots/<camera>/`。

振铃/接听/拒绝/结束事件同时进事件流（`/api/events`，filter `doorbell.`），
开启 `mqtt.status_events` 后转发到 `{prefix}/event/doorbell.ring` 等主题，
供家庭自动化联动。

REST 接口（给集成用）：`GET /api/doorbell/calls`、
`POST /api/doorbell/calls/{id}/answer|reject|bye`，以及
`/api/doorbell/calls/{id}/talk` WebSocket（双向二进制 G.711 帧——与 GB28181
对讲同一套约定）。

## 说明与边界

- 音频只做 **G.711 A-law/μ-law**——SIP 门禁人手一个的编解码；offer 里
  没有 G.711 的 INVITE 以 488 拒绝。
- 对讲走浏览器 WebSocket（麦克风 → A-law → WS → RTP），与 GB28181 双向
  对讲同一条久经验证的管线，而非 WebRTC；若时延有更高要求可后续升级
  WHEP。
- INVITE 里的视频（Doorbird 会随音频发 H.264 RTP）本版不桥接——音频
  对讲加上关联相机的既有直播已覆盖场景。
- 无人接听 `ring_timeout`（默认 60 秒）后自动终止；已接听通话上限
  10 分钟。

## 故障排查

- **门铃注册不上**——确认设备用 Digest 鉴权（非明文）、密码一致、
  `listen`/防火墙放行 UDP 5070；日志组件名 `doorbell`。
- **弹窗不出现**——Web UI 需能访问事件 SSE（同源、已登录会话）。
- **对讲只有单向**——部分门禁需强制 `a=sendrecv`（多数默认已开）；检查
  设备的音频方向设置。

## 下一步

- [事件与自动化](api/README.md)——`doorbell.*` 事件主题
- [GB28181](gb28181-guide.md)——面向摄像头的平台侧协议
