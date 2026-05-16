# Plugin Development Guide

## Overview

The MiBee NVR plugin system allows developers to extend the recorder with custom camera protocols and recording mechanisms. Plugins are self-contained Go modules that implement the `RecorderPlugin` interface, enabling support for new camera protocols like Xiaomi cloud cameras, IoT devices, or proprietary streaming protocols.

The plugin system automatically integrates with the existing camera management, configuration system, and web API, allowing seamless extension of the NVR functionality.

## RecorderPlugin Interface

The core interface that all plugins must implement:

```go
type RecorderPlugin interface {
    // Name returns the unique plugin identifier (e.g. "xiaomi").
    Name() string
    
    // Protocols returns the transport protocols this plugin handles (e.g. ["xiaomi"]).
    Protocols() []string
    
    // NewRecorder creates a new Recorder for the given camera configuration.
    NewRecorder(cfg config.CameraConfig, store *storage.Manager, db *storage.DB, opts ...*metrics.Metrics) model.Recorder
    
    // RegisterRoutes adds plugin-specific HTTP routes to the router.
    RegisterRoutes(r chi.Router)
    
    // ConfigSchema returns an example config struct for documentation/validation.
    ConfigSchema() interface{}
}
```

### Interface Methods

#### `Name() string`
Returns the unique identifier for your plugin. This should be a lowercase string that matches the protocol name used in camera configurations.

#### `Protocols() []string`
Returns a list of protocol strings that this plugin can handle. These are the protocol values that users will specify in their camera configuration.

#### `NewRecorder(cfg config.CameraConfig, store *storage.Manager, db *storage.DB, opts ...*metrics.Metrics) model.Recorder`
Creates a new recorder instance for the given camera configuration. This is where you'll implement your custom recording logic.

#### `RegisterRoutes(r chi.Router)`
Optional method to add custom HTTP endpoints for plugin-specific functionality. Most plugins don't need this unless they provide additional API endpoints.

#### `ConfigSchema() interface{}`
Returns an example configuration struct that documents the additional fields available for cameras using this plugin.

## Quick Start

Here's a minimal plugin implementation:

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
    // Create and return your recorder
    return &MyRecorder{
        cameraID: cfg.ID,
        url:      cfg.URL,
        // ... other fields from config
    }
}

func (p *MyPlugin) RegisterRoutes(r chi.Router) {
    // Optional: Add custom API routes
    r.Get("/api/myplugin/status", p.handleStatus)
}

func (p *MyPlugin) ConfigSchema() interface{} {
    // Optional: Return example config structure
    return nil
}
```

## Registration

Plugins are automatically registered using Go's `init()` function and blank imports:

1. **Package Import**: Add `import _ "github.com/Mi-Bee-Studio/MiBeeNvr/plugins/your-plugin-name"` to `cmd/mibee-nvr/main.go`

2. **Plugin Registration**: In your plugin's `init()` function, call `plugin.Register(&YourPlugin{})`

The plugin system uses a global registry that's populated during application startup. Plugins are loaded in the order they're imported.

## CameraConfig Fields

Plugins have access to the standard `config.CameraConfig` fields:

```go
type CameraConfig struct {
    ID       string `yaml:"id"`                                    // Unique camera identifier
    Name     string `yaml:"name"`                                  // Display name
    Protocol string `yaml:"protocol"`                              // Your plugin protocol name
    Encoding string `yaml:"encoding"`                              // h264, h265, jpeg (used for metadata)
    URL      string `yaml:"url"`                                   // Camera URL/endpoint
    Username string `yaml:"username"`                              // Optional authentication
    Password string `yaml:"password"`                              // Optional authentication
    Enabled  bool   `yaml:"enabled"`                               // Whether camera is active
    // ... other standard fields
}
```

Additionally, plugins can define custom fields in their config schema and access them through the config struct.

## Recorder Implementation

Your plugin must implement the `model.Recorder` interface:

```go
type Recorder interface {
    Start(ctx context.Context) error
    Stop() error
    Status() RecorderStatus
}
```

### Example Implementation Structure

```go
type MyRecorder struct {
    cameraID string
    url      string
    ctx      context.Context
    cancel   context.CancelFunc
    // ... other recorder state
}

func (r *MyRecorder) Start(ctx context.Context) error {
    r.ctx, r.cancel = context.WithCancel(ctx)
    
    // Start your recording logic
    go r.recordLoop()
    
    return nil
}

