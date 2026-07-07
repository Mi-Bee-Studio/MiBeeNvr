// Package timelapse — Pure Go H.264 NAL → MP4 muxer tests.
//
// These tests verify that H264GoMerger:
//   - Produces valid MP4 output (verified via abema/go-mp4 box structure)
//   - Correctly splits Annex-B NALUs and builds length-prefixed samples
//   - Parses H.264 SPS for dimensions
//   - Handles edge cases (empty, single frame, context cancellation)
//   - Builds with CGO_ENABLED=0
package timelapse

import (
	"bytes"
	"context"
	"fmt"
	"math/bits"
	"os"
	"path/filepath"
	"testing"

	"github.com/abema/go-mp4"
)

// --- Test H.264 frame generators ---

// buildTestH264Frame creates a complete H.264 access unit in Annex-B format
// containing SPS, PPS, and an IDR slice, suitable for writing to a .h264 file.
func buildTestH264Frame(sps, pps, idr []byte) []byte {
	var buf bytes.Buffer
	// Write SPS with start code
	buf.Write([]byte{0x00, 0x00, 0x00, 0x01})
	buf.Write(sps)
	// Write PPS with start code
	buf.Write([]byte{0x00, 0x00, 0x00, 0x01})
	buf.Write(pps)
	// Write IDR with start code
	buf.Write([]byte{0x00, 0x00, 0x00, 0x01})
	buf.Write(idr)
	return buf.Bytes()
}

// buildTestSPS constructs a minimal valid H.264 SPS NAL unit for the given
// dimensions. Width and height must be multiples of 16.
func buildTestSPS(width, height int) []byte {
	if width%16 != 0 || height%16 != 0 {
		panic(fmt.Sprintf("buildTestSPS: width(%d) and height(%d) must be multiples of 16", width, height))
	}
	buf := &bytes.Buffer{}
	// NAL header: forbidden=0, nal_ref_idc=3, nal_unit_type=7 (SPS)
	buf.WriteByte(0x67)

	// profile_idc: Baseline (66)
	buf.WriteByte(66)

	// constraint_set0_flag=1 (Baseline), other flags=0
	buf.WriteByte(0x80)

	// level_idc: 30 (Level 3.0)
	buf.WriteByte(30)

	// Write exp-golomb coded fields via bit-level packer.
	bw := &testBitWriter{}

	// seq_parameter_set_id = 0
	bw.writeUE(0)
	// log2_max_frame_num_minus4 = 0
	bw.writeUE(0)
	// pic_order_cnt_type = 0
	bw.writeUE(0)
	// log2_max_pic_order_cnt_lsb_minus4 = 0
	bw.writeUE(0)
	// max_num_ref_frames = 0
	bw.writeUE(0)
	// gaps_in_frame_num_value_allowed_flag = false
	bw.writeBit(0)
	// pic_width_in_mbs_minus1
	picWidthInMBs := uint32(width/16 - 1)
	bw.writeUE(picWidthInMBs)
	// pic_height_in_map_units_minus1
	picHeightInMapUnits := uint32(height/16 - 1)
	bw.writeUE(picHeightInMapUnits)
	// frame_mbs_only_flag = 1 (progressive)
	bw.writeBit(1)
	// direct_8x8_inference_flag = 1
	bw.writeBit(1)
	// frame_cropping_flag = 0
	bw.writeBit(0)
	// vui_parameters_present_flag = 0
	bw.writeBit(0)

	buf.Write(bw.bytes())
	return buf.Bytes()
}

