# MiBeeVision ↔ MiBeeNVR 协同文档

本目录存放 MiBeeVision（Rust AI/硬件算力后端）与 MiBeeNVR（Go 轻量 NVR）之间的协同接口设计、开发进度和需求文档。

## 文档索引

| 文档 | 内容 | 状态 |
|------|------|------|
| [interaction-protocol.md](./interaction-protocol.md) | **交互协议总纲**：功能域划分、认证方案、REST/SSE/流协议接口定义 | 设计中 |
| [0.8.0-milestone.md](./0.8.0-milestone.md) | **0.8.0 协同 milestone**：功能域 1-6 的具体实施计划 | 待评审 |
| [future-design.md](./future-design.md) | **后续设计**：实时流转发(7)、视频回写(8)、转码委托(5) 的设计文档 | 构想 |

## 架构概览

```
┌── 边缘设备 (RPi / Banana Pi) ──────┐        ┌── 算力服务器 (x86 + GPU) ─────┐
│  MiBeeNVR (Go)                     │        │  MiBeeVision (Rust)            │
│                                    │        │                                │
│  ✅ 录制 / 存储 / 流媒体分发        │ REST   │  🔴 AI 后处理推理               │
│  ✅ browser-side AI（实时轻量）     │  +SSE  │  🔴 硬件转码（GPU 编解码）      │
│  ✅ Web UI（展示所有结果）          │◀──────▶│  🔴 加解密                      │
│  ⚠️ 转码/AI 受限于低配硬件          │  +流协议│  🔴 高级视频分析                │
│                                    │        │  ✅ 兼容其他 NVR / 裸视频       │
└────────────────────────────────────┘        └────────────────────────────────┘
```

## 核心设计原则

1. **MiBeeNVR 优先兼容**：接口以 MiBeeNVR 的需求为主，但保持通用性（MiBeeVision 也支持其他视频源）
2. **低配友好**：MiBeeNVR 侧零额外依赖，所有重计算卸载到 MiBeeVision
3. **双向可见**：MiBeeVision 的增删改数据在 MiBeeNVR Web 上实时体现
4. **多部署形态**：同服务器（共享存储/localhost）和跨服务器（HTTP/RTSP）都支持
5. **渐进迁移**：MiBeeNVR 的硬件依赖功能逐步迁移到 MiBeeVision，不破坏现有功能

## 交互方式

| 类型 | 协议 | 场景 |
|------|------|------|
| 控制面 | REST + API Key | 录像操作、AI 事件回写、状态查询 |
| 事件面 | SSE（长连接） | segment 完成通知、segment 删除通知 |
| 视频面 | HTTP 下载 / 共享存储 / RTSP / RTMP | 录像文件获取、处理结果回写、实时流交互 |
