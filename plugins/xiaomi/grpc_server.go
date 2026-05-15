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
//
// TODO(Task 10): Replace placeholder frame generation with real Xiaomi
// protocol (StreamRecorder + MISS client + CS2 transport).
// For now, sends synthetic H.264 frames to validate the gRPC plumbing.
func (s *PluginServer) StartRecorder(cfg *gen.RecorderConfig, stream grpc.ServerStreamingServer[gen.Frame]) error {
	cameraID := cfg.GetCameraId()
	ctx, cancel := context.WithCancel(stream.Context())

	rs := &recorderState{
		cameraID:  cameraID,
		state:     gen.RecorderState_RECORDER_STATE_RECORDING,
		startTime: time.Now(),
		cancel:    cancel,
	}

	s.mu.Lock()
	if existing, ok := s.recorders[cameraID]; ok {
		existing.cancel()
	}
	s.recorders[cameraID] = rs
	s.mu.Unlock()

	// Placeholder frame generation — will be replaced by real Xiaomi protocol
	// in Task 10 when StreamRecorder is integrated.
	startCode := []byte{0x00, 0x00, 0x00, 0x01}
	frameSeq := 0
	ptsNs := uint64(0)
	ticker := time.NewTicker(33 * time.Millisecond)
	defer ticker.Stop()

	grpcServerLogger.Info("recorder started (placeholder)", "camera_id", cameraID)

	for {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			rs.state = gen.RecorderState_RECORDER_STATE_STOPPED
			s.mu.Unlock()
			return nil
		case <-ticker.C:
		}

		frameSeq++

		if frameSeq == 1 {
			frame := &gen.Frame{
				Data:        append(startCode, 0x67, 0x42, 0xc0, 0x1e, 0xd9, 0x00, 0x00),
				IsCodecInfo: true,
				Codec:       gen.Codec_CODEC_H264,
				Extra:       map[string]string{"sps_hex": "6742c01ed90000"},
			}
			if err := stream.Send(frame); err != nil {
				return err
			}
			rs.bytesWritten.Add(int64(len(frame.Data)))
		}

		if frameSeq == 1 {
			frame := &gen.Frame{
				Data:        append(startCode, 0x68, 0xce, 0x38, 0x80),
				IsCodecInfo: true,
				Codec:       gen.Codec_CODEC_H264,
				Extra:       map[string]string{"pps_hex": "68ce3880"},
			}
			if err := stream.Send(frame); err != nil {
				return err
			}
			rs.bytesWritten.Add(int64(len(frame.Data)))
		}

		isIDR := (frameSeq-1)%30 == 0
		var nalPayload []byte
		if isIDR {
			nalPayload = make([]byte, 10240)
			nalPayload[0] = 0x65
		} else {
			nalPayload = make([]byte, 5120)
			nalPayload[0] = 0x41
		}

		frame := &gen.Frame{
			Data:  append(startCode, nalPayload...),
			PtsNs: ptsNs,
			IsIdr: isIDR,
			Codec: gen.Codec_CODEC_H264,
		}
		if err := stream.Send(frame); err != nil {
			return err
		}
		rs.bytesWritten.Add(int64(len(frame.Data)))
		ptsNs += uint64(33 * time.Millisecond.Nanoseconds())

		if isIDR && frameSeq > 1 {
			rs.segments.Add(1)
		}
	}
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
