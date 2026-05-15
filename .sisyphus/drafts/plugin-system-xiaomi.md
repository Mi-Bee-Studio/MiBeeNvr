# Draft: Plugin System + Xiaomi Camera Support

## Requirements (confirmed)
- 实现 Go 编译时插件接口体系（接口注册 + 构建标签）
- 从 go2rtc 复制 pkg/xiaomi 代码适配为 NVR 插件
- 前端：小米账号登录 → 设备发现 → 一键添加摄像头
- 在独立分支开发
- 包含文档更新、部署测试

## Technical Decisions
- 方案：接口注册 + 构建标签（编译时插件），不使用 Go plugin (.so)
- CGO_ENABLED=0 保持不变
- 从 go2rtc MIT 代码复制适配：crypto.go 零改动，cs2/conn.go 小改，miss/producer.go 重写
- 零新增依赖（golang.org/x/crypto + pion/rtp 已有）
- 构建标签：`//go:build xiaomi`

## Scope Boundaries
- INCLUDE: 插件接口定义、CameraManager 改造、小米插件实现、前端账号登录+设备发现+一键添加、构建标签、文档、部署测试
- EXCLUDE: 不做运行时动态加载、不做子进程插件协议、不做 legacy 旧型号（初期）、不做 Wyze/Tuya 等其他品牌

## Research Findings
- go2rtc pkg/xiaomi 代码：2,630 行，MIT 协议
- 小米协议：MISS (Mi Secure Streaming) + CS2 P2P 传输 + ChaCha20 加密
- 需要小米云 API 获取加密密钥（每次连接需联网）
- URL 格式：xiaomi://userID:region@ip?did=deviceID&model=modelName
- 设备发现 API：/v2/home/device_list_page
- 支持 H264/H265 + OPUS/PCMA

## Open Questions
- (none - all cleared)

## Test Strategy Decision
- Infrastructure exists: YES (Go testify + Playwright E2E)
- Automated tests: YES (TDD - tests first for plugin interface, integration tests for xiaomi recorder)
- Agent-Executed QA: ALWAYS
