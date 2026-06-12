package ai

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// MQTTPublisher publishes AI detection events via MQTT.
// Defined as an interface to avoid importing internal/mqtt from the ai package.
type MQTTPublisher interface {
	PublishAIDetection(ctx context.Context, cameraID string, event string, detections []MQTTDetection) error
}

// MQTTDetection mirrors mqtt.AiDetectionObj without importing the mqtt package.
type MQTTDetection struct {
	Label      string    `json:"label"`
	Confidence float64   `json:"confidence"`
	BBox       [4]float64 `json:"bbox"`
}

// StreamSubscriber is the subset of model.StreamHub needed by CameraDetector.
// Defined as an interface for testability.
type StreamSubscriber interface {
	Subscribe(id string, cb model.FrameCallback) error
	Unsubscribe(id string)
}

// CameraDetectorConfig holds configuration for a CameraDetector.
type CameraDetectorConfig struct {
	CameraID      string
	Engine        *InferenceEngine
	Hub           StreamSubscriber
	Bus           *event.EventBus
	MQTT          MQTTPublisher // optional — may be nil
	FrameSkipRate int           // process every Nth frame (default: 10)
	MaxGoroutines int           // max inference worker goroutines (default: 2)
}

// frameJob holds a single frame to be processed by a worker goroutine.
type frameJob struct {
	frame []byte // concatenated JPEG bytes
	pts   int64
}

// CameraDetector subscribes to a camera's StreamHub, runs AI inference
// on JPEG frames in a worker goroutine pool, and publishes detection events
// to the EventBus and optionally to MQTT.
//
// It follows the KeyframeExtractor pattern:
//  1. Start → hub.Subscribe(consumerID, onFrame) → spawn worker goroutines
//  2. onFrame is the StreamHub callback — MUST be non-blocking:
//     Deep-copy frame data, non-blocking send to worker channel (drop if full)
//  3. Worker goroutines process frames from the channel
//  4. Stop → cancel → wait for done → Unsubscribe
type CameraDetector struct {
	cameraID      string
	engine        *InferenceEngine
	consumerID    string
	frameSkipRate int
	maxGoroutines int

	hub  StreamSubscriber
	bus  *event.EventBus
	mqtt MQTTPublisher

	workerCh chan frameJob

	cancel  context.CancelFunc
	done    chan struct{}
	running atomic.Bool

	frameCount atomic.Int64
}

// NewCameraDetector creates a new CameraDetector with the given configuration.
// Defaults are applied for FrameSkipRate (10) and MaxGoroutines (2) if zero.
func NewCameraDetector(cfg CameraDetectorConfig) *CameraDetector {
	if cfg.FrameSkipRate <= 0 {
		cfg.FrameSkipRate = 10
	}
	if cfg.MaxGoroutines <= 0 {
		cfg.MaxGoroutines = 2
	}

	return &CameraDetector{
		cameraID:      cfg.CameraID,
		engine:        cfg.Engine,
		consumerID:    fmt.Sprintf("ai-detector-%s", cfg.CameraID),
		frameSkipRate: cfg.FrameSkipRate,
		maxGoroutines: cfg.MaxGoroutines,
		hub:           cfg.Hub,
		bus:           cfg.Bus,
		mqtt:          cfg.MQTT,
		workerCh:      make(chan frameJob, cfg.MaxGoroutines),
	}
}

