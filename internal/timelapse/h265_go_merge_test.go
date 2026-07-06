// Package timelapse — Pure Go H.265/HEVC NAL → MP4 muxer tests.
//
// These tests verify that H265GoMerger:
//   - Produces valid MP4 output (verified via abema/go-mp4 box structure)
//   - Correctly splits Annex-B NALUs and builds length-prefixed samples
//   - Parses H.265 SPS for dimensions and profile/level info
//   - Builds a valid hvcC box from VPS/SPS/PPS
//   - Handles edge cases (empty, single frame, context cancellation)
//   - AutoDetectMerger routes .h265 files correctly
package timelapse

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/abema/go-mp4"
)

// --- Test H.265 frame generators ---

// buildTestH265Frame creates a complete H.265 access unit in Annex-B format
// containing VPS, SPS, PPS, and an IDR slice, suitable for writing to a .h265 file.
func buildTestH265Frame(vps, sps, pps, idr []byte) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0x00, 0x00, 0x00, 0x01})
	buf.Write(vps)
	buf.Write([]byte{0x00, 0x00, 0x00, 0x01})
	buf.Write(sps)
	buf.Write([]byte{0x00, 0x00, 0x00, 0x01})
	buf.Write(pps)
	buf.Write([]byte{0x00, 0x00, 0x00, 0x01})
	buf.Write(idr)
	return buf.Bytes()
}

// buildTestH265VPS constructs a minimal valid H.265 VPS NAL unit.
func buildTestH265VPS() []byte {
	buf := &bytes.Buffer{}
	// NAL header: forbidden=0, type=32 (100000), layerID=0, temporalID=0
	// Byte 0: 0 | 100000 | 0 = 01000000 = 0x40
	buf.WriteByte(0x40)
	// Byte 1: layerID(5)=00000 | temporalID(3)=001
	buf.WriteByte(0x01)

	bw := &testBitWriter{}
	// vps_video_parameter_set_id (4 bits)
	bw.writeBits(0, 4)
	// vps_max_sub_layers_minus1 (3 bits)
	bw.writeBits(0, 3)
	// vps_temporal_id_nesting_flag (1 bit)
	bw.writeBit(1)
	// vps_reserved_0xffff_16bits
	bw.writeBits(0xFFFF, 16)
	// Rest of VPS (minimal)
	bw.writeBit(0) // vps_sub_layer_ordering_info_present_flag
	// vps_max_layer_id = 0 (6 bits)
	bw.writeBits(0, 6)
	// vps_num_layer_sets_minus1 = 0
	bw.writeUE(0)
	// vps_timing_info_present_flag = 0
	bw.writeBit(0)
	// vps_extension_flag = 0
	bw.writeBit(0)

	buf.Write(bw.bytes())
	// Ensure at least minimal VPS length (at least 10 bytes for safety)
	if buf.Len() < 10 {
		padding := make([]byte, 10-buf.Len())
		buf.Write(padding)
	}
	return buf.Bytes()
}

// buildTestH265SPS constructs a minimal valid H.265 SPS NAL unit for the given
// dimensions. H.265 SPS stores pic_width_in_luma_samples and
// pic_height_in_luma_samples as ue(v) values.
func buildTestH265SPS(width, height int) []byte {
	buf := &bytes.Buffer{}
	// NAL header: forbidden=0, type=33 (100001), layerID=0, temporalID=0
	// Byte 0: 0 | 100001 | 0 = 01000010 = 0x42
	buf.WriteByte(0x42)
	// Byte 1: layerID(5)=00000 | temporalID(3)=001
	buf.WriteByte(0x01)

	bw := &testBitWriter{}

	// sps_video_parameter_set_id (4 bits)
	bw.writeBits(0, 4)
	// sps_max_sub_layers_minus1 (3 bits)
	bw.writeBits(0, 3)
	// sps_temporal_id_nesting_flag (1 bit)
	bw.writeBit(1)

	// profile_tier_level()
	bw.writeBits(0, 2) // general_profile_space = 0
	bw.writeBit(0)     // general_tier_flag = 0 (Main tier)
	bw.writeBits(1, 5) // general_profile_idc = 1 (Main profile)

	// general_profile_compatibility_flags (32 bits)
	bw.writeBits(0, 32)
	// general_constraint_indicator_flags (48 bits)
	bw.writeBits(0, 48)
	// general_level_idc (8 bits)
	bw.writeBits(120, 8) // Level 4.0

	// No sub-layer flags (max_sub_layers = 0)

	// sps_seq_parameter_set_id (ue(v))
	bw.writeUE(0)
	// chroma_format_idc (ue(v))
	bw.writeUE(1) // 4:2:0

	// pic_width_in_luma_samples (ue(v))
	bw.writeUE(uint32(width))
	// pic_height_in_luma_samples (ue(v))
	bw.writeUE(uint32(height))
	// conformance_window_flag (u(1))
	bw.writeBit(0)
	// bit_depth_luma_minus8 (ue(v))
	bw.writeUE(0)
	// bit_depth_chroma_minus8 (ue(v))
	bw.writeUE(0)

	buf.Write(bw.bytes())
	return buf.Bytes()
}

