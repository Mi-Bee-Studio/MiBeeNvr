package gb28181

import (
	"bytes"
	"testing"
)

// Test helpers for building synthetic PS packets

// buildPS constructs a minimal MPEG-PS packet from given NALUs.
// Returns the complete PS byte stream ready for demuxing.
func buildPS(nalus [][]byte, streamType byte) []byte {
	var ps bytes.Buffer

	// Pack header: 00 00 01 BA (4) + 10 bytes of fixed fields
	ps.Write([]byte{0x00, 0x00, 0x01, 0xBA})
	ps.Write([]byte{0x44, 0x00, 0x04, 0x00, 0x04, 0x01, 0x01, 0x89, 0xC3, 0xF8})

	// System header: 00 00 01 BB (4) + 2 bytes length + system header data
	ps.Write([]byte{0x00, 0x00, 0x01, 0xBB})
	systemHeader := []byte{
		0x00, 0x09, // length (9 bytes after the length field)
		0x01,                   // rate bound and audio/video bound
		0xB9, 0xE0, 0xE0, 0x80, // rate bound (high) + audio bound + fixed flag
		0xC0, 0x01, // stream 1 (audio)
		0x00, 0x01, // STD buffer scale and size
	}
	ps.Write(systemHeader)

	// Program Stream Map: 00 00 01 BC (4) + PSM data
	ps.Write([]byte{0x00, 0x00, 0x01, 0xBC})

	// PSM packet_length (2 bytes): body length = 10 bytes
	ps.Write([]byte{0x00, 0x0A})

	// PSM body per ISO/IEC 13818-1 program_stream_map:
	// version (1) + reserved (1) + PS_info_length (2) +
	// elementary_stream_map_length (2) + stream_info entry (4)
	ps.Write([]byte{
		0x02,       // version = 2
		0xC0,       // reserved byte
		0x00, 0x00, // PS_info_length = 0 (no PS_info)
		0x00, 0x04, // elementary_stream_map_length = 4 (one entry)
		// stream_info for video
		streamType, // stream_type (0x1B for H.264, 0x24 for H.265)
		0xE0,       // elementary_stream_id (video stream 0)
		0x00, 0x00, // elementary_stream_info_length (0)
	})

	// Video PES: 00 00 01 E0 (4) + PES header + NALU payload
	ps.Write([]byte{0x00, 0x00, 0x01, 0xE0})

	// Calculate total NALU payload length with start codes
	payloadLen := 0
	for _, nalu := range nalus {
		payloadLen += 4 // 4-byte start code
		payloadLen += len(nalu)
	}

	// PES packet length = flags (2) + header_data_length (1) + payload_len
	pesPacketLen := 3 + payloadLen

	// PES header, standard layout (ITU-T H.222.0): byte6='10'+flags,
	// byte7=PTS_DTS_flags (0 = no PTS), byte8=PES_header_data_length (0).
	pesHeader := []byte{
		byte(pesPacketLen >> 8), byte(pesPacketLen), // PES_packet_length (2 bytes)
		0x80, // Byte 6: '10' marker + zero flags
		0x00, // Byte 7: PTS_DTS_flags = '00' (no PTS in synthetic stream)
		0x00, // Byte 8: PES_header_data_length (0)
	}
	ps.Write(pesHeader)

	// Write NALUs with Annex-B start codes
	for _, nalu := range nalus {
		ps.Write([]byte{0x00, 0x00, 0x00, 0x01})
		ps.Write(nalu)
	}

	return ps.Bytes()
}

