// SPDX-License-Identifier: MIT

package xiaomi

import (
	"context"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	gen "github.com/Mi-Bee-Studio/MiBeeNvr/plugin/proto/gen"
)

// --- FrameSender mock ---

// mockFrameSender records all frames sent via SendFrame.
type mockFrameSender struct {
	mu     sync.Mutex
	frames []*gen.Frame
	err    error // if set, SendFrame returns this error
}

func (m *mockFrameSender) SendFrame(_ context.Context, frame *gen.Frame) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.frames = append(m.frames, frame)
	return nil
}

func (m *mockFrameSender) getFrames() []*gen.Frame {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*gen.Frame, len(m.frames))
	copy(out, m.frames)
	return out
}

func (m *mockFrameSender) lastFrame() *gen.Frame {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.frames) == 0 {
		return nil
	}
	return m.frames[len(m.frames)-1]
}

// --- H264 NALU → Frame conversion tests ---

func TestStreamRecorderH264IDR(t *testing.T) {
	t.Helper()
	sender := &mockFrameSender{}
	r := newTestStreamRecorder(sender)
	r.codec = gen.Codec_CODEC_H264
	r.codecOK = true
	r.streamStart = time.Now()

	// H264 IDR: NAL type 5
	nalu := []byte{0x65, 0x01, 0x02, 0x03}
	r.processH264NALU(context.Background(), nalu)

	frames := sender.getFrames()
	require.Len(t, frames, 1)
	require.Equal(t, nalu, frames[0].Data)
	require.True(t, frames[0].IsIdr)
	require.Equal(t, gen.Codec_CODEC_H264, frames[0].Codec)
	require.False(t, frames[0].IsCodecInfo)
	require.True(t, frames[0].PtsNs > 0)
}

func TestStreamRecorderH264PFrame(t *testing.T) {
	t.Helper()
	sender := &mockFrameSender{}
	r := newTestStreamRecorder(sender)
	r.codec = gen.Codec_CODEC_H264
	r.codecOK = true
	r.streamStart = time.Now()

	// H264 non-IDR: NAL type 1
	nalu := []byte{0x41, 0x01, 0x02}
	r.processH264NALU(context.Background(), nalu)

	frames := sender.getFrames()
	require.Len(t, frames, 1)
	require.False(t, frames[0].IsIdr)
	require.Equal(t, gen.Codec_CODEC_H264, frames[0].Codec)
}

func TestStreamRecorderH264SPS(t *testing.T) {
	t.Helper()
	sender := &mockFrameSender{}
	r := newTestStreamRecorder(sender)
	r.codec = gen.Codec_CODEC_H264
	r.codecOK = true
	r.streamStart = time.Now()

	// H264 SPS: NAL type 7
	sps := []byte{0x67, 0x42, 0xc0, 0x1e}
	r.processH264NALU(context.Background(), sps)

	require.NotNil(t, r.sps)
	require.Equal(t, sps, r.sps)

	frames := sender.getFrames()
	require.Len(t, frames, 1)
	require.True(t, frames[0].IsCodecInfo)
	require.False(t, frames[0].IsIdr)
	require.Equal(t, gen.Codec_CODEC_H264, frames[0].Codec)
	require.Equal(t, hex.EncodeToString(sps), frames[0].Extra["sps_hex"])
}

func TestStreamRecorderH264PPS(t *testing.T) {
	t.Helper()
	sender := &mockFrameSender{}
	r := newTestStreamRecorder(sender)
	r.codec = gen.Codec_CODEC_H264
	r.codecOK = true
	r.streamStart = time.Now()

	// H264 PPS: NAL type 8
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	r.processH264NALU(context.Background(), pps)

	require.NotNil(t, r.pps)
	require.Equal(t, pps, r.pps)

	frames := sender.getFrames()
	require.Len(t, frames, 1)
	require.True(t, frames[0].IsCodecInfo)
	require.Equal(t, hex.EncodeToString(pps), frames[0].Extra["pps_hex"])
}

