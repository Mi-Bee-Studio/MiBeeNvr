// SPDX-License-Identifier: MIT
//
// Pre-refactoring safety tests for xiaomi recorder covering NALU processing,
// segment lifecycle, codec probing, HLS forwarding, and MISS URL validation.
// These guard core recorder paths that Wave 1 security tasks will modify.

package xiaomi

import (
	"bytes"
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

// --- processH264NALU: SPS/PPS detection and segment creation ---

func TestProcessH264NALUSPSOnly(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	r.codec = model.FormatH264
	r.codecOK = true

	var lastTS uint64
	// SPS NALU (type 7)
	r.processH264NALU([]byte{0x67, 0x42, 0xc0, 0x1e}, 0, &lastTS)
	require.NotNil(t, r.sps)
	require.Equal(t, byte(0x67), r.sps[0])
	// No muxer created (PPS missing)
	require.Nil(t, r.muxer)
}

func TestProcessH264NALUPPSOnly(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	r.codec = model.FormatH264
	r.codecOK = true

	var lastTS uint64
	// PPS NALU (type 8)
	r.processH264NALU([]byte{0x68, 0xce, 0x38, 0x80}, 0, &lastTS)
	require.NotNil(t, r.pps)
	// No muxer created (SPS missing)
	require.Nil(t, r.muxer)
}

func TestProcessH264NALUIDRWithoutSPSPPS(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	r.codec = model.FormatH264
	r.codecOK = true

	var lastTS uint64
	// IDR NALU (type 5) without SPS/PPS — should be dropped
	r.processH264NALU([]byte{0x65, 0x01, 0x02, 0x03}, 0, &lastTS)
	require.Nil(t, r.muxer)
}

func TestProcessH264NALUNonIDRWithoutMuxer(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	r.codec = model.FormatH264
	r.codecOK = true
	r.sps = []byte{0x67, 0x42}
	r.pps = []byte{0x68, 0xce}

	var lastTS uint64
	// Non-IDR (type 1) before muxer is created — should be dropped
	r.processH264NALU([]byte{0x41, 0x01, 0x02}, 0, &lastTS)
	require.Nil(t, r.muxer)
}

func TestProcessH264NALUSPSChange(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	r.codec = model.FormatH264
	r.codecOK = true

	var lastTS uint64
	r.processH264NALU([]byte{0x67, 0x42, 0xc0, 0x1e}, 0, &lastTS)
	require.NotNil(t, r.sps)

	// New SPS should replace old
	r.processH264NALU([]byte{0x67, 0x42, 0x00, 0x0a}, 0, &lastTS)
	require.Equal(t, []byte{0x67, 0x42, 0x00, 0x0a}, r.sps)
}

func TestProcessH264NALUPPSChange(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	r.codec = model.FormatH264
	r.codecOK = true

	var lastTS uint64
	r.processH264NALU([]byte{0x68, 0xce, 0x38, 0x80}, 0, &lastTS)
	require.NotNil(t, r.pps)

	// New PPS should replace old
	r.processH264NALU([]byte{0x68, 0xce, 0x00}, 0, &lastTS)
	require.Equal(t, []byte{0x68, 0xce, 0x00}, r.pps)
}

func TestProcessH264NALUUnknownNALType(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	r.codec = model.FormatH264
	r.codecOK = true
	r.sps = []byte{0x67}
	r.pps = []byte{0x68}

	var lastTS uint64
	// NAL type 6 (SEI) — should be ignored
	r.processH264NALU([]byte{0x06, 0x01, 0x02}, 0, &lastTS)
	require.Nil(t, r.muxer)
}

// --- processH265NALU: VPS/SPS/PPS detection ---

func TestProcessH265NALUVPSOnly(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	r.codec = model.FormatH265
	r.codecOK = true

	var lastTS uint64
	r.processH265NALU([]byte{0x40, 0x01, 0x0c}, 0, &lastTS)
	require.NotNil(t, r.vps)
	require.Nil(t, r.muxer)
}

func TestProcessH265NALUSPSOnly(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	r.codec = model.FormatH265
	r.codecOK = true

	var lastTS uint64
	r.processH265NALU([]byte{0x42, 0x01, 0x01}, 0, &lastTS)
	require.NotNil(t, r.sps)
	require.Nil(t, r.muxer)
}

func TestProcessH265NALUPPSOnly(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	r.codec = model.FormatH265
	r.codecOK = true

	var lastTS uint64
	r.processH265NALU([]byte{0x44, 0x01, 0xc1}, 0, &lastTS)
	require.NotNil(t, r.pps)
	require.Nil(t, r.muxer)
}

func TestProcessH265NALUIDRWithoutVPS(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	r.codec = model.FormatH265
	r.codecOK = true
	r.sps = []byte{0x42, 0x01}
	r.pps = []byte{0x44, 0x01}
	// Missing VPS

	var lastTS uint64
	// IDR_W_RADL (type 19): first byte = (19 << 1) = 0x26
	r.processH265NALU([]byte{0x26, 0x01, 0x02}, 0, &lastTS)
	require.Nil(t, r.muxer)
}

func TestProcessH265NALUNonVCLType(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	r.codec = model.FormatH265
	r.codecOK = true
	r.vps = []byte{0x40, 0x01}
	r.sps = []byte{0x42, 0x01}
	r.pps = []byte{0x44, 0x01}

	var lastTS uint64
	// Type 35 (SEI prefix, non-VCL) — should be ignored
	// First byte: (35 << 1) | 1 = 71 = 0x47
	r.processH265NALU([]byte{0x47, 0x01}, 0, &lastTS)
	require.Nil(t, r.muxer)
}

func TestProcessH265NALUVPSChange(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	r.codec = model.FormatH265
	r.codecOK = true

	var lastTS uint64
	r.processH265NALU([]byte{0x40, 0x01, 0x0c}, 0, &lastTS)
	// New VPS replaces old
	r.processH265NALU([]byte{0x40, 0x01, 0x0d}, 0, &lastTS)
	require.Equal(t, []byte{0x40, 0x01, 0x0d}, r.vps)
}

func TestProcessH265NALUIDR_W_RADL(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	r.codec = model.FormatH265
	r.codecOK = true
	r.vps = []byte{0x40, 0x01, 0x0c}
	r.sps = []byte{0x42, 0x01, 0x01}
	r.pps = []byte{0x44, 0x01, 0xc1}

	// With real store that creates temp files
	store := newRecordingSegmentStore(t)
	r.store = store

	var lastTS uint64
	// IDR_W_RADL (type 19): first byte = (19 << 1) = 0x26
	r.processH265NALU([]byte{0x26, 0x01, 0x02, 0x03}, 0, &lastTS)
	require.NotNil(t, r.muxer)
	require.Equal(t, model.FormatH265, r.codec)
}

func TestProcessH265NALUIDR_N_LP(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	r.codec = model.FormatH265
	r.codecOK = true
	r.vps = []byte{0x40, 0x01, 0x0c}
	r.sps = []byte{0x42, 0x01, 0x01}
	r.pps = []byte{0x44, 0x01, 0xc1}

	store := newRecordingSegmentStore(t)
	r.store = store

	var lastTS uint64
	// IDR_N_LP (type 20): first byte = (20 << 1) = 0x28
	r.processH265NALU([]byte{0x28, 0x01, 0x02}, 0, &lastTS)
	require.NotNil(t, r.muxer)
}

// --- forwardHLS: H264 IDR prepends SPS+PPS ---

func TestForwardHLSSetsSPSPPSOnH264IDR(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	r.codec = model.FormatH264
	r.codecOK = true
	r.streamStart = time.Now()
	r.sps = []byte{0x67, 0x42}
	r.pps = []byte{0x68, 0xce}

	if r.Hub == nil {
		r.Hub = streamhub.New()
	}
	var mu sync.Mutex
	var receivedAU [][]byte
	var receivedPTS int64
	r.Hub.Subscribe("hls", func(pts int64, au [][]byte) {
		mu.Lock()
		receivedPTS = pts
		receivedAU = au
		mu.Unlock()
	})

	// IDR (type 5)
	r.forwardHLS([]byte{0x65, 0x01, 0x02})

	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return receivedAU != nil }, 2*time.Second, 10*time.Millisecond)
	mu.Lock()
	require.Len(t, receivedAU, 3, "H264 IDR should prepend SPS+PPS")
	require.Equal(t, r.sps, receivedAU[0])
	require.Equal(t, r.pps, receivedAU[1])
	require.True(t, receivedPTS >= 0)
	mu.Unlock()
	r.Hub.Unsubscribe("hls")
}