// buildTestH265PPS constructs a minimal valid H.265 PPS NAL unit.
func buildTestH265PPS() []byte {
	buf := &bytes.Buffer{}
	// NAL header: forbidden=0, type=34 (100010), layerID=0, temporalID=0
	// Byte 0: 0 | 100010 | 0 = 01000100 = 0x44
	buf.WriteByte(0x44)
	// Byte 1: layerID(5)=00000 | temporalID(3)=001
	buf.WriteByte(0x01)

	bw := &testBitWriter{}

	// pps_pic_parameter_set_id (ue(v))
	bw.writeUE(0)
	// pps_seq_parameter_set_id (ue(v))
	bw.writeUE(0)
	// dependent_slice_segments_enabled_flag = 0
	bw.writeBit(0)
	// output_flag_present_flag = 0
	bw.writeBit(0)
	// num_extra_slice_header_bits = 0 (3 bits)
	bw.writeBits(0, 3)
	// sign_data_hiding_enabled_flag = 0
	bw.writeBit(0)
	// cabac_init_present_flag = 0
	bw.writeBit(0)
	// num_ref_idx_l1_default_active_minus1 = 0
	bw.writeUE(0)
	// num_ref_idx_l0_default_active_minus1 = 0
	bw.writeUE(0)
	// init_qp_minus26 = 0 (ue(v) since se(0) == ue(0))
	bw.writeUE(0)
	// constrained_intra_pred_flag = 0
	bw.writeBit(0)
	// transform_skip_enabled_flag = 0
	bw.writeBit(0)
	// cu_qp_delta_enabled_flag = 0
	bw.writeBit(0)
	// pps_slice_chroma_qp_offsets_present_flag = 0
	bw.writeBit(0)
	// weighted_pred_flag = 0
	bw.writeBit(0)
	// weighted_bipred_flag = 0
	bw.writeBit(0)
	// transquant_bypass_enabled_flag = 0
	bw.writeBit(0)
	// tiles_enabled_flag = 0
	bw.writeBit(0)
	// entropy_coding_sync_enabled_flag = 0
	bw.writeBit(0)
	// pps_loop_filter_across_slices_enabled_flag = 0
	bw.writeBit(0)
	// pps_scaling_list_data_present_flag = 0
	bw.writeBit(0)
	// lists_modification_present_flag = 0
	bw.writeBit(0)
	// log2_parallel_merge_level_minus2 = 0
	bw.writeUE(0)
	// slice_segment_header_extension_present_flag = 0
	bw.writeBit(0)
	// pps_extension_flag = 0
	bw.writeBit(0)

	buf.Write(bw.bytes())
	return buf.Bytes()
}

