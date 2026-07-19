# 小米 API

## 小米云认证

**端点：** `POST /api/xiaomi/auth`

通过小米云服务进行认证。

**请求体：**
```json
{
  "username": "xiaomi@example.com",
  "password": "password123"
}
```

**请求：**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "username": "xiaomi@example.com",
    "password": "password123"
  }' \
  "http://localhost:9090/api/xiaomi/auth"
```

**响应：**
```json
{
  "user_id": "1234567890",
  "token": "xiaomi_token_123",
  "region": "cn"
}
```

## 获取小米验证码

**端点：** `POST /api/xiaomi/captcha`

获取小米认证所需的验证码。

**请求体：**
```json
{
  "username": "xiaomi@example.com"
}
```

**请求：**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "username": "xiaomi@example.com"
  }' \
  "http://localhost:9090/api/xiaomi/captcha"
```

**响应：**
```json
{
  "captcha_id": "captcha_123",
  "captcha_image": "base64_encoded_image"
}
```

## 验证小米验证码

**端点：** `POST /api/xiaomi/verify`

验证验证码并完成认证。

**请求体：**
```json
{
  "captcha_id": "captcha_123",
  "captcha_code": "ABC123",
  "username": "xiaomi@example.com",
  "password": "password123"
}
```

**请求：**
```bash
curl -u username:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "captcha_id": "captcha_123",
    "captcha_code": "ABC123",
    "username": "xiaomi@example.com",
    "password": "password123"
  }' \
  "http://localhost:9090/api/xiaomi/verify"
```

**响应：**
```json
{
  "user_id": "1234567890",
  "token": "xiaomi_token_123",
  "region": "cn"
}
```

## 列出小米设备

**端点：** `GET /api/xiaomi/devices`

从云端获取小米设备列表。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/xiaomi/devices"
```

**响应：**
```json
{
  "devices": [
    {
      "did": "camera_did_123",
      "name": "Front Door Camera",
      "model": "xiaomi.camera.v2",
      "online": true
    }
  ]
}
```

## 同步小米设备

**端点：** `POST /api/xiaomi/sync`

将小米设备与本地配置同步。

**请求：**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/xiaomi/sync"
```

**响应：**
```json
{
  "synced": 2,
  "added": 1,
  "removed": 0
}
```

## 检查小米供应商

**端点：** `GET /api/xiaomi/check-vendor`

通过 DID 检查小米设备的供应商协议。自 v0.9.0 起，CS2 和 TUTK（旧版）协议都受支持，因此此端点对两者都返回 `compatible: true`。`vendor` 字段告诉你设备使用的协议，以便 UI 选择正确的传输。当小米集成被禁用、未配置 token 或供应商查询出错时，端点也会返回 `compatible: true` 且 `vendor: "unknown"`（预添加闸门从不在不确定时阻止）。

**查询参数：**

| 参数 | 类型 | 必填 | 描述 | 示例 |
|-----------|------|----------|-------------|---------|
| `did` | string | 是 | 要检查的设备 DID | `camera_did_123` |

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/xiaomi/check-vendor?did=camera_did_123"
```

**响应（CS2 设备）：**
```json
{
  "vendor": "cs2",
  "compatible": true
}
```

**响应（TUTK / 旧版设备）：**
```json
{
  "vendor": "tutk",
  "compatible": true
}
```

**响应（未知 —— 集成禁用、无 token 或查询出错）：**
```json
{
  "vendor": "unknown",
  "compatible": true
}
```