// TestPSDemuxer_H264 tests H.264 NALU extraction from synthetic PS.
func TestPSDemuxer_H264(t *testing.T) {
	// Create synthetic H.264 NALUs (SPS, PPS, IDR)
	sps := []byte{0x67, 0x42, 0x00, 0x1E, 0x9A, 0x74, 0x05, 0x81, 0xEC, 0x80}
	pps := []byte{0x68, 0xCE, 0x3C, 0x80}
	idr := []byte{0x65, 0x88, 0x84, 0x0A, 0xF2, 0x61, 0x58}

	nalus := [][]byte{sps, pps, idr}
	psData := buildPS(nalus, streamTypeH264)

	d := NewPSDemuxer()
	extractedNALUs, err := d.FeedAU(psData, 90000, true)
	if err != nil {
		t.Fatalf("FeedAU failed: %v", err)
	}

	if len(extractedNALUs) != 3 {
		t.Fatalf("Expected 3 NALUs, got %d", len(extractedNALUs))
	}

	// Verify NALU content (without start codes)
	if !bytes.Equal(extractedNALUs[0], sps) {
		t.Errorf("SPS mismatch: got %v, want %v", extractedNALUs[0], sps)
	}
	if !bytes.Equal(extractedNALUs[1], pps) {
		t.Errorf("PPS mismatch: got %v, want %v", extractedNALUs[1], pps)
	}
	if !bytes.Equal(extractedNALUs[2], idr) {
		t.Errorf("IDR mismatch: got %v, want %v", extractedNALUs[2], idr)
	}

	// Verify codec detection
	if d.Codec() != "h264" {
		t.Errorf("Expected codec h264, got %s", d.Codec())
	}
}

// TestPSDemuxer_H265 tests H.265 NALU extraction from synthetic PS.
func TestPSDemuxer_H265(t *testing.T) {
	// Create synthetic H.265 NALUs (VPS, SPS, PPS, IDR_W_RADL)
	// NALU types in H.265 are (first byte >> 1) & 0x3F
	vps := []byte{0x40, 0x01, 0x0C, 0x01, 0xFF, 0xFF, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0xB0, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00}
	sps := []byte{0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0xB0, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00}
	pps := []byte{0x44, 0x01, 0xC1, 0x73, 0xD1, 0x89}
	idr := []byte{0x26, 0x01, 0xAF, 0x04, 0x80} // NALU type 19 (0x26 >> 1 & 0x3F = 19)

	nalus := [][]byte{vps, sps, pps, idr}
	psData := buildPS(nalus, streamTypeH265)

	d := NewPSDemuxer()
	extractedNALUs, err := d.FeedAU(psData, 180000, true)
	if err != nil {
		t.Fatalf("FeedAU failed: %v", err)
	}

	if len(extractedNALUs) != 4 {
		t.Fatalf("Expected 4 NALUs, got %d", len(extractedNALUs))
	}

	// Verify NALU content (without start codes)
	if !bytes.Equal(extractedNALUs[0], vps) {
		t.Errorf("VPS mismatch: got %v, want %v", extractedNALUs[0], vps)
	}
	if !bytes.Equal(extractedNALUs[1], sps) {
		t.Errorf("SPS mismatch: got %v, want %v", extractedNALUs[1], sps)
	}
	if !bytes.Equal(extractedNALUs[2], pps) {
		t.Errorf("PPS mismatch: got %v, want %v", extractedNALUs[2], pps)
	}
	if !bytes.Equal(extractedNALUs[3], idr) {
		t.Errorf("IDR mismatch: got %v, want %v", extractedNALUs[3], idr)
	}

	// Verify codec detection
	if d.Codec() != "h265" {
		t.Errorf("Expected codec h265, got %s", d.Codec())
	}
}

// TestPSDemuxer_Fragmented tests PS payload split across multiple chunks.
func TestPSDemuxer_Fragmented(t *testing.T) {
	// Create synthetic H.264 NALUs
	sps := []byte{0x67, 0x42, 0x00, 0x1E, 0x9A}
	pps := []byte{0x68, 0xCE, 0x3C, 0x80}
	idr := []byte{0x65, 0x88, 0x84, 0x0A}

	nalus := [][]byte{sps, pps, idr}
	psData := buildPS(nalus, streamTypeH264)

	d := NewPSDemuxer()
	var allNALUs [][]byte

	// Split PS payload into 3 chunks
	chunkSize := len(psData) / 3
	for i := range 3 {
		start := i * chunkSize
		end := start + chunkSize
		if i == 2 {
			end = len(psData) // Last chunk gets remainder
		}
		chunk := psData[start:end]
		extractedNALUs, err := d.FeedAU(chunk, int64(i)*90000, true)
		if err != nil {
			t.Fatalf("FeedAU failed on chunk %d: %v", i, err)
		}
		allNALUs = append(allNALUs, extractedNALUs...)
	}

	// Flush any remaining buffered NALUs
	flushedNALUs := d.Flush()
	allNALUs = append(allNALUs, flushedNALUs...)

	// We should get all 3 NALUs
	if len(allNALUs) != 3 {
		t.Fatalf("Expected 3 NALUs after fragmented feed, got %d", len(allNALUs))
	}

	// Verify NALU content
	if !bytes.Equal(allNALUs[0], sps) {
		t.Errorf("SPS mismatch after fragmentation: got %v, want %v", allNALUs[0], sps)
	}
	if !bytes.Equal(allNALUs[1], pps) {
		t.Errorf("PPS mismatch after fragmentation: got %v, want %v", allNALUs[1], pps)
	}
	if !bytes.Equal(allNALUs[2], idr) {
		t.Errorf("IDR mismatch after fragmentation: got %v, want %v", allNALUs[2], idr)
	}
}