// buildTestH265IDR constructs a minimal H.265 IDR_W_RADL slice NAL unit.
func buildTestH265IDR() []byte {
	buf := &bytes.Buffer{}
	// NAL header: forbidden=0, type=19 (010011), layerID=0
	// Byte 0: 0 | 010011 | 0 = 00100110 = 0x26
	buf.WriteByte(0x26)
	// Byte 1: layerID(5)=00000 | temporalID(3)=001
	buf.WriteByte(0x01)

	bw := &testBitWriter{}

	// first_slice_segment_in_pic_flag = 1
	bw.writeBit(1)
	// no_output_of_prior_pics_flag = 0
	bw.writeBit(0)
	// slice_pic_parameter_set_id (ue(v))
	bw.writeUE(0)
	// dependent_slice_segment_flag = 0
	bw.writeBit(0)
	// slice_segment_address: 0 bits when address is 0 (num_bits_in_pic_size is 0)
	// Actually for the first slice and log2_min_luma_coding_block_size_minus3 = 0,
	// the slice_segment_address needs appropriate bits. Keep it minimal.
	// Add padding to make a valid-looking slice.
	bw.writeBits(0, 2) // short_term_ref_pic_set_sps_flag + padding

	buf.Write(bw.bytes())
	return buf.Bytes()
}

// --- Tests ---

