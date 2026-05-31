package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/ai"
)

// --- Test helpers ---

// setupMockEngine creates an Engine with mocked pipes simulating a running
// subprocess. The engine is set to running=true with pipes wired up.
// The ready channel is pre-closed so Start() calls succeed immediately.
func setupMockEngine(t *testing.T) (*Engine, *mockPipes) {
	t.Helper()

	dir := t.TempDir()
	toolsDir := filepath.Join(dir, "tools")
	modelsDir := filepath.Join(dir, "models")
	if err := os.MkdirAll(toolsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(modelsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a fake onnxruntime binary (just needs to exist for path checks).
	binPath := filepath.Join(toolsDir, "onnxruntime")
	f, err := os.Create(binPath)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := os.Chmod(binPath, 0755); err != nil {
		t.Fatal(err)
	}

	e := New(dir)

	// Wire up pipes: engine writes to engineWriteToSubprocess (subprocess reads),
	// engine reads from engineReadFromSubprocess (subprocess writes).
	subprocessReadFromEngine, engineWriteToSubprocess := io.Pipe()
	engineReadFromSubprocess, subprocessWriteToEngine := io.Pipe()

	pipes := &mockPipes{
		readFromEngine:      subprocessReadFromEngine,
		writeToEngine:       engineWriteToSubprocess,
		writeToSubprocess:   subprocessWriteToEngine,
		readFromSubprocess:  engineReadFromSubprocess,
	}

	e.mu.Lock()
	e.running = true
	e.ready = make(chan struct{})
	e.done = make(chan struct{})
	e.stdin = engineWriteToSubprocess
	e.stdout = engineReadFromSubprocess
	e.cmd = &exec.Cmd{Process: &os.Process{Pid: 1234}} // fake cmd+process for IsAvailable check
	e.cancel = func() { close(e.done) } // cancel closes done for clean Stop()
	e.mu.Unlock()

	// Pre-close ready so any waits succeed.
	close(e.ready)

	return e, pipes
}

// mockPipes holds the pipe ends for controlling a mocked subprocess.
type mockPipes struct {
	readFromEngine     io.ReadCloser   // subprocess reads requests from engine
	writeToEngine      *io.PipeWriter  // engine writes requests to subprocess
	writeToSubprocess  io.WriteCloser  // subprocess writes responses to engine
	readFromSubprocess *io.PipeReader  // engine reads responses from subprocess
}

// writeResponse writes a JSON response line as the subprocess would.
func (p *mockPipes) writeResponse(t *testing.T, resp any) {
	t.Helper()
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if _, err := p.writeToSubprocess.Write(data); err != nil {
		t.Fatal(err)
	}
}

// readRequest reads and unmarshals a request from the engine.
func (p *mockPipes) readRequest(t *testing.T) detectRequest {
	t.Helper()
	dec := json.NewDecoder(p.readFromEngine)
	var req detectRequest
	if err := dec.Decode(&req); err != nil {
		t.Fatal(err)
	}
	return req
}

// closeAll closes all pipe ends.
func (p *mockPipes) closeAll(t *testing.T) {
	t.Helper()
	p.writeToSubprocess.Close()
	p.readFromEngine.Close()
}

// --- Tests ---

func TestNew(t *testing.T) {
	dir := t.TempDir()
	e := New(dir)

	if e == nil {
		t.Fatal("expected non-nil engine")
	}
	if e.dataDir != dir {
		t.Fatalf("expected dataDir=%q, got %q", dir, e.dataDir)
	}
	if e.IsAvailable() {
		t.Fatal("new engine should not be available")
	}
}

func TestName(t *testing.T) {
	dir := t.TempDir()
	e := New(dir)

	if got := e.Name(); got != "onnxruntime-local" {
		t.Fatalf("expected Name()=%q, got %q", "onnxruntime-local", got)
	}
}

func TestNilReceiver(t *testing.T) {
	var e *Engine

	if e.IsAvailable() {
		t.Fatal("nil IsAvailable() should return false")
	}
	_, err := e.NewDetector("model")
	if err == nil {
		t.Fatal("nil NewDetector() should return error")
	}
	_, err = e.Detect(context.Background(), nil)
	if err == nil {
		t.Fatal("nil Detect() should return error")
	}
	e.Stop() // should not panic
}

func TestIsAvailable_States(t *testing.T) {
	e, pipes := setupMockEngine(t)
	defer pipes.closeAll(t)

	if !e.IsAvailable() {
		t.Fatal("expected IsAvailable()=true after setup")
	}

	e.Stop()

	if e.IsAvailable() {
		t.Fatal("expected IsAvailable()=false after Stop()")
	}
}

func TestNewDetector(t *testing.T) {
	dir := t.TempDir()
	e := New(dir)

	// With model path
	det, err := e.NewDetector("/custom/model.onnx")
	if err != nil {
		t.Fatalf("NewDetector failed: %v", err)
	}
	if det != e {
		t.Fatal("NewDetector should return self")
	}
	if e.model != "/custom/model.onnx" {
		t.Fatalf("expected model=/custom/model.onnx, got %q", e.model)
	}

	// Empty model uses default
	det, err = e.NewDetector("")
	if err != nil {
		t.Fatalf("NewDetector empty failed: %v", err)
	}
	if det != e {
		t.Fatal("NewDetector should return self")
	}
	expected := filepath.Join(dir, "models", "yolov11n.onnx")
	if e.model != expected {
		t.Fatalf("expected model=%q, got %q", expected, e.model)
	}
}

func TestModelPath(t *testing.T) {
	dir := t.TempDir()
	e := New(dir)

	expected := filepath.Join(dir, "models", "yolov11n.onnx")
	if got := e.ModelPath(); got != expected {
		t.Fatalf("expected ModelPath=%q, got %q", expected, got)
	}

	e.model = "/custom/model.onnx"
	if got := e.ModelPath(); got != "/custom/model.onnx" {
		t.Fatalf("expected ModelPath=/custom/model.onnx, got %q", got)
	}
}

func TestBinaryPath(t *testing.T) {
	dir := t.TempDir()
	e := New(dir)

	expected := filepath.Join(dir, "tools", "onnxruntime")
	if got := e.BinaryPath(); got != expected {
		t.Fatalf("expected BinaryPath=%q, got %q", expected, got)
	}
}

func TestDetect_MockSubprocess(t *testing.T) {
	e, pipes := setupMockEngine(t)
	defer pipes.closeAll(t)

	// Spawn a goroutine to read request and write response.
	done := make(chan struct{})
	go func() {
		defer close(done)
		req := pipes.readRequest(t)
		if req.Frame == "" {
			t.Error("expected non-empty frame base64")
		}

		resp := detectResponse{
			Detections: []rawDetection{
				{Label: "person", Confidence: 0.95, Box: [4]float32{0.1, 0.2, 0.3, 0.4}},
				{Label: "car", Confidence: 0.87, Box: [4]float32{0.5, 0.5, 0.2, 0.3}},
			},
		}
		pipes.writeResponse(t, resp)
	}()

	frame := []byte("fake-jpeg-data")
	detections, err := e.Detect(context.Background(), frame)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if len(detections) != 2 {
		t.Fatalf("expected 2 detections, got %d", len(detections))
	}

	if detections[0].Label != "person" {
		t.Fatalf("expected label=person, got %q", detections[0].Label)
	}
	if detections[0].Confidence != 0.95 {
		t.Fatalf("expected confidence=0.95, got %f", detections[0].Confidence)
	}
	if detections[0].Box != [4]float32{0.1, 0.2, 0.3, 0.4} {
		t.Fatalf("expected box=[0.1,0.2,0.3,0.4], got %v", detections[0].Box)
	}

	<-done // wait for goroutine to finish
}

func TestDetect_EmptyDetections(t *testing.T) {
	e, pipes := setupMockEngine(t)
	defer pipes.closeAll(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		pipes.readRequest(t)
		resp := detectResponse{Detections: nil}
		pipes.writeResponse(t, resp)
	}()

	detections, err := e.Detect(context.Background(), []byte("frame"))
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}
	if len(detections) != 0 {
		t.Fatalf("expected 0 detections, got %d", len(detections))
	}

	<-done
}

func TestDetect_SubprocessError(t *testing.T) {
	e, pipes := setupMockEngine(t)
	defer pipes.closeAll(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		pipes.readRequest(t)
		resp := detectResponse{Error: "model load failed"}
		pipes.writeResponse(t, resp)
	}()

	_, err := e.Detect(context.Background(), []byte("frame"))
	if err == nil {
		t.Fatal("expected error when subprocess returns error")
	}
	if got := err.Error(); got == "" {
		t.Fatal("expected non-empty error message")
	}

	<-done
}

func TestDetect_NotRunning(t *testing.T) {
	dir := t.TempDir()
	e := New(dir)

	_, err := e.Detect(context.Background(), []byte("frame"))
	if err == nil {
		t.Fatal("expected error when engine not running")
	}
}

func TestDetect_ContextCancelled(t *testing.T) {
	e, pipes := setupMockEngine(t)

	// Close the subprocess output side — readLine will get context cancellation
	// before data arrives. But we need to close the write side too so that
	// the readLine goroutine's bufio reader gets EOF (not context cancel).
	// Since context is already cancelled, readLine returns ctx.Err().
	pipes.writeToSubprocess.Close()
	pipes.readFromEngine.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := e.Detect(ctx, []byte("frame"))
	if err == nil {
		t.Fatal("expected error when context cancelled")
	}
}

func TestDetect_SubprocessCrash_WriteFail(t *testing.T) {
	e, pipes := setupMockEngine(t)

	// Close the engine-side write pipe (stdin closed = subprocess crashed on read side).
	pipes.writeToEngine.Close()

	_, err := e.Detect(context.Background(), []byte("frame"))
	if err == nil {
		t.Fatal("expected error when engine stdin is closed")
	}
}

func TestDetect_SubprocessCrash_ReadFail(t *testing.T) {
	e, pipes := setupMockEngine(t)
	defer pipes.closeAll(t)

	// Close the read side of engine stdin pipe so writes fail fast.
	// This simulates the subprocess having crashed.
	pipes.readFromEngine.Close()

	_, err := e.Detect(context.Background(), []byte("frame"))
	if err == nil {
		t.Fatal("expected error when subprocess pipes are closed")
	}
}

func TestDetect_Concurrent(t *testing.T) {
	e, pipes := setupMockEngine(t)
	defer pipes.closeAll(t)

	const numGoroutines = 5
	var wg sync.WaitGroup
	var failCount atomic.Int32

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// Each goroutine: read request in a helper, respond, then detect.
			// The request/response order is sequential due to pipe semantics.
			done := make(chan struct{})
			go func() {
				defer close(done)
				req := pipes.readRequest(t)
				if req.Frame == "" {
					t.Errorf("goroutine %d: empty frame", id)
				}
				resp := detectResponse{
					Detections: []rawDetection{
						{Label: "person", Confidence: 0.9, Box: [4]float32{0.1, 0.1, 0.2, 0.2}},
					},
				}
				pipes.writeResponse(t, resp)
			}()

			detections, err := e.Detect(context.Background(), []byte(fmt.Sprintf("frame-%d", id)))
			if err != nil {
				failCount.Add(1)
				t.Errorf("goroutine %d: Detect failed: %v", id, err)
				return
			}
			if len(detections) != 1 {
				t.Errorf("goroutine %d: expected 1 detection, got %d", id, len(detections))
				return
			}
			if detections[0].Label != "person" {
				t.Errorf("goroutine %d: expected label=person, got %q", id, detections[0].Label)
			}

			<-done
		}(i)
	}

	wg.Wait()
	if got := failCount.Load(); got != 0 {
		t.Fatalf("%d goroutines failed", got)
	}
}