// buildTestSPSHighProfile constructs a minimal High Profile SPS for testing
// high-profile-specific field parsing.
func buildTestSPSHighProfile(width, height int) []byte {
	if width%16 != 0 || height%16 != 0 {
		panic(fmt.Sprintf("buildTestSPS: width(%d) and height(%d) must be multiples of 16", width, height))
	}
	buf := &bytes.Buffer{}
	// NAL header: forbidden=0, nal_ref_idc=3, nal_unit_type=7 (SPS)
	buf.WriteByte(0x67)

	// profile_idc: High (100)
	buf.WriteByte(100)

	// constraint_set0_flag=0, others=0
	buf.WriteByte(0x00)

	// level_idc: 40 (Level 4.0)
	buf.WriteByte(40)

	bw := &testBitWriter{}

	// seq_parameter_set_id = 0
	bw.writeUE(0)

	// chroma_format_idc = 1 (4:2:0)
	bw.writeUE(1)
	// bit_depth_luma_minus8 = 0
	bw.writeUE(0)
	// bit_depth_chroma_minus8 = 0
	bw.writeUE(0)
	// qpprime_y_zero_transform_bypass_flag = 0
	bw.writeBit(0)
	// seq_scaling_matrix_present_flag = 0
	bw.writeBit(0)

	// log2_max_frame_num_minus4 = 0
	bw.writeUE(0)
	// pic_order_cnt_type = 0
	bw.writeUE(0)
	// log2_max_pic_order_cnt_lsb_minus4 = 0
	bw.writeUE(0)
	// max_num_ref_frames = 0
	bw.writeUE(0)
	// gaps_in_frame_num_value_allowed_flag = 0
	bw.writeBit(0)
	// pic_width_in_mbs_minus1
	bw.writeUE(uint32(width/16 - 1))
	// pic_height_in_map_units_minus1
	bw.writeUE(uint32(height/16 - 1))
	// frame_mbs_only_flag = 1
	bw.writeBit(1)
	// direct_8x8_inference_flag = 1
	bw.writeBit(1)
	// frame_cropping_flag = 0
	bw.writeBit(0)
	// vui_parameters_present_flag = 0
	bw.writeBit(0)

	buf.Write(bw.bytes())
	return buf.Bytes()
}

// buildTestPPS constructs a minimal valid H.264 PPS NAL unit.
func buildTestPPS() []byte {
	buf := &bytes.Buffer{}
	// NAL header: forbidden=0, nal_ref_idc=3, nal_unit_type=8 (PPS)
	buf.WriteByte(0x68)

	bw := &testBitWriter{}

	// pic_parameter_set_id = 0
	bw.writeUE(0)
	// seq_parameter_set_id = 0
	bw.writeUE(0)
	// entropy_coding_mode_flag = 0 (CAVLC)
	bw.writeBit(0)
	// pic_order_present_flag = 0
	bw.writeBit(0)
	// num_slice_groups_minus1 = 0
	bw.writeUE(0)

	buf.Write(bw.bytes())
	return buf.Bytes()
}

// buildTestPPSWithCABAC constructs a PPS with CABAC enabled for variety.
func buildTestPPSWithCABAC() []byte {
	buf := &bytes.Buffer{}
	buf.WriteByte(0x68)

	bw := &testBitWriter{}
	bw.writeUE(0)  // pic_parameter_set_id
	bw.writeUE(0)  // seq_parameter_set_id
	bw.writeBit(1) // entropy_coding_mode_flag = 1 (CABAC)
	bw.writeBit(0) // pic_order_present_flag
	bw.writeUE(0)  // num_slice_groups_minus1

	buf.Write(bw.bytes())
	return buf.Bytes()
}

// buildTestIDR constructs a minimal H.264 IDR slice NAL unit.
// This is a simplified slice header with minimal data; the exact content
// is less important than having a valid NAL type 5.
func buildTestIDR() []byte {
	buf := &bytes.Buffer{}
	// NAL header: forbidden=0, nal_ref_idc=3, nal_unit_type=5 (IDR)
	buf.WriteByte(0x65)

	bw := &testBitWriter{}
	// first_mb_in_slice = 0
	bw.writeUE(0)
	// slice_type = 2 (I slice) - exp-golomb(2) = "011"
	bw.writeUE(2)
	// pic_parameter_set_id = 0
	bw.writeUE(0)
	// frame_num = 0 (using log2_max_frame_num_minus4=0 → 1 bit)
	bw.writeBits(0, 1)
	// idr_pic_id = 0 (ue(v))
	bw.writeUE(0)

	// Minimal additional data to form a valid-looking slice.
	bw.writeBit(0) // no more slices (end_of_slice_flag equivalent)
	// Add a few more bits for minimum slice data validity.
	bw.writeBits(0x80, 8) // padding

	buf.Write(bw.bytes())
	return buf.Bytes()
}

