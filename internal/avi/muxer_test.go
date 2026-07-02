package avi

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
)

// Helper: create a minimal MJPEG frame (JFIF header + some data).
func makeJPEGFrame(t *testing.T, seed byte, size int) []byte {
	t.Helper()
	frame := make([]byte, size)
	frame[0] = 0xFF
	frame[1] = 0xD8
	for i := 2; i < size; i++ {
		frame[i] = seed + byte(i)
	}
	return frame
}

// Helper: create G.711 mu-law audio data.
func makeG711Audio(t *testing.T, seed byte, size int) []byte {
	t.Helper()
	data := make([]byte, size)
	for i := range data {
		data[i] = seed + byte(i)
	}
	return data
}

// readU32LE reads a uint32 in little-endian from data at offset.
func readU32LE(t *testing.T, data []byte, offset int) uint32 {
	t.Helper()
	if offset+4 > len(data) {
		return 0
	}
	return binary.LittleEndian.Uint32(data[offset:])
}

// fourccStr converts a LE uint32 FOURCC value back to its ASCII representation.
// The FOURCC is stored in LE: e.g. fccAVI=0x20205641 stores bytes [0x41,0x56,0x49,0x20]="AVI ".
func fourccStr(v uint32) string {
	b := [4]byte{
		byte(v),
		byte(v >> 8),
		byte(v >> 16),
		byte(v >> 24),
	}
	return string(b[:])
}

// findFOURCC finds the offset of a given FOURCC uint32 value in data.
func findFOURCC(t *testing.T, data []byte, fourcc uint32) int {
	t.Helper()
	for i := 0; i <= len(data)-4; i++ {
		if binary.LittleEndian.Uint32(data[i:]) == fourcc {
			return i
		}
	}
	return -1
}

