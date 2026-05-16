# 插件开发指南
## 概述

MiBee NVR 插件系统允许开发者通过自定义相机协议和录制机制来扩展录制器。插件是自包含的 Go 模块，实现 `RecorderPlugin` 接口，支持新的相机协议，如小米云相机、物联网设备或专有流媒体协议。

插件系统自动与现有的相机管理系统、配置系统和 Web API 集成，无缝扩展 NVR 功能。

## RecorderPlugin 接口

所有插件必须实现的核心接口：

```go
type RecorderPlugin interface {
    // Name 返回插件唯一标识符（例如 "xiaomi"）。
    Name() string
    
    // Protocols 返回此插件处理的传输协议（例如 ["xiaomi"]）。
    Protocols() []string
    
    // NewRecorder 为给定的相机配置创建一个新的录制器。
    NewRecorder(cfg config.CameraConfig, store *storage.Manager, db *storage.DB, opts ...*metrics.Metrics) model.Recorder
    
    // RegisterRoutes 向路由器添加插件特定的 HTTP 路由。
    RegisterRoutes(r chi.Router)
    
    // ConfigSchema 返回用于文档/验证的示例配置结构体。
    ConfigSchema() interface{}
}
```

### 接口方法

#### `Name() string`

返回插件的唯一标识符。这应该是一个小写字符串，与相机配置中使用的协议名称匹配。

#### `Protocols() []string`

返回此插件可以处理的协议字符串列表。这些是用户在相机配置中指定的协议值。

#### `NewRecorder(cfg config.CameraConfig, store *storage.Manager, db *storage.DB, opts ...*metrics.Metrics) model.Recorder`

为给定的相机配置创建一个新的录制器实例。这是您实现自定义录制逻辑的地方。

#### `RegisterRoutes(r chi.Router)`

可选方法，用于添加插件特定的 HTTP 端点。大多数插件不需要这个，除非它们提供额外的 API 端点。

#### `ConfigSchema() interface{}`

返回用于使用此插件的相机可用额外字段的示例配置结构体。

## 快速开始

这是一个最小的插件实现：

```go
package myplugin

import (
    "github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
    "github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
    "github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
    "github.com/Mi-Bee-Studio/MiBeeNvr/internal/plugin"
    "github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
    "github.com/go-chi/chi/v5"
)

type MyPlugin struct{}

func init() {
    plugin.Register(&MyPlugin{})
}

func (p *MyPlugin) Name() string { return "my-plugin" }
func (p *MyPlugin) Protocols() []string { return []string{"my-protocol"} }

func (p *MyPlugin) NewRecorder(cfg config.CameraConfig, store *storage.Manager, db *storage.DB, opts ...*metrics.Metrics) model.Recorder {
    // 创建并返回您的录制器
    return &MyRecorder{
        cameraID: cfg.ID,
        url:      cfg.URL,
        // ... 来自配置的其他字段
    }
}

func (p *MyPlugin) RegisterRoutes(r chi.Router) {
    // 可选：添加自定义 API 路由
    r.Get("/api/myplugin/status", p.handleStatus)
}

func (p *MyPlugin) ConfigSchema() interface{} {
    // 可选：返回示例配置结构体
    return nil
}
```

## 注册

插件使用 Go 的 `init()` 函数和空白导入自动注册：

1. **包导入**：将 `import _ "github.com/Mi-Bee-Studio/MiBeeNvr/plugins/your-plugin-name"` 添加到 `cmd/mibee-nvr/main.go`

2. **插件注册**：在插件的 `init()` 函数中，调用 `plugin.Register(&YourPlugin{})`

插件系统使用在应用程序启动期间填充的全局注册表。插件按照导入的顺序加载。

## CameraConfig 字段

插件可以访问标准的 `config.CameraConfig` 字段：

```go
type CameraConfig struct {
    ID       string `yaml:"id"`                                    // 唯一相机标识符
    Name     string `yaml:"name"`                                  // 显示名称
    Protocol string `yaml:"protocol"`                              // 您的插件协议名称
    Encoding string `yaml:"encoding"`                              // h264, h265, jpeg（用于元数据）
    URL      string `yaml:"url"`                                   // 相机 URL/端点
    Username string `yaml:"username"`                              // 可选认证
    Password string `yaml:"password"`                              // 可选认证
    Enabled  bool   `yaml:"enabled"`                               // 相机是否激活
    // ... 其他标准字段
}
```