func TestStop_Idempotent(t *testing.T) {
	dir := t.TempDir()
	e := New(dir)

	e.Stop() // should not panic
	e.Stop() // double stop should not panic

	if e.IsAvailable() {
		t.Fatal("engine should not be available after stop")
	}
}

func TestStart_AlreadyRunning(t *testing.T) {
	e, pipes := setupMockEngine(t)
	defer pipes.closeAll(t)

	// Start on already-running engine should be no-op.
	err := e.Start(context.Background())
	if err != nil {
		t.Fatalf("Start on running engine should return nil, got: %v", err)
	}
}

func TestStart_NilEngine(t *testing.T) {
	var e *Engine

	// Nil engine Start should be no-op.
	err := e.Start(context.Background())
	if err != nil {
		t.Fatalf("Start on nil engine should return nil, got: %v", err)
	}
}

func TestBackoffDuration(t *testing.T) {
	tests := []struct {
		crashes  int
		expected time.Duration
	}{
		{0, 0},
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 16 * time.Second},
		{6, 30 * time.Second},  // capped
		{10, 30 * time.Second}, // capped
	}

	for _, tt := range tests {
		got := backoffDuration(tt.crashes)
		if got != tt.expected {
			t.Errorf("backoffDuration(%d) = %v, want %v", tt.crashes, got, tt.expected)
		}
	}
}

