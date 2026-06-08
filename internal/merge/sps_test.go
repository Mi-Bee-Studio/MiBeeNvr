package merge

import (
	"testing"
)

// Known-good H.264 SPS for 640x128 baseline profile (from muxer testSPS).
var h264SPS640 = []byte{
	0x67, 0x42, 0xc0, 0x1e, 0xd9, 0x00, 0xa0, 0x47, 0xfe, 0xc8,
}

// Known-good H.264 SPS for 1280x720 baseline profile (from muxer testSPS720).
var h264SPS1280 = []byte{
	0x67, 0x42, 0xc0, 0x1e, 0xf4, 0x02, 0x80, 0x2d, 0x80,
}

// Known-good H.264 SPS for 1920x1080 baseline profile (from muxer testSPS1080).
var h264SPS1920 = []byte{
	0x67, 0x42, 0xc0, 0x28, 0xf4, 0x03, 0xc0, 0x11, 0x2f, 0x28,
}

// Valid HEVC SPS for 1920x1080 (from internal/recorder test data).
var hevcSPS1920 = []byte{
	0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x90,
	0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x78, 0xa0, 0x03,
	0xc0, 0x80, 0x10, 0xe5, 0x96, 0x66, 0x69, 0x24, 0xca, 0xe0,
	0x10, 0x00, 0x00, 0x03, 0x00, 0x10, 0x00, 0x00, 0x03, 0x01,
	0xe0, 0x80,
}

func TestParseSPSResolution_Valid(t *testing.T) {
	tests := []struct {
		name           string
		sps            []byte
		expectedWidth  int
		expectedHeight int
	}{
		{"640x128", h264SPS640, 640, 128},
		{"1280x720", h264SPS1280, 1280, 720},
		{"1920x1080", h264SPS1920, 1920, 1080},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, h, err := parseSPSResolution(tt.sps)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if w != tt.expectedWidth {
				t.Errorf("width = %d, want %d", w, tt.expectedWidth)
			}
			if h != tt.expectedHeight {
				t.Errorf("height = %d, want %d", h, tt.expectedHeight)
			}
		})
	}
}

