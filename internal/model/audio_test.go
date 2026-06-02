package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAudioFormatConstants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value Format
		want  string
	}{
		{"aac", FormatAAC, "aac"},
		{"g711", FormatG711, "g711"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, string(tt.value))
		})
	}
}

func TestAudioCodecConstants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value AudioCodec
		want  string
	}{
		{"aac", AudioAAC, "aac"},
		{"g711", AudioG711, "g711"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, string(tt.value))
		})
	}
}

func TestAudioFrameStruct(t *testing.T) {
	t.Parallel()
	// Test zero value
	var f AudioFrame
	require.Equal(t, int64(0), f.PTS)
	require.Equal(t, AudioCodec(""), f.Codec)
	require.Nil(t, f.Data)

	// Test populated value
	f = AudioFrame{
		PTS:   12345,
		Codec: AudioAAC,
		Data:  []byte{0x00, 0x01, 0x02},
	}
	require.Equal(t, int64(12345), f.PTS)
	require.Equal(t, AudioAAC, f.Codec)
	require.Equal(t, []byte{0x00, 0x01, 0x02}, f.Data)

	// Test G711 frame
	f2 := AudioFrame{
		PTS:   67890,
		Codec: AudioG711,
		Data:  []byte{0xff, 0x7f},
	}
	require.Equal(t, int64(67890), f2.PTS)
	require.Equal(t, AudioG711, f2.Codec)
	require.Equal(t, []byte{0xff, 0x7f}, f2.Data)
}
