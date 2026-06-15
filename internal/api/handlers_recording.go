package api


import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/go-chi/chi/v5"
)

// --- Recording endpoints ---

func (h *Handler) handleListRecordings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filter := model.RecordingFilter{
		CameraID: r.URL.Query().Get("camera_id"),
		Format:   model.Format(r.URL.Query().Get("format")),
	}

	if v := r.URL.Query().Get("merged"); v != "" {
		merged := v == "true" || v == "1"
		filter.Merged = &merged
	}

	if v := r.URL.Query().Get("start"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.StartTime = t
		}
	}

	if v := r.URL.Query().Get("end"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.EndTime = t
		}
	}

	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			filter.Limit = n
		}
	}

	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			filter.Offset = n
		}
	}

	// Sorting
	filter.SortBy = r.URL.Query().Get("sort_by")
	filter.SortOrder = r.URL.Query().Get("order")

	filter.Search = r.URL.Query().Get("search")

	recordings, err := h.db.ListRecordings(ctx, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list recordings")
		return
	}

	if recordings == nil {
		recordings = []model.Recording{}
	}

	total, err := h.db.CountRecordingsWithFilter(ctx, filter)
	if err != nil {
		total = 0 // non-fatal
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"recordings": recordings,
		"total":      total,
	})
}

// handleTimelineSeekEvent records a timeline seek for observability (0.8.0 M6).
// Body: {"camera_id":"front-door","type":"segment"}
// type is "segment" (cross-recording) or "intra" (within same recording).
func (h *Handler) handleTimelineSeekEvent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CameraID string `json:"camera_id"`
		Type     string `json:"type"` // "segment" | "intra"
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	seekType := body.Type
	if seekType != "segment" && seekType != "intra" {
		seekType = "segment"
	}
	cameraID := body.CameraID
	if cameraID == "" {
		cameraID = "unknown"
	}
	if apiMetrics != nil {
		apiMetrics.TimelineSeeksTotal.WithLabelValues(cameraID, seekType).Inc()
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "recorded"})
}

func (h *Handler) handleGetRecording(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rec, err := h.db.GetRecording(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		writeError(w, http.StatusNotFound, "recording not found")
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (h *Handler) handleDeleteRecording(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()

	rec, err := h.db.GetRecording(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		writeError(w, http.StatusNotFound, "recording not found")
		return
	}

	// Delete from DB first (authoritative source)
	if err := h.db.DeleteRecording(ctx, id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete recording")
		return
	}

	// Then delete file (non-fatal if fails)
	if rec.FilePath != "" {
		if err := h.store.DeleteFile(rec.FilePath); err != nil {
			logger.Warn("failed to delete file", "file_path", rec.FilePath, "error", err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) handleBatchDeleteRecordings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var body struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(body.IDs) == 0 {
		writeError(w, http.StatusBadRequest, "ids must not be empty")
		return
	}
	if len(body.IDs) > 100 {
		writeError(w, http.StatusBadRequest, "ids must not exceed 100")
		return
	}
	// Fetch file paths before batch delete
	filePaths := map[string]string{}
	for _, id := range body.IDs {
		rec, err := h.db.GetRecording(ctx, id)
		if err == nil && rec != nil && rec.FilePath != "" {
			filePaths[id] = rec.FilePath
		}
	}

	// Delete DB records (transaction)
	deleted, err := h.db.DeleteRecordingsBatch(ctx, body.IDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete recordings")
		return
	}

	// Attempt file deletion for successfully deleted records (non-fatal)
	failed := []string{}
	deletedSet := make(map[string]bool, len(deleted))
	for _, id := range deleted {
		deletedSet[id] = true
		if fp, ok := filePaths[id]; ok {
			if err := h.store.DeleteFile(fp); err != nil {
				logger.Warn("batch delete: failed to delete file", "file_path", fp, "error", err)
			}
		}
	}
	for _, id := range body.IDs {
		if !deletedSet[id] {
			failed = append(failed, id)
		}
	}

	result := map[string]any{"deleted": deleted}
	if len(failed) > 0 {
		result["failed"] = failed
	} else {
		result["failed"] = []string{}
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) handleDownloadRecording(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rec, err := h.db.GetRecording(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		writeError(w, http.StatusNotFound, "recording not found")
		return
	}

	// Timelapse recordings are JPEG sequences — cannot be downloaded as a single file.
	if rec.Format == model.Format("timelapse") {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("Timelapse recordings (JPEG sequences) cannot be downloaded as a single file. Use /api/recordings/%s/timelapse-frames to access individual frames.", id))
		return
	}
	if rec.FilePath == "" {
		writeError(w, http.StatusNotFound, "file not available")
		return
	}

	// Validate that the recording file path is within the storage root to prevent
	// path traversal. This ensures rec.FilePath (which may come from external
	// sources like WebDAV uploads) is confined to the storage directory.
	validPath, err := storage.ValidatePath(h.store.RootDir(), rec.FilePath)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, &model.PathTraversalError{Path: rec.FilePath})
		return
	}

	// Check for frame parameter (MJPEG frame download)
	frameStr := r.URL.Query().Get("frame")
	if frameStr != "" && rec.Format == model.FormatMJPEG {
		frameIndex, err := strconv.Atoi(frameStr)
		if err == nil {
			entries, err := os.ReadDir(validPath)
			if err == nil {
				jpgFiles := []os.DirEntry{}
				for _, e := range entries {
					if !e.IsDir() && isImageFile(e.Name()) {
						jpgFiles = append(jpgFiles, e)
					}
				}
				sort.Slice(jpgFiles, func(i, j int) bool { return jpgFiles[i].Name() < jpgFiles[j].Name() })
				if frameIndex >= 0 && frameIndex < len(jpgFiles) {
					framePath := filepath.Join(validPath, jpgFiles[frameIndex].Name())
					http.ServeFile(w, r, framePath)
					return
				}
			}
		}
		http.Error(w, "frame not found", http.StatusNotFound)
		return
	}

	filePath := validPath
	info, err := os.Stat(filePath)
	if err != nil {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}
	if info.IsDir() {
		entries, err := os.ReadDir(filePath)
		if err != nil || len(entries) == 0 {
			writeError(w, http.StatusNotFound, "no files in recording directory")
			return
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasSuffix(name, ".jpg") || strings.HasSuffix(name, ".jpeg") || strings.HasSuffix(name, ".mp4") {
				filePath = filepath.Join(filePath, name)
				break
			}
		}
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=\"%s\"", filepath.Base(filePath)))
	http.ServeFile(w, r, filePath)
}

func (h *Handler) handleListFrames(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rec, err := h.db.GetRecording(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		writeError(w, http.StatusNotFound, "recording not found")
		return
	}

	if rec.Format != "mjpeg" {
		writeError(w, http.StatusBadRequest, "not a JPEG recording")
		return
	}

	filePath := rec.FilePath
	info, err := os.Stat(filePath)
	if err != nil {
		writeError(w, http.StatusNotFound, "recording files not found")
		return
	}
	if !info.IsDir() {
		writeError(w, http.StatusNotFound, "recording is not a directory")
		return
	}

	entries, err := os.ReadDir(filePath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read recording directory")
		return
	}

	type FrameInfo struct {
		Index    int    `json:"index"`
		Filename string `json:"filename"`
		Size     int64  `json:"size"`
	}

	var frames []FrameInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".jpg") && !strings.HasSuffix(strings.ToLower(name), ".jpeg") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		frames = append(frames, FrameInfo{
			Filename: name,
			Size:     fi.Size(),
		})
	}

	// Sort by filename (natural order - timestamp-based names sort correctly)
	sort.Slice(frames, func(i, j int) bool {
		return frames[i].Filename < frames[j].Filename
	})

	// Assign sequential indices
	for i := range frames {
		frames[i].Index = i
	}

	if frames == nil {
		frames = []FrameInfo{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"frames": frames,
	})
}