此外，插件可以在其配置模式中定义自定义字段，并通过配置结构体访问它们。

## 录制器实现

您的插件必须实现 `model.Recorder` 接口：

```go
type Recorder interface {
    Start(ctx context.Context) error
    Stop() error
    Status() RecorderStatus
}
```

### 示例实现结构

```go
type MyRecorder struct {
    cameraID string
    url      string
    ctx      context.Context
    cancel   context.CancelFunc
    // ... 其他录制器状态
}

func (r *MyRecorder) Start(ctx context.Context) error {
    r.ctx, r.cancel = context.WithCancel(ctx)
    
    // 启动您的录制逻辑
    go r.recordLoop()
    
    return nil
}

func (r *MyRecorder) Stop() error {
    r.cancel()
    // 清理资源
    return nil
}

func (r *MyRecorder) Status() RecorderStatus {
    // 返回当前状态
    return StatusRecording
}

func (r *MyRecorder) recordLoop() {
    // 您的主录制逻辑
    // 这应该：
    // - 连接到相机
    // - 处理断开连接和重新连接
    // - 通过 store.WriteFrame() 将帧写入存储
    // - 适当更新状态
}
```

### 存储集成

使用提供的 `storage.Manager` 来处理段创建和帧写入：

```go
// 创建新的录制段
segment, err := store.CreateSegment(cameraID, model.SegmentMeta{
    Format:     model.FormatH264,
    StartedAt:  time.Now(),
})

// 写入视频帧
_, err := store.WriteFrame(segment.ID, frameData)
if err != nil {
    // 处理写入错误
}

// 录制停止时关闭段
recording, err := store.CloseSegment(segment.ID)
```

### 参考实现

要查看完整的示例，请参阅内置的录制器：
- `internal/recorder/h264.go` - H.264 RTSP 录制器
- `internal/recorder/mjpeg.go` - MJPEG RTSP 录制器
- `plugins/xiaomi/plugin.go` - 小米云相机插件

## 测试模式

### 使用模拟存储进行单元测试

```go
package myplugin

import (
    "testing"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
    
    "github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
    "github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
    "github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

func TestMyRecorder(t *testing.T) {
    // 创建测试相机配置
    cfg := config.CameraConfig{
        ID:       "test-camera",
        Name:     "Test Camera",
        Protocol: "my-protocol",
        URL:      "http://test-camera.local",
        Enabled:  true,
    }
    
    // 创建模拟存储管理器
    tmpDir := t.TempDir()
    store, err := storage.NewManager(tmpDir)
    require.NoError(t, err)
    defer store.CleanupTempFiles()
    
    // 测试录制器创建
    plugin := &MyPlugin{}
    recorder := plugin.NewRecorder(cfg, store, nil, nil)
    assert.NotNil(t, recorder)
    
    // 测试启动和停止录制器
    ctx := context.Background()
    err = recorder.Start(ctx)
    assert.NoError(t, err)
    
    err = recorder.Stop()
    assert.NoError(t, err)
    
    // 测试状态报告
    status := recorder.Status()
    assert.NotEmpty(t, status)
}
```

### 集成测试

对于需要实际相机连接的集成测试，使用测试容器或模拟服务器来模拟相机端点。

## 构建标签

未来的版本将支持条件插件编译的构建标签。目前，所有插件都始终包含在二进制文件中。

当实现构建标签时，您将能够使用：

```go
//go:build myplugin

package myplugin

// 插件代码在这里
```

这允许创建模块化二进制文件，其中插件可以根据构建标志选择性地包含。

## 插件指南

### 最佳实践

1. **错误处理**：实现带有自动重连逻辑的健壮错误处理
2. **资源清理**：确保所有资源（连接、缓冲区）在 `Stop()` 中正确释放
3. **状态报告**：保持录制器状态最新以便监控
4. **性能**：最小化内存使用并实现适当的帧缓冲
5. **文档**：在 README.md 中记录您的协议要求

### 常见模式

#### 重连逻辑

