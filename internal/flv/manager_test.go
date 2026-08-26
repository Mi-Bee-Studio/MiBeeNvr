package flv

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// --- Test Helpers ---

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	return NewManager(WithMaxViewers(3), withWriteBufSize(10))
}

// newTestManagerWithHub creates a Manager and a StreamHub for integration testing.
// The hub is passed to RegisterStream per-stream, not set on the Manager.
func newTestManagerWithHub(t *testing.T) (*Manager, *model.StreamHub) {
	t.Helper()
	hub := model.NewStreamHub()
	mgr := NewManager(WithMaxViewers(3), withWriteBufSize(10))
	return mgr, hub
}

// minimalSPS is a minimal H.264 SPS NALU (Baseline profile, 16x16).
var minimalSPS = []byte{0x67, 0x42, 0xc0, 0x0a, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0x88}

// minimalPPS is a minimal H.264 PPS NALU.
var minimalPPS = []byte{0x68, 0xce, 0x38, 0x80}

// minimalVPS is a minimal H.265 VPS NALU.
var minimalVPS = []byte{0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60}

// minimalH265SPS is a minimal H.265 SPS NALU.
var minimalH265SPS = []byte{0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x78, 0x95, 0x98, 0x09}

// minimalH265PPS is a minimal H.265 PPS NALU.
var minimalH265PPS = []byte{0x44, 0x01, 0xc1, 0x73, 0xd1, 0x89}

// IDR NALU with Annex B start code for H.264.
var idrNALU = []byte{0x65, 0x88, 0x80, 0x40}

// Non-IDR (P-frame) NALU.
var nonIDRNALU = []byte{0x41, 0x9a, 0x21, 0x6c, 0x04}

// --- FLV Format Tests ---

func TestFLVHeader(t *testing.T) {
	buf := flvHeader()
	require.Len(t, buf, 9, "FLV header must be 9 bytes")

	// Signature: "FLV"
	require.Equal(t, []byte("FLV"), buf[0:3])
	// Version: 1
	require.Equal(t, byte(0x01), buf[3])
	// Flags: 0x05 = has audio + has video
	require.Equal(t, byte(0x05), buf[4])
	// Header size: 9 (big-endian uint32)
	require.Equal(t, []byte{0x00, 0x00, 0x00, 0x09}, buf[5:9])
}

func TestPreviousTagSize0(t *testing.T) {
	buf := previousTagSize0()
	require.Len(t, buf, 4)
	require.Equal(t, []byte{0x00, 0x00, 0x00, 0x00}, buf)
}

func TestH264SequenceHeader(t *testing.T) {
	tag := h264SequenceHeader(minimalSPS, minimalPPS)
	require.NotNil(t, tag)

	// Parse tag type
	require.Equal(t, byte(0x09), tag[0], "tag type should be video (0x09)")

	// Parse data size (3 bytes big-endian)
	dataSize := int(tag[1])<<16 | int(tag[2])<<8 | int(tag[3])
	require.Greater(t, dataSize, 0)

	// Timestamp should be 0 for sequence header
	ts := int(tag[4])<<16 | int(tag[5])<<8 | int(tag[6])
	require.Equal(t, 0, ts)

	// StreamID should be 0
	streamID := int(tag[7])<<16 | int(tag[8])<<8 | int(tag[9])
	require.Equal(t, 0, streamID)

	// Video tag data: FrameType(4bits) + CodecID(4bits) = 0x17 (keyframe + AVC)
	require.Equal(t, byte(0x17), tag[11])
	// AVC packet type: 0 = sequence header
	require.Equal(t, byte(0x00), tag[12])
	// Composition time offset: 0 (3 bytes)
	require.Equal(t, []byte{0x00, 0x00, 0x00}, tag[13:16])

	// AVCDecoderConfigurationRecord starts at offset 16
	configData := tag[16:]
	require.Greater(t, len(configData), 0)

	// Verify previous tag size at end
	prevSize := int(tag[dataSize+11])<<24 | int(tag[dataSize+11+1])<<16 |
		int(tag[dataSize+11+2])<<8 | int(tag[dataSize+11+3])
	require.Equal(t, dataSize+11, prevSize)
}