func TestStreamRecorderH264SkipsUnknownNALTypes(t *testing.T) {
	t.Helper()
	sender := &mockFrameSender{}
	r := newTestStreamRecorder(sender)
	r.codec = gen.Codec_CODEC_H264
	r.codecOK = true
	r.streamStart = time.Now()

	// NAL type 6 (SEI) — should be silently skipped
	r.processH264NALU(context.Background(), []byte{0x06, 0x01})
	// NAL type 9 (AUD) — should be silently skipped
	r.processH264NALU(context.Background(), []byte{0x09, 0x10})

	require.Len(t, sender.getFrames(), 0)
}

// --- H265 NALU → Frame conversion tests ---

func TestStreamRecorderH265IDR(t *testing.T) {
	t.Helper()
	sender := &mockFrameSender{}
	r := newTestStreamRecorder(sender)
	r.codec = gen.Codec_CODEC_H265
	r.codecOK = true
	r.streamStart = time.Now()

	// H265 IDR_W_RADL: NAL type 19 — byte: (19 << 1) = 0x26
	nalu := []byte{0x26, 0x01, 0x56, 0xAB}
	r.processH265NALU(context.Background(), nalu)

	frames := sender.getFrames()
	require.Len(t, frames, 1)
	require.True(t, frames[0].IsIdr)
	require.Equal(t, gen.Codec_CODEC_H265, frames[0].Codec)
	require.False(t, frames[0].IsCodecInfo)
}

func TestStreamRecorderH265IDR_N_LP(t *testing.T) {
	t.Helper()
	sender := &mockFrameSender{}
	r := newTestStreamRecorder(sender)
	r.codec = gen.Codec_CODEC_H265
	r.codecOK = true
	r.streamStart = time.Now()

	// H265 IDR_N_LP: NAL type 20 — byte: (20 << 1) = 0x28
	nalu := []byte{0x28, 0x01, 0x56, 0xAB}
	r.processH265NALU(context.Background(), nalu)

	frames := sender.getFrames()
	require.Len(t, frames, 1)
	require.True(t, frames[0].IsIdr)
}

func TestStreamRecorderH265PFrame(t *testing.T) {
	t.Helper()
	sender := &mockFrameSender{}
	r := newTestStreamRecorder(sender)
	r.codec = gen.Codec_CODEC_H265
	r.codecOK = true
	r.streamStart = time.Now()

	// H265 non-IDR: NAL type 1 — byte: (1 << 1) = 0x02
	nalu := []byte{0x02, 0x01, 0x56}
	r.processH265NALU(context.Background(), nalu)

	frames := sender.getFrames()
	require.Len(t, frames, 1)
	require.False(t, frames[0].IsIdr)
	require.Equal(t, gen.Codec_CODEC_H265, frames[0].Codec)
}

func TestStreamRecorderH265VPS(t *testing.T) {
	t.Helper()
	sender := &mockFrameSender{}
	r := newTestStreamRecorder(sender)
	r.codec = gen.Codec_CODEC_H265
	r.codecOK = true
	r.streamStart = time.Now()

	// H265 VPS: NAL type 32 — byte: (32 << 1) = 0x40
	vps := []byte{0x40, 0x01, 0x0c}
	r.processH265NALU(context.Background(), vps)

	require.NotNil(t, r.vps)
	require.Equal(t, vps, r.vps)

	frames := sender.getFrames()
	require.Len(t, frames, 1)
	require.True(t, frames[0].IsCodecInfo)
	require.Equal(t, hex.EncodeToString(vps), frames[0].Extra["vps_hex"])
}

