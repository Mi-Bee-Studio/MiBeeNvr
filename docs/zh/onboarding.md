# 相机零接触接入

不少相机出厂时指向厂商默认网段——海康系设备出厂即 `192.168.1.64`。NVR 与其不同网段时，相机不可达，只能人工改地址。零接触接入让 NVR 自己完成这件事：在相机的默认网段里临时借一个地址，找到设备，把它改到 NVR 所在网段，最后纳管——全程不用笔记本、不用手工改网。

它是 [ONVIF 自动发现](onvif-discovery.md) 的跨网段兄弟：发现解决「已经在局域网里」的相机，零接触接入打捞「还不在网段里」的相机。

## 适用场景

- 新相机还在出厂默认地址（`192.168.1.x`，NVR 在别的网段）。
- 存量相机恢复出厂后漂回厂商默认地址。
- 批量上线：扫一次，逐台纳管。

相机已经在 NVR 所在网段的，走 [ONVIF 自动发现](onvif-discovery.md) 即可，无需别名。

## 工作原理

1. **别名**。NVR 在主网卡上临时挂一个默认网段内的辅助地址（别名）。该网段的相机因此可以直连 NVR——否则厂商固件严格走路由，一律从网关绕。
2. **扫描**。有界扫描探测默认网段的全部主机地址（.1–.254）的 80 端口——上限 /24、并行 16、单探测 1.5s，整个 /24 最坏 ~30s 扫完，并对应答设备分类：
   - 先探海康引导面：无认证 `GET /ISAPI/System/deviceInfo`。
   - 再探通用 ONVIF 面：无认证 `GetDeviceInformation`。
3. **纳管**。对选中的候选设备改写网络配置——先走 ONVIF `SetNetworkInterfaces`，固件拒绝一切 ONVIF 写形状时自动回退海康 ISAPI 写（仅静态模式）。然后在新地址按扫描时读到的 serial 定位相机，并以 serial 作为稳定身份纳管。
4. **收尾**。别名在所有退出路径上都会摘除——成功、失败、超时一视同仁。

现款海康固件改网即生效（实测免重启；设备确需重启时结果里的 `reboot_needed` 会标出）。

## 前置条件

| 条件 | 原因 |
|---|---|
| 功能开启（`camera_onboarding.enabled: true`） | 默认关闭——流程会改写设备网络配置 |
| Linux 上有 `CAP_NET_ADMIN` | 挂/摘别名是网络管理操作（见 `deploy/mibee-nvr.service` 注释） |
| 默认网段在同一广播域 | 扫描只可达二层相邻主机；被路由隔离的网段够不着 |
| 设备 80 端口可达 | 两个探测面（ISAPI、ONVIF）都走相机 HTTP 服务 |

## 配置

```yaml
camera_onboarding:
  enabled: true
  default_subnet: "192.168.1.0/24"  # 出厂相机所在的默认网段
  alias_ip: ""                      # 默认：网段最后一个主机地址（/24 即 .254）
  target_dhcp: true                 # 纳管后把相机改到 DHCP
  target_static_ip: ""              # 或指定静态目标地址（在 NVR 所在网段）
  scan_interval_s: 0                # 0 = 仅手动扫描；>= 60 = 周期后台扫描
```

| 键 | 默认值 | 说明 |
|-----|---------|-------|
| `enabled` | `false` | 关闭时其余键全部忽略 |
| `default_subnet` | —（必填） | CIDR；更宽的网段会被钳到其 /24 基址 |
| `alias_ip` | 网段最后一个主机地址 | 必须落在 `default_subnet` 内 |
| `target_dhcp` | 未设 `target_static_ip` 时为 `true` | 三态；纳管请求可逐台覆盖 |
| `target_static_ip` | — | 静态模式下必须是合法 IPv4 |
| `scan_interval_s` | `0` | `0` = 仅手动；低于 60 的值启动校验直接拒绝（会锤局域网） |

## 操作走查

以下请求假设 API 在 `localhost:9090`，HTTP Basic 认证。功能关闭时所有 onboarding 端点一律 `409 Conflict`。

### 1. 探测功能状态

```bash
curl -u admin:password http://localhost:9090/api/onboarding/status
```

开启后，status 返回配置的网段、别名当前是否挂载、上次扫描结果。

### 2. 扫描默认网段