```go
func (r *MyRecorder) connectAndRetry() {
    for {
        select {
        case <-r.ctx.Done():
            return
        default:
            if err := r.connect(); err != nil {
                time.Sleep(5 * time.Second)
                continue
            }
            r.runRecordingLoop()
        }
    }
}
```

#### 帧缓冲

```go
type MyRecorder struct {
    frameBuffer chan []byte
    // ... 其他字段
}

func (r *MyRecorder) Start(ctx context.Context) error {
    r.frameBuffer = make(chan []byte, 1000) // 最多缓冲 1000 帧
    // ... 启动录制循环
}
```

### 故障排除

1. **插件未加载**：检查插件是否在 main.go 中使用 `_ "path/to/plugin"` 导入
2. **录制器未创建**：验证您的协议名称与相机配置中的匹配
3. **录制问题**：检查存储管理器日志中的写入错误
4. **内存问题**：实现适当的帧缓冲和清理

## API 扩展

如果您的插件需要自定义 API 端点，请实现 `RegisterRoutes` 方法：

```go
func (p *MyPlugin) RegisterRoutes(r chi.Router) {
    r.Group(func(r chi.Router) {
        r.Use(authmw.Authenticate) // 如需要，应用认证
        
        r.Get("/api/myplugin/{cameraID}/status", p.handleCameraStatus)
        r.Post("/api/myplugin/{cameraID}/command", p.handleCommand)
    })
}
```

## 示例：小米插件

小米插件展示了真实的实现：

```go
// plugins/xiaomi/plugin.go
type XiaomiPlugin struct{}

func init() {
    plugin.Register(&XiaomiPlugin{})
}

func (p *XiaomiPlugin) Name() string { return "xiaomi" }
func (p *XiaomiPlugin) Protocols() []string { return []string{"xiaomi"} }

func (p *XiaomiPlugin) NewRecorder(cfg config.CameraConfig, store *storage.Manager, db *storage.DB, opts ...*metrics.Metrics) model.Recorder {
    return NewXiaomiRecorder(XiaomiRecorderConfig{
        CameraID:   cfg.ID,
        MISSURL:    cfg.URL,
        SegmentDur: 30 * time.Second,
        DB:         db,
    }, store, opts...)
}

func (p *XiaomiPlugin) ConfigSchema() interface{} {
    return config.XiaomiConfig{}
}
```

此实现展示了如何：
- 使用特定于您的插件的自定义配置字段
- 实现复杂的相机连接逻辑
- 处理云 API 交互
- 与现有存储系统集成

## 下一步

1. **检查现有插件**：查看 `plugins/xiaomi/` 了解真实实现
2. **研究核心录制器**：检查 `internal/recorder/` 中的内置实现
3. **测试您的插件**：使用上述测试模式确保可靠性
4. **文档您的插件**：使用示例和配置说明更新您的插件

# gRPC 插件开发

MiBee NVR 插件系统支持两种操作模式：进程内插件（如上所述）和 gRPC 插件。本节涵盖具有进程隔离和崩溃恢复的 gRPC 插件开发工作流程。

## 双模式插件架构

MiBee NVR 插件系统使用双模式架构，支持两种类型的插件：

### 进程内插件
- **实现**：在 `init()` 函数中通过 `plugin.Register()` 注册
- **执行**：在与主 NVR 应用程序相同进程中运行
- **用例**：简单协议、快速开发、最小隔离需求
- **优点**：开销低，直接访问 Go 包，简单调试
- **局限性**：没有崩溃隔离，插件崩溃会使整个应用程序崩溃

### gRPC 插件
- **实现**：实现 `gen.PluginServiceServer` 接口
- **执行**：由 HashiCorp go-plugin 管理的独立进程
- **传输**：Unix 域套接字上的 gRPC
- **用例**：复杂协议、云 API、崩溃隔离、安全边界
- **优点**：进程隔离、崩溃恢复、不同的 Go 版本、语言互操作性
- **局限性**：更高的开销、网络延迟、复杂的调试

### 相机管理器调度顺序
当相机管理器需要创建录制器时，它遵循以下优先级：
1. **gRPC 插件**：首先尝试为协议查找 gRPC 插件
2. **进程内插件**：然后尝试进程内插件注册表
3. **内置录制器**：最后回退到内置录制器实现

这允许您使用任一插件类型覆盖内置协议，同时保持向后兼容性。

