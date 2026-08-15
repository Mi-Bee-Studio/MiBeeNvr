package gb28181

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

// encodePESPTS packs a 33-bit PTS into the 5-byte H.222.0 §2.4.3.6 layout.
func encodePESPTS(pts int64) []byte {
	return []byte{
		0x20 | byte((pts>>30)&0x07)<<1 | 1,
		byte(pts >> 22),
		byte(pts>>15)<<1 | 1,
		byte(pts >> 7),
		byte(pts&0x7F)<<1 | 1,
	}
}

// buildAudioPS assembles a minimal PS program: pack header, PSM declaring
// the video (0xE0) and audio (0xC0) stream types, one video PES carrying
// ptsVideo, then one audio PES carrying ptsAudio. Used to exercise the PS
// audio demux path end-to-end.
func buildAudioPS(videoNALU []byte, audio []byte, videoStreamType, audioStreamType byte, ptsVideo, ptsAudio int64, rtpVideo int64) []byte {
	var ps bytes.Buffer

	// Pack header
	ps.Write([]byte{0x00, 0x00, 0x01, 0xBA})
	ps.Write([]byte{0x44, 0x00, 0x04, 0x00, 0x04, 0x01, 0x01, 0x89, 0xC3, 0xF8})

	// PSM with two entries: video + audio
	ps.Write([]byte{0x00, 0x00, 0x01, 0xBC})
	body := []byte{
		0x02,       // version
		0xC0,       // reserved
		0x00, 0x00, // PS_info_length = 0
		0x00, 0x08, // elementary_stream_map_length = 8 (two entries)
		videoStreamType, 0xE0, 0x00, 0x00,
		audioStreamType, 0xC0, 0x00, 0x00,
	}
	ps.Write([]byte{byte(len(body) >> 8), byte(len(body))})
	ps.Write(body)

	// Video PES with PTS: standard layout (flags 0x80, hdrlen 5)
	es := append([]byte{0x00, 0x00, 0x00, 0x01}, videoNALU...)
	pesHdrLen := 3 + 5
	ps.Write([]byte{0x00, 0x00, 0x01, 0xE0})
	ps.Write([]byte{byte((pesHdrLen + len(es)) >> 8), byte(pesHdrLen + len(es))})
	ps.Write([]byte{0x80, 0x80, 0x05})
	ps.Write(encodePESPTS(ptsVideo))
	ps.Write(es)

	// Audio PES with PTS
	pesHdrLen = 3 + 5
	ps.Write([]byte{0x00, 0x00, 0x01, 0xC0})
	ps.Write([]byte{byte((pesHdrLen + len(audio)) >> 8), byte(pesHdrLen + len(audio))})
	ps.Write([]byte{0x80, 0x80, 0x05})
	ps.Write(encodePESPTS(ptsAudio))
	ps.Write(audio)

	_ = rtpVideo
	return ps.Bytes()
}

func TestPSDemuxAudioG711A(t *testing.T) {
	d := NewPSDemuxer()
	audio := bytes.Repeat([]byte{0x55}, 160) // 20ms of A-law
	ps := buildAudioPS([]byte{0x67, 0x42, 0x00, 0x1F}, audio, streamTypeH264, streamTypeG711A, 90000, 90160, 9000)

	nalus, err := d.FeedAU(ps, 9000, true)
	require.NoError(t, err)
	require.NotEmpty(t, nalus, "video NALUs must still be extracted")

	frames := d.DrainAudio()
	require.Len(t, frames, 1)
	require.Equal(t, AudioCodecG711A, frames[0].Codec)
	require.Equal(t, audio, frames[0].Data)
	require.Equal(t, 160, frames[0].Samples)
	// Clock offset: video PES PTS 90000 vs RTP 9000 → offset 81000.
	// Audio PES PTS 90160 → 90160-81000 = 9160 (= RTP 9000 + 2×20ms).
	require.Equal(t, int64(9160), frames[0].PTSTicks)
	require.Empty(t, d.DrainAudio(), "drain must reset the collector")
}

func TestPSDemuxAudioG711U(t *testing.T) {
	d := NewPSDemuxer()
	ps := buildAudioPS([]byte{0x67, 0x42}, []byte{1, 2, 3}, streamTypeH264, streamTypeG711U, 0, 160, 0)

	_, err := d.FeedAU(ps, 0, true)
	require.NoError(t, err)
	frames := d.DrainAudio()
	require.Len(t, frames, 1)
	require.Equal(t, AudioCodecG711U, frames[0].Codec)
}

func TestPSDemuxAudioAACADTS(t *testing.T) {
	d := NewPSDemuxer()

	// One ADTS frame: AAC-LC, 48kHz, stereo, 4 payload bytes.
	payload := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	adts := buildADTS(payload, 48000, 2)

	ps := buildAudioPS([]byte{0x67, 0x42}, adts, streamTypeH264, streamTypeAAC, 90000, 90213, 9000)
	_, err := d.FeedAU(ps, 9000, true)
	require.NoError(t, err)

	frames := d.DrainAudio()
	require.Len(t, frames, 1)
	require.Equal(t, AudioCodecAAC, frames[0].Codec)
	require.Equal(t, payload, frames[0].Data, "ADTS header must be stripped")
	require.Equal(t, []byte{0x11, 0x90}, frames[0].Config, "ASC: AAC-LC(AOT2) 48kHz(idx3) stereo")
	require.Equal(t, 1024, frames[0].Samples)
}

