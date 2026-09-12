package timelapse

// Wiring test for #754 — the frame extractor reads each recording exactly
// once; it must hint POSIX_FADV_SEQUENTIAL after open and POSIX_FADV_DONTNEED
// after the last sampled frame (single consumer, so the video-pass close is
// the right moment).

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/fadvise"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

func TestExtractorFadviseSequence(t *testing.T) {
	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "frames")

	var samples []testSample
	for i := range 30 {
		var samp testSample
		if i%5 == 0 {
			samp = testSample{data: buildH264IDRSample(), isKeyFrame: true, duration: 1}
		} else {
			samp = testSample{data: buildH264NonIDRSample(), isKeyFrame: false, duration: 1}
		}
		samples = append(samples, samp)
	}
	mp4Path := createTestMP4(t, tmpDir, "adv.h264.mp4", false, samples, 30)

	type adviseCall struct {
		fd     int
		advice int
	}
	var calls []adviseCall
	restore := fadvise.SwapForTest(func(fd, advice int) error {
		calls = append(calls, adviseCall{fd, advice})
		return nil
	})
	defer restore()

	extractor := NewRecordingFrameExtractor()
	n, err := extractor.ExtractFrames(mp4Path, model.FormatH264, 200*time.Millisecond, outputDir)
	require.NoError(t, err)
	require.Positive(t, n)

	require.GreaterOrEqual(t, len(calls), 2, "extractor must issue cache hints")
	first, last := calls[0], calls[len(calls)-1]
	require.Equal(t, fadvise.AdviceSequential, first.advice, "SEQUENTIAL right after open")
	require.Equal(t, fadvise.AdviceDontNeed, last.advice, "DONTNEED after the last read")
	require.Equal(t, first.fd, last.fd)
}
