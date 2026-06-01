package engine

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/ai"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// --- Helpers ---

// newMockEngineSimple creates a minimal mock engine without pipe wiring.
// It returns an Engine that appears running but will fail Detect() if called
// through the normal path. For testing AiDetector lifecycle, use this
// and inject a custom OnDetection callback.
func newMockEngineSimple(t *testing.T) *Engine {
	t.Helper()
	e, _ := setupMockEngine(t)
	return e
}

// sendFrame sends a frame to a StreamHub as if a recorder broadcast it.
func sendFrame(t *testing.T, hub *model.StreamHub, pts int64, au [][]byte) {
	t.Helper()
	hub.Broadcast(pts, au, false)
}

// makeNALU creates a simple NALU with the given header byte and data.
func makeNALU(header byte, data []byte) []byte {
	nalu := make([]byte, len(data)+1)
	nalu[0] = header
	copy(nalu[1:], data)
	return nalu
}

// mockEngineWithResponses creates a running Engine with a goroutine that
// reads Detect requests and writes canned responses.
func mockEngineWithResponses(t *testing.T, responses []detectResponse) (*Engine, *mockPipes) {
	t.Helper()
	e, pipes := setupMockEngine(t)

	go func() {
		respIdx := 0
		dec := json.NewDecoder(pipes.readFromEngine)
		for {
			var req detectRequest
			if err := dec.Decode(&req); err != nil {
				return // pipe closed
			}

			resp := detectResponse{}
			if respIdx < len(responses) {
				resp = responses[respIdx]
				respIdx++
			}
			pipes.writeResponse(t, resp)
		}
	}()

	return e, pipes
}

// mockEngineWithCallback creates a running Engine with a goroutine that
// reads Detect requests and invokes the callback for each response.
func mockEngineWithCallback(t *testing.T, fn func(frame string) []rawDetection) (*Engine, *mockPipes) {
	t.Helper()
	e, pipes := setupMockEngine(t)

	go func() {
		dec := json.NewDecoder(pipes.readFromEngine)
		for {
			var req detectRequest
			if err := dec.Decode(&req); err != nil {
				if err == io.EOF || err.Error() == "io: read/write on closed pipe" {
					return
				}
				t.Logf("mock decoder error: %v", err)
				return
			}

			var dets []rawDetection
			if fn != nil {
				dets = fn(req.Frame)
			}
			pipes.writeResponse(t, detectResponse{Detections: dets})
		}
	}()

	return e, pipes
}

// --- Tests ---

func TestNewAiDetector(t *testing.T) {
	eng := newMockEngineSimple(t)
	defer eng.Stop()

	d := NewAiDetector(eng)
	if d == nil {
		t.Fatal("expected non-nil AiDetector")
	}
	if d.IsEnabled("test-cam") {
		t.Fatal("newly created detector should not have cameras enabled")
	}
	if len(d.EnabledCameras()) != 0 {
		t.Fatal("expected no enabled cameras")
	}
}

func TestAiDetectorEnableDisableCamera(t *testing.T) {
	eng := newMockEngineSimple(t)
	defer eng.Stop()

	d := NewAiDetector(eng)
	hub := model.NewStreamHub()

	// Enable camera.
	err := d.EnableCamera("cam-1", hub)
	if err != nil {
		t.Fatalf("EnableCamera failed: %v", err)
	}
	if !d.IsEnabled("cam-1") {
		t.Fatal("expected cam-1 to be enabled")
	}
	cams := d.EnabledCameras()
	if len(cams) != 1 || cams[0] != "cam-1" {
		t.Fatalf("expected [cam-1], got %v", cams)
	}

	// Already enabled — should error.
	err = d.EnableCamera("cam-1", hub)
	if err == nil {
		t.Fatal("expected error when enabling already-enabled camera")
	}

	// Disable camera.
	d.DisableCamera("cam-1")
	if d.IsEnabled("cam-1") {
		t.Fatal("expected cam-1 to be disabled")
	}

	// Disable non-existent — should be no-op (no panic).
	d.DisableCamera("nonexistent")
}

func TestAiDetectorEnableCameraNilHub(t *testing.T) {
	eng := newMockEngineSimple(t)
	defer eng.Stop()

	d := NewAiDetector(eng)

	err := d.EnableCamera("cam-1", nil)
	if err == nil {
		t.Fatal("expected error for nil hub")
	}
}