## gRPC 插件开发

gRPC 插件为复杂的相机集成提供进程隔离和崩溃恢复。按照此分步工作流创建 gRPC 插件：

### 1. 插件目录结构
在 `plugins/{name}/` 目录中创建您的插件，具有以下结构：

```
plugins/
└── my-plugin/
    ├── cmd/
    │   └── my-plugin-plugin/
    │       └── main.go          # 插件入口点
    ├── plugin.go                # 插件服务器实现
    ├── recorder.go              # 录制器实现
    ├── proto/
    │   └── my-plugin.proto      # 协议定义（如果需要）
    ├── go.mod                   # Go 模块文件
    └── README.md                # 插件文档
```

### 2. 实现 PluginServiceServer 接口
您的插件必须实现 `plugin/proto/nvr.proto` 中定义的 `gen.PluginServiceServer` 接口：

```go
import (
    "context"
    "github.com/Mi-Bee-Studio/MiBeeNvr/plugin/proto/gen"
    "google.golang.org/grpc"
)

type MyPluginServer struct {
    // 服务器实现字段
}

// 必需的接口方法
func (s *MyPluginServer) GetPluginInfo(ctx context.Context, req *gen.Empty) (*gen.PluginInfo, error) {
    return &gen.PluginInfo{
        Name:       "my-plugin",
        Version:    "1.0.0",
        Protocols: []string{"my-protocol"},
        SupportedEncodings: []gen.Codec{gen.Codec_CODEC_H264},
    }, nil
}

func (s *MyPluginServer) StartRecorder(req *gen.RecorderConfig, stream gen.PluginService_StartRecorderServer) error {
    // 实现您的相机连接和帧流传输逻辑
    for frame := range frameChannel {
        if err := stream.Send(&gen.Frame{
            Data: frame.Data,
            PtsNs: frame.PtsNs,
            IsIdr: frame.IsIdr,
            Codec: gen.Codec_CODEC_H264,
        }); err != nil {
            return err
        }
    }
    return nil
}

func (s *MyPluginServer) StopRecorder(ctx context.Context, req *gen.StopRequest) (*gen.StopResponse, error) {
    // 停止特定的录制器
    return &gen.StopResponse{}, nil
}

func (s *MyPluginServer) GetRecorderStatus(ctx context.Context, req *gen.StatusRequest) (*gen.RecorderStatus, error) {
    // 返回当前录制器状态
    return &gen.RecorderStatus{State: gen.RecorderState_RECORDER_STATE_IDLE}, nil
}

func (s *MyPluginServer) HealthCheck(ctx context.Context, req *gen.Empty) (*gen.HealthCheckResponse, error) {
    // 返回插件健康状态
    return &gen.HealthCheckResponse{Healthy: true}, nil
}

func (s *MyPluginServer) SetCloudConfig(ctx context.Context, req *gen.CloudConfig) (*gen.CloudConfigResponse, error) {
    // 如需要，处理云认证
    return &gen.CloudConfigResponse{Success: true}, nil
}
```

### 3. 创建插件入口点
在 `plugins/{name}/cmd/{name}-plugin/main.go` 创建入口点二进制文件：

```go
package main

import (
    "context"
    sharedPlugin "github.com/Mi-Bee-Studio/MiBeeNvr/plugin"
    "github.com/Mi-Bee-Studio/MiBeeNvr/plugin/proto/gen"
    "github.com/hashicorp/go-plugin"
    "google.golang.org/grpc"
)

type MyPluginGRPC struct {
    plugin.NetRPCUnsupportedPlugin
    Impl gen.PluginServiceServer
}

func (p *MyPluginGRPC) GRPCServer(_ *plugin.GRPCBroker, s *grpc.Server) error {
    gen.RegisterPluginServiceServer(s, p.Impl)
    return nil
}

func (p *MyPluginGRPC) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, c *grpc.ClientConn) (interface{}, error) {
    return gen.NewPluginServiceClient(c), nil
}

func main() {
    plugin.Serve(&plugin.ServeConfig{
        HandshakeConfig: sharedPlugin.Handshake,
        Plugins: map[string]plugin.Plugin{
            "plugin": &MyPluginGRPC{Impl: NewMyPluginServer()},
        },
        GRPCServer: plugin.DefaultGRPCServer,
    })
}
```