func TestStreamRecorderH265SPS(t *testing.T) {
	t.Helper()
	sender := &mockFrameSender{}
	r := newTestStreamRecorder(sender)
	r.codec = gen.Codec_CODEC_H265
	r.codecOK = true
	r.streamStart = time.Now()

	// H265 SPS: NAL type 33 — byte: (33 << 1) = 0x42
	sps := []byte{0x42, 0x01, 0x01}
	r.processH265NALU(context.Background(), sps)

	require.NotNil(t, r.sps)
	frames := sender.getFrames()
	require.Len(t, frames, 1)
	require.Equal(t, hex.EncodeToString(sps), frames[0].Extra["sps_hex"])
}

func TestStreamRecorderH265PPS(t *testing.T) {
	t.Helper()
	sender := &mockFrameSender{}
	r := newTestStreamRecorder(sender)
	r.codec = gen.Codec_CODEC_H265
	r.codecOK = true
	r.streamStart = time.Now()

	// H265 PPS: NAL type 34 — byte: (34 << 1) = 0x44
	pps := []byte{0x44, 0x01, 0xc1}
	r.processH265NALU(context.Background(), pps)

	require.NotNil(t, r.pps)
	frames := sender.getFrames()
	require.Len(t, frames, 1)
	require.Equal(t, hex.EncodeToString(pps), frames[0].Extra["pps_hex"])
}

func TestStreamRecorderH265SkipsNonVCL(t *testing.T) {
	t.Helper()
	sender := &mockFrameSender{}
	r := newTestStreamRecorder(sender)
	r.codec = gen.Codec_CODEC_H265
	r.codecOK = true
	r.streamStart = time.Now()

	// NAL type 39 (prefix SEI) — non-VCL, should be skipped
	r.processH265NALU(context.Background(), []byte{0x4E, 0x01})

	require.Len(t, sender.getFrames(), 0)
}

// --- PTS calculation tests ---

func TestStreamRecorderPTSCalculation(t *testing.T) {
	t.Helper()
	sender := &mockFrameSender{}
	r := newTestStreamRecorder(sender)
	r.codec = gen.Codec_CODEC_H264
	r.codecOK = true

	// Before streamStart is set, PTS should be 0
	require.Equal(t, uint64(0), r.ptsNanoseconds())

	// Set streamStart to now
	r.streamStart = time.Now()
	time.Sleep(2 * time.Millisecond)

	pts := r.ptsNanoseconds()
	require.True(t, pts > 0, "PTS should be > 0 after stream start")
	// Should be at least 2ms = 2_000_000 ns
	require.True(t, pts >= 2_000_000, "PTS should be at least 2ms, got %d ns", pts)
}

func TestStreamRecorderPTSMonotonicallyIncreasing(t *testing.T) {
	t.Helper()
	sender := &mockFrameSender{}
	r := newTestStreamRecorder(sender)
	r.codec = gen.Codec_CODEC_H264
	r.codecOK = true
	r.streamStart = time.Now()

	var lastPts uint64
	for i := 0; i < 5; i++ {
		nalu := []byte{0x41, byte(i)} // P-frame
		r.processH264NALU(context.Background(), nalu)
		f := sender.lastFrame()
		require.NotNil(t, f)
		require.True(t, f.PtsNs >= lastPts, "PTS should be monotonically increasing: %d >= %d", f.PtsNs, lastPts)
		lastPts = f.PtsNs
	}
}

// --- is_idr flag tests ---

func TestStreamRecorderIDRFlagCorrectness(t *testing.T) {
	t.Helper()
	sender := &mockFrameSender{}
	r := newTestStreamRecorder(sender)
	r.codec = gen.Codec_CODEC_H264
	r.codecOK = true
	r.streamStart = time.Now()

	// IDR (type 5)
	r.processH264NALU(context.Background(), []byte{0x65, 0x01})
	require.True(t, sender.lastFrame().IsIdr)

	// P-frame (type 1)
	r.processH264NALU(context.Background(), []byte{0x41, 0x01})
	require.False(t, sender.lastFrame().IsIdr)

	// Another IDR
	r.processH264NALU(context.Background(), []byte{0x65, 0x02})
	require.True(t, sender.lastFrame().IsIdr)
}