func (r *MyRecorder) Stop() error {
    r.cancel()
    // Cleanup resources
    return nil
}

func (r *MyRecorder) Status() RecorderStatus {
    // Return current status
    return StatusRecording
}

func (r *MyRecorder) recordLoop() {
    // Your main recording logic
    // This should:
    // - Connect to the camera
    // - Handle disconnections and reconnections
    // - Write frames to storage via store.WriteFrame()
    // - Update status appropriately
}
```

### Storage Integration

Use the provided `storage.Manager` to handle segment creation and frame writing:

```go
// Create a new recording segment
segment, err := store.CreateSegment(cameraID, model.SegmentMeta{
    Format:     model.FormatH264,
    StartedAt:  time.Now(),
})

// Write video frames
_, err := store.WriteFrame(segment.ID, frameData)
if err != nil {
    // Handle write errors
}

// Close the segment when recording stops
recording, err := store.CloseSegment(segment.ID)
```

### Reference Implementation

For a complete example, see the built-in recorders:
- `internal/recorder/h264.go` - H.264 RTSP recorder
- `internal/recorder/mjpeg.go` - MJPEG RTSP recorder
- `plugins/xiaomi/plugin.go` - Xiaomi cloud camera plugin

## Testing Patterns

### Unit Testing with Mock Stores

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
    // Create test camera config
    cfg := config.CameraConfig{
        ID:       "test-camera",
        Name:     "Test Camera",
        Protocol: "my-protocol",
        URL:      "http://test-camera.local",
        Enabled:  true,
    }
    
    // Create mock storage manager
    tmpDir := t.TempDir()
    store, err := storage.NewManager(tmpDir)
    require.NoError(t, err)
    defer store.CleanupTempFiles()
    
    // Test recorder creation
    plugin := &MyPlugin{}
    recorder := plugin.NewRecorder(cfg, store, nil, nil)
    assert.NotNil(t, recorder)
    
    // Test starting and stopping recorder
    ctx := context.Background()
    err = recorder.Start(ctx)
    assert.NoError(t, err)
    
    err = recorder.Stop()
    assert.NoError(t, err)
    
    // Test status reporting
    status := recorder.Status()
    assert.NotEmpty(t, status)
}
```

### Integration Testing

For integration testing that requires actual camera connectivity, use test containers or mock servers to simulate camera endpoints.

## Build Tags

Future versions will support build tags for conditional plugin compilation. For now, all plugins are always included in the binary.

When build tags are implemented, you'll be able to use:

```go
//go:build myplugin

package myplugin

// Plugin code here
```

This allows creating modular binaries where plugins can be optionally included based on build flags.

## Plugin Guidelines

### Best Practices

1. **Error Handling**: Implement robust error handling with automatic reconnection logic
2. **Resource Cleanup**: Ensure all resources (connections, buffers) are properly released in `Stop()`
3. **Status Reporting**: Keep the recorder status up-to-date for monitoring
4. **Performance**: Minimize memory usage and implement proper frame buffering
5. **Documentation**: Document your protocol requirements in README.md

### Common Patterns

#### Reconnection Logic

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

#### Frame Buffering

```go
type MyRecorder struct {
    frameBuffer chan []byte
    // ... other fields
}

func (r *MyRecorder) Start(ctx context.Context) error {
    r.frameBuffer = make(chan []byte, 1000) // Buffer up to 1000 frames
    // ... start recording loop
}
```

### Troubleshooting

1. **Plugin Not Loading**: Check that the plugin is imported in main.go with `_ "path/to/plugin"`
2. **Recorder Not Created**: Verify that your protocol name matches the one in camera configuration
3. **Recording Issues**: Check storage manager logs for write errors
4. **Memory Issues**: Implement proper frame buffering and cleanup

## API Extension

If your plugin needs custom API endpoints, implement the `RegisterRoutes` method:

```go
func (p *MyPlugin) RegisterRoutes(r chi.Router) {
    r.Group(func(r chi.Router) {
        r.Use(authmw.Authenticate) // Apply authentication if needed
        
        r.Get("/api/myplugin/{cameraID}/status", p.handleCameraStatus)
        r.Post("/api/myplugin/{cameraID}/command", p.handleCommand)
    })
}
```

## Example: Xiaomi Plugin