### 4. 共享握手配置
始终使用 `plugin/handshake.go` 中的共享握手配置。永远不要硬编码握手值：

```go
import sharedPlugin "github.com/Mi-Bee-Studio/MiBeeNvr/plugin"

// 在您的插件入口点中：
plugin.Serve(&plugin.ServeConfig{
    HandshakeConfig: sharedPlugin.Handshake,  // 使用共享配置
    // ... 其余配置
})
```

共享握手确保 NVR 主机和插件二进制文件就通信协议和安全魔数 Cookie 值达成一致。

### 5. 构建和部署
为目标架构构建您的插件：

```bash
# 为 ARM64（树莓派）构建
cd plugins/my-plugin
GOOS=linux GOARCH=arm64 go build -o cmd/my-plugin-plugin/main.go

# 或使用项目的构建系统（如果可用）
make plugin-my-plugin-arm64
```

将插件二进制文件部署到目标系统上的 `{plugins-dir}/{name}/{name-plugin}`：

```bash
# 示例部署结构
mkdir -p /path/to/plugins/my-plugin
cp cmd/my-plugin-plugin/main.go /path/to/plugins/my-plugin/my-plugin-plugin
chmod +x /path/to/plugins/my-plugin/my-plugin-plugin
```

## 关键约定

### 插件二进制命名
- **位置**：`{plugins-dir}/{name}/{name}-plugin`
- **示例**：`/opt/mibee-nvr/plugins/xiaomi/xiaomi-plugin`
- **权限**：必须可执行（`chmod +x`）

### 握手配置
- **始终使用**：来自 `plugin/handshake.go` 的 `sharedPlugin.Handshake`
- **永不硬编码**魔数值
- **协议版本**：当前在共享配置中设置为 1

### 插件映射键
- **始终使用**：`"plugin"` 作为插件映射中的键
- **不要更改**此值，因为 NVR 主机期望这个特定的键

### 必需的 gRPC 方法
所有 gRPC 插件必须实现这些方法：
- `GetPluginInfo`：返回插件元数据和功能
- `StartRecorder`：开始将相机帧流传输到主机
- `StopRecorder`：指示录制器停止流传输
- `GetRecorderStatus`：查询当前录制器状态
- `HealthCheck`：验证插件进程是否存活
- `SetCloudConfig`：处理云认证（如果适用）

### 帧格式
通过 gRPC 发送的帧必须遵循此格式：
- **NAL 单元字节**：带有起始代码前缀（00 00 00 01）的原始视频数据
- **时间戳**：自段开始以来的纳秒级呈现时间戳
- **IDR 标志**：关键帧为真（H.264 的 IDR 帧，H.265 的 VPS/IDR）
- **编解码器**：H.264、H.265 或 MJPEG 枚举值
- **编解码器信息**：SPS/PPS/VPS 参数集为真
- **额外映射**：可选的编解码器特定参数

### 帧流传输模式
在 `StartRecorder` 中实现适当的帧流传输：

```go
func (s *MyPluginServer) StartRecorder(req *gen.RecorderConfig, stream gen.PluginService_StartRecorderServer) error {
    // 连接到相机
    camera, err := s.connectToCamera(req)
    if err != nil {
        return err
    }
    defer camera.Close()

    // 流传输帧
    for frame := range camera.FrameStream() {
        if err := stream.Send(&gen.Frame{
            Data:    frame.Data,
            PtsNs:   frame.PtsNs,
            IsIdr:   frame.IsIdr,
            Codec:   gen.Codec_CODEC_H264,
            IsCodecInfo: frame.IsCodecInfo,
            Extra:   frame.Extra,
        }); err != nil {
            // 主机关闭流
            return nil
        }
    }
    return nil
}
```

### 错误处理
为所有 gRPC 方法实现健壮的错误处理：
- **网络错误**：使用指数退避重新连接
- **流错误**：优雅关闭并返回适当的状态
- **相机错误**：更新录制器状态并等待重新连接
- **gRPC 错误**：使用适当的错误代码和消息

### 进程生命周期
go-plugin 框架自动管理插件进程：
- **启动**：当 NVR 主机需要时插件启动
- **健康监控**：主机通过 `HealthCheck` ping 插件
- **崩溃恢复**：如果插件崩溃，插件会自动重启
- **关闭**：当 NVR 关闭或移除相机时插件停止

