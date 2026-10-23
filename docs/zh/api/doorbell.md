# 门铃 API

SIP 门铃/对讲端点（#971）。均需鉴权。来电类端点要求功能已在配置中开启
（`doorbell.enabled: true`，可在 设置 → 门铃 修改），否则一律返回
`501 Not Implemented`；下方状态端点始终返回 200。功能说明见
[门铃](../doorbell.md)。

## 运行状态

**端点：** `GET /api/doorbell/status`

门铃接入的配置与运行状态。`enabled` 是配置侧开关，`running` 是 SIP 服务
是否实际已构建——两者在"保存设置后、重启前"这段窗口内不一致，正是设置
面板展示的"等待重启生效"状态。

```bash
curl -u username:password http://localhost:9090/api/doorbell/status
```

**响应：**
```json
{
  "enabled": true,
  "running": true,
  "registered": [
    {
      "user": "front-door",
      "source": "192.0.2.50:5060",
      "expires_at": "2026-10-15T20:35:00+08:00"
    }
  ]
}
```

`registered` 列出当前存活的模式 A 注册（已向 NVR REGISTER 且未过期的
门站）；模式 B 经 PBX 的门站不出现在这里。

## 来电列表

**端点：** `GET /api/doorbell/calls`

最近的通话，最新在前（进行中 + 历史，内存保存，有上限）。

```bash
curl -u username:password http://localhost:9090/api/doorbell/calls
```

**响应：**
```json
{
  "calls": [
    {
      "id": "0f1e2d3c-...",
      "user": "front-door",
      "name": "前门",
      "camera_id": "cam-xxxx",
      "snapshot_path": "snapshots/cam-xxxx/20261015-203000.123.jpg",
      "state": "ringing",
      "started_at": "2026-10-15T20:30:00+08:00",
      "end_reason": ""
    }
  ]
}
```

`state` 取值 `ringing`、`answered`、`ended`。

## 接听

**端点：** `POST /api/doorbell/calls/{id}/answer`

接听振铃中的通话：门禁收到带 NVR RTP 端点的 `200 OK`，通话转为
`answered`（可挂载对讲桥）。

- `409`——通话不在振铃状态（已接听/已结束）。
- `404`——无此通话。

## 拒绝

**端点：** `POST /api/doorbell/calls/{id}/reject`

拒绝振铃中的通话（向门禁回 `603 Decline`）。

## 挂断

**端点：** `POST /api/doorbell/calls/{id}/bye`

结束已接听的通话（向门禁发 SIP `BYE`）。

## 按铃抓拍

**端点：** `GET /api/doorbell/calls/{id}/snapshot`

返回按铃瞬间抓拍的 JPEG（`image/jpeg`、`Cache-Control: no-store`）。
无抓拍时（未关联相机或抓拍失败）返回 `404`。

## 对讲 WebSocket

**端点：** `GET /api/doorbell/calls/{id}/talk`

桥接已接听通话音频的二进制 WebSocket——与
[GB28181 对讲](../gb28181-guide.md)同一套约定：

- 浏览器 → 服务端：二进制帧，每帧字节为发往门禁喇叭的裸 **G.711
  A-law**（标准形式）载荷。
- 服务端 → 浏览器：来自门禁麦克风的裸 G.711 A-law 二进制帧（按
  8 kHz 单声道解码播放）。
- 每路通话只允许一个桥；第二个客户端得到 `409`。
- 关闭 WebSocket 只解除桥，**不**结束通话（结束用 `bye` 端点）；JSON
  文本帧 `{"close": true}` 同样视为客户端主动解除。

鉴权：与其余 WebSocket 一致，无法带 Cookie 时用 `?token=` 传会话令牌。

## 事件

每次通话状态变化都会发布到事件流（[事件](events.md)，filter
`doorbell.`）：

| 主题 | 时机 |
|------|------|
| `doorbell.ring` | 来自已注册/已配置门禁的 INVITE 被接受 |
| `doorbell.answered` | 某客户端接听 |
| `doorbell.rejected` | 某客户端拒绝 |
| `doorbell.ended` | BYE / CANCEL / 振铃超时 / 挂断 / 关停（`reason` 字段） |

载荷：`{"call_id", "user", "name", "camera_id", "snapshot_path", "reason", "timestamp"}`。
开启 `mqtt.status_events` 后同样转发到 MQTT `{prefix}/event/doorbell.*`。
