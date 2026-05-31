package engine

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/ai"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

const (
	// defaultFrameSkip is the default number of frames to skip between detections.
	defaultFrameSkip = 3

	// defaultConfidenceThreshold is the minimum confidence for a detection to be published.
	defaultConfidenceThreshold float32 = 0.3

	// aiSubscriberPrefix is the StreamHub subscriber ID prefix for AI detection.
	aiSubscriberPrefix = "ai-"
)

// DetectionResult contains inference results for a single camera frame.
// Published via the OnDetection callback.
type DetectionResult struct {
	CameraID   string         `json:"camera_id"`
	PTStime    int64          `json:"pts"`
	Detections []ai.Detection `json:"detections"`
}

// OnDetectionFunc is called when inference results are available.
// Implementations MUST be non-blocking to avoid stalling the frame pipeline.
type OnDetectionFunc func(result DetectionResult)

// DetectorConfig holds per-camera AI detection configuration.
type DetectorConfig struct {
	// FrameSkip processes every Nth frame. Default: 3 (every 3rd frame).
	FrameSkip int

	// ConfidenceThreshold filters detections below this confidence. Default: 0.3.
	ConfidenceThreshold float32
}

// cameraEntry holds per-camera AI detection state.
type cameraEntry struct {
	camID   string
	hub     *model.StreamHub
	hubSub  string
	cancel  context.CancelFunc
	counter atomic.Int64 // frame counter for skipping
}

// AiDetector manages AI detection across cameras by subscribing to StreamHub
// for live frames and running inference at configurable intervals.
//
// It holds a reference to the Engine for inference and subscribes to each
// camera's StreamHub using the "ai-" prefix (similar to "flv-" for FLV manager).
//
// All frame callbacks and inference calls are non-blocking — the recording
// pipeline is never stalled.
type AiDetector struct {
	mu    sync.RWMutex
	eng   *Engine             // inference engine (may be nil if not started)
	cams  map[string]*cameraEntry
	cfg   DetectorConfig      // global default config (copied per camera)
	onDet OnDetectionFunc     // detection result callback (may be nil)
	log   *slog.Logger
}

// NewAiDetector creates a new AiDetector bound to the given Engine.
// If engine is nil, detection calls will be no-ops until SetEngine is called.
func NewAiDetector(eng *Engine) *AiDetector {
	return &AiDetector{
		eng: eng,
		cams: make(map[string]*cameraEntry),
		cfg: DetectorConfig{
			FrameSkip:           defaultFrameSkip,
			ConfidenceThreshold: defaultConfidenceThreshold,
		},
		log: logger.With("component", "ai-detector"),
	}
}

// OnDetection sets the callback invoked when detection results are available.
// If nil, detection results are silently discarded.
func (d *AiDetector) OnDetection(cb OnDetectionFunc) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onDet = cb
}

// SetEngine replaces the inference engine. Existing camera subscriptions
// continue with the new engine.
func (d *AiDetector) SetEngine(eng *Engine) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.eng = eng
}

// SetFrameSkip changes the frame skip interval for all cameras.
// Setting n <= 0 has no effect.
func (d *AiDetector) SetFrameSkip(n int) {
	if n <= 0 {
		return
	}
	d.mu.Lock()
	d.cfg.FrameSkip = n
	d.mu.Unlock()
}

// SetConfidenceThreshold changes the minimum confidence for all cameras.
// Setting t <= 0 or t > 1.0 has no effect.
func (d *AiDetector) SetConfidenceThreshold(t float32) {
	if t <= 0 || t > 1.0 {
		return
	}
	d.mu.Lock()
	d.cfg.ConfidenceThreshold = t
	d.mu.Unlock()
}

// EnableCamera subscribes to a camera's StreamHub for live frame processing.
// Frames are processed at the configured frame skip interval.
// If the camera is already enabled, this is a no-op.
//
// The StreamHub subscription ID is "ai-{camID}".
func (d *AiDetector) EnableCamera(camID string, hub *model.StreamHub) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, ok := d.cams[camID]; ok {
		return fmt.Errorf("camera %q already enabled", camID)
	}

	if hub == nil {
		return fmt.Errorf("camera %q: hub is nil", camID)
	}

	hubSub := aiSubscriberPrefix + camID
	ctx, cancel := context.WithCancel(context.Background())

	entry := &cameraEntry{
		camID:  camID,
		hub:    hub,
		hubSub: hubSub,
		cancel: cancel,
	}

	// Subscribe to StreamHub. The callback runs in its own goroutine
	// managed by StreamHub's consumerEntry drain loop, so it may block.
	err := hub.Subscribe(hubSub, func(pts int64, au [][]byte) {
		d.processFrame(ctx, camID, pts, au)
	})
	if err != nil {
		cancel()
		return fmt.Errorf("camera %q: subscribe to hub: %w", camID, err)
	}

	d.cams[camID] = entry
	d.log.Info("AI detection enabled", "camera_id", camID, "frame_skip", d.cfg.FrameSkip)
	return nil
}