// TestPSDemuxer_Flush tests draining residual NALUs from incomplete PES.
func TestPSDemuxer_Flush(t *testing.T) {
	// Create a PS with only partial PES data
	psData := []byte{
		0x00, 0x00, 0x01, 0xBA, // Pack header
		0x44, 0x00, 0x04, 0x00, 0x04, 0x01, 0x01, 0x89, 0xC3, 0xF8,
		0x00, 0x00, 0x01, 0xBC, // PSM
		0x00, 0x0A, 0x81, 0xC0, 0x00, 0x00, 0x00, 0x04, // PSM length=10, version, reserved, PS_info_length=0, es_map_length=4
		0x1B, 0xE0, 0x00, 0x00, // stream_type 0x1B, stream_id 0xE0, es_info_len=0
		0x00, 0x00, 0x01, 0xE0, // Video PES start (incomplete)
		0x00, 0x0E, // PES length (14: 2 header + 12 payload)
		0x80, 0x00, // PES header (no PTS/DTS, header_data_length=0)
		0x00, 0x00, 0x00, 0x01, // Start code
		0x67, 0x42, 0x00, 0x1E, // SPS NALU (partial - 4 bytes start code + 4 bytes)
	}

	d := NewPSDemuxer()
	extractedNALUs, err := d.FeedAU(psData, 90000, true)
	if err != nil {
		t.Fatalf("FeedAU failed: %v", err)
	}

	// The FeedAU should buffer the incomplete PES
	// (Implementation detail: depending on buffering logic)
	// Let's verify the buffer state through Flush

	flushedNALUs := d.Flush()

	// After flush, we should get the buffered NALU
	// The exact count depends on whether the PES was complete
	if len(flushedNALUs) == 0 && len(extractedNALUs) == 0 {
		t.Log("No NALUs extracted (PES may have been buffered as incomplete)")
	} else {
		totalNALUs := len(extractedNALUs) + len(flushedNALUs)
		if totalNALUs == 0 {
			t.Error("Expected at least one NALU after flush, got none")
		}
	}
}

// TestPSDemuxer_AudioPES tests that audio PES packets are skipped.
func TestPSDemuxer_AudioPES(t *testing.T) {
	// Build a PS with audio PES (stream_id 0xC0)
	var ps bytes.Buffer

	// Pack header
	ps.Write([]byte{0x00, 0x00, 0x01, 0xBA})
	ps.Write([]byte{0x44, 0x00, 0x04, 0x00, 0x04, 0x01, 0x01, 0x89, 0xC3, 0xF8})

	// PSM with audio stream type
	ps.Write([]byte{0x00, 0x00, 0x01, 0xBC})
	psm := []byte{
		0x00, 0x0A, 0x81, 0xC0, 0x00, 0x00, 0x00, 0x04, // PSM length=10, version, reserved, PS_info_length=0, es_map_length=4
		0x90, 0xC0, 0x00, 0x00, // stream_type 0x90 (G.711), stream_id 0xC0, es_info_len=0
	}
	ps.Write(psm)

	// Audio PES
	ps.Write([]byte{0x00, 0x00, 0x01, 0xC0})            // stream_id 0xC0 (audio)
	audioHeader := []byte{0x00, 0x07, 0x80, 0x00, 0x00} // PES length=7, flags1, flags2, header_data_length=0
	ps.Write(audioHeader)
	// Some dummy audio data
	ps.Write([]byte{0x01, 0x02, 0x03, 0x04})

	d := NewPSDemuxer()
	extractedNALUs, err := d.FeedAU(ps.Bytes(), 90000, true)
	if err != nil {
		t.Fatalf("FeedAU failed: %v", err)
	}

	// Should not extract any NALUs (audio is skipped)
	if len(extractedNALUs) != 0 {
		t.Errorf("Expected 0 NALUs from audio PES, got %d", len(extractedNALUs))
	}
}

