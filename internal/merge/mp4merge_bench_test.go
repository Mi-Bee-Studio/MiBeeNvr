package merge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
)

// Benchmark-driven merge feasibility analysis. These benchmarks establish the
// empirical baseline needed to assess quasi-real-time (<10s) merge feasibility
// on the target hardware (RPi 3B, USB HDD). Run on real hardware before making
// architectural decisions based on these numbers.

// Valid H.264 Baseline SPS/PPS (CIF-ish, level 1.0) — reused from mp4merge_test.go.
var (
	benchSPS = []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	benchPPS = []byte{0x68, 0xce, 0x38, 0x80}
)

// benchIDRNAL is a small IDR slice NAL — real segments have larger NALs but
// the merge cost is dominated by I/O (seek+read+write), not NAL content.
var benchIDRNAL = make([]byte, 4096) // ~4KB per IDR frame (typical for a keyframe at low bitrate)

// benchPNAL is a small P-slice NAL.
var benchPNAL = make([]byte, 512) // ~0.5KB per P-frame

func init() {
	// Fill with non-zero pattern so the data isn't zero-compressed by the OS page cache.
	for i := range benchIDRNAL {
		benchIDRNAL[i] = byte(i)
	}
	for i := range benchPNAL {
		benchPNAL[i] = byte(i + 1)
	}
}

// createBenchSegment creates an H.264 MP4 segment with a realistic frame
// pattern: one IDR (keyframe) followed by frameRate*duration-1 P-frames.
// This approximates a real recording segment.
func createBenchSegment(b *testing.B, dir, name string, frameRate int, duration time.Duration) string {
	b.Helper()
	path := filepath.Join(dir, name)

	m := muxer.NewMP4Muxer(path)
	trackID, err := m.AddH264Track(benchSPS, benchPPS)
	if err != nil {
		b.Fatal(err)
	}

	frameDur := time.Second / time.Duration(frameRate)
	totalFrames := int(duration / frameDur)
	if totalFrames < 1 {
		totalFrames = 1
	}

	for i := range totalFrames {
		var nalu []byte
		if i%frameRate == 0 {
			nalu = benchIDRNAL // IDR once per second
		} else {
			nalu = benchPNAL // P-frame
		}
		pts := time.Duration(i) * frameDur
		if err := m.WriteSample(trackID, nalu, pts, frameDur); err != nil {
			b.Fatal(err)
		}
	}

	if err := m.Close(); err != nil {
		b.Fatal(err)
	}
	return path
}

// parseBenchSegments parses a set of segment files, failing fast on error.
func parseBenchSegments(b *testing.B, paths []string) []*SegmentInfo {
	b.Helper()
	infos := make([]*SegmentInfo, len(paths))
	for i, p := range paths {
		info, err := ParseSegment(p)
		if err != nil {
			b.Fatalf("ParseSegment(%s): %v", p, err)
		}
		infos[i] = info
	}
	return infos
}

// ---------------------------------------------------------------------------
// BenchmarkParseSegment — moov-only extraction cost (skips mdat).
//
// ParseSegment is called once per source segment during merge. It walks the
// MP4 box structure via abema/go-mp4's ReadBoxStructure, reads stbl tables,
// and does per-sample keyframe detection (6 bytes/sample via ReadAt).
// This is CPU+small-random-read bound, NOT sequential I/O bound. For a 30s
// segment at 30fps (900 samples), detectKeyframes does 900 ReadAt calls.
// ---------------------------------------------------------------------------

