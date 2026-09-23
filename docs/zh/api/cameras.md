# 摄像头 API

## 摄像头管理

### 列出摄像头

**端点：** `GET /api/cameras`

获取所有已配置摄像头的列表。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras"
```

**响应：**
```json
[
  {
    "id": "front-door",
    "name": "Front Door",
    "protocol": "rtsp",
    "encoding": "h264",
    "url": "rtsp://192.168.1.100:554/stream",
    "enabled": true,
    "status": "recording",
    "last_seen": "2024-01-01T10:15:00Z",
    "retention_days": 30,
    "username": "admin",
    "has_password": true,
    "sub_stream_url": "",
    "snapshot_url": "",
    "sample_interval": 1,
    "hls_max_fps": 30,
    "did": "",
    "vendor": ""
  }
]
```

### 创建摄像头

**端点：** `POST /api/cameras`

添加新的摄像头配置。

**请求体：**
```json
{
  "name": "Front Door",
  "protocol": "rtsp",
  "encoding": "h264", 
  "url": "rtsp://192.168.1.100:554/stream",
  "username": "admin",
  "password": "secret",
  "enabled": true,
  "retention_days": 30,
  "recording_mode": "adaptive",
  "adaptive": {
    "calm_threshold": "60s",
    "timelapse_interval": "30s",
    "spike_factor": 5.0,
    "ambient_audio": false
  },
  "audio_trigger": {"enabled": true, "min_dbfs": -45, "pre_capture_s": 3},
  "sub_stream_url": "rtsp://192.168.1.100:554/sub_stream",
  "sub_profile_token": "",
  "snapshot_url": "http://192.168.1.100:8080/snapshot",
  "sample_interval": 1,
  "hls_max_fps": 30,
  "cascade_enabled": true,
  "cascade_sub_stream": false
}
```

> `recording_mode` / `adaptive` / `audio_trigger` 详见[自适应录制](../adaptive-recording.md)；`sub_stream_url` / `sub_profile_token` 详见[子码流](../sub-stream.md)；`cascade_*` 详见 [GB/T 28181 指南](../gb28181-guide.md)。

**请求：**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Front Door",
    "protocol": "rtsp",
    "encoding": "h264",
    "url": "rtsp://192.168.1.100:554/stream",
    "username": "admin",
    "password": "secret",
    "enabled": true
  }' \
  "http://localhost:9090/api/cameras"
```

**响应（201 Created）：**
```json
{
  "id": "front-door",
  "name": "Front Door",
  "protocol": "rtsp",
  "encoding": "h264",
  "url": "rtsp://192.168.1.100:554/stream",
  "enabled": true
}
```

### 获取摄像头

**端点：** `GET /api/cameras/{id}`

获取指定摄像头的配置。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door"
```

**响应：**
```json
{
  "id": "front-door",
  "name": "Front Door", 
  "protocol": "rtsp",
  "encoding": "h264",
  "url": "rtsp://192.168.1.100:554/stream",
  "enabled": true,
  "status": "recording",
  "last_seen": "2024-01-01T10:15:00Z"
}
```

### 更新摄像头

**端点：** `PUT /api/cameras/{id}`

更新摄像头配置。所有字段均为可选，支持部分更新。

**请求体：**
```json
{
  "name": "Updated Front Door",
  "url": "rtsp://192.168.1.100:554/new_stream",
  "enabled": false,
  "retention_days": 7
}
```

**请求：**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Updated Front Door",
    "url": "rtsp://192.168.1.100:554/new_stream",
    "enabled": false
  }' \
  "http://localhost:9090/api/cameras/front-door"
```

**响应：**
```json
{
  "id": "front-door",
  "name": "Updated Front Door",
  "protocol": "rtsp",
  "encoding": "h264",
  "url": "rtsp://192.168.1.100:554/new_stream",
  "enabled": false
}
```

### 删除摄像头

**端点：** `DELETE /api/cameras/{id}`

删除摄像头配置。

**请求：**
```bash
curl -u username:password \
  -X DELETE \
  "http://localhost:9090/api/cameras/backyard"
```

**响应：**
```json
{
  "status": "deleted"
}
```

### 测试连接

**端点：** `POST /api/cameras/test-connection`

使用提供的配置测试摄像头连接。

**请求体：**
```json
{
  "protocol": "rtsp",
  "encoding": "h264",
  "url": "rtsp://192.168.1.100:554/stream",
  "username": "admin", 
  "password": "secret"
}
```

**请求：**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "protocol": "rtsp",
    "encoding": "h264",
    "url": "rtsp://192.168.1.100:554/stream",
    "username": "admin",
    "password": "secret"
  }' \
  "http://localhost:9090/api/cameras/test-connection"
```

**响应：**
```json
{
  "success": true,
  "message": "Connection successful",
  "details": {
    "protocol": "rtsp",
    "encoding": "h264",
    "latency_ms": 45,
    "frames_received": 10
  }
}
```

### 启动摄像头

**端点：** `POST /api/cameras/{id}/start`

启动摄像头的录制。

**请求：**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/cameras/front-door/start"
```

**响应：**
```json
{
  "status": "started"
}
```

### 停止摄像头

**端点：** `POST /api/cameras/{id}/stop`

停止摄像头的录制。