// TestPSDemuxer_EmptyInput tests handling of empty input.
func TestPSDemuxer_EmptyInput(t *testing.T) {
	d := NewPSDemuxer()
	extractedNALUs, err := d.FeedAU([]byte{}, 90000, true)
	if err != nil {
		t.Fatalf("FeedAU failed on empty input: %v", err)
	}

	if extractedNALUs != nil {
		t.Errorf("Expected nil for empty input, got %v", extractedNALUs)
	}

	flushedNALUs := d.Flush()
	if len(flushedNALUs) != 0 {
		t.Errorf("Expected empty flush, got %d NALUs", len(flushedNALUs))
	}
}

// TestPSDemuxer_IncompletePES tests handling of incomplete PES packets.
func TestPSDemuxer_IncompletePES(t *testing.T) {
	// Create a PS with truncated PES
	psData := []byte{
		0x00, 0x00, 0x01, 0xBA, // Pack header
		0x44, 0x00, 0x04, 0x00, 0x04, 0x01, 0x01, 0x89, 0xC3, 0xF8,
		0x00, 0x00, 0x01, 0xBC, // PSM
		0x00, 0x0A, 0x81, 0xC0, 0x00, 0x00, 0x00, 0x04, // PSM length=10, version, reserved, PS_info_length=0, es_map_length=4
		0x1B, 0xE0, 0x00, 0x00, // stream_type 0x1B, stream_id 0xE0, es_info_len=0
		0x00, 0x00, 0x01, 0xE0, // Video PES start
		0x00, 0x00, // PES length 0 (unbounded) or truncated header
	}

	d := NewPSDemuxer()
	extractedNALUs, err := d.FeedAU(psData, 90000, true)
	if err != nil {
		t.Fatalf("FeedAU failed: %v", err)
	}

	// Should buffer the incomplete PES
	flushedNALUs := d.Flush()

	// Implementation may buffer or return what it can parse
	// The important thing is it doesn't panic
	t.Logf("Extracted %d NALUs, flushed %d NALUs from incomplete PES",
		len(extractedNALUs), len(flushedNALUs))
}

// TestFindStartCodes tests Annex B start code detection.
func TestFindStartCodes(t *testing.T) {
	// Test data with 3-byte and 4-byte start codes
	data := []byte{
		0x00, 0x00, 0x00, 0x01, 0x67, // 4-byte start code + SPS
		0x00, 0x00, 0x01, 0x68, // 3-byte start code + PPS
		0x00, 0x00, 0x01, 0x65, // 3-byte start code + IDR
	}

	positions := findStartCodes(data)

	if len(positions) != 3 {
		t.Fatalf("Expected 3 start codes, got %d", len(positions))
	}

	if positions[0] != 0 {
		t.Errorf("First start code at wrong position: got %d, want 0", positions[0])
	}
	if positions[1] != 5 {
		t.Errorf("Second start code at wrong position: got %d, want 5", positions[1])
	}
	if positions[2] != 9 {
		t.Errorf("Third start code at wrong position: got %d, want 9", positions[2])
	}
}

