package api

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/mediaprobe"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// timelapseCodecCache caches probe results by file path. Merge output files are
// immutable (never modified after creation), so the codec never changes — caching
// avoids re-parsing the MP4 box header on every playback request.
var timelapseCodecCache sync.Map // path → string (codec)

// probeTimelapseCodecCached returns the codec for a timelapse merge MP4, using
// an in-memory cache keyed by file path. Since merge outputs are write-once,
// the cache is permanent per path (no invalidation needed).

// probeTimelapseCodecCached returns the codec for a timelapse merge MP4, using
// an in-memory cache keyed by file path. Since merge outputs are write-once,
// the cache is permanent per path (no invalidation needed).
func probeTimelapseCodecCached(path string) string {
	if v, ok := timelapseCodecCache.Load(path); ok {
		return v.(string)
	}
	codec := probeTimelapseCodec(path)
	timelapseCodecCache.Store(path, codec)
	return codec
}

// probeTimelapseCodec returns "h264" / "h265" / "mjpeg" for the MP4 at the
// given path, or "" if the codec could not be determined. Used to populate the
// X-Timelapse-Codec response header that the frontend player consults.

// probeTimelapseCodec returns "h264" / "h265" / "mjpeg" for the MP4 at the
// given path, or "" if the codec could not be determined. Used to populate the
// X-Timelapse-Codec response header that the frontend player consults.
func probeTimelapseCodec(path string) string {
	info, err := mediaprobe.ProbeMP4(path)
	if err != nil {
		return ""
	}
	switch info.Codec {
	case model.TimelapseMergeCodecH264, model.TimelapseMergeCodecH265:
		return info.Codec
	default:
		// mediaprobe returns raw codec string for mjpa; normalize to "mjpeg".
		return model.TimelapseMergeCodecMJPEG
	}
}

// extractFrameTimestamp extracts the capture timestamp from a frame file.
// Priority order:
// 1. Filename pattern: frame_YYYYMMDD_HHMMSS.jpg
// 2. JPEG EXIF DateTimeOriginal (or DateTime as fallback)
// 3. File ModTime (fallback)

// extractFrameTimestamp extracts the capture timestamp from a frame file.
// Priority order:
// 1. Filename pattern: frame_YYYYMMDD_HHMMSS.jpg
// 2. JPEG EXIF DateTimeOriginal (or DateTime as fallback)
// 3. File ModTime (fallback)
func extractFrameTimestamp(name, filePath string, modTime time.Time) time.Time {
	// Try filename pattern first (no file I/O needed)
	if t, ok := parseFrameFilename(name); ok {
		return t
	}

	// Fall back to EXIF DateTimeOriginal
	if t, ok := extractEXIFDateTime(filePath); ok {
		return t
	}

	// Last resort: file ModTime
	return modTime
}

// parseFrameFilename attempts to parse a timestamp from a frame filename.
// Supported formats:
//   - frame_20240101_120000.jpg
//   - frame_20240101_120000_001.jpg
//   - frame_000001.jpg (no timestamp — returns false)

// parseFrameFilename attempts to parse a timestamp from a frame filename.
// Supported formats:
//   - frame_20240101_120000.jpg
//   - frame_20240101_120000_001.jpg
//   - frame_000001.jpg (no timestamp — returns false)
func parseFrameFilename(name string) (time.Time, bool) {
	// Must start with "frame_" prefix
	if !strings.HasPrefix(name, "frame_") {
		return time.Time{}, false
	}

	// Remove prefix
	rest := name[6:]

	// Expected pattern: 8 digits (date) + "_" + 6 digits (time) [+ optional suffix]
	if len(rest) < 15 {
		return time.Time{}, false
	}
	if rest[8] != '_' {
		return time.Time{}, false
	}

	dateStr := rest[:8]
	timeStr := rest[9:15]

	// Validate both are digits
	for _, c := range dateStr {
		if c < '0' || c > '9' {
			return time.Time{}, false
		}
	}
	for _, c := range timeStr {
		if c < '0' || c > '9' {
			return time.Time{}, false
		}
	}

	// Parse as YYYYMMDD_HHMMSS
	t, err := time.Parse("20060102_150405", dateStr+"_"+timeStr)
	if err != nil {
		return time.Time{}, false
	}

	return t, true
}

// extractEXIFDateTime extracts DateTimeOriginal (or DateTime) from a JPEG file's EXIF data.
// Returns the parsed time and true if successful.

// extractEXIFDateTime extracts DateTimeOriginal (or DateTime) from a JPEG file's EXIF data.
// Returns the parsed time and true if successful.
func extractEXIFDateTime(filePath string) (time.Time, bool) {
	f, err := os.Open(filePath)
	if err != nil {
		return time.Time{}, false
	}
	defer f.Close()

	// Read enough for JPEG SOI + APP1 marker + EXIF header + TIFF structure
	// EXIF data is typically in the first few KB; read 64KB to be safe
	buf := make([]byte, 65536)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return time.Time{}, false
	}
	if n < 4 {
		return time.Time{}, false
	}
	data := buf[:n]

	// Check JPEG SOI marker (0xFF 0xD8)
	if data[0] != 0xFF || data[1] != 0xD8 {
		return time.Time{}, false
	}

	// Find APP1 segment (0xFF 0xE1)
	i := 2
	found := false
	for i < n-1 {
		if data[i] != 0xFF {
			return time.Time{}, false
		}
		marker := data[i+1]
		if i+3 >= n {
			return time.Time{}, false
		}
		segLen := int(binary.BigEndian.Uint16(data[i+2:i+4])) + 2
		if segLen < 2 || i+segLen > n {
			return time.Time{}, false
		}

		if marker == 0xE1 && segLen >= 10 {
			// Check for "Exif\0\0" header (6 bytes after the length field)
			if string(data[i+4:i+10]) == "Exif\x00\x00" {
				found = true
				// Parse EXIF starting after the "Exif\0\0" header (i+10)
				exifData := data[i+10 : i+segLen]
				if t, ok := parseEXIFTIFF(exifData); ok {
					return t, true
				}
			}
		}
		i += segLen
	}

	if !found {
		return time.Time{}, false
	}

	return time.Time{}, false
}

