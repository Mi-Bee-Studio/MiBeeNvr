# 文档目录

> 每个页面都有中英双语版本（`docs/zh/` 与 `docs/en/`，文件名一致）。刚接触 MiBee NVR？按 ★ 标注的顺序读即可。

## 入门

| 页面 | 内容 |
|---|---|
| ★ [产品介绍](intro.md) | MiBee NVR 是什么，一页讲清 |
| ★ [功能总览](features.md) | 全部能力一页地图，每节链接到专门指南 |
| ★ [快速上手](getting-started.md) | 安装、首次配置、添加第一台相机（含入门级排障小节） |
| [初始化向导](wizard.md) | 首启向导：账号、语言、存储一步配置 |
| [Web 界面导览](web-ui.md) | 仪表盘、实时视图、录像页 |
| [场景手册](scenarios.md) | 六条「从零到能用」的路径（NAS、树莓派、远程访问…） |
| [同类产品对比说明](comparison.md) | 与 MediaMTX / Frigate / Shinobi 的定位差异 |
| [桌面版](desktop.md) | Windows / macOS 桌面安装器与信任模型 |

## 安装部署

| 页面 | 内容 |
|---|---|
| [部署总览](deployment.md) | systemd、反向代理、交叉编译，及全部平台指南入口 |
| [Linux 软件包](install-linux.md) | deb / rpm 原生包安装、升级、卸载 |
| [Docker 安装](install-docker.md) | Compose 配置、卷挂载、升级 |
| [NAS 安装](install-nas.md) | 按机型选择 NAS 安装路径 |
| [unRAID](deployment-unraid.md) · [fnOS](deployment-fnos.md) · [iStoreOS](deployment-istoreos.md) · [群晖](deployment-synology.md) · [威联通](deployment-qnap.md) · [极空间](deployment-zspace.md) | 各平台指南 |
| [Docker 自动更新](deployment-autoupdate.md) | Watchtower 自动更新与回滚 |
| [树莓派](raspberrypi.md) | Pi 专属配置与调优 |
| [MediaMTX 指南](mediamtx-guide.md) | CSI 相机（Pi Camera）经 MediaMTX 接入 |

## 摄像头接入

| 页面 | 内容 |
|---|---|
| [摄像头指南](camera-guide.md) | 相机接入、品牌兼容性、排障 |
| [ONVIF 指南](onvif-guide.md) | ONVIF 接入、云台控制、成像设置 |
| [ONVIF 自动发现](onvif-discovery.md) | 自动发现、即插即录、IP 自愈 |
| [GB/T 28181](gb28181-guide.md) | 国标 SIP 平台（海康/大华/宇视设备） |
| [小米相机](xiaomi-setup.md) | 小米 CS2 + 旧版 TUTK 云台机 |
| [SRT / RTMP 接入](srt-rtmp.md) | 跨网络推流接入 |
| [WHIP 推流](whip-ingest.md) | 浏览器 / 手机 / OBS 的 WebRTC 推流 |
| [子码流](sub-stream.md) | 低分辨率流的录制与观看 |
| [SIP 门铃 / 对讲](doorbell.md) | 标准 SIP 门口机直接呼叫 NVR |

## 录像与存储

| 页面 | 内容 |
|---|---|
| [录像与回放](recording-playback.md) | 分段、时间轴、连续播放、全天 VOD 拖动 |
| [自适应录制](adaptive-recording.md) | 平静时降为延时基线省盘 75%–98%，动静/声音/语义触发恢复全速，切换零丢帧 |
| [延时摄影](timelapse.md) | 定时延时录制与合并 |
| [音频](audio.md) | 音频采集（AAC / G.711 / Opus）、实时试听 |
| [独立录音源](audio-sources.md) | 网络拾音器等纯音频源的接入 |
| [存储管理](storage-management.md) | 多磁盘根目录、按相机存储、保留策略 |
| [存储优化](storage-optimization.md) | 磁盘调优、fsync 分级、合并策略 |
| [对象存储冷备](storage-offload.md) | S3 兼容冷备、本地驱逐、签名 URL 回放 |

## 流媒体与远程访问

| 页面 | 内容 |
|---|---|
| [协议自动选择](streaming-protocol-selection.md) | 宫格如何按相机选最优协议、运行时降级 |
| [转推指南](relay-guide.md) | 向 RTMP 平台转推出流 |
| [远程访问](remote-access.md) | Tailscale / Cloudflare Tunnel / ICE-STUN-TURN 配置 |
| [DLNA 媒体服务器](dlna.md) | 电视与网络播放器经 SSDP 发现 NVR |

## 智能与集成

| 页面 | 内容 |
|---|---|
| [AI 检测调优](ai-detection-tuning.md) | 浏览器端 YOLO 检测：灵敏度、类别、性能 |
| [MQTT 集成](mqtt-integration.md) | 智能家居事件触发录像 |
| [Webhook 集成](webhook-integration.md) | HMAC 签名触发端点 |
| [告警通知](notifications.md) | 告警 Webhook 转发（mibee-alert/1 报文与接收端要点） |
| [Home Assistant](home-assistant.md) | HA 相机/REST/传感器集成 |
| [WebDAV / FTP](webdav-ftp.md) | WebDAV 与 FTP 文件访问——对比与快速上手 |
| [WebDAV 集成](webdav-integration.md) | 内置 WebDAV 服务器详解 |
| [FTP 集成](ftp-integration.md) | 内置 FTP 服务器详解 |
| [转码](transcoding.md) | FFmpeg 转码配置 |
| [Docker 硬件转码](docker-hardware-transcoding.md) | VAAPI / NVENC / V4L2 设备直通 |

## 运维与参考

| 页面 | 内容 |
|---|---|
| [配置参考](configuration.md) | 全部配置键完整说明 |
| [配置速查](config.md) | 顶层键速查与设置修改途径 |
| [CLI 参考](cli.md) | 全部子命令与参数 |
| [API 参考](api/README.md) | REST API 逐端点文档 |
| [升级指南](upgrade-guide.md) | 各版本破坏性变更与迁移步骤 |
| [故障排查](troubleshooting.md) | 常见问题与解决方案 |
| [性能调优](performance.md) | 内存分层、GOMEMLIMIT、后台 IO 预算 |
| [性能压测报告](performance-benchmark.md) | 2026-09 自测报告：三个硬件档位的容量与资源曲线 |
| [Prometheus 指标](metrics.md) | 指标参考：类型、标签、示例 |
| [可观测性](observability.md) | 链路页、帧追踪、健康稳定性、Prometheus/Grafana、OpenTelemetry 追踪与远程日志 |

## 开发者与内部设计

| 页面 | 内容 |
|---|---|
| [架构总览](architecture.md) | 包结构与数据流 |
| [并发架构](concurrency-architecture.md) | 线程模型、同步原语及背后的决策 |
| [WASM 播放器设计](wasm-player-design.md) | 浏览器端 H.265 播放器设计 |
| [存储层研究](storage-research.md) | v0.9.0 存储重构期的历史研究文档 |
| [已知问题（深度剖析）](known-issues/ai-logit-false-positives.md) | 根因分析：[AI logit 误报](known-issues/ai-logit-false-positives.md) · [ONNX gzip trailer](known-issues/ai-onnx-gzip-trailer.md) · [H.265 多相机](known-issues/h265-multicam.md) |
