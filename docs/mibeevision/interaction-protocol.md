# 交互协议总纲 — MiBeeVision ↔ MiBeeNVR

> **状态**：已实施（代码 2026-08 校准；原设计 2026-06-15）
> **读者**：MiBeeNVR 团队 (Go)、MiBeeVision 团队 (Rust)
> **基础**：REST + SSE + 流协议，API Key 认证
>
> ⚠️ **本文档以下部分已按实际代码校准**（早期设计稿与实现有出入）：SSE `data:` 行负载是整个 `Event{Topic,Data}` 嵌套 JSON（非扁平 `{event,data}`）；`file_path` 是绝对路径（非相对 storage root）；`POST /api/ai/events` 的 JSON key 是 `class_name`；`ai_status` 合法值为 `pending/processing/completed/failed`（无 `done`/`skipped`）。

---

## 一、功能域划分

| 域 | 名称 | 方向 | 0.8.0 | 说明 |
|----|------|------|-------|------|
| 1 | 事件感知 | NVR → Vision | ✅ 实施 | SSE 推送 segment.completed/deleted |
| 2 | 录像访问 | Vision → NVR | ✅ 已有 | REST 查询 + 下载录像文件 |
| 3 | AI 事件回写 | Vision → NVR | ✅ 实施 | 检测结果写入 NVR，Web 展示 |
| 4 | 录像操作 | Vision → NVR | ✅ 实施 | 增删改录像同步 NVR DB |
| 5 | 转码委托 | NVR → Vision | 📄 设计 | NVR 转码任务委托给 Vision（替代本地 FFmpeg） |
| 6 | 处理状态 | 双向 | ✅ 实施 | ai_status 管理，避免重复处理 |
| 7 | 实时流转发 | NVR → Vision | 📄 设计 | RTSP/RTMP 流交互（实时分析/结果回推） |
| 8 | 视频回写 | Vision → NVR | 📄 设计 | 处理结果视频/快照写回 NVR |

---

## 二、认证 — API Key

### 设计
- NVR 设置页生成 API Key（`mibee-nvr generate-api-key` 或 Web UI）
- API Key 独立于用户密码，可撤销，不暴露用户凭证
- MiBeeVision 配置中填入 API Key
- 所有 REST/SSE 请求携带 `Authorization: Bearer {api_key}`

### NVR 侧实现
```yaml
# mibee-nvr.yaml 新增
api_keys:
  - key: "mbv_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"  # 前缀 mbv_ 标识 MiBeeVision
    name: "MiBeeVision Production"
    created_at: "2026-06-15T12:00:00Z"
    revoked: false
```

### 中间件
- 新增 `middleware/apikey.go`：校验 `Authorization: Bearer mbv_*`，与 BasicAuth 并行
- API Key 认证的请求跳过 rate limiting（机器间通信）
- SSE 长连接用 `?api_key=mbv_*` query param（WebSocket 兼容模式）

---

## 三、功能域 1 — 事件感知（SSE）

> **状态**：SSE 基础设施已存在（`GET /api/events`），需标准化事件 data 字段。

### segment.completed（已实施）

SSE 的 `data:` 行是**整个 Event 结构的 JSON**（嵌套），不是扁平对象：

```
event: segment.completed
data: {"Topic":"segment.completed","Data":{"camera_id":"cam01","file_path":"/mnt/data/nvr/cam01/20260615/120000.mp4","format":"mp4","encoding":"h265","started_at":"2026-06-15T12:00:00Z","ended_at":"2026-06-15T12:00:30Z","file_size":1234567,"recording_id":"abc123"}}
```

`Data` 内字段（对应 `internal/event/types.go::SegmentCompleted`）：

| 字段 | 说明 |
|---|---|
| `camera_id` | 摄像头 ID |
| `file_path` | **绝对**路径（如 `/mnt/data/nvr/cam01/...`，由 `storage.Manager` 的 `filepath.Join(rootDir,...)` 构造） |
| `format` | mp4 / ... |
| `encoding` | h264 / h265 / mjpeg |
| `started_at` / `ended_at` | 时间戳（RFC3339Nano 或 DB 时间戳格式） |
| `file_size` | 字节 |
| `recording_id` | 录像 ID |

> **注意**：没有 `download_url` 字段；下载走 `GET /api/recordings/{recording_id}/download`。`file_path` 在 struct 上有"relative to storage root"的注释，但实际发布的是**绝对**路径（设计意图未落地）。

### segment.deleted（已实施）

```
event: segment.deleted
data: {"Topic":"segment.deleted","Data":{"recording_id":"abc123","camera_id":"cam01","file_path":"/mnt/data/nvr/...","reason":"retention_expired"}}
```