func TestParseSPSResolution_Invalid(t *testing.T) {
	tests := []struct {
		name string
		sps  []byte
	}{
		{"nil", nil},
		{"empty", []byte{}},
		{"too_short", []byte{0x00}},
		{"short_7_bytes", []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}},
		{"short_rbsp", []byte{
			0x67, 0x64, 0x00, 0x1e, 0xac, 0xd9, 0x40, 0xb4,
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := parseSPSResolution(tt.sps)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestParseHEVCSPSResolution_Valid(t *testing.T) {
	w, h, err := parseHEVCSPSResolution(hevcSPS1920)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != 1920 {
		t.Errorf("width = %d, want 1920", w)
	}
	if h != 1080 {
		t.Errorf("height = %d, want 1080", h)
	}
}

func TestParseHEVCSPSResolution_Invalid(t *testing.T) {
	tests := []struct {
		name string
		sps  []byte
	}{
		{"nil", nil},
		{"empty", []byte{}},
		{"too_short", []byte{0x00}},
		{"short_7_bytes", []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}},
		{"short_rbsp", []byte{
			0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x00,
			0x80, 0x00, 0x00, 0x00, 0x00, 0x00,
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := parseHEVCSPSResolution(tt.sps)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestBitReader_Overflow(t *testing.T) {
	r := &bitReader{data: []byte{0x00}}
	// Read 8 bits (the single byte)
	for i := 0; i < 8; i++ {
		bit, err := r.readBit()
		if err != nil {
			t.Fatalf("unexpected error at bit %d: %v", i, err)
		}
		if bit != 0 {
			t.Errorf("bit %d = %d, want 0", i, bit)
		}
	}
	// Read past end
	_, err := r.readBit()
	if err == nil {
		t.Fatal("expected overflow error, got nil")
	}
}

func TestBitReader_TruncatedSPS(t *testing.T) {
	// A valid SPS header (high profile) that requires more bits than available.
	// profile_idc=100 (high profile), but only 8 bytes — overflows during parsing.
	sps := []byte{
		0x67, 0x64, 0x00, 0x1e, 0xac, 0xd9, 0x40, 0xb4,
	}
	_, _, err := parseSPSResolution(sps)
	if err == nil {
		t.Fatal("expected error for truncated SPS, got nil")
	}
}

func TestBitReader_LeadingZerosOverflow(t *testing.T) {
	// A bit stream where all bits are 0 (leading zeros overflow).
	// readUE will keep reading 0 bits until it hits the 32 leading zeros limit.
	r := &bitReader{data: []byte{0x00, 0x00, 0x00, 0x00, 0x00}}
	_, err := r.readUE()
	if err == nil {
		t.Fatal("expected leadingZeros overflow error, got nil")
	}
}

func TestBitReader_ReadBits(t *testing.T) {
	r := &bitReader{data: []byte{0b10101010}}
	bits, err := r.readBits(4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bits != 0b1010 {
		t.Errorf("readBits(4) = %d (0b%b), want 0b1010", bits, bits)
	}
}

func TestBitReader_ReadBitsOverflow(t *testing.T) {
	r := &bitReader{data: []byte{0xFF}}
	_, err := r.readBits(9)
	if err == nil {
		t.Fatal("expected overflow error, got nil")
	}
}

func TestParseSPSResolution_Minimal(t *testing.T) {
	// A valid brief SPS that will fail at reading pic_width.
	// We just verify it returns an error, not 0,0 silently.
	_, _, err := parseSPSResolution([]byte{0x00})
	if err == nil {
		t.Fatal("expected error for 1-byte SPS, got nil")
	}
}

func TestReadSE(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected int
	}{
		// UE=0 (bit "1") -> SE=0 (0%2=0 -> -(0/2)=0)
		{"ue0_se0", []byte{0x80}, 0},
		// UE=1 (bits "010") -> SE=1 (1%2=1 -> (1+1)/2=1)
		{"ue1_se1", []byte{0x40}, 1},
		// UE=2 (bits "011") -> SE=-1 (2%2=0 -> -(2/2)=-1)
		{"ue2_se_neg1", []byte{0x60}, -1},
		// UE=3 (bits "00100") -> SE=2 (3%2=1 -> (3+1)/2=2)
		{"ue3_se2", []byte{0x20}, 2},
		// UE=4 (bits "00101") -> SE=-2 (4%2=0 -> -(4/2)=-2)
		{"ue4_se_neg2", []byte{0x28}, -2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &bitReader{data: tt.data}
			v, err := r.readSE()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if v != tt.expected {
				t.Errorf("readSE() = %d, want %d", v, tt.expected)
			}
		})
	}
}

func TestReadSE_Overflow(t *testing.T) {
	// Read past end (should get error from readUE)
	r := &bitReader{data: []byte{}}
	_, err := r.readSE()
	if err == nil {
		t.Fatal("expected overflow error, got nil")
	}
}

// High profile H.264 SPS (profile_idc=100, chroma 4:2:0, resolution 640x128).
// Tests the highProfile branch with chroma_format_idc parsing.
var h264SPSHighProfile640 = []byte{
	0x67, 0x64, 0x00, 0x00,
	0xAC, 0xF0, 0x28, 0x11, 0x00,
}

func TestParseSPSResolution_HighProfile(t *testing.T) {
	w, h, err := parseSPSResolution(h264SPSHighProfile640)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != 640 {
		t.Errorf("width = %d, want 640", w)
	}
	if h != 128 {
		t.Errorf("height = %d, want 128", h)
	}
}

// High profile SPS with scaling lists present (all present flags = 0).
var h264SPSHighProfileScaling640 = []byte{
	0x67, 0x64, 0x00, 0x00,
	0xAD, 0x00, 0xF0, 0x28, 0x11, 0x00,
}

func TestParseSPSResolution_ScalingLists(t *testing.T) {
	w, h, err := parseSPSResolution(h264SPSHighProfileScaling640)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != 640 {
		t.Errorf("width = %d, want 640", w)
	}
	if h != 128 {
		t.Errorf("height = %d, want 128", h)
	}
}

func TestParseSPSResolution_FrameCropping(t *testing.T) {
	// The existing h264SPS1920 (testSPS1080) has frame_cropping enabled.
	// Profile is baseline (66), crop is applied.
	w, h, err := parseSPSResolution(h264SPS1920)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != 1920 {
		t.Errorf("width = %d, want 1920", w)
	}
	if h != 1080 {
		t.Errorf("height = %d, want 1080", h)
	}
}

func TestParseSPSResolution_ZeroResolution(t *testing.T) {
	// Parse existing SPS and verify valid resolutions.
	// For zero resolution, the check is: width <= 0 || height <= 0.
	_, _, err := parseSPSResolution([]byte{0x00})
	if err == nil {
		t.Fatal("expected error for invalid SPS, got nil")
	}
}

// HEVC SPS with chroma_format_idc=3 to test separate_colour_plane_flag path.
var hevcSPSChroma3 = []byte{
	0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x90,
	0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x78, 0xa0, 0x03,
	0xc0, 0x80, 0x10, 0xe5, 0x96, 0x66, 0x69, 0x24, 0xca, 0xe0,
	0x10, 0x01, 0xe0, 0x80,
}

func TestParseHEVCSPSResolution_ChromaFormat3(t *testing.T) {
	w, h, err := parseHEVCSPSResolution(hevcSPS1920)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != 1920 || h != 1080 {
		t.Fatalf("hevcSPS1920: want 1920x1080, got %dx%d", w, h)
	}
}

func TestParseHEVCSPSResolution_ShortRbsp(t *testing.T) {
	// HEVC SPS that passes the initial length check but has too short RBSP after emulation prevention.
	_, _, err := parseHEVCSPSResolution([]byte{0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x00})
	if err == nil {
		t.Fatal("expected error for short HEVC SPS RBSP, got nil")
	}
}
// H.264 SPS with pic_order_cnt_type=1 (baseline profile, 640x128).
// Bit layout after profile/constraints/level (24 bits):
// [seq_param ue0=1] [log2_max_frame_num ue0=1] [poc_type ue1=010]
// [delta_flag=0] [se0=1] [se0=1] [ue0 cycles=1] [max_ref ue0=1] [gap=0]
// [ue39=00000101000] [ue7=0001000] [mbs=1] [direct=1] [crop=0]
var h264SPS640PicOrderCnt1 = []byte{
	0x67, 0x42, 0x00, 0x00, // NAL + profile(66) + constraints + level
	0xD3, 0xC0, 0xA0, 0x46, // exp-golomb: 1,1,010,0,1,1,1,1,0 + ue39 + ue7 + flags
}

func TestParseSPSResolution_PicOrderCntType1(t *testing.T) {
	w, h, err := parseSPSResolution(h264SPS640PicOrderCnt1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != 640 {
		t.Errorf("width = %d, want 640", w)
	}
	if h != 128 {
		t.Errorf("height = %d, want 128", h)
	}
}

// H.264 SPS with frame_mbs_only_flag=0 (interlaced, 640x128).
// Tests the frameMbsOnly==0 branch (lines 230-234 of sps.go).
var h264SPS640Interlaced = []byte{
	0x67, 0x42, 0x00, 0x00, // NAL + profile(66) + constraints + level
	0xF8, 0x14, 0x10, 0x80, // exp-golomb: frame_mbs_only=0, pic_height_in_map=4
}

func TestParseSPSResolution_Interlaced(t *testing.T) {
	w, h, err := parseSPSResolution(h264SPS640Interlaced)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != 640 {
		t.Errorf("width = %d, want 640", w)
	}
	if h != 128 {
		t.Errorf("height = %d, want 128", h)
	}
}

// H.264 SPS with high profile (100), chromaFormatIDC=0 (monochrome), frame cropping enabled.
// Tests the chromaFormatIDC==0 cropping branch (line 261 of sps.go).
// Uncropped: 640x128, cropped: 638x126 (crop all sides: left=right=top=bottom=1px).
// High-profile fields (before common fields): seq_param(ue0), chroma_ue0(0), depth_luma(ue0),
// depth_chroma(ue0), qpprime(0), scaling(0) = 6 bits total.
var h264SPSHighChroma0Crop = []byte{
	0x67, 0x64, 0x00, 0x00, // NAL + profile(100) + constraints + level
	0xF3, 0xC0, 0xA0, 0x47, // high-profile fields + exp-golomb values
	0x49, 0x20, // cropping: left=1, right=1, top=1, bottom=1 + padding
}

func TestParseSPSResolution_ChromaFormatIDC0(t *testing.T) {
	w, h, err := parseSPSResolution(h264SPSHighChroma0Crop)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// chroma=0: cropUnitX=1, cropUnitY=1 (frameMbsOnly=1)
	// cropped width  = 640 - (1+1)*1 = 638
	// cropped height = 128 - (1+1)*1 = 126
	if w != 638 {
		t.Errorf("width = %d, want 638", w)
	}
	if h != 126 {
		t.Errorf("height = %d, want 126", h)
	}
}

// HEVC SPS with sps_max_sub_layers_minus1 > 0 (1 sub-layer).
// Tests the sub-layer profile/level present loop and reserved bits loop
// at lines 318-329 of sps.go.
// NAL header: 0x42, 0x01. RBSP: 14 bytes.
// Layout: vps_id=0, max_sub_layers=1, nesting=1, profile=1,
// compat_flags=0, constraints=0, level=0, sub_layer_presents=00,
// reserved=0, seq_param_id=0, chroma=0, width=1, height=1.
var hevcSPSMaxSubLayers1 = []byte{
	0x42, 0x01, // NAL header (HEVC SPS)
	0x03, 0x01, // vps_id=0, max_sub_layers=1, nesting=1, profile=1
	0x00, 0x00, 0x00, 0x00, // profile compatibility flags (32 bits, all 0)
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // constraint indicators (48 bits, all 0)
	0x00, // general_level_idc = 0
	0x1A, 0x40, // sub_layer bits + exp-golomb: chroma=0, width=1 ue(1), height=1 ue(1)
}

func TestParseHEVCSPSResolution_MaxSubLayers(t *testing.T) {
	w, h, err := parseHEVCSPSResolution(hevcSPSMaxSubLayers1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != 1 {
		t.Errorf("width = %d, want 1", w)
	}
	if h != 1 {
		t.Errorf("height = %d, want 1", h)
	}
}

// HEVC SPS with chroma_format_idc=3 (4:4:4 chroma) and separate_colour_plane_flag=0.
// Tests the chromaFormatIDC==3 branch lines 337-341 of sps.go.
// NAL: 0x42, 0x01. maxSubLayers=0. profile=1. chroma=3 (ue=00100).
// Width=2 (ue(1)=010), Height=2 (ue(1)=010). Padding after.
var hevcSPSChromaFormat3 = []byte{
	0x42, 0x01, // NAL header (HEVC SPS)
	0x01, 0x01, // vps_id=0, max_sub=0, nesting=1, profile=1
	0x00, 0x00, 0x00, 0x00, // profile compatibility flags
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // constraint indicators
	0x00, // general_level_idc = 0
	0x90, 0xD8, // chroma=3 (00100), sep=0, width ue(2)=011, height ue(2)=011 + padding
}

func TestParseHEVCSPSResolution_Chroma3(t *testing.T) {
	w, h, err := parseHEVCSPSResolution(hevcSPSChromaFormat3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != 2 {
		t.Errorf("width = %d, want 2", w)
	}
	if h != 2 {
		t.Errorf("height = %d, want 2", h)
	}
}


// H.264 SPS with high profile (100), chromaFormatIDC=3 (4:4:4 chroma), no cropping.
// Tests the chromaFormatIDC==3 branch (separate_colour_plane_flag at line 128-132 of sps.go).
// Bit layout: profile=100, chroma=3 (ue=00100), sep_plane=0, depth=0, width=640, height=128.
var h264SPSHighChroma3 = []byte{
	0x67, 0x64, 0x00, 0x00, // NAL + profile(100) + constraints + level
	0x91, 0x9E, 0x05, 0x02, // chroma=3: seq=1, chroma=00100, sep=0, depth=1,1, qpprime=0, scaling=0
	0x30, // frame_mbs_only=1, direct=1, crop=0 + padding
}

func TestParseSPSResolution_ChromaFormatIDC3(t *testing.T) {
	w, h, err := parseSPSResolution(h264SPSHighChroma3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != 640 {
		t.Errorf("width = %d, want 640", w)
	}
	if h != 128 {
		t.Errorf("height = %d, want 128", h)
	}
}

// H.264 SPS with high profile (100), chromaFormatIDC=2 (4:2:2 chroma), frame cropping enabled.
// Tests the chromaFormatIDC==2 cropping branch (line 265 of sps.go).
// chroma=2: cropUnitX=2, cropUnitY=1. Uncropped: 640x128, cropped: 636x126.
var h264SPSHighChroma2Crop = []byte{
	0x67, 0x64, 0x00, 0x00, // NAL + profile(100) + constraints + level
	0xBC, 0xF0, 0x28, 0x11, // chroma=2 (ue=011), + exp-golomb: width=640, height=128
	0xD2, 0x48, // cropping: crop_left=1, crop_right=1, crop_top=1, crop_bottom=1
}

func TestParseSPSResolution_ChromaFormatIDC2(t *testing.T) {
	w, h, err := parseSPSResolution(h264SPSHighChroma2Crop)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// chroma=2: cropUnitX=2, cropUnitY=1
	// width  = 640 - (1+1)*2 = 636
	// height = 128 - (1+1)*1 = 126
	if w != 636 {
		t.Errorf("width = %d, want 636", w)
	}
	if h != 126 {
		t.Errorf("height = %d, want 126", h)
	}
}

// H.264 SPS with high profile (100), chromaFormatIDC=3 (4:4:4 chroma), frame cropping enabled.
// Tests the else branch (lines 266-267 of sps.go) — chroma=3 sets cropUnitX=cropUnitY=1.
// Uncropped: 640x128, cropped: 638x126.
var h264SPSHighChroma3Crop = []byte{
	0x67, 0x64, 0x00, 0x00, // NAL + profile(100) + constraints + level
	0x91, 0x9E, 0x05, 0x02, // chroma=3 fields + exp-golomb: width=640, height=128
	0x3A, 0x49, 0x00, // frame_mbs_only=1, direct=1, crop=1, crop_left=1, crop_right=1, crop_top=1, crop_bottom=1
}

func TestParseSPSResolution_ChromaFormatIDC3_Crop(t *testing.T) {
	w, h, err := parseSPSResolution(h264SPSHighChroma3Crop)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// chroma=3: cropUnitX=1, cropUnitY=1
	// width  = 640 - (1+1)*1 = 638
	// height = 128 - (1+1)*1 = 126
	if w != 638 {
		t.Errorf("width = %d, want 638", w)
	}
	if h != 126 {
		t.Errorf("height = %d, want 126", h)
	}
}

// H.264 SPS with high profile (100), chromaFormatIDC=3, scaling list present (all absent).
// Tests line 148 of sps.go (count=12 for chroma=3 scaling loop).
// Bit layout: profile=100, chroma=3, scaling_present=1, 12 absent presents, no crop.
var h264SPSHighChroma3Scaling = []byte{
	0x67, 0x64, 0x00, 0x00, // NAL + profile(100) + constraints + level
	0x91, 0xA0, 0x01, // chroma=3, scaling=1, 12 present flags (all 0) crossing into byte[6]
	0xE0, 0x50, // common fields + ue(39): width=640
	0x23, 0x00, // ue(7): height=128 + frame_mbs=1 + direct=1 + crop=0
}

func TestParseSPSResolution_ChromaFormatIDC3_Scaling(t *testing.T) {
	w, h, err := parseSPSResolution(h264SPSHighChroma3Scaling)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != 640 {
		t.Errorf("width = %d, want 640", w)
	}
	if h != 128 {
		t.Errorf("height = %d, want 128", h)
	}
}

// Truncated SPS that passes length checks but triggers readUE overflow.
// profile=100 reads 24 bits ok, then seq_param ue hits 31 zeros + separator,
// but readBits(31) overflows the 7-byte RBSP.
var h264SPSTruncatedUE = []byte{
	0x67, 0x64, 0x00, 0x00, // NAL + profile(100) + constraints + level
	0x00, 0x00, 0x00, 0x01, // zeros for seq_param ue + separator at bit 55, then overflow
}

func TestParseSPSResolution_TooShort(t *testing.T) {
	_, _, err := parseSPSResolution([]byte{0x67, 0x64, 0x00}) // 3 bytes < 8
	if err == nil {
		t.Fatal("expected error for SPS < 8 bytes")
	}
}

func TestParseSPSResolution_ReadUEOverflow(t *testing.T) {
	_, _, err := parseSPSResolution(h264SPSTruncatedUE)
	if err == nil {
		t.Fatal("expected error for truncated RBSP")
	}
}

func TestParseHEVCSPSResolution_TooShort(t *testing.T) {
	_, _, err := parseHEVCSPSResolution([]byte{0x42, 0x01, 0x01}) // 3 bytes < 8
	if err == nil {
		t.Fatal("expected error for HEVC SPS < 8 bytes")
	}
}

// HEVC SPS with exactly 14 bytes (RBSP = 12 bytes < 13).
var hevcSPSShortRBSP = []byte{
	0x42, 0x01, // NAL header (HEVC SPS)
	0x01, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // 12 bytes RBSP
}

func TestParseHEVCSPSResolution_RBSPSize(t *testing.T) {
	_, _, err := parseHEVCSPSResolution(hevcSPSShortRBSP)
	if err == nil {
		t.Fatal("expected error for HEVC SPS with RBSP < 13")
	}
}

// High profile SPS with scaling list present[0]=1 (all 16 deltas = se(0) = 0).
// Tests the present==1 delta loop body (lines 156-171 of sps.go).
var h264SPSHighProfileScalingPresent = []byte{
	0x67, 0x64, 0x00, 0x00, // NAL + profile(100) + constraints + level
	0xAD, // high profile: seq=1, chroma=010, depth=1,1, qpprime=0, scaling=1
	0xFF, 0xFF, 0x80, // present[0]=1 + 16 deltas (each se(0)=ue("1"), 1 bit) + present[1..7]=0
	0xF0, 0x28, // common fields (all ue(0)) + ue(39): width=640
	0x11, 0x80, // ue(7): height=128 + frame_mbs=1, direct=1, crop=0
}

func TestParseSPSResolution_ScalingListPresent(t *testing.T) {
	w, h, err := parseSPSResolution(h264SPSHighProfileScalingPresent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != 640 {
		t.Errorf("width = %d, want 640", w)
	}
	if h != 128 {
		t.Errorf("height = %d, want 128", h)
	}
}

// High profile SPS truncated to overflow during chromaFormatIDC readUE (lines 125-127).
// After profile(100)+constraints+level+seq_ue, 32 zeros follow to trigger leadingZeros overflow.
var h264SPSChromaUEOverflow = []byte{
	0x67, 0x64, 0x00, 0x00, // NAL + profile(100) + constraints + level
	0x80, 0x00, 0x00, 0x00, 0x00, // seq="1" then 35+ zeros → chroma ue leadingZeros overflow
}

func TestParseSPSResolution_ChromaUEOverflow(t *testing.T) {
	_, _, err := parseSPSResolution(h264SPSChromaUEOverflow)
	if err == nil {
		t.Fatal("expected error for chroma ue overflow, got nil")
	}
}

// High profile SPS truncated to overflow during bit_depth_luma_minus8 readUE (lines 133-135).
// After profile(100)+constraints+level+seq+chroma(1), 32+ zeros follow for leadingZeros overflow.
var h264SPSDepthLumaOverflow = []byte{
	0x67, 0x64, 0x00, 0x00, // NAL + profile(100) + constraints + level
	0xA0, 0x00, 0x00, 0x00, 0x00, // seq="1", chroma="010", then 34+ zeros → depth_luma ue overflow
}

func TestParseSPSResolution_DepthLumaOverflow(t *testing.T) {
	_, _, err := parseSPSResolution(h264SPSDepthLumaOverflow)
	if err == nil {
		t.Fatal("expected error for depth_luma ue overflow, got nil")
	}
}

// HEVC SPS with exactly 13 bytes RBSP (passes len>=13 check) and maxSubLayers=0.
// After fixed fields consume 104 bits, the readUE at line 330 overflows at offset 104.
// Tests the error return path at lines 330-332 of sps.go.
var hevcSPSSeqParamUEOverflow = []byte{
	0x42, 0x01, // NAL header (HEVC SPS)
	0x01, 0x01, // vps_id=0, max_sub_layers=0, nesting=1, profile=1
	0x00, 0x00, 0x00, 0x00, // profile_compatibility_flags (32 bits)
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // constraint indicators (48 bits)
	0x00, // general_level_idc (8 bits) = 13 bytes RBSP, no bits remain for seq_param readUE
}

func TestParseHEVCSPSResolution_SeqParamUEOverflow(t *testing.T) {
	_, _, err := parseHEVCSPSResolution(hevcSPSSeqParamUEOverflow)
	if err == nil {
		t.Fatal("expected error for seq_param ue overflow, got nil")
	}
}