func TestAiDetectorStreamHubSubscription(t *testing.T) {
	eng := newMockEngineSimple(t)
	defer eng.Stop()

	hub := model.NewStreamHub()
	d := NewAiDetector(eng)

	// Verify StreamHub starts empty.
	if hub.ConsumerCount() != 0 {
		t.Fatalf("expected 0 consumers, got %d", hub.ConsumerCount())
	}

	err := d.EnableCamera("cam-1", hub)
	if err != nil {
		t.Fatalf("EnableCamera failed: %v", err)
	}

	// Should have 1 consumer with "ai-cam-1" ID.
	if hub.ConsumerCount() != 1 {
		t.Fatalf("expected 1 consumer, got %d", hub.ConsumerCount())
	}

	// Disable should remove the consumer.
	d.DisableCamera("cam-1")
	if hub.ConsumerCount() != 0 {
		t.Fatalf("expected 0 consumers after disable, got %d", hub.ConsumerCount())
	}
}

func TestAiDetectorFrameSkipping(t *testing.T) {
	eng, pipes := mockEngineWithCallback(t, func(frame string) []rawDetection {
		return []rawDetection{
			{Label: "person", Confidence: 0.95, Box: [4]float32{0, 0, 0.5, 0.5}},
		}
	})
	defer func() {
		eng.Stop()
		pipes.closeAll(t)
	}()

	hub := model.NewStreamHub()
	d := NewAiDetector(eng)
	d.SetFrameSkip(3)

	// Track detection results.
	var detectCount atomic.Int64
	resultCh := make(chan DetectionResult, 100)
	d.OnDetection(func(result DetectionResult) {
		detectCount.Add(1)
		select {
		case resultCh <- result:
		default:
		}
	})

	err := d.EnableCamera("cam-1", hub)
	if err != nil {
		t.Fatalf("EnableCamera failed: %v", err)
	}

	// Send 9 frames.
	nalu := makeNALU(0x05, []byte("test-frame"))
	for i := 0; i < 9; i++ {
		sendFrame(t, hub, int64(i*45000), [][]byte{nalu})
	}

	// Wait for frames to process.
	time.Sleep(500 * time.Millisecond)

	// With frameSkip=3, should get ~3 detections (frames 3, 6, 9).
	d.DisableCamera("cam-1")

	count := detectCount.Load()
	t.Logf("Detection count: %d (expected ~3 with frameSkip=3)", count)
	if count < 2 || count > 3 {
		t.Fatalf("expected 2-3 detections with frameSkip=3, got %d", count)
	}
}

func TestAiDetectorOnDetectionCallback(t *testing.T) {
	personResp := detectResponse{
		Detections: []rawDetection{
			{Label: "person", Confidence: 0.95, Box: [4]float32{0, 0, 0.5, 0.5}},
		},
	}

	eng, pipes := mockEngineWithResponses(t, []detectResponse{personResp})
	defer func() {
		eng.Stop()
		pipes.closeAll(t)
	}()

	hub := model.NewStreamHub()
	d := NewAiDetector(eng)
	d.SetFrameSkip(1) // process every frame

	var mu sync.Mutex
	var results []DetectionResult
	d.OnDetection(func(result DetectionResult) {
		mu.Lock()
		results = append(results, result)
		mu.Unlock()
	})

	err := d.EnableCamera("cam-1", hub)
	if err != nil {
		t.Fatalf("EnableCamera failed: %v", err)
	}

	// Send a frame.
	nalu := makeNALU(0x05, []byte("frame-data"))
	sendFrame(t, hub, 45000, [][]byte{nalu})

	time.Sleep(200 * time.Millisecond)
	d.DisableCamera("cam-1")

	mu.Lock()
	defer mu.Unlock()
	if len(results) != 1 {
		t.Fatalf("expected 1 detection result, got %d", len(results))
	}
	if results[0].CameraID != "cam-1" {
		t.Fatalf("expected camera_id=cam-1, got %s", results[0].CameraID)
	}
	if results[0].PTStime != 45000 {
		t.Fatalf("expected pts=45000, got %d", results[0].PTStime)
	}
	if len(results[0].Detections) != 1 {
		t.Fatalf("expected 1 detection, got %d", len(results[0].Detections))
	}
	if results[0].Detections[0].Label != "person" {
		t.Fatalf("expected label=person, got %s", results[0].Detections[0].Label)
	}
}