func TestBytesBuf(t *testing.T) {
	b := &bytesBuf{}

	n, err := b.Write([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("expected 5 bytes written, got %d", n)
	}
	if b.String() != "hello" {
		t.Fatalf("expected %q, got %q", "hello", b.String())
	}

	// Write beyond 4KB cap — buffer should be trimmed.
	large := bytes.Repeat([]byte("x"), 8192)
	b.Write(large)
	s := b.String()
	if len(s) > 4096 {
		t.Fatalf("expected bytesBuf capped at 4096, got %d", len(s))
	}
}

func TestInterfaceSatisfaction(t *testing.T) {
	// Compile-time check — if this compiles, the interfaces are satisfied.
	var _ ai.AIProvider = (*Engine)(nil)
	var _ ai.Detector = (*Engine)(nil)

	// Also check that NewDetector returns a Detector.
	dir := t.TempDir()
	e := New(dir)
	det, err := e.NewDetector("model")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := det.(ai.Detector); !ok {
		t.Fatal("NewDetector should return a Detector")
	}
}

func TestDetect_FrameTooLarge(t *testing.T) {
	e, pipes := setupMockEngine(t)
	defer pipes.closeAll(t)

	// Temporarily lower maxFrameSize for testing.
	originalMaxFrameSize := maxFrameSize
	maxFrameSize = 100 // very small for testing
	defer func() { maxFrameSize = originalMaxFrameSize }()

	// Create a frame whose base64 encoding exceeds maxFrameSize.
	largeFrame := bytes.Repeat([]byte("x"), 1000) // base64 will be > 100

	_, err := e.Detect(context.Background(), largeFrame)
	if err == nil {
		t.Fatal("expected error for frame too large")
	}
}

func TestHealthCheck_NotAvailable(t *testing.T) {
	dir := t.TempDir()
	e := New(dir)

	err := e.HealthCheck(context.Background())
	if err == nil {
		t.Fatal("expected error when engine not available")
	}
}

func TestDetect_ProtocolFormat(t *testing.T) {
	e, pipes := setupMockEngine(t)
	defer pipes.closeAll(t)

	// Verify the exact JSON format sent to subprocess.
	done := make(chan struct{})
	go func() {
		defer close(done)
		req := pipes.readRequest(t)

		// Verify request structure.
		if req.Width != 0 || req.Height != 0 {
			t.Errorf("expected width=0 height=0, got %d %d", req.Width, req.Height)
		}
		// Frame should be valid base64.
		if req.Frame == "" {
			t.Error("expected non-empty base64 frame")
		}

		// Respond with known data.
		resp := detectResponse{
			Detections: []rawDetection{
				{Label: "dog", Confidence: 0.42, Box: [4]float32{0.0, 0.0, 1.0, 1.0}},
			},
		}
		pipes.writeResponse(t, resp)
	}()

	detections, err := e.Detect(context.Background(), []byte("test-frame"))
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}
	if len(detections) != 1 {
		t.Fatalf("expected 1 detection, got %d", len(detections))
	}
	if detections[0].Label != "dog" {
		t.Fatalf("expected label=dog, got %q", detections[0].Label)
	}

	<-done
}

