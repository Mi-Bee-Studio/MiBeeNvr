package mp4util

import (
	"bytes"
	"testing"

	"github.com/abema/go-mp4"
)

// marshalHvcC serializes the *mp4.HvcC record to its on-wire bytes (the payload
// of the hvcC box, without the 8-byte box header). Mirrors what mp4.Marshal
// emits inside the hvc1 sample entry so byte-level assertions match the
// production output exactly.
func marshalHvcC(t *testing.T, hvcC *mp4.HvcC) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := mp4.Marshal(&buf, hvcC, mp4.Context{}); err != nil {
		t.Fatalf("mp4.Marshal(hvcC) failed: %v", err)
	}
	return buf.Bytes()
}

// TestBuildHvcC_HeaderFields checks the fixed hvcC header fields and the
// conservative Main-tier / Main-profile defaults that make ONVIF SPS
// inconsistencies playable in Edge (PR #92 / #236).
func TestBuildHvcC_HeaderFields(t *testing.T) {
	vps := []byte{0x40, 0x01, 0x0c, 0x01, 0xff, 0xff} // arbitrary >1-byte NAL
	sps := []byte{
		0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03,
		0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x5d,
	}
	pps := []byte{0x44, 0x01, 0xc1, 0x73, 0xd1, 0x89}

	hvcC := marshalHvcC(t, BuildHvcC(vps, sps, pps))

	if len(hvcC) < 23 {
		t.Fatalf("hvcC too short: %d bytes (need >= 23 for header)", len(hvcC))
	}
	if hvcC[0] != 1 {
		t.Errorf("configurationVersion = %d, want 1", hvcC[0])
	}
	// Byte 1: profile_space(2) | tier_flag(1) | profile_idc(5). We force
	// space=0 and tier=0, so profile_idc occupies the low 5 bits alone.
	if hvcC[1]>>6 != 0 {
		t.Errorf("general_profile_space+tier_flag bits = 0b%03b, want 0 (Main tier)", hvcC[1]>>5)
	}
	// numOfArrays (byte 22) must be 3 (VPS/SPS/PPS).
	if hvcC[22] != 3 {
		t.Errorf("numOfArrays = %d, want 3", hvcC[22])
	}
}

// TestBuildHvcC_ArrayNumNalusIsOne is the regression guard for the bug where
// numNalus held the NALU byte length instead of 1 (PR #89). Per ISO 14496-15
// §8.3.3.1.2 each array is [type(1)][numNalus(2)][len(2)][data]; with one NALU
// per array numNalus must be exactly 1.
func TestBuildHvcC_ArrayNumNalusIsOne(t *testing.T) {
	vps := []byte{0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60} // 8 bytes (>1, catches len/numNalus confusion)
	sps := []byte{
		0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x00,
		0x03, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x5d, 0xac, 0x09,
	}
	pps := []byte{0x44, 0x01, 0xc1, 0x73, 0xd1, 0x89, 0x13, 0x34}
	if len(vps) <= 1 || len(sps) <= 1 || len(pps) <= 1 {
		t.Fatalf("test NALUs must be >1 byte")
	}

	hvcC := marshalHvcC(t, BuildHvcC(vps, sps, pps))

	off := 23                       // header is 23 bytes
	wantTypes := []byte{32, 33, 34} // VPS, SPS, PPS
	wantNALUs := [][]byte{vps, sps, pps}
	for i := range wantTypes {
		if off+5 > len(hvcC) {
			t.Fatalf("array %d: ran off end of hvcC at offset %d", i, off)
		}
		arrType := hvcC[off] & 0x3F
		if arrType != wantTypes[i] {
			t.Errorf("array %d: NAL_unit_type = %d, want %d", i, arrType, wantTypes[i])
		}
		numNalus := uint16(hvcC[off+1])<<8 | uint16(hvcC[off+2])
		if numNalus != 1 {
			t.Errorf("array %d (type %d): numNalus = %d, want 1", i, wantTypes[i], numNalus)
		}
		nalLen := uint16(hvcC[off+3])<<8 | uint16(hvcC[off+4])
		if nalLen != uint16(len(wantNALUs[i])) {
			t.Errorf("array %d: nalUnitLength = %d, want %d", i, nalLen, len(wantNALUs[i]))
		}
		off += 5 + len(wantNALUs[i])
	}
}