// DisableCamera unsubscribes from a camera's StreamHub and stops inference.
// If the camera is not enabled, this is a no-op.
func (d *AiDetector) DisableCamera(camID string) {
	d.mu.Lock()
	entry, ok := d.cams[camID]
	if ok {
		delete(d.cams, camID)
	}
	d.mu.Unlock()

	if ok {
		// Unsubscribe from StreamHub first (waits for drain goroutine).
		entry.hub.Unsubscribe(entry.hubSub)
		// Cancel context to stop any in-flight inference.
		entry.cancel()
		d.log.Info("AI detection disabled", "camera_id", camID)
	}
}

// IsEnabled returns true if AI detection is active for the given camera.
func (d *AiDetector) IsEnabled(camID string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	_, ok := d.cams[camID]
	return ok
}

// EnabledCameras returns the list of camera IDs with AI detection enabled.
func (d *AiDetector) EnabledCameras() []string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	ids := make([]string, 0, len(d.cams))
	for id := range d.cams {
		ids = append(ids, id)
	}
	return ids
}

// StopAll disables AI detection for all cameras.
func (d *AiDetector) StopAll() {
	d.mu.Lock()
	entries := make([]*cameraEntry, 0, len(d.cams))
	for _, e := range d.cams {
		entries = append(entries, e)
		delete(d.cams, e.camID)
	}
	d.mu.Unlock()

	for _, e := range entries {
		e.hub.Unsubscribe(e.hubSub)
		e.cancel()
	}
	d.log.Info("AI detection stopped for all cameras")
}

// processFrame handles a frame received from StreamHub.
// It applies frame skipping and runs inference on the Engine.
//
// This callback runs in StreamHub's dedicated goroutine per consumer,
// so it may block without affecting other consumers or the Broadcast caller.
// However, inference should be fast enough to keep up with the frame skip rate.
func (d *AiDetector) processFrame(ctx context.Context, camID string, pts int64, au [][]byte) {
	// Check if this camera is still enabled (race guard).
	d.mu.RLock()
	entry, ok := d.cams[camID]
	if !ok {
		d.mu.RUnlock()
		return
	}
	frameSkip := d.cfg.FrameSkip
	d.mu.RUnlock()


	// Frame skipping: increment counter, only process every Nth frame.
	count := entry.counter.Add(1)
	if frameSkip > 1 && count%int64(frameSkip) != 0 {
		return
	}

	// Get engine reference.
	d.mu.RLock()
	eng := d.eng
	cb := d.onDet
	confThresh := d.cfg.ConfidenceThreshold
	d.mu.RUnlock()

	if eng == nil {
		return
	}

	// Convert NALUs to a single byte slice for the Engine.
	// Engine.Detect expects JPEG-encoded bytes, but for NALU data
	// the subprocess handles decoding. Pass raw NALU data concatenated.
	frameData := concatNALUs(au)
	if len(frameData) == 0 {
		return
	}

	// Run inference with the camera's context.
	detections, err := eng.Detect(ctx, frameData)
	if err != nil {
		d.log.Warn("inference failed",
			"camera_id", camID,
			"error", err,
		)
		return
	}

	// Filter by confidence threshold.
	filtered := filterDetections(detections, confThresh)
	if len(filtered) == 0 {
		return
	}

	// Publish results via callback (non-blocking).
	if cb != nil {
		result := DetectionResult{
			CameraID:   camID,
			PTStime:    pts,
			Detections: filtered,
		}
		cb(result)
	}
}

// concatNALUs concatenates NALU slices into a single byte slice.
// Each NALU is prefixed with a start code (00 00 00 01) as expected
// by the subprocess decoder.
func concatNALUs(au [][]byte) []byte {
	if len(au) == 0 {
		return nil
	}

	startCode := []byte{0x00, 0x00, 0x00, 0x01}
	total := 0
	for _, nalu := range au {
		total += len(startCode) + len(nalu)
	}

	out := make([]byte, 0, total)
	for _, nalu := range au {
		out = append(out, startCode...)
		out = append(out, nalu...)
	}
	return out
}

// filterDetections returns detections above the confidence threshold.
func filterDetections(detections []ai.Detection, threshold float32) []ai.Detection {
	if threshold <= 0 {
		return detections
	}
	filtered := make([]ai.Detection, 0, len(detections))
	for _, d := range detections {
		if d.Confidence >= threshold {
			filtered = append(filtered, d)
		}
	}
	return filtered
}