The Xiaomi plugin demonstrates real-world implementation:

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

This implementation shows how to:
- Use custom config fields specific to your plugin
- Implement complex camera connection logic
- Handle cloud API interactions
- Integrate with the existing storage system

## Next Steps

1. **Examine Existing Plugins**: Look at `plugins/xiaomi/` for a real implementation
2. **Study Core Recorders**: Check `internal/recorder/` for built-in implementations
3. **Test Your Plugin**: Use the testing patterns above to ensure reliability
4. **Documentation**: Update your plugin with examples and configuration instructions

# gRPC Plugin Development

The MiBee NVR plugin system supports two modes of operation: in-process plugins (documented above) and gRPC plugins. This section covers the gRPC plugin development workflow for creating plugins with process isolation and crash recovery.

## Dual-Mode Plugin Architecture

The MiBee NVR plugin system uses a dual-mode architecture that supports two types of plugins:

### In-Process Plugins
- **Implementation**: Register via `plugin.Register()` in `init()` function
- **Execution**: Runs in the same process as the main NVR application
- **Use Cases**: Simple protocols, quick development, minimal isolation needs
- **Benefits**: Low overhead, direct access to Go packages, simple debugging
- **Limitations**: No crash isolation, plugin crash takes down entire application

### gRPC Plugins
- **Implementation**: Implement `gen.PluginServiceServer` interface
- **Execution**: Separate process managed by HashiCorp go-plugin
- **Transport**: gRPC over Unix Domain Sockets
- **Use Cases**: Complex protocols, cloud APIs, crash isolation, security boundaries
- **Benefits**: Process isolation, crash recovery, different Go versions, language interoperability
- **Limitations**: Higher overhead, network latency, complex debugging

### Camera Manager Dispatch Order
When the camera manager needs to create a recorder, it follows this priority:
1. **gRPC plugins**: First tries to find a gRPC plugin for the protocol
2. **In-process plugins**: Then tries the in-process plugin registry
3. **Built-in recorders**: Finally falls back to built-in recorder implementations

This allows you to override built-in protocols with either plugin type while maintaining backward compatibility.

## gRPC Plugin Development

gRPC plugins provide process isolation and crash recovery for complex camera integrations. Follow this step-by-step workflow to create a gRPC plugin:

### 1. Plugin Directory Structure
Create your plugin in the `plugins/{name}/` directory with the following structure:

```
plugins/
└── my-plugin/
    ├── cmd/
    │   └── my-plugin-plugin/
    │       └── main.go          # Plugin entry point
    ├── plugin.go                # Plugin server implementation
    ├── recorder.go              # Recorder implementation
    ├── proto/
    │   └── my-plugin.proto      # Protocol definition (if needed)
    ├── go.mod                   # Go module file
    └── README.md                # Plugin documentation
```

### 2. Implement PluginServiceServer Interface
Your plugin must implement the `gen.PluginServiceServer` interface defined in `plugin/proto/nvr.proto`:

```go
import (
    "context"
    "github.com/Mi-Bee-Studio/MiBeeNvr/plugin/proto/gen"
    "google.golang.org/grpc"
)

type MyPluginServer struct {
    // Server implementation fields
}

// Required interface methods
func (s *MyPluginServer) GetPluginInfo(ctx context.Context, req *gen.Empty) (*gen.PluginInfo, error) {
    return &gen.PluginInfo{
        Name:       "my-plugin",
        Version:    "1.0.0",
        Protocols: []string{"my-protocol"},
        SupportedEncodings: []gen.Codec{gen.Codec_CODEC_H264},
    }, nil
}

func (s *MyPluginServer) StartRecorder(req *gen.RecorderConfig, stream gen.PluginService_StartRecorderServer) error {
    // Implement your camera connection and frame streaming logic
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
    // Stop the specific recorder
    return &gen.StopResponse{}, nil
}

func (s *MyPluginServer) GetRecorderStatus(ctx context.Context, req *gen.StatusRequest) (*gen.RecorderStatus, error) {
    // Return current recorder status
    return &gen.RecorderStatus{State: gen.RecorderState_RECORDER_STATE_IDLE}, nil
}

func (s *MyPluginServer) HealthCheck(ctx context.Context, req *gen.Empty) (*gen.HealthCheckResponse, error) {
    // Return plugin health status
    return &gen.HealthCheckResponse{Healthy: true}, nil
}

func (s *MyPluginServer) SetCloudConfig(ctx context.Context, req *gen.CloudConfig) (*gen.CloudConfigResponse, error) {
    // Handle cloud authentication if needed
    return &gen.CloudConfigResponse{Success: true}, nil
}
}```