```bash
curl -u admin:password -X POST http://localhost:9090/api/onboarding/scan
# {"job_id":"onb-..."}

curl -u admin:password http://localhost:9090/api/onboarding/jobs/onb-...
```

轮询任务到 `state` 为 `done`，分类结果在 `candidates`。

### 3. 读候选设备

```json
{
  "ip": "192.168.1.64",
  "state": "auth_required",
  "serial": "DS-2CD2225Y-YN...",
  "manufacturer": "Hikvision",
  "model": "DS-2CD2225Y-YN",
  "device_name": "IP Camera"
}
```

| 状态 | 含义 | 能做什么 |
|-------|---------|-----------------|
| `unactivated` | 出厂未激活 | 仅报告——先激活（相机 Web UI 或厂商工具）；激活协议的挑战-应答变体暂未支持 |
| `activated` | 已激活，设备信息可无凭据读取 | 带凭据纳管 |
| `auth_required` | 已激活，设备信息也要求凭据 | 带凭据纳管 |

### 4. 纳管

```bash
curl -u admin:password -X POST http://localhost:9090/api/onboarding/adopt \
  -H "Content-Type: application/json" \
  -d '{"ip":"192.168.1.64","username":"admin","password":"你的密码","mode":"static","static_ip":"192.168.10.84"}'
```

任务串起整条链——改网、重新定位、纳管——并回报：

```json
{
  "serial": "DS-2CD2225Y-YN...",
  "new_ip": "192.168.10.84",
  "camera_id": "cam-ab12cd34",
  "reboot_needed": false
}
```

`mode`、`static_ip`、`prefix_len` 是逐台覆盖项；不传则跟随配置目标（默认 DHCP）。

- **静态模式**（推荐）：NVR 轮询目标地址，直到预期 serial 的相机应答（约 60s 预算）。
- **DHCP 模式**：NVR 在本网段扫描找 serial（约 90s 预算，设备重启时更长）。若多台设备报同一 serial，拒绝猜测并判任务失败——改静态模式重跑。

### 5. 验证

纳管完成的相机出现在 `GET /api/cameras`，ID 由 serial 派生且稳定；录像行为与手工添加的相机完全一致。

## 运行须知

- **同一时刻一个操作**。扫描与纳管共用别名；任务运行中的并发请求一律 `409`。
- **任务是短期登记项**。已结束的任务可查询；登记表保留最近 64 条。
- **厂商实测（海康）**：
  - 部分固件对一切形状的 ONVIF `SetNetworkInterfaces` 都回 InvalidArgVal，而等价的 ISAPI 写可用——流程自动回退（静态模式）。
  - ONVIF 接口可能强制 SHA-256 HTTP Digest，且用户表独立于设备管理员账号。相机 Web UI 能用的凭据走 ONVIF 未必可用；在相机 UI 里建好/核对 ONVIF 用户。
  - 改网即生效；DHCP 重新取址一般一分钟内落定。
- **出厂未激活设备**必须先激活才能纳管（见候选状态表）。

## 排障

| 症状 | 可能原因 / 处理 |
|---------|--------------------|
| 所有端点都 `409` | 功能未开启——设 `camera_onboarding.enabled: true` 并重启 |
| 扫描无结果 | 相机不在 `default_subnet`；不同广播域（VLAN、路由隔离）；80 端口被防火墙拦 |
| 扫到了，纳管认证失败 | 凭据错误；或 ONVIF 用户不是设备管理员（相机 UI 里建 ONVIF 用户） |
| 纳管在改网一步失败 | 两个写面都被拒——ISAPI 回退只覆盖海康静态模式；其他厂商需要可用的 ONVIF 写 |
| DHCP 纳管超时 | NVR 所在网段没有 DHCP 服务；或扫描发现多台同 serial（fail-safe）——改静态模式 |
| 纳管后相机「消失」 | 其实是搬成功了——去新地址找 / 到摄像头页确认 |

## 安全说明

- 功能整体**默认关闭**，所有端点都要认证。
- 扫描只做设备信息端点的无认证读；扫描过程不写任何东西。
- 别名只在扫描/纳管运行期间存在，结束后必定摘除。
- 纳管会把设备凭据经本地 HTTP 连接发给相机本身——请保证默认网段处于可信二层。

## 另见

- [API 参考 — Onboarding](api/onboarding.md)
- [摄像头指南](camera-guide.md) — 同一流程的简版章节
- [ONVIF 自动发现](onvif-discovery.md) — 同网段即插即用纳管
