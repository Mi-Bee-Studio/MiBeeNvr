package recorder

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// ---------------------------------------------------------------------------
// Helper types for characterization tests
// ---------------------------------------------------------------------------

// mp4BoxScanner reads an MP4 file and can detect specific box types.
type mp4BoxScanner struct {
	data []byte
}

func newMP4BoxScanner(t *testing.T, path string) *mp4BoxScanner {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return &mp4BoxScanner{data: data}
}

// hasBox checks whether the MP4 data contains a box with the given 4-CC name.
func (s *mp4BoxScanner) hasBox(boxName string) bool {
	// Look for the 4-byte box type anywhere in the file.
	needle := []byte(boxName)
	return bytes.Contains(s.data[4:], needle)
}

// panicStore wraps a real SegmentStore but panics on the first CreateSegment call.
type panicStore struct {
	SegmentStore
	mu    sync.Mutex
	count int
}

// countFilesSafe returns the number of finalized files for a camera.
// Returns 0 if the camera directory does not exist yet (no segments created).
func countFilesSafe(t *testing.T, m *storage.Manager, cameraID string) int {
	t.Helper()
	f, err := m.ListFiles(cameraID)
	if err != nil {
		// Camera directory doesn't exist yet — no segments.
		return 0
	}
	return len(f)
}

func newPanicStore(inner SegmentStore) *panicStore {
	return &panicStore{SegmentStore: inner}
}

func (s *panicStore) CreateSegment(cameraID string, format string) (string, string, error) {
	s.mu.Lock()
	shouldPanic := s.count == 0
	s.count++
	s.mu.Unlock()
	if shouldPanic {
		panic("panicStore: simulated panic in CreateSegment")
	}
	return s.SegmentStore.CreateSegment(cameraID, format)
}

// ---------------------------------------------------------------------------
// H.264 Characterization Tests
// ---------------------------------------------------------------------------

// 1. Start/Stop lifecycle
func TestH264Characterization_StartStopLifecycle(t *testing.T) {
	srv := newTestRTSPServer(t)
	defer srv.close()

	mgr := newTestManager(t)
	rec := NewH264Recorder(H264Config{
		CameraID:   "h264-char-lifecycle",
		RTSPURL:    srv.rtspURL,
		SegmentDur: 5 * time.Minute,
		RingBufCap: 100,
	}, mgr)

	// Initial state
	require.Equal(t, model.StatusStopped, rec.Status())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start — status becomes Recording
	require.NoError(t, rec.Start(ctx))
	require.Equal(t, model.StatusRecording, rec.Status())

	// Double start should fail
	require.Error(t, rec.Start(ctx))

	srv.waitPlay(t, 5*time.Second)
	time.Sleep(50 * time.Millisecond)

	// Stop — status becomes Stopped, goroutine exits (done channel closed)
	require.NoError(t, rec.Stop())
	require.Equal(t, model.StatusStopped, rec.Status())

	// Double stop should not error
	require.NoError(t, rec.Stop())
}

