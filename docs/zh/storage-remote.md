# 远程对象存储归档（S3 兼容）

MiBee NVR 可将合并后的录像上传到 S3 兼容对象存储（AWS S3、MinIO、Cloudflare R2、Backblaze B2、阿里云 OSS、腾讯云 COS —— 同一套协议），用于异地容灾或小盘 NAS 上云归档。本地录制完全不受影响：录制、合并、回放、清理照旧，上传是纯粹的后台旁路，并让路录制 I/O。

## 工作原理

- **上传对象**：rolling merge 产物 —— 合并引擎已经生成的按小时合并的 MP4 文件（而非 30 秒碎片段）。一条录像 = 一个对象，对象数与请求数可控。
- **触发时机**：后台周期扫描（默认 60s）发现窗口已关闭超过 `upload.min_age_s`（默认 15 分钟，覆盖合并 debounce 与 backfill 延迟，绝不上传仍在增长的窗口）的合并录像。漏网的迟到追加由尺寸检查兜底重传（同对象幂等覆盖）。
- **崩溃安全**：每次上传记录在 outbox 表（`pending → uploading → uploaded → evicted`）。上传中途被 kill -9，下次启动自动重排重传 —— 同对象键、幂等覆盖，无重复对象、无丢段。
- **上传确认**：PUT 之后以 HEAD 请求比对远端对象尺寸，一致才标记为 `uploaded`。截断或假成功永远不会被当作已确认。
- **上行带宽是瓶颈，不是 NVR**：上传以共享 I/O 预算的 `offload` 租户限速让路录制；积压上限触发时停止入队并大声告警（上行跟不上产能时止损）——绝不静默丢弃数据。

## 配置

```yaml
storage:
  remote:
    enabled: true
    endpoint_url: "http://192.168.1.10:9000"   # S3 API 端点
    region: "auto"                              # R2/B2/MinIO 忽略
    bucket: "nvr-archive"
    path_style: true                            # MinIO/自建保持 true
    access_key_id: "${S3_ACCESS_KEY}"           # 支持环境变量引用
    secret_access_key: "${S3_SECRET_KEY}"
    upload:
      max_concurrency: 1                        # 并发上传数
      scan_interval_s: 60                       # 发现扫描周期
      min_age_s: 900                            # 窗口收尾宽限
      backlog_limit: 5000                       # 积压上限（0 = 不限）
    evict:
      after_days: 0                             # 上传确认后本地保留天数
    playback:
      presigned: false                          # 302 直连播放（见下）
      endpoint_url: ""                          # 浏览器可达端点覆写
      ttl_s: 3600
```

设置在下次启动生效；Web 界面（设置 → 存储）提供同样的字段并显示实时队列状态。远程归档默认关闭。

### 凭据

`access_key_id` / `secret_access_key` 支持 `${VAR}` 环境变量引用 —— NVR 只在构建 S3 客户端时展开，绝不把明文写回配置文件。设置了 `NVR_ENCRYPTION_KEY` 的环境另有静态加密（`mibee-nvr encrypt-config`）。

### 按相机路由

把指定相机路由到别的桶和/或键前缀（与按相机存储根目录同语义）：

```yaml
storage:
  remote:
    camera_overrides:
      front-door: { bucket: "important", prefix: "yard" }
      back-yard:   { prefix: "outdoor" }
```

outbox 行在入队时钉死对象所在桶 —— 之后的配置改动不会移动已上传的对象。指向未知相机的覆写会在校验时报错。

## 驱逐（释放本地空间）

仅上传永远不会删除本地文件。上传确认后释放空间的两种方式：

- **自动** —— `evict.after_days: 30`：上传确认 30 天后自动删除本地副本。
- **手动 CLI** —— 先验证再执行：

```bash
mibee-nvr offload evict --config /path/to/mibee-nvr.yaml --all-uploaded          # 干跑报告
mibee-nvr offload evict --config /path/to/mibee-nvr.yaml --all-uploaded --execute
```

每次驱逐在删除本地文件前，都以一次全新的 HEAD 请求复核远端对象（尺寸精确匹配）。校验失败即拒绝驱逐并大声报告 —— 这是防"上传假成功丢录像"的唯一防线。**NVR 永不删除远端对象**；桶内保留交给存储方的 lifecycle 规则。

## 远端录像回放

被驱逐的录像在 Web 界面仍然可见：录像页的"云端归档"区块按日期列出，可内联播放与下载。

- **默认（代理）**：回放经 NVR 提供（`GET /api/offload/objects/{id}`，支持 HTTP Range）。代理对浏览器拖动进度条产生的大量小范围请求做范围合并 —— 按块对齐抓取 + 小型 LRU 缓存 —— 不会打爆对象存储。
- **Presigned 直连**（`playback.presigned: true`）：回放端点改为 302 到短时效签名 URL，视频字节从存储直达浏览器，NVR 零中转。仅在浏览器能访问存储端点时开启 —— 若 NVR 通过 Docker 内网名访问 MinIO，请用 `playback.endpoint_url` 填浏览器可达地址。签名失败会静默回退代理。

## 观测

```bash
mibee-nvr offload status --config /path/to/mibee-nvr.yaml
```

同样的计数以 Prometheus 指标暴露：`nvr_offload_outbox_rows{status}`（积压 = pending+uploading）与 `nvr_offload_uploaded_bytes_total`；`/api/offload/status` 供设置页使用。

## 常见问题

**哪些存储可用？** 任何 S3 API 兼容实现：AWS S3、MinIO（自建，局域网首选）、Cloudflare R2、Backblaze B2、阿里云 OSS、腾讯云 COS。MinIO 与多数自建存储需要 path-style 寻址（默认已开）；AWS 虚拟主机桶设 `path_style: false`。

**上行带宽跟不上录制怎么办？** 积压增长到 `backlog_limit` 后停止入队并告警（本地保留策略照常，数据不丢）。可以调大上限、接受延迟，或减少录制内容。

**能否完全上云（不落本地盘）？** 不能，这是有意的设计。崩溃一致性、合并与回放都将依赖上行带宽 —— 最弱的一环不能做主路径。本地优先 + 异步上传是受支持的模型。

**费用提示**：付费存储的主要成本是出口流量与请求数 —— 代理的范围合并、以及上传合并段而非碎片段，都是为了压低请求数。
