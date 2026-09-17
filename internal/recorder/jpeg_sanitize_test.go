package recorder

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

// makeTestJPEG encodes a tiny valid JPEG (a 2×2 gradient) for sanitize tests.
func makeSanitizeTestJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := range 8 {
		for x := range 8 {
			img.Set(x, y, color.RGBA{R: uint8(x * 32), G: uint8(y * 32), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatalf("encode test jpeg: %v", err)
	}
	return buf.Bytes()
}

// makeSynthStub builds the header-only JPEG stub the gortsplib rtpmjpeg
// depacketizer prepends when a sender packs complete JPEGs into RTP payloads:
// SOI + DQT + DHT + SOS with no entropy data and no EOI.
func makeSynthStub(t *testing.T, extraBytes int) []byte {
	t.Helper()
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8})                        // SOI
	b.Write([]byte{0xFF, 0xDB})                        // DQT
	binary.Write(&b, binary.BigEndian, uint16(2+1+64)) // 8-bit table len
	b.WriteByte(0x00)                                  // Pq=0 Tq=0
	for i := range 64 {
		b.WriteByte(byte(i + 1))
	}
	b.Write([]byte{0xFF, 0xC4}) // DHT (short fake)
	binary.Write(&b, binary.BigEndian, uint16(2+1+16))
	b.WriteByte(0x00)
	for i := range 16 {
		b.WriteByte(byte(i + 1))
	}
	b.Write([]byte{0xFF, 0xDA}) // SOS
	binary.Write(&b, binary.BigEndian, uint16(2+6))
	b.Write([]byte{1, 0, 2, 3, 4, 5})
	for range extraBytes {
		b.WriteByte(0xAB) // arbitrary junk (never 0xFF — no accidental SOI/EOI)
	}
	return b.Bytes()
}

func TestExtractCompleteJPEGs_CleanPassthrough(t *testing.T) {
	t.Parallel()
	clean := makeSanitizeTestJPEG(t)
	got := ExtractCompleteJPEGs(clean)
	if len(got) != 1 {
		t.Fatalf("clean single JPEG: want 1 image, got %d", len(got))
	}
	if !bytes.Equal(got[0], clean) {
		t.Fatal("clean JPEG must be returned byte-identical")
	}
}

func TestExtractCompleteJPEGs_StripsSynthesizedStub(t *testing.T) {
	t.Parallel()
	clean := makeSanitizeTestJPEG(t)
	buf := append(makeSynthStub(t, 60), clean...) // stub + complete image

	got := ExtractCompleteJPEGs(buf)
	if len(got) != 1 {
		t.Fatalf("stub+image: want 1 image, got %d", len(got))
	}
	if !bytes.Equal(got[0], clean) {
		t.Fatal("extracted image differs from the embedded complete JPEG")
	}
}

func TestExtractCompleteJPEGs_SplitsGluedFrames(t *testing.T) {
	t.Parallel()
	f1 := makeSanitizeTestJPEG(t)
	f2 := makeSanitizeTestJPEG(t)
	buf := append(append(makeSynthStub(t, 0), f1...), f2...) // stub + 2 complete images

	got := ExtractCompleteJPEGs(buf)
	if len(got) != 2 {
		t.Fatalf("stub+2 images: want 2 images, got %d", len(got))
	}
	if !bytes.Equal(got[0], f1) || !bytes.Equal(got[1], f2) {
		t.Fatal("glued images must be split in order")
	}
}

func TestExtractCompleteJPEGs_TrailingZeroPadding(t *testing.T) {
	t.Parallel()
	clean := makeSanitizeTestJPEG(t)
	buf := append(append([]byte{}, clean...), 0x00, 0x00, 0x00) // + AVI-style pad

	got := ExtractCompleteJPEGs(buf)
	if len(got) != 1 || !bytes.Equal(got[0], clean) {
		t.Fatalf("zero padding after EOI must be trimmed; got %d images", len(got))
	}
}

func TestExtractCompleteJPEGs_Garbage(t *testing.T) {
	t.Parallel()
	if got := ExtractCompleteJPEGs([]byte{0x01, 0x02, 0xFF, 0xD9}); len(got) != 0 {
		t.Fatalf("no SOI → want 0 images, got %d", len(got))
	}
	if got := ExtractCompleteJPEGs(nil); len(got) != 0 {
		t.Fatalf("nil → want 0 images, got %d", len(got))
	}
	// Stub only (no complete image anywhere) → nothing salvageable.
	if got := ExtractCompleteJPEGs(makeSynthStub(t, 100)); len(got) != 0 {
		t.Fatalf("stub only → want 0 images, got %d", len(got))
	}
}