func TestH265SequenceHeader(t *testing.T) {
	tag := h265SequenceHeader(minimalVPS, minimalH265SPS, minimalH265PPS)
	require.NotNil(t, tag)

	// Tag type: video
	require.Equal(t, byte(0x09), tag[0])

	dataSize := int(tag[1])<<16 | int(tag[2])<<8 | int(tag[3])
	require.Greater(t, dataSize, 0)

	// HEVC video tag: FrameType(4bits) + CodecID(4bits) = 0x1C (keyframe + HEVC)
	require.Equal(t, byte(0x1C), tag[11])
	// HEVC packet type: 0 = sequence header (VPS/SPS/PPS)
	require.Equal(t, byte(0x00), tag[12])
	// Composition time: 0
	require.Equal(t, []byte{0x00, 0x00, 0x00}, tag[13:16])

	// HEVCDecoderConfigurationRecord starts at offset 16
	configData := tag[16:]
	require.Greater(t, len(configData), 0)

	// Verify config record starts with configurationVersion=1
	require.Equal(t, byte(0x01), configData[0])
}

func TestVideoFrameTag_H264(t *testing.T) {
	nalus := [][]byte{idrNALU}
	pts := int64(90000) // 1 second at 90kHz
	isKeyframe := true

	tag := videoFrameTag(model.FormatH264, nalus, pts, isKeyframe, flvIngestUnknown)
	require.NotNil(t, tag)

	// Tag type: video
	require.Equal(t, byte(0x09), tag[0])

	dataSize := int(tag[1])<<16 | int(tag[2])<<8 | int(tag[3])
	require.Greater(t, dataSize, 0)

	// FrameType + CodecID: 0x17 (keyframe + AVC)
	require.Equal(t, byte(0x17), tag[11])
	// AVC packet type: 1 = NALU
	require.Equal(t, byte(0x01), tag[12])

	// Timestamp in FLV tag (3 bytes) — PTS is 90kHz, FLV timestamp is ms
	ts := int(tag[4])<<16 | int(tag[5])<<8 | int(tag[6])
	require.Equal(t, (90000/90)&0xFFFFFF, ts)

	// Check that NALU data is in AVCC format (4-byte length prefix)
	// After tag header (11 bytes) + video header (5 bytes) = offset 16
	avccPayload := tag[16 : dataSize+11]
	// First 4 bytes are NALU length (big-endian)
	naluLen := int(avccPayload[0])<<24 | int(avccPayload[1])<<16 |
		int(avccPayload[2])<<8 | int(avccPayload[3])
	require.Equal(t, len(idrNALU), naluLen)
	// Following bytes should match the NALU
	require.Equal(t, idrNALU, avccPayload[4:4+naluLen])
}

func TestVideoFrameTag_H265(t *testing.T) {
	nalus := [][]byte{idrNALU}
	pts := int64(45000)
	isKeyframe := true

	tag := videoFrameTag(model.FormatH265, nalus, pts, isKeyframe, flvIngestUnknown)
	require.NotNil(t, tag)

	// FrameType + CodecID: 0x1C (keyframe + HEVC)
	require.Equal(t, byte(0x1C), tag[11])
	// HEVC packet type: 1 = NALU
	require.Equal(t, byte(0x01), tag[12])
}

func TestVideoFrameTag_NonKeyframe(t *testing.T) {
	nalus := [][]byte{nonIDRNALU}
	pts := int64(3000)
	isKeyframe := false

	tag := videoFrameTag(model.FormatH264, nalus, pts, isKeyframe, flvIngestUnknown)

	// FrameType + CodecID: 0x27 (inter-frame + AVC)
	require.Equal(t, byte(0x27), tag[11])
}

// --- Manager Tests ---

func TestRegisterStream_H264(t *testing.T) {
	mgr := newTestManager(t)
	err := mgr.RegisterStream("cam1", model.FormatH264, minimalSPS, minimalPPS, nil, nil)
	require.NoError(t, err)
	require.True(t, mgr.IsActive("cam1"))
}

func TestRegisterStream_H265(t *testing.T) {
	mgr := newTestManager(t)
	err := mgr.RegisterStream("cam1", model.FormatH265, minimalH265SPS, minimalH265PPS, minimalVPS, nil)
	require.NoError(t, err)
	require.True(t, mgr.IsActive("cam1"))
}