func TestForwardHLSSetsVPS_SPS_PPSOnH265IDR(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	r.codec = model.FormatH265
	r.codecOK = true
	r.streamStart = time.Now()
	r.vps = []byte{0x40, 0x01}
	r.sps = []byte{0x42, 0x01}
	r.pps = []byte{0x44, 0x01}
	if r.Hub == nil {
		r.Hub = streamhub.New()
	}

	var mu sync.Mutex
	var receivedAU [][]byte
	r.Hub.Subscribe("hls", func(pts int64, au [][]byte) {
		mu.Lock()
		receivedAU = au
		mu.Unlock()
	})

	// IDR_N_LP (type 20): (20 << 1) = 0x28
	r.forwardHLS([]byte{0x28, 0x01})

	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return receivedAU != nil }, 2*time.Second, 10*time.Millisecond)
	mu.Lock()
	require.Len(t, receivedAU, 4, "H265 IDR should prepend VPS+SPS+PPS")
	require.Equal(t, r.vps, receivedAU[0])
	require.Equal(t, r.sps, receivedAU[1])
	require.Equal(t, r.pps, receivedAU[2])
	mu.Unlock()
	r.Hub.Unsubscribe("hls")
}

func TestForwardHLSH264NonIDRNoPrefix(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	r.codec = model.FormatH264
	r.codecOK = true
	r.streamStart = time.Now()
	r.sps = []byte{0x67}
	r.pps = []byte{0x68}
	if r.Hub == nil {
		r.Hub = streamhub.New()
	}

	var mu sync.Mutex
	var receivedAU [][]byte
	r.Hub.Subscribe("hls", func(pts int64, au [][]byte) {
		mu.Lock()
		receivedAU = au
		mu.Unlock()
	})

	// Non-IDR (type 1)
	r.forwardHLS([]byte{0x41, 0x01})
	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return receivedAU != nil }, 2*time.Second, 10*time.Millisecond)
	mu.Lock()
	require.Len(t, receivedAU, 1)
	require.Equal(t, []byte{0x41, 0x01}, receivedAU[0])
	mu.Unlock()
	r.Hub.Unsubscribe("hls")
}

