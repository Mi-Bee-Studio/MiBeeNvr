# 接入 Home Assistant

> 适用于 MiBee NVR v0.12.0+（内置 RTSP 输出、MQTT 触发与状态发布）

MiBee NVR 没有官方 Home Assistant 集成，但通过 RTSP 输出、REST API、MQTT 触发与状态发布四个能力，可以拼出完整的接入方案。本文按用途拆分路径，全部可独立启用。

## 方案总览

| 用途 | 路径 | 依赖 |
|------|------|------|
| 看画面（H.264/H.265 相机） | RTSP 输出 + Generic Camera | 无（HA 原生） |
| 看画面（MJPEG/JPEG 相机） | `stream.mjpeg` + MJPEG Camera | 无（HA 原生） |
| 触发联动（HA → NVR） | MQTT 触发 | MQTT 代理 |
| 状态回传（NVR → HA） | MQTT 状态发布（推荐）或 REST 轮询 | 无 / MQTT 代理 |
| 低延迟看画面 | HACS WebRTC 卡片 | HACS |

最省事的组合是「RTSP 看画面 + MQTT 触发 + MQTT 状态回传」，全程只用 HA 原生集成。

## 前置准备

- NVR 的 REST 接口使用 Web 登录凭据做 Basic Auth（`/api/health`、`/api/events`、`/api/metrics` 除外，它们是公开的）。
- 摄像头 ID 用 NVR 中配置的 `id`（kebab-case，如 `front-door`）。
- 密码不要硬编码进版本库管理的 YAML，使用 HA 的 `secrets`。

## 方案 A：RTSP 输出 + Generic Camera（H.264/H.265 相机）

NVR 内置 RTSP 输出服务器（默认开启），每个摄像头一个拉流地址：

```text
rtsp://<NVR-IP>:8554/<camera_id>
```

- 仅 H.264/H.265 原生分发，不转码、只有视频（无音频）；**MJPEG/JPEG 相机不供流**，请改用方案 A'。
- 默认无鉴权（局域网开放）；可在配置中启用用户名密码：

```yaml
# mibee-nvr.yaml
server:
  rtsp:
    enabled: true
    port: 8554
    username: ""     # 可选；设置后地址写 rtsp://user:pass@<NVR-IP>:8554/<camera_id>
    password: ""
```

Docker 部署需要发布 8554 端口（官方 compose 文件已包含）。

在 HA 的 `configuration.yaml` 中为每个摄像头加一段 Generic Camera：

```yaml
camera:
  - platform: generic
    name: "前门"
    still_image_url: "http://192.168.63.30:9090/api/cameras/front-door/snapshot"
    stream_source: "rtsp://192.168.63.30:8554/front-door"
    username: admin
    password: "!secret nvr_password"
```

关于 `still_image_url`：

- `GET /api/cameras/{id}/latest-frame` 仅对 JPEG 系相机（HTTP-JPEG/MJPEG）有效，H.264/H.265 相机返回 404。
- H.264/H.265 相机可用 `GET /api/cameras/{id}/snapshot`（转发相机自身的快照 URL，仅 ONVIF 快照能力的设备可用），或直接填相机厂商的快照地址。

## 方案 A'：MJPEG/JPEG 相机（如 ESP32 摄像头）

MJPEG/JPEG 相机不走 RTSP 输出，改用 HA 原生的 MJPEG Camera 直接拉 NVR 的 MJPEG 流：

```yaml
camera:
  - platform: mjpeg
    name: "院子 ESP32"
    mjpeg_url: "http://192.168.63.30:9090/api/cameras/yard-esp32/stream.mjpeg"
    username: admin
    password: "!secret nvr_password"
    verify_ssl: false
```

静态缩略图可再配 `still_image_url: "http://.../api/cameras/{id}/latest-frame"`（JPEG 系相机可用，带 ETag 304）。

## 方案 B：MQTT 触发联动（HA → NVR）

运动传感器触发时让 NVR 立即录制。先在 NVR 侧配置 MQTT（详见 [MQTT 集成](./mqtt-integration.md)）：

```yaml
# mibee-nvr.yaml
mqtt:
  enabled: true
  broker: "tcp://192.168.63.1:1883"
  client_id: "mibee-nvr"
  topic: "mibee"
  username: "mqtt_user"
  password: "mqtt_password"
```