// --- Timelapse endpoints ---

// isTimelapseFrame checks if a filename has a supported timelapse frame extension.
func isTimelapseFrame(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") ||
		strings.HasSuffix(lower, ".h264") || strings.HasSuffix(lower, ".h265")
}

// handleTimelapseFrames handles GET /api/recordings/{id}/timelapse-frames.
// Returns JSON array of frame metadata for timelapse recordings.
func (h *Handler) handleTimelapseFrames(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rec, err := h.db.GetRecording(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		writeError(w, http.StatusNotFound, "recording not found")
		return
	}
	if rec.Format != model.Format("timelapse") {
		writeError(w, http.StatusNotFound, "not a timelapse recording")
		return
	}

	info, err := os.Stat(rec.FilePath)
	if err != nil {
		writeError(w, http.StatusNotFound, "timelapse directory not found")
		return
	}
	if !info.IsDir() {
		writeError(w, http.StatusNotFound, "timelapse recording is not a directory")
		return
	}

	entries, err := os.ReadDir(rec.FilePath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read timelapse directory")
		return
	}

	type TimelapseFrameInfo struct {
		Filename  string `json:"filename"`
		URL       string `json:"url"`
		Size      int64  `json:"size"`
		Timestamp string `json:"timestamp"`
	}

	var frames []TimelapseFrameInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !isTimelapseFrame(name) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		frames = append(frames, TimelapseFrameInfo{
			Filename:  name,
			URL:       fmt.Sprintf("/api/recordings/%s/timelapse-frames/%s", id, name),
			Size:      fi.Size(),
			Timestamp: extractFrameTimestamp(name, filepath.Join(rec.FilePath, name), fi.ModTime()).UTC().Format(time.RFC3339),
		})
	}

	sort.Slice(frames, func(i, j int) bool {
		return frames[i].Filename < frames[j].Filename
	})

	if frames == nil {
		frames = []TimelapseFrameInfo{}
	}

	writeJSON(w, http.StatusOK, frames)
}

// handleTimelapseFrame handles GET /api/recordings/{id}/timelapse-frames/{filename}.
// Serves an individual JPEG frame from a timelapse recording.
func (h *Handler) handleTimelapseFrame(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	filename := chi.URLParam(r, "filename")

	// Validate filename — only allow alphanumeric, underscore, dash, dot
	for _, c := range filename {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.') {
			writeError(w, http.StatusBadRequest, "invalid filename")
			return
		}
	}
	if filename == "" || filename == "." || filename == ".." {
		writeError(w, http.StatusBadRequest, "invalid filename")
		return
	}

	rec, err := h.db.GetRecording(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		writeError(w, http.StatusNotFound, "recording not found")
		return
	}
	if rec.Format != model.Format("timelapse") {
		writeError(w, http.StatusNotFound, "not a timelapse recording")
		return
	}

	filePath := filepath.Join(rec.FilePath, filename)
	http.ServeFile(w, r, filePath)
}

// handleMergedRecording handles GET /api/recordings/{id}/merged.
// Serves the merged MP4 file for a timelapse recording if it has been merged.
func (h *Handler) handleMergedRecording(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rec, err := h.db.GetRecording(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		writeError(w, http.StatusNotFound, "recording not found")
		return
	}
	if rec.MergePath == "" {
		writeError(w, http.StatusNotFound, "merged recording not available")
		return
	}
	http.ServeFile(w, r, rec.MergePath)
}

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
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
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
	for j := 0; j < numEntries; j++ {
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
		for j := 0; j < numExifEntries; j++ {
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
