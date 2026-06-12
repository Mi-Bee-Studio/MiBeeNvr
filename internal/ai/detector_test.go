package ai

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

// mockStreamHub implements StreamSubscriber for testing.
type mockStreamHub struct {
	mu       sync.Mutex
	callback model.FrameCallback
}

func (h *mockStreamHub) Subscribe(id string, cb model.FrameCallback) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.callback = cb
	return nil
}

func (h *mockStreamHub) Unsubscribe(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.callback = nil
}

// deliverFrame simulates a frame arriving from the StreamHub.
func (h *mockStreamHub) deliverFrame(pts int64, au [][]byte) {
	h.mu.Lock()
	cb := h.callback
	h.mu.Unlock()
	if cb != nil {
		cb(pts, au)
	}
}

// mockPreprocessor implements Preprocessor for testing.
type mockPreprocessor struct {
	tensorFn func() []float32
	err      error
	callCount atomic.Int64
}

func (m *mockPreprocessor) Preprocess(frame []byte, width, height, inputSize int) ([]float32, error) {
	m.callCount.Add(1)
	if m.err != nil {
		return nil, m.err
	}
	if m.tensorFn != nil {
		return m.tensorFn(), nil
	}
	return make([]float32, 3*inputSize*inputSize), nil
}

// mockInferencer implements Inferencer for testing.
type mockInferencer struct {
	rawOutputFn func() [][]float32
	err         error
	available   bool
	callCount   atomic.Int64
}

func (m *mockInferencer) Infer(ctx context.Context, tensor []float32, dims []int64) ([][]float32, error) {
	m.callCount.Add(1)
	if m.err != nil {
		return nil, m.err
	}
	if m.rawOutputFn != nil {
		return m.rawOutputFn(), nil
	}
	return [][]float32{{}}, nil
}

func (m *mockInferencer) IsAvailable() bool { return m.available }

// mockPostprocessor implements Postprocessor for testing.
type mockPostprocessor struct {
	detectionsFn func() []Detection
	err          error
	callCount    atomic.Int64
}

func (m *mockPostprocessor) Postprocess(rawOutput [][]float32, dims []int64, inputSize int, threshold float64) ([]Detection, error) {
	m.callCount.Add(1)
	if m.err != nil {
		return nil, m.err
	}
	if m.detectionsFn != nil {
		return m.detectionsFn(), nil
	}
	return nil, nil
}

// mockMQTT implements MQTTPublisher for testing.
type mockMQTT struct {
	mu    sync.Mutex
	calls []mqttCall
}

type mqttCall struct {
	cameraID   string
	event      string
	detections []MQTTDetection
}

func (m *mockMQTT) PublishAIDetection(ctx context.Context, cameraID, eventType string, detections []MQTTDetection) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, mqttCall{cameraID: cameraID, event: eventType, detections: detections})
	return nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// testDetectorConfig holds optional overrides for testDetectorSetup.
type testDetectorConfig struct {
	cameraID      string
	frameSkipRate int
	maxGoroutines int
	withMQTT      bool
	preprocessor  Preprocessor
	inferencer    Inferencer
	postprocessor Postprocessor
}

// testDetectorSetup creates a CameraDetector with mock dependencies.
// Returns the detector, mock hub, event bus, and optional mock MQTT.
func testDetectorSetup(t *testing.T, cfg testDetectorConfig) (*CameraDetector, *mockStreamHub, *event.EventBus, *mockMQTT) {
	t.Helper()

	hub := &mockStreamHub{}
	bus := event.NewEventBus(64)

	pre := cfg.preprocessor
	if pre == nil {
		pre = &mockPreprocessor{}
	}

	inf := cfg.inferencer
	if inf == nil {
		inf = &mockInferencer{
			rawOutputFn: func() [][]float32 { return [][]float32{{}} },
			available:   true,
		}
	}

	post := cfg.postprocessor
	if post == nil {
		post = &mockPostprocessor{
			detectionsFn: func() []Detection { return nil },
		}
	}

	engine := &InferenceEngine{
		Preprocessor:  pre,
		Inferencer:    inf,
		Postprocessor: post,
		ModelInfo: ModelInfo{
			Name:      "test-model",
			InputSize: 640,
		},
	}

	frameSkipRate := cfg.frameSkipRate
	if frameSkipRate <= 0 {
		frameSkipRate = 10
	}

	maxGoroutines := cfg.maxGoroutines
	if maxGoroutines <= 0 {
		maxGoroutines = 2
	}

	var mqttPub MQTTPublisher
	var mqttRaw *mockMQTT
	if cfg.withMQTT {
		mqttRaw = &mockMQTT{}
		mqttPub = mqttRaw
	}

	cameraID := cfg.cameraID
	if cameraID == "" {
		cameraID = "test-cam"
	}

	detector := NewCameraDetector(CameraDetectorConfig{
		CameraID:      cameraID,
		Engine:        engine,
		Hub:           hub,
		Bus:           bus,
		MQTT:          mqttPub,
		FrameSkipRate: frameSkipRate,
		MaxGoroutines: maxGoroutines,
	})

	return detector, hub, bus, mqttRaw
}