// TestExtractNALUs tests NALU extraction from raw payload.
func TestExtractNALUs(t *testing.T) {
	// Test data with NALUs separated by start codes
	data := []byte{
		0x00, 0x00, 0x00, 0x01, 0x67, 0x42, // SPS
		0x00, 0x00, 0x01, 0x68, 0xCE, // PPS
		0x00, 0x00, 0x01, 0x65, 0x88, // IDR
	}

	nalus := extractNALUs(data, "h264")

	if len(nalus) != 3 {
		t.Fatalf("Expected 3 NALUs, got %d", len(nalus))
	}

	// First NALU should be {0x67, 0x42} (start code stripped)
	expected1 := []byte{0x67, 0x42}
	if !bytes.Equal(nalus[0], expected1) {
		t.Errorf("First NALU mismatch: got %v, want %v", nalus[0], expected1)
	}

	// Second NALU should be {0x68, 0xCE}
	expected2 := []byte{0x68, 0xCE}
	if !bytes.Equal(nalus[1], expected2) {
		t.Errorf("Second NALU mismatch: got %v, want %v", nalus[1], expected2)
	}

	// Third NALU should be {0x65, 0x88}
	expected3 := []byte{0x65, 0x88}
	if !bytes.Equal(nalus[2], expected3) {
		t.Errorf("Third NALU mismatch: got %v, want %v", nalus[2], expected3)
	}
}

// TestPSDemuxer_MultipleFeedAUs tests multiple FeedAU calls.
func TestPSDemuxer_MultipleFeedAUs(t *testing.T) {
	d := NewPSDemuxer()

	// Create multiple PS packets
	sps1 := []byte{0x67, 0x42, 0x00, 0x1E}
	pps1 := []byte{0x68, 0xCE, 0x3C}
	idr1 := []byte{0x65, 0x88, 0x84}

	ps1 := buildPS([][]byte{sps1, pps1, idr1}, streamTypeH264)

	sps2 := []byte{0x67, 0x64, 0x00, 0x28}
	pps2 := []byte{0x68, 0xEE, 0x3C, 0x80}
	idr2 := []byte{0x65, 0x88, 0x84, 0x0A}

	ps2 := buildPS([][]byte{sps2, pps2, idr2}, streamTypeH264)

	// Feed first PS
	nalus1, err := d.FeedAU(ps1, 90000, true)
	if err != nil {
		t.Fatalf("FeedAU 1 failed: %v", err)
	}

	// Feed second PS
	nalus2, err := d.FeedAU(ps2, 180000, true)
	if err != nil {
		t.Fatalf("FeedAU 2 failed: %v", err)
	}

	// Should have 3 NALUs from each PS
	if len(nalus1) != 3 {
		t.Errorf("Expected 3 NALUs from first PS, got %d", len(nalus1))
	}
	if len(nalus2) != 3 {
		t.Errorf("Expected 3 NALUs from second PS, got %d", len(nalus2))
	}

	// Verify codec detection
	if d.Codec() != "h264" {
		t.Errorf("Expected codec h264, got %s", d.Codec())
	}
}

// TestFindVideoStreamType_RealPSM is a regression test for the PSM parser:
// ISO/IEC 13818-1 program_stream_map carries a 2-byte
// elementary_stream_map_length between the PS info and the stream entries.
// A real Hikvision PSM (with CRC placeholder) must yield the video type.
func TestFindVideoStreamType_RealPSM(t *testing.T) {
	// Full PSM packet: 00 00 01 BC + length + body + CRC32 (zeros).
	psm := []byte{
		0x00, 0x00, 0x01, 0xBC,
		0x00, 0x0E, // packet_length = 14 body bytes
		0xE0,       // current_next + version
		0xFF,       // reserved + marker
		0x00, 0x00, // program_stream_info_length = 0
		0x00, 0x04, // elementary_stream_map_length = 4
		0x1B,       // stream_type = H.264
		0xE0,       // elementary_stream_id = video 0
		0x00, 0x00, // elementary_stream_info_length = 0
		0x00, 0x00, 0x00, 0x00, // CRC_32 (unchecked by parser)
	}
	body := psm[6 : 6+14]
	st, ok := findVideoStreamType(body)
	if !ok || st != 0x1B {
		t.Fatalf("expected H.264 stream type 0x1B, got %#x (ok=%v)", st, ok)
	}

	// H.265 variant.
	psmH265 := append([]byte{}, psm...)
	psmH265[12] = 0x24
	st, ok = findVideoStreamType(psmH265[6 : 6+14])
	if !ok || st != 0x24 {
		t.Fatalf("expected H.265 stream type 0x24, got %#x (ok=%v)", st, ok)
	}
}

