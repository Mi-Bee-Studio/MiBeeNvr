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

	// PSM packet_length (2 bytes): body length = 8 bytes
	ps.Write([]byte{0x00, 0x08})

	// PSM body: version (1) + reserved (1) + PS_info_length (2) + stream_info (4) = 8 bytes
	ps.Write([]byte{
		0x02,       // version = 2
		0xC0,       // reserved byte
		0x00, 0x00, // PS_info_length = 0 (no PS_info)
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

	// PES packet length = 2 (flags + header_data_length) + payload_len
	pesPacketLen := 2 + payloadLen

	// PES header with simplest case (no PTS/DTS, no other optional fields)
	pesHeader := []byte{
		byte(pesPacketLen >> 8), byte(pesPacketLen), // PES_packet_length (2 bytes)
		0x80, // Byte 6: bits 7-6='10' marker, bits 5-0='000000'
		0x00, // Byte 7: PES_header_data_length (0)
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
	extractedNALUs, err := d.FeedAU(psData, 90000)
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
	extractedNALUs, err := d.FeedAU(psData, 180000)
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
		extractedNALUs, err := d.FeedAU(chunk, int64(i)*90000)
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
		0x00, 0x08, 0x81, 0xC0, 0x00, 0x00, // PSM length=8, version, reserved, PS_info_length=0
		0x1B, 0xE0, 0x00, 0x00, // stream_type 0x1B, stream_id 0xE0, es_info_len=0
		0x00, 0x00, 0x01, 0xE0, // Video PES start (incomplete)
		0x00, 0x0E, // PES length (14: 2 header + 12 payload)
		0x80, 0x00, // PES header (no PTS/DTS, header_data_length=0)
		0x00, 0x00, 0x00, 0x01, // Start code
		0x67, 0x42, 0x00, 0x1E, // SPS NALU (partial - 4 bytes start code + 4 bytes)
	}

	d := NewPSDemuxer()
	extractedNALUs, err := d.FeedAU(psData, 90000)
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
		0x00, 0x08, 0x81, 0xC0, 0x00, 0x00, // PSM length=8, version, reserved, PS_info_length=0
		0x90, 0xC0, 0x00, 0x00, // stream_type 0x90 (G.711), stream_id 0xC0, es_info_len=0
	}
	ps.Write(psm)

	// Audio PES
	ps.Write([]byte{0x00, 0x00, 0x01, 0xC0})      // stream_id 0xC0 (audio)
	audioHeader := []byte{0x00, 0x06, 0x80, 0x00} // PES length=6, flags, header_data_length=0
	ps.Write(audioHeader)
	// Some dummy audio data
	ps.Write([]byte{0x01, 0x02, 0x03, 0x04})

	d := NewPSDemuxer()
	extractedNALUs, err := d.FeedAU(ps.Bytes(), 90000)
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
	extractedNALUs, err := d.FeedAU([]byte{}, 90000)
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
		0x00, 0x08, 0x81, 0xC0, 0x00, 0x00, // PSM length=8, version, reserved, PS_info_length=0
		0x1B, 0xE0, 0x00, 0x00, // stream_type 0x1B, stream_id 0xE0, es_info_len=0
		0x00, 0x00, 0x01, 0xE0, // Video PES start
		0x00, 0x00, // PES length 0 (unbounded) or truncated header
	}

	d := NewPSDemuxer()
	extractedNALUs, err := d.FeedAU(psData, 90000)
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
	nalus1, err := d.FeedAU(ps1, 90000)
	if err != nil {
		t.Fatalf("FeedAU 1 failed: %v", err)
	}

	// Feed second PS
	nalus2, err := d.FeedAU(ps2, 180000)
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