func BenchmarkParseSegment(b *testing.B) {
	cases := []struct {
		name      string
		frameRate int
		duration  time.Duration
	}{
		{"30s_30fps", 30, 30 * time.Second}, // realistic segment (default SegmentDur)
		{"10min_30fps", 30, 10 * time.Minute},
		{"30s_15fps", 15, 30 * time.Second}, // low-FPS camera
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			dir := b.TempDir()
			segPath := createBenchSegment(b, dir, "bench_seg.mp4", tc.frameRate, tc.duration)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := ParseSegment(segPath); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// BenchmarkMergeMP4Segments — the core streaming merge throughput.
//
// Measures the full merge pipeline: ftyp + placeholder moov + streaming mdat
// copy (1MB buffer) + mdat size patch + real moov rewrite. This is the
// dominant cost of a quasi-real-time rolling merge.
//
// Sub-benchmarks vary segment count to show scaling. Each case creates N
// segments then merges them into one output. b.ReportMetric reports MB/s
// throughput so it can be compared against USB HDD sequential write speed.
// ---------------------------------------------------------------------------

func BenchmarkMergeMP4Segments(b *testing.B) {
	segmentCounts := []int{2, 5, 10} // 2 = rolling append, 10 = hourly batch
	for _, n := range segmentCounts {
		b.Run(fmt.Sprintf("segs=%d", n), func(b *testing.B) {
			dir := b.TempDir()
			// Pre-create segments (outside timer).
			paths := make([]string, n)
			for i := range n {
				paths[i] = createBenchSegment(b, dir,
					fmt.Sprintf("src_%d.mp4", i), 30, 30*time.Second)
			}
			infos := parseBenchSegments(b, paths)

			// Measure total source bytes for throughput reporting.
			var totalSrcBytes int64
			for _, p := range paths {
				if fi, err := os.Stat(p); err == nil {
					totalSrcBytes += fi.Size()
				}
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := range b.N {
				outputPath := filepath.Join(dir, fmt.Sprintf("merged_%d.mp4", i))
				if _, err := MergeMP4Segments(
					context.Background(), infos, outputPath,
				); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()

			// Report throughput in MB/s based on source bytes processed.
			// b.N iterations each processed totalSrcBytes of source data.
			if totalSrcBytes > 0 {
				mbPerOp := float64(totalSrcBytes) / (1024 * 1024)
				nsPerOp := b.Elapsed().Nanoseconds() / int64(b.N)
				if nsPerOp > 0 {
					mbPerSec := mbPerOp / (float64(nsPerOp) / 1e9)
					b.ReportMetric(mbPerSec, "MB/s")
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// BenchmarkMergeMP4Segments_LargeNALs — merge with larger frame sizes.
//
// Real 1080p H.264 segments have much larger NALs (IDR ~50-200KB, P ~5-20KB).
// This variant uses bigger payloads to test whether the merge throughput
// changes with realistic data sizes (it should — larger NALs mean fewer
// per-sample seeks, better sequential I/O utilization).
// ---------------------------------------------------------------------------

func BenchmarkMergeMP4Segments_LargeNALs(b *testing.B) {
	const n = 5
	const frameRate = 30
	const segmentDur = 30 * time.Second

	// Large NALs approximating 1080p H.264 at moderate bitrate.
	largeIDR := make([]byte, 100*1024) // 100KB IDR
	largeP := make([]byte, 15*1024)    // 15KB P-frame
	for i := range largeIDR {
		largeIDR[i] = byte(i)
	}
	for i := range largeP {
		largeP[i] = byte(i + 1)
	}

	b.Run("5x30s_1080p", func(b *testing.B) {
		dir := b.TempDir()
		paths := make([]string, n)
		frameDur := time.Second / time.Duration(frameRate)
		totalFrames := int(segmentDur / frameDur)

		for i := range n {
			path := filepath.Join(dir, fmt.Sprintf("1080p_%d.mp4", i))
			m := muxer.NewMP4Muxer(path)
			trackID, err := m.AddH264Track(benchSPS, benchPPS)
			if err != nil {
				b.Fatal(err)
			}
			for j := range totalFrames {
				nalu := largeP
				if j%frameRate == 0 {
					nalu = largeIDR
				}
				pts := time.Duration(j) * frameDur
				if err := m.WriteSample(trackID, nalu, pts, frameDur); err != nil {
					b.Fatal(err)
				}
			}
			if err := m.Close(); err != nil {
				b.Fatal(err)
			}
			paths[i] = path
		}
		infos := parseBenchSegments(b, paths)

		var totalSrcBytes int64
		for _, p := range paths {
			if fi, err := os.Stat(p); err == nil {
				totalSrcBytes += fi.Size()
			}
		}

		b.ReportAllocs()
		b.ResetTimer()
		for i := range b.N {
			outputPath := filepath.Join(dir, fmt.Sprintf("merged_%d.mp4", i))
			if _, err := MergeMP4Segments(
				context.Background(), infos, outputPath,
			); err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()

		if totalSrcBytes > 0 {
			mbPerOp := float64(totalSrcBytes) / (1024 * 1024)
			nsPerOp := b.Elapsed().Nanoseconds() / int64(b.N)
			if nsPerOp > 0 {
				b.ReportMetric(mbPerOp/(float64(nsPerOp)/1e9), "MB/s")
			}
		}
	})
}

// ---------------------------------------------------------------------------
// BenchmarkRollingMergeSimulation — simulates the quasi-real-time rolling
// merge pattern: merge [existing_merged + new_segment] iteratively.
//
// This is the critical benchmark for the <10s latency target. Each iteration
// appends one new 30s segment to an accumulating merged file, matching the
// proposed RollingMergeCoordinator's behavior. The Nth append merges a file
// containing (N-1) segments + 1 new segment.
//
// If the per-append latency stays <10s even at N=120 (1 hour of 30s segments),
// the rolling approach is viable without fMP4.
// ---------------------------------------------------------------------------

func BenchmarkRollingMergeSimulation(b *testing.B) {
	// Simulate 1 hour of recording: 120 segments × 30s each.
	// We measure each append operation independently.
	const numAppends = 120

	b.Run("iterative_append_1h", func(b *testing.B) {
		dir := b.TempDir()

		// Pre-create all source segments.
		srcPaths := make([]string, numAppends)
		for i := range numAppends {
			srcPaths[i] = createBenchSegment(b, dir,
				fmt.Sprintf("src_%03d.mp4", i), 30, 30*time.Second)
		}

		b.ReportAllocs()
		b.ResetTimer()

		// First "merged" file is just the first segment (parsed).
		currentMergedPath := srcPaths[0]
		var lastDurationMs float64

		for appendIdx := 1; appendIdx < numAppends; appendIdx++ {
			// Parse the current accumulated file + the new segment.
			mergedInfo, err := ParseSegment(currentMergedPath)
			if err != nil {
				b.Fatal(err)
			}
			newInfo, err := ParseSegment(srcPaths[appendIdx])
			if err != nil {
				b.Fatal(err)
			}

			outputPath := filepath.Join(dir, fmt.Sprintf("rolling_%03d.mp4", appendIdx))
			start := time.Now()
			if _, err := MergeMP4Segments(
				context.Background(),
				[]*SegmentInfo{mergedInfo, newInfo},
				outputPath,
			); err != nil {
				b.Fatal(err)
			}
			elapsed := time.Since(start)
			lastDurationMs = float64(elapsed.Milliseconds())

			// The output becomes the input for the next append.
			currentMergedPath = outputPath

			// Clean up old merged files to avoid filling tmp (keep last 2).
			if appendIdx > 2 {
				old := filepath.Join(dir, fmt.Sprintf("rolling_%03d.mp4", appendIdx-2))
				os.Remove(old)
			}
		}

		b.StopTimer()
		// Report the last (worst-case) append latency — this is the number
		// that determines whether <10s is achievable at full 1h accumulation.
		b.ReportMetric(lastDurationMs, "last_append_ms")
	})
}