func TestPSDemuxAudioWithoutPSMSkipped(t *testing.T) {
	d := NewPSDemuxer()
	// Video-only PSM: audio stream never declared → audio frames are dropped.
	ps := buildAudioPS([]byte{0x67, 0x42}, []byte{1, 2, 3}, streamTypeH264, streamTypeG711A, 90000, 90160, 9000)
	// Overwrite the PSM audio entry's stream_type with a video type so the
	// audio codec lookup misses (audioCodecs[0xC0] == "").
	idx := bytes.Index(ps, []byte{streamTypeG711A, 0xC0, 0x00, 0x00})
	require.GreaterOrEqual(t, idx, 0)
	ps[idx] = streamTypeSVACA // unsupported audio type

	_, err := d.FeedAU(ps, 9000, true)
	require.NoError(t, err)
	require.Empty(t, d.DrainAudio())
}

func TestPSDemuxAudioStraddlingAUBoundary(t *testing.T) {
	d := NewPSDemuxer()
	audio := bytes.Repeat([]byte{0x77}, 100)
	ps := buildAudioPS([]byte{0x67, 0x42}, audio, streamTypeH264, streamTypeG711A, 90000, 90160, 9000)

	half := len(ps) / 2
	// First half (mid audio PES): partial — buffered, no frames emitted.
	_, err := d.FeedAU(ps[:half], 9000, false)
	require.NoError(t, err)
	require.Empty(t, d.DrainAudio(), "incomplete audio PES must not emit")

	// Second half completes the audio PES.
	_, err = d.FeedAU(ps[half:], 9000, true)
	require.NoError(t, err)
	frames := d.DrainAudio()
	require.Len(t, frames, 1)
	require.Equal(t, audio, frames[0].Data)
}

func TestSplitADTSMultipleFrames(t *testing.T) {
	a := buildADTS([]byte{1, 2}, 8000, 1)
	b := buildADTS([]byte{3, 4, 5}, 8000, 1)
	frames := splitADTS(append(append([]byte{}, a...), b...))
	require.Len(t, frames, 2)
	require.Equal(t, []byte{1, 2}, frames[0].data)
	require.Equal(t, []byte{3, 4, 5}, frames[1].data)
}

func TestSplitADTSRawPassthrough(t *testing.T) {
	raw := []byte{0x11, 0x22, 0x33}
	frames := splitADTS(raw)
	require.Len(t, frames, 1)
	require.Nil(t, frames[0].header)
	require.Equal(t, raw, frames[0].data)
}

func TestADTSToASC(t *testing.T) {
	// AAC-LC (profile 1), 44100 (idx 4), mono.
	hdr := buildADTS([]byte{0}, 44100, 1)
	asc := adtsToASC(hdr)
	require.Equal(t, []byte{0x12, 0x08}, asc)
	require.Equal(t, 44100, adtsSampleRate(hdr))
	require.Nil(t, adtsToASC(nil))
}

func TestAudioStreamTypesParsing(t *testing.T) {
	// psmData = body after 6-byte header (same layout as findVideoStreamType).
	psmData := []byte{
		0x02,       // version
		0xC0,       // reserved
		0x00, 0x00, // PS_info_length = 0
		0x00, 0x08, // elementary_stream_map_length
		streamTypeG711A, 0xC0, 0x00, 0x00,
		streamTypeG711U, 0xC1, 0x00, 0x00,
	}
	types := audioStreamTypes(psmData)
	require.Equal(t, byte(streamTypeG711A), types[0xC0])
	require.Equal(t, byte(streamTypeG711U), types[0xC1])
}

// buildADTS wraps payload in a 7-byte ADTS header for the given rate/channels.
func buildADTS(payload []byte, sampleRate int, channels int) []byte {
	freqIdx := byte(0xFF)
	for i, r := range adtsSampleRates {
		if r == sampleRate {
			freqIdx = byte(i)
			break
		}
	}
	if freqIdx == 0xFF {
		panic("unknown sample rate")
	}
	frameLen := 7 + len(payload)
	hdr := []byte{
		0xFF, 0xF1, // sync, MPEG-4, no CRC
		byte(1<<6 | int(freqIdx)<<2 | (channels>>2)&0x01), // profile=LC(1), freq, chans hi
		byte(channels<<6) | byte(frameLen>>11),            // chans lo + frame length hi
		byte(frameLen >> 3),                               // frame length mid
		byte(frameLen&0x07)<<5 | 0x1F,                     // frame length lo + buffer fullness hi
		0xFC,                                              // buffer fullness lo
	}
	return append(hdr, payload...)
}
