// Package engine implements a local ONNX Runtime AI provider that communicates
// with an external subprocess over stdin/stdout JSON. This preserves
// CGO_ENABLED=0 by delegating inference to a separate binary.
//
// Protocol:
//   - stdin:  JSON request  {"frame":"<base64>","width":N,"height":N}
//   - stdout: JSON response {"detections":[{"label":"...","confidence":0.95,"box":[x,y,w,h]}]}
package engine

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/ai"
)

var (
	logger = slog.Default().With("component", "ai-engine")

	// maxFrameSize limits base64-encoded frame size sent to subprocess (64 MB).
	maxFrameSize = 64 << 20

	// defaultRestartBackoff controls the maximum restart delay after crashes.
	defaultRestartBackoff = 30 * time.Second

	// healthTimeout is the deadline for health-check pings.
	healthTimeout = 10 * time.Second
)

// detectRequest is the JSON message sent to the ONNX subprocess on stdin.
type detectRequest struct {
	Frame  string `json:"frame"`  // base64-encoded JPEG
	Width  int    `json:"width"`  // frame width in pixels
	Height int    `json:"height"` // frame height in pixels
}

// detectResponse is the JSON message read from the ONNX subprocess stdout.
type detectResponse struct {
	Detections []rawDetection `json:"detections"`
	Error      string         `json:"error,omitempty"`
}

// rawDetection mirrors the JSON shape returned by the subprocess.
type rawDetection struct {
	Label      string    `json:"label"`
	Confidence float32   `json:"confidence"`
	Box        [4]float32 `json:"box"` // [x, y, width, height] normalized
}

// Engine implements ai.AIProvider and ai.Detector.
type Engine struct {
	mu sync.Mutex

	dataDir   string
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.Reader
	stderrBuf *bytesBuf // captures subprocess stderr for logging

	ready    chan struct{} // closed once subprocess signals readiness
	running  bool
	cancel   context.CancelFunc
	done     chan struct{} // closed when monitor goroutine exits

	// model holds the last model path passed to NewDetector.
	model string

	// backoff state for crash recovery
	consecutiveCrashes int
	lastCrash          time.Time
}

// --- compile-time interface checks ---

var (
	_ ai.AIProvider = (*Engine)(nil)
	_ ai.Detector   = (*Engine)(nil)
)

// bytesBuf is a thread-safe io.Writer that captures stderr.
type bytesBuf struct {
	mu  sync.Mutex
	buf []byte
}

func (b *bytesBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	// Trim to keep last 4 KB to avoid unbounded growth.
	const maxCap = 4096
	if len(b.buf) > maxCap {
		b.buf = b.buf[len(b.buf)-maxCap:]
	}
	return len(p), nil
}

func (b *bytesBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// New creates an Engine with the given data directory.
// The subprocess binary is expected at {dataDir}/tools/onnxruntime.
// The default model path is {dataDir}/models/yolov11n.onnx.
func New(dataDir string) *Engine {
	return &Engine{
		dataDir: dataDir,
	}
}

// --- AIProvider interface ---

// Name returns the provider identifier.
func (e *Engine) Name() string {
	return "onnxruntime-local"
}

// IsAvailable reports whether the subprocess is running and healthy.
func (e *Engine) IsAvailable() bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running && e.cmd != nil && e.cmd.Process != nil
}

