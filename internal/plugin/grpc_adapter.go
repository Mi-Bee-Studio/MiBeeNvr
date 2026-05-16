package plugin

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/grpc"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"

	gen "github.com/Mi-Bee-Studio/MiBeeNvr/plugin/proto/gen"
)

var grpcAdapterLogger = slog.Default().With("component", "grpc-adapter")

// FrameHandler processes frames received from a gRPC plugin.
// This decouples the adapter from the FrameReceiver implementation (Task 5).
type FrameHandler interface {
	HandleFrame(ctx context.Context, frame *gen.Frame) error
	Close() error
}

// gRPCRecorderAdapter implements model.Recorder by proxying to a gRPC plugin client.
// It bridges the CameraManager (which only knows about model.Recorder) and the
// gRPC plugin system.
//
// The adapter does NOT handle reconnection — that is the recorder's responsibility.
// When the gRPC stream ends (error or plugin crash), the adapter sets status to
// Error and signals done.
type gRPCRecorderAdapter struct {
	client     gen.PluginServiceClient
	handler    FrameHandler
	cfg        config.CameraConfig
	cameraID   string
	segmentDur time.Duration

	mu     sync.Mutex
	cancel context.CancelFunc
	status model.RecorderStatus
	done   chan struct{}
}

// Compile-time interface check.
var _ model.Recorder = (*gRPCRecorderAdapter)(nil)

// NewGRPCRecorderAdapter creates a new adapter that proxies model.Recorder calls
// to the given gRPC plugin client. Frames received from the plugin are forwarded
// to the FrameHandler.
func NewGRPCRecorderAdapter(client gen.PluginServiceClient, handler FrameHandler, cfg config.CameraConfig, segmentDur time.Duration) *gRPCRecorderAdapter {
	if segmentDur <= 0 {
		segmentDur = 30 * time.Second
	}
	return &gRPCRecorderAdapter{
		client:     client,
		handler:    handler,
		cfg:        cfg,
		cameraID:   cfg.ID,
		segmentDur: segmentDur,
		status:     model.StatusStopped,
	}
}

// Start establishes a gRPC stream with the plugin and begins receiving frames.
func (a *gRPCRecorderAdapter) Start(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.status == model.StatusRecording || a.status == model.StatusReconnecting {
		return fmt.Errorf("grpc adapter for %q already running (status=%s)", a.cameraID, a.status)
	}

	ctx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	a.done = make(chan struct{})
	a.status = model.StatusRecording

	recorderConfig := a.buildRecorderConfig()
	stream, err := a.client.StartRecorder(ctx, recorderConfig)
	if err != nil {
		cancel()
		a.status = model.StatusError
		return fmt.Errorf("StartRecorder RPC failed for %q: %w", a.cameraID, err)
	}

	go a.receiveLoop(ctx, stream)
	return nil
}

// Stop signals the plugin to stop recording, cancels the context, and waits
// for the receive goroutine to finish.
func (a *gRPCRecorderAdapter) Stop() error {
	a.mu.Lock()
	if a.cancel != nil {
		a.cancel()
	}
	a.mu.Unlock()

	// Best-effort: tell plugin to stop. Ignore error — plugin may already be gone.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	if _, err := a.client.StopRecorder(stopCtx, &gen.StopRequest{CameraId: a.cameraID}); err != nil {
		grpcAdapterLogger.Warn("StopRecorder RPC failed", "camera_id", a.cameraID, "error", err)
	}

	// Wait for receive goroutine to finish.
	if a.done != nil {
		select {
		case <-a.done:
		case <-time.After(10 * time.Second):
			grpcAdapterLogger.Warn("timeout waiting for receive goroutine", "camera_id", a.cameraID)
		}
	}

	a.mu.Lock()
	a.status = model.StatusStopped
	a.mu.Unlock()

	if a.handler != nil {
		if err := a.handler.Close(); err != nil {
			grpcAdapterLogger.Warn("handler close failed", "camera_id", a.cameraID, "error", err)
		}
	}

	return nil
}

// Status returns the current recorder status.
func (a *gRPCRecorderAdapter) Status() model.RecorderStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status
}

// receiveLoop reads frames from the gRPC stream and forwards them to the handler.
func (a *gRPCRecorderAdapter) receiveLoop(ctx context.Context, stream grpc.ServerStreamingClient[gen.Frame]) {
	defer close(a.done)

	for {
		frame, err := stream.Recv()
		if err != nil {
			if ctx.Err() != nil {
				// Context cancelled — intentional stop.
				a.setStatus(model.StatusStopped)
				return
			}
			grpcAdapterLogger.Error("stream recv error", "camera_id", a.cameraID, "error", err)
			a.setStatus(model.StatusError)
			return
		}

		if err := a.handler.HandleFrame(ctx, frame); err != nil {
			grpcAdapterLogger.Error("handle frame error", "camera_id", a.cameraID, "error", err)
			a.setStatus(model.StatusError)
			return
		}
	}
}
func (a *gRPCRecorderAdapter) setStatus(s model.RecorderStatus) {
	a.mu.Lock()
	a.status = s
	a.mu.Unlock()
}

// buildRecorderConfig converts a CameraConfig to a proto RecorderConfig.
func (a *gRPCRecorderAdapter) buildRecorderConfig() *gen.RecorderConfig {
	opts := make(map[string]string)
	// Forward protocol-specific fields as options.
	if a.cfg.DID != "" {
		opts["did"] = a.cfg.DID
	}
	if a.cfg.Vendor != "" {
		opts["vendor"] = a.cfg.Vendor
	}
	if a.cfg.ONVIFEndpoint != "" {
		opts["onvif_endpoint"] = a.cfg.ONVIFEndpoint
	}
	if a.cfg.ProfileToken != "" {
		opts["profile_token"] = a.cfg.ProfileToken
	}
	if a.cfg.StreamEncoding != "" {
		opts["stream_encoding"] = a.cfg.StreamEncoding
	}

	return &gen.RecorderConfig{
		CameraId:          a.cfg.ID,
		Name:              a.cfg.Name,
		Url:               a.cfg.URL,
		Username:          a.cfg.Username,
		Password:          a.cfg.Password,
		SegmentDurationNs: uint64(a.segmentDur),
		Options:           opts,
		Encoding:          a.cfg.Encoding,
	}
}
