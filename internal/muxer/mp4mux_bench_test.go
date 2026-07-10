package muxer

import (
	"path/filepath"
	"testing"
	"time"
)

// Muxer benchmarks — establish the cost of MP4Muxer.Close() (the only write
// path) and the per-sample memory overhead. This data is needed to assess
// whether the current non-fMP4 muxer is viable for quasi-real-time merge
// or whether fMP4 is required.
//
// Key questions these benchmarks answer:
// 1. How much RAM does a 30s/10min segment consume in track.samples?
// 2. How long does Close() take (ftyp + moov sizing + mdat write)?
// 3. Does Close() scale linearly with sample count?

var benchTestSPS = []byte{0x67, 0x42, 0xc0, 0x1e, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0xc8}
var benchTestPPS = []byte{0x68, 0xce, 0x38, 0x80}

// benchNAL4K is a ~4KB IDR NAL — realistic for a CIF/SD keyframe.
var benchNAL4K = make([]byte, 4096)

// benchNAL512 is a ~512B P-frame NAL.
var benchNAL512 = make([]byte, 512)

func init() {
	for i := range benchNAL4K {
		benchNAL4K[i] = byte(i)
	}
	for i := range benchNAL512 {
		benchNAL512[i] = byte(i + 1)
	}
}

// writeBenchSamples writes frameRate*duration samples to the muxer using a
// realistic IDR+P-frame pattern (one IDR per second).
func writeBenchSamples(b *testing.B, m *MP4Muxer, trackID int, frameRate int, duration time.Duration) {
	b.Helper()
	frameDur := time.Second / time.Duration(frameRate)
	totalFrames := int(duration / frameDur)
	if totalFrames < 1 {
		totalFrames = 1
	}
	for i := 0; i < totalFrames; i++ {
		nalu := benchNAL512
		if i%frameRate == 0 {
			nalu = benchNAL4K
		}
		pts := time.Duration(i) * frameDur
		if err := m.WriteSample(trackID, nalu, pts, frameDur); err != nil {
			b.Fatal(err)
		}
	}
}

// ---------------------------------------------------------------------------
// BenchmarkMP4MuxerWriteSample — per-sample append cost + memory.
//
// WriteSample appends to track.samples (in-memory slice). This measures the
// per-sample overhead (slice growth + data copy). At 30fps this runs 30x/sec
// on the recording hot path, so even a few µs per call matters.
// ---------------------------------------------------------------------------

func BenchmarkMP4MuxerWriteSample(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "bench.mp4")
	m := NewMP4Muxer(path)
	trackID, err := m.AddH264Track(benchTestSPS, benchTestPPS)
	if err != nil {
		b.Fatal(err)
	}
	frameDur := 33 * time.Millisecond

	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		pts := time.Duration(i) * frameDur
		if err := m.WriteSample(trackID, benchNAL512, pts, frameDur); err != nil {
			b.Fatal(err)
		}
	}
}

// ---------------------------------------------------------------------------
// BenchmarkMP4MuxerClose — the finalize cost (ftyp + moov + mdat write).
//
// Close() does: moov size pre-computation (in-memory bytesWriter) → ftyp →
// real moov → collectMdatData (loads ALL samples into one []byte) → mdat write.
// This is the bottleneck that prevents incremental/streaming output.
//
// Sub-benchmarks vary sample count to show scaling. Each case creates a fresh
// muxer, writes samples (outside timer), then benchmarks Close() only.
// b.ReportMetric reports peak alloc to track the RAM ceiling (relevant for
// the 512MB process budget on RPi 3B).
// ---------------------------------------------------------------------------

func BenchmarkMP4MuxerClose(b *testing.B) {
	cases := []struct {
		name      string
		frameRate int
		duration  time.Duration
		desc      string
	}{
		{"30s_30fps_900samples", 30, 30 * time.Second, "default segment"},
		{"10min_30fps_18000samples", 30, 10 * time.Minute, "legacy segment dur"},
		{"30s_15fps_450samples", 15, 30 * time.Second, "low-fps cam"},
		{"5min_30fps_9000samples", 30, 5 * time.Minute, "mid-length"},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				b.StopTimer()
				dir := b.TempDir()
				path := filepath.Join(dir, "bench.mp4")
				m := NewMP4Muxer(path)
				trackID, err := m.AddH264Track(benchTestSPS, benchTestPPS)
				if err != nil {
					b.Fatal(err)
				}
				writeBenchSamples(b, m, trackID, tc.frameRate, tc.duration)
				b.StartTimer()

				if err := m.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
