package plugin

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"

	gen "github.com/Mi-Bee-Studio/MiBeeNvr/plugin/proto/gen"
)

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

// mockFrameHandler records frames received via HandleFrame.
type mockFrameHandler struct {
	mu       sync.Mutex
	frames   []*gen.Frame
	closed   bool
	err      error // if set, HandleFrame returns this error
	frameCh  chan *gen.Frame
}

func newMockFrameHandler() *mockFrameHandler {
	return &mockFrameHandler{
		frameCh: make(chan *gen.Frame, 100),
	}
}

func (h *mockFrameHandler) HandleFrame(_ context.Context, frame *gen.Frame) error {
	h.mu.Lock()
	h.frames = append(h.frames, frame)
	h.mu.Unlock()
	h.frameCh <- frame
	if h.err != nil {
		return h.err
	}
	return nil
}

func (h *mockFrameHandler) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	close(h.frameCh)
	return nil
}

func (h *mockFrameHandler) getFrames() []*gen.Frame {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.frames
}

// mockStream implements grpc.ServerStreamingClient[gen.Frame].
type mockStream struct {
	grpc.ClientStream
	frames   []*gen.Frame
	sendIdx  int
	errAfter int // return error after this many frames (0 = no error)
	err      error
	blockCh  chan struct{} // closed to unblock Recv when no more frames
	mu       sync.Mutex
}

func newMockStream(frames []*gen.Frame) *mockStream {
	return &mockStream{
		frames:  frames,
		blockCh: make(chan struct{}),
	}
}

func (s *mockStream) Recv() (*gen.Frame, error) {
	s.mu.Lock()
	sendIdx := s.sendIdx
	s.mu.Unlock()

	if s.errAfter > 0 && sendIdx >= s.errAfter {
		return nil, s.err
	}

	s.mu.Lock()
	if s.sendIdx >= len(s.frames) {
		s.mu.Unlock()
		// Block until test unblocks us or stream is done.
		<-s.blockCh
		return nil, s.err
	}
	frame := s.frames[s.sendIdx]
	s.sendIdx++
	s.mu.Unlock()
	return frame, nil
}

// mockPluginClient implements gen.PluginServiceClient.
type mockPluginClient struct {
	gen.PluginServiceClient // embed for forward compat (unimplemented)

	stream    *mockStream
	stopErr   error
	stopCalled atomic.Int32
	mu         sync.Mutex
}

func (m *mockPluginClient) StartRecorder(_ context.Context, _ *gen.RecorderConfig, _ ...grpc.CallOption) (grpc.ServerStreamingClient[gen.Frame], error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stream == nil {
		return nil, errors.New("no stream configured")
	}
	return m.stream, nil
}

func (m *mockPluginClient) StopRecorder(_ context.Context, req *gen.StopRequest, _ ...grpc.CallOption) (*gen.StopResponse, error) {
	m.stopCalled.Add(1)
	if m.stopErr != nil {
		return nil, m.stopErr
	}
	return &gen.StopResponse{}, nil
}

func (m *mockPluginClient) GetRecorderStatus(_ context.Context, _ *gen.StatusRequest, _ ...grpc.CallOption) (*gen.RecorderStatus, error) {
	return &gen.RecorderStatus{State: gen.RecorderState_RECORDER_STATE_RECORDING}, nil
}

