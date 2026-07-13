package recorder

import (
	"encoding/binary"
	"fmt"
	"image/jpeg"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
)

// DetectDarkMJPEGDir checks if an MJPEG segment directory contains only dark frames.
// It samples up to 3 JPEG files (first, middle, last by sorted filename) and computes
// the average luminance of each. If all sampled frames are below the threshold,
// the segment is classified as "dark".
//
// Returns (isDark, avgBrightness, error).
func DetectDarkMJPEGDir(dirPath string, threshold int) (bool, int, error) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return false, 0, fmt.Errorf("read dir: %w", err)
	}

	var jpgFiles []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			continue
		}
		ext := filepath.Ext(name)
		if ext == ".jpg" || ext == ".jpeg" {
			jpgFiles = append(jpgFiles, filepath.Join(dirPath, name))
		}
	}
	if len(jpgFiles) == 0 {
		return false, 0, fmt.Errorf("no JPEG files in directory")
	}

	sort.Strings(jpgFiles)

	// Sample 3 frames: first, middle, last.
	sampleIdx := []int{0}
	if len(jpgFiles) > 2 {
		sampleIdx = append(sampleIdx, len(jpgFiles)/2)
	}
	if len(jpgFiles) > 1 {
		sampleIdx = append(sampleIdx, len(jpgFiles)-1)
	}

	totalBrightness := 0
	validSamples := 0
	for _, idx := range sampleIdx {
		brightness, err := jpegBrightness(jpgFiles[idx])
		if err != nil {
			continue // skip unreadable frames
		}
		totalBrightness += brightness
		validSamples++
	}
	if validSamples == 0 {
		return false, 0, fmt.Errorf("failed to decode any sampled frames")
	}

	avgBrightness := totalBrightness / validSamples
	isDark := avgBrightness < threshold
	return isDark, avgBrightness, nil
}

// DetectDarkAVIFile checks if an AVI file contains only dark MJPEG frames.
// It uses raw AVI chunk reading to extract up to 3 video chunks (first, middle, last)
// without full demuxing. Each chunk is a JPEG frame that can be decoded.
//
// Returns (isDark, avgBrightness, error).
func DetectDarkAVIFile(filePath string, threshold int) (bool, int, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return false, 0, fmt.Errorf("open: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return false, 0, fmt.Errorf("stat: %w", err)
	}
	fileSize := fi.Size()

	// Collect video chunk offsets by scanning the movi LIST.
	// AVI structure: RIFF header (12 bytes) → hdrl LIST → movi LIST → idx1.
	// We do a lightweight scan: find "movi" LIST, then scan for "00dc" chunks.
	moviOffset, moviSize, err := findMoviList(f, fileSize)
	if err != nil {
		return false, 0, fmt.Errorf("find movi: %w", err)
	}

	// Scan video chunks within movi.
	type chunkInfo struct {
		offset int64
		size   int32
	}
	var chunks []chunkInfo
	pos := moviOffset + 8 // skip "movi" + size
	moviEnd := moviOffset + int64(moviSize)
	for pos < moviEnd-8 {
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			break
		}
		var fcc [4]byte
		var size int32
		if _, err := f.Read(fcc[:]); err != nil {
			break
		}
		if err := binary.Read(f, binary.LittleEndian, &size); err != nil {
			break
		}
		if size <= 0 {
			break
		}
		fccStr := string(fcc[:])
		dataOffset := pos + 8
		if fccStr == "00dc" || fccStr == "00db" {
			chunks = append(chunks, chunkInfo{offset: dataOffset, size: size})
		}
		// Move to next chunk (padded to even byte).
		nextPos := dataOffset + int64(size)
		if size%2 != 0 {
			nextPos++
		}
		pos = nextPos
	}
	if len(chunks) == 0 {
		return false, 0, fmt.Errorf("no video chunks found in AVI")
	}

	// Sample 3 chunks.
	sampleIdx := []int{0}
	if len(chunks) > 2 {
		sampleIdx = append(sampleIdx, len(chunks)/2)
	}
	if len(chunks) > 1 {
		sampleIdx = append(sampleIdx, len(chunks)-1)
	}

	totalBrightness := 0
	validSamples := 0
	for _, idx := range sampleIdx {
		c := chunks[idx]
		if _, err := f.Seek(c.offset, io.SeekStart); err != nil {
			continue
		}
		// Limit read to chunk size (avoids reading past chunk boundary).
		limitReader := io.LimitReader(f, int64(c.size))
		brightness, err := jpegBrightnessFromReader(limitReader)
		if err != nil {
			continue
		}
		totalBrightness += brightness
		validSamples++
	}
	if validSamples == 0 {
		return false, 0, fmt.Errorf("failed to decode any sampled frames")
	}

	avgBrightness := totalBrightness / validSamples
	isDark := avgBrightness < threshold
	return isDark, avgBrightness, nil
}

// findMoviList scans the AVI file for the "movi" LIST header.
// Returns the offset of the LIST header (before the LIST fcc) and its size.
func findMoviList(f *os.File, fileSize int64) (int64, int32, error) {
	// Scan in chunks for "LIST....movi"
	buf := make([]byte, 8192)
	pos := int64(0)
	for pos < fileSize-12 {
		_, err := f.Seek(pos, io.SeekStart)
		if err != nil {
			return 0, 0, err
		}
		n, err := f.Read(buf)
		if err != nil || n < 12 {
			break
		}
		for i := range n - 12 {
			if buf[i] == 'L' && buf[i+1] == 'I' && buf[i+2] == 'S' && buf[i+3] == 'T' {
				size := int32(binary.LittleEndian.Uint32(buf[i+4 : i+8]))
				if buf[i+8] == 'm' && buf[i+9] == 'o' && buf[i+10] == 'v' && buf[i+11] == 'i' {
					return pos + int64(i), size, nil
				}
			}
		}
		pos += int64(n - 12)
	}
	return 0, 0, fmt.Errorf("movi LIST not found")
}

// jpegBrightness decodes a JPEG file and returns the average luminance (0-255).
// Uses a downsampled approach: decodes at 1/8 scale, reads pixel values,
// converts RGB to luminance via Y = 0.299R + 0.587G + 0.114B.
func jpegBrightness(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return jpegBrightnessFromReader(f)
}

// jpegBrightnessFromReader decodes JPEG data from a reader and returns avg luminance.
func jpegBrightnessFromReader(r io.Reader) (int, error) {
	img, err := jpeg.Decode(r)
	if err != nil {
		return 0, fmt.Errorf("jpeg decode: %w", err)
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w == 0 || h == 0 {
		return 0, fmt.Errorf("empty image")
	}

	// Downsample: read at most 32x32 pixels spread across the image.
	sampleW := int(math.Min(32, float64(w)))
	sampleH := int(math.Min(32, float64(h)))
	stepX := w / sampleW
	stepY := h / sampleH
	if stepX < 1 {
		stepX = 1
	}
	if stepY < 1 {
		stepY = 1
	}

	totalLum := 0
	count := 0
	for y := bounds.Min.Y; y < bounds.Max.Y; y += stepY {
		for x := bounds.Min.X; x < bounds.Max.X; x += stepX {
			r, g, b, _ := img.At(x, y).RGBA()
			// RGBA() returns 16-bit values; shift to 8-bit.
			lum := (299*int(r>>8) + 587*int(g>>8) + 114*int(b>>8)) / 1000
			totalLum += lum
			count++
		}
	}
	if count == 0 {
		return 0, fmt.Errorf("no pixels sampled")
	}
	return totalLum / count, nil
}