### 3. Create Plugin Entry Point
Create the entry point binary at `plugins/{name}/cmd/{name}-plugin/main.go`:

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

### 4. Shared Handshake Configuration
Always use the shared handshake configuration from `plugin/handshake.go`. NEVER hardcode handshake values:

```go
import sharedPlugin "github.com/Mi-Bee-Studio/MiBeeNvr/plugin"

// In your plugin entry point:
plugin.Serve(&plugin.ServeConfig{
    HandshakeConfig: sharedPlugin.Handshake,  // Use shared config
    // ... rest of config
})
```

The shared handshake ensures that the NVR host and plugin binaries agree on communication protocol and magic cookie values for security.

### 5. Build and Deploy
Build your plugin for the target architecture:

```bash
# Build for ARM64 (Raspberry Pi)
cd plugins/my-plugin
GOOS=linux GOARCH=arm64 go build -o cmd/my-plugin-plugin/main.go

# Or use the project's build system if available
make plugin-my-plugin-arm64
```

Deploy the plugin binary to the target system at `{plugins-dir}/{name}/{name-plugin}`:

```bash
# Example deployment structure
mkdir -p /path/to/plugins/my-plugin
cp cmd/my-plugin-plugin/main.go /path/to/plugins/my-plugin/my-plugin-plugin
chmod +x /path/to/plugins/my-plugin/my-plugin-plugin
```

## Key Conventions

### Plugin Binary Naming
- **Location**: `{plugins-dir}/{name}/{name}-plugin`
- **Example**: `/opt/mibee-nvr/plugins/xiaomi/xiaomi-plugin`
- **Permissions**: Must be executable (`chmod +x`)

### Handshake Configuration
- **Always use**: `sharedPlugin.Handshake` from `plugin/handshake.go`
- **Never hardcode** the magic cookie values
- **Protocol version**: Currently set to 1 in the shared config

### Plugin Map Key
- **Always use**: `"plugin"` as the key in the plugin map
- **Do not change** this value as the NVR host expects this specific key

### Required gRPC Methods
All gRPC plugins must implement these methods:

- `GetPluginInfo`: Return plugin metadata and capabilities
- `StartRecorder`: Begin streaming frames from camera to host
- `StopRecorder`: Signal recorder to stop streaming
- `GetRecorderStatus`: Query current recorder state
- `HealthCheck`: Verify plugin process is alive
- `SetCloudConfig`: Handle cloud authentication (if applicable)

### Frame Format
Frames sent over gRPC must follow this format:

- **NAL unit bytes**: Raw video data with start code prefix (00 00 00 01)
- **Timestamp**: Presentation timestamp in nanoseconds since segment start
- **IDR flag**: True for keyframes (IDR frames for H.264, VPS/IDR for H.265)
- **Codec**: H.264, H.265, or MJPEG enum value
- **Codec info**: True for SPS/PPS/VPS parameter sets
- **Extra map**: Optional codec-specific parameters

### Frame Streaming Pattern
Implement proper frame streaming in `StartRecorder`:

```go
func (s *MyPluginServer) StartRecorder(req *gen.RecorderConfig, stream gen.PluginService_StartRecorderServer) error {
    // Connect to camera
    camera, err := s.connectToCamera(req)
    if err != nil {
        return err
    }
    defer camera.Close()

    // Stream frames
    for frame := range camera.FrameStream() {
        if err := stream.Send(&gen.Frame{
            Data:    frame.Data,
            PtsNs:   frame.PtsNs,
            IsIdr:   frame.IsIdr,
            Codec:   gen.Codec_CODEC_H264,
            IsCodecInfo: frame.IsCodecInfo,
            Extra:   frame.Extra,
        }); err != nil {
            // Stream closed by host
            return nil
        }
    }
    return nil
}
```

### Error Handling
Implement robust error handling for all gRPC methods:

- **Network errors**: Reconnect with exponential backoff
- **Stream errors**: Close gracefully and return appropriate status
- **Camera errors**: Update recorder status and wait for reconnection
- **gRPC errors**: Use proper error codes and messages