func TestRegisterStream_Duplicate(t *testing.T) {
	mgr := newTestManager(t)
	err := mgr.RegisterStream("cam1", model.FormatH264, minimalSPS, minimalPPS, nil, nil)
	require.NoError(t, err)

	err = mgr.RegisterStream("cam1", model.FormatH264, minimalSPS, minimalPPS, nil, nil)
	require.ErrorIs(t, err, ErrStreamExists)
}

func TestUnregisterStream(t *testing.T) {
	mgr := newTestManager(t)
	_ = mgr.RegisterStream("cam1", model.FormatH264, minimalSPS, minimalPPS, nil, nil)
	require.True(t, mgr.IsActive("cam1"))

	mgr.unregisterStream("cam1")
	require.False(t, mgr.IsActive("cam1"))
}

func TestUnregisterStream_NotActive(t *testing.T) {
	mgr := newTestManager(t)
	// Should not panic
	mgr.unregisterStream("nonexistent")
}

// --- Max Viewers Tests ---

func TestMaxViewers_Enforced(t *testing.T) {
	mgr := NewManager(WithMaxViewers(2), withWriteBufSize(10))
	_ = mgr.RegisterStream("cam1", model.FormatH264, minimalSPS, minimalPPS, nil, nil)

	// First 2 viewers should succeed
	for range 2 {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/live/cam1.flv", nil)
		// Consume in background so ServeFLV doesn't block
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = mgr.ServeFLV("cam1", w, r)
		}()
		time.Sleep(50 * time.Millisecond) // let goroutine start
	}

	// Third viewer should get error
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/live/cam1.flv", nil)
	err := mgr.ServeFLV("cam1", w, r)
	require.ErrorIs(t, err, ErrMaxViewers)

	mgr.unregisterStream("cam1")
}

// --- Client Disconnect Detection ---

func TestClientDisconnect_Cleanup(t *testing.T) {
	mgr := newTestManager(t)
	_ = mgr.RegisterStream("cam1", model.FormatH264, minimalSPS, minimalPPS, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/live/cam1.flv", nil).WithContext(ctx)

	viewerDone := make(chan struct{})
	go func() {
		defer close(viewerDone)
		_ = mgr.ServeFLV("cam1", w, r)
	}()

	time.Sleep(50 * time.Millisecond)

	// Cancel context to simulate disconnect
	cancel()
	<-viewerDone

	// Viewer should be cleaned up
	require.Eventually(t, func() bool {
		return mgr.viewerCount("cam1") == 0
	}, 2*time.Second, 50*time.Millisecond)
}

// --- Non-Blocking Write ---

func TestNonBlockingWrite_DropsFrames(t *testing.T) {
	mgr := NewManager(WithMaxViewers(3), withWriteBufSize(2)) // tiny buffer
	_ = mgr.RegisterStream("cam1", model.FormatH264, minimalSPS, minimalPPS, nil, nil)

	// Start a viewer that never reads (blocking writer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &blockingResponseWriter{}
	r := httptest.NewRequest(http.MethodGet, "/live/cam1.flv", nil).WithContext(ctx)

	viewerDone := make(chan struct{})
	go func() {
		defer close(viewerDone)
		_ = mgr.ServeFLV("cam1", w, r)
	}()
	time.Sleep(100 * time.Millisecond)

	// Write many frames — should not block, excess frames dropped
	for i := range 20 {
		start := time.Now()
		mgr.writeH264("cam1", int64(i*3000), [][]byte{idrNALU})
		require.WithinDuration(t, start, time.Now(), 100*time.Millisecond,
			"WriteH264 should not block even with full buffer")
	}

	cancel()
	<-viewerDone
}

func TestWriteH264_InactiveStream(t *testing.T) {
	mgr := newTestManager(t)
	// Should not panic/error on inactive stream
	mgr.writeH264("nonexistent", 1000, [][]byte{idrNALU})
}

func TestWriteH265_InactiveStream(t *testing.T) {
	mgr := newTestManager(t)
	mgr.writeH265("nonexistent", 1000, [][]byte{idrNALU})
}

// --- GOP Cache Tests ---

func TestGOPCache_NewClientGetsCachedKeyframe(t *testing.T) {
	mgr := newTestManager(t)
	_ = mgr.RegisterStream("cam1", model.FormatH264, minimalSPS, minimalPPS, nil, nil)

	// Write IDR (keyframe) + P-frames
	mgr.writeH264("cam1", 0, [][]byte{idrNALU})       // IDR - should be cached
	mgr.writeH264("cam1", 3000, [][]byte{nonIDRNALU}) // P-frame
	mgr.writeH264("cam1", 6000, [][]byte{nonIDRNALU}) // P-frame

	time.Sleep(50 * time.Millisecond) // let GOP cache settle

	// New client connects — should receive cached GOP
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var receivedData []byte
	var mu sync.Mutex
	w := &capturingResponseWriter{
		onWrite: func(p []byte) {
			mu.Lock()
			receivedData = append(receivedData, p...)
			mu.Unlock()
		},
	}
	r := httptest.NewRequest(http.MethodGet, "/live/cam1.flv", nil).WithContext(ctx)

	viewerDone := make(chan struct{})
	go func() {
		defer close(viewerDone)
		_ = mgr.ServeFLV("cam1", w, r)
	}()

	// Wait for data to be written
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(receivedData) > 0
	}, 2*time.Second, 50*time.Millisecond)

	cancel()
	<-viewerDone

	mu.Lock()
	defer mu.Unlock()

	// Should contain FLV header + PreviousTagSize0 + sequence header + IDR
	require.True(t, len(receivedData) > 9+4, "should have received FLV data")

	// Verify FLV header
	require.Equal(t, []byte("FLV"), receivedData[0:3])
}