// createTestJPEGFrame produces a JPEG access unit of the given dimensions.
func createTestJPEGFrame(t *testing.T, width, height int) [][]byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.NRGBA{
				R: uint8((x * 255) / width),
				G: uint8((y * 255) / height),
				B: 128,
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 70})
	require.NoError(t, err)
	return [][]byte{buf.Bytes()}
}

// jpegFrame is a small reusable JPEG test frame.
var jpegFrame = func() [][]byte {
	img := image.NewNRGBA(image.Rect(0, 0, 320, 240))
	for y := 0; y < 240; y++ {
		for x := 0; x < 320; x++ {
			img.Set(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 50})
	return [][]byte{buf.Bytes()}
}()

// h264Frame simulates an H.264 access unit (Phase 2 — should be skipped).
var h264Frame = [][]byte{
	{0x67, 0x42, 0x00, 0x1e, 0xab}, // SPS
	{0x68, 0xce, 0x3c, 0x80},       // PPS
	{0x65, 0x88, 0x84, 0x00, 0x01}, // IDR slice
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestCameraDetector_StartStop(t *testing.T) {
	detector, _, _, _ := testDetectorSetup(t, testDetectorConfig{})
	ctx := context.Background()

	// Start.
	err := detector.Start(ctx)
	require.NoError(t, err)
	assert.True(t, detector.IsRunning())

	// Double start should return an error.
	err = detector.Start(ctx)
	assert.Error(t, err)

	// Stop.
	detector.Stop()
	assert.False(t, detector.IsRunning())

	// Double stop should be safe.
	detector.Stop()
	assert.False(t, detector.IsRunning())
}

func TestCameraDetector_ProcessesJPEGFrames(t *testing.T) {
	// Create a postprocessor that returns known detections.
	expectedDets := []Detection{
		{ClassLabel: "person", Confidence: 0.95, BBox: [4]float64{0.1, 0.2, 0.3, 0.4}, ClassID: 0},
		{ClassLabel: "car", Confidence: 0.85, BBox: [4]float64{0.5, 0.6, 0.7, 0.8}, ClassID: 2},
	}

	post := &mockPostprocessor{
		detectionsFn: func() []Detection { return expectedDets },
	}

	detector, hub, bus, _ := testDetectorSetup(t, testDetectorConfig{
		frameSkipRate: 1, // process every frame
		maxGoroutines: 2,
		postprocessor: post,
	})

	// Subscribe to the detection event topic.
	eventCh := make(chan event.Event, 16)
	err := bus.Subscribe(event.TopicAIDetection, eventCh, 16)
	require.NoError(t, err)
	defer bus.Unsubscribe(event.TopicAIDetection, eventCh)

	ctx := context.Background()
	err = detector.Start(ctx)
	require.NoError(t, err)
	defer detector.Stop()

	// Deliver a JPEG frame.
	hub.deliverFrame(1000, jpegFrame)

	// Wait for the event (with timeout).
	select {
	case evt := <-eventCh:
		detectionEvt, ok := evt.Data.(DetectionEvent)
		require.True(t, ok, "event data should be DetectionEvent")
		assert.Equal(t, "test-cam", detectionEvt.CameraID)
		assert.Len(t, detectionEvt.Detections, 2)
		assert.Equal(t, "person", detectionEvt.Detections[0].ClassLabel)
		assert.Equal(t, "car", detectionEvt.Detections[1].ClassLabel)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for detection event")
	}
}

func TestCameraDetector_FrameSkip(t *testing.T) {
	post := &mockPostprocessor{
		detectionsFn: func() []Detection {
			return []Detection{{ClassLabel: "person", Confidence: 0.9, BBox: [4]float64{0, 0, 0.5, 0.5}}}
		},
	}
	pre := &mockPreprocessor{}
	inf := &mockInferencer{
		rawOutputFn: func() [][]float32 { return [][]float32{{}} },
	}

	detector, hub, bus, _ := testDetectorSetup(t, testDetectorConfig{
		frameSkipRate: 3, // process every 3rd frame
		maxGoroutines: 2,
		preprocessor:  pre,
		inferencer:    inf,
		postprocessor: post,
	})

	eventCh := make(chan event.Event, 16)
	err := bus.Subscribe(event.TopicAIDetection, eventCh, 16)
	require.NoError(t, err)
	defer bus.Unsubscribe(event.TopicAIDetection, eventCh)

	ctx := context.Background()
	err = detector.Start(ctx)
	require.NoError(t, err)
	defer detector.Stop()

	// Deliver 5 JPEG frames. With frameSkipRate=3, frames at count 3 should be processed.
	for i := 0; i < 5; i++ {
		hub.deliverFrame(int64(i), jpegFrame)
	}

	// Wait for processing.
	time.Sleep(500 * time.Millisecond)

	// Only frame 3 should have been processed (count 3 is the 3rd increment).
	// Frame counts: 1(skip), 2(skip), 3(process), 4(skip), 5(skip) = 1 processed.
	// Actually 5/3 = 1 frame processed.
	assert.Equal(t, int64(1), post.callCount.Load(), "postprocessor should have been called once")
}

func TestCameraDetector_NonBlockingCallback(t *testing.T) {
	// Use a slow inferencer to keep workers busy.
	releaseCh := make(chan struct{})
	inf := &mockInferencer{
		rawOutputFn: func() [][]float32 {
			<-releaseCh // block until released
			return [][]float32{{}}
		},
		available: true,
	}

	detector, hub, _, _ := testDetectorSetup(t, testDetectorConfig{
		frameSkipRate: 1,  // process every frame
		maxGoroutines: 1,  // single worker, buffer size = 1
		inferencer:    inf,
	})

	ctx := context.Background()
	err := detector.Start(ctx)
	require.NoError(t, err)
	defer detector.Stop()

	// Deliver first frame — it will be consumed by the worker and block on releaseCh.
	hub.deliverFrame(0, jpegFrame)

	// Give the worker time to pick up the job.
	time.Sleep(50 * time.Millisecond)

	// Deliver second frame — it will go into the buffer (buffer size = 1).
	hub.deliverFrame(1, jpegFrame)

	// Deliver third frame — buffer is full, should be dropped (non-blocking).
	done := make(chan struct{})
	go func() {
		hub.deliverFrame(2, jpegFrame)
		close(done)
	}()

	select {
	case <-done:
		// Callback returned without blocking — success.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("onFrame blocked when worker channel was full")
	}

	// Release the blocked worker.
	close(releaseCh)
}

func TestCameraDetector_SkipsNonJPEG(t *testing.T) {
	post := &mockPostprocessor{
		detectionsFn: func() []Detection {
			return []Detection{{ClassLabel: "person", Confidence: 0.9, BBox: [4]float64{0, 0, 0.5, 0.5}}}
		},
	}
	pre := &mockPreprocessor{}

	detector, hub, bus, _ := testDetectorSetup(t, testDetectorConfig{
		frameSkipRate: 1, // process every frame
		maxGoroutines: 2,
		preprocessor:  pre,
		postprocessor: post,
	})

	eventCh := make(chan event.Event, 16)
	err := bus.Subscribe(event.TopicAIDetection, eventCh, 16)
	require.NoError(t, err)
	defer bus.Unsubscribe(event.TopicAIDetection, eventCh)

	ctx := context.Background()
	err = detector.Start(ctx)
	require.NoError(t, err)
	defer detector.Stop()

	// Deliver an H.264 frame — should be skipped.
	hub.deliverFrame(0, h264Frame)

	// Deliver a JPEG frame — should be processed.
	hub.deliverFrame(1, jpegFrame)

	// Wait for event from JPEG frame.
	select {
	case <-eventCh:
		// Got detection event from JPEG.
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for detection event — non-JPEG may have blocked pipeline")
	}

	// Verify the preprocessor was only called once (for the JPEG frame).
	assert.Equal(t, int64(1), pre.callCount.Load(), "preprocessor should only be called once")

	// Verify no more events (only one detection).
	select {
	case <-eventCh:
		t.Fatal("unexpected second event — non-JPEG frame should have been skipped")
	case <-time.After(200 * time.Millisecond):
		// Expected: no more events.
	}
}

func TestCameraDetector_PublishesEvents(t *testing.T) {
	dets := []Detection{
		{ClassLabel: "person", Confidence: 0.95, BBox: [4]float64{0.1, 0.2, 0.3, 0.4}, ClassID: 0},
		{ClassLabel: "car", Confidence: 0.85, BBox: [4]float64{0.5, 0.6, 0.7, 0.8}, ClassID: 2},
		{ClassLabel: "dog", Confidence: 0.75, BBox: [4]float64{0.2, 0.3, 0.4, 0.5}, ClassID: 16},
		{ClassLabel: "bicycle", Confidence: 0.65, BBox: [4]float64{0.3, 0.4, 0.5, 0.6}, ClassID: 1},
	}

	post := &mockPostprocessor{
		detectionsFn: func() []Detection { return dets },
	}

	detector, hub, bus, mqttPub := testDetectorSetup(t, testDetectorConfig{
		frameSkipRate: 1,
		maxGoroutines: 2,
		withMQTT:      true,
		postprocessor: post,
	})

	// Subscribe to all AI topics.
	aiCh := make(chan event.Event, 16)
	personCh := make(chan event.Event, 16)
	vehicleCh := make(chan event.Event, 16)
	animalCh := make(chan event.Event, 16)

	bus.Subscribe(event.TopicAIDetection, aiCh, 16)
	bus.Subscribe(event.TopicAIPerson, personCh, 16)
	bus.Subscribe(event.TopicAIVehicle, vehicleCh, 16)
	bus.Subscribe(event.TopicAIAnimal, animalCh, 16)

	defer bus.Unsubscribe(event.TopicAIDetection, aiCh)
	defer bus.Unsubscribe(event.TopicAIPerson, personCh)
	defer bus.Unsubscribe(event.TopicAIVehicle, vehicleCh)
	defer bus.Unsubscribe(event.TopicAIAnimal, animalCh)

	ctx := context.Background()
	err := detector.Start(ctx)
	require.NoError(t, err)
	defer detector.Stop()

	hub.deliverFrame(0, jpegFrame)

	// Wait for all expected events.
	time.Sleep(500 * time.Millisecond)

	// Check general detection topic.
	select {
	case evt := <-aiCh:
		de, ok := evt.Data.(DetectionEvent)
		require.True(t, ok)
		assert.Len(t, de.Detections, 4)
	default:
		t.Fatal("expected detection event on ai.detection")
	}

	// Check person topic (person).
	select {
	case evt := <-personCh:
		de, ok := evt.Data.(DetectionEvent)
		require.True(t, ok)
		assert.Equal(t, "test-cam", de.CameraID)
	default:
		t.Fatal("expected event on ai.detection.person")
	}

	// Check vehicle topic (car, bicycle).
	select {
	case evt := <-vehicleCh:
		de, ok := evt.Data.(DetectionEvent)
		require.True(t, ok)
		// Event should contain at least one vehicle detection.
		assert.Equal(t, "test-cam", de.CameraID)
	default:
		t.Fatal("expected event on ai.detection.vehicle")
	}

	// Check animal topic (dog).
	select {
	case evt := <-animalCh:
		de, ok := evt.Data.(DetectionEvent)
		require.True(t, ok)
		assert.Equal(t, "test-cam", de.CameraID)
	default:
		t.Fatal("expected event on ai.detection.animal")
	}

	// Check MQTT was called.
	require.NotNil(t, mqttPub)
	mqttPub.mu.Lock()
	calls := len(mqttPub.calls)
	mqttPub.mu.Unlock()
	assert.Equal(t, 1, calls, "MQTT should have been called once")

	if calls > 0 {
		mqttPub.mu.Lock()
		call := mqttPub.calls[0]
		mqttPub.mu.Unlock()
		assert.Equal(t, "test-cam", call.cameraID)
		assert.Equal(t, "detection", call.event)
		assert.Len(t, call.detections, 4)
	}
}

func TestCameraDetector_PublishesEvents_NoDetections(t *testing.T) {
	// Postprocessor returns nil (no detections).
	post := &mockPostprocessor{
		detectionsFn: func() []Detection { return nil },
	}

	detector, hub, bus, mqttPub := testDetectorSetup(t, testDetectorConfig{
		frameSkipRate: 1,
		maxGoroutines: 2,
		withMQTT:      true,
		postprocessor: post,
	})

	eventCh := make(chan event.Event, 16)
	bus.Subscribe(event.TopicAIDetection, eventCh, 16)
	defer bus.Unsubscribe(event.TopicAIDetection, eventCh)

	ctx := context.Background()
	err := detector.Start(ctx)
	require.NoError(t, err)
	defer detector.Stop()

	hub.deliverFrame(0, jpegFrame)

	time.Sleep(300 * time.Millisecond)

	// No events should be published.
	select {
	case <-eventCh:
		t.Fatal("should not publish event when no detections")
	case <-time.After(200 * time.Millisecond):
	}

	// MQTT should not have been called.
	if mqttPub != nil {
		mqttPub.mu.Lock()
		assert.Empty(t, mqttPub.calls, "MQTT should not be called when no detections")
		mqttPub.mu.Unlock()
	}
}

func TestCameraDetector_MaxGoroutines(t *testing.T) {
	// Use a slow inferencer to keep workers busy.
	var infMu sync.Mutex
	blockedWorkers := 0
	releaseAll := make(chan struct{})

	inf := &mockInferencer{
		rawOutputFn: func() [][]float32 {
			infMu.Lock()
			blockedWorkers++
			infMu.Unlock()
			<-releaseAll // block until all are released
			return [][]float32{{}}
		},
		available: true,
	}

	post := &mockPostprocessor{
		detectionsFn: func() []Detection {
			return []Detection{{ClassLabel: "person", Confidence: 0.9, BBox: [4]float64{0, 0, 0.5, 0.5}}}
		},
	}

	detector, hub, _, _ := testDetectorSetup(t, testDetectorConfig{
		frameSkipRate: 1,
		maxGoroutines: 2, // 2 workers, buffer size = 2
		inferencer:    inf,
		postprocessor: post,
	})

	ctx := context.Background()
	err := detector.Start(ctx)
	require.NoError(t, err)
	defer detector.Stop()

	// Deliver frames until the buffer is full and some get dropped.
	for i := 0; i < 10; i++ {
		hub.deliverFrame(int64(i), jpegFrame)
	}

	// Give workers time to pick up jobs and block.
	time.Sleep(100 * time.Millisecond)

	// At this point both workers are blocked in Infer.
	// The buffer (size=2) is full with 2 frames.
	// Frames 5-9 should have been dropped.

	// Release all workers.
	close(releaseAll)

	// Wait for processing to complete.
	time.Sleep(500 * time.Millisecond)

	// The postprocessor should have been called at most 4 times (2 workers + 2 buffered).
	// It could be 2-4 depending on timing.
	calls := post.callCount.Load()
	assert.LessOrEqual(t, calls, int64(4), "should process at most 4 frames (2 workers + 2 buffered)")
	assert.GreaterOrEqual(t, calls, int64(2), "should process at least 2 frames (2 workers)")
}

func TestCameraDetector_StopWhileProcessing(t *testing.T) {
	blockCh := make(chan struct{})
	inf := &mockInferencer{
		rawOutputFn: func() [][]float32 {
			<-blockCh
			return [][]float32{{}}
		},
		available: true,
	}

	detector, hub, _, _ := testDetectorSetup(t, testDetectorConfig{
		frameSkipRate: 1,
		maxGoroutines: 1,
		inferencer:    inf,
	})

	ctx := context.Background()
	err := detector.Start(ctx)
	require.NoError(t, err)

	// Deliver a frame that will block the worker.
	hub.deliverFrame(0, jpegFrame)
	time.Sleep(50 * time.Millisecond)

	// Deliver one more frame (buffered).
	hub.deliverFrame(1, jpegFrame)

	// Stop while worker is blocked — should not hang.
	done := make(chan struct{})
	go func() {
		detector.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Stop completed.
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() hung while worker was blocked")
	}

	// Clean up blocked goroutines.
	close(blockCh)
}

func TestCameraDetector_SkipsFrameOnInferenceError(t *testing.T) {
	inf := &mockInferencer{
		err:       assert.AnError,
		available: true,
	}

	detector, hub, bus, _ := testDetectorSetup(t, testDetectorConfig{
		frameSkipRate: 1,
		maxGoroutines: 2,
		inferencer:    inf,
	})

	eventCh := make(chan event.Event, 16)
	bus.Subscribe(event.TopicAIDetection, eventCh, 16)
	defer bus.Unsubscribe(event.TopicAIDetection, eventCh)

	ctx := context.Background()
	err := detector.Start(ctx)
	require.NoError(t, err)
	defer detector.Stop()

	hub.deliverFrame(0, jpegFrame)

	time.Sleep(300 * time.Millisecond)

	select {
	case <-eventCh:
		t.Fatal("should not publish event when inference fails")
	case <-time.After(200 * time.Millisecond):
	}
}

// ---------------------------------------------------------------------------
// Unit tests for isJPEGFrame and concatAU
// ---------------------------------------------------------------------------

func TestIsJPEGFrame(t *testing.T) {
	tests := []struct {
		name string
		au   [][]byte
		want bool
	}{
		{
			name: "single JPEG frame with SOI",
			au:   [][]byte{{0xFF, 0xD8, 0xFF, 0xE0, 0x00}},
			want: true,
		},
		{
			name: "single H.264 NALU",
			au:   [][]byte{{0x67, 0x42, 0x00, 0x1e}},
			want: false,
		},
		{
			name: "empty access unit",
			au:   [][]byte{},
			want: false,
		},
		{
			name: "multi-AU with JPEG in first",
			au:   [][]byte{{0xFF, 0xD8, 0xFF}, {0x00, 0x01, 0x02}},
			want: true,
		},
		{
			name: "multi-AU with JPEG in second",
			au:   [][]byte{{0x00, 0x01}, {0xFF, 0xD8, 0xFF}},
			want: true,
		},
		{
			name: "too short NALU",
			au:   [][]byte{{0xFF}},
			want: false,
		},
		{
			name: "nil access unit",
			au:   nil,
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isJPEGFrame(tc.au)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestConcatAU(t *testing.T) {
	tests := []struct {
		name string
		au   [][]byte
		want []byte
	}{
		{
			name: "single NALU",
			au:   [][]byte{{0x01, 0x02, 0x03}},
			want: []byte{0x01, 0x02, 0x03},
		},
		{
			name: "multiple NALUs",
			au:   [][]byte{{0x01}, {0x02, 0x03}, {0x04}},
			want: []byte{0x01, 0x02, 0x03, 0x04},
		},
		{
			name: "empty access unit",
			au:   [][]byte{},
			want: []byte{},
		},
		{
			name: "empty NALUs",
			au:   [][]byte{{}, {0x01}, {}},
			want: []byte{0x01},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := concatAU(tc.au)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestCameraDetector_SkipsOnPreprocessError(t *testing.T) {
	pre := &mockPreprocessor{
		err: assert.AnError,
	}

	detector, hub, bus, _ := testDetectorSetup(t, testDetectorConfig{
		frameSkipRate: 1,
		maxGoroutines: 2,
		preprocessor:  pre,
	})

	eventCh := make(chan event.Event, 16)
	bus.Subscribe(event.TopicAIDetection, eventCh, 16)
	defer bus.Unsubscribe(event.TopicAIDetection, eventCh)

	ctx := context.Background()
	err := detector.Start(ctx)
	require.NoError(t, err)
	defer detector.Stop()

	hub.deliverFrame(0, jpegFrame)

	time.Sleep(300 * time.Millisecond)

	select {
	case <-eventCh:
		t.Fatal("should not publish event when preprocess fails")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestCameraDetector_NilEngineStartStop(t *testing.T) {
	// Verify that Start/Stop work correctly even with a nil engine.

	hub := &mockStreamHub{}
	detector := NewCameraDetector(CameraDetectorConfig{
		CameraID:      "nil-engine-cam",
		Engine:        nil, // intentionally nil
		Hub:           hub,
		FrameSkipRate: 1,
		MaxGoroutines: 1,
	})

	ctx := context.Background()
	err := detector.Start(ctx)
	require.NoError(t, err)
	assert.True(t, detector.IsRunning())

	// Stop should complete without hanging.
	detector.Stop()
	assert.False(t, detector.IsRunning())
}

func TestCameraDetector_EmptyFrameSkipped(t *testing.T) {
	detector, hub, bus, _ := testDetectorSetup(t, testDetectorConfig{
		frameSkipRate: 1,
		maxGoroutines: 2,
	})

	eventCh := make(chan event.Event, 16)
	bus.Subscribe(event.TopicAIDetection, eventCh, 16)
	defer bus.Unsubscribe(event.TopicAIDetection, eventCh)

	ctx := context.Background()
	err := detector.Start(ctx)
	require.NoError(t, err)
	defer detector.Stop()

	// Deliver empty access unit.
	hub.deliverFrame(0, [][]byte{})

	time.Sleep(200 * time.Millisecond)

	select {
	case <-eventCh:
		t.Fatal("should not publish event for empty frame")
	case <-time.After(100 * time.Millisecond):
	}
}
