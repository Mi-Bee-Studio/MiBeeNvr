# 健康与系统 API

## 健康检查

**端点：** `GET /api/health`

获取系统整体健康状态，包括数据库和存储磁盘空间。公开端点（无需认证）。

响应中的 `device_id` 是首次启动自动生成并持久化的稳定 UUID（`server.device_id`），
`device_name` 默认取主机名 —— 局域网客户端可以用它们锚定设备身份，而非易变的 IP。

**请求：**
```bash
curl http://localhost:9090/api/health
```

**响应：**
```json
{
  "status": "ok",
  "checks": {
    "database": {
      "status": "ok",
      "message": ""
    },
    "goroutines": {
      "status": "ok",
      "message": "167 goroutines"
    },
    "storage": {
      "status": "ok",
      "message": "43% used (1272250408960 / 2953130397696 bytes)"
    }
  },
  "uptime": "2h34m15s",
  "setup_required": false,
  "device_id": "371da2dc-7804-4706-b424-ce50d14ce2d2",
  "device_name": "mibee-nvr",
  "cameras": {
    "total": 13,
    "recording": 10,
    "reconnecting": 0,
    "error": 3,
    "offline": 0,
    "details": [
      { "id": "front-door", "name": "前门", "status": "healthy", "score": 75 }
    ]
  }
}
```

`setup_required` 为 `true` 时表示尚未初始化（无管理员密码），应引导用户完成初始化向导。

## 就绪检查

**端点：** `GET /api/readyz`

检查系统是否已准备好接受请求（与健康检查相同）。

**请求：**
```bash
curl http://localhost:9090/api/readyz
```

**响应：**
```json
{
  "status": "ok"
}
```

## 系统统计

**端点：** `GET /api/stats/system`

获取详细的系统统计信息，包括 CPU、内存和网络使用情况。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/stats/system"
```

**响应：**
```json
{
  "cpu": {
    "total": 1234567,
    "idle": 987654
  },
  "memory": {
    "total": 1073741824,
    "available": 536870912,
    "process_rss": 10485760
  },
  "network": {
    "bytes_sent": 1048576,
    "bytes_recv": 2097152
  },
  "uptime": "2h34m15s",
  "timestamp": 1716789012
}
```

## 一键升级（裸机）

升级执行链路的人工入口：应用进程自身无法替换二进制（沙箱限制），因此 `POST /apply` 与自动升级钩子走同一套交接——写入请求文件，启动 polkit 授权的 root 辅助单元（`mibee-nvr-update.service`）。跨重启的进度来自辅助单元持久化在数据目录里的生命周期文件。整体说明见[自动升级](../deployment-autoupdate.md)。

### 触发升级

**端点：** `POST /api/update/apply`

对 `GET /api/update/check` 报告的最新版本触发裸机升级。幂等：已有待处理 / 进行中的升级时，直接返回其当前状态，不叠加新请求。

**请求：**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/update/apply"
```

**响应（200 OK）：**
```json
{
  "id": "apply-18f2a4c1b3d9e701",
  "state": "requested",
  "from": "v0.12.3",
  "to": "v0.13.0"
}
```

**响应（409 Conflict，Docker 部署）：**
```json
{
  "error": "auto-upgrade is not available for Docker deployments — the container is immutable",
  "deployment": "docker",
  "guidance": "use Watchtower or `docker compose pull && docker compose up -d` (see docs: deployment-autoupdate)"
}
```

**响应（409 Conflict，无可用更新）：**
```json
{
  "error": "no update available",
  "state": "idle"
}
```

> 请求文件写入或辅助单元启动失败返回 500（附提示：检查 polkit 规则是否已通过 `install.sh` 安装，回退方案 `sudo mibee-nvr update`）。

### 升级状态

**端点：** `GET /api/update/apply/status`

由生命周期文件 + 辅助单元存活状态合成跨重启的升级状态。`state` 取值：

| 状态 | 含义 |
|------|------|
| `applying` | 辅助单元正在运行 |
| `requested` | 请求文件已写入，辅助单元尚未运行 |
| `success` | 上次升级成功（终态） |
| `failed_rolled_back` | 上次升级失败，已回滚到旧版本（终态） |
| `failed` | 上次升级失败（终态） |
| `idle` | 从未执行过升级 |

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/update/apply/status"
```

**响应：**
```json
{
  "state": "success",
  "auto_apply": false,
  "id": "apply-18f2a4c1b3d9e701",
  "from": "v0.12.3",
  "to": "v0.13.0",
  "time": "2026-09-20T03:12:45Z"
}
```

`auto_apply` 反映 `update.auto_apply` 配置（`true` 时感知层发现新版自动执行，无需人工触发）。`id` / `from` / `to` / `error` / `time` 仅在有过一次升级尝试后返回；`error` 为空表示成功。

### 升级历史

**端点：** `GET /api/update/history`

最近 10 条升级记录，最新在前。

**请求：**
```bash
curl -u username:password \
  "http://localhost:9090/api/update/history"
```

**响应：**
```json
[
  {
    "time": "2026-09-20T03:12:45Z",
    "from": "v0.12.3",
    "to": "v0.13.0",
    "result": "ok"
  },
  {
    "time": "2026-08-02T22:41:10Z",
    "from": "v0.12.1",
    "to": "v0.12.2",
    "result": "failed",
    "error": "health gate: /api/readyz timeout"
  }
]
```

`result`：`ok` | `failed`（`failed` 时附 `error` 原因）。

## 系统管理（仅环回）

以下两个端点供桌面版使用（Windows 托盘 / macOS 菜单栏助手是独立进程，通过它们驱动本机 NVR）。两者**只接受来自 NVR 本机的环回请求**：环回连接 + 环回 / `localhost` Host 头、无代理转发头——本机操作者本来就能 Ctrl+C 进程或手改配置文件，远程调用一律 403。

### 优雅停机

**端点：** `POST /api/system/shutdown`

触发与 SIGINT/SIGTERM 相同的优雅停机路径。响应先返回，停机在后台执行。

**请求：**
```bash
curl -X POST "http://127.0.0.1:9090/api/system/shutdown"
```

**响应：**
```json
{
  "status": "shutting down"
}
```

**响应（403 Forbidden，远程请求）：**
```json
{
  "error": "shutdown is only available from a local session on the NVR machine"
}
```

### 运行中修改监听地址

**端点：** `PUT /api/system/listen`

运行中重绑 HTTP 监听地址（如 `127.0.0.1:9090` → `0.0.0.0:9090` 打开局域网访问）：先绑定新地址、持久化配置、再排空旧监听器——全程无需重启。

**请求体：**
```json
{"listen": "0.0.0.0:9090"}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `listen` | string | `[host:]port` 形式的监听地址（如 `127.0.0.1:9090`、`0.0.0.0:9090`、`:9090`、`[::1]:9090`），端口 1–65535 |

**请求：**
```bash
curl -X PUT \
  -H "Content-Type: application/json" \
  -d '{"listen": "0.0.0.0:9090"}' \
  "http://127.0.0.1:9090/api/system/listen"
```

**响应（202 Accepted）：**
```json
{
  "status": "applying",
  "listen": "0.0.0.0:9090"
}
```

> 响应送达后再后台执行重绑；重绑失败只记录服务端日志，当前监听器继续服务。地址格式非法返回 400，远程请求返回 403。
