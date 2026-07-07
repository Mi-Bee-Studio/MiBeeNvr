// SPDX-License-Identifier: MIT
//
// TUTK P2P transport adapted from go2rtc (https://github.com/AlexxIT/go2rtc)
// Copyright (c) go2rtc contributors
// Licensed under the MIT License.

package tutk

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// buildFrameInfo creates a 40-byte FrameInfo buffer positioned at the end of
// the given totalSize (totalSize must be >= 40).
func buildFrameInfo(t *testing.T, totalSize int, codec byte, flags byte, timestamp uint32, sessionID uint32, payloadSize uint32, frameNo uint32) []byte {
	t.Helper()
	b := make([]byte, 40)
	b[0] = codec
	b[2] = flags
	binary.LittleEndian.PutUint32(b[8:], timestamp)
	binary.LittleEndian.PutUint32(b[12:], sessionID)
	binary.LittleEndian.PutUint32(b[16:], payloadSize)
	binary.LittleEndian.PutUint32(b[20:], frameNo)
	return b
}

// buildVideoPacket builds a complete TUTK frame for ChannelIVideo with the given
// parameters. When frameType is EndSingle/EndMulti/EndExt a 40-byte FrameInfo is
// appended. For single-packet frames (PktTotal==1) the marker at the pktIdx
// position is set to 0x0028 (HasFrameInfo indicator).
func buildVideoPacket(t *testing.T, frameType byte, pktTotal uint16, pktIdx uint16, frameNo uint32, payload []byte) []byte {
	t.Helper()
	switch frameType {
	case FrameTypeStart, FrameTypeStartAlt:
		return buildVideoPacket36(t, frameType, pktTotal, pktIdx, frameNo, payload)
	default:
		return buildVideoPacket28(t, frameType, pktTotal, pktIdx, frameNo, payload)
	}
}

func buildVideoPacket28(t *testing.T, frameType byte, pktTotal uint16, pktIdx uint16, frameNo uint32, payload []byte) []byte {
	t.Helper()
	hasFI := IsEndFrame(frameType) || pktTotal == 1
	fiSize := 0
	if hasFI {
		fiSize = 40
	}

	totalLen := 28 + len(payload) + fiSize
	data := make([]byte, totalLen)

	data[0] = ChannelIVideo
	data[1] = frameType
	binary.LittleEndian.PutUint16(data[12:], pktTotal)

	if hasFI {
		binary.LittleEndian.PutUint16(data[14:], 0x0028)
	} else {
		binary.LittleEndian.PutUint16(data[14:], pktIdx)
	}

	binary.LittleEndian.PutUint16(data[16:], uint16(len(payload)))
	binary.LittleEndian.PutUint32(data[24:], frameNo)
	copy(data[28:], payload)

	if hasFI {
		fi := data[28+len(payload):]
		fi[0] = CodecH264
		fi[2] = 0x01 // keyframe
		binary.LittleEndian.PutUint32(fi[8:], 0)  // timestamp (set later if needed)
		binary.LittleEndian.PutUint32(fi[12:], 1) // SessionID
		binary.LittleEndian.PutUint32(fi[16:], uint32(len(payload)))
		binary.LittleEndian.PutUint32(fi[20:], frameNo)
	}

	return data
}

func buildVideoPacket36(t *testing.T, frameType byte, pktTotal uint16, pktIdx uint16, frameNo uint32, payload []byte) []byte {
	t.Helper()
	totalLen := 36 + len(payload)
	data := make([]byte, totalLen)

	data[0] = ChannelIVideo
	data[1] = frameType
	binary.LittleEndian.PutUint16(data[20:], pktTotal)
	binary.LittleEndian.PutUint16(data[22:], pktIdx)
	binary.LittleEndian.PutUint16(data[24:], uint16(len(payload)))
	binary.LittleEndian.PutUint32(data[32:], frameNo)
	copy(data[36:], payload)

	return data
}

// setFrameInfoTimestamp overwrites the timestamp field in an existing EndSingle/EndMulti
// packet's 40-byte FrameInfo trailer.
func setFrameInfoTimestamp(t *testing.T, data []byte, ts uint32) {
	t.Helper()
	if len(data) < 28+40 {
		return
	}
	fiStart := len(data) - 40
	binary.LittleEndian.PutUint32(data[fiStart+8:], ts)
}

// setFrameInfoPayloadSize overwrites the PayloadSize field in an existing
// EndSingle/EndMulti packet's 40-byte FrameInfo trailer.
func setFrameInfoPayloadSize(t *testing.T, data []byte, sz uint32) {
	t.Helper()
	if len(data) < 28+40 {
		return
	}
	fiStart := len(data) - 40
	binary.LittleEndian.PutUint32(data[fiStart+16:], sz)
}

// ---------------------------------------------------------------------------
// FrameInfo
// ---------------------------------------------------------------------------

func TestParseFrameInfo(t *testing.T) {
	data := make([]byte, 40)
	data[0] = CodecH264       // CodecID
	data[2] = 0x01            // Flags: keyframe
	data[3] = 1               // CamIndex
	data[4] = 2               // OnlineNum
	data[5] = 30              // FPS
	data[6] = 4               // ResTier
	data[7] = 100             // Bitrate
	binary.LittleEndian.PutUint32(data[8:], 12345)   // Timestamp
	binary.LittleEndian.PutUint32(data[12:], 42)     // SessionID
	binary.LittleEndian.PutUint32(data[16:], 64000)  // PayloadSize
	binary.LittleEndian.PutUint32(data[20:], 7)      // FrameNo

	fi := ParseFrameInfo(data)
	require.NotNil(t, fi)
	require.Equal(t, byte(CodecH264), fi.CodecID)
	require.Equal(t, uint8(0x01), fi.Flags)
	require.Equal(t, uint8(1), fi.CamIndex)
	require.Equal(t, uint8(2), fi.OnlineNum)
	require.Equal(t, uint8(30), fi.FPS)
	require.Equal(t, uint8(4), fi.ResTier)
	require.Equal(t, uint8(100), fi.Bitrate)
	require.Equal(t, uint32(12345), fi.Timestamp)
	require.Equal(t, uint32(42), fi.SessionID)
	require.Equal(t, uint32(64000), fi.PayloadSize)
	require.Equal(t, uint32(7), fi.FrameNo)
}

