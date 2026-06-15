# 后续设计 — 转码委托 / 实时流 / 视频回写

> **状态**：构想文档，0.8.0 不实施，为后续版本提供设计参考。
> 功能域 5（转码委托）、7（实时流转发）、8（视频回写）。

---

## 功能域 5 — 转码委托

### 场景
RPi 3B 软解转码 H.265→H.264 极慢（~2fps）。MiBeeVision 部署在有 GPU 的服务器上，可硬件转码（NVENC/VAAPI）。

### 委托流程
```
1. NVR 录像完成 → segment.completed 事件
2. NVR 判断需要转码（per-camera config）
3. NVR 检查 MiBeeVision 可用性 (GET /api/vision/health)
4. 可用 → POST /api/vision/transcode
   {
     "recording_id": "abc123",
     "input_url": "/api/recordings/abc123",   -- NVR 下载 URL
     "target_codec": "h264",
     "crf": 23,
     "callback_url": "/api/recordings/abc123/ai-status"  -- 完成后通知
   }
5. MiBeeVision 下载 → GPU 转码 → 回写（PUT /api/recordings/abc123/file）
6. MiBeeVision PATCH ai-status: done
```

### Fallback 策略
- MiBeeVision 不可用 → 本地 FFmpeg（现有逻辑）
- 委托超时（5 分钟）→ 本地重试
- NVR 配置项：`transcoding.delegate_to_vision: true/false`

### 关键决策
- 转码队列归属：NVR 管理 vs MiBeeVision 管理？
- 建议双队列：NVR 发送 → MiBeeVision 接收并排队 → 完成回写

---

## 功能域 7 — 实时流转发

### 场景
MiBeeVision 需要实时分析（如区域入侵实时告警），不能等录像完成后处理。

### 方案：RTSP 转推
```
NVR recorder → StreamHub → RTSP 转推 → MiBeeVision RTSP 拉流
                                        → 实时 AI 推理
                                        → POST /api/ai/events (recording_id=null)
```

### NVR 侧实现
- StreamHub 新增 RTSP tap consumer（类似 HLS/FLV consumer）
- `POST /api/vision/stream/tap`：NVR 通知 MiBeeVision "开始拉流 camera_id=X"
- 转推地址：`rtsp://{nvr_host}:8554/{camera_id}`（RTSP server 已有基础）

### RTMP 转推（备选）
- NVR 通过 RTMP push client 把流推给 MiBeeVision
- 适合 MiBeeVision 在公网/NAT 后的场景

### 关键决策
- 实时分析 vs 后处理的优先级？
- 多路实时分析的带宽开销？（RPi 3B 100Mbps 网口上限）

---

## 功能域 8 — 视频回写

### 三种回写方式

#### 8a. 共享存储（同服务器，零拷贝）
```
MiBeeVision → 写 /data/ai-output/{camera_id}/{recording_id}_analyzed.mp4
            → POST /api/recordings (file_path=ai-output/...)
NVR DB 记录 → Web UI 展示
```
- 最快，无网络开销
- 要求文件系统可见（同服务器或 NFS/SMB 挂载）

#### 8b. API 上传（跨服务器，通用）
```
MiBeeVision → PUT /api/recordings/{id}/file (multipart/octet-stream)
            → NVR 写入 storage root → 更新 DB
```
- 通用，适合所有部署形态
- 大文件占带宽

#### 8c. RTSP/RTMP 转推（实时结果流）
```
MiBeeVision → RTSP/RTMP push → NVR ingest 端口
            → NVR 录制为新录像 (type=ai_analysis)
            → Web UI 实时播放
```
- 适合实时分析结果（如画了 bbox 的实时流）
- NVR 的 RTMP ingest server 已有基础（`internal/rtmp/`）

### 选择策略（由部署形态 + 场景决定）
| 场景 | 推荐方式 |
|------|---------|
| 同服务器 + 后处理 | 8a 共享存储 |
| 跨服务器 + 后处理 | 8b API 上传 |
| 实时分析结果流 | 8c RTSP/RTMP |
| 快照/缩略图 | 8a 或 8b（小文件） |

### 录像类型标识
```sql
ALTER TABLE recordings ADD COLUMN type TEXT DEFAULT 'normal';
-- normal=常规录像, ai_analysis=AI分析产出, transcoded=转码产出, timelapse=延时
```
Web UI 按类型筛选展示。
