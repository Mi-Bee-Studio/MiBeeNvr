# Webhook 触发集成

MiBee NVR 提供一个 HMAC 签名的 HTTP 触发端点，是 [MQTT 触发](mqtt-integration.md)的对等物：传感器、自动化平台、IoT 网关等外部系统不需要 MQTT 代理，直接 POST 一个签名请求即可启动 / 停止录像或抓拍。

## 概述

- **端点**: `POST /api/trigger/webhook/{camera_id}?action=record|stop|snapshot`
- **鉴权**: 不走 BasicAuth——凭据是对请求体的 HMAC-SHA256 签名（Stripe 风格头 `X-MiBee-Signature`），第三方系统只需持有一把预共享密钥
- **动作语义**: 与 MQTT 触发完全一致（两者接入同一个动作分发器）
- **防重放**: 签名时间戳必须落在重放窗口内（默认 ±300 秒，双向校验）
- **限流**: 与 `/api/health` 同级的公开限流组（每 IP 60 次 / 分钟）
- **请求体上限**: 1 MB

## 配置

```yaml
trigger:
  webhook:
    enabled: true            # 挂载端点（默认 false）
    secret: "whsec_your_key" # 预共享 HMAC-SHA256 密钥，启用时必填（支持 encrypt-config 静态加密）
    replay_window_s: 300     # 签名时间戳允许的偏移窗口（秒，默认 300）
```

## 签名与调用

签名头格式：

```
X-MiBee-Signature: t=<unix-秒>,v1=<hex(HMAC-SHA256(secret, "<t>.<body>"))>
```

时间戳参与 MAC 运算，攻击者无法给旧签名「续期」；被捕获的请求只在重放窗口内有效。

```bash
# 触发请求体通常为空——签名串就是 "<t>."
T=$(date +%s)
SIG=$(printf '%s.' "$T" | openssl dgst -sha256 -hmac "whsec_your_key" -hex | cut -d' ' -f2)

curl -X POST "http://192.168.1.50:9090/api/trigger/webhook/cam-xxxx?action=record" \
  -H "X-MiBee-Signature: t=$T,v1=$SIG"
```

**响应**：

| 状态码 | 含义 |
|--------|------|
| `202 accepted` | 触发已受理（动作异步执行，结果进结构化日志与审计） |
| `401` | 签名无效 / 过期 / 缺失（统一返回，不区分具体原因，避免成为探测 oracle） |
| `400` | `action` 不是 `record` / `stop` / `snapshot` 之一 |
| `404` | 摄像头不存在（签名校验通过后才会返回，未签名调用者拿不到存在性信息） |
| `503` | 触发分发器未就绪 |

## 与 MQTT 触发的取舍

| | MQTT 触发 | Webhook 触发 |
|---|---|---|
| 依赖 | 需要 MQTT 代理 | 零依赖（HTTP 直连） |
| 方向 | 双向（还可[订阅状态发布](mqtt-integration.md#状态发布)） | 单向（仅触发） |
| 鉴权 | 代理账号密码 | HMAC 签名 + 重放窗 |
| 适合 | 已有智能家居总线的环境 | 单点传感器 / 脚本 / 云函数回调 |
