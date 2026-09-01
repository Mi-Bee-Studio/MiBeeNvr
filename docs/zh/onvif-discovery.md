# ONVIF 自动发现

> 适用于 MiBeeNvr v0.12.0

MiBee NVR 通过 ONVIF 协议自动发现局域网中的 IP 摄像头，并自动检测编码格式（H.264 / H.265）。

## 工作原理

1. **网络扫描**：NVR 向局域网广播 WS-Discovery 探测包
2. **设备响应**：支持 ONVIF 的摄像头会自动响应
3. **编码检测**：NVR 获取摄像头的媒体配置，自动检测编码格式
4. **添加设备**：选择要添加的摄像头，输入凭据即可

## 使用方式

### Web UI 发现

1. 进入「**摄像头**」页
2. 点击「**扫描设备**」，在弹出的协议菜单中选择「**ONVIF**」（也可选择「Xiaomi」扫描小米摄像头）
3. 等待扫描完成（通常 5-10 秒），已添加的摄像头会标注识别
4. 在结果列表中确认目标设备（显示型号、序列号和设备地址）
5. 在顶部凭据栏输入摄像头的 ONVIF 用户名 / 密码（用于获取流地址）
6. 点击单个设备的「**添加为摄像头**」，或点击「**全部添加（N）**」批量接入

![ONVIF 扫描面板](images/onvif-discovery.webp)

> **Docker 提示**：WS-Discovery 使用 UDP 多播，Docker 默认 bridge 网络会阻断它。容器内发现不到摄像头时，请改用 `network_mode: host`，或使用面板底部的「**手动探测**」直接输入设备 IP 探测。

### CLI 发现

```bash
# 扫描局域网中的 ONVIF 设备
./mibee-nvr-amd64 discover --protocol onvif

# 输出示例
# 发现设备：
#   - 名称：DS-2CD2043G2-I
#     地址：192.168.1.100:80
#     厂商：Hikvision
#     模型：DS-2CD2043G2-I
```

## 支持的 ONVIF 版本

| 版本 | 支持状态 | 说明 |
|------|----------|------|
| ONVIF Profile S | ✅ 完整支持 | 流媒体服务 |
| ONVIF Profile T | ✅ 完整支持 | H.265 视频 |
| ONVIF Profile G | ⚠️ 部分支持 | 录像管理 |

## 编码格式检测

ONVIF 发现会自动检测摄像头的视频编码：

- **H.264**：`encoding` 字段设为 `h264`
- **H.265**：`encoding` 字段设为 `h265`
- **MJPEG**：`encoding` 字段设为 `mjpeg`

> **重要**：从 v0.10.0 开始，MiBee NVR 不再接受组合格式（如 `rtsp_h264`）。请使用独立的 `protocol` + `encoding` 字段。

## 常见问题

### 未发现摄像头

检查以下项：

1. **网络连通性**：NVR 和摄像头在同一子网
2. **ONVIF 是否启用**：在摄像头管理界面确认 ONVIF 已启用
3. **防火墙**：允许 3702（WS-Discovery）和摄像头的 ONVIF 端口（通常 80 或 8080）
4. **多网卡**：如果 NVR 有多块网卡，确保扫描的是摄像头所在的网段

```bash
# 手动测试 WS-Discovery
curl http://192.168.1.100:80/onvif/device_service
```

### 发现但无法添加

- **凭据错误**：确认 ONVIF 用户名和密码（通常与摄像头 Web 管理界面相同）
- **协议不兼容**：某些老旧摄像头的 ONVIF 实现不标准，尝试手动添加 RTSP URL

### 扫描速度慢

- 局域网设备过多会减慢扫描速度
- 可通过配置文件限制扫描范围

```yaml
onvif:
  discovery:
    timeout: "5s"        # 单个设备超时
    network: "192.168.1.0/24"  # 限定扫描网段
```

## 手动添加 ONVIF 摄像头

如果自动发现不可用，可以手动添加：

```yaml
cameras:
  - id: "office-camera"
    name: "办公室摄像头"
    protocol: "onvif"
    encoding: "h264"
    url: "http://192.168.1.100:80/onvif/device_service"
    username: "admin"
    password: "camera123"
    enabled: true
```

## 摄像头品牌兼容性

参阅[摄像头品牌兼容指南](https://raw.githubusercontent.com/Mi-Bee-Studio/MiBeeNvr/main/docs/zh/camera-guide.md)获取海康、大华、宇视、Axis、Reolink 等 20+ 品牌的详细 ONVIF 配置。

## 下一步

- [小米摄像头接入](xiaomi-setup.md) — TUTK P2P 连接
- [SRT / RTMP 推流接入](srt-rtmp.md) — 推流摄像头配置
- [树莓派摄像头接入](raspberrypi.md) — libcamera 配置
