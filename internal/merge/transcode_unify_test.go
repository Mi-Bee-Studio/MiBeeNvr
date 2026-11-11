package merge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// buildFFStub compiles the ffstub stand-in (testdata/ffstub/main.go, excluded
// from the package build by its ignore tag) into a temp executable. The stub
// copies the FIRST concat-list entry to the output — deterministic on any
// host, no real ffmpeg required.
func buildFFStub(t *testing.T) string {
	t.Helper()
	stub := filepath.Join(t.TempDir(), "ffstub.bin")
	cmd := exec.Command("go", "build", "-o", stub, filepath.Join("testdata", "ffstub", "main.go"))
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "building ffstub: %s", out)
	return stub
}

// TestTranscodeUnify_MixedCodecsStub — #1150 C②: a run the Go merger rejects
// (SPS mismatch) unifies through the ffmpeg path. ffmpegLookup is pointed at
// the deterministic stub, which copies the first concat entry to the output —
// exercising the list building, argument wiring, and output validation.
func TestTranscodeUnify_MixedCodecsStub(t *testing.T) {
	stub := buildFFStub(t)
	orig := ffmpegLookup
	ffmpegLookup = func() (string, error) { return stub, nil }
	defer func() { ffmpegLookup = orig }()

	dir := t.TempDir()
	spsA := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	idrNAL := []byte{0x65, 0x88, 0x80, 0x40}
	pNAL := []byte{0x41, 0x10, 0x00, 0x0c}
	spsB := []byte{0x67, 0x64, 0x00, 0x1f, 0xac, 0xd9, 0x40, 0x50, 0x05, 0xbb, 0x01, 0x10, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x7b, 0xac, 0x09}

	segA := createH264SegmentWithSamples(t, dir, "a.mp4", spsA, pps, [][]byte{idrNAL, pNAL})
	segB := createH264SegmentWithSamples(t, dir, "b.mp4", spsB, pps, [][]byte{idrNAL, pNAL, pNAL})
	infoA, err := ParseSegment(segA)
	require.NoError(t, err)
	infoB, err := ParseSegment(segB)
	require.NoError(t, err)
	// Sanity: the Go merger rejects this exact pair — that is why the unify
	// path exists (#1150 field case).
	_, err = MergeMP4Segments(context.Background(), []*SegmentInfo{infoA, infoB}, filepath.Join(dir, "go.mp4"))
	require.Error(t, err)

	out := filepath.Join(dir, "unified.mp4")
	// Capture the stub's argv to pin the flag wiring (see the -f mp4 comment
	// in transcodeUnifyTo — the M5 field catch).
	argvFile := filepath.Join(dir, "ffstub.argv")
	t.Setenv("FFSTUB_ARGV_FILE", argvFile)
	stats, err := transcodeUnifyTo(context.Background(), []*SegmentInfo{infoA, infoB}, out)
	require.NoError(t, err)
	require.Equal(t, []int{0, 1}, stats.Included, "ffmpeg renders the run or fails outright — all inputs included")
	require.NotEmpty(t, stats.WallToFile)

	argv, rerr := os.ReadFile(argvFile)
	require.NoError(t, rerr)
	lines := strings.Split(string(argv), "\n")
	hasOutputMuxer := false
	for i, l := range lines {
		if l == "-f" && i+2 < len(lines) && lines[i+1] == "mp4" && lines[i+2] == "-y" {
			hasOutputMuxer = true
		}
	}
	require.True(t, hasOutputMuxer, "output side must pin -f mp4: ffmpeg infers the muxer from the file extension and the out path is a .tmp during the run")

	merged, err := ParseSegment(out)
	require.NoError(t, err)
	require.Greater(t, merged.SampleCount, 0)
}

// TestTranscodeUnify_DurationGuard — the M5 field catch class: a broken
// join (concat demuxer on codec-mismatched files) exits 0 with a valid
// output that only contains the FIRST segment. The stub reproduces that
// shape (copies the first -i input); the output-span gate must reject it.
func TestTranscodeUnify_DurationGuard(t *testing.T) {
	stub := buildFFStub(t)
	orig := ffmpegLookup
	ffmpegLookup = func() (string, error) { return stub, nil }
	defer func() { ffmpegLookup = orig }()

	dir := t.TempDir()
	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	idrNAL := []byte{0x65, 0x88, 0x80, 0x40}
	pNAL := []byte{0x41, 0x10, 0x00, 0x0c}
	// segA ≈ 99ms; segB ≈ 825ms (25 samples × 33ms) so the gate's
	// 0.8×wall − 0.25s slack has real room to trip.
	segA := createH264SegmentWithSamples(t, dir, "a.mp4", sps, pps, [][]byte{idrNAL, pNAL, pNAL})
	samplesB := make([][]byte, 25)
	for i := range samplesB {
		samplesB[i] = pNAL
	}
	segB := createH264SegmentWithSamples(t, dir, "b.mp4", sps, pps, samplesB)
	infoA, err := ParseSegment(segA)
	require.NoError(t, err)
	infoB, err := ParseSegment(segB)
	require.NoError(t, err)

	out := filepath.Join(dir, "unified.mp4")
	_, err = transcodeUnifyTo(context.Background(), []*SegmentInfo{infoA, infoB}, out)
	require.Error(t, err)
	require.Contains(t, err.Error(), "inputs were dropped")
}

// TestTranscodeUnify_FFMpegMissing — a transcode_merge camera on a host
// without ffmpeg fails the unify with an actionable error (the caller marks
// the run incompatible with the reason; retryable once ffmpeg appears).
func TestTranscodeUnify_FFMpegMissing(t *testing.T) {
	orig := ffmpegLookup
	ffmpegLookup = func() (string, error) { return "", os.ErrNotExist }
	defer func() { ffmpegLookup = orig }()

	dir := t.TempDir()
	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	seg := createH264SegmentWithSamples(t, dir, "a.mp4", sps, pps, [][]byte{{0x65, 0x88, 0x80, 0x40}})
	info, err := ParseSegment(seg)
	require.NoError(t, err)

	stats, err := transcodeUnifyTo(context.Background(), []*SegmentInfo{info}, filepath.Join(dir, "out.mp4"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires ffmpeg")
	require.Nil(t, stats)
}
