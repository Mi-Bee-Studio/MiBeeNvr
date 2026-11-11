# Onboarding API（零接触接入）

相机零接触接入：发现落在出厂默认网段的相机，改网到 NVR 所在网段并纳管。概念与走查见[相机零接触接入](../onboarding.md)。

所有端点都需要认证；功能关闭时（`camera_onboarding.enabled: false`，默认值）一律回 **`409 Conflict`**。

## 状态

**端点：** `GET /api/onboarding/status`

功能状态与上次扫描结果。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/onboarding/status"
```

**响应：**
```json
{
  "enabled": true,
  "subnet": "192.168.1.0/24",
  "alias_ip": "192.168.1.254",
  "alias_active": false,
  "last_scan": "2026-11-11T08:30:00Z",
  "last_scan_found": 2
}
```

| 字段 | 说明 |
|-------|-------------|
| `enabled` | 功能是否开启 |
| `subnet` | 配置的 `default_subnet` |
| `alias_ip` | 解析后的别名地址（显式配置或网段最后一个主机地址） |
| `alias_active` | 别名当前是否挂载（扫描/纳管运行期间） |
| `last_scan` | 上次完成扫描的 RFC3339 时间戳（有过扫描才有） |
| `last_scan_found` | 该次扫描的候选设备数 |

## 扫描

**端点：** `POST /api/onboarding/scan`

后台扫描默认网段（80 端口，上限 /24，并行 16），返回任务 ID。

**请求：**
```bash
curl -u username:password -X POST \
  "http://localhost:9090/api/onboarding/scan"
```

**响应 — `202 Accepted`：**
```json
{
  "job_id": "onb-a1b2c3d4"
}
```

经[任务](#任务)端点轮询。扫描回 `409 Conflict` 且提示「同一时刻一个操作」说明有扫描/纳管仍在运行——等它结束。

## 纳管

**端点：** `POST /api/onboarding/adopt`

引导纳管：改写相机网络配置（先 ONVIF，海康静态模式走 ISAPI 回退），在新地址按 serial 重新定位，最后纳管。

**请求：**
```bash
curl -u username:password -X POST \
  -H "Content-Type: application/json" \
  -d '{"ip":"192.168.1.64","username":"admin","password":"secret","mode":"static","static_ip":"192.168.10.84"}' \
  "http://localhost:9090/api/onboarding/adopt"
```

**请求体：**

| 字段 | 类型 | 必填 | 说明 |
|-------|------|----------|-------------|
| `ip` | string | 是 | 相机当前在默认网段的地址 |
| `username` / `password` | string | 是 | 设备凭据（激活后） |
| `mode` | string | 否 | `"dhcp"` 或 `"static"`；空 = 跟随配置目标 |
| `static_ip` | string | 否 | 静态目标覆盖；静态模式下必须是合法 IPv4 |
| `prefix_len` | int | 否 | 静态目标前缀长度覆盖 |

**响应 — `202 Accepted`：**
```json
{
  "job_id": "onb-e5f6a7b8"
}
```

**错误：** `400` 缺 `ip` 或请求体非法；`409` 功能关闭或有扫描/纳管正在运行。

## 任务

**端点：** `GET /api/onboarding/jobs/{id}`

任务状态与结果。扫描任务带候选设备；纳管任务带纳管结果。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/onboarding/jobs/onb-a1b2c3d4"
```

**响应 — `200 OK`（已完成的扫描）：**
```json
{
  "id": "onb-a1b2c3d4",
  "kind": "scan",
  "state": "done",
  "started_at": "2026-11-11T08:30:00Z",
  "candidates": [
    {
      "ip": "192.168.1.64",
      "state": "auth_required",
      "serial": "DS-2CD2225Y-YN...",
      "manufacturer": "Hikvision",
      "model": "DS-2CD2225Y-YN",
      "device_name": "IP Camera"
    }
  ]
}
```

**响应 — `200 OK`（已完成的纳管）：**
```json
{
  "id": "onb-e5f6a7b8",
  "kind": "adopt",
  "state": "done",
  "started_at": "2026-11-11T08:32:10Z",
  "adopt": {
    "serial": "DS-2CD2225Y-YN...",
    "new_ip": "192.168.10.84",
    "camera_id": "cam-ab12cd34",
    "reboot_needed": false
  }
}
```

**任务模型：**

| 字段 | 说明 |
|-------|-------------|
| `kind` | `"scan"` 或 `"adopt"` |
| `state` | `running` / `done` / `error` |
| `error` | `state: "error"` 时有值 |
| `candidates` | 扫描结果，按 IP 排序；纳管任务为空 |
| `adopt` | 纳管任务的纳管结果 |

候选设备 `state` 取值：`unactivated`（出厂未激活，纳管前须先激活）、`activated`（无凭据可读）、`auth_required`（需凭据）。处理方式见指南页。

**错误：** `404` 任务 ID 不存在；登记表满 64 条后按最旧优先清理已结束任务。

## 另见

- [相机零接触接入指南](../onboarding.md)
- [摄像头 API](cameras.md) — 纳管完成后就是普通相机
- [ONVIF API](onvif.md) — 逐台 ONVIF 操作
