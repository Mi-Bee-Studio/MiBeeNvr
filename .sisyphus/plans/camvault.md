# CamVault — 轻量级 NVR 系统构建计划

## TL;DR

> **Quick Summary**: 在树莓派 3B 上用 Go 构建一个轻量级 NVR 系统，支持从 ESP32 网络摄像头（5+ 台，混合类型）通过 RTSP/HTTP/FTP 等多协议录制视频，提供 Web UI 管理录像、WebDAV/FTP 文件访问，存储在 2.7T 外挂硬盘上。
> 
> **Deliverables**:
> - 单一 Go 静态二进制文件 `camvault`（零 CGO 依赖）
> - H.264 RTSP → MP4 分段录制管线
> - MJPEG RTSP → JPEG 序列录制管线
> - HTTP JPEG 上传接口
> - FTP 服务器（摄像头上传）
> - MQTT 事件触发录制
> - WebDAV 只读文件访问
> - Web UI（浏览、回放、置顶保护、删除）
> - SQLite 元数据库 + 混合清理策略
> - Caddy 反代配置示例
> - systemd 服务文件
> 
> **Estimated Effort**: Large
> **Parallel Execution**: YES - 5 waves
> **Critical Path**: Task 1→5→8→12→16→F1-F4

---

## Context

### Original Request
在空目录中用 Go 实现一个可提供 WebDAV、FTP 等协议的 NVR 项目，部署在挂载 3T 硬盘的树莓派 3B 上，用于从 ESP32 网络摄像头下载和管理录像。

### Interview Summary
**Key Discussions**:
- 摄像头接入方式：多协议（RTSP推流、HTTP上传、FTP上传、MQTT触发、SD卡同步）
- 摄像头数量：5+ 台，混合类型（ESP32-CAM OV2640、ESP32+USB摄像头等）
- 管理界面：需要 Web UI（浏览、回放、删除录像）
- 现有服务：整合到 Caddy 反代
- 存储策略：混合模式（重要录像手动置顶保护，普通录像自动清理）
- 实时预览：不需要，只录不看
- 认证：简单用户名密码
- 测试策略：TDD
- 项目名称：camvault

**Research Findings**:
- **gortsplib v5** (Benchmark 87.7): 纯 Go RTSP 客户端/服务器，支持 H264/H265/MJPEG/AV1/VP9，ARM64 兼容，可读取原始帧
- **chi v5**: 轻量级可组合路由器，适合 REST API + 静态文件服务
- **abema/go-mp4**: 纯 Go MP4 封装器，被 RTSPtoWeb/monibuca/lalmax 等生产项目使用
- **modernc.org/sqlite**: 纯 Go SQLite 驱动，零 CGO，ARM64 交叉编译零障碍
- **ESP32-CAM 关键发现**: OV2640 传感器只输出 JPEG/MJPEG，不输出 H.264！需要两条录制管线
- **FTP 不能反代**: FTP 使用独立数据连接，无法通过 Caddy 反代，需独立端口
- **WebDAV 可反代**: 通过 Caddy 反代可行，但需正确处理 PROPFIND 等方法

### Metis Review
**Identified Gaps** (已处理):
- ESP32 摄像头编码类型：设计双管线（H.264→MP4 + MJPEG→JPEG序列）
- SQLite 驱动选择：使用 modernc.org/sqlite（纯 Go，零 CGO）
- MP4 封装：使用 abema/go-mp4（纯 Go），不依赖 ffmpeg
- 内存预算：NVR 进程 RSS ≤300MB 硬上限
- FTP 反代限制：FTP 需独立端口，不通过 Caddy
- WebDAV 设为只读（v1）：防止元数据不同步
- SD 卡同步推迟到 v2
- 原子文件写入：写入 .tmp 后 rename()
- 崩溃恢复：启动时清理不完整段文件
- RTSP 断线重连：指数退避（1s→2s→4s→8s→max 60s）
- 摄像头分辨率变化：关闭当前段，开启新段

---

## Work Objectives

### Core Objective
构建一个零转码、低内存、单二进制的 NVR 系统，能同时录制 5+ 台 ESP32 摄像头的视频流，并通过 Web UI 和文件协议（WebDAV/FTP）提供录像管理。

### Concrete Deliverables
- `camvault` 单一静态二进制（`CGO_ENABLED=0` 编译）
- YAML 配置文件驱动所有行为
- `/mnt/data/nvr/` 为录像存储根目录
- SQLite 元数据库（`/mnt/data/nvr/camvault.db`）
- systemd 服务单元文件
- Caddy 反代配置示例片段

### Definition of Done
- [x] `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o camvault .` 成功
- [ ] 在树莓派上运行 `./camvault -config config.yaml` 绑定所有配置的端口
- [ ] 5 台摄像头同时录制 1 小时，RSS ≤300MB
- [ ] `ffprobe` 验证录制的 MP4 文件有效
- [ ] Web UI 可浏览、回放、置顶保护、删除录像
- [ ] WebDAV 可只读访问录像文件
- [ ] FTP 可接收摄像头上传
- [x] `go test ./...` 全部通过

### Must Have
- H.264 RTSP 拉流录制为 MP4 分段（10 分钟一段）
- MJPEG RTSP 拉流录制为 JPEG 序列
- HTTP POST JPEG 上传接口
- FTP 服务器（接收摄像头上传）
- MQTT 事件触发（订阅主题，触发录制启停）
- WebDAV 只读文件访问
- REST API（录像列表、回放 URL、置顶保护、删除）
- Web UI（嵌入式前端，go:embed）
- SQLite 元数据（摄像头、录像段、置顶状态）
- 混合清理策略（按时间自动删除 + 置顶保护豁免）
- HTTP Basic Auth 认证
- 原子文件写入（.tmp + rename）
- RTSP 断线自动重连（指数退避）
- 摄像头格式变化检测（关闭当前段，开启新段）
- 存储可用性检测（HDD 掉线保护）
- systemd 服务文件
- Caddy 反代配置示例

### Must NOT Have (Guardrails)
- ❌ 任何形式的视频转码/解码/重编码
- ❌ 实时预览/直播功能
- ❌ CGO 依赖（必须 `CGO_ENABLED=0`）
- ❌ 录像数据写入 SD 卡（只写 /mnt/data/nvr/）
- ❌ SD 卡同步功能（推迟到 v2）
- ❌ 超过 3 种输入格式（仅 H.264 RTSP、MJPEG RTSP、HTTP JPEG POST）
- ❌ OAuth/JWT/RBAC 认证
- ❌ 存储配额清理或智能保留策略
- ❌ Web UI 中的摄像头配置向导或系统设置面板
- ❌ 生成或管理 Caddy 配置文件
- ❌ AI-slop 模式：过度注释、过度抽象、泛型命名（data/result/item/temp）
- ❌ 每个摄像头的并发写入同一文件（单写入 goroutine）
- ❌ 段文件关闭后再修改（元数据只在 SQLite）
- ❌ 信任摄像头端时间戳（全部使用服务器端时间）

---

## Verification Strategy (MANDATORY)

> **ZERO HUMAN INTERVENTION** - ALL verification is agent-executed. No exceptions.

### Test Decision
- **Infrastructure exists**: NO (新项目)
- **Automated tests**: YES (TDD)
- **Framework**: Go 标准 `testing` 包 + `testify` 断言
- **TDD 流程**: 每个 Task 遵循 RED（失败测试）→ GREEN（最小实现）→ REFACTOR

### QA Policy
Every task MUST include agent-executed QA scenarios.
Evidence saved to `.sisyphus/evidence/task-{N}-{scenario-slug}.{ext}`.

- **API/Backend**: Use Bash (curl) - Send requests, assert status + response fields
- **Library/Module**: Use Bash (`go test`) - Run tests, verify pass/fail
- **Video Validation**: Use Bash (ffprobe) - Verify file integrity and codec
- **Process Metrics**: Use Bash (ps) - Check memory usage

---

## Execution Strategy

### Parallel Execution Waves