// NewDetector returns a Detector bound to the given model path.
// If model is empty, the default path under dataDir is used.
// The engine itself satisfies Detector, so it returns self.
func (e *Engine) NewDetector(model string) (ai.Detector, error) {
	if e == nil {
		return nil, fmt.Errorf("ai engine: nil engine")
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	if model == "" {
		model = filepath.Join(e.dataDir, "models", "yolov11n.onnx")
	}
	e.model = model
	return e, nil
}

// --- Detector interface ---

// Detect sends a frame to the ONNX subprocess and returns detections.
// Frame should be JPEG-encoded image bytes.
func (e *Engine) Detect(ctx context.Context, frame []byte) ([]ai.Detection, error) {
	if e == nil {
		return nil, fmt.Errorf("ai engine: nil engine")
	}
	e.mu.Lock()
	if !e.running || e.stdin == nil {
		e.mu.Unlock()
		return nil, fmt.Errorf("ai engine: not running")
	}
	stdin := e.stdin
	stdout := e.stdout
	e.mu.Unlock()

	// Encode frame as base64.
	b64 := base64.StdEncoding.EncodeToString(frame)
	if len(b64) > maxFrameSize {
		return nil, fmt.Errorf("ai engine: frame too large (%d bytes base64)", len(b64))
	}

	// Build and send request.
	req := detectRequest{
		Frame: b64,
		Width:  0, // unknown — subprocess determines from decoded JPEG
		Height: 0,
	}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("ai engine: marshal request: %w", err)
	}
	data = append(data, '\n')

	if _, err := stdin.Write(data); err != nil {
		return nil, fmt.Errorf("ai engine: write to subprocess: %w", err)
	}

	// Read response line with context timeout.
	line, err := readLine(ctx, stdout)
	if err != nil {
		return nil, fmt.Errorf("ai engine: read from subprocess: %w", err)
	}

	var resp detectResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("ai engine: unmarshal response: %w", err)
	}

	if resp.Error != "" {
		return nil, fmt.Errorf("ai engine: subprocess error: %s", resp.Error)
	}

	// Convert raw detections to ai.Detection.
	detections := make([]ai.Detection, 0, len(resp.Detections))
	for _, d := range resp.Detections {
		detections = append(detections, ai.Detection{
			Label:      d.Label,
			Confidence: d.Confidence,
			Box:        d.Box,
		})
	}

	return detections, nil
}

// --- Lifecycle ---

// Start launches the ONNX Runtime subprocess and waits for it to become ready.
// The subprocess is started with the model path as its first argument.
// It monitors for crashes and auto-restarts with exponential backoff.
func (e *Engine) Start(ctx context.Context) error {
	if e == nil {
		return nil
	}

	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return nil
	}
	e.cancel = func() {} // will be replaced below
	e.running = true
	e.ready = make(chan struct{})
	e.done = make(chan struct{})
	e.mu.Unlock()

	// Start the monitor goroutine that manages subprocess lifecycle.
	go e.monitor(ctx)

	// Wait for first ready signal or context cancellation.
	select {
	case <-e.ready:
		return nil
	case <-ctx.Done():
		e.Stop()
		return ctx.Err()
	}
}

// Stop gracefully shuts down the subprocess and waits for it to exit.
func (e *Engine) Stop() {
	if e == nil {
		return
	}
	e.mu.Lock()
	if !e.running {
		e.mu.Unlock()
		return
	}
	e.running = false
	if e.cancel != nil {
		e.cancel()
	}
	e.mu.Unlock()

	// Wait for monitor to finish.
	select {
	case <-e.done:
	case <-time.After(5 * time.Second):
		logger.Warn("ai engine: stop timed out waiting for monitor")
	}
	logger.Info("ai engine: stopped")
}

// monitor manages the subprocess lifecycle: start, crash recovery, shutdown.
func (e *Engine) monitor(parentCtx context.Context) {
	defer close(e.done)

	for {
		// Check if we should stop before (re)starting.
		e.mu.Lock()
		if !e.running {
			e.mu.Unlock()
			return
		}
		e.mu.Unlock()

		if parentCtx.Err() != nil {
			return
		}

		// Apply backoff delay after consecutive crashes.
		e.mu.Lock()
		crashes := e.consecutiveCrashes
		lastCrash := e.lastCrash
		e.mu.Unlock()

		if crashes > 0 {
			delay := backoffDuration(crashes)
			elapsed := time.Since(lastCrash)
			if remaining := delay - elapsed; remaining > 0 {
				logger.Warn("ai engine: waiting before restart",
					"crashes", crashes,
					"wait", remaining.Round(time.Millisecond),
				)
				select {
				case <-time.After(remaining):
				case <-parentCtx.Done():
					return
				}
			}
		}

		// Start subprocess.
		if err := e.startSubprocess(parentCtx); err != nil {
			e.mu.Lock()
			e.consecutiveCrashes++
			e.lastCrash = time.Now()
			e.mu.Unlock()

			logger.Warn("ai engine: subprocess failed to start", "error", err)
			if parentCtx.Err() != nil {
				return
			}
			continue
		}

		// Subprocess exited — record crash state.
		e.mu.Lock()
		e.consecutiveCrashes++
		e.lastCrash = time.Now()
		e.mu.Unlock()

		logger.Warn("ai engine: subprocess exited unexpectedly")

		if parentCtx.Err() != nil {
			return
		}
	}
}

