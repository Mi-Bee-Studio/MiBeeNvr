# 子码流（低分辨率第二流）

> 适用于 MiBeeNvr v0.12.0

多数 IP 相机会同时输出两路流：**主码流**（高分辨率，用于录像）和**子码流**（低分辨率低码率，常用于预览）。MiBee NVR 把子码流做成**按需拉取**的一等公民：宫格预览、级联上报、外部 AI 消费走子码流，**主码流录像完全不受影响**；没有任何观看者时子码流拉取自动停止——「无观看者零成本」。

## 谁在消费子码流

| 消费方 | 行为 |
|--------|------|
| 监控大屏（宫格） | 页头「**流畅 / 高清**」切换，默认「流畅」——多路同看时带宽与解码压力骤降 |
| 直播页（单摄像头） | 按摄像头的画质切换器（仅该相机有子码流时显示） |
| 直播 API | `stream/ws` / `stream.flv` 加 `?quality=sub`；HLS 用路径式 `/api/cameras/{id}/stream/sub/index.m3u8` |
| WebRTC (WHEP) | 子码流为 H.264 时建立真子流会话；H.265 子码流自动回退主码流（WebRTC 仅支持 H.264） |
| GB28181 级联上报 | 相机级「级联上报子码流」开关——上级平台预览不再吃满上行带宽（见 [GB/T 28181 指南](gb28181-guide.md)） |
| 外部 AI 推送 | 子流分析层——低分辨率段解码成本为主流的 1/4~1/16（见[配置参考](configuration.md#vision-推送集成配置)） |

**回退语义**：相机没有子码流、或拉取失败时，一切消费自动回退主码流，直播响应头带 `X-Stream-Quality: main|sub` 如实回报实际使用的流——前端不会黑屏，脚本可判定。

## 相机侧配置

摄像头编辑表单的「**子码流**」折叠区块：

| 字段 | 说明 |
|------|------|
| 子码流 RTSP 地址 | 手填任意相机的 `rtsp://…` 子流地址（海康惯例 `…/Streaming/Channels/102`，大华 `…/cam/realmonitor?channel=1&subtype=1`） |
| ONVIF 子流 Profile Token | **留空 = 自动发现**：ONVIF 相机连接后自动取主 profile 之外分辨率次高者（填一次；清空保存可重新发现） |

两类目标任填其一即可。GB28181 相机另有厂商惯例子通道探测，详见 [GB/T 28181 指南](gb28181-guide.md)。

> **ONVIF profile 元数据可能撒谎**（标 H.264 实际 H.265）——NVR 以 RTSP DESCRIBE 实探为准，无需人工核对。

## 服务端配置

```yaml
server:
  substream:
    idle_timeout_s: 30   # 无消费者后保持拉取多久再回收（默认 30s）
    ready_timeout_s: 8   # quality=sub 请求等待首帧就绪的超时（默认 8s）
```

## RTSP 输出（第三方平台取流）

NVR 内置 RTSP 输出服务端，每路相机一个固定取流地址，可直接填进群晖 Surveillance Station 等第三方平台：

```text
rtsp://<NVR-IP>:8554/<camera_id>
```

- H.264 / H.265 原生输出（无转码），多客户端并发各自独立
- 凭据可选，默认局域网开放；`server.rtsp {enabled, port, username, password}` 配置
- Docker 部署需发布端口（`-p 8554:8554`）；端口被占仅记录错误，不影响主服务

## 观测与判定

- `GET /api/cameras/{id}/protocols` 的 `sub_stream` 条目回报子码流可用性、来源与原因（`available / source / reason`）
- 仪表盘「链路」树里子码流是独立分支（虚线），带差分 fps/kbps 与消费者类型；无消费者自动消失
- 日志关键词：`sub-stream live`（拉起）、`recycled reason=idle`（空闲回收）、`WHEP: sub-stream not servable over WebRTC`（H.265 子流回退主流，预期行为）

## 常见问题

| 问题 | 说明 |
|------|------|
| 宫格「流畅」没变化？ | 该相机可能没有子码流——回退主码流是静默的，查 `/protocols` 的 `sub_stream` |
| 子码流画面延迟高/卡顿 | 子码流由 NVR 单独 RTSP 拉取，受相机子流编码影响；确认相机子流帧率不低于 15fps |
| WHEP 为什么总是主码流？ | WebRTC 仅支持 H.264——H.265 子码流的相机必然回退主码流（日志可见） |
| 录像用的是哪路流？ | 录像永远使用**主码流**；子码流只服务于预览/级联/推送消费方 |

## 下一步

- [播放协议选择](streaming-protocol-selection.md) — WebRTC / WS / FLV / HLS / WASM 怎么选
- [GB/T 28181 指南](gb28181-guide.md) — 级联上报子码流
- [配置参考](configuration.md) — 全部配置键
