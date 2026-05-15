// Package main - Mock gRPC PluginService server for integration testing.
// Sends synthetic H.264 NAL frames when StartRecorder is called.
package main

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/plugin/proto/gen"
	"google.golang.org/grpc"
)

// Default constants for the mock recorder.
const (
	DefaultFrameRate  = 30              // frames per second
	DefaultFramePTS   = 33 * time.Millisecond // ~30fps
	IDRFrameSize      = 10240           // ~10KB
	PFrameSize        = 5120            // ~5KB
)

// NAL unit start code prefix (4-byte Annex B).
var startCode = []byte{0x00, 0x00, 0x00, 0x01}

// mockState tracks a single recorder instance's state.
type mockState struct {
	cameraID     string
	state        gen.RecorderState
	errMsg       string
	bytesWritten atomic.Int64
	segments     atomic.Int64
	startTime    time.Time
	cancel       context.CancelFunc
}

// MockServer implements gen.PluginServiceServer for integration testing.
type MockServer struct {
	gen.UnimplementedPluginServiceServer

	mu        sync.RWMutex
	recorders map[string]*mockState

	// StartError can be set to inject a failure in StartRecorder.
	StartError error
}

// NewMockServer creates a new MockServer ready for use.
func NewMockServer() *MockServer {
	return &MockServer{
		recorders: make(map[string]*mockState),
	}
}

// GetPluginInfo returns mock plugin metadata.
func (s *MockServer) GetPluginInfo(_ context.Context, _ *gen.Empty) (*gen.PluginInfo, error) {
	return &gen.PluginInfo{
		Name:    "mock",
		Version: "0.1.0",
		Protocols: []string{"mock"},
		Capabilities: &gen.Capabilities{
			Hls:       false,
			Ptz:       false,
			Snapshot:  false,
			Discovery: false,
			Auth:      false,
		},
		SupportedEncodings: []gen.Codec{gen.Codec_CODEC_H264},
	}, nil
}

// StartRecorder begins streaming synthetic H.264 NAL frames.
func (s *MockServer) StartRecorder(cfg *gen.RecorderConfig, stream grpc.ServerStreamingServer[gen.Frame]) error {
	if s.StartError != nil {
		return s.StartError
	}

	ctx, cancel := context.WithCancel(context.Background())

	ms := &mockState{
		cameraID:  cfg.GetCameraId(),
		state:     gen.RecorderState_RECORDER_STATE_RECORDING,
		startTime: time.Now(),
		cancel:    cancel,
	}

	s.mu.Lock()
	// Cancel existing recorder for this camera if any
	if existing, ok := s.recorders[cfg.GetCameraId()]; ok {
		existing.cancel()
	}
	s.recorders[cfg.GetCameraId()] = ms
	s.mu.Unlock()

	frameSeq := 0
	ptsNs := uint64(0)
	ticker := time.NewTicker(DefaultFramePTS)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			ms.state = gen.RecorderState_RECORDER_STATE_STOPPED
			s.mu.Unlock()
			return nil
		case <-ticker.C:
		}

		frameSeq++

		// Send SPS on first iteration
		if frameSeq == 1 {
			frame := &gen.Frame{
				Data:        append(startCode, 0x67, 0x42, 0xc0, 0x1e, 0xd9, 0x00, 0x00),
				IsCodecInfo: true,
				Codec:       gen.Codec_CODEC_H264,
				Extra: map[string]string{
					"sps_hex": "6742c01ed90000",
				},
			}
			if err := stream.Send(frame); err != nil {
				return err
			}
			ms.bytesWritten.Add(int64(len(frame.Data)))
		}

		// Send PPS on first iteration (after SPS)
		if frameSeq == 1 {
			frame := &gen.Frame{
				Data:        append(startCode, 0x68, 0xce, 0x38, 0x80),
				IsCodecInfo: true,
				Codec:       gen.Codec_CODEC_H264,
				Extra: map[string]string{
					"pps_hex": "68ce3880",
				},
			}
			if err := stream.Send(frame); err != nil {
				return err
			}
			ms.bytesWritten.Add(int64(len(frame.Data)))
		}

		// Determine if this is an IDR (keyframe every 30 frames)
		isIDR := (frameSeq-1)%30 == 0
		var nalPayload []byte
		if isIDR {
			nalPayload = make([]byte, IDRFrameSize)
			nalPayload[0] = 0x65 // NAL type 5 (IDR) with nal_ref_idc=3
			_, _ = rand.Read(nalPayload[1:])
		} else {
			nalPayload = make([]byte, PFrameSize)
			nalPayload[0] = 0x41 // NAL type 1 (non-IDR) with nal_ref_idc=2
			_, _ = rand.Read(nalPayload[1:])
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
		ms.bytesWritten.Add(int64(len(frame.Data)))
		ptsNs += uint64(DefaultFramePTS.Nanoseconds())

		// Track segment boundaries (after each 30-frame group)
		if isIDR && frameSeq > 1 {
			ms.segments.Add(1)
		}
	}
}

// StopRecorder cancels the frame sending goroutine for the given camera.
func (s *MockServer) StopRecorder(_ context.Context, req *gen.StopRequest) (*gen.StopResponse, error) {
	s.mu.Lock()
	ms, ok := s.recorders[req.GetCameraId()]
	if ok {
		ms.cancel()
		delete(s.recorders, req.GetCameraId())
	}
	s.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("recorder not found for camera: %s", req.GetCameraId())
	}
	return &gen.StopResponse{}, nil
}

// GetRecorderStatus returns the current state of a recorder.
func (s *MockServer) GetRecorderStatus(_ context.Context, req *gen.StatusRequest) (*gen.RecorderStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ms, ok := s.recorders[req.GetCameraId()]
	if !ok {
		return &gen.RecorderStatus{
			State: gen.RecorderState_RECORDER_STATE_IDLE,
		}, nil
	}

	uptime := time.Since(ms.startTime).Nanoseconds()
	return &gen.RecorderStatus{
		State:           ms.state,
		ErrorMsg:        ms.errMsg,
		BytesRecorded:   ms.bytesWritten.Load(),
		SegmentsCreated: ms.segments.Load(),
		UptimeNs:        uint64(uptime),
	}, nil
}

// HealthCheck returns healthy.
func (s *MockServer) HealthCheck(_ context.Context, _ *gen.Empty) (*gen.HealthCheckResponse, error) {
	return &gen.HealthCheckResponse{
		Healthy: true,
		Message: "mock plugin v0.1.0 - ok",
	}, nil
}

// SetCloudConfig accepts cloud config (no-op for mock).
func (s *MockServer) SetCloudConfig(_ context.Context, _ *gen.CloudConfig) (*gen.CloudConfigResponse, error) {
	return &gen.CloudConfigResponse{
		Success: true,
		Message: "cloud config accepted",
	}, nil
}