func TestAiDetectorConfidenceFiltering(t *testing.T) {
	eng, pipes := mockEngineWithCallback(t, func(frame string) []rawDetection {
		return []rawDetection{
			{Label: "person", Confidence: 0.95, Box: [4]float32{0, 0, 0.5, 0.5}},
			{Label: "bird", Confidence: 0.15, Box: [4]float32{0, 0, 0.1, 0.1}},
		}
	})
	defer func() {
		eng.Stop()
		pipes.closeAll(t)
	}()

	hub := model.NewStreamHub()
	d := NewAiDetector(eng)
	d.SetFrameSkip(1)
	d.SetConfidenceThreshold(0.5) // filter out low-confidence detections

	var mu sync.Mutex
	var results []DetectionResult
	d.OnDetection(func(result DetectionResult) {
		mu.Lock()
		results = append(results, result)
		mu.Unlock()
	})

	err := d.EnableCamera("cam-1", hub)
	if err != nil {
		t.Fatalf("EnableCamera failed: %v", err)
	}

	nalu := makeNALU(0x05, []byte("frame"))
	sendFrame(t, hub, 45000, [][]byte{nalu})

	time.Sleep(200 * time.Millisecond)
	d.DisableCamera("cam-1")

	mu.Lock()
	defer mu.Unlock()
	if len(results) != 1 {
		t.Fatalf("expected 1 result (low-confidence filtered), got %d", len(results))
	}
	if len(results[0].Detections) != 1 {
		t.Fatalf("expected 1 detection (bird filtered out), got %d", len(results[0].Detections))
	}
	if results[0].Detections[0].Label != "person" {
		t.Fatalf("expected label=person, got %s", results[0].Detections[0].Label)
	}
}

func TestAiDetectorSetFrameSkip(t *testing.T) {
	eng := newMockEngineSimple(t)
	defer eng.Stop()

	d := NewAiDetector(eng)

	// Invalid values — should be no-op.
	d.SetFrameSkip(0)
	d.SetFrameSkip(-1)

	// Verify default is still in effect.
	d.mu.RLock()
	skip := d.cfg.FrameSkip
	d.mu.RUnlock()
	if skip != defaultFrameSkip {
		t.Fatalf("expected default frame skip %d, got %d", defaultFrameSkip, skip)
	}

	d.SetFrameSkip(10)
	d.mu.RLock()
	skip = d.cfg.FrameSkip
	d.mu.RUnlock()
	if skip != 10 {
		t.Fatalf("expected frame skip 10, got %d", skip)
	}
}

func TestAiDetectorSetConfidenceThreshold(t *testing.T) {
	eng := newMockEngineSimple(t)
	defer eng.Stop()

	d := NewAiDetector(eng)

	// Invalid values — should be no-op.
	d.SetConfidenceThreshold(0)
	d.SetConfidenceThreshold(-0.5)
	d.SetConfidenceThreshold(1.5)

	d.mu.RLock()
	thresh := d.cfg.ConfidenceThreshold
	d.mu.RUnlock()
	if thresh != defaultConfidenceThreshold {
		t.Fatalf("expected default threshold %v, got %v", defaultConfidenceThreshold, thresh)
	}

	d.SetConfidenceThreshold(0.5)
	d.mu.RLock()
	thresh = d.cfg.ConfidenceThreshold
	d.mu.RUnlock()
	if thresh != 0.5 {
		t.Fatalf("expected threshold 0.5, got %v", thresh)
	}
}