func TestGOPCache_UpdatedOnNewKeyframe(t *testing.T) {
	mgr := newTestManager(t)
	_ = mgr.RegisterStream("cam1", model.FormatH264, minimalSPS, minimalPPS, nil, nil)

	// Write first keyframe + deltas
	mgr.writeH264("cam1", 0, [][]byte{idrNALU})
	mgr.writeH264("cam1", 3000, [][]byte{nonIDRNALU})

	time.Sleep(50 * time.Millisecond)

	// Write second keyframe — should replace old GOP
	newIDR := []byte{0x65, 0xAA, 0xBB, 0xCC}
	mgr.writeH264("cam1", 6000, [][]byte{newIDR})

	time.Sleep(50 * time.Millisecond)

	// Verify GOP was updated by checking the stream entry
	mgr.mu.RLock()
	entry, ok := mgr.streams["cam1"]
	mgr.mu.RUnlock()
	require.True(t, ok)
	require.NotNil(t, entry.gopCache)
	entry.gopMu.RLock()
	frames := len(entry.gopCache.frames)
	entry.gopMu.RUnlock()
	require.True(t, frames > 0)
}

// --- ServeFLV Tests ---

func TestServeFLV_StreamNotActive(t *testing.T) {
	mgr := newTestManager(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/live/nonexistent.flv", nil)
	err := mgr.ServeFLV("nonexistent", w, r)
	require.ErrorIs(t, err, ErrStreamNotActive)
}

func TestServeFLV_SetsCorrectHeaders(t *testing.T) {
	mgr := newTestManager(t)
	_ = mgr.RegisterStream("cam1", model.FormatH264, minimalSPS, minimalPPS, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/live/cam1.flv", nil).WithContext(ctx)

	// Cancel quickly to end the connection
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_ = mgr.ServeFLV("cam1", w, r)

	// Check response headers
	require.Equal(t, "video/x-flv", w.Header().Get("Content-Type"))
}

func TestServeFLV_WritesFLVHeader(t *testing.T) {
	mgr := newTestManager(t)
	_ = mgr.RegisterStream("cam1", model.FormatH264, minimalSPS, minimalPPS, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())

	var buf bytes.Buffer
	w := &capturingResponseWriter{w: &buf}
	r := httptest.NewRequest(http.MethodGet, "/live/cam1.flv", nil).WithContext(ctx)

	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_ = mgr.ServeFLV("cam1", w, r)

	data := buf.Bytes()
	require.GreaterOrEqual(t, len(data), 13, "should have FLV header + PreviousTagSize0")

	// FLV header
	require.Equal(t, []byte("FLV"), data[0:3])
	require.Equal(t, byte(0x01), data[3])
	require.Equal(t, byte(0x05), data[4])
	// PreviousTagSize0
	require.Equal(t, []byte{0x00, 0x00, 0x00, 0x00}, data[9:13])
}

// --- StreamHub Integration ---

func TestStreamHubIntegration_SubscribeOnFirstViewer(t *testing.T) {
	mgr, hub := newTestManagerWithHub(t)
	_ = mgr.RegisterStream("cam1", model.FormatH264, minimalSPS, minimalPPS, nil, hub)

	// After registration with hub, consumer should be subscribed immediately
	require.Equal(t, 1, hub.ConsumerCount())

	// Start a viewer
	ctx, cancel := context.WithCancel(context.Background())
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/live/cam1.flv", nil).WithContext(ctx)

	viewerDone := make(chan struct{})
	go func() {
		defer close(viewerDone)
		_ = mgr.ServeFLV("cam1", w, r)
	}()

	cancel()
	<-viewerDone

	// After unregister, consumer should be cleaned up
	mgr.unregisterStream("cam1")
	require.Eventually(t, func() bool {
		return hub.ConsumerCount() == 0
	}, 2*time.Second, 50*time.Millisecond)
}

// --- ViewerCount Tests ---

func TestViewerCount(t *testing.T) {
	mgr := newTestManager(t)
	_ = mgr.RegisterStream("cam1", model.FormatH264, minimalSPS, minimalPPS, nil, nil)

	require.Equal(t, 0, mgr.viewerCount("cam1"))

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	w1 := httptest.NewRecorder()
	r1 := httptest.NewRequest(http.MethodGet, "/live/cam1.flv", nil).WithContext(ctx1)

	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		_ = mgr.ServeFLV("cam1", w1, r1)
	}()

	require.Eventually(t, func() bool {
		return mgr.viewerCount("cam1") == 1
	}, 2*time.Second, 50*time.Millisecond)

	cancel1()
	<-done1

	require.Eventually(t, func() bool {
		return mgr.viewerCount("cam1") == 0
	}, 2*time.Second, 50*time.Millisecond)
}

func TestViewerCount_InactiveStream(t *testing.T) {
	mgr := newTestManager(t)
	require.Equal(t, 0, mgr.viewerCount("nonexistent"))
}

// --- Concurrent Tests ---

func TestConcurrentWrites(t *testing.T) {
	mgr := newTestManager(t)
	_ = mgr.RegisterStream("cam1", model.FormatH264, minimalSPS, minimalPPS, nil, nil)

	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 50 {
				mgr.writeH264("cam1", int64(i*3000), [][]byte{idrNALU})
			}
		}()
	}
	wg.Wait()
	// No panic = success
}

