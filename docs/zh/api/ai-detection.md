# AI 检测 API

## 架构

> **AI 推理完全在浏览器端运行**，基于 [ONNX Runtime Web](https://onnxruntime.ai/docs/tutorials/web/)（优先使用 WebGPU，回退到 WASM SIMD）。Go 后端**不执行任何 AI 推理**。

这是刻意的设计：NVR 以静态 `CGO_ENABLED=0` 二进制形式发布，需要交叉编译到 ARM64/ARMv7。在 Go 中捆绑 ONNX 运行时（`libonnxruntime`）会引入 C 依赖、使二进制膨胀，并增加 ARM 交叉编译的复杂度——FFmpeg 已经是最重的依赖。此外，检测针对的是已经解码的实时流、按观看者进行，因此在浏览器端运行可避免服务器重复解码，并随观看者而非摄像头数量扩展。

后端的职责仅限于：

- **(a) 持久化 AI 配置与 ROI 区域** —— 即下方所有 `/api/ai/*` 端点。
- **(b) 向浏览器提供 `.onnx` 模型文件** —— 公开路由 [`GET /models/{filename}`](#提供模型文件)。

浏览器端推理流水线位于 `web/src/lib/ai-detection/`（`runtime.ts` = ONNX Runtime Web 会话，`inference.ts` = YOLOv11-nano 预处理 / NMS / EMA 平滑）。**不存在** `POST /api/ai/enable`、`POST /api/ai/disable` 或 `GET /api/ai/events` SSE 端点——检测结果不会流经后端。

> 所有 `/api/ai/*` 端点均需 HTTP Basic Auth。仅 [`GET /models/{filename}`](#提供模型文件) 为公开。

---

## 获取 AI 状态

**端点：** `GET /api/ai/status`

返回全局 AI 配置。此处仅为配置状态——不存在按摄像头的推理状态，因为推理在浏览器端运行。

**请求：**
```bash
curl -u admin:password \
  "http://localhost:9090/api/ai/status"
```

**响应：** `200 OK`
```json
{
  "enabled": true,
  "model_url": "/models/yolo11n.onnx",
  "confidence_threshold": 0.5,
  "frame_skip_rate": 10
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `enabled` | bool | 是否启用 AI（由浏览器 UI 读取） |
| `model_url` | string | 浏览器加载的模型路径（相对路径或白名单 HTTPS） |
| `confidence_threshold` | float | 检测置信度阈值 `[0, 1]` |
| `frame_skip_rate` | int | 每隔 N 帧执行一次推理 |

---

## 更新 AI 配置

**端点：** `PUT /api/ai/config`

运行时更新全局 AI 配置。所有字段均可选——仅更新提供的字段（部分更新）。更改会原子地持久化到 YAML 配置。

**请求体：**
```json
{
  "enabled": true,
  "confidence_threshold": 0.6,
  "frame_skip_rate": 5,
  "model_url": "/models/yolo11n.onnx"
}
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `enabled` | bool | 否 | 启用/禁用 AI |
| `confidence_threshold` | float | 否 | 必须在 `[0, 1]` 范围内 |
| `frame_skip_rate` | int | 否 | 必须 `> 0` |
| `model_url` | string | 否 | 模型路径（相对路径或白名单 HTTPS） |

**请求：**
```bash
curl -u admin:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": true,
    "confidence_threshold": 0.6,
    "frame_skip_rate": 5
  }' \
  "http://localhost:9090/api/ai/config"
```

**响应：** `200 OK`
```json
{ "status": "updated" }
```

**错误：** `400 Bad Request` —— `{"error":"invalid request body"}`（JSON 体无法解析时）。

---

## 列出 ROI 区域

**端点：** `GET /api/ai/zones`

列出所有摄像头的 ROI（感兴趣区域）。始终返回数组（无区域时为空数组）。

**请求：**
```bash
curl -u admin:password \
  "http://localhost:9090/api/ai/zones"
```

**响应：** `200 OK`
```json
{
  "zones": [
    {
      "camera_id": "front-door",
      "zone": {
        "name": "driveway",
        "points": [[0.1, 0.1], [0.9, 0.1], [0.9, 0.9], [0.1, 0.9]]
      },
      "enabled": true
    }
  ]
}
```

---

## 创建 ROI 区域

**端点：** `POST /api/ai/zones`

创建新的 ROI 区域。区域**名称在所有摄像头间全局唯一**。

**请求体：**
```json
{
  "camera_id": "front-door",
  "zone": {
    "name": "driveway",
    "points": [[0.1, 0.1], [0.9, 0.1], [0.9, 0.9], [0.1, 0.9]]
  },
  "enabled": true
}
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `camera_id` | string | 是 | 目标摄像头 ID |
| `zone.name` | string | 是 | 全局唯一的区域名称（用作更新/删除时的 `{id}`） |
| `zone.points` | `[[x,y],...]` | 是 | 多边形顶点，≥ 3 个点，归一化 `[0, 1]` |
| `enabled` | bool | 否 | 是否启用该区域过滤 |

**请求：**
```bash
curl -u admin:password \
  -X POST \
  -H "Content-Type: application/json" \
  -d '{
    "camera_id": "front-door",
    "zone": {
      "name": "driveway",
      "points": [[0.1, 0.1], [0.9, 0.1], [0.9, 0.9], [0.1, 0.9]]
    },
    "enabled": true
  }' \
  "http://localhost:9090/api/ai/zones"
```

**响应：** `201 Created`
```json
{
  "camera_id": "front-door",
  "zone": {
    "name": "driveway",
    "points": [[0.1, 0.1], [0.9, 0.1], [0.9, 0.9], [0.1, 0.9]]
  },
  "enabled": true
}
```

**错误：**
- `400 Bad Request` —— `{"error":"camera_id and zone.name are required"}`
- `409 Conflict` —— `{"error":"zone with this name already exists"}`

---

## 更新 ROI 区域

**端点：** `PUT /api/ai/zones/{id}`

更新现有区域。`{id}` 路径参数为区域的**名称**（区域名全局唯一）。支持重命名和/或替换多边形顶点。

**路径参数：**

| 参数 | 说明 |
|------|------|
| `id` | 要更新的区域**名称** |

**请求体：**
```json
{
  "camera_id": "front-door",
  "zone": {
    "name": "front-lawn",
    "points": [[0.0, 0.0], [1.0, 0.0], [1.0, 1.0], [0.0, 1.0]]
  },
  "enabled": true
}
```

**请求：**
```bash
curl -u admin:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "camera_id": "front-door",
    "zone": { "points": [[0.0, 0.0], [1.0, 0.0], [1.0, 1.0]] }
  }' \
  "http://localhost:9090/api/ai/zones/driveway"
```

**响应：** `200 OK`
```json
{ "status": "updated" }
```

**错误：**
- `400 Bad Request` —— 缺少区域 id 或请求体无效
- `404 Not Found` —— `{"error":"zone not found"}`
- `409 Conflict` —— `{"error":"zone with new name already exists"}`（重命名为已存在的名称时）

---

## 删除 ROI 区域

**端点：** `DELETE /api/ai/zones/{id}`

按名称删除区域。`{id}` 为区域**名称**。

**请求：**
```bash
curl -u admin:password \
  -X DELETE \
  "http://localhost:9090/api/ai/zones/driveway"
```

**响应：** `200 OK`
```json
{ "status": "deleted" }
```

**错误：**
- `400 Bad Request` —— 缺少区域 id
- `404 Not Found` —— `{"error":"zone not found"}`

---

## 提供模型文件

**端点：** `GET /models/{filename}`

使用 `http.ServeFile` 从 `{storage_root}/models/` 目录提供 AI 模型文件（支持 HTTP range 部分下载）。

> **公开——无需认证。** 此路由刻意不做认证，以便浏览器在依赖会话认证的流媒体之前/之外也能获取 ONNX 模型。这是**唯一**公开的 AI 相关路由；所有 `/api/ai/*` 端点仍需 Basic Auth。

**路径参数：**

| 参数 | 说明 |
|------|------|
| `filename` | 模型文件名，例如 `yolo11n.onnx` |

**请求：**
```bash
curl "http://localhost:9090/models/yolo11n.onnx" -o yolo11n.onnx
```

**响应：** `200 OK`，返回文件字节（`Content-Type` 自动识别，`Accept-Ranges: bytes`）。

**错误：** `filename` 为空时 `400 Bad Request`；文件不存在时返回标准 `404`。

### 将模型放入磁盘

模型文件由运维人员放入 `{storage_root}/models/` 目录——**没有**用于模型上传/下载的 HTTP 端点。内置 CLI 子命令会下载默认的 YOLOv11-nano 模型：

```bash
mibee-nvr download-model -config mibee-nvr.yaml
# 或：make download-model RPi_HOST=user@host  （在远程主机上部署并下载）
```

---

## 数据模型

### ROI 区域

```json
{
  "camera_id": "front-door",
  "zone": {
    "name": "driveway",
    "points": [[0.1, 0.1], [0.9, 0.1], [0.9, 0.9], [0.1, 0.9]]
  },
  "enabled": true
}
```

- **`points`** —— `[x, y]` 形式的多边形顶点，使用**归一化 `[0, 1]`** 坐标（相对于画面，与分辨率无关）。最少 3 个点。多边形隐式闭合（最后一个顶点连回第一个）。
- **`zone.name`** —— 在所有摄像头间全局唯一；同时用作更新/删除时的 `{id}` URL 参数。

### 错误响应

所有错误使用统一的辅助格式：

```json
{ "error": "可读的错误信息" }
```

| 状态码 | 含义 |
|--------|------|
| `200` / `201` | 成功 |
| `400` | 请求错误（请求体无效、缺少必填字段） |
| `404` | 区域未找到 |
| `409` | 冲突（区域名重复） |