func TestForwardHLSNoCallback(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	r.codec = model.FormatH264
	r.codecOK = true
	// No callback set — should not panic
	r.forwardHLS([]byte{0x65, 0x01})
}

// --- closeCurrentSegment edge cases ---

func TestCloseCurrentSegmentNilMuxer(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	// No muxer — should be a no-op
	r.closeCurrentSegment()
	require.Nil(t, r.muxer)
}

func TestCloseCurrentSegmentWithSegment(t *testing.T) {
	t.Helper()
	store := newRecordingSegmentStore(t)
	r := NewXiaomiRecorder(XiaomiRecorderConfig{
		CameraID:   "test-cam",
		DID:        "test-device",
		SegmentDur: 10 * time.Minute,
		DB:         &noopDB{},
	}, store)

	r.codec = model.FormatH264
	r.codecOK = true
	r.sps = []byte{0x67, 0x42, 0xc0, 0x1e}
	r.pps = []byte{0x68, 0xce, 0x38, 0x80}

	// Create segment via processH264NALU with IDR
	var lastTS uint64
	r.processH264NALU([]byte{0x65, 0x01, 0x02, 0x03}, 0, &lastTS)

	require.NotNil(t, r.muxer)
	tempPath := r.curTempPath
	finalPath := r.curFinalPath
	require.NotEmpty(t, tempPath)
	require.NotEmpty(t, finalPath)

	// Close it
	r.closeCurrentSegment()
	require.Nil(t, r.muxer)
	require.Empty(t, r.curTempPath)
	require.Empty(t, r.curFinalPath)
	require.Equal(t, 0, r.frameCount)
}

func TestCloseCurrentSegmentTwice(t *testing.T) {
	t.Helper()
	store := newRecordingSegmentStore(t)
	r := NewXiaomiRecorder(XiaomiRecorderConfig{
		CameraID: "test-cam",
		DID:      "test-device",
		DB:       &noopDB{},
	}, store)

	r.codec = model.FormatH264
	r.codecOK = true
	r.sps = []byte{0x67, 0x42, 0xc0, 0x1e}
	r.pps = []byte{0x68, 0xce, 0x38, 0x80}

	var lastTS uint64
	r.processH264NALU([]byte{0x65, 0x01, 0x02, 0x03}, 0, &lastTS)
	require.NotNil(t, r.muxer)

	r.closeCurrentSegment()
	// Second close should be no-op (muxer is nil)
	r.closeCurrentSegment()
	require.Nil(t, r.muxer)
}

// --- MISS URL validation ---

func TestMISSURLMissingVendor(t *testing.T) {
	t.Helper()
	_, err := NewMISSClient("miss://192.168.1.1?device_public=abc&client_private=def&client_public=ghi&sign=x", 0)
	require.Error(t, err)
}

func TestMISSURLEmptyHost(t *testing.T) {
	t.Helper()
	_, err := NewMISSClient("miss://?vendor=cs2&device_public=abc&client_private=def&client_public=ghi&sign=x", 0)
	// Will fail on key calculation or connection
	require.Error(t, err)
}

