# 远程访问 Remote Access

MiBee NVR 默认只在局域网内提供服务。本文介绍如何让 NVR 在 **外部网络(4G / 远程 WiFi / 跨地域)** 下被访问。

> **本文只覆盖最小可用配置**。每个工具的高级特性(子网路由、ACL、exit node、自建证书等)请参考对应工具的官方文档。

---

## 场景对比

| 方案 | 适合场景 | 需要公网 IP | 复杂度 | WebRTC | 备注 |
|------|---------|------------|--------|--------|------|
| 端口转发 / UPnP | 家庭固定公网 IP | ✅ 需要 | 低 | ✅ 可用 | 最简单,但暴露面大,建议只开 TLS 端口 |
| **Tailscale** | 个人/小团队自用 | ❌ 不需要 | 极低 | ⚠️ 见下文 | 推荐:零配置 mesh VPN,UDP 打洞 |
| **Cloudflare Tunnel** | 分享给他人、无公网 IP | ❌ 不需要 | 中 | ❌ TCP only | 适合 HLS,不适合 WebRTC |
| 自建 WireGuard | 高级用户、需要完全控制 | ✅ 需要 | 高 | ✅ 可用 | 性能最好,但配置门槛高 |
| frp / ngrok | 临时调试、内网穿透 | 取决于部署 | 中 | 取决于模式 | 文档不在本文展开,请自查 |

---

## WebRTC 跨网访问(必读)

WebRTC 默认走 **UDP**,需要 ICE 服务器(STUN/TURN)来穿越 NAT。MiBee NVR 的 WebRTC(WHEP)在默认配置下只收集 mDNS host candidate,**仅适用于局域网**。

要在外网通过 WebRTC 看视频,需要在 `mibee-nvr.yaml` 中配置 ICE 服务器:

```yaml
streaming:
  webrtc:
    enabled: true
    ice_servers:
      - urls: ["stun:stun.l.google.com:19302"]            # 公共 STUN(免费)
      - urls: ["turn:turn.example.com:3478?transport=udp"] # TURN 中继(对称 NAT 必须)
        username: "user"
        credential: "pass"
```