```
Wave 1 (Start Immediately - project scaffolding + types + storage):
├── Task 1:  Go 项目脚手架 + 构建系统 [quick]
├── Task 2:  配置系统 (YAML加载+校验) [quick]
├── Task 3:  核心类型定义 + 接口设计 [quick]
├── Task 4:  SQLite 元数据库 schema + 迁移 [quick]
├── Task 5:  存储管理器 (文件系统操作) [unspecified-high]
└── Task 6:  认证中间件 (HTTP Basic Auth) [quick]

Wave 2 (After Wave 1 - recording pipelines + upload):
├── Task 7:  H.264 RTSP 录制管线 (depends: 2,3,4,5) [deep]
├── Task 8:  MJPEG RTSP 录制管线 (depends: 2,3,4,5) [deep]
├── Task 9:  HTTP JPEG 上传接口 (depends: 2,3,4,5) [unspecified-high]
├── Task 10: FTP 服务器 (depends: 2,3,4,5) [unspecified-high]
├── Task 11: MQTT 事件触发 (depends: 2,3,4) [unspecified-high]
└── Task 12: MP4 封装器 (abema/go-mp4) (depends: 3) [deep]

Wave 3 (After Wave 2 - API + cleanup + WebDAV):
├── Task 13: REST API — 录像管理 (depends: 4,5,6,7,8,9,12) [unspecified-high]
├── Task 14: 自动清理策略 (depends: 4,5) [unspecified-high]
├── Task 15: WebDAV 只读服务器 (depends: 5,6) [unspecified-high]
└── Task 16: 摄像头管理器 (编排所有录制管线) (depends: 7,8,11) [deep]

Wave 4 (After Wave 3 - Web UI + integration):
├── Task 17: Web UI 前端 (Svelte, embedded) (depends: 13,15) [visual-engineering]
├── Task 18: 集成测试 + 崩溃恢复 (depends: 16,14) [deep]
└── Task 19: 部署配置 (systemd + Caddy + 文档) (depends: 17,18) [quick]

Wave FINAL (After ALL tasks — 4 parallel reviews):
├── Task F1: Plan compliance audit (oracle)
├── Task F2: Code quality review (unspecified-high)
├── Task F3: Real manual QA (unspecified-high)
└── Task F4: Scope fidelity check (deep)
→ Present results → Get explicit user okay

Critical Path: Task 1→5→7→12→13→17→19→F1-F4
Parallel Speedup: ~60% faster than sequential
Max Concurrent: 6 (Wave 2)
```

### Dependency Matrix

| Task | Depends On | Blocks | Wave |
|------|-----------|--------|------|
| 1    | -         | 2,3,4,5,6 | 1 |
| 2    | 1         | 7,8,9,10,11 | 1 |
| 3    | 1         | 7,8,9,10,12 | 1 |
| 4    | 1         | 7,8,9,10,11,13,14 | 1 |
| 5    | 1         | 7,8,9,10,13,14,15 | 1 |
| 6    | 1         | 13,15 | 1 |
| 7    | 2,3,4,5   | 13,16 | 2 |
| 8    | 2,3,4,5   | 13,16 | 2 |
| 9    | 2,3,4,5   | 13 | 2 |
| 10   | 2,3,4,5   | - | 2 |
| 11   | 2,3,4     | 16 | 2 |
| 12   | 3         | 13 | 2 |
| 13   | 4,5,6,7,8,9,12 | 17 | 3 |
| 14   | 4,5       | 18 | 3 |
| 15   | 5,6       | 17 | 3 |
| 16   | 7,8,11    | 18 | 3 |
| 17   | 13,15     | 19 | 4 |
| 18   | 16,14     | 19 | 4 |
| 19   | 17,18     | F1-F4 | 4 |

### Agent Dispatch Summary

- **Wave 1**: **6 tasks** - T1→`quick`, T2→`quick`, T3→`quick`, T4→`quick`, T5→`unspecified-high`, T6→`quick`
- **Wave 2**: **6 tasks** - T7→`deep`, T8→`deep`, T9→`unspecified-high`, T10→`unspecified-high`, T11→`unspecified-high`, T12→`deep`
- **Wave 3**: **4 tasks** - T13→`unspecified-high`, T14→`unspecified-high`, T15→`unspecified-high`, T16→`deep`
- **Wave 4**: **3 tasks** - T17→`visual-engineering`, T18→`deep`, T19→`quick`
- **FINAL**: **4 tasks** - F1→`oracle`, F2→`unspecified-high`, F3→`unspecified-high`, F4→`deep`

---

## TODOs

- [x] 1. Go 项目脚手架 + 构建系统

  **What to do**:
  - 初始化 Go module: `go mod init github.com/mickey/camvault`
  - 创建标准项目目录结构:
    ```
    camvault/
    ├── cmd/camvault/main.go          # 入口
    ├── internal/
    │   ├── config/                    # YAML 配置加载
    │   ├── types/                     # 核心类型和接口
    │   ├── storage/                   # SQLite + 文件存储
    │   ├── recorder/                  # RTSP 录制管线
    │   ├── upload/                    # HTTP 上传接口
    │   ├── ftp/                       # FTP 服务器
    │   ├── mqtt/                      # MQTT 触发
    │   ├── muxer/                     # MP4 封装
    │   ├── api/                       # REST API
    │   ├── cleanup/                   # 自动清理
    │   ├── webdav/                    # WebDAV 服务器
    │   ├── camera/                    # 摄像头管理器
    │   └── middleware/                 # 认证中间件
    ├── web/                           # 前端源码 (Svelte)
    ├── web_embed/                     # go:embed 嵌入目录 (构建后)
    ├── deploy/                        # 部署配置
    ├── tests/                         # 集成测试
    ├── config.example.yaml            # 配置示例
    └── Makefile                       # 构建脚本
    ```
  - 添加核心依赖到 go.mod:
    - `github.com/go-chi/chi/v5`
    - `modernc.org/sqlite`
    - `github.com/bluenviron/gortsplib/v5`
    - `github.com/abema/go-mp4`
    - `github.com/fclairamb/ftpserverlib`
    - `golang.org/x/net/webdav`
    - `github.com/eclipse/paho.mqtt.golang`
    - `gopkg.in/yaml.v3`
    - `github.com/stretchr/testify`
  - 创建 Makefile（build, test, cross-compile, lint 目标）
  - 创建 `.gitignore`
  - 创建 `cmd/camvault/main.go` 骨架（解析 -config flag，打印版本）
  - `go mod tidy` 确保依赖解析

  **Must NOT do**:
  - 不要添加任何业务逻辑
  - 不要安装 CGO 依赖
  - 不要创建多余的 README 或文档

  **Recommended Agent Profile**:
  - **Category**: `quick`
    - Reason: 纯脚手架任务，无复杂逻辑
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO (Wave 1 基础任务)
  - **Parallel Group**: Wave 1
  - **Blocks**: Tasks 2, 3, 4, 5, 6
  - **Blocked By**: None

  **References**:
  - Go module 初始化标准流程
  - 项目目录结构遵循 Go 标准布局

  **Acceptance Criteria**:
  - [ ] `go mod tidy` 成功
  - [ ] `go build ./cmd/camvault/` 成功
  - [ ] 目录结构如上所述
  - [ ] Makefile 包含 build, test, lint 目标

  **QA Scenarios:**
  ```
  Scenario: Build succeeds
    Tool: Bash
    Preconditions: Go installed on dev machine
    Steps:
      1. Run `go build ./cmd/camvault/`
      2. Assert exit code 0
      3. Run `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /dev/null ./cmd/camvault/`
      4. Assert exit code 0 (zero CGO cross-compile works)
    Expected Result: Both builds succeed
    Failure Indicators: Build errors, CGO errors
    Evidence: .sisyphus/evidence/task-1-build.txt

  Scenario: Dependencies resolve correctly
    Tool: Bash
    Preconditions: Project initialized
    Steps:
      1. Run `go mod tidy`
      2. Run `go vet ./...`
      3. Assert exit code 0
    Expected Result: All dependencies resolve, no vet errors
    Failure Indicators: Module errors, version conflicts
    Evidence: .sisyphus/evidence/task-1-deps.txt
  ```

  **Commit**: YES
  - Message: `feat: initialize Go project with build system`
  - Files: go.mod, go.sum, Makefile, .gitignore, cmd/camvault/main.go