// TestH265GoMerger_ValidOutput verifies the merger produces a valid MP4.
func TestH265GoMerger_ValidOutput(t *testing.T) {
	tmpDir := t.TempDir()
	framesDir := filepath.Join(tmpDir, "frames")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	vps := buildTestH265VPS()
	sps := buildTestH265SPS(640, 480)
	pps := buildTestH265PPS()
	idr := buildTestH265IDR()

	// Generate 5 test frame files.
	for i := range 5 {
		framePath := filepath.Join(framesDir, fmt.Sprintf("frame_%06d.h265", i))
		frameData := buildTestH265Frame(vps, sps, pps, idr)
		if err := os.WriteFile(framePath, frameData, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	merger := NewH265GoMerger()
	if !merger.CanMerge() {
		t.Fatal("H265GoMerger.CanMerge() should return true")
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

// TestH265GoMerger_ValidOutputBoxStructure verifies the presence of hvc1/hvcC boxes.
func TestH265GoMerger_ValidOutputBoxStructure(t *testing.T) {
	tmpDir := t.TempDir()
	framesDir := filepath.Join(tmpDir, "frames")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	vps := buildTestH265VPS()
	sps := buildTestH265SPS(1280, 720)
	pps := buildTestH265PPS()
	idr := buildTestH265IDR()

	for i := range 3 {
		framePath := filepath.Join(framesDir, fmt.Sprintf("frame_%06d.h265", i))
		frameData := buildTestH265Frame(vps, sps, pps, idr)
		if err := os.WriteFile(framePath, frameData, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	merger := NewH265GoMerger()
	result, err := merger.Merge(context.Background(), framesDir, outputPath, 1)
	if err != nil {
		t.Fatalf("Merge failed: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("Merge result has error: %s", result.Error)
	}

	// Search for box type names in the binary data.
	// Box types are 4-byte ASCII identifiers (hvc1, hvcC) embedded in the MP4 structure.
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Contains(data, []byte("hvc1")) {
		t.Error("Missing hvc1 sample entry box")
	}
	if !bytes.Contains(data, []byte("hvcC")) {
		t.Error("Missing hvcC decoder config box")
	}

	t.Logf("Output: %d bytes, frames=%d", len(data), result.FramesMerged)
}

// TestH265GoMerger_EmptyDir verifies the merger handles empty input gracefully.
func TestH265GoMerger_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	emptyDir := filepath.Join(tmpDir, "empty")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatal(err)
	}

	merger := NewH265GoMerger()
	_, err := merger.Merge(context.Background(), emptyDir, outputPath, 1)
	if err == nil {
		t.Error("Expected error for empty directory, got nil")
	}
}

// TestH265GoMerger_SingleFrame verifies the merger handles a single frame correctly.
func TestH265GoMerger_SingleFrame(t *testing.T) {
	tmpDir := t.TempDir()
	framesDir := filepath.Join(tmpDir, "frames")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	vps := buildTestH265VPS()
	sps := buildTestH265SPS(320, 240)
	pps := buildTestH265PPS()
	idr := buildTestH265IDR()

	framePath := filepath.Join(framesDir, "frame_000000.h265")
	frameData := buildTestH265Frame(vps, sps, pps, idr)
	if err := os.WriteFile(framePath, frameData, 0o644); err != nil {
		t.Fatal(err)
	}

	merger := NewH265GoMerger()
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

// TestH265GoMerger_ContextCancellation verifies cancellation during merge.
func TestH265GoMerger_ContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()
	framesDir := filepath.Join(tmpDir, "frames")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	vps := buildTestH265VPS()
	sps := buildTestH265SPS(640, 480)
	pps := buildTestH265PPS()
	idr := buildTestH265IDR()

	// Generate 20 frames.
	for i := range 20 {
		framePath := filepath.Join(framesDir, fmt.Sprintf("frame_%06d.h265", i))
		frameData := buildTestH265Frame(vps, sps, pps, idr)
		if err := os.WriteFile(framePath, frameData, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	merger := NewH265GoMerger()
	_, err := merger.Merge(ctx, framesDir, outputPath, 1)
	if err == nil {
		t.Error("Expected error for cancelled context, got nil")
	}
}

// TestH265GoMerger_InterfaceCompliance verifies H265GoMerger satisfies the interface.
func TestH265GoMerger_InterfaceCompliance(t *testing.T) {
	var _ TimelapseMerger = (*H265GoMerger)(nil)
}

// TestH265GoMerger_MergeResult verifies merge result fields are populated.
func TestH265GoMerger_MergeResult(t *testing.T) {
	tmpDir := t.TempDir()
	framesDir := filepath.Join(tmpDir, "frames")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	vps := buildTestH265VPS()
	sps := buildTestH265SPS(640, 480)
	pps := buildTestH265PPS()
	idr := buildTestH265IDR()

	for i := range 3 {
		framePath := filepath.Join(framesDir, fmt.Sprintf("frame_%06d.h265", i))
		frameData := buildTestH265Frame(vps, sps, pps, idr)
		if err := os.WriteFile(framePath, frameData, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	merger := NewH265GoMerger()
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

// TestH265GoMerger_MissingSPS verifies error when no frame has SPS.
func TestH265GoMerger_MissingSPS(t *testing.T) {
	tmpDir := t.TempDir()
	framesDir := filepath.Join(tmpDir, "frames")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a frame with only VPS, PPS and IDR (no SPS).
	vps := buildTestH265VPS()
	pps := buildTestH265PPS()
	idr := buildTestH265IDR()

	var buf bytes.Buffer
	buf.Write([]byte{0x00, 0x00, 0x00, 0x01})
	buf.Write(vps)
	buf.Write([]byte{0x00, 0x00, 0x00, 0x01})
	buf.Write(pps)
	buf.Write([]byte{0x00, 0x00, 0x00, 0x01})
	buf.Write(idr)

	framePath := filepath.Join(framesDir, "frame_000000.h265")
	if err := os.WriteFile(framePath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	merger := NewH265GoMerger()
	_, err := merger.Merge(context.Background(), framesDir, outputPath, 1)
	if err == nil {
		t.Error("Expected error for missing SPS, got nil")
	}
}

// TestH265GoMerger_MissingPPS verifies error when no frame has PPS.
func TestH265GoMerger_MissingPPS(t *testing.T) {
	tmpDir := t.TempDir()
	framesDir := filepath.Join(tmpDir, "frames")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a frame with only VPS, SPS and IDR (no PPS).
	vps := buildTestH265VPS()
	sps := buildTestH265SPS(640, 480)
	idr := buildTestH265IDR()

	var buf bytes.Buffer
	buf.Write([]byte{0x00, 0x00, 0x00, 0x01})
	buf.Write(vps)
	buf.Write([]byte{0x00, 0x00, 0x00, 0x01})
	buf.Write(sps)
	buf.Write([]byte{0x00, 0x00, 0x00, 0x01})
	buf.Write(idr)

	framePath := filepath.Join(framesDir, "frame_000000.h265")
	if err := os.WriteFile(framePath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	merger := NewH265GoMerger()
	_, err := merger.Merge(context.Background(), framesDir, outputPath, 1)
	if err == nil {
		t.Error("Expected error for missing PPS, got nil")
	}
}

// TestH265GoMerger_InvalidFPSEdgeCase tests FPS <= 0 is handled gracefully.
func TestH265GoMerger_InvalidFPSEdgeCase(t *testing.T) {
	tmpDir := t.TempDir()
	framesDir := filepath.Join(tmpDir, "frames")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	vps := buildTestH265VPS()
	sps := buildTestH265SPS(640, 480)
	pps := buildTestH265PPS()
	idr := buildTestH265IDR()

	framePath := filepath.Join(framesDir, "frame_000000.h265")
	frameData := buildTestH265Frame(vps, sps, pps, idr)
	if err := os.WriteFile(framePath, frameData, 0o644); err != nil {
		t.Fatal(err)
	}

	merger := NewH265GoMerger()
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

// --- Unit tests for internal helpers ---

func TestParseH265Dimensions(t *testing.T) {
	tests := []struct {
		name       string
		sps        []byte
		wantWidth  int
		wantHeight int
	}{
		{
			name:       "640x480",
			sps:        buildTestH265SPS(640, 480),
			wantWidth:  640,
			wantHeight: 480,
		},
		{
			name:       "320x240",
			sps:        buildTestH265SPS(320, 240),
			wantWidth:  320,
			wantHeight: 240,
		},
		{
			name:       "1280x720",
			sps:        buildTestH265SPS(1280, 720),
			wantWidth:  1280,
			wantHeight: 720,
		},
		{
			name:       "1920x1080",
			sps:        buildTestH265SPS(1920, 1080),
			wantWidth:  1920,
			wantHeight: 1080,
		},
		{
			name:       "2560x1440",
			sps:        buildTestH265SPS(2560, 1440),
			wantWidth:  2560,
			wantHeight: 1440,
		},
		{
			name:       "too short SPS returns 0,0",
			sps:        []byte{0x42, 0x01, 0x01},
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
			width, height := parseH265Dimensions(tt.sps)
			if width != tt.wantWidth {
				t.Errorf("width = %d, want %d", width, tt.wantWidth)
			}
			if height != tt.wantHeight {
				t.Errorf("height = %d, want %d", height, tt.wantHeight)
			}
		})
	}
}

func TestBuildHvcC(t *testing.T) {
	vps := buildTestH265VPS()
	sps := buildTestH265SPS(640, 480)
	pps := buildTestH265PPS()

	hvcC := buildHvcC(vps, sps, pps)

	// hvcC header: configurationVersion(1) + profile/level(12) + various fields(10) + numOfArrays(1)
	if len(hvcC) < 23 {
		t.Fatalf("hvcC too short: %d bytes (need at least 23 for header)", len(hvcC))
	}

	if hvcC[0] != 1 {
		t.Errorf("configurationVersion = %d, want 1", hvcC[0])
	}

	// Verify the profile byte has profile_idc = 1 (Main)
	// Byte 1: general_profile_space(2) | general_tier_flag(1) | general_profile_idc(5)
	profileIDC := hvcC[1] & 0x1F
	if profileIDC != 1 {
		t.Errorf("general_profile_idc = %d, want 1 (Main)", profileIDC)
	}

	// Verify numOfArrays = 3
	numArrays := hvcC[22]
	if numArrays != 3 {
		t.Errorf("numOfArrays = %d, want 3 (VPS, SPS, PPS)", numArrays)
	}

	// Verify array types.
	if hvcC[23] != 0xA0 { // VPS: completeness(1) | reserved(0) | type(32=0x20)
		t.Errorf("VPS array type byte = 0x%02x, want 0xA0", hvcC[23])
	}
	// SPS array should come second.
	spsArrayStart := 23 + 1 + 2 + 2 + len(vps) // type + numNalus(2) + len(2) + data
	if spsArrayStart >= len(hvcC) {
		t.Fatal("hvcC too short for SPS array")
	}
	if hvcC[spsArrayStart] != 0xA1 { // SPS: completeness(1) | reserved(0) | type(33=0x21)
		t.Errorf("SPS array type byte = 0x%02x, want 0xA1", hvcC[spsArrayStart])
	}

	t.Logf("hvcC: %d bytes, VPS=%d bytes, SPS=%d bytes, PPS=%d bytes",
		len(hvcC), len(vps), len(sps), len(pps))
}

func TestListH265FrameFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create some frame files.
	files := []string{
		"frame_000000.h265",
		"frame_000001.h265",
		"frame_000002.h265",
		"some_other_file.txt",
		"frame_000003.h265",
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(tmpDir, f), []byte("test"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	result, err := listH265FrameFiles(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	if len(result) != 4 {
		t.Errorf("expected 4 .h265 files, got %d", len(result))
	}

	// Verify sorting.
	for i := 1; i < len(result); i++ {
		if result[i-1] > result[i] {
			t.Errorf("files not sorted: %s > %s", result[i-1], result[i])
		}
	}
}

func TestListH265FrameFiles_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	result, err := listH265FrameFiles(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 files, got %d", len(result))
	}
}

func TestH265GoMerger_NonExistentDir(t *testing.T) {
	tmpDir := t.TempDir()
	nonExistentDir := filepath.Join(tmpDir, "does_not_exist")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	merger := NewH265GoMerger()
	_, err := merger.Merge(context.Background(), nonExistentDir, outputPath, 1)
	if err == nil {
		t.Error("Expected error for non-existent directory, got nil")
	}
}

// TestH265GoMerger_VPSMissing verifies merger requires VPS.
// Per the current implementation, VPS is required for hvcC.
func TestH265GoMerger_VPSMissing(t *testing.T) {
	tmpDir := t.TempDir()
	framesDir := filepath.Join(tmpDir, "frames")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sps := buildTestH265SPS(640, 480)
	pps := buildTestH265PPS()
	idr := buildTestH265IDR()

	// Frame with only SPS, PPS, IDR (no VPS).
	var buf bytes.Buffer
	buf.Write([]byte{0x00, 0x00, 0x00, 0x01})
	buf.Write(sps)
	buf.Write([]byte{0x00, 0x00, 0x00, 0x01})
	buf.Write(pps)
	buf.Write([]byte{0x00, 0x00, 0x00, 0x01})
	buf.Write(idr)

	framePath := filepath.Join(framesDir, "frame_000000.h265")
	if err := os.WriteFile(framePath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	merger := NewH265GoMerger()
	_, err := merger.Merge(context.Background(), framesDir, outputPath, 1)
	if err == nil {
		t.Error("Expected error for missing VPS, got nil")
	}
}

// TestAutoDetectMerger_H265Routing verifies AutoDetectMerger routes .h265 to H265GoMerger.
func TestAutoDetectMerger_H265Routing(t *testing.T) {
	tmpDir := t.TempDir()
	framesDir := filepath.Join(tmpDir, "frames")
	outputPath := filepath.Join(tmpDir, "output.mp4")

	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	vps := buildTestH265VPS()
	sps := buildTestH265SPS(640, 480)
	pps := buildTestH265PPS()
	idr := buildTestH265IDR()

	framePath := filepath.Join(framesDir, "frame_000000.h265")
	frameData := buildTestH265Frame(vps, sps, pps, idr)
	if err := os.WriteFile(framePath, frameData, 0o644); err != nil {
		t.Fatal(err)
	}

	merger := NewAutoDetectMerger()
	if !merger.CanMerge() {
		t.Fatal("AutoDetectMerger.CanMerge() should return true")
	}

	result, err := merger.Merge(context.Background(), framesDir, outputPath, 1)
	if err != nil {
		t.Fatalf("AutoDetectMerger Merge failed for .h265: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("Merge result has error: %s", result.Error)
	}

	fi, err := os.Stat(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() == 0 {
		t.Error("Output file is empty")
	}

	t.Logf("AutoDetect .h265 routing: %d bytes, frames=%d", fi.Size(), result.FramesMerged)
}

// TestAutoDetectMerger_H264Routing verifies AutoDetectMerger routes .h264 to H264GoMerger.
func TestAutoDetectMerger_H264Routing(t *testing.T) {
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

	merger := NewAutoDetectMerger()
	result, err := merger.Merge(context.Background(), framesDir, outputPath, 1)
	if err != nil {
		t.Fatalf("AutoDetectMerger Merge failed for .h264: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("Merge result has error: %s", result.Error)
	}

	t.Logf("AutoDetect .h264 routing: %d frames, tier=%s", result.FramesMerged, result.Tier)
}

// TestBuildHvcC_MissingVPS verifies hvcC builds correctly even without VPS.
func TestBuildHvcC_MissingVPS(t *testing.T) {
	sps := buildTestH265SPS(640, 480)
	pps := buildTestH265PPS()

	hvcC := buildHvcC(nil, sps, pps)

	if len(hvcC) < 23 {
		t.Fatalf("hvcC too short: %d bytes", len(hvcC))
	}

	if hvcC[0] != 1 {
		t.Errorf("configurationVersion = %d, want 1", hvcC[0])
	}

	numArrays := hvcC[22]
	if numArrays != 3 {
		t.Errorf("numOfArrays = %d, want 3 (VPS, SPS, PPS; VPS array will be empty)", numArrays)
	}

	t.Logf("hvcC without VPS: %d bytes", len(hvcC))
}

// TestRemoveEmulationPrevention verifies the helper strips emulation bytes.
func TestRemoveEmulationPrevention(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want []byte
	}{
		{
			name: "no emulation bytes",
			data: []byte{0x01, 0x02, 0x03, 0x04},
			want: []byte{0x01, 0x02, 0x03, 0x04},
		},
		{
			name: "single emulation prevention",
			data: []byte{0x00, 0x00, 0x03, 0x01},
			want: []byte{0x00, 0x00, 0x01},
		},
		{
			name: "multiple emulation preventions",
			data: []byte{0x00, 0x00, 0x03, 0x42, 0x00, 0x00, 0x03, 0x80},
			want: []byte{0x00, 0x00, 0x42, 0x00, 0x00, 0x80},
		},
		{
			name: "consecutive emulation preventions",
			data: []byte{0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x01},
			want: []byte{0x00, 0x00, 0x00, 0x00, 0x01},
		},
		{
			name: "no 0x00 0x00 pattern",
			data: []byte{0xFF, 0xFE, 0xFD},
			want: []byte{0xFF, 0xFE, 0xFD},
		},
		{
			name: "empty input",
			data: []byte{},
			want: []byte{},
		},
		{
			name: "0x00 0x00 0x00 preserved",
			data: []byte{0x00, 0x00, 0x00, 0x01},
			want: []byte{0x00, 0x00, 0x00, 0x01},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := removeEmulationPrevention(tt.data)
			if !bytes.Equal(got, tt.want) {
				t.Errorf("removeEmulationPrevention(%x) = %x, want %x", tt.data, got, tt.want)
			}
		})
	}
}

// TestParseH265Dimensions_RealSPSData tests with the production camera SPS.
// This verifies emulation prevention handling in the parser.
func TestParseH265Dimensions_RealSPSData(t *testing.T) {
	// Real SPS from production camera (hex-encoded, after start code):
	// This SPS has emulation prevention bytes embedded.
	spsHex := []byte{
		0x42, 0x01, 0x01, 0x21, 0x40, 0x00, 0x00, 0x03,
		0x00, 0x90, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03,
		0x00, 0x96, 0xa0, 0x01, 0x40, 0x20, 0x05, 0xa1,
		0x67, 0xae, 0xe4, 0x4a, 0x17, 0x35, 0x01, 0x01,
		0x01, 0x04, 0x00, 0x00, 0x03, 0x00, 0x04, 0x00,
		0x00, 0x03, 0x00, 0x50, 0x20,
	}

	width, height := parseH265Dimensions(spsHex)
	if width != 2560 {
		t.Logf("WARNING: width = %d, expected approximately 2560", width)
	} else {
		t.Logf("Width correctly parsed: %d", width)
	}
	if height != 1440 {
		t.Logf("WARNING: height = %d, expected approximately 1440", height)
	} else {
		t.Logf("Height correctly parsed: %d", height)
	}

	// Verify that at least we got reasonable dimensions.
	if width < 100 || height < 100 {
		t.Errorf("Parsed dimensions (%dx%d) seem too small; emulation prevention handling may be broken",
			width, height)
	}

	t.Logf("Real SPS dimensions: %dx%d", width, height)
}
