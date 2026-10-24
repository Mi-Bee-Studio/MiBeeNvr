# Linux 原生软件包（deb / rpm）

适用：不使用 Docker 的 **Debian / Ubuntu**（amd64、arm64）与 **RHEL / Fedora / Rocky / AlmaLinux / openSUSE**（x86_64、aarch64）。其他架构（含 armv7）请走[二进制部署](deployment.md)或 [Docker](install-docker.md)。

## 安装

从 [Releases](https://github.com/Mi-Bee-Studio/MiBeeNvr/releases/latest) 下载对应架构的包（文件名含架构），本地安装：

```bash
# Debian / Ubuntu（arm64 机器用 _arm64.deb）
sudo apt install ./mibee-nvr_<版本>-1_amd64.deb

# RHEL / Fedora / Rocky / AlmaLinux（arm64 机器用 .aarch64.rpm）
sudo dnf install ./mibee-nvr-<版本>-1.x86_64.rpm
# openSUSE
sudo zypper install ./mibee-nvr-<版本>-1.x86_64.rpm
```

包内容：`/usr/bin/mibee-nvr` 二进制、`mibee-nvr.service` systemd 单元、`/etc/mibee-nvr/config.example.yaml`（仅作参考的注释版示例）。安装过程自动创建 `nvr` 系统用户并注册服务，随后启动：

```bash
sudo systemctl enable --now mibee-nvr
```

首次启动会在数据目录自动生成默认配置（存储根目录默认即数据目录），Web 界面随后可通过 `http://<主机IP>:9090` 访问。

## 数据与配置位置

| 路径 | 内容 |
|---|---|
| `/var/lib/mibee-nvr/mibee-nvr.yaml` | 配置（首启自动生成）。服务以 `ProtectSystem=strict` 运行，`/etc` 只读，故配置与数据同目录 |
| `/var/lib/mibee-nvr` | 数据目录：数据库、录像 |
| `/etc/mibee-nvr/config.example.yaml` | 注释版示例配置（conffile——升级不会覆盖你的修改） |

日志走 journald：`journalctl -u mibee-nvr -f`。

## 升级

```bash
sudo apt install ./mibee-nvr_<新版本>-1_amd64.deb    # 或 dnf / zypper 安装新 rpm
```

服务在运行则自动重启；配置与数据不受影响。

## 卸载

`apt remove` / `dnf remove` 停止并移除程序，但**永不删除** `/var/lib/mibee-nvr`（录像与配置保留，防止误删录像）。确认无需保留后手动清理：

```bash
sudo systemctl stop mibee-nvr && sudo rm -rf /var/lib/mibee-nvr
```

## 与其他安装方式的关系

- systemd 单元是[裸机部署单元](https://github.com/Mi-Bee-Studio/MiBeeNvr/blob/main/deploy/mibee-nvr.service)的包变体——仅二进制路径不同（`/usr/bin` vs `/usr/local/bin`），安全加固、崩溃熔断与内存调优注释同源。
- 包由 CI 从同一份 [nfpm 规格](https://github.com/Mi-Bee-Studio/MiBeeNvr/blob/main/deploy/packaging/nfpm.yaml)构建，SHA-256 见每个 Release 附带的 `checksums.txt`。
- 暂无 apt/yum 在线仓库——包直接从 Release 页下载；希望加仓库托管请开 issue。
