// SPDX-License-Identifier: MIT
//
// Xiaomi gRPC PluginService server implementation.
// Runs inside the plugin process, manages streaming recorder lifecycle,
// and handles cloud config for Xiaomi cameras.

package xiaomi

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	gen "github.com/Mi-Bee-Studio/MiBeeNvr/plugin/proto/gen"
	"google.golang.org/grpc"
)

var grpcServerLogger = slog.Default().With("component", "xiaomi-grpc-server")

// recorderState tracks a single streaming recorder instance.
type recorderState struct {
	cameraID     string
	state        gen.RecorderState
	errMsg       string
	bytesWritten atomic.Int64
	segments     atomic.Int64
	startTime    time.Time
	cancel       context.CancelFunc
}

// streamSender adapts a gRPC ServerStreamingServer to the FrameSender interface.
type streamSender struct {
	stream       grpc.ServerStreamingServer[gen.Frame]
	bytesWritten *atomic.Int64
}

// SendFrame sends a frame over the gRPC stream.
func (s *streamSender) SendFrame(_ context.Context, frame *gen.Frame) error {
	if err := s.stream.Send(frame); err != nil {
		return err
	}
	s.bytesWritten.Add(int64(len(frame.Data)))
	return nil
}

// PluginServer implements gen.PluginServiceServer for the Xiaomi plugin.
type PluginServer struct {
	gen.UnimplementedPluginServiceServer

	mu        sync.RWMutex
	recorders map[string]*recorderState

	// Cloud config for Xiaomi camera URL resolution.
	cloudMu  sync.RWMutex
	cloudCfg XiaomiCloudConfig
}

// NewPluginServer creates a new PluginServer ready for use.
func NewPluginServer() *PluginServer {
	return &PluginServer{
		recorders: make(map[string]*recorderState),
	}
}

// GetPluginInfo returns Xiaomi plugin metadata.
func (s *PluginServer) GetPluginInfo(_ context.Context, _ *gen.Empty) (*gen.PluginInfo, error) {
	return &gen.PluginInfo{
		Name:     "xiaomi",
		Version:  "1.0.0",
		Protocols: []string{"xiaomi"},
		Capabilities: &gen.Capabilities{
			Discovery: true,
		},
		SupportedEncodings: []gen.Codec{gen.Codec_CODEC_H264, gen.Codec_CODEC_H265},
	}, nil
}

// StartRecorder begins streaming NAL frames from a Xiaomi camera.
func (s *PluginServer) StartRecorder(cfg *gen.RecorderConfig, stream grpc.ServerStreamingServer[gen.Frame]) error {
	cameraID := cfg.GetCameraId()
	ctx := stream.Context()

	rs := &recorderState{
		cameraID:  cameraID,
		state:     gen.RecorderState_RECORDER_STATE_RECORDING,
		startTime: time.Now(),
		cancel:    func() {}, // stream context controls lifecycle
	}

	s.mu.Lock()
	if existing, ok := s.recorders[cameraID]; ok {
		existing.cancel()
	}
	s.recorders[cameraID] = rs
	s.mu.Unlock()

	// Build StreamRecorder config from the gRPC RecorderConfig options.
	did := cfg.GetOptions()["did"]
	if did == "" {
		did = extractDID(cfg.GetUrl())
	}
	modelName := cfg.GetOptions()["model"]
	if modelName == "" {
		modelName = "unknown"
	}

	streamCfg := StreamRecorderConfig{
		CameraID:    cameraID,
		DID:         did,
		Model:       modelName,
		CloudCfg:    s.GetCloudConfig(),
		MaxBackoff:  defaultStreamMaxBackoff,
		InitBackoff: defaultStreamInitBackoff,
	}

	sender := &streamSender{
		stream:       stream,
		bytesWritten: &rs.bytesWritten,
	}

	rec := NewStreamRecorder(streamCfg, sender)
	grpcServerLogger.Info("starting Xiaomi stream recorder", "camera_id", cameraID, "did", did, "model", modelName)

	if err := rec.Start(ctx); err != nil {
		s.mu.Lock()
		rs.state = gen.RecorderState_RECORDER_STATE_ERROR
		rs.errMsg = err.Error()
		s.mu.Unlock()
		return fmt.Errorf("stream recorder start failed for %q: %w", cameraID, err)
	}

	// Block until stream context is cancelled (client disconnect or StopRecorder).
	<-ctx.Done()

	_ = rec.Stop()

	s.mu.Lock()
	rs.state = gen.RecorderState_RECORDER_STATE_STOPPED
	s.mu.Unlock()

	grpcServerLogger.Info("recorder stopped", "camera_id", cameraID)
	return nil
}

// StopRecorder cancels the frame streaming for the given camera.
func (s *PluginServer) StopRecorder(_ context.Context, req *gen.StopRequest) (*gen.StopResponse, error) {
	s.mu.Lock()
	rs, ok := s.recorders[req.GetCameraId()]
	if ok {
		rs.cancel()
		delete(s.recorders, req.GetCameraId())
	}
	s.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("recorder not found for camera: %s", req.GetCameraId())
	}

	grpcServerLogger.Info("recorder stop requested", "camera_id", req.GetCameraId())
	return &gen.StopResponse{}, nil
}

// GetRecorderStatus returns the current state of a recorder.
func (s *PluginServer) GetRecorderStatus(_ context.Context, req *gen.StatusRequest) (*gen.RecorderStatus, error) {
	s.mu.RLock()
	rs, ok := s.recorders[req.GetCameraId()]
	s.mu.RUnlock()

	if !ok {
		return &gen.RecorderStatus{
			State: gen.RecorderState_RECORDER_STATE_IDLE,
		}, nil
	}

	uptime := time.Since(rs.startTime).Nanoseconds()
	return &gen.RecorderStatus{
		State:           rs.state,
		ErrorMsg:        rs.errMsg,
		BytesRecorded:   rs.bytesWritten.Load(),
		SegmentsCreated: rs.segments.Load(),
		UptimeNs:        uint64(uptime),
	}, nil
}

// HealthCheck returns the plugin health status.
func (s *PluginServer) HealthCheck(_ context.Context, _ *gen.Empty) (*gen.HealthCheckResponse, error) {
	return &gen.HealthCheckResponse{
		Healthy: true,
		Message: "xiaomi plugin v1.0.0 - ok",
	}, nil
}

// SetCloudConfig stores Xiaomi cloud credentials for URL resolution.
func (s *PluginServer) SetCloudConfig(_ context.Context, cfg *gen.CloudConfig) (*gen.CloudConfigResponse, error) {
	s.cloudMu.Lock()
	defer s.cloudMu.Unlock()

	s.cloudCfg = XiaomiCloudConfig{
		UserID: cfg.GetUserId(),
		Token:  cfg.GetServiceToken(),
		Region: cfg.GetRegion(),
	}

	grpcServerLogger.Info("cloud config updated", "user_id", cfg.GetUserId(), "region", cfg.GetRegion())

	return &gen.CloudConfigResponse{
		Success: true,
		Message: "cloud config accepted",
	}, nil
}

// GetCloudConfig returns the stored cloud config.
func (s *PluginServer) GetCloudConfig() XiaomiCloudConfig {
	s.cloudMu.RLock()
	defer s.cloudMu.RUnlock()
	return s.cloudCfg
}
