package merge

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// #488 regression tests: keyframe-aligned stitching. A segment that starts
// mid-GOP references frames that only exist in the previous source file —
// after the merge deletes it, players conceal those P-frames with gray until
// the next IDR. The merge must drop each segment's leading non-keyframe
// samples and skip keyframe-less segments entirely.

func TestMergeMP4Segments_KeyframeAlignmentDropsLeadingNonKeyframe(t *testing.T) {
	dir := t.TempDir()

	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	idrNAL := []byte{0x65, 0x88, 0x80, 0x40}
	pNAL := []byte{0x41, 0x10, 0x00, 0x0c}

	// seg1 starts at an IDR (normal); seg2 starts with two P-frames — the
	// TL-exit flush / reconnect-micro-segment shape from the field.
	seg1 := createH264SegmentWithSamples(t, dir, "seg1.mp4", sps, pps, [][]byte{idrNAL, pNAL})
	seg2 := createH264SegmentWithSamples(t, dir, "seg2.mp4", sps, pps, [][]byte{pNAL, pNAL, idrNAL, pNAL})

	info1, err := ParseSegment(seg1)
	require.NoError(t, err)
	info2, err := ParseSegment(seg2)
	require.NoError(t, err)
	require.Equal(t, 4, info2.SampleCount)

	outputPath := filepath.Join(dir, "merged.mp4")
	stats, err := MergeMP4Segments(context.Background(), []*SegmentInfo{info1, info2}, outputPath)
	require.NoError(t, err)
	require.Equal(t, []int{0, 1}, stats.Included)
	require.Empty(t, stats.SkippedNoKeyframe)
	require.Equal(t, 2, stats.LeadingDropped[1], "seg2's two leading P-frames must be dropped")
	require.Equal(t, 0, stats.LeadingDropped[0])

	// Input infos are mutated to their aligned state (documented contract).
	require.Equal(t, 2, info2.SampleCount)

	merged, err := ParseSegment(outputPath)
	require.NoError(t, err)
	require.Equal(t, 4, merged.SampleCount, "2 (seg1) + 2 (aligned seg2)")
	require.Equal(t, 132*time.Millisecond, merged.TotalDuration)

	// Every segment run in the output must start at a keyframe: exactly two
	// keyframes — seg1's IDR and seg2's aligned IDR.
	keyframes := 0
	for _, s := range merged.Samples {
		if s.IsKeyFrame {
			keyframes++
		}
	}
	require.Equal(t, 2, keyframes)
}

func TestMergeMP4Segments_SkipsKeyframelessSegment(t *testing.T) {
	dir := t.TempDir()

	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	idrNAL := []byte{0x65, 0x88, 0x80, 0x40}
	pNAL := []byte{0x41, 0x10, 0x00, 0x0c}

	seg1 := createH264SegmentWithSamples(t, dir, "seg1.mp4", sps, pps, [][]byte{idrNAL, pNAL})
	seg2 := createH264SegmentWithSamples(t, dir, "seg2.mp4", sps, pps, [][]byte{pNAL, pNAL, pNAL})

	info1, err := ParseSegment(seg1)
	require.NoError(t, err)
	info2, err := ParseSegment(seg2)
	require.NoError(t, err)

	outputPath := filepath.Join(dir, "merged.mp4")
	stats, err := MergeMP4Segments(context.Background(), []*SegmentInfo{info1, info2}, outputPath)
	require.NoError(t, err)
	require.Equal(t, []int{0}, stats.Included)
	require.Equal(t, []int{1}, stats.SkippedNoKeyframe)

	merged, err := ParseSegment(outputPath)
	require.NoError(t, err)
	require.Equal(t, 2, merged.SampleCount, "output carries only seg1's samples")
}

func TestMergeMP4Segments_ErrNoKeyframeWhenAllSkipped(t *testing.T) {
	dir := t.TempDir()

	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	pNAL := []byte{0x41, 0x10, 0x00, 0x0c}

	seg := createH264SegmentWithSamples(t, dir, "seg.mp4", sps, pps, [][]byte{pNAL, pNAL})
	info, err := ParseSegment(seg)
	require.NoError(t, err)

	outputPath := filepath.Join(dir, "merged.mp4")
	_, err = MergeMP4Segments(context.Background(), []*SegmentInfo{info}, outputPath)
	require.ErrorIs(t, err, ErrNoKeyframe)
	require.NoFileExists(t, outputPath)
}

