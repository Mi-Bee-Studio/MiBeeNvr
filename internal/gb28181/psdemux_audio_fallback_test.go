package gb28181

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

// buildAudioPSVar is buildAudioPS with toggles: PSM present, audio entry in
// the PSM, and PTS on the audio PES. Video PES always carries PTS (it anchors
// the clock domain exactly like real devices).
func buildAudioPSVar(videoNALU, audio []byte, psm, audioEntry, audioPTS bool, ptsVideo, ptsAudio int64) []byte {
	var ps bytes.Buffer

	ps.Write([]byte{0x00, 0x00, 0x01, 0xBA})
	ps.Write([]byte{0x44, 0x00, 0x04, 0x00, 0x04, 0x01, 0x01, 0x89, 0xC3, 0xF8})

	if psm {
		ps.Write([]byte{0x00, 0x00, 0x01, 0xBC})
		body := []byte{
			0x02,       // version
			0xC0,       // reserved
			0x00, 0x00, // PS_info_length = 0
		}
		esMap := []byte{streamTypeH264, 0xE0, 0x00, 0x00}
		if audioEntry {
			esMap = append(esMap, streamTypeG711A, 0xC0, 0x00, 0x00)
		}
		body = append(body, byte(len(esMap)>>8), byte(len(esMap)))
		body = append(body, esMap...)
		ps.Write([]byte{byte(len(body) >> 8), byte(len(body))})
		ps.Write(body)
	}

	if videoNALU != nil {
		es := append([]byte{0x00, 0x00, 0x00, 0x01}, videoNALU...)
		ps.Write([]byte{0x00, 0x00, 0x01, 0xE0})
		pesHdrLen := 3 + 5
		ps.Write([]byte{byte((pesHdrLen + len(es)) >> 8), byte(pesHdrLen + len(es))})
		ps.Write([]byte{0x80, 0x80, 0x05})
		ps.Write(encodePESPTS(ptsVideo))
		ps.Write(es)
	}

	ps.Write([]byte{0x00, 0x00, 0x01, 0xC0})
	if audioPTS {
		hdrLen := 3 + 5
		ps.Write([]byte{byte((hdrLen + len(audio)) >> 8), byte(hdrLen + len(audio))})
		ps.Write([]byte{0x80, 0x80, 0x05})
		ps.Write(encodePESPTS(ptsAudio))
	} else {
		hdrLen := 3
		ps.Write([]byte{byte((hdrLen + len(audio)) >> 8), byte(hdrLen + len(audio))})
		ps.Write([]byte{0x80, 0x00, 0x00})
	}
	ps.Write(audio)
	return ps.Bytes()
}

// mulawIsh returns a G.711 μ-law-shaped payload: near-silent samples cluster
// at 0xFF/0x7F, plus a few mid-amplitude speech bytes.
func mulawIsh() []byte {
	return append(bytes.Repeat([]byte{0xFF, 0x7F}, 60), 0x25, 0xA5, 0x30, 0xB0)
}

// alawIsh returns a G.711 A-law-shaped payload (quiet cluster 0x55/0xD5).
func alawIsh() []byte {
	return append(bytes.Repeat([]byte{0x55, 0xD5}, 60), 0x70, 0xF0, 0x74, 0xF4)
}

func feedAUs(t *testing.T, d *PSDemuxer, audio []byte, psm, audioEntry, audioPTS bool, n int) []AudioFrame {
	t.Helper()
	var frames []AudioFrame
	for i := range n {
		ps := buildAudioPSVar([]byte{0x67, 0x42}, audio, psm, audioEntry, audioPTS,
			int64(90000+i*3000), int64(90160+i*3000))
		_, err := d.FeedAU(ps, int64(9000+i*3000), true)
		require.NoError(t, err)
		frames = append(frames, d.DrainAudio()...)
	}
	return frames
}

