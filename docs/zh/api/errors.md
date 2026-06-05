# 错误响应

所有错误响应遵循以下格式：

```json
{
  "error": "Error message",
  "code": "ERROR_CODE"
}
```

## 错误代码参考

| 代码 | 描述 | HTTP 状态码 |
|------|-------------|-------------|
| `CAMERA_NOT_FOUND` | 指定 ID 的摄像头不存在 | 404 |
| `CAMERA_ALREADY_RUNNING` | 摄像头录制器已处于活跃状态 | 400 |
| `CAMERA_DISABLED` | 摄像头已禁用，无法启动 | 400 |
| `CAMERA_ALREADY_EXISTS` | 指定 ID 的摄像头已存在 | 409 |
| `RECORDING_NOT_FOUND` | 指定 ID 的录制文件不存在 | 404 |
| `STORAGE_FULL` | 磁盘空间严重不足 | 507 |
| `AUTH_REQUIRED` | 需要身份认证 | 401 |
| `AUTH_FAILED` | 身份认证凭据被拒绝 | 401 |
| `INVALID_INPUT` | 请求包含无效参数 | 400 |
| `PATH_TRAVERSAL` | 检测到路径遍历尝试 | 400 |
| `HLS_MAX_STREAMS` | 已达到最大并发 HLS 流限制 | 503 |
| `HLS_UNSUPPORTED_CODEC` | 摄像头编码格式不支持 HLS 流 | 400 |
| `ONVIF_NOT_CAMERA` | 设备不是 ONVIF 摄像头 | 400 |
| `ONVIF_CONNECTION_FAILED` | 连接到 ONVIF 设备失败 | 500 |
| `ONVIF_NO_PROFILES` | 未找到 ONVIF 摄像头的媒体配置文件 | 400 |
| `INTERNAL` | 内部服务器错误 | 500 |

## 常见错误示例

### 认证失败
```json
{
  "error": "authentication failed: invalid username or password",
  "code": "AUTH_FAILED"
}
```

### 摄像头未找到
```json
{
  "error": "camera not found: non-existent-camera",
  "code": "CAMERA_NOT_FOUND"
}
```

### 无效输入
```json
{
  "error": "invalid input: camera URL must be valid",
  "code": "INVALID_INPUT"
}
```

### 存储已满
```json
{
  "error": "storage full: disk space critically low",
  "code": "STORAGE_FULL"
}
```

## HTTP 状态码

| 状态码 | 描述 |
|-------------|-------------|
| 200 | OK - 请求成功 |
| 201 | Created - 资源创建成功 |
| 202 | Accepted - 请求已被接受处理（例如异步下载） |
| 204 | No Content - 请求成功，无响应体 |
| 400 | Bad Request - 请求参数无效 |
| 401 | Unauthorized - 认证失败或需要认证 |
| 403 | Forbidden - 资源访问被拒绝 |
| 404 | Not Found - 资源不存在 |
| 409 | Conflict - 资源冲突（例如摄像头 ID 重复） |
| 500 | Internal Server Error - 服务器端错误 |
| 502 | Bad Gateway - 上游服务错误（例如 ONVIF 探测） |
| 503 | Service Unavailable - 服务暂时不可用 |
| 507 | Insufficient Storage - 磁盘空间不足 |
