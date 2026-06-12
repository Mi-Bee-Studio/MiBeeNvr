package ai

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// Inferencer is the low-level interface for running inference against a loaded model.
type Inferencer interface {
	Infer(ctx context.Context, tensor []float32, dims []int64) ([][]float32, error)
	IsAvailable() bool
}

// Preprocessor converts raw video frames into normalized float32 tensors
// suitable for model inference.
type Preprocessor interface {
	Preprocess(frame []byte, width, height, inputSize int) ([]float32, error)
}

// Postprocessor converts raw model output tensors into structured Detection results.
type Postprocessor interface {
	Postprocess(rawOutput [][]float32, dims []int64, inputSize int, threshold float64) ([]Detection, error)
}

// ModelInfo holds metadata about a loaded AI model.
type ModelInfo struct {
	Name      string
	Version   string
	InputSize int
	Labels    []string
}

// CameraAIStatus provides a snapshot of the AI inference state for a single camera.
type CameraAIStatus struct {
	Running         bool       `json:"running"`
	FPS             float64    `json:"fps"`
	LastDetection   *time.Time `json:"last_detection,omitempty"`
	DetectionsCount int64      `json:"detections_count"`
	Model           string     `json:"model"`
}

// InferenceEngine orchestrates the full inference pipeline: infer → postprocess.
// It wires together an Inferencer, Postprocessor, and ModelInfo.
// Preprocessing is expected to happen before calling Run.
type InferenceEngine struct {
	Inferencer    Inferencer
	Preprocessor  Preprocessor
	Postprocessor Postprocessor
	ModelInfo     ModelInfo
}

// NewInferenceEngine creates a new InferenceEngine wired to the given components.
func NewInferenceEngine(infer Inferencer, pre Preprocessor, post Postprocessor, info ModelInfo) *InferenceEngine {
	return &InferenceEngine{
		Inferencer:    infer,
		Preprocessor:  pre,
		Postprocessor: post,
		ModelInfo:     info,
	}
}

// Run executes inference then postprocessing on the given input tensor.
func (e *InferenceEngine) Run(ctx context.Context, tensor []float32, dims []int64) ([]Detection, error) {
	rawOutput, err := e.Inferencer.Infer(ctx, tensor, dims)
	if err != nil {
		return nil, err
	}
	detections, err := e.Postprocessor.Postprocess(rawOutput, dims, e.ModelInfo.InputSize, 0.5)
	if err != nil {
		return nil, err
	}
	return detections, nil
}

// Config mirrors config.AIConfig fields needed by the AI manager.
// Defined locally to avoid a circular import (config imports ai for the ROI type).
type Config struct {
	Enabled             bool              `yaml:"enabled"`
	EnabledCameras      []string          `yaml:"enabled_cameras"`
	ModelURL            string            `yaml:"model_url"`
	MaxGoroutines       int               `yaml:"max_goroutines"`
	Zones               map[string][]ROI  `yaml:"zones"`
	InferenceTimeoutMs  int               `yaml:"inference_timeout_ms"`
	FrameSkipRate       int               `yaml:"frame_skip_rate"`
	ConfidenceThreshold float64           `yaml:"confidence_threshold"`
	ModelPath           string            `yaml:"model_path"`
}

// Manager manages per-camera AI inference lifecycle.
// Each camera gets its own inference pipeline with dedicated lifecycle control
// via context cancellation.
type Manager struct {
	cfg         Config
	bus         *event.EventBus
	engines     map[string]*InferenceEngine
	mu          sync.Mutex
	cancelFuncs map[string]context.CancelFunc
}

// NewManager creates a new AI Manager.
func NewManager(cfg Config, bus *event.EventBus) *Manager {
	return &Manager{
		cfg:         cfg,
		bus:         bus,
		engines:     make(map[string]*InferenceEngine),
		cancelFuncs: make(map[string]context.CancelFunc),
	}
}

// StartCamera starts AI inference for a camera. Currently a stub —
// actual StreamHub subscription and frame processing will be implemented in a later task.
func (m *Manager) StartCamera(ctx context.Context, cameraID string, hub *model.StreamHub) error {
	slog.Warn("AI: StartCamera not yet implemented", "camera_id", cameraID)
	return nil
}

// StopCamera stops AI inference for a camera by cancelling its context
// and cleaning up its state.
func (m *Manager) StopCamera(cameraID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if cancel, ok := m.cancelFuncs[cameraID]; ok {
		cancel()
		delete(m.cancelFuncs, cameraID)
	}
	delete(m.engines, cameraID)

	slog.Info("AI: stopped camera inference", "camera_id", cameraID)
	return nil
}

// IsRunning reports whether AI inference is active for the given camera.
func (m *Manager) IsRunning(cameraID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.cancelFuncs[cameraID]
	return ok
}

// Status returns a snapshot of AI inference state for all cameras
// that have been registered with the manager.
func (m *Manager) Status() map[string]CameraAIStatus {
	m.mu.Lock()
	defer m.mu.Unlock()

	statuses := make(map[string]CameraAIStatus, len(m.engines))
	for id, engine := range m.engines {
		_, running := m.cancelFuncs[id]
		statuses[id] = CameraAIStatus{
			Running: running,
			Model:   engine.ModelInfo.Name,
		}
	}
	return statuses
}