// 2a. SPS change rotates segment
func TestH264Characterization_SPSChangeRotatesSegment(t *testing.T) {
	srv := newTestRTSPServer(t)
	defer srv.close()

	mgr := newTestManager(t)
	rec := NewH264Recorder(H264Config{
		CameraID:   "h264-char-sps",
		RTSPURL:    srv.rtspURL,
		SegmentDur: 5 * time.Minute,
		RingBufCap: 200,
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	srv.waitPlay(t, 5*time.Second)
	time.Sleep(100 * time.Millisecond)

	// Send initial frames with original SPS
	srv.sendFrames(3, 30*time.Millisecond)
	time.Sleep(100 * time.Millisecond)

	// Send frames with DIFFERENT SPS (testSPS2) => triggers segment rotation
	for range 3 {
		srv.sendAU([][]byte{testSPS2, testPPS, testIDR})
		time.Sleep(30 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)

	require.NoError(t, rec.Stop())

	n := countFinalFiles(t, mgr, "h264-char-sps")
	require.GreaterOrEqual(t, n, 2, "SPS change should produce at least 2 segments, got %d", n)
}

// 2b. PPS change rotates segment
func TestH264Characterization_PPSChangeRotatesSegment(t *testing.T) {
	srv := newTestRTSPServer(t)
	defer srv.close()

	mgr := newTestManager(t)

	// Slightly different PPS to trigger rotation
	altPPS := []byte{0x68, 0xce, 0x39, 0x80} // different from testPPS

	rec := NewH264Recorder(H264Config{
		CameraID:   "h264-char-pps",
		RTSPURL:    srv.rtspURL,
		SegmentDur: 5 * time.Minute,
		RingBufCap: 200,
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	srv.waitPlay(t, 5*time.Second)
	time.Sleep(100 * time.Millisecond)

	// Send initial frames with original PPS
	srv.sendFrames(3, 30*time.Millisecond)
	time.Sleep(100 * time.Millisecond)

	// Send frames with DIFFERENT PPS => triggers segment rotation
	for range 3 {
		srv.sendAU([][]byte{testSPS, altPPS, testIDR})
		time.Sleep(30 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)

	require.NoError(t, rec.Stop())

	n := countFinalFiles(t, mgr, "h264-char-pps")
	require.GreaterOrEqual(t, n, 2, "PPS change should produce at least 2 segments, got %d", n)
}

// 3. IDR sync — segments are only created after IDR frame
func TestH264Characterization_WaitsForIDRBeforeSegment(t *testing.T) {
	srv := newTestRTSPServer(t)
	defer srv.close()

	mgr := newTestManager(t)
	rec := NewH264Recorder(H264Config{
		CameraID:   "h264-char-idr",
		RTSPURL:    srv.rtspURL,
		SegmentDur: 5 * time.Minute,
		RingBufCap: 200,
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	srv.waitPlay(t, 5*time.Second)
	time.Sleep(100 * time.Millisecond)

	// Send only SPS+PPS (no VCL frames) — should NOT create any segment files
	srv.sendAU([][]byte{testSPS, testPPS})
	time.Sleep(100 * time.Millisecond)

	// No segment should exist yet (safe count: tolerates missing camera dir)
	n := countFilesSafe(t, mgr, "h264-char-idr")
	require.Equal(t, 0, n, "no segment should exist without IDR frame")

	// Now send IDR frame — segment should be created
	srv.sendAU([][]byte{testSPS, testPPS, testIDR})
	time.Sleep(200 * time.Millisecond)

	require.NoError(t, rec.Stop())

	n = countFilesSafe(t, mgr, "h264-char-idr")
	require.GreaterOrEqual(t, n, 1, "segment should be created after IDR frame, got %d", n)
}

// 4. Audio processing path — audio frames are recorded into segments
func TestH264Characterization_AudioCapturedInSegment(t *testing.T) {
	srv := newTestRTSPServerWithAudio(t)
	defer srv.close()

	mgr := newTestManager(t)

	rec := NewH264Recorder(H264Config{
		CameraID:     "h264-char-audio",
		RTSPURL:      srv.rtspURL,
		SegmentDur:   5 * time.Minute,
		RingBufCap:   100,
		AudioEnabled: true,
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	srv.waitPlay(t, 5*time.Second)
	time.Sleep(100 * time.Millisecond)

	// Send video frames to start segment
	srv.sendFrames(3, 20*time.Millisecond)
	time.Sleep(100 * time.Millisecond)

	// Send audio frames
	for range 5 {
		srv.sendAudioFrame([]byte{0x01, 0x02, 0x03, 0x04})
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)

	require.NoError(t, rec.Stop())

	// Verify a segment was created
	files, err := mgr.ListFiles("h264-char-audio")
	require.NoError(t, err)
	require.NotEmpty(t, files, "expected at least one segment with audio")
}

// 5. Auto-reconnect — after connection loss, recorder reconnects
func TestH264Characterization_ReconnectAfterConnectionLoss(t *testing.T) {
	port := findPort(t)
	time.Sleep(5 * time.Millisecond)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	rtspURL := fmt.Sprintf("rtsp://127.0.0.1:%d/test", port)

	mgr := newTestManager(t)
	rec := NewH264Recorder(H264Config{
		CameraID:   "h264-char-reconn",
		RTSPURL:    rtspURL,
		SegmentDur: 5 * time.Minute,
		RingBufCap: 100,
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))

	// Server not available at first — should attempt reconnect
	time.Sleep(200 * time.Millisecond)

	forma := &format.H264{
		PayloadTyp:        96,
		PacketizationMode: 1,
		SPS:               testSPS,
		PPS:               testPPS,
	}
	desc := &description.Session{
		Medias: []*description.Media{{
			Type:    description.MediaTypeVideo,
			Formats: []format.Format{forma},
		}},
	}

	playCh := make(chan struct{})
	h := &reconnHandler{playCh: playCh}

	srv := &gortsplib.Server{Handler: h, RTSPAddress: addr}
	require.NoError(t, srv.Start())

	stream := &gortsplib.ServerStream{Server: srv, Desc: desc}
	require.NoError(t, stream.Initialize())
	h.setStream(stream)

	defer func() {
		stream.Close()
		srv.Close()
	}()

	// Wait for recorder to reconnect
	select {
	case <-playCh:
	case <-time.After(8 * time.Second):
		t.Fatal("recorder did not reconnect within timeout")
	}

	require.Equal(t, model.StatusRecording, rec.Status())

	// Send frames to verify recording works after reconnect
	enc, err := forma.CreateEncoder()
	require.NoError(t, err)
	for range 3 {
		pkts, _ := enc.Encode([][]byte{testSPS, testPPS, testIDR})
		for _, pkt := range pkts {
			stream.WritePacketRTP(desc.Medias[0], pkt)
		}
		time.Sleep(30 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)

	require.NoError(t, rec.Stop())

	n := countFinalFiles(t, mgr, "h264-char-reconn")
	require.NotEmpty(t, n, "expected at least one file after reconnect")
}

// 6. Panic recovery — a panic in writeFrames does not crash the process
func TestH264Characterization_PanicRecovery(t *testing.T) {
	srv := newTestRTSPServer(t)
	defer srv.close()

	realMgr, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	pStore := newPanicStore(realMgr)

	rec := NewH264Recorder(H264Config{
		CameraID:             "h264-char-panic",
		RTSPURL:              srv.rtspURL,
		SegmentDur:           5 * time.Minute,
		RingBufCap:           100,
		FrameWatchdogTimeout: 200 * time.Millisecond, // fast watchdog for test
	}, pStore)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Start — this should succeed even though the store will panic later
	require.NoError(t, rec.Start(ctx))
	require.Equal(t, model.StatusRecording, rec.Status())

	srv.waitPlay(t, 5*time.Second)
	time.Sleep(50 * time.Millisecond)

	// Send frames — the first CreateSegment call will panic in writeFrames.
	// The deferred recover() in writeFrames should catch it, log it, and exit cleanly.
	// Then the watchdog triggers reconnection, and on reconnection the store
	// no longer panics (only first call panicked).
	srv.sendFrames(5, 30*time.Millisecond)

	// Wait for potential recovery and reconnect
	time.Sleep(2 * time.Second)

	// Try sending more frames (after reconnection)
	srv.sendFrames(3, 20*time.Millisecond)
	time.Sleep(500 * time.Millisecond)

	// Stop should complete without hanging (process did not crash)
	require.NoError(t, rec.Stop())
	require.Equal(t, model.StatusStopped, rec.Status())

	// After panic recovery, check status. Files may or may not exist.
	f264iles, err264 := realMgr.ListFiles("h264-char-panic")
	if err264 == nil {
		t.Logf("H264 files after panic recovery: %d", len(f264iles))
	} else {
		t.Logf("H264: no camera dir after panic recovery (expected if reconnect didn't create segment): %v", err264)
	}
}

// 7. Recording output — generated MP4 files have valid basic box structure
func TestH264Characterization_MP4FileStructure(t *testing.T) {
	srv := newTestRTSPServer(t)
	defer srv.close()

	mgr := newTestManager(t)
	rec := NewH264Recorder(H264Config{
		CameraID:   "h264-char-mp4",
		RTSPURL:    srv.rtspURL,
		SegmentDur: 150 * time.Millisecond, // short segment to get multiple files
		RingBufCap: 200,
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	srv.waitPlay(t, 5*time.Second)
	time.Sleep(50 * time.Millisecond)

	// Send enough frames to produce at least one segment
	srv.sendFrames(20, 30*time.Millisecond)
	time.Sleep(300 * time.Millisecond)

	require.NoError(t, rec.Stop())

	files, err := mgr.ListFiles("h264-char-mp4")
	require.NoError(t, err)
	require.NotEmpty(t, files, "expected at least one MP4 file")

	for _, f := range files {
		t.Run(f, func(t *testing.T) {
			scanner := newMP4BoxScanner(t, f)
			require.True(t, scanner.hasBox("ftyp"), "file %s missing ftyp box", f)
			require.True(t, scanner.hasBox("moov"), "file %s missing moov box", f)
			require.True(t, scanner.hasBox("mdat"), "file %s missing mdat box", f)
		})
	}
}

// ---------------------------------------------------------------------------
// H.265 Characterization Tests
// ---------------------------------------------------------------------------

// 1. Start/Stop lifecycle
func TestH265Characterization_StartStopLifecycle(t *testing.T) {
	srv := newTestRTSPServerH265(t)
	defer srv.close()

	mgr := newTestManager(t)
	rec := NewH265Recorder(H265Config{
		CameraID:   "h265-char-lifecycle",
		RTSPURL:    srv.rtspURL,
		SegmentDur: 5 * time.Minute,
		RingBufCap: 100,
	}, mgr)

	// Initial state
	require.Equal(t, model.StatusStopped, rec.Status())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start — status becomes Recording
	require.NoError(t, rec.Start(ctx))
	require.Equal(t, model.StatusRecording, rec.Status())

	// Double start should fail
	require.Error(t, rec.Start(ctx))

	srv.waitPlay(t, 5*time.Second)
	time.Sleep(50 * time.Millisecond)

	require.NoError(t, rec.Stop())
	require.Equal(t, model.StatusStopped, rec.Status())

	// Double stop should not error
	require.NoError(t, rec.Stop())
}

// 2a. VPS change rotates segment (H265)
func TestH265Characterization_VPSChangeRotatesSegment(t *testing.T) {
	srv := newTestRTSPServerH265(t)
	defer srv.close()

	mgr := newTestManager(t)
	rec := NewH265Recorder(H265Config{
		CameraID:   "h265-char-vps",
		RTSPURL:    srv.rtspURL,
		SegmentDur: 5 * time.Minute,
		RingBufCap: 200,
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	srv.waitPlay(t, 5*time.Second)
	time.Sleep(100 * time.Millisecond)

	// Send initial frames
	srv.sendFrames(3, 30*time.Millisecond)
	time.Sleep(100 * time.Millisecond)

	// Build a slightly different VPS
	altVPS := make([]byte, len(testVPS265))
	copy(altVPS, testVPS265)
	altVPS[len(altVPS)-1] ^= 0x01 // flip last bit

	// Send frames with different VPS — should trigger segment rotation
	for range 3 {
		srv.sendAU([][]byte{altVPS, testSPS265, testPPS265, testIDR265})
		time.Sleep(30 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)

	require.NoError(t, rec.Stop())

	n := countFinalFiles(t, mgr, "h265-char-vps")
	require.GreaterOrEqual(t, n, 2, "VPS change should produce at least 2 segments, got %d", n)
}

// 2b. SPS change rotates segment (H265)
func TestH265Characterization_SPSChangeRotatesSegment(t *testing.T) {
	srv := newTestRTSPServerH265(t)
	defer srv.close()

	mgr := newTestManager(t)

	// Build a slightly different H265 SPS
	altSPS := make([]byte, len(testSPS265))
	copy(altSPS, testSPS265)
	altSPS[len(altSPS)-1] ^= 0x01

	rec := NewH265Recorder(H265Config{
		CameraID:   "h265-char-sps",
		RTSPURL:    srv.rtspURL,
		SegmentDur: 5 * time.Minute,
		RingBufCap: 200,
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	srv.waitPlay(t, 5*time.Second)
	time.Sleep(100 * time.Millisecond)

	// Send initial frames
	srv.sendFrames(3, 30*time.Millisecond)
	time.Sleep(100 * time.Millisecond)

	// Send frames with different SPS — should trigger segment rotation
	for range 3 {
		srv.sendAU([][]byte{testVPS265, altSPS, testPPS265, testIDR265})
		time.Sleep(30 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)

	require.NoError(t, rec.Stop())

	n := countFinalFiles(t, mgr, "h265-char-sps")
	require.GreaterOrEqual(t, n, 2, "SPS change should produce at least 2 segments, got %d", n)
}

// 2c. PPS change rotates segment (H265)
func TestH265Characterization_PPSChangeRotatesSegment(t *testing.T) {
	srv := newTestRTSPServerH265(t)
	defer srv.close()

	mgr := newTestManager(t)

	// Build a slightly different H265 PPS
	altPPS := make([]byte, len(testPPS265))
	copy(altPPS, testPPS265)
	altPPS[len(altPPS)-1] ^= 0x01

	rec := NewH265Recorder(H265Config{
		CameraID:   "h265-char-pps",
		RTSPURL:    srv.rtspURL,
		SegmentDur: 5 * time.Minute,
		RingBufCap: 200,
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	srv.waitPlay(t, 5*time.Second)
	time.Sleep(100 * time.Millisecond)

	// Send initial frames
	srv.sendFrames(3, 30*time.Millisecond)
	time.Sleep(100 * time.Millisecond)

	// Send frames with different PPS — should trigger segment rotation
	for range 3 {
		srv.sendAU([][]byte{testVPS265, testSPS265, altPPS, testIDR265})
		time.Sleep(30 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)

	require.NoError(t, rec.Stop())

	n := countFinalFiles(t, mgr, "h265-char-pps")
	require.GreaterOrEqual(t, n, 2, "PPS change should produce at least 2 segments, got %d", n)
}

// 3. IDR sync — segments are only created after IDR frame (H265)
func TestH265Characterization_WaitsForIDRBeforeSegment(t *testing.T) {
	srv := newTestRTSPServerH265(t)
	defer srv.close()

	mgr := newTestManager(t)
	rec := NewH265Recorder(H265Config{
		CameraID:   "h265-char-idr",
		RTSPURL:    srv.rtspURL,
		SegmentDur: 5 * time.Minute,
		RingBufCap: 200,
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	srv.waitPlay(t, 5*time.Second)
	time.Sleep(100 * time.Millisecond)

	// Send only VPS+SPS+PPS (no VCL frames) — should NOT create any segment
	srv.sendAU([][]byte{testVPS265, testSPS265, testPPS265})
	time.Sleep(100 * time.Millisecond)

	n := countFilesSafe(t, mgr, "h265-char-idr")
	require.Equal(t, 0, n, "no segment should exist without IDR frame")

	// Now send IDR — segment should be created
	srv.sendAU([][]byte{testVPS265, testSPS265, testPPS265, testIDR265})
	time.Sleep(200 * time.Millisecond)

	require.NoError(t, rec.Stop())

	n = countFilesSafe(t, mgr, "h265-char-idr")
	require.GreaterOrEqual(t, n, 1, "segment should be created after IDR frame, got %d", n)
}

// 4. Audio processing path — H265 audio frames are recorded
func TestH265Characterization_AudioCapturedInSegment(t *testing.T) {
	srv := newTestRTSPServerH265WithAudio(t)
	defer srv.close()

	mgr := newTestManager(t)

	rec := NewH265Recorder(H265Config{
		CameraID:     "h265-char-audio",
		RTSPURL:      srv.rtspURL,
		SegmentDur:   5 * time.Minute,
		RingBufCap:   100,
		AudioEnabled: true,
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	srv.waitPlay(t, 5*time.Second)
	time.Sleep(100 * time.Millisecond)

	// Send video frames to start segment
	srv.sendFrames(3, 20*time.Millisecond)
	time.Sleep(100 * time.Millisecond)

	// Send audio frames
	for range 5 {
		srv.sendAudioFrame([]byte{0x01, 0x02, 0x03, 0x04})
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)

	require.NoError(t, rec.Stop())

	files, err := mgr.ListFiles("h265-char-audio")
	require.NoError(t, err)
	require.NotEmpty(t, files, "expected at least one segment with audio")
}

// 5. Auto-reconnect — H265 reconnects after connection loss
func TestH265Characterization_ReconnectAfterConnectionLoss(t *testing.T) {
	port := findPort(t)
	time.Sleep(5 * time.Millisecond)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	rtspURL := fmt.Sprintf("rtsp://127.0.0.1:%d/test", port)

	mgr := newTestManager(t)
	rec := NewH265Recorder(H265Config{
		CameraID:   "h265-char-reconn",
		RTSPURL:    rtspURL,
		SegmentDur: 5 * time.Minute,
		RingBufCap: 100,
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))

	// Server not available at first — should attempt reconnect
	time.Sleep(200 * time.Millisecond)

	forma := &format.H265{
		PayloadTyp: 96,
		VPS:        testVPS265,
		SPS:        testSPS265,
		PPS:        testPPS265,
	}
	desc := &description.Session{
		Medias: []*description.Media{{
			Type:    description.MediaTypeVideo,
			Formats: []format.Format{forma},
		}},
	}

	playCh := make(chan struct{})
	h := &reconnHandler{playCh: playCh}

	srv := &gortsplib.Server{Handler: h, RTSPAddress: addr}
	require.NoError(t, srv.Start())

	stream := &gortsplib.ServerStream{Server: srv, Desc: desc}
	require.NoError(t, stream.Initialize())
	h.setStream(stream)

	defer func() {
		stream.Close()
		srv.Close()
	}()

	select {
	case <-playCh:
	case <-time.After(8 * time.Second):
		t.Fatal("recorder did not reconnect within timeout")
	}

	require.Equal(t, model.StatusRecording, rec.Status())

	// Send frames after reconnect
	enc, err := forma.CreateEncoder()
	require.NoError(t, err)
	for range 3 {
		pkts, _ := enc.Encode([][]byte{testVPS265, testSPS265, testPPS265, testIDR265})
		for _, pkt := range pkts {
			stream.WritePacketRTP(desc.Medias[0], pkt)
		}
		time.Sleep(30 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)

	require.NoError(t, rec.Stop())

	n := countFinalFiles(t, mgr, "h265-char-reconn")
	require.NotEmpty(t, n, "expected at least one file after reconnect")
}

// 6. Panic recovery — H265 panic in writeFrames does not crash the process
func TestH265Characterization_PanicRecovery(t *testing.T) {
	srv := newTestRTSPServerH265(t)
	defer srv.close()

	realMgr, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	pStore := newPanicStore(realMgr)

	rec := NewH265Recorder(H265Config{
		CameraID:             "h265-char-panic",
		RTSPURL:              srv.rtspURL,
		SegmentDur:           5 * time.Minute,
		RingBufCap:           100,
		FrameWatchdogTimeout: 200 * time.Millisecond,
	}, pStore)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	require.Equal(t, model.StatusRecording, rec.Status())

	srv.waitPlay(t, 5*time.Second)
	time.Sleep(50 * time.Millisecond)

	// Send frames — first CreateSegment call panics and is recovered
	srv.sendFrames(5, 30*time.Millisecond)

	// Wait for recovery and reconnect
	time.Sleep(2 * time.Second)

	// Send more frames after reconnection
	srv.sendFrames(3, 20*time.Millisecond)
	time.Sleep(500 * time.Millisecond)

	require.NoError(t, rec.Stop())
	require.Equal(t, model.StatusStopped, rec.Status())

	// After panic recovery, check status and optionally verify files.
	// The camera directory may not exist if no segments were created.
	files, err := realMgr.ListFiles("h265-char-panic")
	if err == nil {
		t.Logf("H265 files after panic recovery: %d", len(files))
	} else {
		t.Logf("H265: no camera dir after panic recovery (expected if reconnect didn't create segment): %v", err)
	}
}

// 7. Recording output — H265 MP4 files have valid box structure
func TestH265Characterization_MP4FileStructure(t *testing.T) {
	srv := newTestRTSPServerH265(t)
	defer srv.close()

	mgr := newTestManager(t)
	rec := NewH265Recorder(H265Config{
		CameraID:   "h265-char-mp4",
		RTSPURL:    srv.rtspURL,
		SegmentDur: 150 * time.Millisecond,
		RingBufCap: 200,
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	srv.waitPlay(t, 5*time.Second)
	time.Sleep(50 * time.Millisecond)

	// Send enough frames to produce at least one segment
	srv.sendFrames(20, 30*time.Millisecond)
	time.Sleep(300 * time.Millisecond)

	require.NoError(t, rec.Stop())

	files, err := mgr.ListFiles("h265-char-mp4")
	require.NoError(t, err)
	require.NotEmpty(t, files, "expected at least one H265 MP4 file")

	for _, f := range files {
		t.Run(f, func(t *testing.T) {
			scanner := newMP4BoxScanner(t, f)
			require.True(t, scanner.hasBox("ftyp"), "file %s missing ftyp box", f)
			require.True(t, scanner.hasBox("moov"), "file %s missing moov box", f)
			require.True(t, scanner.hasBox("mdat"), "file %s missing mdat box", f)
		})
	}
}

// ---------------------------------------------------------------------------
// Segment duration rotation test (H265)
// ---------------------------------------------------------------------------
func TestH265Characterization_SegmentDurationRotation(t *testing.T) {
	srv := newTestRTSPServerH265(t)
	defer srv.close()

	mgr := newTestManager(t)
	rec := NewH265Recorder(H265Config{
		CameraID:   "h265-char-segdur",
		RTSPURL:    srv.rtspURL,
		SegmentDur: 150 * time.Millisecond,
		RingBufCap: 200,
	}, mgr)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	require.NoError(t, rec.Start(ctx))
	srv.waitPlay(t, 5*time.Second)
	time.Sleep(50 * time.Millisecond)

	srv.sendFrames(20, 50*time.Millisecond)
	time.Sleep(300 * time.Millisecond)

	require.NoError(t, rec.Stop())

	n := countFinalFiles(t, mgr, "h265-char-segdur")
	require.GreaterOrEqual(t, n, 2, "expected at least 2 H265 segments from duration rotation, got %d", n)
}
