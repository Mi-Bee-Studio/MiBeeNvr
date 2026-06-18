package livetranscode

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// helpers to construct test data

func h264NAL(nalType byte, payload []byte) []byte {
	// H.264 header: forbidden(1) | nal_ref_idc(2) | nal_unit_type(5)
	// Use nal_ref_idc=3 for SPS/PPS/IDR, nal_ref_idc=2 for non-IDR slices
	refIDC := byte(3)
	if nalType == 1 { // non-IDR
		refIDC = 2
	}
	return append([]byte{(refIDC << 5) | nalType}, payload...)
}

func h265NAL(nalType byte, payload []byte) []byte {
	// H.265 header: forbidden(1) | nal_unit_type(6) | nuh_layer_id(1)
	// Then: nuh_layer_id(7) | nuh_temporal_id_plus1(3)
	// For layer_id=0, temporal_id=0: header = [nalType<<1, 0x01]
	header := []byte{nalType << 1, 0x01}
	return append(header, payload...)
}

// helper to create Annex-B buffer with mixed start codes
func annexB(codec Codec, nalus ...[]byte) []byte {
	var buf []byte
	for i, nalu := range nalus {
		if codec == CodecH265 || i%2 == 0 {
			buf = append(buf, []byte{0x00, 0x00, 0x00, 0x01}...)
		} else {
			buf = append(buf, []byte{0x00, 0x00, 0x01}...)
		}
		buf = append(buf, nalu...)
	}
	return buf
}

func requireAULen(t *testing.T, aus []AccessUnit, expectedLen int) {
	t.Helper()
	require.Equal(t, expectedLen, len(aus), "access unit count mismatch")
}

func requireAUNALUCount(t *testing.T, au AccessUnit, expectedNALUCount int) {
	t.Helper()
	require.Equal(t, expectedNALUCount, len(au), "NALU count in access unit mismatch")
}

// --------------- Batch Split Tests ---------------

func TestSplitH264Basic(t *testing.T) {
	// Stream: [SPS PPS IDR] [P] [P] -> 3 AUs
	sps := h264NAL(7, []byte{0x64, 0x00, 0x1e, 0xac})                         // dummy SPS data
	pps := h264NAL(8, []byte{0xe8, 0x43, 0x80})                               // dummy PPS data
	idr := h264NAL(5, []byte{0x88, 0x84, 0x00, 0x0d, 0x08, 0x9a, 0x61, 0xa0}) // IDR slice
	p1 := h264NAL(1, []byte{0x84, 0x01, 0x0d, 0x08})                          // P-slice 1
	p2 := h264NAL(1, []byte{0x84, 0x02, 0x0d, 0x08})                          // P-slice 2

	// Use all 4-byte start codes
	data := annexB(CodecH264, sps, pps, idr, p1, p2)
	aus, ps, err := SplitAnnexB(data, CodecH264)
	require.NoError(t, err)
	requireAULen(t, aus, 3)

	// AU[0]: SPS + PPS + IDR slice
	requireAUNALUCount(t, aus[0], 3)

	// AU[1]: P-slice 1
	requireAUNALUCount(t, aus[1], 1)

	// AU[2]: P-slice 2
	requireAUNALUCount(t, aus[2], 1)

	// Param sets
	require.NotNil(t, ps.SPS)
	require.NotNil(t, ps.PPS)
	require.Nil(t, ps.VPS) // H.264 has no VPS
	require.Equal(t, sps, ps.SPS)
	require.Equal(t, pps, ps.PPS)
}

