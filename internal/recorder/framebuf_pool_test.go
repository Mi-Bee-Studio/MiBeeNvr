package recorder

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestFrameBufPoolSemantics covers the pool's size handling. Pool identity
// (same object returned after put) is a sync.Pool implementation detail and
// deliberately NOT asserted.
func TestFrameBufPoolSemantics(t *testing.T) {
	p := newFrameBufPool()

	a := p.get(8)
	require.Len(t, a.b, 8)
	copy(a.b, []byte{1, 2, 3, 4, 5, 6, 7, 8})
	p.put(a)

	b := p.get(4)
	require.Len(t, b.b, 4)
	b2 := p.get(16)
	require.Len(t, b2.b, 16)
	// Outstanding buffers never alias each other's memory.
	require.NotSame(t, b, b2)

	// Oversized NALU replaces the pooled buffer instead of failing.
	big := p.get(1 << 20)
	require.Len(t, big.b, 1<<20)

	p.put(b)
	p.put(b2)
	p.put(big)
}

// TestPooledFrameBuffers_ParamSetsSurviveRecycle drives writeFrames with REAL
// pooled framing buffers (buf != nil, exactly like the RTP callbacks) and
// verifies the copy contract the pool relies on (#875 H4):
//
//   - setCodecParams must deep-copy the NALU — the pooled buffer is recycled
//     one iteration later, so an aliased snapshot would read recycled bytes;
//   - the drain loop must not recycle a buffer before its iteration finished.
//
// Alternating distinct SPS/PPS values through the pool means each snapshot
// comparison runs against buffers that have been recycled and refilled
// several times over — both failure modes surface as a mismatch.
func TestPooledFrameBuffers_ParamSetsSurviveRecycle(t *testing.T) {
	mgr := newTestManager(t)
	rec := NewH264Recorder(H264Config{
		CameraID:   "h264-pool-lifecycle",
		RTSPURL:    "rtsp://ignored",
		SegmentDur: time.Second,
		RingBufCap: 64,
	}, mgr)
	off := false
	rec.cfg.RecordEnabled = &off // live-only: full NALU parse path, no muxer
	b := rec.baseRecorder
	// Capacity 1 forces producer/drain pacing: the drain goroutine recycles a
	// buffer before the next get, so the pool actually cycles objects (with a
	// deep buffer the producer would run ahead on fresh New() buffers and the
	// recycling path would never be exercised).
	b.frameCh = make(chan framePacket, 1)
	done := make(chan struct{})
	go b.writeFrames(done)
	defer func() {
		close(b.frameCh)
		<-done
	}()

	// send mirrors the RTP callback's framing (pooled buffer, 4-byte start
	// code + NALU) but blocks on a full channel: the test's job is pacing the
	// drain goroutine so buffers actually cycle through the pool.
	send := func(nal []byte) {
		buf := b.frameBufPool.get(4 + len(nal))
		buf.b[0], buf.b[1], buf.b[2], buf.b[3] = 0, 0, 0, 1
		copy(buf.b[4:], nal)
		b.frameCh <- framePacket{data: buf.b, buf: buf, at: time.Now()}
	}

	spsOf := func(i byte) []byte { return append(append([]byte{}, testSPS...), i) }
	ppsOf := func(i byte) []byte { return append(append([]byte{}, testPPS...), i) }

	for i := byte(1); i <= 20; i++ {
		send(spsOf(i))
		send(ppsOf(i))
		require.Eventually(t, func() bool {
			sps, pps, _ := b.codecSnapshot()
			return sps != nil && pps != nil &&
				bytes.Equal(sps, spsOf(i)) && bytes.Equal(pps, ppsOf(i))
		}, 2*time.Second, 5*time.Millisecond,
			"cycle %d: codec snapshot must survive pooled-buffer recycling", i)
	}
}

// TestHandleParamSetCopiesInput pins the copy contract synchronously: the
// pooled framing buffer is recycled (same memory, new content) after the drain
// iteration, so handleParamSet/setCodecParams must not alias the input NALU.
func TestHandleParamSetCopiesInput(t *testing.T) {
	mgr := newTestManager(t)
	rec := NewH264Recorder(H264Config{
		CameraID:   "h264-paramset-copy",
		RTSPURL:    "rtsp://ignored",
		SegmentDur: time.Second,
		RingBufCap: 8,
	}, mgr)
	b := rec.baseRecorder

	original := append([]byte{}, testSPS...)
	nalu := append([]byte{}, original...) // stand-in for the pooled buffer
	H264NALDriver{}.handleParamSet(b, nalu, 7)
	// Simulate recycling: the same backing memory is refilled with the next
	// frame's content before the codec snapshot is read.
	for i := range nalu {
		nalu[i] = 0xFF
	}
	sps, pps, _ := b.codecSnapshot()
	require.NotNil(t, sps)
	require.True(t, bytes.Equal(sps, original),
		"codec snapshot must be a copy — pooled buffers are recycled after the drain iteration")
	require.Nil(t, pps)
}

// BenchmarkRTPFraming compares the per-NALU framing allocation the RTP
// callbacks make: fresh make vs the pool. One representative NALU size per
// class (P-frame ~1KB, IDR ~64KB).
func BenchmarkRTPFraming(b *testing.B) {
	for _, naluSize := range []int{1 << 10, 64 << 10} {
		nalu := make([]byte, naluSize)
		b.Run(fmt.Sprintf("Make/%dKB", naluSize>>10), func(b *testing.B) {
			for b.Loop() {
				data := make([]byte, 4+len(nalu))
				copy(data[4:], nalu)
				sink += len(data)
			}
		})
		b.Run(fmt.Sprintf("Pool/%dKB", naluSize>>10), func(b *testing.B) {
			var p frameBufPool = newFrameBufPool()
			for b.Loop() {
				buf := p.get(4 + len(nalu))
				copy(buf.b[4:], nalu)
				p.put(buf)
				sink += len(buf.b)
			}
		})
	}
}

var sink int
