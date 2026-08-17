# 健康与系统 API

## 健康检查

**端点：** `GET /api/health`

获取系统整体健康状态，包括数据库和存储磁盘空间。公开端点（无需认证）。

响应中的 `device_id` 是首次启动自动生成并持久化的稳定 UUID（`server.device_id`），
`device_name` 默认取主机名 —— 局域网客户端可以用它们锚定设备身份，而非易变的 IP。

**请求：**
```bash
curl http://localhost:9090/api/health
```

**响应：**
```json
{
  "status": "ok",
  "checks": {
    "database": {
      "status": "ok",
      "message": ""
    },
    "goroutines": {
      "status": "ok",
      "message": "167 goroutines"
    },
    "storage": {
      "status": "ok",
      "message": "43% used (1272250408960 / 2953130397696 bytes)"
    }
  },
  "uptime": "2h34m15s",
  "setup_required": false,
  "device_id": "371da2dc-7804-4706-b424-ce50d14ce2d2",
  "device_name": "bananapim5",
  "cameras": {
    "total": 13,
    "recording": 10,
    "reconnecting": 0,
    "error": 3,
    "offline": 0,
    "details": [
      { "id": "front-door", "name": "前门", "status": "healthy", "score": 75 }
    ]
  }
}
```

`setup_required` 为 `true` 时表示尚未初始化（无管理员密码），应引导用户完成初始化向导。

## 就绪检查

**端点：** `GET /api/readyz`

检查系统是否已准备好接受请求（与健康检查相同）。

**请求：**
```bash
curl http://localhost:9090/api/readyz
```

**响应：**
```json
{
  "status": "ok"
}
```

## 系统统计

**端点：** `GET /api/stats/system`

获取详细的系统统计信息，包括 CPU、内存和网络使用情况。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/stats/system"
```

**响应：**
```json
{
  "cpu": {
    "total": 1234567,
    "idle": 987654
  },
  "memory": {
    "total": 1073741824,
    "available": 536870912,
    "process_rss": 10485760
  },
  "network": {
    "bytes_sent": 1048576,
    "bytes_recv": 2097152
  },
  "uptime": "2h34m15s",
  "timestamp": 1716789012
}
```