- **STUN**:免费公共服务器即可,适合大多数 NAT 类型(锥形 NAT)。
- **TURN**:对称 NAT(symmetric NAT,运营商级 NAT 常见)必须用 TURN 中继。TURN 流量会消耗服务器带宽,建议自建 [coturn](https://github.com/coturn/coturn)。
- **TCP-only tunnel(如 Cloudflare Tunnel)走不通 WebRTC UDP**:这种情况请在播放器里手动选择 HLS 协议(播放器切换器/协议降级链会自动落到 HTTP 传输),HLS 走 HTTP/TCP,tunnel 友好。

---

## 方案 A:Tailscale(推荐:个人自用)

[Tailscale](https://tailscale.com/) 是基于 WireGuard 的零配置 mesh VPN。NVR 加入 tailnet 后,通过分配的 `100.x.x.x` IP 即可在外网访问,**不需要公网 IP,不需要端口转发**。

### Docker Compose 示例

在 NVR 的 `docker-compose.yml` 旁加一个 tailscale sidecar:

```yaml
services:
  mibee-nvr:
    image: ghcr.io/mi-bee-studio/mibeenvr:latest
    volumes:
      - ./data:/data
    network_mode: "service:tailscale"   # 关键:共享 tailscale 的网络栈

  tailscale:
    image: tailscale/tailscale:latest
    hostname: mibee-nvr                  # 在 tailnet 里的设备名
    environment:
      - TS_AUTHKEY=tskey-auth-xxxxx      # 从 https://login.tailscale.com/admin/settings/keys 获取
      - TS_STATE_DIR=/var/lib/tailscale
    volumes:
      - tailscale-state:/var/lib/tailscale
    cap_add:
      - NET_ADMIN
      - SYS_MODULE

volumes:
  tailscale-state:
```

启动后,在 Tailscale 管理后台可以看到 `mibee-nvr` 设备,通过 `http://mibee-nvr:9090`(或分配的 IP)即可在外网访问。

### 配置要点

- **WebRTC 与 Tailscale**:Tailscale 的 UDP 打洞通常能让 WebRTC 直连成功,无需额外配 STUN。但若你的 tailnet 节点都在对称 NAT 后,仍需配置 TURN。
- **Auth key 过期**:免费版 auth key 默认 90 天过期,生产环境建议用 **preauthorized** + reusable key,或在管理后台设置 key 不过期。
- **HTTPS**:浏览器 WebRTC(WHEP)需要 Secure Context。Tailscale 自带 HTTPS(MagicDNS + 证书),在管理后台启用即可用 `https://mibee-nvr.tail-xxxx.ts.net`。

---

## 方案 B:Cloudflare Tunnel(推荐:无公网 IP 分享访问)

[Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/) 通过 `cloudflared` 把本地服务反代到 Cloudflare 边缘节点,**无需公网 IP,无需开放端口,自带 HTTPS 和 DDoS 防护**。适合把 NVR 分享给非技术用户(他们只需访问一个域名)。

### Docker Compose 示例

```yaml
services:
  mibee-nvr:
    image: ghcr.io/mi-bee-studio/mibeenvr:latest
    volumes:
      - ./data:/data
    ports:
      - "127.0.0.1:9090:9090"            # 只监听本地,由 cloudflared 反代

  cloudflared:
    image: cloudflare/cloudflared:latest
    command: tunnel run
    environment:
      - TUNNEL_TOKEN=eyJhZxxxxx           # 从 Cloudflare Zero Trust 后台创建 tunnel 获取
    depends_on:
      - mibee-nvr
```

在 Cloudflare Zero Trust 后台把 tunnel 的某个子域名(如 `nvr.example.com`)指向 `http://mibee-nvr:9090`。

### ⚠️ 重要限制:WebRTC 走不通

Cloudflare Tunnel 是 **TCP-only**,无法转发 WebRTC 的 UDP 媒体流。在这种部署下:

- ❌ WebRTC(WHEP)无法工作
- ✅ HLS / LL-HLS / HTTP-FLV / WebSocket 流(走 HTTP)正常工作
- ✅ 回放、管理 API、所有非 WebRTC 功能正常

**建议**:Cloudflare Tunnel 部署时在摄像头播放器上手动选择 HLS(协议切换器默认 Auto,
WebRTC 不可用时会自动降级到 HTTP 传输)。`streaming.default_protocol` 配置项在 0.11.0
已移除,遗留的旧值会被静默忽略。

### 配置要点

- **认证**:Cloudflare 后台可给 tunnel 加 Access Policy(零信任登录),比 BasicAuth 更安全。
- **带宽**:Cloudflare 免费版对带宽有限制,长时间多路视频流可能触发 ToS,生产环境建议评估付费计划。

---

## 其他方案

下列方案不在本文展开,请参考官方文档:

- **WireGuard / OpenVPN**:自建 VPN,需要公网 IP 或 VPS,性能最好但配置门槛高。
- **frp / ngrok**:内网穿透,适合临时调试。frp 需要自建 frps 服务器,ngrok 免费版有限制。
- **ZeroTier**:类似 Tailscale 的 mesh VPN,可自建 controller。

---

## iOS / AVPlayer 的 HLS 播放

携带会话令牌请求 HLS 播放列表时,NVR 会同时下发一个作用域受限的 `mbs_session`
cookie,供无法设置逐请求头的播放器(iOS AVPlayer、部分原生播放器)拉取媒体分片。
远程访问场景下用 Safari 直接打开 Web UI 或复制 HLS 地址到 infuse 等播放器即可,
无需额外配置。0.11.0 之前的版本在 iOS 上播放受保护 HLS 会遇到分片 401,升级即修复。

## 局限性

上述方案都需要用户**自行维护第三方账号、客户端或服务器**,且各有妥协:

- Tailscale 免费版有设备数限制(100 台)
- Cloudflare Tunnel 不支持 WebRTC UDP
- 自建 VPN 需要公网 IP 和运维

如果这些方案的妥协对你不可接受,可以关注 MiBee 后续版本对远程访问体验的改进。

---

## 下一步

- 配置 ICE 服务器后,可在 [WebRTC 测试页](https://webrtc.github.io/samples/src/content/peerconnection/trickle-ice/) 验证 STUN/TURN 是否生效。
- 部署后建议立即修改默认 admin 密码,并启用 TLS(`server.tls_listen`)。
