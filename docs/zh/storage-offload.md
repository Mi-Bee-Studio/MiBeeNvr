# 对象存储冷备（S3 Offload）

> 适用于 MiBeeNvr v0.13.0 · 特性 issue [#874](https://github.com/Mi-Bee-Studio/MiBeeNvr/issues/874)，默认关闭

把合并完成（rolling 合并落盘）后的录像**异步上传**到 S3 兼容对象存储做冷备/归档：本地录像行为完全不变，上传成功并经远端 `HeadObject` 校验后，可选择逐出本地副本释放磁盘。适合：

- 家用宽带/NAS 磁盘有限，想把冷数据归档到便宜的对象存储（Cloudflare R2、B2 无出口流量费）
- 异地容灾——机器被偷/硬盘损坏后录像仍在云端
- 云端对象直接接入支持 S3 协议的回放/取证工具链（保留期交给 bucket lifecycle 管理）

支持的目标：AWS S3、MinIO、Cloudflare R2、Backblaze B2、阿里云 OSS、腾讯云 COS 等 S3 兼容存储（已用真实 MinIO 验证）。

## 工作原理

```
录像段 ──rolling 合并──▶ 合并产物 ──(min_age 宽限后)──▶ offload_outbox 队列
                                                          │
                              扫描循环（默认 60s）发现待上传行 ──▶ S3 PUT ──▶ HeadObject 校验
                                                          │                    │
                                              迟到增长按 size-compare 重传 ◀── 校验通过 → uploaded
                                                                               │
                                                          手动 CLI 逐出本地副本 ◀── evictable
```

- **outbox 表持久化上传状态**（`pending → uploading → uploaded → evicted`，另有终态 `skipped`）：崩溃重启后自动重排队，不会丢也不会重复上传（覆盖写幂等）
- **最小年龄宽限**（默认 15 分钟）：合并 debounce 与回填产物落地后才允许上传，绝不传一个还在增长的桶；万一迟到的增长漏网，size-compare 会触发幂等重传
- **backlog 上限告警**（默认 5000 行）：上游带宽跟不上产能时停止入队并告警，而不是无限排队
- 上传使用进程级 **I/O 预算**（租户 `offload`），与录像/回放/合并共享调度，不挤占前台
- **远端对象永不删除**——本地逐出前逐对象 Head 复核；远端清理交给 bucket lifecycle 策略

对象键布局：`<prefix>/<camera>/<date>/<id>.<ext>`（默认前缀 `recordings`）。

## 开启方式

配置文件 `mibee-nvr.yaml` 的 `storage.remote` 块（默认整体关闭）：

```yaml
storage:
  remote:
    enabled: true
    endpoint_url: https://s3.example.com        # 必填；AWS 填区域端点
    region: auto                                  # R2 用 auto；MinIO 可留默认
    bucket: my-nvr-archive                        # 必填
    path_style: true                              # MinIO/自建必须 true；AWS 虚拟主机桶设 false
    access_key_id: ${S3_ACCESS_KEY}               # 支持 ${VAR} 环境变量引用
    secret_access_key: ${S3_SECRET_KEY}           # 自动加密存储；绝不明文回写
    prefix: recordings
    upload:
      max_concurrency: 1                          # 家用上行是瓶颈，默认 1（上限 8）
      scan_interval_s: 60                         # 发现扫描周期（上限 3600）
      min_age_s: 900                              # 合并完成最小年龄（60–86400）
      backlog_limit: 5000                         # 待上传行数上限（0 = 不限）
    evict:
      after_days: 0                               # 0 = 只上传不自动逐出（当前版本推荐）
```

改完用 `mibee-nvr validate-config` 校验，重启服务生效。

**凭据与安全**：

- `access_key_id` / `secret_access_key` 支持 `${VAR}` 环境变量引用——展开发生在客户端构造时，配置文件里只存引用，从 Web 设置页保存也不会把明文写回 YAML
- `secret_access_key` 属于自动加密字段集：落盘前自动加密，`encrypt-config` 一致处理
- 未设置的 `${VAR}` 展开为空字符串，会在启动校验时报"凭证为空"——错误配置响亮失败而非带病运行

## 逐出本地副本

当前版本逐出走 CLI（自动逐出后续版本提供）。**默认 dry-run**，先看报告再执行：

```bash
# 队列状态：pending/uploading/uploaded/evicted 计数与最老待传年龄
mibee-nvr offload status

# 逐出报告（dry-run，不动任何文件）
mibee-nvr offload evict --all-uploaded

# 只处理一路相机
mibee-nvr offload evict --camera front-door

# 确认无误后执行：每个对象先 HeadObject 复核远端存在，再删本地
mibee-nvr offload evict --all-uploaded --execute
```

- 逐出**只删本地文件**，远端对象永不删除；回滚冷备只需重新下载对象
- 可对运行中的服务执行（WAL 读并发安全）；大批量建议在低峰执行
- `--execute` 前务必确认 bucket 的 lifecycle 策略不会过早删除对象

## 与清理/保留期的关系

- `cleanup`（本地保留期）照常运行——它只管本地文件；上传与否不影响本地清理决策
- 需要冷数据长期保留时：本地 `retention_days` 保持短周期（如 7 天），远端交给 bucket lifecycle（如 365 天）
- 磁盘水位（`disk_threshold_percent`）触发的紧急清理同样不区分是否已上传——想"已上传就先不删"请用逐出 CLI 主动腾空间

## 常见问题

| 问题 | 答案 |
|------|------|
| 上传会不会抢录像的磁盘 IO？ | 不会——offload 是共享 I/O 预算的租户之一，与录像/回放/合并统一调度 |
| 上传一半崩溃了怎么办？ | outbox 行停在 `uploading`，重启后自动重排队幂等覆盖 |
| 为什么某条录像一直不上传？ | 看年龄是否小于 `min_age_s`（默认 15 分钟）；再看 backlog 是否触顶告警 |
| Web 界面能配置吗？ | 设置页可改凭证与开关；逐出与状态查看走 CLI |
| R2 怎么配？ | `endpoint_url: https://<account>.r2.cloudflarestorage.com`、`region: auto`、`path_style: true` |

## 下一步

- [CLI 参考](cli.md) — `offload status / evict` 完整参数
- [性能调优](performance.md) — I/O 预算如何分配各租户
- [存储管理](storage-management.md) — 本地多盘与按相机存储