## 调试 gRPC 插件

### 常见问题

1. **插件未找到**：检查二进制文件路径和权限
2. **握手失败**：验证使用共享握手配置
3. **gRPC 连接错误**：检查网络和防火墙设置
4. **帧流传输问题**：验证 NAL 单元格式和时间戳
5. **内存泄漏**：随时间监控插件内存使用情况

### 调试命令

```bash
# 检查插件二进制文件
ls -la /path/to/plugins/my-plugin/my-plugin-plugin

# 测试插件握手
echo "NVR_PLUGIN=mibee-nvr-plugin" | /path/to/plugins/my-plugin/my-plugin-plugin

# 监控插件日志
tail -f /var/log/mibee-nvr/plugins/my-plugin.log

# 检查插件健康状态
curl -X POST http://localhost:9090/api/plugins/my-plugin/health
```

### 开发技巧

1. **使用小米插件作为参考**：研究 `plugins/xiaomi/` 了解真实实现
2. **使用模拟相机测试**：创建测试相机服务器用于开发
3. **监控 NVR 日志**：观察插件启动和帧处理日志
4. **使用 Go 插件进行本地测试**：开发期间使用进程内插件测试
5. **验证帧格式**：确保 NAL 单元具有正确的起始代码和时间戳

## 示例：真实实现

要查看完整的工作示例，请参阅小米插件实现：
- **入口点**：`plugins/xiaomi/cmd/xiaomi-plugin/main.go`
- **插件服务器**：`plugins/xiaomi/plugin.go`
- **录制器实现**：`plugins/xiaomi/recorder.go`
- **协议实现**：`plugins/xiaomi/miss.go`、`plugins/xiaomi/cs2.go`
- **构建脚本**：在项目 Makefile 中查找 `make plugin-xiaomi-arm64`

小米插件展示了复杂的云认证、CS2 P2P 传输和加密帧流传输模式，您可以将其适应自己的协议。

## 从进程内迁移到 gRPC

如果您有现有的进程内插件并希望迁移到 gRPC：

### 逐步迁移

1. **保留进程内版本**：在过渡期间保持向后兼容性
2. **添加 gRPC 接口**：在现有录制器旁边实现 `gen.PluginServiceServer`
3. **创建入口点**：构建独立的 gRPC 插件二进制文件
4. **部署两者**：部署 gRPC 插件 alongside 进程内版本
5. **测试 gRPC 版本**：使用新架构验证功能
6. **更新文档**：为用户记录两种插件模式
7. **淘汰进程内**：一旦 gRPC 版本稳定，弃用进程内版本

### 架构差异

```go
// 进程内（现有）
func (p *MyPlugin) NewRecorder(cfg config.CameraConfig, store *storage.Manager, db *storage.DB, opts ...*metrics.Metrics) model.Recorder {
    return &MyRecorder{cfg: cfg, store: store, db: db}
}

// gRPC（新）
func (s *MyPluginServer) StartRecorder(req *gen.RecorderConfig, stream gen.PluginService_StartRecorderServer) error {
    // 创建录制器并流传输帧
    recorder := NewMyRecorder(req, stream)
    return recorder.Run()
}
```

### 通信模式

**进程内**：直接函数调用和共享内存

```go
// 直接访问存储管理器
segment, err := store.CreateSegment(cameraID, meta)

// 直接指标更新
metrics.IncActive(cameraID)
```

**gRPC**：通过网络的结构化数据

```go
// 没有直接访问存储管理器
// 通过 gRPC 流将帧发送到主机
stream.Send(&gen.Frame{Data: frameData})

// 主机处理存储集成
// 主机处理指标更新
```

gRPC 模式需要不同的存储访问和指标模式，因为这些由主机进程处理，而不是直接可供插件使用。

## 下一步

1. **研究小米插件**：检查 `plugins/xiaomi/` 了解生产模式
2. **测试您的插件**：使用模拟相机和集成测试
3. **文档您的插件**：创建包含设置和配置的 README
4. **监控性能**：注意内存泄漏和帧处理延迟
5. **处理边缘情况**：实现适当的错误恢复和重连逻辑