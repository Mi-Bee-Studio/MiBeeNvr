package wsstream

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// Tests for the AudioCodecInfo wire format, focused on the backwards-
// compatible `Config` extension (the trailing {config_len}{config} block added
// to carry the AAC AudioSpecificConfig). The legacy 7-byte form (produced by
// older servers) is simulated by constructing packets by hand; the current
// encoder always appends the config_len field.

func TestEncodeAudioCodecInfo_G711NoConfig(t *testing.T) {
	t.Parallel()
	ci := &AudioCodecInfo{
		Codec:      AudioCodecG711Mu,
		SampleRate: 8000,
		Channels:   1,
	}
	encoded, err := EncodeAudioCodecInfo(ci)
	if err != nil {
		t.Fatalf("EncodeAudioCodecInfo: %v", err)
	}
	// type(1) + codec(1) + sample_rate(4) + channels(1) + config_len(2) = 9
	if len(encoded) != 9 {
		t.Fatalf("expected 9 bytes for G.711 (config_len=0), got %d", len(encoded))
	}
	if encoded[0] != MsgTypeAudioCodecInfo {
		t.Errorf("byte 0 = 0x%02x, want 0x%02x", encoded[0], MsgTypeAudioCodecInfo)
	}
	if encoded[1] != AudioCodecG711Mu {
		t.Errorf("byte 1 = 0x%02x, want 0x%02x", encoded[1], AudioCodecG711Mu)
	}
	if got := binary.BigEndian.Uint32(encoded[2:]); got != 8000 {
		t.Errorf("sample_rate = %d, want 8000", got)
	}
	if encoded[6] != 1 {
		t.Errorf("channels = %d, want 1", encoded[6])
	}
	if got := binary.BigEndian.Uint16(encoded[7:]); got != 0 {
		t.Errorf("config_len = %d, want 0", got)
	}
}

func TestEncodeAudioCodecInfo_AACWithConfig(t *testing.T) {
	t.Parallel()
	aasc := []byte{0x12, 0x10} // AAC-LC, 44100Hz, stereo
	ci := &AudioCodecInfo{
		Codec:      AudioCodecAAC,
		SampleRate: 44100,
		Channels:   2,
		Config:     aasc,
	}
	encoded, err := EncodeAudioCodecInfo(ci)
	if err != nil {
		t.Fatalf("EncodeAudioCodecInfo: %v", err)
	}
	// 9 header bytes + 2 config bytes
	if len(encoded) != 11 {
		t.Fatalf("expected 11 bytes for AAC with 2-byte AASC, got %d", len(encoded))
	}
	if encoded[1] != AudioCodecAAC {
		t.Errorf("codec byte = 0x%02x, want 0x%02x", encoded[1], AudioCodecAAC)
	}
	if got := binary.BigEndian.Uint16(encoded[7:]); got != 2 {
		t.Errorf("config_len = %d, want 2", got)
	}
	if !bytes.Equal(encoded[9:], aasc) {
		t.Errorf("config bytes = % X, want % X", encoded[9:], aasc)
	}
}

func TestEncodeAudioCodecInfo_NilRejected(t *testing.T) {
	t.Parallel()
	if _, err := EncodeAudioCodecInfo(nil); err == nil {
		t.Fatal("expected error for nil AudioCodecInfo")
	}
}

// TestEncodeAudioCodecInfo_Legacy7ByteBackwardsCompatible verifies that a
// packet produced by the *current* encoder is still decodable by treating the
// first 7 bytes as the legacy fixed-width form (the extension appends only
// trailing bytes). This pins the wire-format contract: a client that stops
// reading after byte 6 keeps working.
func TestEncodeAudioCodecInfo_Legacy7ByteBackwardsCompatible(t *testing.T) {
	t.Parallel()
	ci := &AudioCodecInfo{
		Codec:      AudioCodecG711A,
		SampleRate: 8000,
		Channels:   1,
	}
	encoded, err := EncodeAudioCodecInfo(ci)
	if err != nil {
		t.Fatalf("EncodeAudioCodecInfo: %v", err)
	}
	if len(encoded) < 7 {
		t.Fatalf("packet shorter than legacy 7-byte form: %d", len(encoded))
	}
	// Legacy decoder logic: only read the fixed 7-byte prefix.
	if encoded[0] != MsgTypeAudioCodecInfo {
		t.Errorf("legacy byte 0 = 0x%02x", encoded[0])
	}
	if encoded[1] != AudioCodecG711A {
		t.Errorf("legacy codec byte = 0x%02x", encoded[1])
	}
	if got := binary.BigEndian.Uint32(encoded[2:6]); got != 8000 {
		t.Errorf("legacy sample_rate = %d", got)
	}
	if encoded[6] != 1 {
		t.Errorf("legacy channels = %d", encoded[6])
	}
}

// ─── EncodeAudioFrame ─────────────────────────────────────────────────

func TestEncodeAudioFrame_AAC(t *testing.T) {
	t.Parallel()
	payload := []byte{0xde, 0xad, 0xbe, 0xef}
	af := &AudioFrameData{
		PTS:   123456,
		Codec: AudioCodecAAC,
		Data:  payload,
	}
	encoded, err := EncodeAudioFrame(af)
	if err != nil {
		t.Fatalf("EncodeAudioFrame: %v", err)
	}
	// type(1) + pts(8) + codec(1) + data_len(4) + data(4) = 18
	if len(encoded) != 18 {
		t.Fatalf("expected 18 bytes, got %d", len(encoded))
	}
	if encoded[0] != MsgTypeAudioFrame {
		t.Errorf("byte 0 = 0x%02x", encoded[0])
	}
	if got := binary.BigEndian.Uint64(encoded[1:9]); got != 123456 {
		t.Errorf("pts = %d, want 123456", got)
	}
	if encoded[9] != AudioCodecAAC {
		t.Errorf("codec = 0x%02x", encoded[9])
	}
	if got := binary.BigEndian.Uint32(encoded[10:14]); got != 4 {
		t.Errorf("data_len = %d, want 4", got)
	}
	if !bytes.Equal(encoded[14:], payload) {
		t.Errorf("data = % X, want % X", encoded[14:], payload)
	}
}

func TestEncodeAudioFrame_NilRejected(t *testing.T) {
	t.Parallel()
	if _, err := EncodeAudioFrame(nil); err == nil {
		t.Fatal("expected error for nil AudioFrameData")
	}
}