// startSubprocess launches the ONNX Runtime binary and waits for it to exit.
func (e *Engine) startSubprocess(parentCtx context.Context) error {
	binPath := filepath.Join(e.dataDir, "tools", "onnxruntime")
	if _, err := os.Stat(binPath); err != nil {
		return fmt.Errorf("onnxruntime binary not found at %s: %w", binPath, err)
	}

	e.mu.Lock()
	model := e.model
	if model == "" {
		model = filepath.Join(e.dataDir, "models", "yolov11n.onnx")
	}
	e.mu.Unlock()

	ctx, cancel := context.WithCancel(parentCtx)

	e.mu.Lock()
	e.cancel = cancel
	e.mu.Unlock()

	cmd := exec.CommandContext(ctx, binPath, "--model", model)
	cmd.Stderr = new(bytesBuf)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stdin pipe: %w", err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		stdinPipe.Close()
		cancel()
		return fmt.Errorf("stdout pipe: %w", err)
	}

	e.mu.Lock()
	e.cmd = cmd
	e.stdin = stdinPipe
	e.stdout = stdoutPipe
	e.stderrBuf = cmd.Stderr.(*bytesBuf)
	e.mu.Unlock()

	if err := cmd.Start(); err != nil {
		e.mu.Lock()
		e.cmd = nil
		e.stdin = nil
		e.stdout = nil
		e.mu.Unlock()
		cancel()
		return fmt.Errorf("start subprocess: %w", err)
	}

	// Reset crash counter on successful start.
	e.mu.Lock()
	e.consecutiveCrashes = 0
	e.mu.Unlock()

	logger.Info("ai engine: subprocess started",
		"pid", cmd.Process.Pid,
		"model", model,
	)

	// Wait for ready signal or exit.
	errCh := make(chan error, 1)
	go func() {
		errCh <- cmd.Wait()
	}()

	select {
	case <-errCh:
		stderr := e.stderrBuf
		return fmt.Errorf("subprocess exited immediately: %s", stderr.String())
	case <-e.ready:
		// First start ready — signal the Start() caller.
		// Subsequent restarts: close ready again if it was already closed.
		select {
		case <-e.ready:
			// Already closed, this is a restart.
		default:
			close(e.ready)
		}
	case <-ctx.Done():
		return ctx.Err()
	}

	// Wait for subprocess to exit in foreground (blocks monitor).
	if err := <-errCh; err != nil {
		if ctx.Err() != nil {
			return nil // intentional shutdown
		}
		return err
	}
	return nil
}

// SignalReady should be called by the subprocess readiness detection.
// In production this would read a "ready" line from stdout.
// For testing, it can be called directly.
func (e *Engine) SignalReady() {
	e.mu.Lock()
	defer e.mu.Unlock()
	select {
	case <-e.ready:
		// already closed
	default:
		close(e.ready)
	}
}

// --- Helpers ---

// readLine reads a single line from the reader with context cancellation.
func readLine(ctx context.Context, r io.Reader) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		reader := bufio.NewReaderSize(r, 65536)
		data, err := reader.ReadBytes('\n')
		ch <- result{data, err}
	}()

	select {
	case res := <-ch:
		if res.err != nil {
			return nil, res.err
		}
		// Strip trailing newline.
		if len(res.data) > 0 && res.data[len(res.data)-1] == '\n' {
			res.data = res.data[:len(res.data)-1]
		}
		return res.data, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// backoffDuration returns the wait time before the next restart attempt.
// Caps at defaultRestartBackoff (30s).
func backoffDuration(crashes int) time.Duration {
	if crashes <= 0 {
		return 0
	}
	d := time.Duration(1<<(crashes-1)) * time.Second
	if d > defaultRestartBackoff {
		d = defaultRestartBackoff
	}
	return d
}

// HealthCheck sends a ping to the subprocess and waits for a response.
func (e *Engine) HealthCheck(ctx context.Context) error {
	if !e.IsAvailable() {
		return fmt.Errorf("ai engine: not available")
	}
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()

	_, err := e.Detect(ctx, nil)
	return err
}

// ModelPath returns the currently configured model path.
func (e *Engine) ModelPath() string {
	if e == nil {
		return ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.model == "" {
		return filepath.Join(e.dataDir, "models", "yolov11n.onnx")
	}
	return e.model
}

// BinaryPath returns the expected ONNX Runtime binary path.
func (e *Engine) BinaryPath() string {
	if e == nil {
		return ""
	}
	return filepath.Join(e.dataDir, "tools", "onnxruntime")
}
