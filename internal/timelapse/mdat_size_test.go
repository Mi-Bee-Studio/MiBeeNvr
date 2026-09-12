package timelapse

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	mp4 "github.com/abema/go-mp4"
)

// TestMdatHeaderSize_SelectsLargeHeaderBeyond4GB pins the #mdat-4gb fix: a
// busy natural-day window's mdat payload exceeds 4GB (2026-09-13 incident:
// 20.6GB source day → ~6.5GB payload). The old code accumulated the payload
// in uint32, wrapped to ~2.2GB, StartBox wrote an 8-byte small header, and
// EndBox failed with "header size changed" because the real size needed the
// 16-byte largesize header.
func TestMdatHeaderSize_SelectsLargeHeaderBeyond4GB(t *testing.T) {
	cases := []struct {
		name    string
		payload uint64
		want    uint64
	}{
		{"small payload uses 8-byte header", 1000, 8},
		{"exactly uint32 max minus 8 still fits small header", 0xFFFFFFF7, 8},
		{"one past the small-box limit needs largesize", 0xFFFFFFF8, 16},
		{"6.5GB incident payload needs largesize", 6_500_000_000, 16},
		{"8GB payload needs largesize", 8 << 30, 16},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Helper()
			if got := mdatHeaderSize(tc.payload); got != tc.want {
				t.Fatalf("mdatHeaderSize(%d) = %d, want %d", tc.payload, got, tc.want)
			}
		})
	}
}

// TestComputeMdatPayloadSize_AccumulatesWithoutParamSets verifies the
// extracted first pass: per-NALU 4-byte length prefixes accumulate, param
// sets are excluded, and the accumulation is uint64-typed (see
// TestMdatHeaderSize_SelectsLargeHeaderBeyond4GB for the >4GB regression).
func TestComputeMdatPayloadSize_AccumulatesWithoutParamSets(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	// One "frame" holding two non-param-set NALUs (10B + 6B) and one SPS.
	// AnnexB start codes separate the NALUs; isH265ParamSet sees SPS (NAL
	// type 33 in the first byte's high 6 bits: 33<<1 = 0x42).
	frame := append([]byte{0, 0, 0, 1, 0x26, 1, 2, 3, 4, 5, 6, 7, 8, 9},
		0, 0, 0, 1, 0x02, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE,
		0, 0, 0, 1, 0x42, 1, 2, 3, 4)
	path := filepath.Join(dir, "frame_000001.h265")
	if err := os.WriteFile(path, frame, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := computeMdatPayloadSize(context.Background(), []string{path}, isH265ParamSet)
	if err != nil {
		t.Fatal(err)
	}
	// (4+10) + (4+6); SPS excluded.
	if want := uint64(24); got != want {
		t.Fatalf("computeMdatPayloadSize = %d, want %d", got, want)
	}
}

// TestComputeMdatPayloadSize_MissingFileErrors keeps the read-failure
// contract of the former inline first pass.
func TestComputeMdatPayloadSize_MissingFileErrors(t *testing.T) {
	t.Helper()
	if _, err := computeMdatPayloadSize(context.Background(), []string{"/nonexistent/frame.h265"}, isH265ParamSet); err == nil {
		t.Fatal("want error for missing frame file, got nil")
	}
}

// TestWriteCodecMdat_UsesProvidedPayloadSize guards the signature change:
// the caller-precomputed payload drives the mdat box size so the moov stco
// offset and the mdat header agree before any bytes stream.
func TestWriteCodecMdat_UsesProvidedPayloadSize(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "out.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	frame := []byte{0, 0, 0, 1, 0x26, 1, 2, 3, 4, 5} // one 6B NALU
	framePath := filepath.Join(dir, "frame_000001.h265")
	if err := os.WriteFile(framePath, frame, 0o644); err != nil {
		t.Fatal(err)
	}

	payload, err := computeMdatPayloadSize(context.Background(), []string{framePath}, isH265ParamSet)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeCodecMdat(mp4.NewWriter(f), []string{framePath}, payload, context.Background(), isH265ParamSet); err != nil {
		t.Fatalf("writeCodecMdat: %v", err)
	}

	// mdat box size written in the header must match header+payload.
	info, err := f.Seek(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = info
	header := make([]byte, 8)
	if _, err := f.Read(header); err != nil {
		t.Fatal(err)
	}
	if string(header[4:8]) != "mdat" {
		t.Fatalf("box type = %q, want mdat", string(header[4:8]))
	}
	boxSize := uint64(header[0])<<24 | uint64(header[1])<<16 | uint64(header[2])<<8 | uint64(header[3])
	if want := mdatHeaderSize(payload) + payload; boxSize != want {
		t.Fatalf("mdat box size = %d, want %d", boxSize, want)
	}
}