// Start subscribes to the given StreamHub and spawns the worker goroutine pool.
// Returns an error if the detector is already running or if subscription fails.
func (d *CameraDetector) Start(ctx context.Context) error {
	if d.running.Load() {
		return fmt.Errorf("ai: detector for %q already running", d.cameraID)
	}

	if err := d.hub.Subscribe(d.consumerID, d.onFrame); err != nil {
		return fmt.Errorf("ai: subscribe to stream hub: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	d.cancel = cancel
	d.done = make(chan struct{})
	d.frameCount.Store(0)
	d.running.Store(true)

	// Spawn worker goroutines.
	for i := 0; i < d.maxGoroutines; i++ {
		go d.worker(ctx)
	}

	// Monitor goroutine: waits for context cancellation then signals done.
	go func() {
		<-ctx.Done()
		close(d.done)
	}()

	slog.Info("AI: detector started",
		"camera_id", d.cameraID,
		"frame_skip_rate", d.frameSkipRate,
		"max_goroutines", d.maxGoroutines,
	)
	return nil
}

// Stop unsubscribes from the StreamHub and stops all worker goroutines.
// Safe to call multiple times.
func (d *CameraDetector) Stop() {
	if !d.running.Load() {
		return
	}

	if d.cancel != nil {
		d.cancel()
	}

	if d.done != nil {
		<-d.done
	}

	d.hub.Unsubscribe(d.consumerID)
	d.running.Store(false)

	slog.Info("AI: detector stopped", "camera_id", d.cameraID)
}

// IsRunning reports whether the detector is currently active.
func (d *CameraDetector) IsRunning() bool {
	return d.running.Load()
}

// onFrame is the StreamHub FrameCallback — MUST be non-blocking.
// It deep-copies the access unit, applies frame skipping, checks for JPEG,
// and enqueues the frame for worker processing via a non-blocking channel send.
func (d *CameraDetector) onFrame(pts int64, au [][]byte) {
	// Frame skip: only process every Nth frame.
	count := d.frameCount.Add(1)
	if count%int64(d.frameSkipRate) != 0 {
		return
	}

	// Phase 1: JPEG only — skip H.264/H.265 NAL units.
	if !isJPEGFrame(au) {
		return
	}

	// Deep copy and concatenate AUs into a single JPEG byte slice.
	frame := concatAU(au)

	// Non-blocking send to worker pool — drop frame if buffer is full.
	select {
	case d.workerCh <- frameJob{frame: frame, pts: pts}:
	default:
		slog.Debug("AI: worker channel full, dropping frame",
			"camera_id", d.cameraID,
		)
	}
}

// worker processes frames from the worker channel in a loop until
// the context is cancelled or the channel is closed.
func (d *CameraDetector) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job, ok := <-d.workerCh:
			if !ok {
				return
			}
			d.processFrame(ctx, job)
		}
	}
}

// processFrame runs the full inference pipeline: preprocess → infer → postprocess,
// then publishes detection events if objects were found.
func (d *CameraDetector) processFrame(ctx context.Context, job frameJob) {
	inputSize := d.engine.ModelInfo.InputSize

	tensor, err := d.engine.Preprocessor.Preprocess(job.frame, 0, 0, inputSize)
	if err != nil {
		slog.Warn("AI: preprocess failed",
			"camera_id", d.cameraID,
			"error", err,
		)
		return
	}

	dims := []int64{1, 3, int64(inputSize), int64(inputSize)}

	detections, err := d.engine.Run(ctx, tensor, dims)
	if err != nil {
		slog.Warn("AI: inference failed",
			"camera_id", d.cameraID,
			"error", err,
		)
		return
	}

	if len(detections) == 0 {
		return
	}

	d.publishEvents(ctx, detections)
}

// publishEvents publishes detection results to the EventBus and optionally to MQTT.
func (d *CameraDetector) publishEvents(ctx context.Context, detections []Detection) {
	evt := DetectionEvent{
		CameraID:   d.cameraID,
		Timestamp:  time.Now().UTC(),
		Detections: detections,
	}

	// Publish to the general AI detection topic.
	d.bus.Publish(ctx, event.TopicAIDetection, evt)

	// Publish to per-class topics.
	for _, det := range detections {
		switch det.ClassLabel {
		case "person":
			d.bus.Publish(ctx, event.TopicAIPerson, evt)
		case "car", "truck", "bus", "motorcycle", "bicycle":
			d.bus.Publish(ctx, event.TopicAIVehicle, evt)
		case "cat", "dog", "bird", "horse", "sheep", "cow", "elephant", "bear", "zebra", "giraffe":
			d.bus.Publish(ctx, event.TopicAIAnimal, evt)
		}
	}

	// Publish to MQTT if configured.
	if d.mqtt != nil {
		mqttDets := make([]MQTTDetection, len(detections))
		for i, det := range detections {
			mqttDets[i] = MQTTDetection{
				Label:      det.ClassLabel,
				Confidence: det.Confidence,
				BBox:       det.BBox,
			}
		}
		if err := d.mqtt.PublishAIDetection(ctx, d.cameraID, "detection", mqttDets); err != nil {
			slog.Warn("AI: MQTT publish failed",
				"camera_id", d.cameraID,
				"error", err,
			)
		}
	}
}

// isJPEGFrame checks if the access unit contains a JPEG SOI marker (0xFF 0xD8).
// Phase 1 only: returns false for H.264/H.265 NAL units.
func isJPEGFrame(au [][]byte) bool {
	if len(au) == 0 {
		return false
	}
	if len(au) == 1 && len(au[0]) >= 2 {
		return au[0][0] == 0xFF && au[0][1] == 0xD8
	}
	for _, nalu := range au {
		if len(nalu) >= 2 && nalu[0] == 0xFF && nalu[1] == 0xD8 {
			return true
		}
	}
	return false
}

// concatAU concatenates all NALUs in an access unit into a single byte slice.
func concatAU(au [][]byte) []byte {
	totalLen := 0
	for _, nalu := range au {
		totalLen += len(nalu)
	}
	result := make([]byte, 0, totalLen)
	for _, nalu := range au {
		result = append(result, nalu...)
	}
	return result
}