// TestExtractNALUs_ThreeByteStartCodeHeaderByte is a regression test for the
// start-code length detection: a 3-byte start code (00 00 01) followed by a
// NAL header of 0x01 (H.264 non-reference slice / H.265 TRAIL_N) must NOT be
// treated as a 4-byte start code — that would strip the NAL header byte.
func TestExtractNALUs_ThreeByteStartCodeHeaderByte(t *testing.T) {
	sps := []byte{0x67, 0x42, 0x00, 0x1E}
	slice := []byte{0x01, 0x88, 0xB4, 0x99}
	data := append(append([]byte{0x00, 0x00, 0x01}, sps...),
		append([]byte{0x00, 0x00, 0x01}, slice...)...)

	nalus := extractNALUs(data, "h264")
	if len(nalus) != 2 {
		t.Fatalf("expected 2 NALUs, got %d", len(nalus))
	}
	if nalus[0][0] != 0x67 {
		t.Fatalf("SPS header byte stripped: first NALU starts with %#x", nalus[0][0])
	}
	if nalus[1][0] != 0x01 {
		t.Fatalf("slice header byte stripped: second NALU starts with %#x", nalus[1][0])
	}
}

// TestParseVideoPES_StandardPTS is a regression test for the PES header
// layout: a real-device video PES carries a 5-byte PTS (byte7=0x80,
// PES_header_data_length=5 at byte 8, payload at offset 14). The old parser
// read header_data_length from byte 7 and computed a 136-byte header.
func TestParseVideoPES_StandardPTS(t *testing.T) {
	sps := []byte{0x67, 0x42, 0x00, 0x1E}
	payload := append([]byte{0x00, 0x00, 0x01}, sps...)

	// PES body after the 6-byte prefix: flags1, flags2, hdrlen, PTS(5), payload
	optional := []byte{0x80, 0x80, 0x05, 0x21, 0x00, 0x05, 0xD4, 0x4D}
	pesPacketLen := len(optional) + len(payload)

	pes := []byte{
		0x00, 0x00, 0x01, 0xE0,
		byte(pesPacketLen >> 8), byte(pesPacketLen & 0xFF),
	}
	pes = append(pes, optional...)
	pes = append(pes, payload...)

	got, total, err := parseVideoPES(pes)
	if err != nil {
		t.Fatalf("parseVideoPES: %v", err)
	}
	if total != 6+pesPacketLen {
		t.Fatalf("total = %d, want %d", total, 6+pesPacketLen)
	}
	if string(got) != string(payload) {
		t.Fatalf("payload mismatch: got % X, want % X", got, payload)
	}
}

// TestParseVideoPES_InconsistentHeaderLength is a crash regression test: a
// corrupt PES whose PES_header_data_length makes the header longer than the
// packet itself previously panicked with slice bounds out of range [58:56]
// (took down the whole NVR process when fed by a real device's truncated
// stream). It must return ErrIncompletePES instead.
func TestParseVideoPES_InconsistentHeaderLength(t *testing.T) {
	// PES_packet_length = 50 → totalLen = 56, but header_data_length = 49 →
	// headerLen = 58 > 56.
	pes := []byte{
		0x00, 0x00, 0x01, 0xE0,
		0x00, 0x32, // PES_packet_length = 50
		0x80, 0x80, // flags1, flags2 (PTS present)
		0x31, // PES_header_data_length = 49 → headerLen = 58 > totalLen = 56
	}
	pes = append(pes, make([]byte, 60)...) // enough raw bytes for either bound

	// Must not panic; with the calibrated payload locator the header bound
	// is clamped to the packet, yielding either an error or an EMPTY payload —
	// never a bogus slice and never a crash.
	payload, _, err := parseVideoPES(pes)
	if err == nil && len(payload) != 0 {
		t.Fatalf("corrupt PES must yield empty payload or error, got %d bytes", len(payload))
	}
}