// --- StopAll Tests ---

func TestStopAll(t *testing.T) {
	mgr := newTestManager(t)
	_ = mgr.RegisterStream("cam1", model.FormatH264, minimalSPS, minimalPPS, nil, nil)
	_ = mgr.RegisterStream("cam2", model.FormatH264, minimalSPS, minimalPPS, nil, nil)

	require.True(t, mgr.IsActive("cam1"))
	require.True(t, mgr.IsActive("cam2"))

	mgr.stopAll()

	require.False(t, mgr.IsActive("cam1"))
	require.False(t, mgr.IsActive("cam2"))
}

// --- Viewer Cleanup Tests ---

func TestViewerCleanup_FreesSlot(t *testing.T) {
	mgr := NewManager(WithMaxViewers(1), withWriteBufSize(10))
	_ = mgr.RegisterStream("cam1", model.FormatH264, minimalSPS, minimalPPS, nil, nil)

	// Connect one viewer — takes the only slot
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	w1 := httptest.NewRecorder()
	r1 := httptest.NewRequest(http.MethodGet, "/live/cam1.flv", nil).WithContext(ctx1)

	viewerDone1 := make(chan struct{})
	go func() {
		defer close(viewerDone1)
		_ = mgr.ServeFLV("cam1", w1, r1)
	}()

	require.Eventually(t, func() bool {
		return mgr.viewerCount("cam1") == 1
	}, 2*time.Second, 50*time.Millisecond)

	// Second viewer should get ErrMaxViewers
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodGet, "/live/cam1.flv", nil)
	err := mgr.ServeFLV("cam1", w2, r2)
	require.ErrorIs(t, err, ErrMaxViewers)

	// Disconnect the first viewer
	cancel1()
	<-viewerDone1

	require.Eventually(t, func() bool {
		return mgr.viewerCount("cam1") == 0
	}, 2*time.Second, 50*time.Millisecond)

	// Now a new viewer should be able to connect (slot freed)
	ctx3, cancel3 := context.WithCancel(context.Background())
	defer cancel3()
	w3 := httptest.NewRecorder()
	r3 := httptest.NewRequest(http.MethodGet, "/live/cam1.flv", nil).WithContext(ctx3)

	viewerDone3 := make(chan struct{})
	go func() {
		defer close(viewerDone3)
		_ = mgr.ServeFLV("cam1", w3, r3)
	}()

	require.Eventually(t, func() bool {
		return mgr.viewerCount("cam1") == 1
	}, 2*time.Second, 50*time.Millisecond)

	cancel3()
	<-viewerDone3
}

