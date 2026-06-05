# 备份 API

## 创建备份

**端点：** `POST /api/backup`

创建数据库的备份。

**请求：**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/backup"
```

**响应：**
```json
{
  "status": "created",
  "file": "nvr-backup-20240101-123456.db"
}
```

## 列出备份

**端点：** `GET /api/backups`

列出可用的备份文件。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/backups"
```

**响应：**
```json
["nvr-backup-20240101-123456.db", "nvr-backup-20240102-091234.db"]
```