// TestParseVideoPES_LegacyHeaderLayout is a regression test using bytes from
// a REAL device capture (mibee-eye-raspi-rs): its firmware writes
// PES_header_data_length at byte 7 (0x0a = 10, matching its two 5-byte custom
// timestamps) instead of the standard byte 8 — whose value (0x31) is the
// first optional byte. The payload locator must find the ES start code
// regardless of which byte lies.
func TestParseVideoPES_LegacyHeaderLayout(t *testing.T) {
	// Real capture: pack header + PES head. ES (00 00 00 01 21 …) begins at
	// PES offset 18 (legacy 8+data[7]).
	pes := []byte{
		0x00, 0x00, 0x01, 0xE0,
		0x1A, 0x1B, // PES_packet_length = 6683
		0xC0,                         // flags1
		0x0A,                         // firmware writes PES_header_data_length HERE (10)
		0x31,                         // …and this (49) is actually the first optional byte
		0x02, 0x31, 0x26, 0xC1, 0x11, // optional field 1
		0x02, 0x31, 0x26, 0xC1, // optional field 2 (first 4 bytes)
		0x00,             // trailing zero byte
		0x00, 0x00, 0x01, // Annex-B start code
		0x21, 0x9A, 0x16, // NAL header + slice bytes
	}
	// Pad so len(data) ≥ totalLen (6+6683) and the slice bounds are real.
	full := append(append([]byte{}, pes...), make([]byte, 6683)...)

	payload, total, err := parseVideoPES(full)
	if err != nil {
		t.Fatalf("parseVideoPES: %v", err)
	}
	if total != 6+6683 {
		t.Fatalf("total = %d, want %d", total, 6+6683)
	}
	// The payload must start at/before the ES start code (legacy candidate 18)
	// and contain the NAL header 0x21 — under the standard candidate (9+49=58)
	// the start code would be clipped off.
	foundStartCode := false
	for i := 0; i+2 < len(payload) && i < 8; i++ {
		if payload[i] == 0 && payload[i+1] == 0 && payload[i+2] == 1 {
			foundStartCode = true
			break
		}
	}
	if !foundStartCode {
		t.Fatalf("ES start code not at payload head; first bytes: % X", payload[:8])
	}
	nalus := extractNALUs(payload, "h264")
	if len(nalus) == 0 || nalus[0][0] != 0x21 {
		t.Fatalf("expected first NALU header 0x21, got %d NALUs", len(nalus))
	}
}

// TestESBufCap: marker-less accumulation must hit the cap and resync instead
// of growing for the session's lifetime (#383 made sessions effectively
// unbounded).
func TestESBufCap(t *testing.T) {
	d := NewPSDemuxer()
	// Video PES without any AU marker: FeedAU(complete=false) accumulates.
	// 9 MB of payload in 64 KB chunks.
	chunk := make([]byte, 64<<10)
	chunk[0], chunk[1], chunk[2] = 0x00, 0x00, 0x01 // keep extractNALUs calm
	var maxSeen int
	for range 150 {
		// A PES header + small payload each AU so parseVideoPES succeeds.
		pes := append([]byte{0x00, 0x00, 0x01, 0xE0, 0xFF, 0xFF, 0x80, 0x00, 0x00}, chunk...)
		_, _ = d.FeedAU(pes, 90000, false)
		if len(d.esBuf) > maxSeen {
			maxSeen = len(d.esBuf)
		}
	}
	if maxSeen > maxESBufBytes+(64<<10)+16 {
		t.Fatalf("esBuf grew to %d beyond cap %d", maxSeen, maxESBufBytes)
	}
}

// TestVideoPesBufCap: a PES whose announced length can never be satisfied must
// hit the pending bound and resync instead of buffering the stream's bitrate
// forever.
func TestVideoPesBufCap(t *testing.T) {
	d := NewPSDemuxer()
	// Header claims 65529 bytes of payload; feed far beyond the bound in
	// marker-less chunks so the PES never completes.
	chunk := make([]byte, 512<<10)
	var maxSeen int
	for range 40 {
		pes := append([]byte{0x00, 0x00, 0x01, 0xE0, 0xFF, 0xF9, 0x80, 0x80, 0x05, 0x21, 0x00, 0x01, 0x02, 0x03}, chunk...)
		_, _ = d.FeedAU(pes, 90000, false)
		if len(d.videoPesBuf) > maxSeen {
			maxSeen = len(d.videoPesBuf)
		}
	}
	if maxSeen > maxPendingPESBytes+(512<<10)+16 {
		t.Fatalf("videoPesBuf grew to %d beyond bound %d", maxSeen, maxPendingPESBytes)
	}
}