NVR 订阅 `mibee/trigger/{camera_id}`，支持 `record`（开始录制）与 `stop`（停止录制）；`snapshot` 尚未实现。HA 自动化示例：

```yaml
automation:
  - alias: "检测到运动时触发前门录像"
    trigger:
      - platform: state
        entity_id: binary_sensor.front_door_motion
        to: "on"
    action:
      - service: mqtt.publish
        data:
          topic: "mibee/trigger/front-door"
          payload: '{"action": "record"}'
```

## 方案 C：状态回传（NVR → HA）

### C1：MQTT 状态发布（推荐）

两条发布通道（详见 [MQTT 集成 — 状态发布](./mqtt-integration.md#状态发布)）：

- `{prefix}/health/{camera_id}`：健康告警（断连/冻结/恢复）。需 `mqtt.enabled: true` + `health.alerts.mqtt: true`。
- `{prefix}/event/{topic}`：录像与摄像头事件（`segment.completed`、`camera.added`、`camera.quality`、`storage.health.changed`）。需 `mqtt.enabled: true` + `mqtt.status_events: true`。

HA 侧示例——每次录像段完成时更新传感器：

```yaml
mqtt:
  sensor:
    - name: "前门 最近录像"
      state_topic: "mibee/event/segment.completed"
      value_template: "{{ value_json.camera_id }}"
      json_attributes_topic: "mibee/event/segment.completed"
```

> 注意：`segment.completed` 是全 NVR 一个主题（按 `camera_id` 字段区分相机）。按相机过滤可在自动化中用条件模板判断 `trigger.payload_json.camera_id`。

### C2：REST 轮询（无需 MQTT）

低频数值用 HA 的 RESTful 集成轮询 NVR 接口。单摄像头统计端点返回 `recording_count` 与 `total_size`：

```yaml
restful:
  - resource: "http://192.168.63.30:9090/api/cameras/front-door/stats"
    username: admin
    password: "!secret nvr_password"
    scan_interval: 60
    sensor:
      - name: "前门录像条数"
        value_template: "{{ value_json.recording_count }}"
      - name: "前门录像占用"
        value_template: "{{ value_json.total_size }}"
```

`GET /api/health`（公开）可轮询整体状态与 `device_id`。

### C3：SSE 事件流（需要中间件）

NVR 提供 SSE 事件流：`GET /api/events`（全 NVR，公开、限流 60 次/分钟）与 `GET /api/cameras/{id}/events`（单相机，需鉴权）。HA 原生不消费 SSE，需要 Node-RED 或自写脚本中转成 MQTT——如果已经启用 C1 的 MQTT 状态发布，通常无需此方案。

## 方案 D：低延迟画面（HACS WebRTC 卡片）

Generic Camera 走 RTSP 有 1–3 秒延迟。需要亚秒级时可装社区卡片（[AlexxIT/WebRTC](https://github.com/AlexxIT/WebRTC)，经 HACS 安装）：

```yaml
type: custom:webrtc-camera
url: rtsp://192.168.63.30:8554/front-door
muted: true
```

该卡片内置 go2rtc 做 RTSP→WebRTC 转换（不直接消费 NVR 的 WHEP 端点），低延迟并支持声音。同样仅适用于 H.264/H.265 相机。

## 已知限制与安全注意事项

- RTSP 输出默认无鉴权，局域网内任何设备都能拉流。要么配置 `server.rtsp.username/password`，要么确保 NVR 处于可信网络。
- MQTT 触发的 `snapshot` 动作尚未实现（仅记录日志）。
- `latest-frame` 仅 JPEG 系相机可用；H.264/H.265 相机的静态图用 `snapshot` 端点或相机自身快照 URL 替代。
- RTSP 输出与 WebRTC 卡片方案均不覆盖 MJPEG/JPEG 相机（用方案 A'）。
- NVR → HA 没有应用级推送通知闭环，告警通知需在 HA 里自行搭自动化（可订阅健康主题触发）。

## 下一步

- [MQTT 集成](./mqtt-integration.md) — 触发动作与状态发布的完整参考
- [配置参考](./configuration.md) — `server.rtsp`、`mqtt`、`health.alerts` 等全部字段
- [API 概览](./api.md) — REST 接口与鉴权说明
