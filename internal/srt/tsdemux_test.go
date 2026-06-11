package srt

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseStreamID(t *testing.T) {
	t.Helper()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"empty", "", ""},
		{"plain camera ID", "front-door", "front-door"},
		{"query string", "camera_id=front-door", "front-door"},
		{"path format", "/live/front-door", "front-door"},
		{"path multi segment", "/live/cameras/front-door", "front-door"},
		{"publish prefix", "publish:/live/front-door", "front-door"},
		{"publish prefix plain", "publish:front-door", "front-door"},
		{"kebab case", "my-camera-1", "my-camera-1"},
		{"query with extra params", "camera_id=garden&token=abc123", "garden"},
		{"path single segment", "/front-door", "front-door"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Helper()
			result := ParseStreamID(tt.input)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestIsKeyframeNALU(t *testing.T) {
	t.Helper()

	tests := []struct {
		name     string
		data     []byte
		expected bool
	}{
		{"empty", nil, false},
		{"IDR", []byte{0x65}, true}, // type 5 (0x65 & 0x1F = 5)
		{"non-IDR", []byte{0x41}, false}, // type 1 (0x41 & 0x1F = 1)
		{"SPS", []byte{0x67}, false}, // type 7
		{"PPS", []byte{0x68}, false}, // type 8
		{"SEI", []byte{0x06}, false}, // type 6
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Helper()
			result := isKeyframeNALU(tt.data)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestFindStartCodes(t *testing.T) {
	t.Helper()

	data := []byte{
		0x00, 0x00, 0x00, 0x01, // start code (4-byte)
		0x67, 0x42, 0x00, 0x0a, // SPS NALU
		0x00, 0x00, 0x00, 0x01, // start code (4-byte)
		0x68, 0xce, 0x38, 0x80, // PPS NALU
		0x00, 0x00, 0x00, 0x01, // start code (4-byte)
		0x65, 0xb8, 0x00, 0x04, // IDR NALU
	}

	positions := findStartCodes(data)
	require.Len(t, positions, 3)
	require.Equal(t, 0, positions[0])
	require.Equal(t, 8, positions[1])
	require.Equal(t, 16, positions[2])
}

func TestExtractNALUs(t *testing.T) {
	t.Helper()

	// Build a test bitstream with SPS + PPS + IDR
	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xf8, 0x41, 0xa2}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	idr := []byte{0x65, 0xb8, 0x00, 0x04, 0x00, 0x00, 0x05}

	// Construct Annex B stream
	var stream []byte
	stream = append(stream, 0x00, 0x00, 0x00, 0x01)
	stream = append(stream, sps...)
	stream = append(stream, 0x00, 0x00, 0x00, 0x01)
	stream = append(stream, pps...)
	stream = append(stream, 0x00, 0x00, 0x00, 0x01)
	stream = append(stream, idr...)

	nalus := extractNALUs(stream, 90000)
	require.Len(t, nalus, 3)

	require.Equal(t, uint8(7), nalus[0].Type) // SPS
	require.Equal(t, sps, nalus[0].Data)

	require.Equal(t, uint8(8), nalus[1].Type) // PPS
	require.Equal(t, pps, nalus[1].Data)

	require.Equal(t, uint8(5), nalus[2].Type) // IDR
	require.Equal(t, idr, nalus[2].Data)

	// All should have the same PTS
	require.Equal(t, int64(90000), nalus[0].PTS)
	require.Equal(t, int64(90000), nalus[1].PTS)
	require.Equal(t, int64(90000), nalus[2].PTS)
}

func TestExtractNALUsThreeByteStartCode(t *testing.T) {
	t.Helper()

	// Test with 3-byte start codes
	sps := []byte{0x67, 0x42}
	idr := []byte{0x65, 0xb8}

	var stream []byte
	stream = append(stream, 0x00, 0x00, 0x01)
	stream = append(stream, sps...)
	stream = append(stream, 0x00, 0x00, 0x01)
	stream = append(stream, idr...)

	nalus := extractNALUs(stream, 0)
	require.Len(t, nalus, 2)
	require.Equal(t, sps, nalus[0].Data)
	require.Equal(t, idr, nalus[1].Data)
}

func TestAssembleAccessUnit(t *testing.T) {
	t.Helper()

	// SPS + PPS + IDR should form one access unit
	nalus := []NALU{
		{PTS: 90000, Data: []byte{0x67, 0x42}, Type: 7}, // SPS
		{PTS: 90000, Data: []byte{0x68, 0xce}, Type: 8}, // PPS
		{PTS: 90000, Data: []byte{0x65, 0xb8}, Type: 5}, // IDR
	}

	frames := assembleAccessUnit(nalus)
	require.Len(t, frames, 1)
	require.Len(t, frames[0], 3)
}

func TestAssembleAccessUnitMultipleFrames(t *testing.T) {
	t.Helper()

	// Two frames: SPS+PPS+IDR, then SPS+PPS+IDR
	nalus := []NALU{
		{PTS: 90000, Data: []byte{0x67, 0x01}, Type: 7}, // SPS
		{PTS: 90000, Data: []byte{0x68, 0x01}, Type: 8}, // PPS
		{PTS: 90000, Data: []byte{0x65, 0x01}, Type: 5}, // IDR
		{PTS: 94500, Data: []byte{0x41, 0x01}, Type: 1}, // non-IDR
		{PTS: 99000, Data: []byte{0x67, 0x02}, Type: 7}, // SPS
		{PTS: 99000, Data: []byte{0x68, 0x02}, Type: 8}, // PPS
		{PTS: 99000, Data: []byte{0x65, 0x02}, Type: 5}, // IDR
	}

	frames := assembleAccessUnit(nalus)
	require.Len(t, frames, 2)
	// Frame 1: SPS+PPS+IDR+non-IDR (4 NALUs — non-IDR is part of same AU as IDR)
	require.Len(t, frames[0], 4)
	// Frame 2: SPS+PPS+IDR (3 NALUs)
	require.Len(t, frames[1], 3)
}

func TestTSDemuxerBasicPES(t *testing.T) {
	t.Helper()

	demuxer := NewTSDemuxer()

	// Build a minimal MPEG-TS stream with one PES packet containing SPS+PPS+IDR
	tsPackets := buildTestTSPackets(t)
	nalus := demuxer.Feed(tsPackets)
	nalus = append(nalus, demuxer.Flush()...)

	// Should have extracted NALUs
	require.NotEmpty(t, nalus, "expected NALUs from MPEG-TS data")

	// Verify we got at least SPS and IDR
	types := make(map[uint8]bool)
	for _, nalu := range nalus {
		types[nalu.Type] = true
	}
	require.True(t, types[7], "expected SPS NALU (type 7)")
}

func TestTSDemuxerFlush(t *testing.T) {
	t.Helper()

	demuxer := NewTSDemuxer()

	// Build test data
	tsPackets := buildTestTSPackets(t)

	// Feed partial data (first half)
	half := len(tsPackets) / 2
	demuxer.Feed(tsPackets[:half])

	// Flush should return remaining NALUs
	nalus := demuxer.Flush()
	// Result depends on data, but Flush should not panic
	_ = nalus
}

func TestTSDemuxerEmptyInput(t *testing.T) {
	t.Helper()

	demuxer := NewTSDemuxer()
	nalus := demuxer.Feed(nil)
	require.Nil(t, nalus)

	nalus = demuxer.Feed([]byte{})
	require.Nil(t, nalus)
}

func TestParsePESHeader(t *testing.T) {
	t.Helper()

	// Build a minimal PES header with PTS
	pes := make([]byte, 20)
	// Start code prefix
	pes[0] = 0x00
	pes[1] = 0x00
	pes[2] = 0x01
	// Stream ID: video stream 0
	pes[3] = 0xE0
	// PES packet length (can be 0 for video)
	pes[4] = 0x00
	pes[5] = 0x00
	// Flags: PTS present (0x80), no DTS
	pes[6] = 0x80
	pes[7] = 0x80 // PTS only flag = 0b10
	// PES header data length
	pes[8] = 0x05
	// PTS (5 bytes): 90000 ticks = 1 second
	// PTS = 90000 = 0x15F90
	pts := int64(90000)
	pes[9] = byte((pts>>29)&0x0E) | 0x21 // marker bits
	pes[10] = byte((pts >> 22) & 0xFF)
	pes[11] = byte((pts>>14)&0xFE) | 0x01
	pes[12] = byte((pts >> 7) & 0xFF)
	pes[13] = byte((pts<<1)&0xFE) | 0x01

	parsedPTS, offset, ok := parsePESHeader(pes)
	require.True(t, ok)
	require.Equal(t, int64(90000), parsedPTS)
	require.Equal(t, 14, offset) // 9 + 5 header data length
}

func TestParsePESHeaderInvalid(t *testing.T) {
	t.Helper()

	// No start code
	pes := []byte{0xFF, 0xFF, 0xFF, 0xE0, 0x00, 0x00, 0x80, 0x80, 0x05}
	_, _, ok := parsePESHeader(pes)
	require.False(t, ok)

	// Non-video stream ID
	pes = []byte{0x00, 0x00, 0x01, 0xC0, 0x00, 0x00, 0x80, 0x80, 0x05}
	_, _, ok = parsePESHeader(pes)
	require.False(t, ok)

	// Too short
	pes = []byte{0x00, 0x00, 0x01}
	_, _, ok = parsePESHeader(pes)
	require.False(t, ok)
}

func TestFormatPTS(t *testing.T) {
	t.Helper()

	require.Equal(t, "1.000s", formatPTS(90000))
	require.Equal(t, "0.000s", formatPTS(0))
	require.Equal(t, "0.000s", formatPTS(-1))
	require.Equal(t, "0.333s", formatPTS(30000))
}

// buildTestTSPackets creates a sequence of MPEG-TS packets containing
// a minimal H.264 bitstream with SPS, PPS, and an IDR NALU.
func buildTestTSPackets(t *testing.T) []byte {
	t.Helper()

	// Build PES payload: SPS + PPS + IDR with start codes
	var payload []byte

	sps := []byte{0x67, 0x42, 0xc0, 0x1e, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0xc8}
	pps := []byte{0x68, 0xce, 0x06, 0xe2}
	idr := []byte{0x65, 0x88, 0x84, 0x00, 0x40, 0x00, 0x00, 0x13, 0xfc}

	payload = append(payload, 0x00, 0x00, 0x00, 0x01)
	payload = append(payload, sps...)
	payload = append(payload, 0x00, 0x00, 0x00, 0x01)
	payload = append(payload, pps...)
	payload = append(payload, 0x00, 0x00, 0x00, 0x01)
	payload = append(payload, idr...)

	// Build PES header
	pts := int64(90000)
	pesHeader := buildPESHeader(pts, len(payload))

	pesData := append(pesHeader, payload...)

	// Split PES data into TS packets
	return buildTSPacketsFromPES(pesData, 256, true)
}

// buildPESHeader creates a PES header with PTS.
func buildPESHeader(pts int64, payloadLen int) []byte {
	headerLen := 9 + 5 // PES header + PTS (5 bytes)
	totalLen := headerLen + payloadLen

	header := make([]byte, headerLen)
	// Start code
	header[0] = 0x00
	header[1] = 0x00
	header[2] = 0x01
	// Stream ID: video
	header[3] = 0xE0
	// PES packet length
	header[4] = byte((totalLen - 6) >> 8)
	header[5] = byte(totalLen - 6)
	// Flags
	header[6] = 0x80 // marker bits
	header[7] = 0x80 // PTS only
	header[8] = 0x05 // header data length
	// PTS (5 bytes)
	header[9] = byte((pts>>29)&0x0E) | 0x21
	header[10] = byte((pts >> 22) & 0xFF)
	header[11] = byte((pts>>14)&0xFE) | 0x01
	header[12] = byte((pts >> 7) & 0xFF)
	header[13] = byte((pts<<1)&0xFE) | 0x01

	return header
}

// buildTSPacketsFromPES splits PES data into MPEG-TS packets.
func buildTSPacketsFromPES(pesData []byte, pid uint16, firstPUSI bool) []byte {
	var result []byte
	offset := 0
	first := true

	for offset < len(pesData) {
		pkt := make([]byte, tsPacketSize)
		pkt[0] = tsSyncByte

		// PUSI set only on first packet
		if first {
			pkt[1] = byte((pid >> 8) & 0x1F) | 0x40 // PUSI + PID high bits
			first = false
		} else {
			pkt[1] = byte((pid >> 8) & 0x1F) // PID high bits
		}
		pkt[2] = byte(pid & 0xFF)

		remaining := tsPacketSize - 4
		if offset+remaining > len(pesData) {
			// Last packet: use adaptation field for padding
			payloadBytes := len(pesData) - offset
			stuffingBytes := remaining - payloadBytes - 1 // -1 for adaptation field length byte
			afLen := byte(stuffingBytes)

			// AFC = 0x30 (adaptation field + payload)
			pkt[3] = 0x30
			pkt[4] = afLen
			// Fill adaptation field with 0xFF stuffing
			for i := 5; i < 5+stuffingBytes; i++ {
				pkt[i] = 0xFF
			}
			// Copy payload after adaptation field
			payloadStart := 5 + stuffingBytes
			copy(pkt[payloadStart:], pesData[offset:])
			offset = len(pesData)
		} else {
			// Full payload packet: AFC = 0x10 (payload only)
			pkt[3] = 0x10
			copy(pkt[4:], pesData[offset:offset+remaining])
			offset += remaining
		}

		result = append(result, pkt...)
	}

	return result
}