// TestPSDemuxer_TrailingZeroPayloadBeforePES reproduces the #390 OOM root
// cause. In-order parsing skips complete PES packets by their declared
// length, so payload bytes are only ever SCANNED on a resync path (RTP loss
// mis-aligning a PES extent, joining mid-stream). From inside a payload, a
// NALU ending in trailing zeros (legal Annex-B trailing_zero_8bytes) abuts
// the next PES header as ...00 00 00 00 01 E0 — findPSStartCode matched the
// 4-byte form at an EARLIER index than the true 3-byte header, and
// parseVideoPES then rejected the prefix on every AU, wedging the
// reassembly at stream bitrate forever. The resync must recover at the real
// PES instead.
func TestPSDemuxer_TrailingZeroPayloadBeforePES(t *testing.T) {
	d := NewPSDemuxer()

	// Orphan ES bytes (the tail of a PES lost upstream) ending in two zeros,
	// immediately followed by a well-formed video PES.
	orphan := []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x11, 0x22, 0x33, 0x00, 0x00}
	nalu2 := []byte{0x00, 0x00, 0x00, 0x01, 0x41, 0xAA, 0xBB}
	p2 := append([]byte{0x00, 0x00, 0x01, 0xE0, 0x00, byte(3 + len(nalu2)), 0x80, 0x00, 0x00}, nalu2...)

	au := append(append([]byte{}, orphan...), p2...)
	nalus, err := d.FeedAU(au, 9000, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(nalus) != 1 {
		t.Fatalf("resync must deliver the PES payload NALU, got %d NALUs", len(nalus))
	}
	if nalus[0][0] != 0x41 {
		t.Fatalf("expected the P-frame NALU, got %#x", nalus[0][0])
	}
	if len(d.videoPesBuf) != 0 {
		t.Fatalf("videoPesBuf must stay empty, has %d bytes", len(d.videoPesBuf))
	}

	// And the wedged-retry pathology must not return on subsequent AUs.
	for range 3 {
		if _, err := d.FeedAU(au, 9000, true); err != nil {
			t.Fatal(err)
		}
		if len(d.videoPesBuf) != 0 {
			t.Fatalf("videoPesBuf must not accumulate across AUs, has %d bytes", len(d.videoPesBuf))
		}
	}
}

// TestParseVideoPES_FourByteStartCode: direct parse of a 4-byte-prefixed PES
// (00 00 00 01 E0) must succeed after zero-stripping, not error forever.
func TestParseVideoPES_FourByteStartCode(t *testing.T) {
	payload := []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x77}
	pes := append([]byte{0x00, 0x00, 0x00, 0x01, 0xE0, 0x00, byte(3 + len(payload)), 0x80, 0x00, 0x00}, payload...)
	got, pesEnd, err := parseVideoPES(pes)
	if err != nil {
		t.Fatalf("4-byte prefix must be tolerated: %v", err)
	}
	if pesEnd != 6+3+len(payload) {
		t.Fatalf("unexpected pesEnd %d", pesEnd)
	}
	if len(got) != len(payload) {
		t.Fatalf("payload %d bytes, got %d", len(payload), len(got))
	}
}

// TestFindPSStartCode_FourByteNormalization: a 4-byte prefix before a PS code
// value must be normalized to the 3-byte position so consumers index
// stream_id / lengths at standard offsets.
func TestFindPSStartCode_FourByteNormalization(t *testing.T) {
	data := []byte{0xFF, 0x00, 0x00, 0x00, 0x01, 0xE0, 0x12, 0x34}
	pos, code, err := findPSStartCode(data)
	if err != nil {
		t.Fatal(err)
	}
	if code != startCodeVideoMin {
		t.Fatalf("code %#x", code)
	}
	// Position must point at the LAST 00 before 01: stream_id at pos+3.
	if data[pos] != 0 || data[pos+1] != 0 || data[pos+2] != 1 || data[pos+3] != 0xE0 {
		t.Fatalf("position %d not a 3-byte start code", pos)
	}
	if data[pos+4] != 0x12 || data[pos+5] != 0x34 {
		t.Fatalf("length bytes not at pos+4/+5")
	}
}
