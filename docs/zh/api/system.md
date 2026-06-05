# 健康与系统 API

## 健康检查

**端点：** `GET /api/health`

获取系统整体健康状态，包括数据库和存储磁盘空间。

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
    "storage": {
      "status": "ok", 
      "message": ""
    }
  },
  "uptime": "2h34m15s"
}
```

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