func (m *mockPluginClient) setStream(frames []*gen.Frame) *mockStream {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := newMockStream(frames)
	s.err = context.Canceled
	m.stream = s
	return s
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func testCameraConfig() config.CameraConfig {
	return config.CameraConfig{
		ID:       "cam-test",
		Name:     "Test Camera",
		Protocol: "xiaomi",
		Encoding: "h264",
		URL:      "xiaomi://test-device",
		Username: "user",
		Password: "pass",
		DID:      "did123",
		Vendor:   "cs2",
	}
}

func makeIDRFrame(data []byte) *gen.Frame {
	return &gen.Frame{
		Data:       data,
		PtsNs:      1000000,
		IsIdr:      true,
		Codec:      gen.Codec_CODEC_H264,
		IsCodecInfo: false,
	}
}

func makeCodecInfoFrame(sps []byte) *gen.Frame {
	return &gen.Frame{
		Data:       sps,
		PtsNs:      0,
		IsIdr:      false,
		Codec:      gen.Codec_CODEC_H264,
		IsCodecInfo: true,
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestNewGRPCRecorderAdapter_DefaultSegmentDur(t *testing.T) {
	client := &mockPluginClient{}
	handler := newMockFrameHandler()
	cfg := testCameraConfig()
	adapter := NewGRPCRecorderAdapter(client, handler, cfg, 0)

	if adapter.segmentDur != 30*time.Second {
		t.Errorf("expected default segment dur 30s, got %v", adapter.segmentDur)
	}
	if adapter.Status() != model.StatusStopped {
		t.Errorf("expected initial status stopped, got %s", adapter.Status())
	}
}

func TestGRPCRecorderAdapter_StartStop(t *testing.T) {
	client := &mockPluginClient{}
	handler := newMockFrameHandler()

	frames := []*gen.Frame{
		makeCodecInfoFrame([]byte{0x67}), // SPS
		makeIDRFrame([]byte{0x65, 0x88, 0x80, 0x40}),
		makeIDRFrame([]byte{0x65, 0x01, 0x02, 0x03}),
	}
	stream := client.setStream(frames)

	cfg := testCameraConfig()
	adapter := NewGRPCRecorderAdapter(client, handler, cfg, 10*time.Second)

	// Start
	if err := adapter.Start(context.Background()); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Wait for frames to be received by handler.
	timeout := time.After(2 * time.Second)
	for len(handler.getFrames()) < 3 {
		select {
		case <-timeout:
			t.Fatalf("timed out waiting for frames, got %d", len(handler.getFrames()))
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Verify frames forwarded.
	received := handler.getFrames()
	if len(received) != 3 {
		t.Fatalf("expected 3 frames, got %d", len(received))
	}
	if !received[1].GetIsIdr() {
		t.Error("frame 1 should be IDR")
	}
	if !received[0].GetIsCodecInfo() {
		t.Error("frame 0 should be codec info")
	}

	// Verify status is recording.
	if adapter.Status() != model.StatusRecording {
		t.Errorf("expected recording status, got %s", adapter.Status())
	}

	// Unblock the stream so Stop() can complete.
	close(stream.blockCh)
	if err := adapter.Stop(); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}
	if adapter.Status() != model.StatusStopped {
		t.Errorf("expected stopped status, got %s", adapter.Status())
	}
	if client.stopCalled.Load() != 1 {
		t.Error("expected StopRecorder to be called once")
	}
	if !handler.closed {
		t.Error("expected handler to be closed")
	}
}

func TestGRPCRecorderAdapter_StartTwiceFails(t *testing.T) {
	client := &mockPluginClient{}
	handler := newMockFrameHandler()
	stream := client.setStream([]*gen.Frame{makeIDRFrame([]byte{0x01})})

	cfg := testCameraConfig()
	adapter := NewGRPCRecorderAdapter(client, handler, cfg, 10*time.Second)

	if err := adapter.Start(context.Background()); err != nil {
		t.Fatalf("first Start() error: %v", err)
	}

	// Second start should fail.
	if err := adapter.Start(context.Background()); err == nil {
		t.Fatal("expected error on double start")
	}

	close(stream.blockCh) // unblock mock before Stop
	adapter.Stop()
}

func TestGRPCRecorderAdapter_StreamError(t *testing.T) {
	client := &mockPluginClient{}
	handler := newMockFrameHandler()

	frames := []*gen.Frame{makeIDRFrame([]byte{0x01})}
	stream := newMockStream(frames)
	stream.errAfter = 1
	stream.err = errors.New("plugin crashed")
	client.stream = stream

	cfg := testCameraConfig()
	adapter := NewGRPCRecorderAdapter(client, handler, cfg, 10*time.Second)

	if err := adapter.Start(context.Background()); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Wait for stream to end with error.
	timeout := time.After(2 * time.Second)
	for adapter.Status() == model.StatusRecording {
		select {
		case <-timeout:
			t.Fatal("timed out waiting for error status")
		case <-time.After(10 * time.Millisecond):
		}
	}

	if adapter.Status() != model.StatusError {
		t.Errorf("expected error status, got %s", adapter.Status())
	}
}

func TestGRPCRecorderAdapter_HandlerError(t *testing.T) {
	client := &mockPluginClient{}
	handler := newMockFrameHandler()
	handler.err = errors.New("handler failed")

	frames := []*gen.Frame{makeIDRFrame([]byte{0x01})}
	client.setStream(frames)

	cfg := testCameraConfig()
	adapter := NewGRPCRecorderAdapter(client, handler, cfg, 10*time.Second)

	if err := adapter.Start(context.Background()); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Wait for handler error to propagate.
	timeout := time.After(2 * time.Second)
	for adapter.Status() == model.StatusRecording {
		select {
		case <-timeout:
			t.Fatal("timed out waiting for error status")
		case <-time.After(10 * time.Millisecond):
		}
	}

	if adapter.Status() != model.StatusError {
		t.Errorf("expected error status after handler failure, got %s", adapter.Status())
	}
}

func TestGRPCRecorderAdapter_StopCancelsStream(t *testing.T) {
	client := &mockPluginClient{}
	handler := newMockFrameHandler()

	// Stream that blocks forever on Recv.
	stream := newMockStream(nil)
	stream.err = context.Canceled
	client.stream = stream

	cfg := testCameraConfig()
	adapter := NewGRPCRecorderAdapter(client, handler, cfg, 10*time.Second)

	if err := adapter.Start(context.Background());
	err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Stop the adapter — this cancels the internal stream context.
	if err := adapter.Stop(); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}

	// Status should be stopped (clean exit via Stop()).
	if s := adapter.Status(); s != model.StatusStopped {
		t.Errorf("expected stopped after Stop(), got %s", s)
	}
}

func TestGRPCRecorderAdapter_StartRPCFailure(t *testing.T) {
	client := &mockPluginClient{}
	handler := newMockFrameHandler()
	// No stream set → StartRecorder returns error.

	cfg := testCameraConfig()
	adapter := NewGRPCRecorderAdapter(client, handler, cfg, 10*time.Second)

	if err := adapter.Start(context.Background()); err == nil {
		t.Fatal("expected error when StartRecorder RPC fails")
	}

	if adapter.Status() != model.StatusError {
		t.Errorf("expected error status after RPC failure, got %s", adapter.Status())
	}
}

func TestGRPCRecorderAdapter_BuildRecorderConfig(t *testing.T) {
	client := &mockPluginClient{}
	handler := newMockFrameHandler()
	cfg := config.CameraConfig{
		ID:       "cam-1",
		Name:     "My Camera",
		Protocol: "xiaomi",
		Encoding: "h265",
		URL:      "xiaomi://device1",
		Username: "admin",
		Password: "secret",
		DID:      "did456",
		Vendor:   "cs2",
	}

	adapter := NewGRPCRecorderAdapter(client, handler, cfg, 5*time.Minute)
	rc := adapter.buildRecorderConfig()

	if rc.CameraId != "cam-1" {
		t.Errorf("expected camera_id=cam-1, got %s", rc.CameraId)
	}
	if rc.Name != "My Camera" {
		t.Errorf("expected name=My Camera, got %s", rc.Name)
	}
	if rc.Url != "xiaomi://device1" {
		t.Errorf("expected url=xiaomi://device1, got %s", rc.Url)
	}
	if rc.Username != "admin" {
		t.Errorf("expected username=admin, got %s", rc.Username)
	}
	if rc.Password != "secret" {
		t.Errorf("expected password=secret, got %s", rc.Password)
	}
	if rc.Encoding != "h265" {
		t.Errorf("expected encoding=h265, got %s", rc.Encoding)
	}
	if rc.SegmentDurationNs != uint64(5*time.Minute) {
		t.Errorf("expected segment_duration_ns=%d, got %d", uint64(5*time.Minute), rc.SegmentDurationNs)
	}
	if rc.Options["did"] != "did456" {
		t.Errorf("expected options.did=did456, got %s", rc.Options["did"])
	}
	if rc.Options["vendor"] != "cs2" {
		t.Errorf("expected options.vendor=cs2, got %s", rc.Options["vendor"])
	}
}

func TestGRPCRecorderAdapter_StopWithoutStart(t *testing.T) {
	client := &mockPluginClient{}
	handler := newMockFrameHandler()
	cfg := testCameraConfig()
	adapter := NewGRPCRecorderAdapter(client, handler, cfg, 10*time.Second)

	// Stop on never-started adapter should not panic.
	if err := adapter.Stop(); err != nil {
		t.Fatalf("Stop() on unstarted adapter should not error: %v", err)
	}
}

func TestGRPCRecorderAdapter_StopRPCFailure(t *testing.T) {
	client := &mockPluginClient{}
	handler := newMockFrameHandler()

	// Set up stream that ends quickly.
	frames := []*gen.Frame{makeIDRFrame([]byte{0x01})}
	stream := newMockStream(frames)
	stream.errAfter = 1
	stream.err = errors.New("stream done")
	client.stream = stream
	client.stopErr = errors.New("plugin unreachable")

	cfg := testCameraConfig()
	adapter := NewGRPCRecorderAdapter(client, handler, cfg, 10*time.Second)

	if err := adapter.Start(context.Background()); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Wait for stream to end.
	time.Sleep(100 * time.Millisecond)

	// Stop should succeed even if StopRecorder RPC fails.
	if err := adapter.Stop(); err != nil {
		t.Fatalf("Stop() should not fail even if RPC fails: %v", err)
	}
	if adapter.Status() != model.StatusStopped {
		t.Errorf("expected stopped, got %s", adapter.Status())
	}
}

func TestGRPCRecorderAdapter_ManyFrames(t *testing.T) {
	client := &mockPluginClient{}
	handler := newMockFrameHandler()

	const numFrames = 50
	frames := make([]*gen.Frame, numFrames)
	for i := range frames {
		frames[i] = makeIDRFrame([]byte{byte(i)})
	}
	stream := client.setStream(frames)

	cfg := testCameraConfig()
	adapter := NewGRPCRecorderAdapter(client, handler, cfg, 10*time.Second)

	if err := adapter.Start(context.Background()); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Wait for all frames.
	timeout := time.After(3 * time.Second)
	for len(handler.getFrames()) < numFrames {
		select {
		case <-timeout:
			t.Fatalf("timed out waiting for frames, got %d/%d", len(handler.getFrames()), numFrames)
		case <-time.After(10 * time.Millisecond):
		}
	}

	close(stream.blockCh)
	if err := adapter.Stop(); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}

	received := handler.getFrames()
	if len(received) != numFrames {
		t.Errorf("expected %d frames, got %d", numFrames, len(received))
	}
}

func TestGRPCRecorderAdapter_FrameFields(t *testing.T) {
	client := &mockPluginClient{}
	handler := newMockFrameHandler()

	frame := &gen.Frame{
		Data:        []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x88},
		PtsNs:       33_000_000, // 33ms
		IsIdr:       true,
		Codec:       gen.Codec_CODEC_H265,
		IsCodecInfo: false,
		Extra:       map[string]string{"sps_hex": "abcdef"},
	}
	stream := client.setStream([]*gen.Frame{frame})

	cfg := testCameraConfig()
	adapter := NewGRPCRecorderAdapter(client, handler, cfg, 10*time.Second)

	if err := adapter.Start(context.Background()); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Wait for frame.
	timeout := time.After(2 * time.Second)
	for len(handler.getFrames()) < 1 {
		select {
		case <-timeout:
			t.Fatal("timed out waiting for frame")
		case <-time.After(10 * time.Millisecond):
		}
	}

	close(stream.blockCh)
	adapter.Stop()

	received := handler.getFrames()
	if len(received) != 1 {
		t.Fatalf("expected 1 frame, got %d", len(received))
	}
	rf := received[0]
	if rf.Codec != gen.Codec_CODEC_H265 {
		t.Errorf("expected H265 codec, got %v", rf.Codec)
	}
	if rf.PtsNs != 33_000_000 {
		t.Errorf("expected pts_ns=33000000, got %d", rf.PtsNs)
	}
	if rf.Extra["sps_hex"] != "abcdef" {
		t.Errorf("expected extra sps_hex=abcdef, got %s", rf.Extra["sps_hex"])
	}
}
