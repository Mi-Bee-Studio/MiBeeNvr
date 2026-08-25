// SPDX-License-Identifier: MIT
//
// Regression tests for issue #520: the xiaomi recorder must honor the
// per-camera AudioInRecordings gate when adding the MP4 audio track, matching
// the direct-RTSP and ONVIF recorder paths (#496 follow-up). Live preview and
// the audio trigger are NOT gated and have separate tests.

package xiaomi

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// feedH264IDR drives the H264 segment-creation path far enough to run the
// audio-track decision, with a detected audio codec armed.
func feedH264IDR(t *testing.T, r *XiaomiRecorder) {
	t.Helper()
	r.codec = model.FormatH264
	r.codecOK = true
	r.cfg.AudioEnabled = true
	r.audioCodecID = missCodecPCMU
	r.store = newRecordingSegmentStore(t)

	var lastTS uint64
	r.processH264NALU([]byte{0x67, 0x42, 0xc0, 0x1e}, 0, &lastTS)
	r.processH264NALU([]byte{0x68, 0xce, 0x38, 0x80}, 0, &lastTS)
	r.processH264NALU([]byte{0x65, 0x01, 0x02, 0x03}, 0, &lastTS)
	require.NotNil(t, r.muxer)
}

// feedH265IDR drives the H265 segment-creation path far enough to run the
// audio-track decision, with a detected audio codec armed.
func feedH265IDR(t *testing.T, r *XiaomiRecorder) {
	t.Helper()
	r.codec = model.FormatH265
	r.codecOK = true
	r.cfg.AudioEnabled = true
	r.audioCodecID = missCodecPCMU
	r.store = newRecordingSegmentStore(t)

	var lastTS uint64
	r.processH265NALU([]byte{0x26, 0x01, 0x02, 0x03}, 0, &lastTS)
	require.NotNil(t, r.muxer)
}

func TestXiaomiAudioTrackGatedByAudioInRecordings_H264(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	r.cfg.AudioInRecordings = false
	feedH264IDR(t, r)
	require.NotNil(t, r.muxer)
	require.Zero(t, r.audioTrackID, "audio track must NOT be added when audio_in_recordings is false")
}

func TestXiaomiAudioTrackKeptWhenAudioInRecordings_H264(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	r.cfg.AudioInRecordings = true
	feedH264IDR(t, r)
	require.NotZero(t, r.audioTrackID, "audio track must be added when audio_in_recordings is true")
}

func TestXiaomiAudioTrackGatedByAudioInRecordings_H265(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	r.vps = []byte{0x40, 0x01, 0x0c}
	r.sps = []byte{0x42, 0x01, 0x01}
	r.pps = []byte{0x44, 0x01, 0xc1}
	r.cfg.AudioInRecordings = false
	feedH265IDR(t, r)
	require.Zero(t, r.audioTrackID, "audio track must NOT be added when audio_in_recordings is false")
}

func TestXiaomiAudioTrackKeptWhenAudioInRecordings_H265(t *testing.T) {
	t.Helper()
	r := makeTestRecorder(t)
	r.vps = []byte{0x40, 0x01, 0x0c}
	r.sps = []byte{0x42, 0x01, 0x01}
	r.pps = []byte{0x44, 0x01, 0xc1}
	r.cfg.AudioInRecordings = true
	feedH265IDR(t, r)
	require.NotZero(t, r.audioTrackID, "audio track must be added when audio_in_recordings is true")
}