// --- Bit-level writer for test SPS/PPS/IDR construction ---

type testBitWriter struct {
	data   []byte
	pos    int // bit position (0-7) in current byte
	offset int // byte offset being written
}

func (w *testBitWriter) ensureByte() {
	if w.offset >= len(w.data) {
		w.data = append(w.data, 0)
	}
}

func (w *testBitWriter) writeBit(val uint8) {
	w.ensureByte()
	if val != 0 {
		w.data[w.offset] |= (1 << (7 - w.pos))
	}
	w.pos++
	if w.pos >= 8 {
		w.pos = 0
		w.offset++
	}
}

func (w *testBitWriter) writeBits(val uint32, nBits int) {
	for i := nBits - 1; i >= 0; i-- {
		w.writeBit(uint8((val >> i) & 1))
	}
}

func (w *testBitWriter) writeUE(val uint32) {
	if val == 0 {
		w.writeBit(1)
		return
	}
	codeNum := val + 1
	leadingZeros := bits.Len32(codeNum) - 1
	for range leadingZeros {
		w.writeBit(0)
	}
	w.writeBit(1)
	w.writeBits(codeNum, leadingZeros)
}

func (w *testBitWriter) bytes() []byte {
	// Pad remaining bits with 1s (RBSP stop bit + alignment).
	if w.pos > 0 && w.pos < 8 {
		remaining := 8 - w.pos
		for range remaining {
			w.writeBit(1)
		}
	}
	return w.data
}

// --- Tests ---