func TestPSDemuxAudioHeuristicMulawNoPSM(t *testing.T) {
	d := NewPSDemuxer()
	// 40 payloads × 124B = 4960B scanned: the 4KB voting threshold crosses on
	// the 34th, so only the tail emits — but it MUST emit.
	frames := feedAUs(t, d, mulawIsh(), false, false, true, 40)
	require.NotEmpty(t, frames, "heuristic must latch after 4KB of payload votes")
	for _, f := range frames {
		require.Equal(t, AudioCodecG711U, f.Codec)
	}
}

func TestPSDemuxAudioHeuristicAlawNoPSM(t *testing.T) {
	d := NewPSDemuxer()
	frames := feedAUs(t, d, alawIsh(), false, false, true, 40)
	require.NotEmpty(t, frames)
	for _, f := range frames {
		require.Equal(t, AudioCodecG711A, f.Codec)
	}
}

func TestPSDemuxAudioVideoOnlyPSMStillFallsBack(t *testing.T) {
	// A PSM with no audio entries is not an authoritative "no audio" when the
	// stream still carries audio PES packets — the fallback engages.
	d := NewPSDemuxer()
	frames := feedAUs(t, d, mulawIsh(), true, false, true, 40)
	require.NotEmpty(t, frames)
	for _, f := range frames {
		require.Equal(t, AudioCodecG711U, f.Codec)
	}
}

func TestPSDemuxAudioHintResolvesImmediately(t *testing.T) {
	d := NewPSDemuxer()
	d.SetAudioCodecHint(AudioCodecG711A)
	frames := feedAUs(t, d, mulawIsh(), false, false, true, 1)
	require.Len(t, frames, 1)
	require.Equal(t, AudioCodecG711A, frames[0].Codec)
}

func TestPSDemuxAudioADTSFallbackNoPSM(t *testing.T) {
	d := NewPSDemuxer()
	adts := buildADTS(bytes.Repeat([]byte{0x33}, 64), 44100, 1)
	frames := feedAUs(t, d, adts, false, false, true, 1)
	require.Len(t, frames, 1)
	require.Equal(t, AudioCodecAAC, frames[0].Codec)
	require.NotNil(t, frames[0].Config, "ADTS-derived AudioSpecificConfig expected")
}

func TestPSDemuxPSMArrivingOverridesHeuristic(t *testing.T) {
	d := NewPSDemuxer()
	// Phase 1: no PSM → heuristic latches μ-law.
	frames := feedAUs(t, d, mulawIsh(), false, false, true, 40)
	require.NotEmpty(t, frames)
	require.Equal(t, AudioCodecG711U, frames[0].Codec)

	// Phase 2: a PSM declaring A-law arrives — the device's own statement wins.
	frames = feedAUs(t, d, alawIsh(), true, true, true, 1)
	require.Len(t, frames, 1)
	require.Equal(t, AudioCodecG711A, frames[0].Codec)
}

func TestPSDemuxAudioNoPTSContinuation(t *testing.T) {
	d := NewPSDemuxer()
	// Video PES (with PTS) anchors the clock; audio PES carries no PTS.
	f1 := feedAUs(t, d, bytes.Repeat([]byte{0x77}, 100), true, true, false, 1)
	require.Len(t, f1, 1)
	require.Equal(t, int64(9000), f1[0].PTSTicks, "first PTS-less frame anchors to the AU RTP clock")

	f2 := feedAUs(t, d, bytes.Repeat([]byte{0x78}, 100), true, true, false, 1)
	require.Len(t, f2, 1)
	require.Equal(t, f1[0].PTSTicks+100, f2[0].PTSTicks,
		"PTS-less frames must advance by their sample count, not inherit the video AU clock")
}

func TestPSDemuxAudioNoPTSAudioOnlyDroppedUntilAnchor(t *testing.T) {
	// Before any PTS-bearing PES establishes the clock domain, PTS-less audio
	// cannot be placed on a timeline and is dropped (pre-existing behavior).
	ps := buildAudioPSVar(nil, bytes.Repeat([]byte{0x77}, 50), false, false, false, 0, 0)
	d := NewPSDemuxer()
	_, err := d.FeedAU(ps, 9000, true)
	require.NoError(t, err)
	require.Empty(t, d.DrainAudio())
}