func TestSplitH265Basic(t *testing.T) {
	// Stream: [VPS SPS PPS IDR] [P] [P] -> 3 AUs
	vps := h265NAL(32, []byte{0x01, 0x02, 0x03})
	sps := h265NAL(33, []byte{0x04, 0x05, 0x06})
	pps := h265NAL(34, []byte{0x07, 0x08})
	idr := h265NAL(19, []byte{0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19})
	p1 := h265NAL(1, []byte{0x20, 0x21, 0x22})
	p2 := h265NAL(1, []byte{0x30, 0x31, 0x32})

	data := annexB(CodecH265, vps, sps, pps, idr, p1, p2)
	aus, ps, err := SplitAnnexB(data, CodecH265)
	require.NoError(t, err)
	requireAULen(t, aus, 3)

	// AU[0]: VPS + SPS + PPS + IDR
	requireAUNALUCount(t, aus[0], 4)

	// AU[1]: P-slice 1
	requireAUNALUCount(t, aus[1], 1)

	// AU[2]: P-slice 2
	requireAUNALUCount(t, aus[2], 1)

	// Param sets
	require.NotNil(t, ps.VPS)
	require.NotNil(t, ps.SPS)
	require.NotNil(t, ps.PPS)
	require.Equal(t, vps, ps.VPS)
	require.Equal(t, sps, ps.SPS)
	require.Equal(t, pps, ps.PPS)
}

func TestSplitH264EmulationPrevention(t *testing.T) {
	// SPS containing emulation prevention bytes: 0x00 0x00 0x03 0x00
	// Original data before EPB: [0x00, 0x00, 0x00]
	// After encoder inserts 0x03: [0x00, 0x00, 0x03, 0x00]
	epbPayload := []byte{0x00, 0x00, 0x03, 0x00, 0x01, 0x02}
	sps := h264NAL(7, epbPayload)
	idr := h264NAL(5, []byte{0x88, 0x84})
	p := h264NAL(1, []byte{0x84, 0x01})

	data := annexB(CodecH264, sps, idr, p)
	aus, ps, err := SplitAnnexB(data, CodecH264)

	require.NoError(t, err)
	requireAULen(t, aus, 2)

	// AU[0]: SPS + IDR
	requireAUNALUCount(t, aus[0], 2)

	// SPS EPB stripped: 00 00 03 00 -> 00 00 00
	// Expected SPS after EPB removal
	expectedSPS := h264NAL(7, []byte{0x00, 0x00, 0x00, 0x01, 0x02})
	require.Equal(t, expectedSPS, ps.SPS)
	// Also verify NALU 0 in AU[0] has EPB stripped
	require.Equal(t, expectedSPS, aus[0][0], "SPS NALU should have EPB stripped")

	// AU[1]: P-slice
	requireAUNALUCount(t, aus[1], 1)
}

func TestSplitH264MixedStartCodes(t *testing.T) {
	// Mixed 3-byte and 4-byte start codes
	sps := h264NAL(7, []byte{0x01, 0x02})
	pps := h264NAL(8, []byte{0x03, 0x04})
	idr := h264NAL(5, []byte{0x05, 0x06})
	p := h264NAL(1, []byte{0x07, 0x08})

	// Manually build buffer with alternating start codes
	// 4-byte then 3-byte then 4-byte then 3-byte
	buf := []byte{}
	buf = append(buf, []byte{0x00, 0x00, 0x00, 0x01}...) // 4-byte
	buf = append(buf, sps...)
	buf = append(buf, []byte{0x00, 0x00, 0x01}...) // 3-byte
	buf = append(buf, pps...)
	buf = append(buf, []byte{0x00, 0x00, 0x00, 0x01}...) // 4-byte
	buf = append(buf, idr...)
	buf = append(buf, []byte{0x00, 0x00, 0x01}...) // 3-byte
	buf = append(buf, p...)

	aus, ps, err := SplitAnnexB(buf, CodecH264)
	require.NoError(t, err)
	requireAULen(t, aus, 2) // [SPS PPS IDR] [P]
	requireAUNALUCount(t, aus[0], 3)
	requireAUNALUCount(t, aus[1], 1)
	require.NotNil(t, ps.SPS)
	require.NotNil(t, ps.PPS)
}

func TestSplitH265Rejects3ByteStartCodes(t *testing.T) {
	// H.265 stream using 3-byte start code -> error
	vps := h265NAL(32, []byte{0x01})

	// Use 3-byte start code
	buf := append([]byte{0x00, 0x00, 0x01}, vps...)
	_, _, err := SplitAnnexB(buf, CodecH265)
	require.Error(t, err)
	require.Contains(t, err.Error(), "start code")
}