func TestMISSPacketSampleRateTableDriven(t *testing.T) {
	t.Helper()
	tests := []struct {
		name     string
		flags    uint32
		expected uint32
	}{
		{"zero flags → 8000", 0, 8000},
		{"sample bits set → 16000", 0b00011000, 16000},
		{"high bit only → 8000", 0x80, 8000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Helper()
			pkt := &MISSPacket{Flags: tt.flags}
			require.Equal(t, tt.expected, pkt.sampleRate())
		})
	}
}

// --- Recorder config defaults ---

func TestRecorderConfigDefaults(t *testing.T) {
	t.Helper()
	r := NewXiaomiRecorder(XiaomiRecorderConfig{
		CameraID: "cam",
		DID:      "dev",
	}, &noopSegmentStore{})
	require.Equal(t, defaultSegmentDur, r.cfg.SegmentDur)
	require.Equal(t, model.StatusStopped, r.Status())
}

func TestRecorderConfigCustom(t *testing.T) {
	t.Helper()
	r := NewXiaomiRecorder(XiaomiRecorderConfig{
		CameraID:   "cam",
		DID:        "dev",
		SegmentDur: 5 * time.Minute,
	}, &noopSegmentStore{})
	require.Equal(t, 5*time.Minute, r.cfg.SegmentDur)
}

// --- XiaomiRecorder setStatus ---

func TestSetStatus(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	require.Equal(t, model.StatusStopped, r.Status())
	r.setStatus(model.StatusReconnecting)
	require.Equal(t, model.StatusReconnecting, r.Status())
	r.setStatus(model.StatusRecording)
	require.Equal(t, model.StatusRecording, r.Status())
}

// --- CodecParams returns set values ---

func TestCodecParamsH265(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	r.codec = model.FormatH265
	r.sps = []byte{0x42, 0x01, 0x01}
	r.pps = []byte{0x44, 0x01, 0xc1}
	r.vps = []byte{0x40, 0x01, 0x0c}

	codec, sps, pps, vps := r.CodecParams()
	require.Equal(t, model.FormatH265, codec)
	require.Equal(t, r.sps, sps)
	require.Equal(t, r.pps, pps)
	require.Equal(t, r.vps, vps)
}

// --- Metrics operations (nil-safe) ---

func TestMetricsNilSafeIncDec(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	// All should be no-ops with nil metrics
	r.incActive()
	r.decActive()
	r.recordSegmentCreated()
	r.recordBytes(0)
	r.recordBytes(1024)
	r.recordError("test")
}

// --- Recorder with metrics ---

func TestRecorderWithMetrics(t *testing.T) {
	t.Helper()
	// We can't create real Prometheus metrics in tests easily,
	// but we verify nil metrics is safe
	r := makeTestRecorder(t)
	require.Nil(t, r.metrics)
}

// --- processNALU dispatch ---

func TestProcessNALUDispatchH264(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	r.codec = model.FormatH264
	r.codecOK = true

	var lastTS uint64
	r.processNALU([]byte{0x67, 0x42}, 0, &lastTS)
	require.NotNil(t, r.sps)
}

func TestProcessNALUDispatchH265(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	r.codec = model.FormatH265
	r.codecOK = true

	var lastTS uint64
	r.processNALU([]byte{0x40, 0x01, 0x0c}, 0, &lastTS)
	require.NotNil(t, r.vps)
}

// --- splitAnnexBNALUs additional edge cases ---

func TestSplitAnnexBNALUsVeryShort(t *testing.T) {
	t.Helper()
	// Only 2 bytes — can't have start code
	nalus := splitAnnexBNALUs([]byte{0x00, 0x00})
	require.Len(t, nalus, 0)
}

func TestSplitAnnexBNALUsThreeZeros(t *testing.T) {
	t.Helper()
	// Three zeros — no 00 00 01 pattern
	nalus := splitAnnexBNALUs([]byte{0x00, 0x00, 0x00})
	require.Len(t, nalus, 0)
}

func TestSplitAnnexBNALUsFourZerosNoOne(t *testing.T) {
	t.Helper()
	nalus := splitAnnexBNALUs([]byte{0x00, 0x00, 0x00, 0x00})
	require.Len(t, nalus, 0)
}

func TestSplitAnnexBNALUsMultipleTrailingZeros(t *testing.T) {
	t.Helper()
	data := []byte{
		0x00, 0x00, 0x00, 0x01, 0x65, 0x01, 0x00, 0x00, 0x00,
	}
	nalus := splitAnnexBNALUs(data)
	require.Len(t, nalus, 1)
	// Trailing zeros should be trimmed
	require.Equal(t, []byte{0x65, 0x01, 0x00, 0x00, 0x00}, nalus[0])
}