// parseEXIFTIFF parses a TIFF structure within EXIF data to find DateTimeOriginal or DateTime.

// parseEXIFTIFF parses a TIFF structure within EXIF data to find DateTimeOriginal or DateTime.
func parseEXIFTIFF(data []byte) (time.Time, bool) {
	if len(data) < 8 {
		return time.Time{}, false
	}

	// Determine byte order
	var bo binary.ByteOrder
	switch string(data[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return time.Time{}, false
	}

	// Verify TIFF magic (0x002A)
	if bo.Uint16(data[2:4]) != 0x002A {
		return time.Time{}, false
	}

	// Offset to IFD0 (from start of TIFF header)
	ifdOffset := int(bo.Uint32(data[4:8]))
	if ifdOffset < 8 || ifdOffset+2 > len(data) {
		return time.Time{}, false
	}

	// Number of IFD0 entries
	numEntries := int(bo.Uint16(data[ifdOffset : ifdOffset+2]))
	ifdEnd := ifdOffset + 2 + numEntries*12
	if ifdEnd > len(data) {
		return time.Time{}, false
	}

	var dateTimeStr string
	var exifIFDOffset int

	// Scan IFD0 entries
	for j := range numEntries {
		entryOff := ifdOffset + 2 + j*12
		if entryOff+12 > len(data) {
			break
		}
		tag := bo.Uint16(data[entryOff : entryOff+2])
		type_ := bo.Uint16(data[entryOff+2 : entryOff+4])
		_ = type_

		switch tag {
		case 0x0132: // DateTime in IFD0 (ASCII)
			// Value is an ASCII string stored in the value field (4 bytes) or as an offset
			if s := readEXIFString(data, entryOff, bo); s != "" {
				dateTimeStr = s
			}
		case 0x8769: // ExifIFD pointer
			if entryOff+12 > len(data) {
				break
			}
			exifIFDOffset = int(bo.Uint32(data[entryOff+8 : entryOff+12]))
			if exifIFDOffset < 0 || exifIFDOffset >= len(data) {
				exifIFDOffset = 0
			}
		}
	}

	// If we found DateTime in IFD0, use it (but prefer DateTimeOriginal from EXIF IFD)
	// Try EXIF IFD first for DateTimeOriginal
	if exifIFDOffset > 0 && exifIFDOffset+2 <= len(data) {
		numExifEntries := int(bo.Uint16(data[exifIFDOffset : exifIFDOffset+2]))
		for j := range numExifEntries {
			entryOff := exifIFDOffset + 2 + j*12
			if entryOff+12 > len(data) {
				break
			}
			tag := bo.Uint16(data[entryOff : entryOff+2])
			if tag == 0x9003 { // DateTimeOriginal
				if s := readEXIFString(data, entryOff, bo); s != "" {
					if t, err := time.Parse("2006:01:02 15:04:05", s); err == nil {
						return t, true
					}
				}
			}
		}
	}

	// Fall back to DateTime from IFD0
	if dateTimeStr != "" {
		if t, err := time.Parse("2006:01:02 15:04:05", dateTimeStr); err == nil {
			return t, true
		}
	}

	return time.Time{}, false
}

// readEXIFString reads an ASCII string value from an EXIF entry.
// For short strings (≤4 bytes), the value is stored inline in the 4-byte value field.
// For longer strings, the value field contains an offset to the string data.

// readEXIFString reads an ASCII string value from an EXIF entry.
// For short strings (≤4 bytes), the value is stored inline in the 4-byte value field.
// For longer strings, the value field contains an offset to the string data.
func readEXIFString(data []byte, entryOff int, bo binary.ByteOrder) string {
	if entryOff+12 > len(data) {
		return ""
	}

	type_ := bo.Uint16(data[entryOff+2 : entryOff+4])
	count := bo.Uint32(data[entryOff+4 : entryOff+8])

	// Type 2 = ASCII string
	if type_ != 2 {
		return ""
	}

	// Calculate total byte count (ASCII strings have 1 byte per character)
	totalBytes := int(count)
	if totalBytes <= 0 || totalBytes > 256 {
		return ""
	}

	var strBytes []byte
	if totalBytes <= 4 {
		// Inline value
		strBytes = data[entryOff+8 : entryOff+8+totalBytes]
	} else {
		// Offset to string data
		offset := int(bo.Uint32(data[entryOff+8 : entryOff+12]))
		if offset < 0 || offset+totalBytes > len(data) {
			return ""
		}
		strBytes = data[offset : offset+totalBytes]
	}

	// Strip null terminator(s)
	s := string(bytes.TrimRight(strBytes, "\x00"))
	return strings.TrimSpace(s)
}

// handleTimelineGaps returns recording gaps (time periods with no recording)
// for a camera on a specific date. Used by the frontend timeline to render
// "断帧" (frame drop) markers.
//
// Query params:
//
//	date=YYYY-MM-DD  — the day to analyze (required)
//	min_gap=30s      — minimum gap duration to report (default 30s)