func TestSplitGarbageInput(t *testing.T) {
	// Pure garbage -> error, no panic
	_, _, err := SplitAnnexB([]byte{0xff, 0xfe, 0xfd, 0xfc, 0xfb}, CodecH264)
	require.Error(t, err)

	_, _, err = SplitAnnexB([]byte{0xff, 0xfe, 0xfd, 0xfc, 0xfb}, CodecH265)
	require.Error(t, err)
}

func TestSplitEmptyBuffer(t *testing.T) {
	_, _, err := SplitAnnexB(nil, CodecH264)
	require.Error(t, err)

	_, _, err = SplitAnnexB([]byte{}, CodecH265)
	require.Error(t, err)
}

func TestSplitParamSetsExtractionH264(t *testing.T) {
	sps := h264NAL(7, []byte{0x64, 0x00, 0x1e})
	pps := h264NAL(8, []byte{0xe8, 0x43})
	idr := h264NAL(5, []byte{0x88, 0x84, 0x00, 0x0d})
	p := h264NAL(1, []byte{0x84, 0x01})

	// Add a second IDR with new SPS/PPS mid-stream
	sps2 := h264NAL(7, []byte{0x64, 0x00, 0x1f})
	pps2 := h264NAL(8, []byte{0xe8, 0x44})
	idr2 := h264NAL(5, []byte{0x88, 0x84, 0x00, 0x0e})

	data := annexB(CodecH264, sps, pps, idr, p, sps2, pps2, idr2)
	aus, ps, err := SplitAnnexB(data, CodecH264)
	require.NoError(t, err)
	requireAULen(t, aus, 3)

	// AU[0]: [SPS, PPS, IDR]
	requireAUNALUCount(t, aus[0], 3)

	// AU[1]: [P]
	requireAUNALUCount(t, aus[1], 1)

	// AU[2]: [SPS2, PPS2, IDR2]
	requireAUNALUCount(t, aus[2], 3)

	// ParamSets should be the most recent (sps2, pps2)
	require.Equal(t, sps2, ps.SPS)
	require.Equal(t, pps2, ps.PPS)
}

func TestSplitParamSetsExtractionH265(t *testing.T) {
	vps := h265NAL(32, []byte{0x01, 0x02})
	sps := h265NAL(33, []byte{0x03, 0x04})
	pps := h265NAL(34, []byte{0x05, 0x06})
	idr := h265NAL(19, []byte{0x07, 0x08, 0x09, 0x0a})
	p := h265NAL(1, []byte{0x0b, 0x0c})

	data := annexB(CodecH265, vps, sps, pps, idr, p)
	aus, ps, err := SplitAnnexB(data, CodecH265)
	require.NoError(t, err)
	requireAULen(t, aus, 2)
	require.NotNil(t, ps.VPS)
	require.NotNil(t, ps.SPS)
	require.NotNil(t, ps.PPS)
	require.Equal(t, vps, ps.VPS)
	require.Equal(t, sps, ps.SPS)
	require.Equal(t, pps, ps.PPS)
}

func TestSplitH264MultipleIDR(t *testing.T) {
	// Stream: [SPS PPS IDR1] [P] [SPS PPS IDR2] [P] -> 4 AUs
	sps1 := h264NAL(7, []byte{0x01})
	pps1 := h264NAL(8, []byte{0x02})
	idr1 := h264NAL(5, []byte{0x03, 0x04})
	p1 := h264NAL(1, []byte{0x05, 0x06})
	sps2 := h264NAL(7, []byte{0x07})
	pps2 := h264NAL(8, []byte{0x08})
	idr2 := h264NAL(5, []byte{0x09, 0x0a})
	p2 := h264NAL(1, []byte{0x0b, 0x0c})

	data := annexB(CodecH264, sps1, pps1, idr1, p1, sps2, pps2, idr2, p2)
	aus, ps, err := SplitAnnexB(data, CodecH264)
	require.NoError(t, err)
	requireAULen(t, aus, 4)
	requireAUNALUCount(t, aus[0], 3) // SPS1 PPS1 IDR1
	requireAUNALUCount(t, aus[1], 1) // P1
	requireAUNALUCount(t, aus[2], 3) // SPS2 PPS2 IDR2
	requireAUNALUCount(t, aus[3], 1) // P2

	// Most recent param sets
	require.Equal(t, sps2, ps.SPS)
	require.Equal(t, pps2, ps.PPS)
}

