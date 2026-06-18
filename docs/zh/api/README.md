# MiBee NVR API 参考

本文档是 MiBee NVR 的 REST API 参考。所有 API 端点均包含其请求格式、响应模式及 `curl` 命令示例。

大多数端点需要通过 HTTP Basic Auth 进行身份验证。详见[身份验证](authentication.md)。

## API 章节

| 章节 | 文件 | 说明 |
|---------|------|-------------|
| 身份验证 | [authentication.md](authentication.md) | 登录、设置、能力查询、Basic Auth 使用 |
| 健康与系统 | [system.md](system.md) | 健康检查、就绪检查、系统统计 |
| 健康监控 | [health-monitoring.md](health-monitoring.md) | 摄像头健康状态、稳定性评分、健康事件 |
| 摄像头 | [cameras.md](cameras.md) | 摄像头增删改查、连接测试、启动/停止、快照 |
| 流媒体 | [streaming.md](streaming.md) | HLS、WebRTC、HTTP-FLV、WebSocket 流媒体、摄像头协议 |
| 摄像头统计与事件 | [camera-details.md](camera-details.md) | 录制统计、摄像头事件流（SSE） |
| ONVIF | [onvif.md](onvif.md) | PTZ 控制、预置位、成像、网络、用户、发现 |
| 录制 | [recordings.md](recordings.md) | 列出、获取、删除、下载录制文件、延时摄影帧 |
| 归档 | [archives.md](archives.md) | 归档组、保留管理 |
| 设置 | [settings.md](settings.md) | 系统统计、设置增删改查、合并/流媒体/转码设置 |
| 小米 | [xiaomi.md](xiaomi.md) | 云认证、验证码、设备管理 |
| 合并与延时配置 | [merge.md](merge.md) | 合并状态、待处理计数、摄像头合并/延时配置 |
| 转码 | [transcoding.md](transcoding.md) | FFmpeg 管理、转码任务、回填、摄像头配置 |
| 转发 | [relay-guide.md](../relay-guide.md) | 推流转发预置方案、摄像头转发状态 |
| AI 检测 | [ai-detection.md](ai-detection.md) | AI 配置、ROI 区域、模型服务、AI 事件 API（浏览器端推理） |
| 事件 | [events.md](events.md) | 事件流（SSE）、摄像头事件、遥测 |
| 延时摄影与协议 | [timelapse-protocols.md](timelapse-protocols.md) | 延时摄影状态、支持的协议、功能标志 |
| 备份 | [backup.md](backup.md) | 创建/列出数据库备份 |
| 错误响应 | [errors.md](errors.md) | 错误码、HTTP 状态码、常见示例 |
| Prometheus 指标 | [../metrics.md](../metrics.md) | 所有 Prometheus 指标定义、类型、标签及使用方法 |

## 快速开始

### 基础身份验证测试

```bash
# 测试健康端点（无需认证）
curl http://localhost:9090/api/health

# 测试身份验证
curl -u admin:password http://localhost:9090/api/cameras
```

### 常见操作

```bash
# 列出所有录制文件
curl -u admin:password "http://localhost:9090/api/recordings"

# 添加新摄像头
curl -u admin:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Living Room Cam",
    "protocol": "rtsp",
    "encoding": "h264",
    "url": "rtsp://192.168.1.50:554/stream",
    "enabled": true
  }' \
  "http://localhost:9090/api/cameras"

# 下载录制文件
curl -u admin:password \
  -o recording.mp4 \
  "http://localhost:9090/api/recordings/1704123456789012345/download"

# 更新设置，清理超过 7 天的录制文件
curl -u admin:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "cleanup": {
      "retention_days": 7
    }
  }' \
  "http://localhost:9090/api/settings"

# 测试摄像头连接
curl -u admin:password \
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

### HLS 流媒体示例

```bash
# 获取 HLS 播放列表
curl -u admin:password \
  "http://localhost:9090/api/cameras/living-room/stream/stream.m3u8"

# 获取 HLS 切片
curl -u admin:password \
  "http://localhost:9090/api/cameras/living-room/stream/segment_001.ts"
```

### 小米摄像头设置

```bash
# 认证小米云
curl -u admin:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "username": "xiaomi@example.com",
    "password": "password123"
  }' \
  "http://localhost:9090/api/xiaomi/auth"

# 列出小米设备
curl -u admin:password \
  "http://localhost:9090/api/xiaomi/devices"
```

## 错误响应

所有错误响应遵循以下格式：

```json
{
  "error": "Error message",
  "code": "ERROR_CODE"
}
```

完整错误码参考和 HTTP 状态码请参见[错误响应](errors.md)。