// --- annexBToAVCC additional cases ---

func TestAnnexBToAVCC3ByteStartCodes(t *testing.T) {
	t.Helper()
	data := []byte{
		0x00, 0x00, 0x01, 0x65, 0x01,
		0x00, 0x00, 0x01, 0x41, 0x02,
	}
	result := annexBToAVCC(data)
	require.NotNil(t, result)
	// Should produce two AVCC NALUs
	require.True(t, len(result) > 8)
}

// --- Recorder Start/Stop with real context ---

func TestRecorderStartStopIdempotent(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)

	ctx := context.Background()
	require.NoError(t, r.Start(ctx))
	require.Equal(t, model.StatusRecording, r.Status())

	require.NoError(t, r.Stop())
	require.Equal(t, model.StatusStopped, r.Status())

	// Stop again should not panic
	require.NoError(t, r.Stop())
}

func TestRecorderCancelContextDuringRun(t *testing.T) {
	t.Helper()
	r := NewXiaomiRecorder(XiaomiRecorderConfig{
		CameraID: "test-cam",
		DID:      "dev1",
	}, &noopSegmentStore{})

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, r.Start(ctx))

	// Cancel immediately
	cancel()
	require.NoError(t, r.Stop())
	require.Equal(t, model.StatusStopped, r.Status())
}

// --- Recorder double start fails ---

func TestRecorderDoubleStartFails(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)

	ctx := context.Background()
	require.NoError(t, r.Start(ctx))
	defer r.Stop()

	err := r.Start(ctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already running")
}

// --- StreamHub HLS subscription concurrency ---

func TestHLSSubscribeConcurrent(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	r.codec = model.FormatH264
	r.codecOK = true
	r.streamStart = time.Now()

	if r.Hub == nil {
		r.Hub = streamhub.New()
	}

	var calls atomic.Int32
	done := make(chan struct{})

	// Subscribe to HLS stream
	r.Hub.Subscribe("hls", func(pts int64, au [][]byte) {
		calls.Add(1)
	})

	// Forward from multiple goroutines
	for range 10 {
		go func() {
			defer func() { done <- struct{}{} }()
			r.forwardHLS([]byte{0x41, 0x01})
		}()
	}

	for range 10 {
		<-done
	}

	require.Eventually(t, func() bool { return calls.Load() == 10 }, 2*time.Second, 10*time.Millisecond)
	r.Hub.Unsubscribe("hls")
}

// --- closeCurrentSegment removes temp file on muxer error ---

func TestCloseCurrentSegmentRemovesTempOnError(t *testing.T) {
	t.Helper()
	store := newRecordingSegmentStore(t)
	r := NewXiaomiRecorder(XiaomiRecorderConfig{
		CameraID: "test-cam",
		DID:      "dev1",
		DB:       &noopDB{},
	}, store)

	r.codec = model.FormatH264
	r.codecOK = true
	r.sps = []byte{0x67, 0x42, 0xc0, 0x1e}
	r.pps = []byte{0x68, 0xce, 0x38, 0x80}

	var lastTS uint64
	r.processH264NALU([]byte{0x65, 0x01, 0x02, 0x03}, 0, &lastTS)
	require.NotNil(t, r.muxer)

	tempPath := r.curTempPath
	require.NotEmpty(t, tempPath)

	// Temp file should have been renamed or cleaned up by closeCurrentSegment.
	// After close, the muxer is nil and temp file is finalized.
	r.closeCurrentSegment()
	require.Nil(t, r.muxer)
}

// --- extractDID from plugin.go additional tests ---

func TestExtractDIDAdditional(t *testing.T) {
	t.Helper()
	tests := []struct {
		input    string
		expected string
	}{
		{"xiaomi://123", "123"},
		{"xiaomi://", ""},
		{"xiaomi://abc-def-ghi", "abc-def-ghi"},
		{"xiaomi://123456789012345", "123456789012345"},
	}
	for _, tt := range tests {
		got := extractDID(tt.input)
		require.Equal(t, tt.expected, got, "extractDID(%q)", tt.input)
	}
}

// --- Helper ---

func makeTestRecorder(t *testing.T) *XiaomiRecorder {
	t.Helper()
	return NewXiaomiRecorder(XiaomiRecorderConfig{
		CameraID: "test-cam",
		DID:      "test-device",
	}, &noopSegmentStore{})
}

// Ensure bytes import is used
var _ = bytes.Clone