func TestSplitH265MultipleIDR(t *testing.T) {
	// Stream: [VPS SPS PPS IDR1] [P] [VPS SPS PPS IDR2] [P] -> 4 AUs
	vps1 := h265NAL(32, []byte{0x01})
	sps1 := h265NAL(33, []byte{0x02})
	pps1 := h265NAL(34, []byte{0x03})
	idr1 := h265NAL(19, []byte{0x04, 0x05})
	p1 := h265NAL(1, []byte{0x06, 0x07})
	vps2 := h265NAL(32, []byte{0x08})
	sps2 := h265NAL(33, []byte{0x09})
	pps2 := h265NAL(34, []byte{0x0a})
	idr2 := h265NAL(20, []byte{0x0b, 0x0c})
	p2 := h265NAL(1, []byte{0x0d, 0x0e})

	data := annexB(CodecH265, vps1, sps1, pps1, idr1, p1, vps2, sps2, pps2, idr2, p2)
	aus, ps, err := SplitAnnexB(data, CodecH265)
	require.NoError(t, err)
	requireAULen(t, aus, 4)
	requireAUNALUCount(t, aus[0], 4)
	requireAUNALUCount(t, aus[1], 1)
	requireAUNALUCount(t, aus[2], 4)
	requireAUNALUCount(t, aus[3], 1)
	require.Equal(t, vps2, ps.VPS)
	require.Equal(t, sps2, ps.SPS)
	require.Equal(t, pps2, ps.PPS)
}

func TestSplitH264WithSEI(t *testing.T) {
	// SEI before IDR should be grouped with IDR
	sei := h264NAL(6, []byte{0x04, 0x05})
	sps := h264NAL(7, []byte{0x64})
	pps := h264NAL(8, []byte{0xe8})
	idr := h264NAL(5, []byte{0x88, 0x84})
	p := h264NAL(1, []byte{0x84})

	data := annexB(CodecH264, sei, sps, pps, idr, p)
	aus, _, err := SplitAnnexB(data, CodecH264)
	require.NoError(t, err)
	requireAULen(t, aus, 2)

	// AU[0]: SEI + SPS + PPS + IDR (all one picture)
	requireAUNALUCount(t, aus[0], 4)

	// AU[1]: P
	requireAUNALUCount(t, aus[1], 1)
}

// --------------- Streaming Parser Tests ---------------

func TestStreamParserBasic(t *testing.T) {
	sps := h264NAL(7, []byte{0x64, 0x00, 0x1e})
	pps := h264NAL(8, []byte{0xe8, 0x43})
	idr := h264NAL(5, []byte{0x88, 0x84, 0x00})
	p1 := h264NAL(1, []byte{0x84, 0x01})
	p2 := h264NAL(1, []byte{0x84, 0x02})

	data := annexB(CodecH264, sps, pps, idr, p1, p2)

	// Feed entire buffer at once + flush for last pending AU
	sp := NewAnnexBStreamParser(CodecH264)
	aus1 := sp.Feed(data)
	aus2 := sp.Flush()
	aus := append(aus1, aus2...)
	requireAULen(t, aus, 3)

	// Most recent param sets
	require.NotNil(t, sp.ParamSets().SPS)
	require.NotNil(t, sp.ParamSets().PPS)
}

func TestStreamParserChunked(t *testing.T) {
	sps := h264NAL(7, []byte{0x64, 0x00, 0x1e})
	pps := h264NAL(8, []byte{0xe8, 0x43})
	idr := h264NAL(5, []byte{0x88, 0x84, 0x00, 0x0d})
	p1 := h264NAL(1, []byte{0x84, 0x01})
	p2 := h264NAL(1, []byte{0x84, 0x02})

	data := annexB(CodecH264, sps, pps, idr, p1, p2)

	// Compare batch parse
	batchAUs, _, err := SplitAnnexB(data, CodecH264)
	require.NoError(t, err)

	// Feed in chunks of 3 bytes
	sp := NewAnnexBStreamParser(CodecH264)
	var streamAUs []AccessUnit
	for i := 0; i < len(data); i += 3 {
		end := i + 3
		if end > len(data) {
			end = len(data)
		}
		chunk := data[i:end]
		aus := sp.Feed(chunk)
		streamAUs = append(streamAUs, aus...)
	}
	// Flush remaining
	final := sp.Flush()
	streamAUs = append(streamAUs, final...)

	requireAULen(t, streamAUs, len(batchAUs))
	for i := range batchAUs {
		require.Equal(t, len(batchAUs[i]), len(streamAUs[i]), "AU %d NALU count mismatch", i)
		for j := range batchAUs[i] {
			require.Equal(t, batchAUs[i][j], streamAUs[i][j], "AU %d NALU %d mismatch", i, j)
		}
	}
}