func TestParseFrameInfo_EdgeOffsets(t *testing.T) {
	// FrameInfo parsed from the LAST 40 bytes of the data slice.
	data := make([]byte, 100)
	data[60] = 0x55
	// Place a valid frameInfo at the end (last 40 bytes = offsets 60-99)
	copy(data[60:], make([]byte, 40))
	data[60] = CodecH264
	binary.LittleEndian.PutUint32(data[68:], 999)
	data[80] = 0x05

	fi := ParseFrameInfo(data)
	require.NotNil(t, fi)
	require.Equal(t, byte(CodecH264), fi.CodecID)
	require.Equal(t, uint32(999), fi.Timestamp)
}

func TestParseFrameInfo_ShortData(t *testing.T) {
	data := make([]byte, 39)
	fi := ParseFrameInfo(data)
	require.Nil(t, fi)
}

func TestParseFrameInfo_Empty(t *testing.T) {
	fi := ParseFrameInfo(nil)
	require.Nil(t, fi)
}

func TestFrameInfoIsKeyframe(t *testing.T) {
	tests := []struct {
		name   string
		flags  uint8
		want   bool
	}{
		{name: "flags_0x01_keyframe", flags: 0x01, want: true},
		{name: "flags_0x00_not_keyframe", flags: 0x00, want: false},
		{name: "flags_0x02_not_keyframe", flags: 0x02, want: false},
		{name: "flags_0x10_not_keyframe", flags: 0x10, want: false},
		{name: "flags_0xFF_not_keyframe", flags: 0xFF, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fi := &FrameInfo{Flags: tt.flags}
			got := fi.IsKeyframe()
			require.Equal(t, tt.want, got)
		})
	}
}

func TestFrameInfoSampleRate(t *testing.T) {
	tests := []struct {
		name  string
		flags uint8
		want  uint32
	}{
		{name: "index_0_8000Hz",  flags: 0x00, want: 8000},
		{name: "index_1_11025Hz", flags: 0x04, want: 11025},
		{name: "index_2_12000Hz", flags: 0x08, want: 12000},
		{name: "index_3_16000Hz", flags: 0x0C, want: 16000},
		{name: "index_4_22050Hz", flags: 0x10, want: 22050},
		{name: "index_5_24000Hz", flags: 0x14, want: 24000},
		{name: "index_6_32000Hz", flags: 0x18, want: 32000},
		{name: "index_7_44100Hz", flags: 0x1C, want: 44100},
		{name: "index_8_48000Hz", flags: 0x20, want: 48000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fi := &FrameInfo{Flags: tt.flags}
			got := fi.SampleRate()
			require.Equal(t, tt.want, got)
		})
	}
}

func TestFrameInfoSampleRate_OutOfRange(t *testing.T) {
	fi := &FrameInfo{Flags: 0xFF}
	// idx = (0xFF >> 2) & 0x0F = 0x3F & 0x0F = 15 → len(sampleRates)=9 → default 16000
	got := fi.SampleRate()
	require.Equal(t, uint32(16000), got)
}