`reason` 取值：`retention_expired` / `manual` / `disk_threshold`。MiBeeVision 收到后取消进行中的处理任务，标记关联的 AI 事件快照为孤儿。

---

## 四、功能域 3 — AI 事件回写

### POST /api/ai/events

```json
POST /api/ai/events
Authorization: Bearer mbv_xxx
Content-Type: application/json

{
  "camera_id": "cam01",
  "recording_id": "abc123",
  "event_type": "zone_intrusion",      // zone_intrusion / line_crossing / loitering / object_detected / custom
  "severity": "warning",               // info / warning / critical
  "zone_name": "后院",                  // 可选
  "class_name": "person",              // ⚠️ JSON key 必须是 class_name（NVR handler 与 DB 列都读 class_name），勿用 label
  "confidence": 0.92,
  "frame_idx": 150,
  "frame_timestamp": "2026-06-15T12:00:15Z",  // 帧在录像中的时间点
  "bbox": [0.1, 0.2, 0.3, 0.5],        // 归一化坐标 [x1,y1,x2,y2]
  "snapshot_path": "ai-snapshots/cam01_abc123_f150.jpg",  // 相对 storage root（可选）
  "metadata": {}                       // 自定义扩展字段
}
```

> **`class_name` 字段名说明**：早期协议草案写 `label`，但 NVR 的 `handleCreateAIEvent`（`internal/api/handlers_ai.go`）和 `ai_events` 表列实际读取的 JSON key 是 `class_name`。外部 AI 后端提交事件时**必须用 `class_name`**（而非 `label`），否则该列恒为空。

**响应**：
```json
201 Created
{ "id": 42, "status": "stored" }
```

### 数据模型

```sql
CREATE TABLE ai_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  camera_id TEXT NOT NULL,
  recording_id TEXT,                   -- 关联录像（可空，实时检测时无录像）
  event_type TEXT NOT NULL,
  severity TEXT NOT NULL DEFAULT 'info',
  zone_name TEXT,
  class_name TEXT,
  confidence REAL,
  frame_idx INTEGER,
  frame_timestamp TEXT,                -- ISO8601
  bbox TEXT,                           -- JSON array "[x1,y1,x2,y2]"
  snapshot_path TEXT,                  -- 相对 storage root
  metadata TEXT,                       -- JSON
  created_at TEXT DEFAULT (datetime('now')),
  FOREIGN KEY (camera_id) REFERENCES cameras(id)
);
CREATE INDEX idx_ai_events_camera_time ON ai_events(camera_id, created_at DESC);
CREATE INDEX idx_ai_events_recording ON ai_events(recording_id);
```

### 查询接口

```
GET /api/ai/events?camera_id=&event_type=&start=&end=&asc=&limit=&offset=   # camera_id 可选
GET /api/ai/events/{id}
GET /api/ai/stats?camera_id=&period=  # camera_id 可选（缺省=全局聚合）；period: 1h/24h/7d/30d
```

> 时间过滤用 `start` / `end`（RFC3339Nano），`asc=true` 升序 —— **无 `since` 参数**。列表响应含 `events`/`total`/`limit`/`offset` 字段。`camera_id` 在两个端点均为可选（issue #213 已修复，缺省时做全局聚合）。
>
> ⚠️ 不存在 `GET /api/ai/events/{id}/snapshot` 路由（`snapshot_path` 存于 DB 但无独立 serving 端点）。

---

## 五、功能域 4 — 录像操作

> MiBeeVision 可以增删改录像，同步 NVR 的 DB，让 Web UI 保持一致。

### 接口

```
# 新增录像（MiBeeVision 处理后产生的衍生视频）
POST /api/recordings
{
  "camera_id": "cam01",
  "source_recording_id": "abc123",    -- 源录像（衍生关系）
  "file_path": "ai-output/cam01_abc123_analyzed.mp4",
  "format": "mp4",
  "encoding": "h264",
  "started_at": "...",
  "ended_at": "...",
  "duration": 30.0,
  "file_size": 1234567,
  "type": "ai_analysis",              -- 录像类型标识
  "label": "区域入侵分析"              -- 显示名称
}

# 更新录像元数据（如转码后替换原文件）
PATCH /api/recordings/{id}
{
  "file_path": "transcoded/cam01_abc123.mp4",
  "format": "mp4",
  "encoding": "h264"
}

# 删除录像（如清理中间产物）
DELETE /api/recordings/{id}?delete_file=true

# 替换录像文件（⚠️ 未实现；见 future-design.md 域 8）
# PUT /api/recordings/{id}/file
# Content-Type: multipart/form-data or application/octet-stream
```

### 权限
- API Key 认证的写操作记录 `modified_by: "mbv"` 审计字段
- 删除操作软删除（标记 `archived=true`），保留 DB 记录

