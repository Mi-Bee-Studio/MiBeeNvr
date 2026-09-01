# 存储管理与录像迁移

> 适用于 MiBeeNvr v0.12.0

录像存到哪里、怎么换盘、怎么把历史录像搬走——全部**运行时可操作，无需重启**。数据库与录像根目录解耦（SQLite 单文件固定在数据目录），切换存储不会带走索引。

## 概念一览

| 概念 | 说明 |
|------|------|
| **默认存储根** | `storage.root_dir`——未单独设置的相机都写到这里 |
| **候选卷** | 可用作存储根的目录白名单（运行时可增删；设置页下拉即从这里取值） |
| **按相机存储** | `storage.camera_roots` 为单路相机指定专属存储（如大码率相机单独挂一块盘） |
| **后台迁移** | 换盘后把历史录像搬去新位置的闲时任务（限速 + 时间窗，不与录制抢 IO） |

## 换盘三步（Web UI）

1. **加候选卷**：设置 → 存储 → 添加候选路径（需已挂载、可写；NAS 部署时由平台授权目录决定，页面会提示「由平台管理」）
2. **切默认根或按相机设置**：存储页直接切换默认存储根；或在摄像头编辑表单的「存储」分区为该相机单独选择
3. **迁移历史录像**：勾选「迁移完成后删除原存储中的录像」（可选），点击「迁移现有录像到新存储」——立即生效，新录像马上写新位置，历史文件后台搬运

切换即时生效（下一段录像就落在新位置），迁移在后台进行，全程不需要重启 NVR。

## 配置文件

```yaml
storage:
  root_dir: "/var/lib/mibee-nvr/recordings"   # 默认存储根
  # db_path: ""                                # 数据库位置（默认数据目录，一般不动；
  #                                             # 裸机部署首次启动会自动固定，换根不受影响）
  camera_roots:                                # 按相机存储覆盖（可省略）
    backyard: "/mnt/bigdisk/recordings"        # 大码率相机单独放一块盘
  migration_rate_mb: 15                        # 后台迁移限速（MB/s，默认 15）
  migration_window: "22:00-06:00"              # 只在该本地时间窗内迁移（空 = 全天限速迁移）
```

迁移器逐文件「复制 → 改写数据库行 → 校验后删源」，每个任务开始前做容量预检（目标盘需留 20% 余量），磁盘不够会直接拒绝而不是写一半失败。

## API

### 管理候选卷

```bash
# 列出候选卷（env_managed=true 表示由部署平台管理，手动添加仅本次会话有效）
curl -u admin:password http://192.168.1.50:9090/api/storage/candidates

# 运行时添加（校验存在/目录/可写，拒绝当前根与已被相机占用的路径）
curl -u admin:password -X POST \
  http://192.168.1.50:9090/api/storage/candidates \
  -H "Content-Type: application/json" \
  -d '{"path": "/mnt/newdisk"}'

# 移除
curl -u admin:password -X DELETE \
  http://192.168.1.50:9090/api/storage/candidates?path=/mnt/newdisk
```

### 按相机存储 + 迁移

```bash
# 设置该相机的存储根；migrate=true 同时把历史录像排入迁移队列
curl -u admin:password -X PUT \
  http://192.168.1.50:9090/api/cameras/backyard/storage-root \
  -H "Content-Type: application/json" \
  -d '{"root": "/mnt/bigdisk/recordings", "migrate": true, "delete_source": true}'

# 查询该相机当前设置与迁移进度
curl -u admin:password http://192.168.1.50:9090/api/cameras/backyard/storage-root
```

### 批量迁移

```bash
# 一键换盘：热切换默认根 + 清除所有按相机覆盖 + 每路相机排入迁移队列
curl -u admin:password -X POST \
  http://192.168.1.50:9090/api/storage/migrate \
  -H "Content-Type: application/json" \
  -d '{"target": "/mnt/newdisk", "delete_source": true}'

# 队列与状态（任务 state: queued/running/paused/done/failed）
curl -u admin:password http://192.168.1.50:9090/api/storage/migrate/status
```

## 平台部署注意事项

- **fnOS / 飞牛**：候选目录来自应用安装时的授权目录（平台管理）——网页里手动添加的路径重启后失效，需要长期使用请在 NAS 侧授权
- **Docker**：容器内路径需先挂载进容器（`-v /mnt/newdisk:/mnt/newdisk`），再作为候选添加；注意「外部存储（USB / 外接盒）」在部分 NAS 平台上受系统内核限制不可用，优先使用存储池目录
- **数据库位置**：SQLite 数据库固定在数据目录（不随录像根切换）。老版本升级后会自动迁移一次到数据卷，无需手工干预

## 常见问题

| 问题 | 说明 |
|------|------|
| 切换后旧录像还在旧盘？ | 正常——新录像立即写新位置，历史文件需触发迁移（存储页按钮或 API `migrate: true`） |
| 迁移会卡住录制吗？ | 不会——限速 15MB/s 默认 + 可配时间窗，容量预检失败会拒绝任务而非半途而废 |
| 能只迁移一路相机吗？ | 能——用按相机存储 API（`PUT /api/cameras/{id}/storage-root`） |
| 迁移中能停吗？ | 迁移器随服务运行，重启后续跑（逐文件断点）；删除源文件发生在每个文件校验通过之后 |

## 下一步

- [自适应录制](adaptive-recording.md) — 从写入密度上先把磁盘占用降下来
- [存储优化研究](storage-research.md) — 存储子系统的设计深潜