func TestAiDetectorConcurrentCameras(t *testing.T) {
	eng, pipes := mockEngineWithCallback(t, func(frame string) []rawDetection {
		return []rawDetection{
			{Label: "object", Confidence: 0.9, Box: [4]float32{0, 0, 0.5, 0.5}},
		}
	})
	defer func() {
		eng.Stop()
		pipes.closeAll(t)
	}()

	d := NewAiDetector(eng)
	d.SetFrameSkip(1)

	var count atomic.Int64
	d.OnDetection(func(result DetectionResult) {
		count.Add(1)
	})

	// Enable 3 cameras.
	hubs := make([]*model.StreamHub, 3)
	for i := 0; i < 3; i++ {
		hubs[i] = model.NewStreamHub()
		err := d.EnableCamera(fmt.Sprintf("cam-%c", 'A'+i), hubs[i])
		if err != nil {
			t.Fatalf("EnableCamera cam-%c failed: %v", 'A'+i, err)
		}
	}

	// Send frames to all cameras concurrently.
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			nalu := makeNALU(0x05, []byte("frame"))
			for j := 0; j < 5; j++ {
					hubs[idx].Broadcast(int64(j*45000), [][]byte{nalu}, false)
			}
		}(i)
	}
	wg.Wait()

	time.Sleep(500 * time.Millisecond)

	cams := d.EnabledCameras()
	if len(cams) != 3 {
		t.Fatalf("expected 3 enabled cameras, got %d", len(cams))
	}

	t.Logf("Total detections across 3 cameras: %d", count.Load())

	// Cleanup.
	d.StopAll()

	if d.IsEnabled("cam-A") {
		t.Fatal("expected camera disabled after StopAll")
	}
	if len(d.EnabledCameras()) != 0 {
		t.Fatalf("expected 0 enabled cameras after StopAll, got %d", len(d.EnabledCameras()))
	}
}

func TestAiDetectorStopAll(t *testing.T) {
	eng := newMockEngineSimple(t)
	defer eng.Stop()

	hub1 := model.NewStreamHub()
	hub2 := model.NewStreamHub()
	d := NewAiDetector(eng)

	d.EnableCamera("cam-1", hub1)
	d.EnableCamera("cam-2", hub2)

	if hub1.ConsumerCount() != 1 || hub2.ConsumerCount() != 1 {
		t.Fatal("expected both hubs to have 1 consumer")
	}

	d.StopAll()

	if d.IsEnabled("cam-1") || d.IsEnabled("cam-2") {
		t.Fatal("expected all cameras disabled after StopAll")
	}
	if hub1.ConsumerCount() != 0 || hub2.ConsumerCount() != 0 {
		t.Fatal("expected both hubs to have 0 consumers after StopAll")
	}
}

func TestAiDetectorSetEngine(t *testing.T) {
	eng1 := newMockEngineSimple(t)
	defer eng1.Stop()

	d := NewAiDetector(eng1)

	// Nil engine — detection should be no-op.
	d.SetEngine(nil)
	d.mu.RLock()
	if d.eng != nil {
		t.Fatal("expected nil engine after SetEngine(nil)")
	}
	d.mu.RUnlock()

	// Replace with new engine.
	eng2 := newMockEngineSimple(t)
	defer eng2.Stop()
	d.SetEngine(eng2)
	d.mu.RLock()
	if d.eng != eng2 {
		t.Fatal("expected engine to be replaced")
	}
	d.mu.RUnlock()
}

func TestAiDetectorNilEngine(t *testing.T) {
	d := NewAiDetector(nil)

	hub := model.NewStreamHub()
	err := d.EnableCamera("cam-1", hub)
	if err != nil {
		t.Fatalf("EnableCamera failed: %v", err)
	}

	// Send frame — should not panic even with nil engine.
	nalu := makeNALU(0x05, []byte("frame"))
	sendFrame(t, hub, 45000, [][]byte{nalu})
	time.Sleep(50 * time.Millisecond)

	d.DisableCamera("cam-1")
}

func TestConcatNALUs(t *testing.T) {
	tests := []struct {
		name string
		au   [][]byte
		want int // expected total length
	}{
		{"empty", nil, 0},
		{"single nalu", [][]byte{{0x01, 0x02}}, 6},        // 4 start code + 2 data
		{"two nalus", [][]byte{{0x01}, {0x02, 0x03}}, 11}, // 4+1 + 4+2
		{"many nalus", [][]byte{{0xFF}, {0xFE}, {0xFD}}, 15}, // 3*(4+1)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := concatNALUs(tt.au)
			if len(out) != tt.want {
				t.Fatalf("concatNALUs: expected len=%d, got %d", tt.want, len(out))
			}
		})
	}
}