---

## 六、功能域 6 — 处理状态

### recordings 表新增字段

```sql
ALTER TABLE recordings ADD COLUMN ai_status TEXT DEFAULT NULL;
-- NULL=未处理, pending=排队中, processing=处理中, completed=完成, failed=失败
-- ⚠️ 无 done / skipped；NVR handler 校验并拒绝非法值（返回 400）
ALTER TABLE recordings ADD COLUMN ai_processed_at TEXT DEFAULT NULL;
ALTER TABLE recordings ADD COLUMN ai_error TEXT DEFAULT NULL;
```

### 接口

```
# MiBeeVision 标记处理状态（body JSON key 是 ai_status）
PATCH /api/recordings/{id}/ai-status
{ "ai_status": "processing" }
PATCH /api/recordings/{id}/ai-status
{ "ai_status": "completed", "event_count": 3 }
PATCH /api/recordings/{id}/ai-status
{ "ai_status": "failed", "ai_error": "decoder initialization failed" }

# NVR Web 查询（已有 recordings API 自动返回 ai_status 字段）
GET /api/recordings?ai_status=pending
```

> 合法值：`pending` / `processing` / `completed` / `failed`（`internal/api/handlers_recording.go::handleUpdateRecordingAIStatus` 校验；其它值返回 400 `"invalid ai_status; must be one of: pending, processing, completed, failed"`）。

### 轮转保护
- Cleanup manager 删除录像时检查 `ai_status`：`processing` 状态的录像跳过删除
- 或设置保护期（如 processing 超过 30 分钟自动释放）

---

## 七、功能域 5 — 转码委托（设计文档）

> NVR 把转码任务委托给 MiBeeVision，替代本地 FFmpeg 软解。

### 委托模式
```
NVR 发现需要转码 → 检查 MiBeeVision 是否可用
  → 可用：POST /api/vision/transcode 发送任务 → MiBeeVision 处理 → 回写结果
  → 不可用：fallback 到本地 FFmpeg（现有逻辑）
```

详见 `future-design.md`。

---

## 八、功能域 7-8 — 实时流 + 视频回写（设计文档）

### 实时流转发（域 7）
- NVR 通过 RTSP 转推把实时流发给 MiBeeVision
- MiBeeVision 做实时分析，结果通过 `POST /api/ai/events`（`recording_id=null`）回写

### 视频回写（域 8）— 三种方式
1. **共享存储**：同服务器时 MiBeeVision 直接写 NVR storage root 子目录
2. **API 上传**：`PUT /api/recordings/{id}/file`（跨服务器）
3. **RTSP/RTMP 转推**：MiBeeVision 转推处理后的流到 NVR 的 RTMP ingest 端口，NVR 录制为新录像

详见 `future-design.md`。

---

## 九、SSE 事件完整清单

| event | data 关键字段 | 触发时机 |
|-------|--------------|----------|
| `segment.completed` | recording_id, camera_id, file_path(**绝对**), encoding | 录像片段录制完成 |
| `segment.deleted` | recording_id, file_path, reason | 录像被轮转/手动删除 |
| `camera.status` | camera_id, status, error | 摄像头状态变化（在线/离线/错误） |
| `ai.event.created` | event_id, camera_id, event_type | AI 事件被创建（Vision→NVR 回写后，NVR 可选广播给其他订阅者） |

> ⚠️ `data:` 行负载是整个 `Event{Topic,Data}` JSON（嵌套），字段在 `Data` 内 —— 见 §三示例。

### SSE 订阅
```
GET /api/events?filter=segment.
```
- MiBeeVision 订阅 `segment.` 前缀获取录像事件。**该端点公开、限流 60/min（无需 api_key）**。
- 心跳：每 15s 发送 `: ping`
- 断线重连：MiBeeVision 用 `Last-Event-ID` header 恢复

---

## 十、错误处理约定

| HTTP Status | 含义 | MiBeeVision 行为 |
|-------------|------|-----------------|
| 400 | 请求格式错误 | 记录日志，不重试 |
| 401 | API Key 无效/过期 | 告警，停止处理 |
| 404 | 录像不存在（可能已删除） | 跳过该录像 |
| 409 | 状态冲突（如重复处理） | 跳过 |
| 429 | 限流 | 指数退避重试 |
| 5xx | NVR 内部错误 | 重试（最多 3 次，指数退避） |

---

## 十一、版本化

- 接口路径暂不加版本前缀（`/api/ai/events` 而非 `/api/v1/ai/events`）
- 通过 `Accept` header 或 query param `?version=1` 协商
- 破坏性变更时新增 `/api/v2/` 前缀
- SSE 事件 data 结构通过 `schema_version` 字段标识
