// SPDX-License-Identifier: MIT
//
// TUTK P2P transport adapted from go2rtc (https://github.com/AlexxIT/go2rtc)
// Copyright (c) go2rtc contributors
// Licensed under the MIT License.

package tutk

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsVideoCodec(t *testing.T) {
	t.Helper()
	tests := []struct {
		id   byte
		want bool
	}{
		{CodecMPEG4, true},
		{CodecH263, true},
		{CodecH264, true},
		{CodecMJPEG, true},
		{CodecH265, true},
		{0x4B, false}, // just below MPEG4
		{0x51, false}, // just above H265
		{0x00, false},
		{0xFF, false},
		{CodecAACRaw, false},  // audio
		{CodecPCMU, false},    // audio
		{CodecOpus, false},    // audio
	}

	for _, tt := range tests {
		got := IsVideoCodec(tt.id)
		require.Equal(t, tt.want, got, "IsVideoCodec(0x%02x) = %v, want %v", tt.id, got, tt.want)
	}
}

func TestIsAudioCodec(t *testing.T) {
	t.Helper()
	tests := []struct {
		id   byte
		want bool
	}{
		{CodecAACRaw, true},
		{CodecAACADTS, true},
		{CodecAACLATM, true},
		{CodecPCMU, true},
		{CodecPCMA, true},
		{CodecADPCM, true},
		{CodecPCML, true},
		{CodecSPEEX, true},
		{CodecMP3, true},
		{CodecG726, true},
		{CodecAACAlt, true},
		{CodecOpus, true},
		{0x85, false}, // just below AACRaw
		{0x93, false}, // just above Opus
		{0x00, false},
		{CodecH264, false},  // video
		{CodecH265, false},  // video
	}

	for _, tt := range tests {
		got := IsAudioCodec(tt.id)
		require.Equal(t, tt.want, got, "IsAudioCodec(0x%02x) = %v, want %v", tt.id, got, tt.want)
	}
}

func TestIsVideoCodecExhaustiveRange(t *testing.T) {
	t.Helper()
	// Video codec range: 0x4C (76) to 0x50 (80)
	for id := 0; id <= 255; id++ {
		b := byte(id)
		isVideo := IsVideoCodec(b)
		shouldBeVideo := b >= CodecMPEG4 && b <= CodecH265
		require.Equal(t, shouldBeVideo, isVideo, "IsVideoCodec(0x%02x) mismatch", b)
	}
}

func TestIsAudioCodecExhaustiveRange(t *testing.T) {
	t.Helper()
	// Audio codec range: 0x86 (134) to 0x92 (146)
	for id := 0; id <= 255; id++ {
		b := byte(id)
		isAudio := IsAudioCodec(b)
		shouldBeAudio := b >= CodecAACRaw && b <= CodecOpus
		require.Equal(t, shouldBeAudio, isAudio, "IsAudioCodec(0x%02x) mismatch", b)
	}
}

func TestGetSampleRateIndex(t *testing.T) {
	t.Helper()
	tests := []struct {
		sampleRate uint32
		want       uint8
	}{
		{8000, 0},
		{11025, 1},
		{12000, 2},
		{16000, 3},
		{22050, 4},
		{24000, 5},
		{32000, 6},
		{44100, 7},
		{48000, 8},
	}

	for _, tt := range tests {
		got := GetSampleRateIndex(tt.sampleRate)
		require.Equal(t, tt.want, got, "GetSampleRateIndex(%d) = %d, want %d", tt.sampleRate, got, tt.want)
	}
}

func TestGetSampleRateIndexDefault(t *testing.T) {
	t.Helper()
	// Unknown sample rate should default to index 3 (16kHz)
	got := GetSampleRateIndex(96000)
	require.Equal(t, uint8(3), got)
}

func TestGetSamplesPerFrame(t *testing.T) {
	t.Helper()
	tests := []struct {
		codecID byte
		want    uint32
	}{
		{CodecAACRaw, 1024},
		{CodecAACADTS, 1024},
		{CodecAACLATM, 1024},
		{CodecAACAlt, 1024},
		{CodecPCMU, 160},
		{CodecPCMA, 160},
		{CodecPCML, 160},
		{CodecADPCM, 160},
		{CodecSPEEX, 160},
		{CodecG726, 160},
		{CodecMP3, 1152},
		{CodecOpus, 960},
	}

	for _, tt := range tests {
		got := GetSamplesPerFrame(tt.codecID)
		require.Equal(t, tt.want, got, "GetSamplesPerFrame(0x%02x) = %d, want %d", tt.codecID, got, tt.want)
	}
}

func TestGetSamplesPerFrameDefault(t *testing.T) {
	t.Helper()
	// Unknown codec should default to 1024
	got := GetSamplesPerFrame(0xFF)
	require.Equal(t, uint32(1024), got)

	// Video codecs should default to 1024
	got = GetSamplesPerFrame(CodecH264)
	require.Equal(t, uint32(1024), got)
}