func TestStreamParserStartCodeSpanChunk(t *testing.T) {
	// Simulate FFmpeg output where start code spans chunk boundaries.
	// The streaming parser buffers NALUs until AU boundaries are confirmed.
	// SPS and IDR belong to the same AU — they are returned together on Flush.
	sps := h264NAL(7, []byte{0x64, 0x00})
	idr := h264NAL(5, []byte{0x88, 0x84})

	// Build buffer: [SC][SPS][SC][IDR]
	buf := []byte{}
	buf = append(buf, []byte{0x00, 0x00, 0x00, 0x01}...)
	buf = append(buf, sps...)
	buf = append(buf, []byte{0x00, 0x00, 0x00, 0x01}...)
	buf = append(buf, idr...)

	// Split at a point that splits a start code: after "00 00" of the second start code
	splitPoint := 4 + len(sps) + 2 // after "00 00" of second start code
	chunk1 := buf[:splitPoint]
	chunk2 := buf[splitPoint:]

	// chunk1 ends with: ... SPS_data 00 00
	// chunk2 starts with: 00 01 IDR_data

	sp := NewAnnexBStreamParser(CodecH264)
	aus1 := sp.Feed(chunk1)
	require.Equal(t, 0, len(aus1), "chunk1 has no complete NALU")

	// Feed chunk2 — completes the second SC, SPS extracted but no AU boundary yet
	aus2 := sp.Feed(chunk2)
	require.Equal(t, 0, len(aus2), "SPS alone cannot confirm AU boundary")

	// Flush returns both SPS and IDR in one AU (correctly grouped)
	aus3 := sp.Flush()
	requireAULen(t, aus3, 1)
	requireAUNALUCount(t, aus3[0], 2)
	require.Equal(t, sps, aus3[0][0], "SPS should be NALU 0")
	require.Equal(t, idr, aus3[0][1], "IDR should be NALU 1")
}

func TestStreamParserH265Chunked(t *testing.T) {
	vps := h265NAL(32, []byte{0x01, 0x02})
	sps := h265NAL(33, []byte{0x03, 0x04})
	pps := h265NAL(34, []byte{0x05, 0x06})
	idr := h265NAL(19, []byte{0x07, 0x08, 0x09, 0x0a})
	p := h265NAL(1, []byte{0x0b, 0x0c})

	data := annexB(CodecH265, vps, sps, pps, idr, p)

	// Batch parse reference
	batchAUs, _, err := SplitAnnexB(data, CodecH265)
	require.NoError(t, err)

	// Feed 5 bytes at a time
	sp := NewAnnexBStreamParser(CodecH265)
	var streamAUs []AccessUnit
	for i := 0; i < len(data); i += 5 {
		end := i + 5
		if end > len(data) {
			end = len(data)
		}
		aus := sp.Feed(data[i:end])
		streamAUs = append(streamAUs, aus...)
	}
	// Flush remaining
	final := sp.Flush()
	streamAUs = append(streamAUs, final...)

	require.Equal(t, len(batchAUs), len(streamAUs))
	for i := range batchAUs {
		require.Equal(t, len(batchAUs[i]), len(streamAUs[i]), "AU %d NALU count mismatch", i)
	}
}

func TestStreamParserEmpty(t *testing.T) {
	sp := NewAnnexBStreamParser(CodecH264)
	aus := sp.Feed(nil)
	require.Nil(t, aus)

	aus = sp.Feed([]byte{})
	require.Nil(t, aus)
}
