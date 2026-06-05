# 归档 API

## 查询归档列表

**端点：** `GET /api/archives`

按摄像头列出所有归档分组。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/archives"
```

**响应：**
```json
{
  "archives": [
    {
      "camera_id": "front-door",
      "retention_days": 30,
      "recordings_count": 150,
      "total_size_mb": 1024
    }
  ]
}
```

## 查询归档录制列表

**端点：** `GET /api/archives/{cameraID}/recordings`

列出指定归档中的录制。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/archives/front-door/recordings"
```

**响应：**
```json
{
  "recordings": [
    {
      "id": "1704123456789012345",
      "started_at": "2024-01-01T12:34:56.789Z",
      "duration": 10.0,
      "file_size": 1048576
    }
  ],
  "total": 1
}
```

## 删除归档分组

**端点：** `DELETE /api/archives/{cameraID}`

删除指定摄像头的整个归档分组。

**请求：**
```bash
curl -u username:password \
  -X DELETE \
  "http://localhost:9090/api/archives/front-door"
```

**响应：**
```json
{
  "status": "deleted"
}
```

## 删除归档录制

**端点：** `DELETE /api/archives/{cameraID}/recordings/{recordingID}`

从归档中删除指定的录制。

**请求：**
```bash
curl -u username:password \
  -X DELETE \
  "http://localhost:9090/api/archives/front-door/recordings/1704123456789012345"
```

**响应：**
```json
{
  "status": "deleted"
}
```

## 设置归档保留期

**端点：** `PUT /api/archives/{cameraID}/retention`

设置归档分组的保留期限。

**请求体：**
```json
{
  "retention_days": 60
}
```

**请求：**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "retention_days": 60
  }' \
  "http://localhost:9090/api/archives/front-door/retention"
```

**响应：**
```json
{
  "status": "updated"
}
```