func TestStreamRecorderH265IDRFlagCorrectness(t *testing.T) {
	t.Helper()
	sender := &mockFrameSender{}
	r := newTestStreamRecorder(sender)
	r.codec = gen.Codec_CODEC_H265
	r.codecOK = true
	r.streamStart = time.Now()

	// IDR_W_RADL (type 19): byte = 19<<1 = 0x26
	r.processH265NALU(context.Background(), []byte{0x26, 0x01})
	require.True(t, sender.lastFrame().IsIdr)

	// IDR_N_LP (type 20): byte = 20<<1 = 0x28
	r.processH265NALU(context.Background(), []byte{0x28, 0x01})
	require.True(t, sender.lastFrame().IsIdr)

	// Non-IDR VCL (type 1): byte = 1<<1 = 0x02
	r.processH265NALU(context.Background(), []byte{0x02, 0x01})
	require.False(t, sender.lastFrame().IsIdr)
}

// --- is_codec_info flag tests ---

func TestStreamRecorderCodecInfoFlagCorrectness(t *testing.T) {
	t.Helper()
	sender := &mockFrameSender{}
	r := newTestStreamRecorder(sender)
	r.codec = gen.Codec_CODEC_H264
	r.codecOK = true
	r.streamStart = time.Now()

	// SPS (type 7) — codec info
	r.processH264NALU(context.Background(), []byte{0x67, 0x42})
	require.True(t, sender.lastFrame().IsCodecInfo)
	require.False(t, sender.lastFrame().IsIdr)

	// PPS (type 8) — codec info
	r.processH264NALU(context.Background(), []byte{0x68, 0xCE})
	require.True(t, sender.lastFrame().IsCodecInfo)

	// IDR (type 5) — NOT codec info
	r.processH264NALU(context.Background(), []byte{0x65, 0x01})
	require.False(t, sender.lastFrame().IsCodecInfo)

	// P-frame (type 1) — NOT codec info
	r.processH264NALU(context.Background(), []byte{0x41, 0x01})
	require.False(t, sender.lastFrame().IsCodecInfo)
}

func TestStreamRecorderH265CodecInfoFlagCorrectness(t *testing.T) {
	t.Helper()
	sender := &mockFrameSender{}
	r := newTestStreamRecorder(sender)
	r.codec = gen.Codec_CODEC_H265
	r.codecOK = true
	r.streamStart = time.Now()

	// VPS (type 32) — codec info
	r.processH265NALU(context.Background(), []byte{0x40, 0x01})
	require.True(t, sender.lastFrame().IsCodecInfo)

	// SPS (type 33) — codec info
	r.processH265NALU(context.Background(), []byte{0x42, 0x01})
	require.True(t, sender.lastFrame().IsCodecInfo)

	// PPS (type 34) — codec info
	r.processH265NALU(context.Background(), []byte{0x44, 0x01})
	require.True(t, sender.lastFrame().IsCodecInfo)

	// IDR (type 19) — NOT codec info
	r.processH265NALU(context.Background(), []byte{0x26, 0x01})
	require.False(t, sender.lastFrame().IsCodecInfo)
}

// --- Full frame sequence test ---