// TestBuildHvcC_ConservativeTierAndCompat pins the PR #92 behavior: when an
// ONVIF camera emits an inconsistent SPS (the production cam-fa049182 SPS with
// High-tier bits in sps[2] and a stray compat flag), the hvcC must still
// advertise Main tier + Main profile + zeroed compat/constraint so Edge's HEVC
// extension accepts the stream.
//
// This is the exact fixture from timelapse's TestBuildHvcC_ConservativeTierAndCompat;
// keeping it here means the shared helper is independently pinned without
// relying on the timelapse wrapper test.
func TestBuildHvcC_ConservativeTierAndCompat(t *testing.T) {
	// Inconsistent SPS captured from cam-fa049182:
	//   sps[0]=0x42 NAL header (type=33 SPS)
	//   sps[1]=0x01 layerID=0 + temporalID=1  → profile_idc (low 5 bits) = 1 (Main)
	//   sps[2]=0x21 profile_space=0 + tier_flag=1 (High!) + profile_idc=1
	//   sps[3..6]=0x40,0x00,0x00,0x03 compat_flags=0x40000003
	//   sps[7..12]=0x00,0x90,0x00,0x00,0x03,0x00 (constraint + level_idc=0)
	inconsistentSPS := []byte{
		0x42, 0x01, 0x21, 0x40, 0x00, 0x00, 0x03,
		0x00, 0x90, 0x00, 0x00, 0x03, 0x00,
		0x96, 0xa0, 0x01, 0x40, 0x20, 0x05, 0xa1,
	}
	pps := []byte{0x44, 0x01, 0xc1, 0x73, 0xd1, 0x89}
	vps := []byte{0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x01, 0x60}

	hvcC := marshalHvcC(t, BuildHvcC(vps, inconsistentSPS, pps))

	// Byte 1: profile_space(2) + tier_flag(1) + profile_idc(5).
	// Must be 0x01 (Main tier forced + Main profile), NOT 0x21 (which would
	// carry the SPS's High-tier bit). Edge rejects tier=1 paired with
	// profile_idc=1 as non-compliant.
	if got := hvcC[1]; got != 0x01 {
		t.Errorf("hvcC[1] = 0x%02x (space=%d tier=%d profile_idc=%d), want 0x01 "+
			"(Main tier + Main profile forced)", got, got>>6, (got>>5)&1, got&0x1F)
	}
	// Bytes 2-5: general_profile_compatibility_flags — must be zeroed, not the
	// SPS's 0x40000003. A stray compat bit paired with Main profile is another
	// Edge rejection trigger.
	for i := 2; i <= 5; i++ {
		if hvcC[i] != 0x00 {
			t.Errorf("hvcC[%d] = 0x%02x, want 0x00 (compat forced to zero)", i, hvcC[i])
		}
	}
	// Bytes 6-11: constraint indicator flags — must be zeroed.
	for i := 6; i <= 11; i++ {
		if hvcC[i] != 0x00 {
			t.Errorf("hvcC[%d] = 0x%02x, want 0x00 (constraint forced to zero)", i, hvcC[i])
		}
	}
}

