package main

// repair_remerge_h265_test.go — tests for detectBuggyHvcC.
//
// Strategy: we don't hand-build MP4 box trees (fragile). Instead we generate
// a known-good H.265 MP4 via the real H265GoMerger (which produces the
// conservative/fixed hvcC from PR #92), then for the "buggy" test case we
// find the hvcC box inside that file and flip its tier bit to simulate the
// pre-PR-#92 bug. This exercises detectBuggyHvcC against real MP4 bytes
// the way it will see them in production.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
)

// generateFixedMP4 produces a small valid H.265 MP4 via the current merger.
// Returns the path. The hvcC inside will have tier=0 + profile_idc=1
// (the conservative/Edge-playable form).
func generateFixedMP4(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	framesDir := filepath.Join(tmp, "frames")
	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(tmp, "merged.mp4")

	// Minimal but valid Annex-B H.265 access unit. SPS carries tier=0 +
	// profile_idc=1 (Main), so the merger's fixed buildHvcC produces
	// tier=0 + profile_idc=1 (Edge-playable).
	vps := []byte{0x40, 0x01, 0x0c, 0x01, 0xff, 0xff, 0x21, 0x40, 0x00, 0x00, 0x03, 0x00, 0x90, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x96, 0x00, 0x80}
	sps := []byte{0x42, 0x01, 0x01, 0x01, 0x60, 0x00, 0x00, 0x03, 0x00, 0x90, 0x00, 0x00, 0x03, 0x00, 0x00, 0x03, 0x00, 0x96, 0xa0, 0x01, 0x40, 0x20, 0x05, 0xa1, 0x67, 0xae, 0xe4, 0x4a, 0x17, 0x35, 0x01, 0x01, 0x01, 0x04, 0x00, 0x00, 0x03, 0x00, 0x04, 0x00, 0x00, 0x03, 0x00, 0x50, 0x20}
	pps := []byte{0x44, 0x01, 0xc0, 0xf7, 0xc0, 0xe6, 0xd9}
	idr := []byte{0x26, 0x01, 0xaf, 0x13, 0x60, 0x00, 0x00, 0x80}

	var frame bytes.Buffer
	for _, nalu := range [][]byte{vps, sps, pps, idr} {
		frame.Write([]byte{0x00, 0x00, 0x00, 0x01})
		frame.Write(nalu)
	}
	if err := os.WriteFile(filepath.Join(framesDir, "frame_000000.h265"), frame.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	m := timelapse.NewH265GoMerger()
	res, err := m.Merge(context.Background(), framesDir, outputPath, 10)
	if err != nil {
		t.Fatalf("merger.Merge: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("merger reported error: %s", res.Error)
	}
	return outputPath
}

// patchHvcCTierBit finds the hvcC box inside an MP4 and sets/clears the
// GeneralTierFlag bit (bit 5 of the byte after "hvcC" + box header). Used to
// simulate the pre-PR-#92 bug on an otherwise-valid file.
//
// hvcC box layout: [size(4)][type="hvcC"(4)][payload...]. The payload's byte
// 1 is profile_space(2)+tier_flag(1)+profile_idc(5). Bit 0x20 is tier_flag.
func patchHvcCTierBit(t *testing.T, srcPath, dstPath string, setTier bool) {
	t.Helper()
	data, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	idx := bytes.Index(data, []byte("hvcC"))
	if idx < 0 || idx < 4 {
		t.Fatalf("hvcC magic not found in %s", srcPath)
	}
	// idx points at the "hvcC" type field. The payload starts at idx+4.
	// Payload byte 0 = configurationVersion; byte 1 = profile+tier.
	payloadByte1Offset := idx + 4 + 1
	if setTier {
		data[payloadByte1Offset] |= 0x20 // set tier_flag = High
	} else {
		data[payloadByte1Offset] &^= 0x20 // clear tier_flag = Main
	}
	if err := os.WriteFile(dstPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDetectBuggyHvcC_FixedMergerOutput verifies the current merger's output
// is NOT flagged as buggy.
func TestDetectBuggyHvcC_FixedMergerOutput(t *testing.T) {
	t.Parallel()
	path := generateFixedMP4(t)

	isBuggy, codec, err := detectBuggyHvcC(path)
	if err != nil {
		t.Fatalf("detectBuggyHvcC: %v", err)
	}
	if codec != "h265" {
		t.Errorf("codec = %q, want \"h265\"", codec)
	}
	if isBuggy {
		t.Errorf("isBuggy = true for fresh PR #92 merger output — merger must NOT emit tier=1 + profile_idc=1")
	}
}

// TestDetectBuggyHvcC_PatchedBuggyFile takes a fixed merger output and flips
// the tier bit to simulate the pre-PR-#92 bug, then asserts detectBuggyHvcC
// catches it.
func TestDetectBuggyHvcC_PatchedBuggyFile(t *testing.T) {
	t.Parallel()
	fixedPath := generateFixedMP4(t)
	buggyPath := filepath.Join(t.TempDir(), "buggy.mp4")
	patchHvcCTierBit(t, fixedPath, buggyPath, true)

	isBuggy, codec, err := detectBuggyHvcC(buggyPath)
	if err != nil {
		t.Fatalf("detectBuggyHvcC: %v", err)
	}
	if codec != "h265" {
		t.Errorf("codec = %q, want \"h265\"", codec)
	}
	if !isBuggy {
		t.Errorf("isBuggy = false, want true — tier=1 + profile_idc=1 is the Edge-rejected mismatch")
	}
}

// TestDetectBuggyHvcC_NonExistentFile returns an error.
func TestDetectBuggyHvcC_NonExistentFile(t *testing.T) {
	t.Parallel()
	_, _, err := detectBuggyHvcC(filepath.Join(t.TempDir(), "does-not-exist.mp4"))
	if err == nil {
		t.Errorf("expected error for non-existent file, got nil")
	}
}

// TestDetectBuggyHvcC_NonMP4File must not crash or false-positive.
func TestDetectBuggyHvcC_NonMP4File(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "text.txt")
	if err := os.WriteFile(path, []byte("hello world not an mp4"), 0o644); err != nil {
		t.Fatal(err)
	}
	isBuggy, codec, err := detectBuggyHvcC(path)
	// Either an error OR (false, "", nil) is acceptable — we just don't want
	// a false-positive "buggy" or a crash.
	if err == nil {
		if codec != "" {
			t.Errorf("codec = %q for plain text, want \"\"", codec)
		}
		if isBuggy {
			t.Errorf("isBuggy = true for plain text — must not false-positive")
		}
	}
}

// TestPatchHvcCTierBit_RoundTrip is a sanity check on the patch helper itself,
// ensuring the bit flip is reversible and lands on the right byte.
func TestPatchHvcCTierBit_RoundTrip(t *testing.T) {
	t.Parallel()
	fixedPath := generateFixedMP4(t)

	// Read original hvcC byte 1.
	orig, err := os.ReadFile(fixedPath)
	if err != nil {
		t.Fatal(err)
	}
	idx := bytes.Index(orig, []byte("hvcC"))
	origTierBit := orig[idx+5] & 0x20 // payload byte 1 = offset idx+4+1

	// Patch to buggy, read back.
	buggyPath := filepath.Join(t.TempDir(), "buggy.mp4")
	patchHvcCTierBit(t, fixedPath, buggyPath, true)
	buggy, _ := os.ReadFile(buggyPath)
	buggyTierBit := buggy[idx+5] & 0x20

	// Patch back to fixed, read back.
	fixed2Path := filepath.Join(t.TempDir(), "fixed2.mp4")
	patchHvcCTierBit(t, buggyPath, fixed2Path, false)
	fixed2, _ := os.ReadFile(fixed2Path)
	fixed2TierBit := fixed2[idx+5] & 0x20

	if origTierBit != 0 {
		t.Errorf("original merger output has tier bit set — merger should produce tier=0")
	}
	if buggyTierBit != 0x20 {
		t.Errorf("after patchHvcCTierBit(set=true), tier bit should be 0x20, got %#02x", buggyTierBit)
	}
	if fixed2TierBit != 0 {
		t.Errorf("after patchHvcCTierBit(set=false), tier bit should be 0, got %#02x", fixed2TierBit)
	}
	// Byte 1 low 5 bits (profile_idc) must be untouched by the patch.
	if orig[idx+5]&0x1F != buggy[idx+5]&0x1F {
		t.Errorf("patch corrupted profile_idc: orig=%#02x buggy=%#02x", orig[idx+5], buggy[idx+5])
	}
	// Everything except byte 1 of hvcC payload must be byte-identical.
	if !bytes.Equal(orig[:idx+5], buggy[:idx+5]) || !bytes.Equal(orig[idx+6:], buggy[idx+6:]) {
		t.Errorf("patch modified bytes outside hvcC payload byte 1")
	}
}