func TestAlignToKeyframe_TrimsAudioHeadWithVideo(t *testing.T) {
	// Video: timescale 90000, 4 samples × 33.33ms, first two are P-frames.
	// Audio: timescale 8000, 10 samples × 23ms (dur 184).
	// Dropped video head = 66.67ms → audio drops the samples whose midpoint
	// falls before it: 11.5, 34.5 and 57.5ms (3 samples, 69ms ≥ 66.67ms —
	// the midpoint rule keeps A/V starts within half a sample).
	seg := &SegmentInfo{
		Timescale:     90000,
		SampleCount:   4,
		TotalDuration: 4 * 33333 * time.Microsecond,
		Samples: []SampleEntry{
			{Offset: 100, Size: 10, Duration: 3000},
			{Offset: 110, Size: 10, Duration: 3000},
			{Offset: 120, Size: 10, Duration: 3000, IsKeyFrame: true},
			{Offset: 130, Size: 10, Duration: 3000},
		},
		AudioTimescale:   8000,
		AudioSampleCount: 10,
	}
	for i := range 10 {
		seg.AudioSamples = append(seg.AudioSamples, SampleEntry{Offset: int64(200 + i), Size: 4, Duration: 184})
	}

	dropped, ok := AlignToKeyframe(seg)
	require.True(t, ok)
	require.Equal(t, 2, dropped)
	require.Len(t, seg.Samples, 2)
	require.Equal(t, 2, seg.SampleCount)
	require.True(t, seg.Samples[0].IsKeyFrame)
	require.Equal(t, 7, seg.AudioSampleCount, "3 leading audio samples trimmed with the video head")
	require.Len(t, seg.AudioSamples, 7)

	// A segment already starting at a keyframe is untouched.
	seg2 := &SegmentInfo{
		Timescale:   90000,
		SampleCount: 1,
		Samples:     []SampleEntry{{Offset: 1, Size: 2, Duration: 3000, IsKeyFrame: true}},
	}
	dropped, ok = AlignToKeyframe(seg2)
	require.True(t, ok)
	require.Equal(t, 0, dropped)

	// A segment with no keyframe reports !ok and is left for the caller to
	// keep standalone (mutating it would silently drop its content).
	seg3 := &SegmentInfo{
		Timescale:   90000,
		SampleCount: 2,
		Samples:     []SampleEntry{{Offset: 1, Size: 2, Duration: 3000}, {Offset: 3, Size: 2, Duration: 3000}},
	}
	dropped, ok = AlignToKeyframe(seg3)
	require.False(t, ok)
	require.Equal(t, 0, dropped)
	require.Len(t, seg3.Samples, 2)
}

func TestSplitRunsByCompatKey_SplitsOnCodecParamsAndAudio(t *testing.T) {
	spsA := []byte{0x67, 0x01}
	spsB := []byte{0x67, 0x02}
	pps := []byte{0x68, 0x01}

	info := func(sps []byte, hasAudio bool) *SegmentInfo {
		si := &SegmentInfo{Codec: "h264", SPS: sps, PPS: pps}
		if hasAudio {
			si.HasAudio = true
			si.AudioCodec = "g711"
			si.G711MULaw = true
			si.AudioTimescale = 8000
		}
		return si
	}

	recs := []*model.Recording{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}
	infos := []*SegmentInfo{
		info(spsA, false), // run 1: alone (SPS A)
		info(spsB, false), // run 2: SPS B, no audio
		info(spsB, false), //        continues (same key)
		info(spsB, true),  // run 3: audio appears → key change
	}

	runs := splitRunsByCompatKey(recs, infos)
	require.Len(t, runs, 3)
	require.Len(t, runs[0].infos, 1)
	require.Len(t, runs[1].infos, 2)
	require.Len(t, runs[2].infos, 1)
}
