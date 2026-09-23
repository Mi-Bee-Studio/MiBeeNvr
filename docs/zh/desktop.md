# 桌面版（Windows / macOS）

> 适用于 MiBeeNvr v0.13.0 · 全新平台形态，与 Docker / 裸机 Linux 版功能一致

v0.13.0 起 MiBee NVR 提供 **Windows 与 macOS 桌面版**：单文件安装器、系统托盘（Windows）/ 菜单栏助手（macOS）、开箱即用的本机免密信任模型。适合把一台常开的 Windows 小主机或 Mac mini 变成家用 NVR，无需 Docker 与命令行。

## 安装

### Windows

1. 从 [GitHub Releases](https://github.com/Mi-Bee-Studio/MiBeeNvr/releases) 下载 `MiBeeNVR-Setup-v0.13.0-windows-amd64.exe`（安装器即服务器本体，单文件）
2. 双击运行——安装、启动、自动打开浏览器进入首跑向导，一步完成
3. 安装后系统托盘出现 MiBee 图标：打开 Web 界面 / 修改密码 / 监听地址 / 退出
4. 卸载：Windows「设置 → 应用」（或控制面板）中的 MiBee NVR 条目；数据默认保留，勾选删除或 `mibee-nvr uninstall --purge` 一并清除

> Windows 版无 cmd 黑窗口（GUI 子系统），关掉浏览器不影响录像；退出 NVR 请用托盘菜单。终端输出在从命令行启动时自动重挂（AttachConsole）。

### macOS

1. 下载 `MiBeeNVR-v0.13.0-macOS-universal.dmg`（arm64 + amd64 通用）
2. 打开 DMG，把 **MiBeeNVR.app** 拖到「应用程序」
3. 首次双击运行：安装服务并打开首跑向导；菜单栏出现 MiBee 助手图标
4. 菜单栏助手提供：打开 Web 界面 / 修改密码 / 修改监听地址 / 退出
5. 卸载：终端执行 `mibee-nvr uninstall`（数据保留；`--purge` 一并清除）

> macOS 13+。若 Gatekeeper 提示未识别开发者，右键 → 打开放行一次即可。

## 信任模型（与服务器版的差异）

桌面版默认按**本机个人软件**对待：

- **只监听 `127.0.0.1`**——不对局域网暴露。需要从手机/其它电脑访问时，在托盘/菜单栏修改监听地址（如 `0.0.0.0:9090`），进程内热切换不中断录像；改开放后请立即设置密码
- **本机免密登录**（`local_bypass`）：环回访问自动放行——本机浏览器打开即用，无需输入密码；**跨站浏览器请求不享受免密**（`Sec-Fetch-Site` 检查）
- 修改密码不再需要旧密码（环回请求即授权）——忘记密码时在本机直接改
- 菜单栏/托盘的「退出」「关机」（`POST /api/system/shutdown`）与「修改监听地址」（`PUT /api/system/listen`）仅接受环回请求，远程调用一律 403

## 数据与升级

- 数据目录：`%LOCALAPPDATA%\MiBeeNVR`（Windows，含 `mibee-nvr.yaml`、录像、数据库与 `nvr.log`）/ `~/Library/Application Support/MiBeeNVR`（macOS）；程序本体在 `%LOCALAPPDATA%\Programs\MiBeeNVR`（Windows）/ `~/Applications/MiBeeNVR`（macOS）
- 卸载默认保留数据；`mibee-nvr uninstall --purge` 一并清除
- **桌面版不参与应用内自动更新**（`mibee-nvr update` 在 Windows/macOS 上拒绝执行）——升级 = 下载新版安装器重跑，配置与数据保留
- 设置页的"一键升级"在桌面版不可用；版本检查照常工作

## 常见问题

| 问题 | 答案 |
|------|------|
| 关掉浏览器 NVR 会停吗？ | 不会——服务器进程常驻；退出只能走托盘/菜单栏 |
| 能从局域网其它设备访问吗？ | 能——先在托盘/菜单栏把监听地址改为 `0.0.0.0:9090`，然后设置密码 |
| 为什么打开页面不用登录？ | 本机环回免密（local_bypass）；这是桌面版的预期行为，局域网访问仍需密码 |
| 开机自启吗？ | 是——Windows 注册当前用户登录自启 + 开始菜单快捷方式；macOS 用 LaunchAgent（仅当前用户，无需管理员权限） |
| 与 Docker 版功能有差异吗？ | 录像/直播/AI/集成能力一致；仅部署形态与信任模型不同 |

## 下一步

- [快速入门](quickstart.md) — 添加第一路相机
- [配置参考](configuration.md) — 配置文件完整说明
- [性能调优](performance.md) — 桌面硬件上的 IO / 内存调优