// TestH264GoMerger_ValidOutput verifies the merger produces a valid MP4.
func TestH264GoMerger_ValidOutput(t *testing.T) {
	tmpDir := t.TempDir()
	framesDir := filepath.Join(tmpDir, "frames")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sps := buildTestSPS(640, 480)
	pps := buildTestPPS()
	idr := buildTestIDR()

	// Generate 5 test frame files.
	for i := range 5 {
		framePath := filepath.Join(framesDir, fmt.Sprintf("frame_%06d.h264", i))
		frameData := buildTestH264Frame(sps, pps, idr)
		if err := os.WriteFile(framePath, frameData, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	merger := NewH264GoMerger()
	if !merger.CanMerge() {
		t.Fatal("H264GoMerger.CanMerge() should return true")
	}

	result, err := merger.Merge(context.Background(), framesDir, outputPath, 1)
	if err != nil {
		t.Fatalf("Merge failed: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("Merge result has error: %s", result.Error)
	}

	// Verify output file exists.
	fi, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("output file not created: %v", err)
	}
	if fi.Size() == 0 {
		t.Fatal("output file is empty")
	}

	// Validate MP4 box structure.
	f, err := os.Open(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	foundFtyp := false
	foundMoov := false
	foundMdat := false
	for {
		boxInfo, err := mp4.ReadBoxInfo(f)
		if err != nil {
			break
		}
		boxName := string(boxInfo.Type[:])
		switch boxName {
		case "ftyp":
			foundFtyp = true
		case "moov":
			foundMoov = true
		case "mdat":
			foundMdat = true
		}
		if _, err := f.Seek(int64(boxInfo.Offset)+int64(boxInfo.Size), 0); err != nil {
			break
		}
		if boxInfo.Size == 0 {
			break
		}
	}

	if !foundFtyp {
		t.Error("Missing ftyp box")
	}
	if !foundMoov {
		t.Error("Missing moov box")
	}
	if !foundMdat {
		t.Error("Missing mdat box")
	}

	t.Logf("Output MP4: %d bytes, frames=%d, duration=%.1fs, tier=%s",
		fi.Size(), result.FramesMerged, result.Duration, result.Tier)
}

// TestH264GoMerger_EmptyDir verifies the merger handles empty input gracefully.
func TestH264GoMerger_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	emptyDir := filepath.Join(tmpDir, "empty")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatal(err)
	}

	merger := NewH264GoMerger()
	_, err := merger.Merge(context.Background(), emptyDir, outputPath, 1)
	if err == nil {
		t.Error("Expected error for empty directory, got nil")
	}
}

// TestH264GoMerger_SingleFrame verifies the merger handles a single frame correctly.
func TestH264GoMerger_SingleFrame(t *testing.T) {
	tmpDir := t.TempDir()
	framesDir := filepath.Join(tmpDir, "frames")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sps := buildTestSPS(320, 240)
	pps := buildTestPPS()
	idr := buildTestIDR()

	framePath := filepath.Join(framesDir, "frame_000000.h264")
	frameData := buildTestH264Frame(sps, pps, idr)
	if err := os.WriteFile(framePath, frameData, 0o644); err != nil {
		t.Fatal(err)
	}

	merger := NewH264GoMerger()
	result, err := merger.Merge(context.Background(), framesDir, outputPath, 1)
	if err != nil {
		t.Fatalf("Merge failed: %v", err)
	}

	fi, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("Output file not found: %v", err)
	}

	if result.FramesMerged != 1 {
		t.Errorf("Expected 1 frame merged, got %d", result.FramesMerged)
	}
	if fi.Size() == 0 {
		t.Error("Output file is empty")
	}

	// Validate MP4 structure for single frame.
	f, err := os.Open(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	foundFtyp := false
	foundMdat := false
	for {
		boxInfo, err := mp4.ReadBoxInfo(f)
		if err != nil {
			break
		}
		boxName := string(boxInfo.Type[:])
		switch boxName {
		case "ftyp":
			foundFtyp = true
		case "mdat":
			foundMdat = true
		}
		if _, err := f.Seek(int64(boxInfo.Offset)+int64(boxInfo.Size), 0); err != nil {
			break
		}
		if boxInfo.Size == 0 {
			break
		}
	}

	if !foundFtyp {
		t.Error("Missing ftyp box")
	}
	if !foundMdat {
		t.Error("Missing mdat box")
	}

	t.Logf("Single frame output: %d bytes", fi.Size())
}

// TestH264GoMerger_ContextCancellation verifies cancellation during merge.
func TestH264GoMerger_ContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()
	framesDir := filepath.Join(tmpDir, "frames")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sps := buildTestSPS(640, 480)
	pps := buildTestPPS()
	idr := buildTestIDR()

	// Generate 20 frames.
	for i := range 20 {
		framePath := filepath.Join(framesDir, fmt.Sprintf("frame_%06d.h264", i))
		frameData := buildTestH264Frame(sps, pps, idr)
		if err := os.WriteFile(framePath, frameData, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	merger := NewH264GoMerger()
	_, err := merger.Merge(ctx, framesDir, outputPath, 1)
	if err == nil {
		t.Error("Expected error for cancelled context, got nil")
	}
}

// TestH264GoMerger_InterfaceCompliance verifies H264GoMerger satisfies the interface.
func TestH264GoMerger_InterfaceCompliance(t *testing.T) {
	var _ TimelapseMerger = (*H264GoMerger)(nil)
}

// TestH264GoMerger_MergeResult verifies merge result fields are populated.
func TestH264GoMerger_MergeResult(t *testing.T) {
	tmpDir := t.TempDir()
	framesDir := filepath.Join(tmpDir, "frames")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sps := buildTestSPS(640, 480)
	pps := buildTestPPS()
	idr := buildTestIDR()

	for i := range 3 {
		framePath := filepath.Join(framesDir, fmt.Sprintf("frame_%06d.h264", i))
		frameData := buildTestH264Frame(sps, pps, idr)
		if err := os.WriteFile(framePath, frameData, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	merger := NewH264GoMerger()
	result, err := merger.Merge(context.Background(), framesDir, outputPath, 1)
	if err != nil {
		t.Fatalf("Merge failed: %v", err)
	}

	if result.Tier != TierGo {
		t.Errorf("Expected Tier=%q, got %q", TierGo, result.Tier)
	}
	if result.FramesMerged != 3 {
		t.Errorf("Expected 3 frames merged, got %d", result.FramesMerged)
	}
	if result.Duration <= 0 {
		t.Errorf("Expected positive duration, got %f", result.Duration)
	}
	if result.OutputPath != outputPath {
		t.Errorf("Expected OutputPath=%q, got %q", outputPath, result.OutputPath)
	}
	if result.Error != "" {
		t.Errorf("Expected no error, got %q", result.Error)
	}

	t.Logf("MergeResult: tier=%s, frames=%d, duration=%.1fs, path=%s",
		result.Tier, result.FramesMerged, result.Duration, result.OutputPath)
}

// TestH264GoMerger_MissingSPS verifies error when first frame has no SPS.
func TestH264GoMerger_MissingSPS(t *testing.T) {
	tmpDir := t.TempDir()
	framesDir := filepath.Join(tmpDir, "frames")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a frame with only PPS and IDR (no SPS).
	pps := buildTestPPS()
	idr := buildTestIDR()

	var buf bytes.Buffer
	buf.Write([]byte{0x00, 0x00, 0x00, 0x01})
	buf.Write(pps)
	buf.Write([]byte{0x00, 0x00, 0x00, 0x01})
	buf.Write(idr)

	framePath := filepath.Join(framesDir, "frame_000000.h264")
	if err := os.WriteFile(framePath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	merger := NewH264GoMerger()
	_, err := merger.Merge(context.Background(), framesDir, outputPath, 1)
	if err == nil {
		t.Error("Expected error for missing SPS, got nil")
	}
}

// TestH264GoMerger_MissingPPS verifies error when first frame has no PPS.
func TestH264GoMerger_MissingPPS(t *testing.T) {
	tmpDir := t.TempDir()
	framesDir := filepath.Join(tmpDir, "frames")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a frame with only SPS and IDR (no PPS).
	sps := buildTestSPS(640, 480)
	idr := buildTestIDR()

	var buf bytes.Buffer
	buf.Write([]byte{0x00, 0x00, 0x00, 0x01})
	buf.Write(sps)
	buf.Write([]byte{0x00, 0x00, 0x00, 0x01})
	buf.Write(idr)

	framePath := filepath.Join(framesDir, "frame_000000.h264")
	if err := os.WriteFile(framePath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	merger := NewH264GoMerger()
	_, err := merger.Merge(context.Background(), framesDir, outputPath, 1)
	if err == nil {
		t.Error("Expected error for missing PPS, got nil")
	}
}

// TestH264GoMerger_InvalidFPSEdgeCase tests FPS <= 0 is handled gracefully.
func TestH264GoMerger_InvalidFPSEdgeCase(t *testing.T) {
	tmpDir := t.TempDir()
	framesDir := filepath.Join(tmpDir, "frames")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sps := buildTestSPS(640, 480)
	pps := buildTestPPS()
	idr := buildTestIDR()

	framePath := filepath.Join(framesDir, "frame_000000.h264")
	frameData := buildTestH264Frame(sps, pps, idr)
	if err := os.WriteFile(framePath, frameData, 0o644); err != nil {
		t.Fatal(err)
	}

	merger := NewH264GoMerger()
	_, err := merger.Merge(context.Background(), framesDir, outputPath, 0)
	if err != nil {
		t.Fatalf("Merge should not fail with fps=0: %v", err)
	}

	// Also test negative fps.
	_, err = merger.Merge(context.Background(), framesDir, outputPath, -5)
	if err != nil {
		t.Fatalf("Merge should not fail with fps=-5: %v", err)
	}
}

// TestH264GoMerger_HighProfileDimensions tests dimension parsing with High Profile SPS.
func TestH264GoMerger_HighProfileDimensions(t *testing.T) {
	tmpDir := t.TempDir()
	framesDir := filepath.Join(tmpDir, "frames")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// High Profile SPS with 1280x720.
	sps := buildTestSPSHighProfile(1280, 720)
	pps := buildTestPPSWithCABAC()
	idr := buildTestIDR()

	framePath := filepath.Join(framesDir, "frame_000000.h264")
	frameData := buildTestH264Frame(sps, pps, idr)
	if err := os.WriteFile(framePath, frameData, 0o644); err != nil {
		t.Fatal(err)
	}

	merger := NewH264GoMerger()
	result, err := merger.Merge(context.Background(), framesDir, outputPath, 10)
	if err != nil {
		t.Fatalf("Merge failed: %v", err)
	}

	if result.FramesMerged != 1 {
		t.Errorf("Expected 1 frame merged, got %d", result.FramesMerged)
	}

	// Verify the output is valid.
	fi, err := os.Stat(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() == 0 {
		t.Error("Output file is empty")
	}

	t.Logf("High profile output: %d bytes, duration=%.1fs", fi.Size(), result.Duration)
}

// --- Unit tests for internal helpers ---

func TestSplitAnnexB(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		want     int   // number of NALUs
		wantType []int // NAL types for first few
	}{
		{
			name: "three NALUs with 4-byte start codes",
			data: func() []byte {
				var b bytes.Buffer
				b.Write([]byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00}) // SPS
				b.Write([]byte{0x00, 0x00, 0x00, 0x01, 0x68, 0xEE})       // PPS
				b.Write([]byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x88})       // IDR
				return b.Bytes()
			}(),
			want:     3,
			wantType: []int{7, 8, 5},
		},
		{
			name:     "single NALU",
			data:     []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x1E},
			want:     1,
			wantType: []int{7},
		},
		{
			name:     "3-byte start code",
			data:     []byte{0x00, 0x00, 0x01, 0x67, 0x42, 0x00, 0x00, 0x00, 0x01, 0x65},
			want:     2,
			wantType: []int{7, 5},
		},
		{
			name:     "empty input",
			data:     []byte{},
			want:     0,
			wantType: nil,
		},
		{
			name:     "no start code (raw NALU)",
			data:     []byte{0x67, 0x42, 0x00},
			want:     1,
			wantType: []int{7},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nalus := splitAnnexB(tt.data)
			if len(nalus) != tt.want {
				t.Errorf("got %d NALUs, want %d", len(nalus), tt.want)
			}
			for i, wantType := range tt.wantType {
				if i < len(nalus) {
					gotType := int(nalus[i][0] & 0x1F)
					if gotType != wantType {
						t.Errorf("NAL[%d] type = %d, want %d", i, gotType, wantType)
					}
				}
			}
		})
	}
}

func TestBuildAvcC(t *testing.T) {
	sps := buildTestSPS(640, 480)
	pps := buildTestPPS()

	avcC := buildAvcC(sps, pps)

	// avcC header: version(1) + profile(1) + compat(1) + level(1) + reserved+length(1) + reserved+numSPS(1)
	if len(avcC) < 6 {
		t.Fatalf("avcC too short: %d bytes", len(avcC))
	}

	if avcC[0] != 1 {
		t.Errorf("configurationVersion = %d, want 1", avcC[0])
	}

	// Profile should match SPS[1].
	if avcC[1] != sps[1] {
		t.Errorf("profile = %d, want %d", avcC[1], sps[1])
	}

	// numOfSequenceParameterSets should be 1.
	numSPS := avcC[5] & 0x1F
	if numSPS != 1 {
		t.Errorf("numSPS = %d, want 1", numSPS)
	}

	// Verify SPS length field and data.
	spsLen := int(avcC[6])<<8 | int(avcC[7])
	if spsLen != len(sps) {
		t.Errorf("SPS length = %d, want %d", spsLen, len(sps))
	}

	// Verify PPS count.
	ppsOffset := 8 + spsLen
	if ppsOffset >= len(avcC) {
		t.Fatal("avcC too short for PPS data")
	}
	numPPS := avcC[ppsOffset]
	if numPPS != 1 {
		t.Errorf("numPPS = %d, want 1", numPPS)
	}

	// Verify PPS length and data.
	ppsLen := int(avcC[ppsOffset+1])<<8 | int(avcC[ppsOffset+2])
	if ppsLen != len(pps) {
		t.Errorf("PPS length = %d, want %d", ppsLen, len(pps))
	}

	t.Logf("avcC: %d bytes, SPS=%d bytes, PPS=%d bytes", len(avcC), spsLen, ppsLen)
}

func TestParseH264Dimensions(t *testing.T) {
	tests := []struct {
		name       string
		sps        []byte
		wantWidth  int
		wantHeight int
	}{
		{
			name:       "640x480 Baseline",
			sps:        buildTestSPS(640, 480),
			wantWidth:  640,
			wantHeight: 480,
		},
		{
			name:       "320x240 Baseline",
			sps:        buildTestSPS(320, 240),
			wantWidth:  320,
			wantHeight: 240,
		},
		{
			name:       "1280x720 Baseline",
			sps:        buildTestSPS(1280, 720),
			wantWidth:  1280,
			wantHeight: 720,
		},
		{
			name:       "1920x1080 Baseline",
			sps:        buildTestSPS(1920, 1088),
			wantWidth:  1920,
			wantHeight: 1088,
		},
		{
			name:       "1280x720 High Profile",
			sps:        buildTestSPSHighProfile(1280, 720),
			wantWidth:  1280,
			wantHeight: 720,
		},
		{
			name:       "too short SPS returns 0,0",
			sps:        []byte{0x67, 0x42},
			wantWidth:  0,
			wantHeight: 0,
		},
		{
			name:       "nil SPS returns 0,0",
			sps:        nil,
			wantWidth:  0,
			wantHeight: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			width, height := parseH264Dimensions(tt.sps)
			if width != tt.wantWidth {
				t.Errorf("width = %d, want %d", width, tt.wantWidth)
			}
			if height != tt.wantHeight {
				t.Errorf("height = %d, want %d", height, tt.wantHeight)
			}
		})
	}
}

func TestListH264FrameFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create some frame files.
	files := []string{
		"frame_000000.h264",
		"frame_000001.h264",
		"frame_000002.h264",
		"some_other_file.txt",
		"frame_000003.h264",
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(tmpDir, f), []byte("test"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	result, err := listH264FrameFiles(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	if len(result) != 4 {
		t.Errorf("expected 4 .h264 files, got %d", len(result))
	}

	// Verify sorting.
	for i := 1; i < len(result); i++ {
		if result[i-1] > result[i] {
			t.Errorf("files not sorted: %s > %s", result[i-1], result[i])
		}
	}
}

func TestListH264FrameFiles_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	result, err := listH264FrameFiles(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 files, got %d", len(result))
	}
}

func TestH264GoMerger_NonExistentDir(t *testing.T) {
	tmpDir := t.TempDir()
	nonExistentDir := filepath.Join(tmpDir, "does_not_exist")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	merger := NewH264GoMerger()
	_, err := merger.Merge(context.Background(), nonExistentDir, outputPath, 1)
	if err == nil {
		t.Error("Expected error for non-existent directory, got nil")
	}
}