func TestFrameInfoChannels(t *testing.T) {
	tests := []struct {
		name  string
		flags uint8
		want  uint8
	}{
		{name: "mono_bit0_0",  flags: 0x00, want: 1},
		{name: "stereo_bit0_1", flags: 0x01, want: 2},
		{name: "stereo_bit0_1_other_bits_set", flags: 0x0D, want: 2},
		{name: "mono_bit0_0_other_bits_set", flags: 0x0C, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fi := &FrameInfo{Flags: tt.flags}
			got := fi.Channels()
			require.Equal(t, tt.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// PacketHeader
// ---------------------------------------------------------------------------

func TestParsePacketHeader_StartFrame(t *testing.T) {
	data := make([]byte, 36)
	data[0] = ChannelIVideo
	data[1] = FrameTypeStart
	binary.LittleEndian.PutUint16(data[20:], 3)
	binary.LittleEndian.PutUint16(data[22:], 0)
	binary.LittleEndian.PutUint16(data[24:], 100)
	binary.LittleEndian.PutUint32(data[32:], 7)

	hdr := ParsePacketHeader(data)
	require.NotNil(t, hdr)
	require.Equal(t, byte(ChannelIVideo), hdr.Channel)
	require.Equal(t, byte(FrameTypeStart), hdr.FrameType)
	require.Equal(t, 36, hdr.HeaderSize)
	require.Equal(t, uint32(7), hdr.FrameNo)
	require.Equal(t, uint16(3), hdr.PktTotal)
	require.Equal(t, uint16(0), hdr.PktIdx)
	require.Equal(t, uint16(100), hdr.PayloadSize)
	require.False(t, hdr.HasFrameInfo)
}

func TestParsePacketHeader_StartAlt(t *testing.T) {
	data := make([]byte, 36)
	data[0] = ChannelIVideo
	data[1] = FrameTypeStartAlt
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 0)
	binary.LittleEndian.PutUint32(data[32:], 3)

	hdr := ParsePacketHeader(data)
	require.NotNil(t, hdr)
	require.Equal(t, 36, hdr.HeaderSize)
	require.Equal(t, byte(FrameTypeStartAlt), hdr.FrameType)
	require.Equal(t, uint16(1), hdr.PktTotal)
}

func TestParsePacketHeader_ContFrame(t *testing.T) {
	data := make([]byte, 28)
	data[0] = ChannelIVideo
	data[1] = FrameTypeCont
	binary.LittleEndian.PutUint16(data[12:], 3)
	binary.LittleEndian.PutUint16(data[14:], 1)
	binary.LittleEndian.PutUint16(data[16:], 50)
	binary.LittleEndian.PutUint32(data[24:], 7)

	hdr := ParsePacketHeader(data)
	require.NotNil(t, hdr)
	require.Equal(t, 28, hdr.HeaderSize)
	require.Equal(t, uint16(1), hdr.PktIdx)
	require.Equal(t, uint16(3), hdr.PktTotal)
	require.Equal(t, uint16(50), hdr.PayloadSize)
	require.False(t, hdr.HasFrameInfo)
}

func TestParsePacketHeader_ContAlt(t *testing.T) {
	data := make([]byte, 28)
	data[0] = ChannelIVideo
	data[1] = FrameTypeContAlt
	binary.LittleEndian.PutUint16(data[12:], 2)
	binary.LittleEndian.PutUint16(data[14:], 1)
	binary.LittleEndian.PutUint32(data[24:], 1)

	hdr := ParsePacketHeader(data)
	require.NotNil(t, hdr)
	require.Equal(t, 28, hdr.HeaderSize)
	require.Equal(t, byte(FrameTypeContAlt), hdr.FrameType)
	require.Equal(t, uint16(1), hdr.PktIdx)
}

func TestParsePacketHeader_EndSingle_WithFrameInfo(t *testing.T) {
	data := make([]byte, 28+40) // header + FrameInfo
	data[0] = ChannelIVideo
	data[1] = FrameTypeEndSingle
	binary.LittleEndian.PutUint16(data[12:], 1)
	binary.LittleEndian.PutUint16(data[14:], 0x0028) // HasFrameInfo marker
	binary.LittleEndian.PutUint32(data[24:], 7)

	hdr := ParsePacketHeader(data)
	require.NotNil(t, hdr)
	require.Equal(t, 28, hdr.HeaderSize)
	require.True(t, hdr.HasFrameInfo)
	// PktIdx should be set to PktTotal-1 when HasFrameInfo
	require.Equal(t, uint16(0), hdr.PktIdx)
	require.Equal(t, uint32(7), hdr.FrameNo)
}

func TestParsePacketHeader_EndSingle_NoFrameInfo(t *testing.T) {
	data := make([]byte, 28)
	data[0] = ChannelIVideo
	data[1] = FrameTypeEndSingle
	binary.LittleEndian.PutUint16(data[12:], 1)
	binary.LittleEndian.PutUint16(data[14:], 0) // no FrameInfo marker
	binary.LittleEndian.PutUint32(data[24:], 7)

	hdr := ParsePacketHeader(data)
	require.NotNil(t, hdr)
	require.False(t, hdr.HasFrameInfo)
	require.Equal(t, uint16(0), hdr.PktIdx)
}

func TestParsePacketHeader_EndMulti_WithFrameInfo(t *testing.T) {
	data := make([]byte, 28+40)
	data[0] = ChannelIVideo
	data[1] = FrameTypeEndMulti
	binary.LittleEndian.PutUint16(data[12:], 5)
	binary.LittleEndian.PutUint16(data[14:], 0x0028)
	binary.LittleEndian.PutUint32(data[24:], 10)

	hdr := ParsePacketHeader(data)
	require.NotNil(t, hdr)
	require.True(t, hdr.HasFrameInfo)
	require.Equal(t, uint16(4), hdr.PktIdx) // PktTotal-1
	require.Equal(t, uint32(10), hdr.FrameNo)
}

func TestParsePacketHeader_EndExt_WithFrameInfo(t *testing.T) {
	data := make([]byte, 36+40)
	data[0] = ChannelIVideo
	data[1] = FrameTypeEndExt
	binary.LittleEndian.PutUint16(data[20:], 2)
	binary.LittleEndian.PutUint16(data[22:], 0x0028)
	binary.LittleEndian.PutUint16(data[24:], 100)
	binary.LittleEndian.PutUint32(data[32:], 7)

	hdr := ParsePacketHeader(data)
	require.NotNil(t, hdr)
	require.Equal(t, 36, hdr.HeaderSize)
	require.True(t, hdr.HasFrameInfo)
	require.Equal(t, uint16(1), hdr.PktIdx) // PktTotal-1
}

func TestParsePacketHeader_ShortData(t *testing.T) {
	data := make([]byte, 27)
	hdr := ParsePacketHeader(data)
	require.Nil(t, hdr)
}

func TestParsePacketHeader_StartWithInsufficientData(t *testing.T) {
	data := make([]byte, 30) // between 28 and 35 — Start needs 36
	data[0] = ChannelIVideo
	data[1] = FrameTypeStart

	hdr := ParsePacketHeader(data)
	require.Nil(t, hdr)
}

func TestParsePacketHeader_EndExtWithInsufficientData(t *testing.T) {
	data := make([]byte, 30) // EndExt needs 36
	data[0] = ChannelIVideo
	data[1] = FrameTypeEndExt

	hdr := ParsePacketHeader(data)
	require.Nil(t, hdr)
}

func TestParsePacketHeader_SinglePktTotalAlsoSetsHasFrameInfo(t *testing.T) {
	// When PktTotal==1 and pktIdxOrMarker==0x0028, HasFrameInfo is true
	// even for frame types that are not IsEndFrame (e.g. Cont).
	data := make([]byte, 28)
	data[0] = ChannelIVideo
	data[1] = FrameTypeCont
	binary.LittleEndian.PutUint16(data[12:], 1) // PktTotal = 1
	binary.LittleEndian.PutUint16(data[14:], 0x0028)
	binary.LittleEndian.PutUint32(data[24:], 1)

	hdr := ParsePacketHeader(data)
	require.NotNil(t, hdr)
	require.True(t, hdr.HasFrameInfo)
	require.Equal(t, uint16(0), hdr.PktIdx)
}

func TestParsePacketHeader_NilSafety(t *testing.T) {
	hdr := ParsePacketHeader(nil)
	require.Nil(t, hdr)

	hdr = ParsePacketHeader([]byte{})
	require.Nil(t, hdr)
}

// ---------------------------------------------------------------------------
// Frame type detection
// ---------------------------------------------------------------------------

func TestIsStartFrame(t *testing.T) {
	tests := []struct {
		frameType byte
		want      bool
	}{
		{FrameTypeStart, true},
		{FrameTypeStartAlt, true},
		{FrameTypeCont, false},
		{FrameTypeContAlt, false},
		{FrameTypeEndSingle, false},
		{FrameTypeEndMulti, false},
		{FrameTypeEndExt, false},
		{0x02, false},
		{0x06, false},
	}
	for _, tt := range tests {
		got := IsStartFrame(tt.frameType)
		require.Equal(t, tt.want, got, "IsStartFrame(0x%02x)", tt.frameType)
	}
}

func TestIsEndFrame(t *testing.T) {
	tests := []struct {
		frameType byte
		want      bool
	}{
		{FrameTypeStart, false},
		{FrameTypeStartAlt, false},
		{FrameTypeCont, false},
		{FrameTypeContAlt, false},
		{FrameTypeEndSingle, true},
		{FrameTypeEndMulti, true},
		{FrameTypeEndExt, true},
		{0x02, false},
		{0x03, false},
	}
	for _, tt := range tests {
		got := IsEndFrame(tt.frameType)
		require.Equal(t, tt.want, got, "IsEndFrame(0x%02x)", tt.frameType)
	}
}

func TestIsContinuationFrame(t *testing.T) {
	tests := []struct {
		frameType byte
		want      bool
	}{
		{FrameTypeStart, false},
		{FrameTypeStartAlt, false},
		{FrameTypeCont, true},
		{FrameTypeContAlt, true},
		{FrameTypeEndSingle, false},
		{FrameTypeEndMulti, false},
		{FrameTypeEndExt, false},
		{0x02, false},
		{0x06, false},
	}
	for _, tt := range tests {
		got := IsContinuationFrame(tt.frameType)
		require.Equal(t, tt.want, got, "IsContinuationFrame(0x%02x)", tt.frameType)
	}
}

// ---------------------------------------------------------------------------
// isADTS
// ---------------------------------------------------------------------------

func TestIsADTS(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{name: "valid_sync_0xFF_F0", data: []byte{0xFF, 0xF0}, want: true},
		{name: "valid_sync_0xFF_F1", data: []byte{0xFF, 0xF1}, want: true},
		{name: "valid_sync_0xFF_FF", data: []byte{0xFF, 0xFF}, want: true},
		{name: "wrong_first_byte", data: []byte{0xFE, 0xF0}, want: false},
		{name: "wrong_top_nibble", data: []byte{0xFF, 0x0F}, want: false},
		{name: "zero_bytes", data: []byte{0x00, 0x00}, want: false},
		{name: "single_byte", data: []byte{0xFF}, want: false},
		{name: "nil", data: nil, want: false},
		{name: "empty", data: []byte{}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isADTS(tt.data)
			require.Equal(t, tt.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// parseAudioParams
// ---------------------------------------------------------------------------

func TestParseAudioParams_ADTS_44100Hz_Stereo(t *testing.T) {
	// Build a minimal ADTS payload (≥7 bytes) with sr_index=4 (44100 Hz),
	// channels=2 (stereo).
	payload := make([]byte, 7)
	payload[0] = 0xFF
	payload[1] = 0xF0                          // syncword top nibble
	payload[2] = 0x10 | (4 << 2)               // bits 5-2 = 0100 (sr_index=4)
	payload[3] = 0x80                           // bits 7-6 = 10 (channels bottom 2 bits)

	sr, ch := parseAudioParams(payload, nil)
	require.Equal(t, uint32(44100), sr)
	require.Equal(t, uint8(2), ch)
}

func TestParseAudioParams_ADTS_8000Hz_Mono(t *testing.T) {
	payload := make([]byte, 7)
	payload[0] = 0xFF
	payload[1] = 0xF0
	payload[2] = 0x00 | (11 << 2) // sr_index=11 (8000 Hz)
	payload[3] = 0x40            // channels: bits 7-6 = 01 (mono)

	sr, ch := parseAudioParams(payload, nil)
	require.Equal(t, uint32(8000), sr)
	require.Equal(t, uint8(1), ch)
}

func TestParseAudioParams_ADTS_48000Hz_Stereo(t *testing.T) {
	payload := make([]byte, 7)
	payload[0] = 0xFF
	payload[1] = 0xF0
	payload[2] = 0x00 | (3 << 2) // sr_index=3 (48000 Hz)
	payload[3] = 0x80             // channels: bits 7-6 = 10 (stereo)

	sr, ch := parseAudioParams(payload, nil)
	require.Equal(t, uint32(48000), sr)
	require.Equal(t, uint8(2), ch)
}

func TestParseAudioParams_ADTS_WithCRC(t *testing.T) {
	// protection_absent=0 (CRC present): byte 1 bit 0 = 0.
	payload := make([]byte, 7)
	payload[0] = 0xFF
	payload[1] = 0xF8                           // 0xF0 | (1<<3) | 0 → ID=1, protection_absent=0
	payload[2] = 0x00 | (4 << 2)                // sr_index=4 (44100 Hz)
	payload[3] = 0x80                            // channels: bits 7-6 = 10 (stereo)

	sr, ch := parseAudioParams(payload, nil)
	require.Equal(t, uint32(44100), sr)
	require.Equal(t, uint8(2), ch, "ADTS with CRC (protection_absent=0) should parse correctly")
}

func TestParseAudioParams_ADTS_ShortPayload(t *testing.T) {
	// ADTS with <7 bytes falls through to fi fallback.
	payload := []byte{0xFF, 0xF0, 0x10}

	fi := &FrameInfo{Flags: 0x0C} // sr_index=3 → 16000Hz, mono
	sr, ch := parseAudioParams(payload, fi)
	require.Equal(t, uint32(16000), sr)
	require.Equal(t, uint8(1), ch)
}

func TestParseAudioParams_NonADTS_UsesFI(t *testing.T) {
	payload := []byte{0x00, 0x01, 0x02, 0x03}
	fi := &FrameInfo{Flags: 0x1C} // sr_index=7 → 44100Hz, stereo (bit0=0 → mono)

	sr, ch := parseAudioParams(payload, fi)
	require.Equal(t, uint32(44100), sr)
	require.Equal(t, uint8(1), ch)
}

func TestParseAudioParams_NonADTS_NilFI(t *testing.T) {
	payload := []byte{0xAA, 0xBB}
	// nil fi + non-ADTS → 16000, 1
	sr, ch := parseAudioParams(payload, nil)
	require.Equal(t, uint32(16000), sr)
	require.Equal(t, uint8(1), ch)
}

func TestParseAudioParams_EmptyPayload(t *testing.T) {
	sr, ch := parseAudioParams(nil, &FrameInfo{Flags: 0x0C})
	require.Equal(t, uint32(16000), sr)
	require.Equal(t, uint8(1), ch)

	sr, ch = parseAudioParams([]byte{}, &FrameInfo{Flags: 0x0C})
	require.Equal(t, uint32(16000), sr)
	require.Equal(t, uint8(1), ch)
}

// ---------------------------------------------------------------------------
// tsTracker
// ---------------------------------------------------------------------------

func TestTsTracker_FirstUpdate(t *testing.T) {
	var tr tsTracker
	ts := tr.update(500000)
	require.Equal(t, uint64(0), ts, "first update returns 0")
	require.True(t, tr.firstTS)
	require.Equal(t, uint32(500000), tr.lastRawTS)
}

func TestTsTracker_Monotonic(t *testing.T) {
	var tr tsTracker
	tr.update(1000) // first → 0

	ts := tr.update(5000) // delta = 4000
	require.Equal(t, uint64(4000), ts)
	require.Equal(t, uint32(5000), tr.lastRawTS)
	require.Equal(t, uint64(4000), tr.accumUS)
}

func TestTsTracker_Accumulates(t *testing.T) {
	var tr tsTracker
	tr.update(0)     // first
	tr.update(1000)  // +1000
	tr.update(2000)  // +1000
	ts := tr.update(5000) // +3000
	require.Equal(t, uint64(5000), ts)
	require.Equal(t, uint64(5000), tr.accumUS)
}

func TestTsTracker_WrapAround(t *testing.T) {
	var tr tsTracker
	tr.update(999990) // first → 0, lastRawTS = 999990

	ts := tr.update(10) // wrapped: delta = (1000000-999990) + 10 = 20
	require.Equal(t, uint64(20), ts)
	require.Equal(t, uint32(10), tr.lastRawTS)
	require.Equal(t, uint64(20), tr.accumUS)
}

func TestTsTracker_WrapAroundMultiple(t *testing.T) {
	var tr tsTracker
	tr.update(999990)             // first
	tr.update(10)                 // wrap: 20
	require.Equal(t, uint64(20), tr.accumUS)

	ts := tr.update(20)           // +10
	require.Equal(t, uint64(30), ts)
	require.Equal(t, uint64(30), tr.accumUS)

	ts = tr.update(30)            // +10
	require.Equal(t, uint64(40), ts)
}

func TestTsTracker_NoWrapWhenRawTSAbovePrevious(t *testing.T) {
	var tr tsTracker
	tr.update(100)
	ts := tr.update(500)
	require.Equal(t, uint64(400), ts)
}

func TestTsTracker_ExactWrapPeriod(t *testing.T) {
	var tr tsTracker
	tr.update(0)      // first
	tr.update(tsWrapPeriod - 1) // delta = 999999
	require.Equal(t, uint64(999999), tr.accumUS)

	ts := tr.update(0) // wrapped: delta = (1000000 - 999999) + 0 = 1
	require.Equal(t, uint64(1000000), ts)
}

// ---------------------------------------------------------------------------
// FrameHandler — FrameInfo method integration with FrameInfo field access
// ---------------------------------------------------------------------------

func TestFrameHandler_New(t *testing.T) {
	h := NewFrameHandler(false)
	require.NotNil(t, h)
	require.NotNil(t, h.Recv())
	require.Empty(t, h.channels)
	require.False(t, h.closed)
}

func TestFrameHandler_RecvChannel(t *testing.T) {
	h := NewFrameHandler(false)
	ch := h.Recv()
	require.NotNil(t, ch)
	// Sending on the internal output channel should be readable via Recv().
	pkt := &Packet{Channel: ChannelIVideo}
	select {
	case h.output <- pkt:
	default:
	}
	select {
	case got := <-ch:
		require.Same(t, pkt, got)
	default:
		t.Fatal("expected to receive packet from Recv() channel")
	}
}

func TestFrameHandler_Handle_Nil(t *testing.T) {
	h := NewFrameHandler(false)
	// Must not panic.
	h.Handle(nil)
}

func TestFrameHandler_Handle_ShortData(t *testing.T) {
	h := NewFrameHandler(false)
	h.Handle([]byte{0x05}) // too short for any header
}

// ---------------------------------------------------------------------------
// FrameHandler — video single-packet frame
// ---------------------------------------------------------------------------

func TestFrameHandler_VideoSinglePacket(t *testing.T) {
	h := NewFrameHandler(false)
	payload := []byte{0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1E}
	data := buildVideoPacket(t, FrameTypeEndSingle, 1, 0, 1, payload)
	// Set a known timestamp in the FrameInfo trailer.
	setFrameInfoTimestamp(t, data, 50000)

	h.Handle(data)

	select {
	case pkt := <-h.Recv():
		require.Equal(t, byte(ChannelIVideo), pkt.Channel)
		require.Equal(t, byte(CodecH264), pkt.Codec)
		require.Equal(t, payload, pkt.Payload)
		require.True(t, pkt.IsKeyframe)
		require.Equal(t, uint32(1), pkt.FrameNo)
		// First frame: accum is 0, so RTP TS = 0.
		require.Equal(t, uint32(0), pkt.Timestamp)
	default:
		t.Fatal("expected packet from single-frame video Handle")
	}
}

// ---------------------------------------------------------------------------
// FrameHandler — video multi-packet reassembly (start + cont + end)
// ---------------------------------------------------------------------------

func TestFrameHandler_VideoMultiPacketReassembly(t *testing.T) {
	h := NewFrameHandler(false)
	frameNo := uint32(42)

	payload1 := []byte{0x00, 0x00, 0x01, 0x67}         // SPS
	payload2 := []byte{0x00, 0x00, 0x01, 0x68}         // PPS
	payload3 := []byte{0x00, 0x00, 0x01, 0x65, 0x88}   // IDR slice

	// Packet 1: Start
	pkt1 := buildVideoPacket(t, FrameTypeStart, 3, 0, frameNo, payload1)
	// Packet 2: Cont
	pkt2 := buildVideoPacket(t, FrameTypeCont, 3, 1, frameNo, payload2)
	// Packet 3: EndMulti with FrameInfo
	pkt3 := buildVideoPacket(t, FrameTypeEndMulti, 3, 2, frameNo, payload3)
	setFrameInfoTimestamp(t, pkt3, 12345)
	setFrameInfoPayloadSize(t, pkt3, uint32(len(payload1)+len(payload2)+len(payload3)))

	h.Handle(pkt1)
	h.Handle(pkt2)
	h.Handle(pkt3)

	select {
	case pkt := <-h.Recv():
		require.Equal(t, byte(ChannelIVideo), pkt.Channel)
		require.Equal(t, byte(CodecH264), pkt.Codec)
		require.True(t, pkt.IsKeyframe)
		require.Equal(t, frameNo, pkt.FrameNo)

		// Accumulated payload = payload1 + payload2 + payload3
		expectedPayload := append(append(payload1, payload2...), payload3...)
		require.Equal(t, expectedPayload, pkt.Payload)

		// First frame: accum is 0, so RTP TS = 0.
		require.Equal(t, uint32(0), pkt.Timestamp)
	default:
		t.Fatal("expected reassembled packet after start+cont+end")
	}

	// Verify no extra packet is queued.
	select {
	case <-h.Recv():
		t.Fatal("unexpected extra packet")
	default:
	}
}

// ---------------------------------------------------------------------------
// FrameHandler — audio single packet
// ---------------------------------------------------------------------------

func TestFrameHandler_AudioSinglePacket(t *testing.T) {
	h := NewFrameHandler(false)

	// Build an audio packet with PCMU codec.
	payload := []byte{0x80, 0x00, 0x00, 0x00, 0x00} // dummy PCMU frame
	data := make([]byte, 28+len(payload)+40)
	data[0] = ChannelAudio
	data[1] = FrameTypeEndSingle
	binary.LittleEndian.PutUint16(data[12:], 1)    // PktTotal
	binary.LittleEndian.PutUint16(data[14:], 0x0028) // FrameInfo marker
	binary.LittleEndian.PutUint16(data[16:], uint16(len(payload)))
	binary.LittleEndian.PutUint32(data[24:], 5)    // FrameNo
	copy(data[28:], payload)

	// FrameInfo trailer (last 40 bytes)
	fi := data[28+len(payload):]
	fi[0] = CodecPCMU
	fi[2] = 0x0C // sr_index=3 (16000Hz), mono (bit0=0)
	binary.LittleEndian.PutUint32(fi[8:], 1000)    // Timestamp
	binary.LittleEndian.PutUint32(fi[12:], 1)      // SessionID
	binary.LittleEndian.PutUint32(fi[16:], uint32(len(payload)))
	binary.LittleEndian.PutUint32(fi[20:], 5)      // FrameNo

	h.Handle(data)

	select {
	case pkt := <-h.Recv():
		require.Equal(t, byte(ChannelAudio), pkt.Channel)
		require.Equal(t, byte(CodecPCMU), pkt.Codec)
		require.Equal(t, payload, pkt.Payload)
		require.Equal(t, uint32(5), pkt.FrameNo)
		require.Equal(t, uint32(16000), pkt.SampleRate)
		require.Equal(t, uint8(1), pkt.Channels)
		// First audio frame: audioTS accum is 0.
		require.Equal(t, uint32(0), pkt.Timestamp)
	default:
		t.Fatal("expected audio packet")
	}
}

// ---------------------------------------------------------------------------
// FrameHandler — audio with AAC codec (uses parseAudioParams)
// ---------------------------------------------------------------------------

func TestFrameHandler_AudioAACADTS(t *testing.T) {
	h := NewFrameHandler(false)

	// Build a 7-byte ADTS header for 44100Hz stereo.
	adts := make([]byte, 7)
	adts[0] = 0xFF
	adts[1] = 0xF0
	adts[2] = 0x10 | (4 << 2) // sr_index=4 (44100)
	adts[3] = 0x80             // channels stereo

	payload := append(adts, []byte{0xAA, 0xBB, 0xCC}...) // ADTS + raw AAC data

	data := make([]byte, 28+len(payload)+40)
	data[0] = ChannelAudio
	data[1] = FrameTypeEndSingle
	binary.LittleEndian.PutUint16(data[12:], 1)
	binary.LittleEndian.PutUint16(data[14:], 0x0028)
	binary.LittleEndian.PutUint16(data[16:], uint16(len(payload)))
	binary.LittleEndian.PutUint32(data[24:], 1)
	copy(data[28:], payload)

	fi := data[28+len(payload):]
	fi[0] = CodecAACADTS
	fi[2] = 0x0C // sr_index=3 (16000Hz) — should be overridden by ADTS parsing
	binary.LittleEndian.PutUint32(fi[8:], 2000)
	binary.LittleEndian.PutUint32(fi[12:], 1)
	binary.LittleEndian.PutUint32(fi[16:], uint32(len(payload)))
	binary.LittleEndian.PutUint32(fi[20:], 1)

	h.Handle(data)

	select {
	case pkt := <-h.Recv():
		require.Equal(t, byte(ChannelAudio), pkt.Channel)
		require.Equal(t, byte(CodecAACADTS), pkt.Codec)
		// ADTS-sourced sample rate should override FI's sample rate
		require.Equal(t, uint32(44100), pkt.SampleRate)
		require.Equal(t, uint8(2), pkt.Channels)
		// First AAC frame: audioTS accum is 0.
		require.Equal(t, uint32(0), pkt.Timestamp)
	default:
		t.Fatal("expected AAC ADTS audio packet")
	}
}

// ---------------------------------------------------------------------------
// FrameHandler — Close
// ---------------------------------------------------------------------------

func TestFrameHandler_Close(t *testing.T) {
	h := NewFrameHandler(false)
	ch := h.Recv()

	h.Close()
	// Channel should be closed; reading should return zero value immediately.
	_, ok := <-ch
	require.False(t, ok, "channel should be closed")
}

func TestFrameHandler_Close_Idempotent(t *testing.T) {
	h := NewFrameHandler(false)
	h.Close()
	h.Close() // must not panic
}

func TestFrameHandler_Close_DropsQueuedPackets(t *testing.T) {
	h := NewFrameHandler(false)
	h.Close()

	// Handle after Close must not panic; queue() checks h.closed and drops silently.
	payload := []byte{0x00, 0x00, 0x01, 0x67}
	data := buildVideoPacket(t, FrameTypeEndSingle, 1, 0, 1, payload)
	h.Handle(data)
}

// ---------------------------------------------------------------------------
// FrameHandler — edge cases
// ---------------------------------------------------------------------------

func TestFrameHandler_PayloadSizeMismatch(t *testing.T) {
	h := NewFrameHandler(false)

	payload := []byte{0x00, 0x00, 0x01, 0x67}
	data := buildVideoPacket(t, FrameTypeEndSingle, 1, 0, 1, payload)
	// Set PayloadSize in FrameInfo to a value that doesn't match.
	setFrameInfoPayloadSize(t, data, 9999) // actual is 4

	h.Handle(data)

	// No packet should be emitted because PayloadSize doesn't match.
	select {
	case <-h.Recv():
		t.Fatal("should not emit packet on PayloadSize mismatch")
	default:
	}
}

func TestFrameHandler_PayloadSizeZeroSkipsCheck(t *testing.T) {
	h := NewFrameHandler(false)

	payload := []byte{0x00, 0x00, 0x01, 0x67}
	data := buildVideoPacket(t, FrameTypeEndSingle, 1, 0, 1, payload)
	// PayloadSize=0 in FrameInfo skips the size check → should produce a packet.
	setFrameInfoPayloadSize(t, data, 0)

	h.Handle(data)

	select {
	case pkt := <-h.Recv():
		require.NotNil(t, pkt)
		require.Equal(t, payload, pkt.Payload)
	default:
		t.Fatal("expected packet when PayloadSize==0 (check skipped)")
	}
}

func TestFrameHandler_OutOfOrderPacketResets(t *testing.T) {
	h := NewFrameHandler(false)

	frameNo := uint32(10)

	// Send packet 1 (start)
	pkt0 := buildVideoPacket(t, FrameTypeStart, 2, 0, frameNo, []byte{0x01})
	h.Handle(pkt0)

	// Send packet with PktIdx=1 when waitSeq=0 (only start received, waitSeq=1)
	// Actually, after pkt0 waitSeq=1. Let's set PktIdx=5 to trigger OOO reset.
	pktBad := buildVideoPacket(t, FrameTypeCont, 2, 5, frameNo, []byte{0x02})
	h.Handle(pktBad)

	// Frame was reset because of OOO. Send a complete single-packet frame.
	newFrame := buildVideoPacket(t, FrameTypeEndSingle, 1, 0, 20, []byte{0x03, 0x04})
	setFrameInfoTimestamp(t, newFrame, 100)

	h.Handle(newFrame)

	select {
	case pkt := <-h.Recv():
		require.Equal(t, uint32(20), pkt.FrameNo)
		require.Equal(t, []byte{0x03, 0x04}, pkt.Payload)
	default:
		t.Fatal("expected packet from new frame after OOO reset")
	}
}

func TestFrameHandler_NewFrameResetsPrevious(t *testing.T) {
	h := NewFrameHandler(false)

	// Start frame 1 (only first packet, incomplete).
	pkt1 := buildVideoPacket(t, FrameTypeStart, 2, 0, 1, []byte{0x01})
	h.Handle(pkt1)

	// A new frame number arrives before the previous one is complete.
	// This should reset the channel state and start assembling frame 2.
	pkt2start := buildVideoPacket(t, FrameTypeEndSingle, 1, 0, 2, []byte{0x02})
	setFrameInfoTimestamp(t, pkt2start, 5000)
	h.Handle(pkt2start)

	// frame 2 is a single-packet frame → should complete.
	select {
	case pkt := <-h.Recv():
		require.Equal(t, uint32(2), pkt.FrameNo)
		require.Equal(t, []byte{0x02}, pkt.Payload)
	default:
		t.Fatal("expected packet from new frame after reset")
	}
}

func TestFrameHandler_EmptyPayload(t *testing.T) {
	h := NewFrameHandler(false)

	// Single-packet video frame with empty payload.
	data := buildVideoPacket(t, FrameTypeEndSingle, 1, 0, 1, []byte{})
	setFrameInfoPayloadSize(t, data, 0)

	h.Handle(data)

	// Empty payload frames are dropped (len(cs.waitData)==0 guard).
	select {
	case <-h.Recv():
		t.Fatal("should not emit packet with empty payload")
	default:
	}
}

func TestFrameHandler_MultipleVideoFramesSequential(t *testing.T) {
	h := NewFrameHandler(false)

	for i := uint32(1); i <= 3; i++ {
		payload := []byte{byte(i)}
		data := buildVideoPacket(t, FrameTypeEndSingle, 1, 0, i, payload)
		setFrameInfoTimestamp(t, data, i*10000)
		setFrameInfoPayloadSize(t, data, 1)

		h.Handle(data)

		select {
		case pkt := <-h.Recv():
			require.Equal(t, i, pkt.FrameNo)
			require.Equal(t, payload, pkt.Payload)
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for frame %d", i)
		}
	}
}

func TestFrameHandler_HandleAudioCodecOnVideoChannel(t *testing.T) {
	// Sending an audio codec (PCMU) on a video channel should be handled
	// by handleVideo (the channel is video, not audio).
	h := NewFrameHandler(false)

	payload := []byte{0x01, 0x02}
	data := make([]byte, 28+len(payload)+40)
	data[0] = ChannelIVideo
	data[1] = FrameTypeEndSingle
	binary.LittleEndian.PutUint16(data[12:], 1)
	binary.LittleEndian.PutUint16(data[14:], 0x0028)
	binary.LittleEndian.PutUint16(data[16:], uint16(len(payload)))
	binary.LittleEndian.PutUint32(data[24:], 1)
	copy(data[28:], payload)

	fi := data[28+len(payload):]
	fi[0] = CodecPCMU // audio codec on video channel
	fi[2] = 0x01      // keyframe flag
	binary.LittleEndian.PutUint32(fi[8:], 100)
	binary.LittleEndian.PutUint32(fi[12:], 1)
	binary.LittleEndian.PutUint32(fi[16:], uint32(len(payload)))
	binary.LittleEndian.PutUint32(fi[20:], 1)

	// With an audio codec on a video channel, extractPayload will not
	// validate it as a video codec (IsVideoCodec returns false), so
	// fi will be nil → frameInfo stays nil → frame is never completed.
	h.Handle(data)

	select {
	case <-h.Recv():
		t.Fatal("should not produce packet: audio codec on video channel")
	default:
	}
}

func TestFrameHandler_MissingFrameInfo(t *testing.T) {
	h := NewFrameHandler(false)

	// Build a multi-packet frame where the end packet does NOT carry FrameInfo
	// (pktIdxOrMarker != 0x0028). FrameInfo stays nil → frame never completes.
	pkt1 := buildVideoPacket(t, FrameTypeStart, 2, 0, 1, []byte{0x01})
	pkt2 := buildVideoPacket(t, FrameTypeEndMulti, 2, 1, 1, []byte{0x02})
	// pkt2 was built by buildVideoPacket28 which sets 0x0028. Override
	// pktIdx at offset 14 to a non-0x0028 value.
	binary.LittleEndian.PutUint16(pkt2[14:], 1)

	h.Handle(pkt1)
	h.Handle(pkt2)

	// Frame never completes → no packet.
	select {
	case <-h.Recv():
		t.Fatal("should not produce packet without FrameInfo")
	default:
	}
}

// ---------------------------------------------------------------------------
// dumpHex
// ---------------------------------------------------------------------------

func TestDumpHex(t *testing.T) {
	fi := &FrameInfo{
		CodecID:     CodecH264,
		Flags:       0x01,
		CamIndex:    1,
		OnlineNum:   2,
		FPS:         30,
		ResTier:     4,
		Bitrate:     100,
		Timestamp:   0x12345678,
		SessionID:   0xABCD,
		PayloadSize: 0x1000,
		FrameNo:     42,
	}

	result := dumpHex(fi)
	require.NotEmpty(t, result)
	require.Contains(t, result, "4e") // CodecH264 = 0x4E
}

// ---------------------------------------------------------------------------
// Channel states are created lazily
// ---------------------------------------------------------------------------

func TestFrameHandler_LazyChannelState(t *testing.T) {
	h := NewFrameHandler(false)
	require.Empty(t, h.channels)

	pkt := buildVideoPacket(t, FrameTypeEndSingle, 1, 0, 1, []byte{0x01})
	setFrameInfoTimestamp(t, pkt, 100)
	h.Handle(pkt)

	// A channel state should now exist for ChannelIVideo.
	require.Contains(t, h.channels, byte(ChannelIVideo))
	require.NotNil(t, h.channels[byte(ChannelIVideo)])
}

func TestFrameHandler_PVideoChannel(t *testing.T) {
	// PVideo (0x07) should be treated as video channel.
	h := NewFrameHandler(false)

	payload := []byte{0x00, 0x00, 0x01, 0x67}
	data := make([]byte, 28+len(payload)+40)
	data[0] = ChannelPVideo
	data[1] = FrameTypeEndSingle
	binary.LittleEndian.PutUint16(data[12:], 1)
	binary.LittleEndian.PutUint16(data[14:], 0x0028)
	binary.LittleEndian.PutUint16(data[16:], uint16(len(payload)))
	binary.LittleEndian.PutUint32(data[24:], 1)
	copy(data[28:], payload)

	fi := data[28+len(payload):]
	fi[0] = CodecH264
	fi[2] = 0x01
	binary.LittleEndian.PutUint32(fi[8:], 100)
	binary.LittleEndian.PutUint32(fi[12:], 1)
	binary.LittleEndian.PutUint32(fi[16:], uint32(len(payload)))
	binary.LittleEndian.PutUint32(fi[20:], 1)

	h.Handle(data)

	select {
	case pkt := <-h.Recv():
		require.Equal(t, byte(ChannelPVideo), pkt.Channel)
		require.Equal(t, byte(CodecH264), pkt.Codec)
		require.Equal(t, payload, pkt.Payload)
	default:
		t.Fatal("expected packet on PVideo channel")
	}
}

// ---------------------------------------------------------------------------
// FrameInfo Method tests for completeness
// ---------------------------------------------------------------------------

func TestFrameInfo_SampleRate_AllValidIndices(t *testing.T) {
	expected := []uint32{8000, 11025, 12000, 16000, 22050, 24000, 32000, 44100, 48000}
	for idx, want := range expected {
		flags := uint8(idx << 2) // shift index to bits 5-2
		fi := &FrameInfo{Flags: flags}
		got := fi.SampleRate()
		require.Equal(t, want, got, "SampleRate for index %d", idx)
	}
}

func TestFrameInfo_Channels_EdgeCases(t *testing.T) {
	// Bit 0 of Flags determines mono/stereo.
	tests := []struct {
		flags uint8
		want  uint8
	}{
		{0x00, 1},
		{0x01, 2},
		{0x02, 1}, // bit 1 set, bit 0 clear
		{0x03, 2}, // both bits 0 and 1 set
		{0x05, 2}, // bit 0 and bit 2 set
	}
	for _, tt := range tests {
		fi := &FrameInfo{Flags: tt.flags}
		got := fi.Channels()
		require.Equal(t, tt.want, got, "Channels for Flags=0x%02x", tt.flags)
	}
}