func TestSignalReady_DoubleClose(t *testing.T) {
	e, pipes := setupMockEngine(t)
	defer pipes.closeAll(t)

	// ready is already closed by setupMockEngine.
	// Calling SignalReady again should not panic.
	e.SignalReady()
	e.SignalReady()
}

func TestReadLine_ContextTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	// Create a pipe and close the write end — read will return EOF,
	// but we want to test context timeout. Use an open pipe reader.
	r, _ := io.Pipe()

	_, err := readLine(ctx, r)
	if err == nil {
		t.Fatal("expected error from readLine with timeout")
	}
	r.Close()
}

func TestReadLine_Success(t *testing.T) {
	ctx := context.Background()
	r, w := io.Pipe()

	go func() {
		w.Write([]byte("hello\n"))
		w.Close()
	}()

	line, err := readLine(ctx, r)
	if err != nil {
		t.Fatalf("readLine failed: %v", err)
	}
	if string(line) != "hello" {
		t.Fatalf("expected %q, got %q", "hello", string(line))
	}
}

func TestReadLine_StripsNewline(t *testing.T) {
	ctx := context.Background()
	r, w := io.Pipe()

	go func() {
		w.Write([]byte("data\n"))
		w.Close()
	}()

	line, err := readLine(ctx, r)
	if err != nil {
		t.Fatalf("readLine failed: %v", err)
	}
	if string(line) != "data" {
		t.Fatalf("expected newline stripped, got %q", string(line))
	}
}