func TestStreamRecorderH264FullSequence(t *testing.T) {
	t.Helper()
	sender := &mockFrameSender{}
	r := newTestStreamRecorder(sender)
	r.codec = gen.Codec_CODEC_H264
	r.codecOK = true
	r.streamStart = time.Now()

	sps := []byte{0x67, 0x42, 0xc0, 0x1e}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	idr := []byte{0x65, 0x01, 0x02, 0x03}
	pframe := []byte{0x41, 0x04, 0x05}

	// SPS → PPS → IDR → P → IDR
	r.processH264NALU(context.Background(), sps)
	r.processH264NALU(context.Background(), pps)
	r.processH264NALU(context.Background(), idr)
	r.processH264NALU(context.Background(), pframe)
	r.processH264NALU(context.Background(), idr)

	frames := sender.getFrames()
	require.Len(t, frames, 5)

	// SPS: codec info, not IDR
	require.True(t, frames[0].IsCodecInfo)
	require.False(t, frames[0].IsIdr)
	require.Equal(t, gen.Codec_CODEC_H264, frames[0].Codec)
	require.Equal(t, hex.EncodeToString(sps), frames[0].Extra["sps_hex"])

	// PPS: codec info, not IDR
	require.True(t, frames[1].IsCodecInfo)
	require.False(t, frames[1].IsIdr)
	require.Equal(t, hex.EncodeToString(pps), frames[1].Extra["pps_hex"])

	// IDR: not codec info, IS IDR
	require.False(t, frames[2].IsCodecInfo)
	require.True(t, frames[2].IsIdr)
	require.Equal(t, idr, frames[2].Data)

	// P-frame: not codec info, not IDR
	require.False(t, frames[3].IsCodecInfo)
	require.False(t, frames[3].IsIdr)
	require.Equal(t, pframe, frames[3].Data)

	// Second IDR: not codec info, IS IDR
	require.False(t, frames[4].IsCodecInfo)
	require.True(t, frames[4].IsIdr)
	require.Equal(t, idr, frames[4].Data)

	// Verify PTS is monotonically increasing
	for i := 1; i < len(frames); i++ {
		require.True(t, frames[i].PtsNs >= frames[i-1].PtsNs,
			"frame %d PTS (%d) should be >= frame %d PTS (%d)", i, frames[i].PtsNs, i-1, frames[i-1].PtsNs)
	}
}

// --- Reconnection logic test ---

func TestStreamRecorderReconnection(t *testing.T) {
	t.Helper()
	sender := &mockFrameSender{}
	r := newTestStreamRecorder(sender)

	// Simulate failed connections by calling connectAndRecord with an invalid URL.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// The recorder should attempt, fail, backoff, and retry until context expires.
	err := r.connectAndRecord(ctx, "miss://invalid?vendor=unknown")
	require.Error(t, err) // Should eventually fail

	// Status should reflect the error state set in run() after connection failure.
	// (connectAndRecord itself doesn't set error status; run() does after this returns)
}

// --- Lifecycle tests ---

func TestStreamRecorderInitialStatus(t *testing.T) {
	t.Helper()
	sender := &mockFrameSender{}
	r := newTestStreamRecorder(sender)
	require.Equal(t, gen.RecorderState_RECORDER_STATE_STOPPED, r.Status())
}

func TestStreamRecorderDoubleStart(t *testing.T) {
	t.Helper()
	sender := &mockFrameSender{}
	r := newTestStreamRecorder(sender)

	err := r.Start(context.Background())
	require.NoError(t, err)
	defer r.Stop()

	err = r.Start(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "already running")
}

func TestStreamRecorderStopWithoutStart(t *testing.T) {
	t.Helper()
	sender := &mockFrameSender{}
	r := newTestStreamRecorder(sender)
	err := r.Stop()
	require.NoError(t, err)
}

func TestStreamRecorderContextCancel(t *testing.T) {
	t.Helper()
	sender := &mockFrameSender{}
	r := newTestStreamRecorder(sender)
	r.cfg.MaxBackoff = 50 * time.Millisecond
	r.cfg.InitBackoff = 50 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	err := r.Start(ctx)
	require.NoError(t, err)

	// Cancel after a short delay to let the goroutine start.
	time.Sleep(50 * time.Millisecond)
	cancel()

	err = r.Stop()
	require.NoError(t, err)
	require.Equal(t, gen.RecorderState_RECORDER_STATE_STOPPED, r.Status())
}

// --- nextBackoff tests ---

func TestNextBackoff(t *testing.T) {
	t.Helper()
	b1 := nextBackoff(1*time.Second, 60*time.Second)
	require.True(t, b1 >= 2*time.Second, "should be at least 2s, got %v", b1)
	require.True(t, b1 <= 60*time.Second, "should be at most 60s, got %v", b1)

	// Should cap at max
	b2 := nextBackoff(100*time.Second, 60*time.Second)
	require.Equal(t, 60*time.Second, b2)
}