### Process Lifecycle
The go-plugin framework manages the plugin process automatically:

- **Startup**: Plugin starts when NVR host needs it
- **Health monitoring**: Host pings plugin via `HealthCheck`
- **Crash recovery**: Plugin restarts automatically if it crashes
- **Shutdown**: Plugin stops when NVR shuts down or camera is removed

## Debugging gRPC Plugins

### Common Issues

1. **Plugin not found**: Check binary path and permissions
2. **Handshake failed**: Verify using shared handshake config
3. **gRPC connection errors**: Check network and firewall settings
4. **Frame streaming issues**: Verify NAL unit format and timestamps
5. **Memory leaks**: Monitor plugin memory usage over time

### Debugging Commands

```bash
# Check plugin binary
ls -la /path/to/plugins/my-plugin/my-plugin-plugin

# Test plugin handshake
echo "NVR_PLUGIN=mibee-nvr-plugin" | /path/to/plugins/my-plugin/my-plugin-plugin

# Monitor plugin logs
tail -f /var/log/mibee-nvr/plugins/my-plugin.log

# Check plugin health
curl -X POST http://localhost:9090/api/plugins/my-plugin/health
```

### Development Tips

1. **Use the Xiaomi plugin as reference**: Study `plugins/xiaomi/` for real-world implementation
2. **Test with mock cameras**: Create test camera servers for development
3. **Monitor NVR logs**: Watch for plugin startup and frame processing logs
4. **Use Go plugins for local testing**: Test with in-process plugins during development
5. **Validate frame format**: Ensure NAL units have correct start codes and timestamps

## Example: Real Implementation

For a complete working example, see the Xiaomi plugin implementation:

- **Entry point**: `plugins/xiaomi/cmd/xiaomi-plugin/main.go`
- **Plugin server**: `plugins/xiaomi/plugin.go`
- **Recorder implementation**: `plugins/xiaomi/recorder.go`
- **Protocol implementation**: `plugins/xiaomi/miss.go`, `plugins/xiaomi/cs2.go`
- **Build script**: Look for `make plugin-xiaomi-arm64` in the project Makefile

The Xiaomi plugin demonstrates complex cloud authentication, CS2 P2P transport, and encrypted frame streaming patterns that you can adapt for your own protocol.


## Migration from In-Process to gRPC

If you have an existing in-process plugin and want to migrate to gRPC:

### Step-by-Step Migration

1. **Keep in-process version**: Maintain backward compatibility during transition
2. **Add gRPC interface**: Implement `gen.PluginServiceServer` alongside existing recorder
3. **Create entry point**: Build the standalone gRPC plugin binary
4. **Deploy both**: Deploy gRPC plugin alongside in-process version
5. **Test gRPC version**: Verify functionality with the new architecture
6. **Update documentation**: Document both plugin modes for users
7. **Phase out in-process**: Once gRPC version is stable, deprecate in-process version

### Architecture Differences

```go
// In-process (existing)
func (p *MyPlugin) NewRecorder(cfg config.CameraConfig, store *storage.Manager, db *storage.DB, opts ...*metrics.Metrics) model.Recorder {
    return &MyRecorder{cfg: cfg, store: store, db: db}
}

// gRPC (new)
func (s *MyPluginServer) StartRecorder(req *gen.RecorderConfig, stream gen.PluginService_StartRecorderServer) error {
    // Create recorder and stream frames
    recorder := NewMyRecorder(req, stream)
    return recorder.Run()
}
```

### Communication Patterns

**In-process**: Direct function calls and shared memory

```go
// Direct access to storage manager
segment, err := store.CreateSegment(cameraID, meta)

// Direct metric updates
metrics.IncActive(cameraID)
```

**gRPC**: Structured data over network

```go
// No direct access to storage manager
// Send frames to host via gRPC stream
stream.Send(&gen.Frame{Data: frameData})

// Host handles storage integration
// Host handles metric updates
```

The gRPC mode requires different patterns for storage access and metrics, as these are handled by the host process rather than directly available to the plugin.

## Next Steps

1. **Study the Xiaomi plugin**: Examine `plugins/xiaomi/` for production patterns
2. **Test your plugin**: Use mock cameras and integration tests
3. **Document your plugin**: Create README with setup and configuration
4. **Monitor performance**: Watch for memory leaks and frame processing latency
5. **Handle edge cases**: Implement proper error recovery and reconnection logic