**请求：**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/cameras/front-door/stop"
```

**响应：**
```json
{
  "status": "stopped"
}
```

### 自适应录制外部触发

**端点：** `POST /api/cameras/{id}/adaptive/trigger`

把一路**自适应录制**相机立即拉回全帧率（MQTT / 脚本 / AI 后端等外部事件入口）。非自适应相机返回错误。

**请求体：**
```json
{
  "source": "automation",
  "hold": "30s",
  "dbfs": -30.2
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `source` | string | 触发来源标识（自由字符串，进日志与健康统计） |
| `hold` | string | 保持全帧率的时长（0–10m，缺省用默认保持时长） |
| `dbfs` | number | 可选，触发时的响度参考（仅记录） |

**请求：**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{"source": "mqtt", "hold": "30s"}' \
  "http://localhost:9090/api/cameras/front-door/adaptive/trigger"
```

**响应（200 OK）：**
```json
{
  "status": "triggered"
}
```

> 非自适应相机返回 400（`camera does not support adaptive triggers`）。

### 按相机存储根

**端点：** `PUT /api/cameras/{id}/storage-root` / `GET /api/cameras/{id}/storage-root`

设置 / 查询该相机的录像存储根（热生效——下一段录像即写新位置）。

**请求体（PUT）：**
```json
{
  "root": "/mnt/bigdisk/recordings",
  "migrate": true,
  "delete_source": true
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `root` | string | 目标存储根（默认根或候选卷内路径；空串 = 清除覆盖、回到默认根） |
| `migrate` | bool | 同时把该相机历史录像排入后台迁移队列（默认 true） |
| `delete_source` | bool | 迁移完成并校验后删除源文件（默认 false） |

**请求：**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{"root": "/mnt/bigdisk/recordings", "migrate": true, "delete_source": true}' \
  "http://localhost:9090/api/cameras/backyard/storage-root"
```

**响应（200 OK）：**
```json
{
  "status": "updated",
  "camera_id": "backyard",
  "storage_root": "/mnt/bigdisk/recordings",
  "migration": {"camera_id": "backyard", "state": "queued", "...": "..."}
}
```

**GET 响应：**
```json
{
  "camera_id": "backyard",
  "override_root": "/mnt/bigdisk/recordings",
  "effective_root": "/mnt/bigdisk/recordings",
  "default_root": "/var/lib/mibee-nvr",
  "migration": null
}
```

> 目标盘空间不足（20% 安全余量校验）时返回 400 并拒绝切换；候选卷管理与批量迁移见[存储管理](../storage-management.md)。

## 相机分组

分组注册表允许**空分组**先于成员相机存在；每路相机的 `group_name` 字段始终是成员关系的唯一事实来源，因此重命名 / 删除分组会在一次调用中遍历全部成员相机（拖拽移动成员则走 `PUT /api/cameras/{id}`）。分组名上限 64 字符（去除首尾空白后非空），注册表上限 200 个分组。

### 列出相机分组

**端点：** `GET /api/cameras/groups`

按保存的顺序返回全部分组名（管理页拖拽排序的结果）。无分组时返回空数组。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/groups"
```

**响应：**
```json
["室内", "室外", "车库"]
```

### 创建相机分组

**端点：** `POST /api/cameras/groups`

注册一个新分组（追加到列表末尾）。同名分组已存在时不报错（幂等）。

**请求体：**
```json
{"name": "室内"}
```

**请求：**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{"name": "室内"}' \
  "http://localhost:9090/api/cameras/groups"
```

**响应（201 Created）：**
```json
{"name": "室内"}
```

> 名称为空或超过 64 字符返回 400。

### 更新分组排序

**端点：** `PUT /api/cameras/groups/order`

保存完整的命名分组顺序（管理页拖拽排序）。列表中未知的名字会被注册（仅派生、尚未持久化的分组借此固化）；未出现在列表里的分组保持原有位置。

**请求体：**
```json
{"names": ["室外", "室内", "车库"]}
```

**请求：**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{"names": ["室外", "室内", "车库"]}' \
  "http://localhost:9090/api/cameras/groups/order"
```

**响应：**
```json
{"count": 3}
```

> 含重复名称或超过 200 个分组时返回 400。

### 重命名相机分组

**端点：** `PUT /api/cameras/groups/{name}`

重命名分组并同步全部成员相机的 `group_name`。**重命名到已有分组名等于合并**：两组的成员相机都落到目标分组上——这是合法操作，不返回冲突错误。

**请求体：**
```json
{"name": "客厅"}
```

**请求：**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{"name": "客厅"}' \
  "http://localhost:9090/api/cameras/groups/室内"
```

**响应：**
```json
{"name": "客厅"}
```

> 新旧名称相同（无需重命名）返回 400。

### 删除相机分组

**端点：** `DELETE /api/cameras/groups/{name}`

删除分组注册表项，成员相机的 `group_name` 清空（回到「未分组」）。响应返回被移出分组的相机数量。

**请求：**
```bash
curl -u username:password \
  -X DELETE \
  "http://localhost:9090/api/cameras/groups/车库"
```

**响应：**
```json
{"ungrouped": 2}
```

## 摄像头快照

**端点：** `GET /api/cameras/{id}/snapshot`

从摄像头获取 JPEG 快照图像。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/cameras/front-door/snapshot" \
  -o snapshot.jpg
```

**响应：** JPEG 图像，`Content-Type: image/jpeg`，`Cache-Control: max-age=5`