// --- Empty NALU test ---

func TestStreamRecorderEmptyNALU(t *testing.T) {
	t.Helper()
	sender := &mockFrameSender{}
	r := newTestStreamRecorder(sender)
	r.codec = gen.Codec_CODEC_H264
	r.codecOK = true

	// Empty NALU should be silently ignored
	r.processNALU(context.Background(), []byte{})
	require.Len(t, sender.getFrames(), 0)

	// Nil NALU should be silently ignored
	r.processNALU(context.Background(), nil)
	require.Len(t, sender.getFrames(), 0)
}

// --- SendFrame error test ---

func TestStreamRecorderSendFrameError(t *testing.T) {
	t.Helper()
	sender := &mockFrameSender{err: fmt.Errorf("stream closed")}
	r := newTestStreamRecorder(sender)
	r.codec = gen.Codec_CODEC_H264
	r.codecOK = true
	r.streamStart = time.Now()

	// Should not panic on send error
	r.processH264NALU(context.Background(), []byte{0x65, 0x01})

	// Frame should not be recorded due to error
	require.Equal(t, int64(0), r.frameCount)
	require.Equal(t, int64(0), r.bytesTotal)
}

// --- Counter tracking test ---

func TestStreamRecorderCounterTracking(t *testing.T) {
	t.Helper()
	sender := &mockFrameSender{}
	r := newTestStreamRecorder(sender)
	r.codec = gen.Codec_CODEC_H264
	r.codecOK = true
	r.streamStart = time.Now()

	// Send 3 frames
	r.processH264NALU(context.Background(), []byte{0x65, 0x01, 0x02}) // IDR: 3 bytes
	r.processH264NALU(context.Background(), []byte{0x41, 0x03, 0x04}) // P: 3 bytes
	r.processH264NALU(context.Background(), []byte{0x65, 0x05})        // IDR: 2 bytes

	require.Equal(t, int64(3), r.frameCount)
	require.Equal(t, int64(8), r.bytesTotal) // 3 + 3 + 2
}

// --- Concurrent access test ---

func TestStreamRecorderConcurrentAccess(t *testing.T) {
	t.Helper()
	sender := &mockFrameSender{}
	r := newTestStreamRecorder(sender)
	r.codec = gen.Codec_CODEC_H264
	r.codecOK = true
	r.streamStart = time.Now()

	var wg sync.WaitGroup
	var sentCount atomic.Int64

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.processH264NALU(context.Background(), []byte{0x41, 0x01})
			sentCount.Add(1)
		}()
	}
	wg.Wait()

	require.Equal(t, int64(50), sentCount.Load())
	require.Equal(t, int64(50), r.frameCount)
}

// --- processNALU dispatch test ---

func TestStreamRecorderProcessNALUDispatch(t *testing.T) {
	t.Helper()
	sender := &mockFrameSender{}
	r := newTestStreamRecorder(sender)
	r.streamStart = time.Now()

	// H264 dispatch
	r.codec = gen.Codec_CODEC_H264
	r.codecOK = true
	r.processNALU(context.Background(), []byte{0x65, 0x01})
	require.Equal(t, gen.Codec_CODEC_H264, sender.lastFrame().Codec)

	// H265 dispatch
	r.codec = gen.Codec_CODEC_H265
	r.processNALU(context.Background(), []byte{0x26, 0x01})
	require.Equal(t, gen.Codec_CODEC_H265, sender.lastFrame().Codec)
}

// --- Helper ---

func newTestStreamRecorder(sender FrameSender) *StreamRecorder {
	return NewStreamRecorder(StreamRecorderConfig{
		CameraID:    "test-cam",
		DID:         "test-device",
		MaxBackoff:  100 * time.Millisecond,
		InitBackoff: 100 * time.Millisecond,
	}, sender)
}