- [x] 2. 配置系统 (YAML 加载 + 校验)

  **What to do**:
  - TDD: 先写配置加载和校验的测试
  - 定义 YAML 配置结构体:
    ```go
    type Config struct {
        Server   ServerConfig   `yaml:"server"`
        Storage  StorageConfig  `yaml:"storage"`
        Cameras  []CameraConfig `yaml:"cameras"`
        Cleanup  CleanupConfig  `yaml:"cleanup"`
        Auth     AuthConfig     `yaml:"auth"`
        FTP      FTPConfig      `yaml:"ftp"`
        MQTT     MQTTConfig     `yaml:"mqtt"`
        WebDAV   WebDAVConfig   `yaml:"webdav"`
    }
    ```
  - ServerConfig: 监听地址、端口（默认 9090）
  - StorageConfig: 根目录（默认 /mnt/data/nvr）、分段时长（默认 10min）
  - CameraConfig: ID、名称、协议类型（rtsp_h264/rtsp_mjpeg/http_jpeg）、URL、用户名密码
  - CleanupConfig: 保留天数（默认 30）、检查间隔（默认 1h）、磁盘使用阈值（默认 95%）
  - AuthConfig: 用户名、密码哈希
  - FTPConfig: 端口（默认 2121）、被动模式端口范围
  - MQTTConfig: broker 地址、主题、客户端 ID
  - WebDAVConfig: 启用/禁用、路径前缀
  - 实现 YAML 文件加载函数
  - 实现配置校验（必填字段检查、端口范围检查、URL 格式检查）
  - 创建 `config.example.yaml` 示例配置文件

  **Must NOT do**:
  - 不要实现环境变量覆盖（YAML only for v1）
  - 不要添加热重载/文件监视

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Tasks 3, 4, 5, 6)
  - **Parallel Group**: Wave 1
  - **Blocks**: Tasks 7, 8, 9, 10, 11
  - **Blocked By**: Task 1

  **References**:
  - `gopkg.in/yaml.v3` 标准用法
  - Go struct tags 和 yaml 解析模式

  **Acceptance Criteria**:
  - [ ] `internal/config/config_test.go` 存在且测试通过
  - [ ] 有效 YAML 加载成功
  - [ ] 无效 YAML 返回明确错误
  - [ ] 缺少必填字段返回校验错误
  - [ ] `config.example.yaml` 包含所有字段且可加载

  **QA Scenarios:**
  ```
  Scenario: Valid config loads correctly
    Tool: Bash
    Steps:
      1. Run `go test ./internal/config/ -run TestLoad -v`
      2. Assert all tests pass
    Expected Result: Config loads, fields populated correctly
    Evidence: .sisyphus/evidence/task-2-config-load.txt

  Scenario: Invalid config returns errors
    Tool: Bash
    Steps:
      1. Run `go test ./internal/config/ -run TestValidate -v`
      2. Assert validation errors for missing fields, invalid ports, bad URLs
    Expected Result: Clear error messages for each invalid input
    Evidence: .sisyphus/evidence/task-2-config-validate.txt
  ```

  **Commit**: YES
  - Message: `feat(config): add YAML config loading and validation`
  - Files: internal/config/*.go, config.example.yaml

- [x] 3. 核心类型定义 + 接口设计

  **What to do**:
  - TDD: 先定义接口和类型的测试（编译期检查）
  - 定义核心接口:
    ```go
    // Recorder 录制器接口 — 每种协议一个实现
    type Recorder interface {
        Start(ctx context.Context) error
        Stop() error
        Status() RecorderStatus
    }

    // StorageProvider 存储提供者
    type StorageProvider interface {
        CreateSegment(cameraID string, meta SegmentMeta) (*Segment, error)
        CloseSegment(segmentID string) error
        WriteFrame(segmentID string, data []byte) error
        ListRecordings(filter RecordingFilter) ([]Recording, error)
        GetRecording(id string) (*Recording, error)
        DeleteRecording(id string) error
        PinRecording(id string) error
        UnpinRecording(id string) error
    }
    ```
  - 定义核心类型:
    - `Camera` — 摄像头定义（ID, 名称, 协议, URL, 状态）
    - `Recording` — 录像记录（ID, 摄像头ID, 开始/结束时间, 文件路径, 大小, 格式, 是否置顶）
    - `Segment` — 录像段（活跃的正在写入的段文件）
    - `RecordingFilter` — 查询过滤器（摄像头ID, 时间范围, 格式, 置顶状态）
    - `RecorderStatus` — 录制状态（recording/stopped/error/reconnecting）
  - 定义常量:
    - 视频格式: `FormatH264`, `FormatMJPEG`
    - 协议类型: `ProtoRTSP`, `ProtoHTTP`, `ProtoFTP`, `ProtoMQTT`
    - 录制状态枚举

  **Must NOT do**:
  - 不要实现任何业务逻辑，只定义类型和接口
  - 不要引入不必要的泛型或过度抽象

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Tasks 2, 4, 5, 6)
  - **Parallel Group**: Wave 1
  - **Blocks**: Tasks 7, 8, 9, 10, 12
  - **Blocked By**: Task 1

  **References**:
  - Go interface 设计最佳实践: 小接口、组合优于继承

  **Acceptance Criteria**:
  - [ ] `internal/types/` 包含所有类型定义
  - [ ] 所有接口方法签名明确
  - [ ] `go vet ./internal/types/` 无错误
  - [ ] 编译通过（接口正确引用）

  **QA Scenarios:**
  ```
  Scenario: Types compile and interfaces are satisfied
    Tool: Bash
    Steps:
      1. Run `go build ./internal/types/`
      2. Run `go vet ./internal/types/`
      3. Assert exit code 0
    Expected Result: All types and interfaces compile cleanly
    Evidence: .sisyphus/evidence/task-3-types.txt
  ```

  **Commit**: YES
  - Message: `feat(types): define core types and interfaces`
  - Files: internal/types/*.go

- [x] 4. SQLite 元数据库 Schema + 迁移

  **What to do**:
  - TDD: 先写数据库操作的测试
  - 使用 `modernc.org/sqlite` (纯 Go，零 CGO)
  - 设计 schema:
    ```sql
    -- 摄像头表
    CREATE TABLE cameras (
        id          TEXT PRIMARY KEY,
        name        TEXT NOT NULL,
        protocol    TEXT NOT NULL, -- rtsp_h264, rtsp_mjpeg, http_jpeg
        url         TEXT NOT NULL,
        username    TEXT DEFAULT '',
        password    TEXT DEFAULT '',
        enabled     BOOLEAN DEFAULT TRUE,
        created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
    );

    -- 录像段表
    CREATE TABLE recordings (
        id          TEXT PRIMARY KEY, -- UUID
        camera_id   TEXT NOT NULL REFERENCES cameras(id),
        file_path   TEXT NOT NULL,
        format      TEXT NOT NULL, -- h264, mjpeg
        started_at  DATETIME NOT NULL,
        ended_at    DATETIME,
        duration    REAL,
        file_size   INTEGER DEFAULT 0,
        frame_count INTEGER DEFAULT 0,
        pinned      BOOLEAN DEFAULT FALSE,
        created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
    );

    -- 启动时清理的未完成段
    CREATE INDEX idx_recordings_camera ON recordings(camera_id);
    CREATE INDEX idx_recordings_time ON recordings(started_at);
    CREATE INDEX idx_recordings_pinned ON recordings(pinned);
    ```
  - 实现数据库初始化（schema 创建 + PRAGMA 设置）
  - PRAGMA: `journal_mode=WAL`, `synchronous=NORMAL`, `busy_timeout=5000`, `cache_size=-2000`
  - 实现 CRUD 操作: InsertRecording, UpdateRecording, GetRecording, ListRecordings, DeleteRecording
  - 实现 PinRecording/UnpinRecording
  - 实现启动时清理未完成段（ended_at IS NULL 的记录）

  **Must NOT do**:
  - 不要使用 mattn/go-sqlite3 (有 CGO)
  - 不要实现 ORM 或复杂查询构建器
  - 不要使用数据库迁移工具（直接在代码中 CREATE TABLE IF NOT EXISTS）

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Tasks 2, 3, 5, 6)
  - **Parallel Group**: Wave 1
  - **Blocks**: Tasks 7, 8, 9, 10, 11, 13, 14
  - **Blocked By**: Task 1

  **References**:
  - `modernc.org/sqlite` 纯 Go SQLite 驱动用法
  - PRAGMA 设置: WAL mode 是 SQLite 并发写入的最佳实践

  **Acceptance Criteria**:
  - [ ] `internal/storage/db_test.go` 全部通过
  - [ ] Schema 创建正确（可重新打开验证）
  - [ ] CRUD 操作测试通过
  - [ ] WAL mode PRAGMA 生效
  - [ ] 清理未完成段功能测试通过

  **QA Scenarios:**
  ```
  Scenario: Database CRUD operations work
    Tool: Bash
    Steps:
      1. Run `go test ./internal/storage/ -run TestDB -v`
      2. Assert all tests pass (insert, get, list, update, delete)
    Expected Result: All CRUD operations succeed
    Evidence: .sisyphus/evidence/task-4-db-crud.txt

  Scenario: Incomplete segments cleaned on startup
    Tool: Bash
    Steps:
      1. Run `go test ./internal/storage/ -run TestCleanupIncomplete -v`
      2. Assert incomplete recordings (ended_at IS NULL) are deleted
    Expected Result: Only complete recordings remain after cleanup
    Evidence: .sisyphus/evidence/task-4-db-cleanup.txt
  ```

  **Commit**: YES
  - Message: `feat(storage): add SQLite schema and migration`
  - Files: internal/storage/db.go, internal/storage/db_test.go

- [x] 5. 存储管理器 (文件系统操作)

  **What to do**:
  - TDD: 先写文件操作的测试（用临时目录）
  - 实现 `StorageManager` 结构体
  - 功能:
    - 创建摄像头目录结构: `/mnt/data/nvr/{camera_id}/`
    - 创建录像段文件: 写入 `.tmp`，完成后 `os.Rename()` 为最终文件名
    - 文件命名: `{camera_id}_{YYYYMMDD_HHmmss}_{YYYYMMDD_HHmmss}.{ext}`
    - 计算文件大小和帧数
    - 列出摄像头下的录像文件
    - 删除录像文件（同时删除 SQLite 记录）
    - 检查磁盘可用性（`fsync` + `Statfs`）
    - 磁盘使用率检查
  - 实现存储路径的计算逻辑
  - 实现原子文件写入（write .tmp → rename）
  - 检查存储可用性（HDD 是否挂载）

  **Must NOT do**:
  - 不要在 SD 卡上写入录像数据
  - 不要实现文件压缩或转码
  - 不要使用文件锁（单写入 goroutine 保证安全）

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: 文件系统操作涉及边界条件处理（原子写入、磁盘检查），需要仔细实现
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Tasks 2, 3, 4, 6)
  - **Parallel Group**: Wave 1
  - **Blocks**: Tasks 7, 8, 9, 10, 13, 14, 15
  - **Blocked By**: Task 1

  **References**:
  - Go `os` 包原子写入模式: write temp → rename
  - `syscall.Statfs` 检查磁盘空间
  - `/mnt/data` 挂载点: 确认 HDD 已挂载

  **Acceptance Criteria**:
  - [ ] `internal/storage/manager_test.go` 全部通过
  - [ ] 原子写入测试（断电模拟: 写入 .tmp 不 rename）
  - [ ] 磁盘空间检查测试
  - [ ] 文件命名符合规范
  - [ ] 目录创建和清理功能

  **QA Scenarios:**
  ```
  Scenario: Atomic file write works correctly
    Tool: Bash
    Steps:
      1. Run `go test ./internal/storage/ -run TestAtomicWrite -v`
      2. Assert file appears with final name only after close
      3. Assert .tmp file is cleaned up
    Expected Result: No partial files visible to readers
    Evidence: .sisyphus/evidence/task-5-atomic-write.txt

  Scenario: Disk space check works
    Tool: Bash
    Steps:
      1. Run `go test ./internal/storage/ -run TestDiskSpace -v`
      2. Assert available space is reported correctly
      3. Assert threshold check works
    Expected Result: Correct disk usage percentage
    Evidence: .sisyphus/evidence/task-5-disk-space.txt
  ```

  **Commit**: YES
  - Message: `feat(storage): add file system storage manager`
  - Files: internal/storage/manager.go, internal/storage/manager_test.go

- [x] 6. 认证中间件 (HTTP Basic Auth)

  **What to do**:
  - TDD: 先写中间件的测试
  - 实现 HTTP Basic Auth 中间件（用于 chi router）
  - 从配置文件读取用户名和密码（bcrypt 哈希存储）
  - 保护所有 /api/* 和 WebDAV 路由
  - 登录失败返回 401 + `WWW-Authenticate` 头
  - `/api/health` 端点不需要认证

  **Must NOT do**:
  - 不要实现 JWT 或 OAuth
  - 不要实现多用户或权限管理
  - 不要添加登录页面（由前端处理）

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Tasks 2, 3, 4, 5)
  - **Parallel Group**: Wave 1
  - **Blocks**: Tasks 13, 15
  - **Blocked By**: Task 1

  **References**:
  - chi 中间件模式: `func(next http.Handler) http.Handler`
  - Go `crypto/bcrypt` 标准库

  **Acceptance Criteria**:
  - [ ] 无 Auth 访问 /api/recordings 返回 401
  - [ ] 错误 Auth 返回 401
  - [ ] 正确 Auth 返回 200
  - [ ] /api/health 不需要 Auth

  **QA Scenarios:**
  ```
  Scenario: Auth required for protected routes
    Tool: Bash
    Steps:
      1. Run `go test ./internal/middleware/ -run TestAuth -v`
      2. Assert 401 for no auth, wrong auth
      3. Assert 200 for correct auth
    Expected Result: Auth middleware blocks unauthorized access
    Evidence: .sisyphus/evidence/task-6-auth.txt

  Scenario: Health endpoint is public
    Tool: Bash
    Steps:
      1. Run `go test ./internal/middleware/ -run TestHealthPublic -v`
      2. Assert /api/health returns 200 without auth
    Expected Result: Health check always accessible
    Evidence: .sisyphus/evidence/task-6-health.txt
  ```

  **Commit**: YES
  - Message: `feat(auth): add HTTP Basic Auth middleware`
  - Files: internal/middleware/auth.go, internal/middleware/auth_test.go

- [x] 7. H.264 RTSP 录制管线

  **What to do**:
  - TDD: 先写录制器的测试（mock RTSP server）
  - 使用 `github.com/bluenviron/gortsplib/v5` 作为 RTSP 客户端
  - 实现 `H264Recorder` 结构体:
    - 连接 RTSP 源（支持认证）
    - 读取 H.264 NAL 单元
    - 按配置的分段时长（默认 10min）创建新段文件
    - 通过 `WriteFrame()` 写入存储管理器
    - 段结束时通过 `CloseSegment()` 完成原子写入
  - 实现 RTSP 断线重连（指数退避: 1s→2s→4s→8s→max 60s）
  - 实现摄像头格式变化检测（SPS/PPS 变化时关闭当前段，开新段）
  - 实现 graceful shutdown（通过 context cancellation）
  - 帧缓冲区: 固定大小环形缓冲，满时丢帧并记录日志

  **Must NOT do**:
  - 不要解码或转码视频帧
  - 不要缓冲超过配置的帧数
  - 不要信任摄像头时间戳（使用服务器端时间）

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: RTSP 协议处理 + 分段逻辑 + 重连机制是核心复杂度
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Tasks 8, 9, 10, 11, 12)
  - **Parallel Group**: Wave 2
  - **Blocks**: Tasks 13, 16
  - **Blocked By**: Tasks 2, 3, 4, 5

  **References**:
  - `github.com/bluenviron/gortsplib/v5` RTSP 客户端示例:
    - `examples/client-play/main.go` — 基本连接和帧读取模式
    - `pkg/format/h264.go` — H.264 格式处理（SPS/PPS）
  - gortsplib 的 `ReadFrame()` API 返回原始 RTP 包，需提取 H.264 NAL 单元

  **Acceptance Criteria**:
  - [ ] `internal/recorder/h264_test.go` 全部通过
  - [ ] mock RTSP 源录制 30s → 生成有效 H.264 文件
  - [ ] 分段切换测试（10min 边界）
  - [ ] 重连测试（mock RTSP 断开 + 恢复）
  - [ ] context 取消 → graceful 停止

  **QA Scenarios:**
  ```
  Scenario: H.264 recording produces valid files
    Tool: Bash
    Preconditions: mock RTSP server running
    Steps:
      1. Run `go test ./internal/recorder/ -run TestH264Record -v -timeout 60s`
      2. Assert recording file exists and size > 0
      3. Run `ffprobe -v error -show_entries stream=codec_name file` (if ffprobe available)
    Expected Result: Valid H.264 elementary stream file
    Failure Indicators: Empty file, no codec detected
    Evidence: .sisyphus/evidence/task-7-h264-record.txt

  Scenario: RTSP reconnection with backoff
    Tool: Bash
    Steps:
      1. Run `go test ./internal/recorder/ -run TestH264Reconnect -v`
      2. Assert reconnect attempts logged with increasing delay
    Expected Result: Recorder reconnects after disconnection
    Evidence: .sisyphus/evidence/task-7-h264-reconnect.txt
  ```

  **Commit**: YES
  - Message: `feat(recorder): add H.264 RTSP recording pipeline`
  - Files: internal/recorder/h264.go, internal/recorder/h264_test.go

- [x] 8. MJPEG RTSP 录制管线

  **What to do**:
  - TDD: 先写 MJPEG 录制器测试
  - 使用 `gortsplib/v5` 连接 MJPEG RTSP 源
  - 实现 `MJPEGRecorder` 结构体:
    - 连接 RTSP 源（支持认证）
    - 读取 MJPEG 帧（每帧是完整 JPEG）
    - 按配置的分段时长创建新段目录
    - 每个 JPEG 帧保存为独立文件: `{timestamp_ms}.jpg`
    - 段目录命名: `{camera_id}_{start_time}/`
  - 同样的断线重连和 graceful shutdown 逻辑
  - 帧率控制: 如果帧率过高，可配置采样（每隔 N 帧保存一帧）

  **Must NOT do**:
  - 不要将 MJPEG 转码为 H.264
  - 不要将 JPEG 拼接为视频文件（在服务器端）

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: MJPEG 处理逻辑与 H.264 不同，需要独立管线
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Tasks 7, 9, 10, 11, 12)
  - **Parallel Group**: Wave 2
  - **Blocks**: Tasks 13, 16
  - **Blocked By**: Tasks 2, 3, 4, 5

  **References**:
  - `gortsplib` 的 MJPEG 格式支持
  - JPEG 帧保存: 直接写入 `os.File`，无需编码

  **Acceptance Criteria**:
  - [ ] `internal/recorder/mjpeg_test.go` 全部通过
  - [ ] mock MJPEG RTSP 录制 → 生成 JPEG 文件序列
  - [ ] 每个 JPEG 文件可被 image/jpeg 解码
  - [ ] 分段切换测试（10min 边界）

  **QA Scenarios:**
  ```
  Scenario: MJPEG recording produces valid JPEG sequence
    Tool: Bash
    Steps:
      1. Run `go test ./internal/recorder/ -run TestMJPEGRecord -v -timeout 60s`
      2. Assert JPEG files exist in output directory
      3. Assert each file is valid JPEG (first bytes: FF D8 FF)
    Expected Result: Sequence of valid JPEG files with timestamp names
    Evidence: .sisyphus/evidence/task-8-mjpeg-record.txt

  Scenario: Frame rate sampling works
    Tool: Bash
    Steps:
      1. Run `go test ./internal/recorder/ -run TestMJPEGSampling -v`
      2. Assert only every Nth frame is saved
    Expected Result: Fewer files than frames received
    Evidence: .sisyphus/evidence/task-8-mjpeg-sampling.txt
  ```

  **Commit**: YES
  - Message: `feat(recorder): add MJPEG RTSP recording pipeline`
  - Files: internal/recorder/mjpeg.go, internal/recorder/mjpeg_test.go

- [x] 9. HTTP JPEG 上传接口

  **What to do**:
  - TDD: 先写上传接口的测试
  - 实现 HTTP POST 上传接口:
    - `POST /api/upload/{camera_id}` — 接收单个 JPEG 帧
    - `POST /api/upload/{camera_id}/batch` — 接收多个帧
    - `POST /api/upload/{camera_id}/video` — 接收完整视频文件
  - 功能:
    - 验证 camera_id 存在于配置中
    - 验证 Content-Type（image/jpeg, video/mp4, video/avi 等）
    - 使用服务器端时间戳命名文件
    - 存储到对应摄像头目录
    - 更新 SQLite 元数据
  - 限制上传大小（可配置，默认 100MB）

  **Must NOT do**:
  - 不要验证视频内容（只看 Content-Type）
  - 不要在上传时进行转码

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Tasks 7, 8, 10, 11, 12)
  - **Parallel Group**: Wave 2
  - **Blocks**: Task 13
  - **Blocked By**: Tasks 2, 3, 4, 5

  **References**:
  - chi 路由参数: `chi.URLParam(r, "camera_id")`
  - HTTP 文件上传: `io.Copy` + `io.LimitReader`

  **Acceptance Criteria**:
  - [ ] `internal/upload/handler_test.go` 全部通过
  - [ ] POST JPEG 返回 201 + 文件路径
  - [ ] POST 超大文件返回 413
  - [ ] 未知 camera_id 返回 404
  - [ ] 元数据正确写入 SQLite

  **QA Scenarios:**
  ```
  Scenario: JPEG upload succeeds
    Tool: Bash (curl)
    Steps:
      1. Run `go test ./internal/upload/ -run TestUploadJPEG -v`
      2. Assert file stored in correct directory
      3. Assert SQLite record created
    Expected Result: 201 response with file path
    Evidence: .sisyphus/evidence/task-9-upload-jpeg.txt

  Scenario: Oversized file rejected
    Tool: Bash
    Steps:
      1. Run `go test ./internal/upload/ -run TestUploadOversized -v`
      2. Assert 413 status for file exceeding limit
    Expected Result: Request rejected with clear error
    Evidence: .sisyphus/evidence/task-9-upload-oversize.txt
  ```

  **Commit**: YES
  - Message: `feat(upload): add HTTP JPEG upload endpoint`
  - Files: internal/upload/handler.go, internal/upload/handler_test.go

- [x] 10. FTP 服务器

  **What to do**:
  - TDD: 先写 FTP 服务器的测试
  - 使用 `github.com/fclairamb/ftpserverlib` 实现 FTP 服务器
  - 实现核心功能:
    - 监听配置的端口（默认 2121）
    - 被动模式支持（配置端口范围）
    - 用户认证（复用配置中的 Auth 用户名密码）
    - 文件上传: PUT/STOR 到摄像头目录
    - 目录列表: LIST 对应摄像头目录
    - 隐式将上传文件关联到配置中的摄像头
  - 上传文件自动命名: `{camera_id}_{server_timestamp}.{ext}`
  - FTP 上传的文件也记录到 SQLite 元数据

  **Must NOT do**:
  - 不要实现 FTPS/FTPE (v1 只需明文 FTP)
  - 不要实现 FXP 传输
  - 不要实现 FTP 断点续传
  - 不要通过 Caddy 反代 FTP（FTP 需独立端口）

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Tasks 7, 8, 9, 11, 12)
  - **Parallel Group**: Wave 2
  - **Blocks**: None (独立协议)
  - **Blocked By**: Tasks 2, 3, 4, 5

  **References**:
  - `github.com/fclairamb/ftpserverlib` — 查看 example 目录的 FTP 服务器实现
  - 被动模式端口范围配置是 NAT/防火墙穿透必需

  **Acceptance Criteria**:
  - [ ] `internal/ftp/server_test.go` 全部通过
  - [ ] FTP 客户端可连接、认证、上传文件
  - [ ] 上传的文件出现在正确目录
  - [ ] 元数据写入 SQLite

  **QA Scenarios:**
  ```
  Scenario: FTP upload works end-to-end
    Tool: Bash
    Steps:
      1. Run `go test ./internal/ftp/ -run TestFTPUpload -v`
      2. Use test FTP client to upload a file
      3. Assert file exists in storage directory
      4. Assert SQLite metadata created
    Expected Result: File stored and indexed correctly
    Evidence: .sisyphus/evidence/task-10-ftp-upload.txt

  Scenario: FTP auth required
    Tool: Bash
    Steps:
      1. Run `go test ./internal/ftp/ -run TestFTPAuth -v`
      2. Assert anonymous login rejected
      3. Assert wrong password rejected
    Expected Result: Only correct credentials accepted
    Evidence: .sisyphus/evidence/task-10-ftp-auth.txt
  ```

  **Commit**: YES
  - Message: `feat(ftp): add FTP server for camera uploads`
  - Files: internal/ftp/server.go, internal/ftp/server_test.go

- [x] 11. MQTT 事件触发

  **What to do**:
  - TDD: 先写 MQTT 订阅和事件处理的测试
  - 使用 `github.com/eclipse/paho.mqtt.golang` 实现 MQTT 客户端
  - 功能:
    - 连接到配置的 MQTT broker
    - 订阅配置的主题 (如 `camvault/trigger/{camera_id}`)
    - 接收 JSON 消息: `{"action": "start"}` 或 `{"action": "stop"}`
    - 触发对应摄像头的录制启动/停止
    - MQTT 仅作为事件信号，不传输视频数据
  - 实现 MQTT 断线重连
  - 如果 MQTT 未配置，此模块不启动（可选功能）

  **Must NOT do**:
  - 不要通过 MQTT 传输视频数据
  - 不要实现 MQTT broker（只是客户端）
  - 不要实现复杂的消息路由

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Tasks 7, 8, 9, 10, 12)
  - **Parallel Group**: Wave 2
  - **Blocks**: Task 16
  - **Blocked By**: Tasks 2, 3, 4

  **References**:
  - `github.com/eclipse/paho.mqtt.golang` 标准用法
  - MQTT 仅作信号: publish/subscribe 模式

  **Acceptance Criteria**:
  - [ ] `internal/mqtt/client_test.go` 全部通过
  - [ ] MQTT start 消息触发录制启动
  - [ ] MQTT stop 消息触发录制停止
  - [ ] MQTT 断线重连测试
  - [ ] 未配置 MQTT 时模块不启动

  **QA Scenarios:**
  ```
  Scenario: MQTT trigger starts recording
    Tool: Bash
    Steps:
      1. Run `go test ./internal/mqtt/ -run TestTriggerStart -v`
      2. Publish MQTT message {"action":"start"}
      3. Assert recording started callback invoked
    Expected Result: Recording pipeline activated
    Evidence: .sisyphus/evidence/task-11-mqtt-start.txt

  Scenario: MQTT disabled by default
    Tool: Bash
    Steps:
      1. Run `go test ./internal/mqtt/ -run TestDisabled -v`
      2. Assert no connection attempted when MQTT config empty
    Expected Result: Module stays idle when not configured
    Evidence: .sisyphus/evidence/task-11-mqtt-disabled.txt
  ```

  **Commit**: YES
  - Message: `feat(mqtt): add MQTT event trigger subscriber`
  - Files: internal/mqtt/client.go, internal/mqtt/client_test.go

- [x] 12. MP4 封装器 (abema/go-mp4)

  **What to do**:
  - TDD: 先写 MP4 封装器的测试
  - 使用 `github.com/abema/go-mp4` 实现纯 Go MP4 封装
  - 实现 `MP4Muxer` 结构体:
    - 创建新 MP4 文件
    - 添加 H.264 视频轨道（使用 SPS/PPS 参数）
    - 写入 H.264 NAL 单元为 MP4 sample
    - 设置时间戳和持续时间
    - 正确关闭文件（写入 moov atom）
  - 关键: 必须正确处理 MP4 ftyp + moov + mdat 结构
  - 支持从 gortsplib 的 H.264 RTP payload 提取 NAL 单元

  **Must NOT do**:
  - 不要解码/重编码视频
  - 不要添加音频轨道（ESP32 摄像头通常无音频）
  - 不要使用 ffmpeg（纯 Go 实现）
  - 不要实现 MP4 播放功能（由浏览器处理）

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: MP4 容器格式复杂，需要理解 ftyp/moov/mdat atom 结构
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Tasks 7, 8, 9, 10, 11)
  - **Parallel Group**: Wave 2
  - **Blocks**: Task 13
  - **Blocked By**: Task 3

  **References**:
  - `github.com/abema/go-mp4` — 生产级 MP4 读写库
    - 查看 `mp4.CreateMuxer()` API
    - 查看 `mp4.CreateTrack()` 和 sample 写入模式
  - RTSPtoWeb (github.com/AlexxIT/rtsp-to-web) 使用此库进行 MP4 封装，可参考其实现
  - MP4 atom 结构: ftyp → moov (metadata) → mdat (media data)

  **Acceptance Criteria**:
  - [ ] `internal/muxer/mp4mux_test.go` 全部通过
  - [ ] H.264 NAL 单元封装为有效 MP4
  - [ ] 生成的 MP4 可被 `ffprobe` 正确解析（如已安装）
  - [ ] MP4 持续时间正确
  - [ ] 文件正确关闭（moov atom 完整）

  **QA Scenarios:**
  ```
  Scenario: MP4 muxing produces valid file
    Tool: Bash
    Steps:
      1. Run `go test ./internal/muxer/ -run TestMP4Mux -v`
      2. Assert output file starts with MP4 ftyp atom (bytes: 00 00 00 20 66 74 79 70)
      3. Assert file size > 0
    Expected Result: Valid MP4 container file
    Evidence: .sisyphus/evidence/task-12-mp4mux.txt

  Scenario: MP4 with H.264 samples is playable
    Tool: Bash
    Steps:
      1. Run `go test ./internal/muxer/ -run TestMP4Playback -v`
      2. If ffprobe available, verify codec_name=h264 and duration correct
    Expected Result: Standard players can decode the file
    Evidence: .sisyphus/evidence/task-12-mp4-playback.txt
  ```

  **Commit**: YES
  - Message: `feat(muxer): add MP4 muxer using abema/go-mp4`
  - Files: internal/muxer/mp4mux.go, internal/muxer/mp4mux_test.go

- [x] 13. REST API — 录像管理

  **What to do**:
  - TDD: 先写 API handler 的测试
  - 使用 chi router 实现 REST API:
    - `GET /api/health` — 健康检查（公开）
    - `POST /api/auth/login` — 登录验证
    - `GET /api/recordings` — 列表查询 (分页、过滤: camera_id, 时间范围, 格式, pinned)
    - `GET /api/recordings/{id}` — 获取单个录像详情
    - `DELETE /api/recordings/{id}` — 删除录像 (文件+元数据)
    - `POST /api/recordings/{id}/pin` — 置顶保护
    - `POST /api/recordings/{id}/unpin` — 取消置顶
    - `GET /api/recordings/{id}/download` — 下载录像文件
    - `GET /api/recordings/{id}/play` — 获取回放 URL
    - `GET /api/cameras` — 列出摄像头及状态
    - `GET /api/stats` — 存储统计 (总容量/已用/录像数)
  - 所有接口返回 JSON
  - 使用 chi middleware 嵌入认证中间件

  **Must NOT do**:
  - 不要实现摄像头配置的增删改 API (YAML 配置)
  - 不要实现 WebSocket 推送
  - 不要添加分页游标（v1 用 offset/limit）

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Tasks 14, 15, 16)
  - **Parallel Group**: Wave 3
  - **Blocks**: Task 17
  - **Blocked By**: Tasks 4, 5, 6, 7, 8, 9, 12

  **References**:
  - chi v5 路由模式: `chi.URLParam()`, `chi.Walk()`, middleware chain
  - JSON 响应: `json.NewEncoder(w).Encode(data)`
  - Content-Type: `application/json`

  **Acceptance Criteria**:
  - [ ] `internal/api/handler_test.go` 全部通过
  - [ ] 所有 CRUD 端点测试通过
  - [ ] 认证正确应用于所有受保护端点
  - [ ] JSON 响应格式正确
  - [ ] 分页和过滤功能正确

  **QA Scenarios:**
  ```
  Scenario: List recordings API
    Tool: Bash (curl)
    Steps:
      1. Run `go test ./internal/api/ -run TestListRecordings -v`
      2. Assert JSON array returned with correct fields
      3. Assert pagination works (offset, limit)
    Expected Result: Paginated recording list with metadata
    Evidence: .sisyphus/evidence/task-13-api-list.txt

  Scenario: Pin/unpin recording
    Tool: Bash
    Steps:
      1. Run `go test ./internal/api/ -run TestPinUnpin -v`
      2. Assert pin toggles correctly in response and database
    Expected Result: Pin status updated and persisted
    Evidence: .sisyphus/evidence/task-13-api-pin.txt
  ```

  **Commit**: YES
  - Message: `feat(api): add REST API for recording management`
  - Files: internal/api/*.go

- [x] 14. 自动清理策略

  **What to do**:
  - TDD: 先写清理策略的测试
  - 实现 `CleanupManager`:
    - 定时检查（配置间隔，默认 1h）
    - 按保留天数删除未置顶的录像（默认 30 天）
    - 磁盘使用率检查: 超过阈值（默认 95%）时强制清理最旧录像
    - 置顶录像永远不被自动删除
    - 清理时同时删除文件和 SQLite 记录
    - 记录清理日志（删除了哪些文件，释放了多少空间）
  - 清理时先删除 SQLite 记录，再删除文件（防止有记录无文件）

  **Must NOT do**:
  - 不要实现智能保留策略（保留更多动态区域的录像）
  - 不要实现压缩或转码来节省空间
  - 不要删除正在写入的段文件

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Tasks 13, 15, 16)
  - **Parallel Group**: Wave 3
  - **Blocks**: Task 18
  - **Blocked By**: Tasks 4, 5

  **References**:
  - 混合清理策略: 时间 + 置顶保护 + 磁盘阈值

  **Acceptance Criteria**:
  - [ ] `internal/cleanup/cleanup_test.go` 全部通过
  - [ ] 超过保留期的未置顶录像被删除
  - [ ] 超过保留期的置顶录像不被删除
  - [ ] 磁盘阈值清理正确工作
  - [ ] 正在写入的段不被清理

  **QA Scenarios:**
  ```
  Scenario: Time-based cleanup respects pin
    Tool: Bash
    Steps:
      1. Run `go test ./internal/cleanup/ -run TestTimeCleanup -v`
      2. Create old recordings (some pinned, some not)
      3. Run cleanup with 1-day retention
      4. Assert unpinned old recordings deleted, pinned kept
    Expected Result: Only unpinned+old recordings removed
    Evidence: .sisyphus/evidence/task-14-cleanup-time.txt

  Scenario: Disk threshold cleanup
    Tool: Bash
    Steps:
      1. Run `go test ./internal/cleanup/ -run TestDiskThreshold -v`
      2. Simulate high disk usage
      3. Assert oldest unpinned recordings deleted first
    Expected Result: Oldest recordings cleaned to free space
    Evidence: .sisyphus/evidence/task-14-cleanup-disk.txt
  ```

  **Commit**: YES
  - Message: `feat(cleanup): add auto-cleanup with pin protection`
  - Files: internal/cleanup/*.go

- [x] 15. WebDAV 只读服务器

  **What to do**:
  - TDD: 先写 WebDAV 服务器的测试
  - 使用 `golang.org/x/net/webdav` 实现 WebDAV 服务器
  - 实现 `WebDAVHandler`:
    - 挂载路径: `/dav/` (可配置)
    - 根目录指向 `/mnt/data/nvr/`
    - 只读模式: 只支持 PROPFIND, GET, HEAD, OPTIONS
    - 不支持 PUT, DELETE, MKCOL, COPY, MOVE, LOCK, UNLOCK
  - 嵌入认证中间件
  - 目录浏览: 显示摄像头目录结构和录像文件

  **Must NOT do**:
  - 不要实现写入操作（PUT, DELETE, MOVE 等）
  - 不要实现文件锁定 (LOCK/UNLOCK)
  - 不要修改文件系统上的录像文件

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Tasks 13, 14, 16)
  - **Parallel Group**: Wave 3
  - **Blocks**: Task 17
  - **Blocked By**: Tasks 5, 6

  **References**:
  - `golang.org/x/net/webdav` — Go 标准库的 WebDAV 实现
  - 只读 WebDAV: 通过自定义 `LockSystem` 禁止写入操作

  **Acceptance Criteria**:
  - [ ] `internal/webdav/server_test.go` 全部通过
  - [ ] PROPFIND 返回目录列表
  - [ ] GET 下载文件成功
  - [ ] PUT 返回 403 (禁止写入)
  - [ ] DELETE 返回 403
  - [ ] 认证正确应用

  **QA Scenarios:**
  ```
  Scenario: WebDAV read-only access
    Tool: Bash (curl)
    Steps:
      1. Run `go test ./internal/webdav/ -run TestReadAccess -v`
      2. PROPFIND /dav/ → assert directory listing
      3. GET /dav/cam1/file.mp4 → assert file content
    Expected Result: Files accessible via WebDAV
    Evidence: .sisyphus/evidence/task-15-webdav-read.txt

  Scenario: WebDAV write operations blocked
    Tool: Bash
    Steps:
      1. Run `go test ./internal/webdav/ -run TestWriteBlocked -v`
      2. PUT /dav/test.txt → assert 403
      3. DELETE /dav/cam1/file.mp4 → assert 403
    Expected Result: All write operations rejected
    Evidence: .sisyphus/evidence/task-15-webdav-write.txt
  ```

  **Commit**: YES
  - Message: `feat(webdav): add read-only WebDAV server`
  - Files: internal/webdav/server.go, internal/webdav/server_test.go

- [x] 16. 摄像头管理器 (编排所有录制管线)

  **What to do**:
  - TDD: 先写管理器的测试
  - 实现 `CameraManager` 结构体:
    - 读取配置中的摄像头列表
    - 根据每个摄像头的协议类型创建对应的 Recorder
    - 管理所有录制管线的生命周期（启动/停止/重启）
    - 提供 Status() 查询所有摄像头状态
    - 响应 MQTT 触发事件
    - 监控每个录制管线的健康状态
    - 异常时自动重启（复用重连逻辑）
  - 实现 graceful shutdown:
    - 接收 SIGTERM/SIGINT
    - 停止所有录制管线
    - 关闭所有打开的段文件
    - 等待所有写入完成
    - 退出
  - 连接 main.go 中的启动逻辑

  **Must NOT do**:
  - 不要实现动态添加/删除摄像头（v1 通过配置文件）
  - 不要实现负载均衡

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 编排多个录制管线是系统核心，涉及并发和生命周期管理
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Tasks 13, 14, 15)
  - **Parallel Group**: Wave 3
  - **Blocks**: Task 18
  - **Blocked By**: Tasks 7, 8, 11

  **References**:
  - Go context 和 goroutine 生命周期管理模式
  - `signal.NotifyContext()` 处理 SIGTERM/SIGINT
  - `errgroup.Group` 管理多个 goroutine

  **Acceptance Criteria**:
  - [ ] `internal/camera/manager_test.go` 全部通过
  - [ ] 配置加载后所有摄像头启动
  - [ ] 单个摄像头失败不影响其他摄像头
  - [ ] graceful shutdown 测试
  - [ ] 状态查询返回正确信息

  **QA Scenarios:**
  ```
  Scenario: Camera manager starts all cameras
    Tool: Bash
    Steps:
      1. Run `go test ./internal/camera/ -run TestStartAll -v`
      2. Assert all configured cameras have status=recording
    Expected Result: All cameras recording simultaneously
    Evidence: .sisyphus/evidence/task-16-manager-start.txt

  Scenario: Graceful shutdown completes cleanly
    Tool: Bash
    Steps:
      1. Run `go test ./internal/camera/ -run TestGracefulShutdown -v`
      2. Send cancellation signal
      3. Assert all segments closed, all goroutines stopped
    Expected Result: No orphan goroutines or open files
    Evidence: .sisyphus/evidence/task-16-manager-shutdown.txt
  ```

  **Commit**: YES
  - Message: `feat(camera): add camera manager orchestration`
  - Files: internal/camera/manager.go, internal/camera/manager_test.go

- [x] 17. Web UI 前端 (Svelte, embedded)

  **What to do**:
  - 创建 Svelte 前端项目在 `web/` 目录
  - 实现 Web UI 页面:
    - **登录页**: 简单用户名密码表单
    - **录像列表页**: 卡片/表格视图，支持过滤（摄像头、日期范围、格式、置顶状态）
    - **录像详情/回放页**: MP4 使用 `<video>` 原生播放，JPEG 序列使用 JavaScript 幻灯片播放
    - **置顶/取消置顶**: 按钮操作 + 确认对话框
    - **删除录像**: 确认对话框 + 删除
    - **存储统计页**: 总容量、已用、录像数量、摄像头状态概览
  - 构建为静态文件，复制到 `web_embed/` 目录
  - 使用 `go:embed` 嵌入到二进制文件
  - chi 路由: `/` 和 `/ui/*` 服务嵌入的前端
  - API 请求代理到 `/api/*`
  - UI 设计要求:
    - 响应式布局（支持手机浏览）
    - 深色主题（NVR 系统常用）
    - 轻量级: 不使用大型 UI 库，用 Tailwind CSS

  **Must NOT do**:
  - 不要实现摄像头配置页面
  - 不要实现用户管理 UI
  - 不要实现实时预览/直播
  - 不要使用 heavy 框架 (Angular, React 全家桶)
  - 不要添加图表库 (Chart.js, D3 等)

  **Recommended Agent Profile**:
  - **Category**: `visual-engineering`
    - Reason: 前端 UI/UX 开发，需要视觉设计和交互实现
  - **Skills**: [`/frontend-ui-ux`]

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Task 18)
  - **Parallel Group**: Wave 4
  - **Blocks**: Task 19
  - **Blocked By**: Tasks 13, 15

  **References**:
  - Svelte + Vite 构建: `npm create vite@latest web -- --template svelte`
  - Tailwind CSS + Svelte 集成
  - Go embed: `//go:embed web_embed/*`
  - chi 静态文件服务: `http.FileServer(http.FS(embed.FS))`

  **Acceptance Criteria**:
  - [ ] `npm run build` 在 `web/` 成功
  - [ ] 构建产物在 `web_embed/` 目录
  - [ ] `go build ./cmd/camvault/` 包含嵌入的前端
  - [ ] 访问 `/` 显示登录页
  - [ ] 登录后显示录像列表
  - [ ] 录像可回放（MP4 用 <video>, JPEG 用幻灯片）
  - [ ] 置顶/删除操作正常

  **QA Scenarios:**
  ```
  Scenario: Web UI loads and functions
    Tool: Bash (curl)
    Steps:
      1. Run `npm run build` in `web/`
      2. Run `go build ./cmd/camvault/`
      3. Start camvault, curl http://localhost:9090/
      4. Assert HTML page returned (contains login form)
    Expected Result: Web UI served from embedded files
    Evidence: .sisyphus/evidence/task-17-webui-load.txt

  Scenario: Recording playback works
    Tool: Bash (curl)
    Steps:
      1. With test recordings in storage
      2. GET /api/recordings → assert recording listed
      3. GET /api/recordings/{id}/play → assert URL or stream returned
    Expected Result: Recordings accessible for playback
    Evidence: .sisyphus/evidence/task-17-webui-playback.txt
  ```

  **Commit**: YES
  - Message: `feat(ui): add embedded Web UI (Svelte)`
  - Files: web/**, web_embed/**

- [x] 18. 集成测试 + 崩溃恢复

  **What to do**:
  - TDD: 编写端到端集成测试
  - 集成测试场景:
    - 完整流程: 配置加载 → 摄像头启动 → 录制 → 段切换 → API 查询 → 清理
    - 多摄像头并发录制
    - HTTP 上传 + API 查询
  - 崩溃恢复测试:
    - 模拟进程崩溃: 段文件写入中途被 kill
    - 重启后: 清理未完成段，已完成段完好
    - SQLite WAL 文件恢复
  - 存储可用性测试:
    - 模拟存储不可用 (unmount)
    - 录制停止并记录日志
    - 存储恢复后自动恢复录制
  - 长时间运行测试 (可选):
    - 运行 30 分钟，监控内存不增长超过 20%

  **Must NOT do**:
  - 不要设置 CI/CD pipeline
  - 不要做性能压测（资源有限）

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: 集成测试涉及多个模块协作，崩溃恢复需要仔细模拟边界条件
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: YES (with Task 17)
  - **Parallel Group**: Wave 4
  - **Blocks**: Task 19
  - **Blocked By**: Tasks 16, 14

  **References**:
  - Go testing: `testing.T`, `TestMain` for setup/teardown
  - 崩溃恢复: 模拟 kill -9 (写临时文件后不 close)

  **Acceptance Criteria**:
  - [ ] `tests/integration_test.go` 全部通过
  - [ ] 崩溃恢复测试: 未完成段被清理，已完成段完好
  - [ ] 多摄像头并发测试通过
  - [ ] 存储不可用测试通过

  **QA Scenarios:**
  ```
  Scenario: Crash recovery preserves data
    Tool: Bash
    Steps:
      1. Run `go test ./tests/ -run TestCrashRecovery -v -timeout 120s`
      2. Assert completed segments remain after restart
      3. Assert incomplete segments are cleaned up
    Expected Result: No data loss for completed segments
    Evidence: .sisyphus/evidence/task-18-crash-recovery.txt

  Scenario: Multi-camera concurrent recording
    Tool: Bash
    Steps:
      1. Run `go test ./tests/ -run TestMultiCamera -v -timeout 120s`
      2. Assert all cameras record simultaneously
      3. Assert no file corruption
    Expected Result: All recordings valid and indexed
    Evidence: .sisyphus/evidence/task-18-multi-camera.txt
  ```

  **Commit**: YES
  - Message: `test: add integration tests and crash recovery`
  - Files: tests/*.go

- [x] 19. 部署配置 (systemd + Caddy + 文档)

  **What to do**:
  - 创建 systemd 服务文件:
    ```ini
    [Unit]
    Description=CamVault NVR
    After=network.target
    
    [Service]
    Type=simple
    User=mickey
    ExecStart=/mnt/data/nvr/bin/camvault -config /mnt/data/nvr/camvault.yaml
    WorkingDirectory=/mnt/data/nvr
    Restart=on-failure
    RestartSec=10
    
    [Install]
    WantedBy=multi-user.target
    ```
  - 创建 Caddy 反代配置片段:
    ```
    # CamVault NVR - 端口 9090
    :9090 {
        reverse_proxy localhost:9090
    }
    ```
    注意: FTP (端口 2121) 不通过 Caddy 反代
  - 更新 Makefile 添加 deploy 目标:
    - `make install`: 编译 + 复制到 `/mnt/data/nvr/bin/`
    - `make install-service`: 安装 systemd 服务
    - `make uninstall-service`: 卸载服务
  - 创建 `config.example.yaml` 完整配置示例（含所有摄像头类型）
  - 更新 `cmd/camvault/main.go` 完整启动逻辑（连接所有组件）

  **Must NOT do**:
  - 不要自动修改 Caddy 配置（只提供示例片段）
  - 不要实现 Docker 部署 (RPi3B 跑 Docker 太重)
  - 不要创建 README 文档（除非用户要求）

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: []

  **Parallelization**:
  - **Can Run In Parallel**: NO (Wave 4 最后一个任务)
  - **Parallel Group**: Wave 4 (sequential after 17, 18)
  - **Blocks**: F1-F4
  - **Blocked By**: Tasks 17, 18

  **References**:
  - 树莓派 systemd 服务配置标准模式
  - Caddy reverse_proxy 指令
  - `/mnt/data` 挂载点路径

  **Acceptance Criteria**:
  - [ ] `deploy/camvault.service` 存在且格式正确
  - [ ] `deploy/Caddyfile.example` 包含反代配置
  - [ ] `make install` 编译并复制二进制
  - [ ] `config.example.yaml` 包含所有字段
  - [ ] `cmd/camvault/main.go` 完整启动所有组件
  - [ ] `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o camvault .` 成功

  **QA Scenarios:**
  ```
  Scenario: Full build and deploy
    Tool: Bash
    Steps:
      1. Run `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o camvault .`
      2. Assert binary exists and size > 0
      3. Assert `file camvault` shows ELF 64-bit LSB executable, ARM aarch64
    Expected Result: ARM64 static binary produced
    Evidence: .sisyphus/evidence/task-19-deploy.txt

  Scenario: Config example loads correctly
    Tool: Bash
    Steps:
      1. Run `go test ./internal/config/ -run TestLoadExample -v`
      2. Load config.example.yaml and validate
    Expected Result: Example config passes all validation
    Evidence: .sisyphus/evidence/task-19-config.txt
  ```

  **Commit**: YES
  - Message: `feat(deploy): add systemd service and Caddy config`
  - Files: deploy/*.service, deploy/Caddyfile.example, cmd/camvault/main.go, Makefile

## Final Verification Wave (MANDATORY — after ALL implementation tasks)

> 4 review agents run in PARALLEL. ALL must APPROVE. Present consolidated results to user and get explicit "okay" before completing.

- [x] F1. **Plan Compliance Audit** — `oracle`
  Read the plan end-to-end. For each "Must Have": verify implementation exists (read file, curl endpoint, run command). For each "Must NOT Have": search codebase for forbidden patterns — reject with file:line if found. Check evidence files exist in .sisyphus/evidence/. Compare deliverables against plan.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [x] F2. **Code Quality Review** — `unspecified-high`
  Run `go vet ./...` + `staticcheck ./...` + `go test ./...`. Review all changed files for: `interface{}` (use `any`), empty catches, `fmt.Println` in prod, commented-out code, unused imports. Check AI slop: excessive comments, over-abstraction, generic names (data/result/item/temp). Verify `CGO_ENABLED=0` build succeeds.
  Output: `Build [PASS/FAIL] | Lint [PASS/FAIL] | Tests [N pass/N fail] | Files [N clean/N issues] | VERDICT`

- [x] F3. **Real Manual QA** — `unspecified-high`
  Start from clean state. Execute EVERY QA scenario from EVERY task — follow exact steps, capture evidence. Test cross-task integration: RTSP recording + Web UI listing + WebDAV access + cleanup policy. Test edge cases: disk full, camera disconnect, format change. Save to `.sisyphus/evidence/final-qa/`.
  Output: `Scenarios [N/N pass] | Integration [N/N] | Edge Cases [N tested] | VERDICT`

- [x] F4. **Scope Fidelity Check** — `deep`
  For each task: read "What to do", read actual diff (git log/diff). Verify 1:1 — everything in spec was built (no missing), nothing beyond spec was built (no creep). Check "Must NOT do" compliance. Detect cross-task contamination. Flag unaccounted changes.
  Output: `Tasks [N/N compliant] | Contamination [CLEAN/N issues] | Unaccounted [CLEAN/N files] | VERDICT`

---

## Commit Strategy

| After Task | Commit Message | Key Files |
|-----------|---------------|-----------|
| 1 | `feat: initialize Go project with build system` | go.mod, Makefile, .goreleaser.yml |
| 2 | `feat(config): add YAML config loading and validation` | internal/config/ |
| 3 | `feat(types): define core types and interfaces` | internal/types/ |
| 4 | `feat(storage): add SQLite schema and migration` | internal/storage/ |
| 5 | `feat(storage): add file system storage manager` | internal/storage/ |
| 6 | `feat(auth): add HTTP Basic Auth middleware` | internal/middleware/ |
| 7 | `feat(recorder): add H.264 RTSP recording pipeline` | internal/recorder/ |
| 8 | `feat(recorder): add MJPEG RTSP recording pipeline` | internal/recorder/ |
| 12 | `feat(muxer): add MP4 muxer using abema/go-mp4` | internal/muxer/ |
| 9 | `feat(upload): add HTTP JPEG upload endpoint` | internal/upload/ |
| 10 | `feat(ftp): add FTP server for camera uploads` | internal/ftp/ |
| 11 | `feat(mqtt): add MQTT event trigger subscriber` | internal/mqtt/ |
| 13 | `feat(api): add REST API for recording management` | internal/api/ |
| 14 | `feat(cleanup): add auto-cleanup with pin protection` | internal/cleanup/ |
| 15 | `feat(webdav): add read-only WebDAV server` | internal/webdav/ |
| 16 | `feat(camera): add camera manager orchestration` | internal/camera/ |
| 17 | `feat(ui): add embedded Web UI (Svelte)` | web/ |
| 18 | `test: add integration tests and crash recovery` | tests/ |
| 19 | `feat(deploy): add systemd service and Caddy config` | deploy/ |

---

## Success Criteria

### Verification Commands
```bash
# Build (must succeed with zero CGO)
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o camvault .

# All tests pass
go test ./... -v -count=1

# Memory check (RSS < 300MB after 10min recording)
ps -o rss= -p $(pgrep camvault)

# Video validation
ffprobe -v error -show_entries stream=codec_name -of csv=p=0 /mnt/data/nvr/cam1/segment_001.mp4
# Expected: h264

# API health
curl -s http://localhost:9090/api/health
# Expected: {"status":"ok"}

# Auth required
curl -s -o /dev/null -w "%{http_code}" http://localhost:9090/api/recordings
# Expected: 401

# Auth works
curl -s -u admin:password http://localhost:9090/api/recordings | jq .
# Expected: array of recording objects
```

### Final Checklist
- [x] All "Must Have" present
- [x] All "Must NOT Have" absent
- [x] All tests pass (`go test ./...`)
- [x] Binary cross-compiles with `CGO_ENABLED=0`
- [ ] Memory usage ≤300MB under 5-camera load
- [ ] Recorded MP4 files valid per ffprobe
- [ ] Web UI renders and functions
- [ ] WebDAV provides read-only file access
- [ ] FTP accepts camera uploads
- [ ] Auto-cleanup respects pin protection