// TestBuildHvcC_FallbackDefaults verifies the safe fallback when the SPS is too
// short to read profile/level: Main profile (1) + Level 4.0 (120), not 0/0.
// This is the behavior change vs. the old merge/muxer typed builders, which
// fell back to 0/0 — a latent bug fixed by unifying on the timelapse defaults
// (#236).
func TestBuildHvcC_FallbackDefaults(t *testing.T) {
	// SPS too short for both reads: len <= 1 skips the profile read, len <= 12
	// skips the level read. Use len==1 so BOTH fallbacks trigger.
	shortSPS := []byte{0x42}
	vps := []byte{0x40, 0x01}
	pps := []byte{0x44, 0x01}

	hvcC := marshalHvcC(t, BuildHvcC(vps, shortSPS, pps))

	// Byte 1 low 5 bits = profile_idc fallback = 1 (Main).
	if got := hvcC[1] & 0x1F; got != defaultHEVCProfile {
		t.Errorf("profile fallback = %d, want %d (Main)", got, defaultHEVCProfile)
	}
	// Byte 12 = level_idc fallback = 120 (Level 4.0).
	if hvcC[12] != defaultHEVCLevel {
		t.Errorf("level fallback = %d, want %d (Level 4.0)", hvcC[12], defaultHEVCLevel)
	}
}

// TestBuildAvcC verifies the avcC record carries sps[1..3] verbatim and wraps
// one SPS + one PPS with correct lengths.
func TestBuildAvcC(t *testing.T) {
	sps := []byte{0x67, 0x42, 0xc0, 0x1e, 0xda, 0x02, 0xd0} // 7 bytes
	pps := []byte{0x68, 0xce, 0x3c, 0x80}                   // 4 bytes

	rec := BuildAvcC(sps, pps)
	if rec.ConfigurationVersion != 1 {
		t.Errorf("ConfigurationVersion = %d, want 1", rec.ConfigurationVersion)
	}
	if rec.Profile != sps[1] {
		t.Errorf("Profile = 0x%02x, want 0x%02x (sps[1] verbatim)", rec.Profile, sps[1])
	}
	if rec.ProfileCompatibility != sps[2] {
		t.Errorf("ProfileCompatibility = 0x%02x, want 0x%02x (sps[2] verbatim)", rec.ProfileCompatibility, sps[2])
	}
	if rec.Level != sps[3] {
		t.Errorf("Level = 0x%02x, want 0x%02x (sps[3] verbatim)", rec.Level, sps[3])
	}
	if rec.LengthSizeMinusOne != 3 {
		t.Errorf("LengthSizeMinusOne = %d, want 3", rec.LengthSizeMinusOne)
	}
	if rec.NumOfSequenceParameterSets != 1 {
		t.Errorf("NumOfSequenceParameterSets = %d, want 1", rec.NumOfSequenceParameterSets)
	}
	if rec.NumOfPictureParameterSets != 1 {
		t.Errorf("NumOfPictureParameterSets = %d, want 1", rec.NumOfPictureParameterSets)
	}
	if len(rec.SequenceParameterSets) != 1 || rec.SequenceParameterSets[0].Length != uint16(len(sps)) {
		t.Errorf("SPS entry wrong: got %+v", rec.SequenceParameterSets)
	}
	if len(rec.PictureParameterSets) != 1 || rec.PictureParameterSets[0].Length != uint16(len(pps)) {
		t.Errorf("PPS entry wrong: got %+v", rec.PictureParameterSets)
	}
}

// TestBuildAvcC_FallbackDefaults verifies the Baseline / Level 3.0 fallback
// when the SPS is shorter than 4 bytes. This matches the former timelapse
// hand-rolled builder's behavior and fixes a panic latent in the merge/muxer
// inline avcC literals (#236).
func TestBuildAvcC_FallbackDefaults(t *testing.T) {
	shortSPS := []byte{0x67, 0x42} // len < 4 → fallback kicks in
	pps := []byte{0x68, 0xce, 0x3c, 0x80}

	rec := BuildAvcC(shortSPS, pps)
	if rec.Profile != defaultAVCProfile {
		t.Errorf("Profile fallback = %d, want %d (Baseline)", rec.Profile, defaultAVCProfile)
	}
	if rec.ProfileCompatibility != defaultAVCCompat {
		t.Errorf("ProfileCompatibility fallback = 0x%02x, want 0x%02x", rec.ProfileCompatibility, defaultAVCCompat)
	}
	if rec.Level != defaultAVCLevel {
		t.Errorf("Level fallback = %d, want %d (Level 3.0)", rec.Level, defaultAVCLevel)
	}
}
