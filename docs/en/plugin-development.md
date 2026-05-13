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