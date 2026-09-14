# MiBee NVR × Home Assistant

[English](./README.md)

[MiBee NVR](https://github.com/Mi-Bee-Studio/MiBeeNvr)（轻量自托管网络录像机）随仓附带的
[Home Assistant](https://www.home-assistant.io/) 自定义集成。通过 mDNS 自动发现局域网内的
NVR，为每台相机创建摄像头实体、连接/录制传感器和录制开关——无需编写任何 YAML。

## 功能

- **零配置发现** — 自动识别 NVR 的 `_mibee-nvr._tcp` mDNS 广播，只需输入 Web 登录凭据。
- **摄像头实体**
  - H.264/H.265 相机 → 走 NVR 内置 RTSP 输出直播（Home Assistant `stream`，1–3 秒延迟）。
  - MJPEG/JPEG 相机（如 ESP32 板子）→ 走 NVR 的 `stream.mjpeg` 直通，HA 原生 MJPEG 摄像头。
- **每相机两个二进制传感器** — *Connected*（连接状态，connectivity 设备类）与
  *Recording*（正在写入录像段）。
- **每相机一个开关** — 切换相机的 `recording_enabled` 录制开关。
- 设备注册表分组：一台 NVR 主设备 + 每相机一个子设备。

## 安装

手动复制（集成位于 NVR 主仓的子目录，HACS 无法从主仓直接安装）：

```bash
git clone https://github.com/Mi-Bee-Studio/MiBeeNvr.git /tmp/mibee-nvr
mkdir -p <HA配置目录>/custom_components
cp -r /tmp/mibee-nvr/deploy/home-assistant/custom_components/mibee_nvr \
      <HA配置目录>/custom_components/
```

复制后重启 Home Assistant。更新时在克隆目录 `git pull` 后重新复制即可。

## 配置

设置 → 设备与服务 → 添加集成 → **MiBee NVR**。

- 局域网内的 NVR 会被自动发现；也支持手动填写主机/端口。
- 凭据即 NVR 的 **Web UI** 登录账号（BasicAuth）。
- 集成选项：RTSP 输出端口（默认 `8554`）与轮询间隔（默认 `30` 秒）。

## NVR 侧要求

- MiBee NVR **v0.12.0+**，H.264/H.265 直播需启用内置 RTSP 输出（默认开启）。
- JPEG 系相机的 `latest-frame` 缩略图开箱即用；H.264/H.265 相机若需缩略图，
  还需在 NVR 主机上安装可选的 FFmpeg（见
  [NVR Home Assistant 文档](../../docs/zh/home-assistant.md)）。

## 替代方案

NVR 也支持完全无自定义代码的接入方式——RTSP 通用摄像头、MQTT 触发与 MQTT 状态发布均有
官方文档（[NVR 手册](../../docs/zh/home-assistant.md)）。
本集成适合想要自动发现、按相机生成实体、免维护 YAML 的用户。

## 开发

```bash
cd deploy/home-assistant
python3 -m venv .venv && . .venv/bin/activate
pip install -r requirements_test.txt
ruff check custom_components tests
ruff format --check custom_components tests
pytest -v
```

测试套件固定 `homeassistant==2026.6.0` 并包含导入校验测试——组件面对的是真实
Home Assistant，不只是桩。CI 的 `home-assistant` job 运行相同步骤。

## 许可

属于 MiBeeNvr 仓库的一部分 — AGPL-3.0-only，见
[仓库许可](../../LICENSE)。