// TestAVIMuxer_RIFFLayout verifies the overall RIFF structure.
func TestAVIMuxer_RIFFLayout(t *testing.T) {
	var buf bytes.Buffer
	m := NewMuxer(&buf, 640, 480, 8000, true)

	if err := m.WriteVideo(makeJPEGFrame(t, 0xAA, 100), 0); err != nil {
		t.Fatalf("WriteVideo: %v", err)
	}
	if err := m.WriteAudio(makeG711Audio(t, 0xBB, 80), 0); err != nil {
		t.Fatalf("WriteAudio: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data := buf.Bytes()
	t.Logf("Total file size: %d bytes", len(data))

	// Check RIFF magic at offset 0.
	if v := binary.LittleEndian.Uint32(data[0:]); v != fccRIFF {
		t.Fatalf("expected RIFF at offset 0, got 0x%08X", v)
	}
	riffSize := readU32LE(t, data, 4)
	if int(riffSize) != len(data)-8 {
		t.Errorf("RIFF size mismatch: expected %d, got %d", len(data)-8, riffSize)
	}
	// Check AVI form type at offset 8.
	if v := binary.LittleEndian.Uint32(data[8:]); v != fccAVI {
		t.Fatalf("expected 'AVI ' at offset 8, got 0x%08X (%q)", v, fourccStr(v))
	}

	// Check hdrl LIST.
	if binary.LittleEndian.Uint32(data[12:]) != fccLIST {
		t.Fatal("expected LIST at offset 12")
	}
	if binary.LittleEndian.Uint32(data[20:]) != fcchdrl {
		t.Fatal("expected hdrl at offset 20")
	}

	// Check avih chunk inside hdrl.
	avihOff := findFOURCC(t, data, fccavih)
	if avihOff < 0 {
		t.Fatal("avih not found")
	}
	avihSize := readU32LE(t, data, avihOff+4)
	if avihSize != 56 {
		t.Errorf("avih size: expected 56, got %d", avihSize)
	}

	// Check avih.dwStreams = 2 at avihOff + 8 + 24 = avihOff + 32.
	dwStreams := readU32LE(t, data, avihOff+8+24)
	if dwStreams != 2 {
		t.Errorf("dwStreams: expected 2, got %d", dwStreams)
	}

	// Check video strh has fccType='vids'.
	strhOff := findFOURCC(t, data, fccstrh)
	if strhOff < 0 {
		t.Fatal("strh not found")
	}
	// strh data starts at strhOff+8.
	// fccType is at strhOff+8+0 = strhOff+8.
	if v := binary.LittleEndian.Uint32(data[strhOff+8:]); v != fccvids {
		t.Fatalf("expected 'vids' in video strh, got 0x%08X (%q)", v, fourccStr(v))
	}

	// Check fccHandler = 'MJPG' at strhOff+8+4 = strhOff+12.
	if v := binary.LittleEndian.Uint32(data[strhOff+12:]); v != fccMJPG {
		t.Fatalf("expected 'MJPG' in video strh, got 0x%08X (%q)", v, fourccStr(v))
	}

	// Check video strf (BITMAPINFOHEADER).
	strfOff := findFOURCC(t, data, fccstrf)
	if strfOff < 0 {
		t.Fatal("strf not found")
	}
	strfSize := readU32LE(t, data, strfOff+4)
	if strfSize != 40 {
		t.Errorf("video strf size: expected 40, got %d", strfSize)
	}
	// biWidth at strfOff+8+4
	biWidth := readU32LE(t, data, strfOff+8+4)
	if biWidth != 640 {
		t.Errorf("biWidth: expected 640, got %d", biWidth)
	}
	// biHeight at strfOff+8+8
	biHeight := readU32LE(t, data, strfOff+8+8)
	if biHeight != 480 {
		t.Errorf("biHeight: expected 480, got %d", biHeight)
	}
	// biCompression = 'MJPG' at strfOff+8+16
	if v := binary.LittleEndian.Uint32(data[strfOff+8+16:]); v != fccMJPG {
		t.Errorf("biCompression: expected MJPG (0x%08X), got 0x%08X", fccMJPG, v)
	}

	// Check audio strh has fccType='auds'.
	// Find the second strh.
	strhCount := 0
	var audioStrhOff int
	for i := 0; i <= len(data)-4; i++ {
		if binary.LittleEndian.Uint32(data[i:]) == fccstrh {
			strhCount++
			if strhCount == 2 {
				audioStrhOff = i
				break
			}
		}
	}
	if audioStrhOff == 0 {
		t.Fatal("audio strh not found")
	}
	if v := binary.LittleEndian.Uint32(data[audioStrhOff+8:]); v != fccauds {
		t.Fatalf("expected 'auds' in audio strh, got 0x%08X (%q)", v, fourccStr(v))
	}

	// Check audio WAVEFORMATEX - find the second strf.
	strfCount := 0
	var audioStrfOff int
	for i := 0; i <= len(data)-4; i++ {
		if binary.LittleEndian.Uint32(data[i:]) == fccstrf {
			strfCount++
			if strfCount == 2 {
				audioStrfOff = i
				break
			}
		}
	}
	if audioStrfOff == 0 {
		t.Fatal("audio strf not found")
	}
	// WAVEFORMATEX: wFormatTag at audioStrfOff+8+0
	wFormatTag := readU32LE(t, data, audioStrfOff+8) & 0xFFFF
	if wFormatTag != 0x0006 {
		t.Errorf("wFormatTag: expected 0x0006 (MU-LAW), got 0x%04X", wFormatTag)
	}

	// Check idx1 at the end.
	idx1Off := findFOURCC(t, data, fccidx1)
	if idx1Off < 0 {
		t.Fatal("idx1 not found")
	}
	idx1DataSize := readU32LE(t, data, idx1Off+4)
	expectedIdx1Size := 2 * 16 // 2 entries * 16 bytes
	if int(idx1DataSize) != expectedIdx1Size {
		t.Errorf("idx1 data size: expected %d, got %d", expectedIdx1Size, idx1DataSize)
	}
}

// TestAVIMuxer_MJPEGVideoChunk verifies 00dc fourcc and chunk structure.
func TestAVIMuxer_MJPEGVideoChunk(t *testing.T) {
	var buf bytes.Buffer
	m := NewMuxer(&buf, 320, 240, 8000, true)

	frame := makeJPEGFrame(t, 0xCC, 50)
	if err := m.WriteVideo(frame, 0); err != nil {
		t.Fatalf("WriteVideo: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data := buf.Bytes()

	// Find 00dc chunk in movi.
	dcOff := findFOURCC(t, data, fcc00dc)
	if dcOff < 0 {
		t.Fatal("00dc not found")
	}
	// Verify chunk data matches.
	chunkSize := readU32LE(t, data, dcOff+4)
	if int(chunkSize) != len(frame) {
		t.Errorf("chunk data size: expected %d, got %d", len(frame), chunkSize)
	}
	chunkData := data[dcOff+8 : dcOff+8+int(chunkSize)]
	if !bytes.Equal(chunkData, frame) {
		t.Error("chunk data does not match written frame")
	}
}

// TestAVIMuxer_G711AudioChunk verifies 01wb fourcc and WAVEFORMATEX.
func TestAVIMuxer_G711AudioChunk(t *testing.T) {
	var buf bytes.Buffer
	m := NewMuxer(&buf, 320, 240, 8000, true)

	if err := m.WriteAudio(makeG711Audio(t, 0xDD, 160), 0); err != nil {
		t.Fatalf("WriteAudio: %v", err)
	}
	if err := m.WriteVideo(makeJPEGFrame(t, 0xEE, 60), 0); err != nil {
		t.Fatalf("WriteVideo: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data := buf.Bytes()

	// Find audio strf.
	strfCount := 0
	var audioStrfOff int
	for i := 0; i <= len(data)-4; i++ {
		if binary.LittleEndian.Uint32(data[i:]) == fccstrf {
			strfCount++
			if strfCount == 2 {
				audioStrfOff = i
				break
			}
		}
	}
	if audioStrfOff == 0 {
		t.Fatal("audio strf not found")
	}

	// Check wFormatTag = 0x0006 (MU-LAW)
	wFormatTag := readU32LE(t, data, audioStrfOff+8) & 0xFFFF
	if wFormatTag != 0x0006 {
		t.Errorf("wFormatTag: expected 0x0006 (MU-LAW), got 0x%04X", wFormatTag)
	}

	// nChannels = 1 at +10
	nChannels := readU32LE(t, data, audioStrfOff+10) & 0xFFFF
	if nChannels != 1 {
		t.Errorf("nChannels: expected 1, got %d", nChannels)
	}

	// nSamplesPerSec = 8000 at +12
	nSamplesPerSec := readU32LE(t, data, audioStrfOff+12)
	if nSamplesPerSec != 8000 {
		t.Errorf("nSamplesPerSec: expected 8000, got %d", nSamplesPerSec)
	}

	// wBitsPerSample = 8 at +18 (2 bytes)
	wBitsPerSample := readU32LE(t, data, audioStrfOff+22) & 0xFFFF
	if wBitsPerSample != 8 {
		t.Errorf("wBitsPerSample: expected 8, got %d", wBitsPerSample)
	}

	// Find first chunk in movi - should be 01wb (audio, written first).
	wbOff := findFOURCC(t, data, fcc01wb)
	if wbOff < 0 {
		t.Fatal("01wb not found")
	}
	chunkSize := readU32LE(t, data, wbOff+4)
	if chunkSize != 160 {
		t.Errorf("audio chunk size: expected 160, got %d", chunkSize)
	}

	// Next chunk should be 00dc (video).
	nextOff := wbOff + 8 + int(chunkSize)
	if chunkSize%2 == 1 {
		nextOff++
	}
	if binary.LittleEndian.Uint32(data[nextOff:]) != fcc00dc {
		t.Fatalf("expected 00dc after audio, got 0x%08X", binary.LittleEndian.Uint32(data[nextOff:]))
	}
}

// TestAVIMuxer_AlawFormat verifies A-law format tag.
func TestAVIMuxer_AlawFormat(t *testing.T) {
	var buf bytes.Buffer
	m := NewMuxer(&buf, 320, 240, 8000, false) // A-law

	if err := m.WriteVideo(makeJPEGFrame(t, 0xFF, 50), 0); err != nil {
		t.Fatalf("WriteVideo: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data := buf.Bytes()

	// Find audio strf.
	var audioStrfOff int
	strfCount := 0
	for i := 0; i <= len(data)-4; i++ {
		if binary.LittleEndian.Uint32(data[i:]) == fccstrf {
			strfCount++
			if strfCount == 2 {
				audioStrfOff = i
				break
			}
		}
	}
	if audioStrfOff == 0 {
		t.Fatal("audio strf not found")
	}
	wFormatTag := readU32LE(t, data, audioStrfOff+8) & 0xFFFF
	if wFormatTag != 0x0007 {
		t.Errorf("wFormatTag: expected 0x0007 (A-LAW), got 0x%04X", wFormatTag)
	}
}

// TestAVIMuxer_AVIHeaderFields verifies avih fields.
func TestAVIMuxer_AVIHeaderFields(t *testing.T) {
	var buf bytes.Buffer
	m := NewMuxer(&buf, 1920, 1080, 8000, true)
	for i := 0; i < 5; i++ {
		if err := m.WriteVideo(makeJPEGFrame(t, byte(i), 200+i), 0); err != nil {
			t.Fatalf("WriteVideo %d: %v", i, err)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data := buf.Bytes()

	avihOff := findFOURCC(t, data, fccavih)
	if avihOff < 0 {
		t.Fatal("avih not found")
	}

	// avih data starts at avihOff+8.
	// dwStreams = 2 at avihOff+8+24
	dwStreams := readU32LE(t, data, avihOff+8+24)
	if dwStreams != 2 {
		t.Errorf("dwStreams: expected 2, got %d", dwStreams)
	}

	// dwTotalFrames at avihOff+8+16
	dwTotalFrames := readU32LE(t, data, avihOff+8+16)
	if dwTotalFrames != 5 {
		t.Errorf("dwTotalFrames: expected 5, got %d", dwTotalFrames)
	}

	// dwWidth at avihOff+8+32
	dwWidth := readU32LE(t, data, avihOff+8+32)
	if dwWidth != 1920 {
		t.Errorf("dwWidth: expected 1920, got %d", dwWidth)
	}

	// dwHeight at avihOff+8+36
	dwHeight := readU32LE(t, data, avihOff+8+36)
	if dwHeight != 1080 {
		t.Errorf("dwHeight: expected 1080, got %d", dwHeight)
	}

	// Check flags at avihOff+8+12
	flags := readU32LE(t, data, avihOff+8+12)
	if flags&avifHasIndex == 0 {
		t.Error("AVIF_HASINDEX flag not set")
	}

	// Find video strh and check dwLength = 5
	// strh data starts at strhOff+8. dwLength is at +32.
	strhCount := 0
	var videoStrhOff int
	for i := 0; i <= len(data)-4; i++ {
		if binary.LittleEndian.Uint32(data[i:]) == fccstrh {
			strhCount++
			if strhCount == 1 {
				videoStrhOff = i
				break
			}
		}
	}
	if videoStrhOff == 0 {
		t.Fatal("video strh not found")
	}
	dwLength := readU32LE(t, data, videoStrhOff+8+32)
	if dwLength != 5 {
		t.Errorf("video dwLength: expected 5, got %d", dwLength)
	}
}

// TestAVIMuxer_EmptyFile tests creating a muxer with no data then closing.
func TestAVIMuxer_EmptyFile(t *testing.T) {
	var buf bytes.Buffer
	m := NewMuxer(&buf, 640, 480, 8000, true)
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data := buf.Bytes()
	if len(data) == 0 {
		t.Fatal("empty file produced")
	}
	if v := binary.LittleEndian.Uint32(data[0:]); v != fccRIFF {
		t.Fatalf("expected RIFF, got 0x%08X", v)
	}
}

// TestAVIMuxer_MultipleChunks verifies many interleaved chunks.
func TestAVIMuxer_MultipleChunks(t *testing.T) {
	var buf bytes.Buffer
	m := NewMuxer(&buf, 640, 480, 8000, true)

	for i := 0; i < 3; i++ {
		if err := m.WriteVideo(makeJPEGFrame(t, byte(i), 100+i*10), int64(i)*33333); err != nil {
			t.Fatalf("WriteVideo %d: %v", i, err)
		}
		if err := m.WriteAudio(makeG711Audio(t, byte(i), 80+i*5), int64(i)*1000000/30); err != nil {
			t.Fatalf("WriteAudio %d: %v", i, err)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data := buf.Bytes()

	// Verify chunk order: 00dc, 01wb, 00dc, 01wb, 00dc, 01wb.
	expectedOrder := []uint32{fcc00dc, fcc01wb, fcc00dc, fcc01wb, fcc00dc, fcc01wb}
	var chunks []uint32
	off := findFOURCC(t, data, fccmovi)
	if off < 0 {
		t.Fatal("movi not found")
	}
	off += 4 // skip 'movi' fourcc

	for i := 0; i < 6; i++ {
		if off+8 > len(data) {
			t.Fatalf("chunk %d: out of data at offset %d", i, off)
		}
		ckID := binary.LittleEndian.Uint32(data[off:])
		chunks = append(chunks, ckID)
		ckSize := readU32LE(t, data, off+4)
		off += 8 + int(ckSize)
		if int(ckSize)%2 == 1 {
			off++
		}
	}

	for i, expected := range expectedOrder {
		if i >= len(chunks) {
			t.Errorf("chunk %d: missing, expected 0x%08X", i, expected)
			continue
		}
		if chunks[i] != expected {
			t.Errorf("chunk %d: expected 0x%08X (%q), got 0x%08X (%q)",
				i, expected, fourccStr(expected), chunks[i], fourccStr(chunks[i]))
		}
	}

	// Verify idx1 has 6 entries.
	idx1Off := findFOURCC(t, data, fccidx1)
	if idx1Off < 0 {
		t.Fatal("idx1 not found")
	}
	idx1DataSize := readU32LE(t, data, idx1Off+4)
	expectedIdxSize := 6 * 16
	if int(idx1DataSize) != expectedIdxSize {
		t.Errorf("idx1 data size: expected %d, got %d", expectedIdxSize, idx1DataSize)
	}

	// Verify index entries reference correct chunks.
	idx1Data := data[idx1Off+8:]
	for i := 0; i < 6; i++ {
		entryOff := i * 16
		ckID := readU32LE(t, idx1Data, entryOff)
		entryOffset := readU32LE(t, idx1Data, entryOff+8)

		if i%2 == 0 && ckID != fcc00dc {
			t.Errorf("idx1 entry %d: expected 00dc (0x%08X), got 0x%08X", i, fcc00dc, ckID)
		}
		if i%2 == 1 && ckID != fcc01wb {
			t.Errorf("idx1 entry %d: expected 01wb (0x%08X), got 0x%08X", i, fcc01wb, ckID)
		}

		if entryOffset > uint32(len(data)) {
			t.Errorf("idx1 entry %d: offset %d out of range", i, entryOffset)
		}
	}
}

// TestAVIMuxer_PadByte verifies odd-sized chunk gets pad byte.
func TestAVIMuxer_PadByte(t *testing.T) {
	var buf bytes.Buffer
	m := NewMuxer(&buf, 640, 480, 8000, true)

	oddFrame := makeJPEGFrame(t, 0xAB, 101) // odd size
	if err := m.WriteVideo(oddFrame, 0); err != nil {
		t.Fatalf("WriteVideo: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data := buf.Bytes()
	dcOff := findFOURCC(t, data, fcc00dc)
	if dcOff < 0 {
		t.Fatal("00dc not found")
	}

	chunkSize := readU32LE(t, data, dcOff+4)
	// After 8-byte header + chunkSize + pad byte = next chunk.
	padOffset := dcOff + 8 + int(chunkSize)
	if padOffset >= len(data) {
		t.Fatal("pad offset out of range")
	}
	if data[padOffset] != 0 {
		t.Errorf("expected pad byte 0 at offset %d, got 0x%02X", padOffset, data[padOffset])
	}

	// After padding, should be idx1.
	expectedAfterPad := padOffset + 1
	if binary.LittleEndian.Uint32(data[expectedAfterPad:]) != fccidx1 {
		t.Errorf("expected idx1 after padding at offset %d, got 0x%08X",
			expectedAfterPad, readU32LE(t, data, expectedAfterPad))
	}
}

// TestAVIMuxer_MulawFormat256 tests even-sized audio chunk (no pad).
func TestAVIMuxer_MulawFormat256(t *testing.T) {
	var buf bytes.Buffer
	m := NewMuxer(&buf, 320, 240, 8000, true)

	audio := makeG711Audio(t, 0xDD, 256) // even size
	if err := m.WriteAudio(audio, 0); err != nil {
		t.Fatalf("WriteAudio: %v", err)
	}
	if err := m.WriteVideo(makeJPEGFrame(t, 0xEE, 60), 0); err != nil {
		t.Fatalf("WriteVideo: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data := buf.Bytes()
	wbOff := findFOURCC(t, data, fcc01wb)
	if wbOff < 0 {
		t.Fatal("01wb not found")
	}

	chunkSize := readU32LE(t, data, wbOff+4)
	if chunkSize != 256 {
		t.Errorf("audio chunk size: expected 256, got %d", chunkSize)
	}

	// Even size: no pad byte, next chunk right after.
	nextOff := wbOff + 8 + int(chunkSize)
	if nextOff >= len(data) {
		t.Fatal("next chunk offset out of range")
	}
	if binary.LittleEndian.Uint32(data[nextOff:]) != fcc00dc {
		t.Fatalf("expected 00dc after audio, got 0x%08X", readU32LE(t, data, nextOff))
	}
}

// TestAVIMuxer_ErrorAfterClose verifies writing after Close returns error.
func TestAVIMuxer_ErrorAfterClose(t *testing.T) {
	var buf bytes.Buffer
	m := NewMuxer(&buf, 640, 480, 8000, true)
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err := m.WriteVideo(makeJPEGFrame(t, 0xFF, 100), 0)
	if err == nil {
		t.Error("expected error writing after Close")
	}
	if !strings.Contains(err.Error(), "closed") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestAVIMuxer_MultipleClose verifies Close can be called multiple times.
func TestAVIMuxer_MultipleClose(t *testing.T) {
	var buf bytes.Buffer
	m := NewMuxer(&buf, 640, 480, 8000, true)
	if err := m.WriteVideo(makeJPEGFrame(t, 0xAB, 100), 0); err != nil {
		t.Fatalf("WriteVideo: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := m.Close(); err == nil {
		t.Error("expected error on second Close")
	}
}

// TestAVIMuxer_DifferentResolutions tests various video dimensions.
func TestAVIMuxer_DifferentResolutions(t *testing.T) {
	tests := []struct {
		width  int
		height int
	}{
		{320, 240},
		{640, 480},
		{1280, 720},
		{1920, 1080},
		{2592, 1944},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%dx%d", tt.width, tt.height), func(t *testing.T) {
			var buf bytes.Buffer
			m := NewMuxer(&buf, tt.width, tt.height, 8000, true)
			if err := m.WriteVideo(makeJPEGFrame(t, 0xAA, 200), 0); err != nil {
				t.Fatalf("WriteVideo: %v", err)
			}
			if err := m.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			data := buf.Bytes()
			strfOff := findFOURCC(t, data, fccstrf)
			if strfOff < 0 {
				t.Fatal("strf not found")
			}
			// biWidth at strfOff+8+4, biHeight at strfOff+8+8
			biWidth := readU32LE(t, data, strfOff+8+4)
			biHeight := readU32LE(t, data, strfOff+8+8)
			if int(biWidth) != tt.width {
				t.Errorf("biWidth: expected %d, got %d", tt.width, biWidth)
			}
			if int(biHeight) != tt.height {
				t.Errorf("biHeight: expected %d, got %d", tt.height, biHeight)
			}
		})
	}
}

// TestAVIMuxer_Idx1Offsets verifies idx1 offset values are relative to movi data.
func TestAVIMuxer_Idx1Offsets(t *testing.T) {
	var buf bytes.Buffer
	m := NewMuxer(&buf, 640, 480, 8000, true)

	if err := m.WriteVideo(makeJPEGFrame(t, 0x01, 100), 0); err != nil {
		t.Fatalf("WriteVideo 0: %v", err)
	}
	if err := m.WriteAudio(makeG711Audio(t, 0x02, 80), 0); err != nil {
		t.Fatalf("WriteAudio 0: %v", err)
	}
	if err := m.WriteVideo(makeJPEGFrame(t, 0x03, 200), 33333); err != nil {
		t.Fatalf("WriteVideo 1: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data := buf.Bytes()

	// Find movi data start.
	moviOff := findFOURCC(t, data, fccmovi)
	if moviOff < 0 {
		t.Fatal("movi not found")
	}
	// movi data starts at moviOff+4 (we already have 'movi' in the find)...
	// Actually findFOURCC finds 'movi' at offset. movi data = offset + 0.
	// But the movi LIST is: 'LIST'(4) + size(4) + 'movi'(4) + data.
	// So movi data starts at moviOff - 4 + 4 = moviOff. Wait, let me reconsider.
	// findFOURCC finds the uint32 fccmovi. That's at some offset.
	// The structure is: 'LIST'(4) + size(4) + 'movi'(4) + data_chunks.
	// fccmovi is at offset (moviStart - 4) in the buffer.
	// Actually: LIST is at some offset X. X+4 is size. X+8 is 'movi'.
	// findFOURCC finds the 'movi' at X+8. So moviDataStart = X+12.
	moviDataStart := moviOff + 4

	idx1Off := findFOURCC(t, data, fccidx1)
	if idx1Off < 0 {
		t.Fatal("idx1 not found")
	}

	idx1Data := data[idx1Off+8:]

	for i := 0; i < 3; i++ {
		entryOff := i * 16
		ckID := readU32LE(t, idx1Data, entryOff)
		entryOffset := readU32LE(t, idx1Data, entryOff+8)
		entryLength := readU32LE(t, idx1Data, entryOff+12)

		chunkStart := moviDataStart + int(entryOffset)
		if chunkStart+8+int(entryLength) > len(data) {
			t.Errorf("entry %d: offset %d + size %d exceeds file", i, entryOffset, entryLength)
			continue
		}

		actualCkID := binary.LittleEndian.Uint32(data[chunkStart:])
		if actualCkID != ckID {
			t.Errorf("entry %d: idx1 ckID 0x%08X != chunk 0x%08X", i, ckID, actualCkID)
		}

		actualSize := readU32LE(t, data, chunkStart+4)
		if actualSize != entryLength {
			t.Errorf("entry %d: idx1 length %d != chunk size %d", i, entryLength, actualSize)
		}
	}
}