func TestGOPCacheMiss_Metric(t *testing.T) {
	m := metrics.NewMetrics()
	mgr := NewManager(WithMaxViewers(3), withWriteBufSize(10), WithMetrics(m))
	_ = mgr.RegisterStream("cam1", model.FormatH264, minimalSPS, minimalPPS, nil, nil)

	// Connect a viewer — no frames were written, so GOP cache is empty --> miss
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/live/cam1.flv", nil).WithContext(ctx)

	viewerDone := make(chan struct{})
	go func() {
		defer close(viewerDone)
		_ = mgr.ServeFLV("cam1", w, r)
	}()
	time.Sleep(100 * time.Millisecond)

	cancel()
	<-viewerDone

	// Verify the GOP cache miss metric was incremented
	families, err := m.Registry.Gather()
	require.NoError(t, err)
	found := false
	for _, f := range families {
		if f.GetName() == "nvr_flv_gop_cache_misses_total" {
			found = true
			require.Len(t, f.GetMetric(), 1, "expected 1 label combo for cam1")
			require.Equal(t, float64(1), f.GetMetric()[0].GetCounter().GetValue(),
				"expected 1 GOP cache miss for cam1")
			break
		}
	}
	require.True(t, found, "expected nvr_flv_gop_cache_misses_total metric family")
}

// --- Helper types ---

// blockingResponseWriter never reads, causing writes to eventually block.
type blockingResponseWriter struct {
	header http.Header
	code   int
}

func (w *blockingResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *blockingResponseWriter) Write(p []byte) (int, error) {
	// Discard — simulates a slow/blocking client
	return len(p), nil
}

func (w *blockingResponseWriter) WriteHeader(code int) {
	w.code = code
}

func (w *blockingResponseWriter) Flush() {}

// capturingResponseWriter captures all written bytes and optionally calls onWrite.
type capturingResponseWriter struct {
	w       io.Writer
	header  http.Header
	code    int
	onWrite func(p []byte)
}

func (w *capturingResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *capturingResponseWriter) Write(p []byte) (int, error) {
	if w.w != nil {
		n, err := w.w.Write(p)
		if w.onWrite != nil {
			w.onWrite(p[:n])
		}
		return n, err
	}
	if w.onWrite != nil {
		w.onWrite(p)
	}
	return len(p), nil
}

func (w *capturingResponseWriter) WriteHeader(code int) {
	w.code = code
}

func (w *capturingResponseWriter) Flush() {}

// ensure http.ResponseWriter and http.Flusher interfaces
var (
	_ http.ResponseWriter = (*blockingResponseWriter)(nil)
	_ http.Flusher        = (*blockingResponseWriter)(nil)
	_ http.ResponseWriter = (*capturingResponseWriter)(nil)
	_ http.Flusher        = (*capturingResponseWriter)(nil)
)

// ensure io.Writer is satisfied
var _ io.Writer = (*bytes.Buffer)(nil)