func TestFilterDetections(t *testing.T) {
	detections := []ai.Detection{
		{Label: "person", Confidence: 0.95, Box: [4]float32{0, 0, 0.5, 0.5}},
		{Label: "car", Confidence: 0.6, Box: [4]float32{0, 0, 0.3, 0.3}},
		{Label: "bird", Confidence: 0.15, Box: [4]float32{0, 0, 0.1, 0.1}},
	}

	tests := []struct {
		name      string
		threshold float32
		want      int
	}{
		{"no filter", 0, 3},
		{"0.5 threshold", 0.5, 2},
		{"0.7 threshold", 0.7, 1},
		{"1.0 threshold", 1.0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filtered := filterDetections(detections, tt.threshold)
			if len(filtered) != tt.want {
				t.Fatalf("filterDetections(threshold=%v): expected %d, got %d",
					tt.threshold, tt.want, len(filtered))
			}
		})
	}
}

func TestFilterDetectionsZeroThreshold(t *testing.T) {
	detections := []ai.Detection{
		{Label: "person", Confidence: 0.01, Box: [4]float32{0, 0, 0.5, 0.5}},
	}
	filtered := filterDetections(detections, 0)
	if len(filtered) != 1 {
		t.Fatalf("expected all detections with threshold=0, got %d", len(filtered))
	}
}

func TestAiDetectorDisableNonExistentCamera(t *testing.T) {
	d := NewAiDetector(nil)
	// Should not panic.
	d.DisableCamera("nonexistent")
}

func TestAiDetectorStopAllEmpty(t *testing.T) {
	d := NewAiDetector(nil)
	// Should not panic.
	d.StopAll()
}

func TestAiDetectorContextCancellation(t *testing.T) {
	eng, pipes := mockEngineWithCallback(t, func(frame string) []rawDetection {
		return []rawDetection{
			{Label: "person", Confidence: 0.9, Box: [4]float32{0, 0, 0.5, 0.5}},
		}
	})
	defer func() {
		eng.Stop()
		pipes.closeAll(t)
	}()

	hub := model.NewStreamHub()
	d := NewAiDetector(eng)
	d.SetFrameSkip(1)

	var count atomic.Int64
	d.OnDetection(func(result DetectionResult) {
		count.Add(1)
	})

	err := d.EnableCamera("cam-1", hub)
	if err != nil {
		t.Fatalf("EnableCamera failed: %v", err)
	}

	// Send frames.
	nalu := makeNALU(0x05, []byte("frame-data"))
	for i := 0; i < 5; i++ {
		sendFrame(t, hub, int64(i*45000), [][]byte{nalu})
	}

	time.Sleep(200 * time.Millisecond)

	preDisable := count.Load()
	t.Logf("Detections before disable: %d", preDisable)

	// Disable camera (cancels context).
	d.DisableCamera("cam-1")

	// Send more frames after disable — should be no-op.
	for i := 0; i < 5; i++ {
		sendFrame(t, hub, int64((i+5)*45000), [][]byte{nalu})
	}

	time.Sleep(200 * time.Millisecond)

	postDisable := count.Load()
	t.Logf("Detections after disable: %d (should be same as before disable)", postDisable)

	if postDisable != preDisable {
		t.Fatalf("expected no new detections after disable: before=%d after=%d", preDisable, postDisable)
	}

	// Verify hub is cleaned up.
	if hub.ConsumerCount() != 0 {
		t.Fatalf("expected 0 consumers after disable, got %d", hub.ConsumerCount())
	}
}

func TestAiDetectorUnregisterCallback(t *testing.T) {
	eng, pipes := mockEngineWithResponses(t, []detectResponse{{
		Detections: []rawDetection{
			{Label: "person", Confidence: 0.9, Box: [4]float32{0, 0, 0.5, 0.5}},
		},
	}})
	defer func() {
		eng.Stop()
		pipes.closeAll(t)
	}()

	hub := model.NewStreamHub()
	d := NewAiDetector(eng)
	d.SetFrameSkip(1)

	// Register callback and get ID.
	eventCh := make(chan DetectionResult, 16)
	callbackID := d.OnDetection(func(result DetectionResult) {
		eventCh <- result
	})
	if callbackID == "" {
		t.Fatal("expected non-empty callback ID")
	}

	// Unregister and close channel (simulate client disconnect).
	d.UnregisterCallback(callbackID)
	close(eventCh)

	// Enable camera and send a frame — should not panic.
	if err := d.EnableCamera("cam-1", hub); err != nil {
		t.Fatalf("EnableCamera failed: %v", err)
	}
	nalu := makeNALU(0x05, []byte("frame"))
	sendFrame(t, hub, 45000, [][]byte{nalu})
	time.Sleep(200 * time.Millisecond)
	d.DisableCamera("cam-1")
}