// Ingest push paths (RTMP/SRT/WHIP) prepend the cached SPS/PPS to IDR frames
// before hub broadcast — au[0] is a parameter set, never the IDR. writeFrame
// used to check only au[0], marking every ingest keyframe as an inter frame:
// the GOP cache never filled and FLV viewers got a P-frame-only stream that
// no player could start from (fnOS live-push topology, 2026-08-19).
func TestWriteFrame_IngestStyleAU_KeyframeDetected(t *testing.T) {
	mgr := newTestManager(t)
	require.NoError(t, mgr.RegisterStream("cam1", model.FormatH264, minimalSPS, minimalPPS, nil, nil))

	mgr.mu.RLock()
	entry := mgr.streams["cam1"]
	mgr.mu.RUnlock()
	require.NotNil(t, entry)

	// [SPS, PPS, IDR] — ingest-style AU with prepended param sets.
	idr := append([]byte{0x65}, make([]byte, 16)...)
	au := [][]byte{append([]byte{0x67}, minimalSPS[1:]...), append([]byte{0x68}, minimalPPS[1:]...), idr}
	mgr.writeFrame("cam1", 9000, au)

	// writeLoop consumes frameCh and updates the GOP cache on keyframes —
	// before the fix this AU was flagged inter and the cache stayed empty.
	require.Eventually(t, func() bool {
		entry.gopMu.RLock()
		defer entry.gopMu.RUnlock()
		return len(entry.gopCache.frames) == 1 && entry.gopCache.frames[0].isKeyframe
	}, 2*time.Second, 20*time.Millisecond, "ingest-style [SPS,PPS,IDR] AU must land in the GOP cache as a keyframe")
}

// --- ServeFLV stall regression (#539) ---
//
// stalledWriter blocks inside Write for the test duration, emulating a client
// whose kernel socket buffer is full (probe read a few KB then half-closed).
type stalledWriter struct {
	header  http.Header
	release chan struct{}
}

func newStalledWriter() *stalledWriter {
	return &stalledWriter{header: make(http.Header), release: make(chan struct{})}
}

func (w *stalledWriter) Header() http.Header { return w.header }
func (w *stalledWriter) Write(p []byte) (int, error) {
	<-w.release
	return len(p), nil
}
func (w *stalledWriter) WriteHeader(int) {}

// A stalled client must not wedge the camera's FLV pipeline: with the old
// lock-held writes, the stalled ServeFLV blocked while holding viewerMu, so
// ViewerCount (/api/streams) and every subsequent ServeFLV queued behind it.
func TestServeFLV_StalledClientDoesNotWedgePipeline(t *testing.T) {
	mgr, hub := newTestManagerWithHub(t)
	require.NoError(t, mgr.RegisterStream("cam1", model.FormatH264, minimalSPS, minimalPPS, nil, hub))
	// Prime the GOP cache so ServeFLV has a replay to (stall on) write.
	hub.Broadcast(1000, [][]byte{minimalSPS, minimalPPS, idrNALU}, true)
	require.Eventually(t, func() bool {
		mgr.mu.RLock()
		defer mgr.mu.RUnlock()
		e, ok := mgr.streams["cam1"]
		if !ok {
			return false
		}
		e.gopMu.RLock()
		defer e.gopMu.RUnlock()
		return len(e.gopCache.frames) > 0
	}, 3*time.Second, 20*time.Millisecond)

	sw := newStalledWriter()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/cameras/cam1/stream.flv", nil).WithContext(ctx)
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- mgr.ServeFLV("cam1", sw, req) }()

	// The viewer must be registered promptly (registration no longer waits
	// on any client write).
	require.Eventually(t, func() bool { return mgr.ViewerCount("cam1") == 1 },
		2*time.Second, 20*time.Millisecond)

	// /api/streams' ViewerCount must answer WHILE the stalled serve is stuck
	// in its (lock-free) write — the old code deadlocked here.
	counted := make(chan int, 1)
	go func() { counted <- mgr.ViewerCount("cam1") }()
	select {
	case n := <-counted:
		require.Equal(t, 1, n)
	case <-time.After(2 * time.Second):
		t.Fatal("ViewerCount wedged behind a stalled ServeFLV client")
	}

	// A second serve must reach its own write path (not queue on the lock).
	sw2 := newStalledWriter()
	done2 := make(chan error, 1)
	go func() { done2 <- mgr.ServeFLV("cam1", sw2, req) }()
	require.Eventually(t, func() bool { return mgr.ViewerCount("cam1") == 2 },
		2*time.Second, 20*time.Millisecond)

	// Release both stalled writers, then cancel the request contexts — the
	// serves are streaming loops, they end with their ctx.
	close(sw.release)
	close(sw2.release)
	cancel()
	require.Eventually(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, 3*time.Second, 50*time.Millisecond, "first serve did not finish after release")
	require.Eventually(t, func() bool { return mgr.ViewerCount("cam1") == 0 },
		3*time.Second, 50*time.Millisecond, "viewers not cleaned up after disconnect")
}
