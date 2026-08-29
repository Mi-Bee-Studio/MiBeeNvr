package api

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/avi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/go-chi/chi/v5"
)

// sortedImageFiles returns the sorted list of image filenames in dir, using a short-TTL
// cache to avoid os.ReadDir + sort on every request. The cache is invalidated when the
// directory's mtime changes (new frames written) or after frameListCacheTTL. This matters
// for MJPEG/timelapse frame dirs which can hold thousands of JPEGs; without it each
// ?frame=N / list-frames request re-scanned and re-sorted the whole directory.
func (h *Handler) sortedImageFiles(dir string) ([]string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	mtime := info.ModTime().Unix()

	h.frameListMu.Lock()
	if h.frameListCache == nil {
		h.frameListCache = make(map[string]*frameListEntry)
	}
	cached, ok := h.frameListCache[dir]
	if ok && cached.dirMtime == mtime && time.Since(cached.scannedAt) < frameListCacheTTL {
		names := cached.names
		h.frameListMu.Unlock()
		return names, nil
	}
	h.frameListMu.Unlock()

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if isImageFile(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })

	h.frameListMu.Lock()
	h.frameListCache[dir] = &frameListEntry{names: names, dirMtime: mtime, scannedAt: time.Now()}
	h.frameListMu.Unlock()
	return names, nil
}

func (h *Handler) handleDownloadRecording(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rec, err := h.db.GetRecording(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		WriteError(w, http.StatusNotFound, "recording not found")
		return
	}

	// Timelapse recordings are JPEG sequences — cannot be downloaded as a single file.
	if rec.Format == model.Format("timelapse") {
		WriteError(w, http.StatusBadRequest, fmt.Sprintf("Timelapse recordings (JPEG sequences) cannot be downloaded as a single file. Use /api/recordings/%s/timelapse-frames to access individual frames.", id))
		return
	}
	if rec.FilePath == "" {
		WriteError(w, http.StatusNotFound, "file not available")
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
			jpgFiles, err := h.sortedImageFiles(validPath)
			if err == nil {
				if frameIndex >= 0 && frameIndex < len(jpgFiles) {
					framePath := filepath.Join(validPath, jpgFiles[frameIndex])
					http.ServeFile(w, r, framePath)
					return
				}
			}
		}
		WriteError(w, http.StatusNotFound, "frame not found")
		return
	}

	filePath := validPath
	info, err := os.Stat(filePath)
	if err != nil {
		WriteError(w, http.StatusNotFound, "file not found")
		return
	}
	if info.IsDir() {
		entries, err := os.ReadDir(filePath)
		if err != nil || len(entries) == 0 {
			WriteError(w, http.StatusNotFound, "no files in recording directory")
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
		WriteError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		WriteError(w, http.StatusNotFound, "recording not found")
		return
	}

	if rec.Format != "mjpeg" {
		WriteError(w, http.StatusBadRequest, "not a JPEG recording")
		return
	}

	filePath := rec.FilePath
	info, err := os.Stat(filePath)
	if err != nil {
		WriteError(w, http.StatusNotFound, "recording files not found")
		return
	}
	if !info.IsDir() {
		WriteError(w, http.StatusNotFound, "recording is not a directory")
		return
	}

	entries, err := os.ReadDir(filePath)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to read recording directory")
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

// isTimelapseFrame checks if a filename has a supported timelapse frame extension.
func isTimelapseFrame(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") ||
		strings.HasSuffix(lower, ".h264") || strings.HasSuffix(lower, ".h265")
}

// handleTimelapseFrames handles GET /api/recordings/{id}/timelapse-frames.
// Returns JSON array of frame metadata for timelapse recordings.

// handleTimelapseFrames handles GET /api/recordings/{id}/timelapse-frames.
// Returns JSON array of frame metadata for timelapse recordings.
func (h *Handler) handleTimelapseFrames(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rec, err := h.db.GetRecording(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		WriteError(w, http.StatusNotFound, "recording not found")
		return
	}
	if rec.Format == model.FormatAVI {
		h.handleAviFrames(w, r, rec)
		return
	}
	if rec.Format != model.FormatTimelapse && rec.Format != model.FormatMJPEG {
		WriteError(w, http.StatusNotFound, "not a timelapse or MJPEG recording")
		return
	}

	info, err := os.Stat(rec.FilePath)
	if err != nil {
		WriteError(w, http.StatusNotFound, "timelapse directory not found")
		return
	}
	if !info.IsDir() {
		WriteError(w, http.StatusNotFound, "timelapse recording is not a directory")
		return
	}

	entries, err := os.ReadDir(rec.FilePath)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to read timelapse directory")
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

// handleAviFrames serves the timelapse-frames listing contract for AVI
// recordings (#321): video frames (complete JPEGs) indexed by walking the
// movi chunk headers. The frontend JPEG cycler — which already has seamless
// cross-segment chaining and seeking — consumes this exactly like timelapse
// frames, giving AVI recordings seekable playback instead of the realtime-only
// WebSocket player.

// handleAviFrames serves the timelapse-frames listing contract for AVI
// recordings (#321): video frames (complete JPEGs) indexed by walking the
// movi chunk headers. The frontend JPEG cycler — which already has seamless
// cross-segment chaining and seeking — consumes this exactly like timelapse
// frames, giving AVI recordings seekable playback instead of the realtime-only
// WebSocket player.
func (h *Handler) handleAviFrames(w http.ResponseWriter, r *http.Request, rec *model.Recording) {
	f, err := os.Open(rec.FilePath)
	if err != nil {
		WriteError(w, http.StatusNotFound, "AVI file not found")
		return
	}
	defer f.Close()

	dmx, err := avi.NewDemuxer(f)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "parse AVI: "+err.Error())
		return
	}
	entries, err := dmx.VideoFrameIndex()
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "index AVI frames: "+err.Error())
		return
	}

	type TimelapseFrameInfo struct {
		Filename  string `json:"filename"`
		URL       string `json:"url"`
		Size      int64  `json:"size"`
		Timestamp string `json:"timestamp"`
	}
	started := rec.StartedAt
	frames := make([]TimelapseFrameInfo, 0, len(entries))
	for _, e := range entries {
		name := fmt.Sprintf("f%06d.jpg", e.Index)
		frames = append(frames, TimelapseFrameInfo{
			Filename:  name,
			URL:       fmt.Sprintf("/api/recordings/%s/timelapse-frames/%s", rec.ID, name),
			Size:      int64(e.Size),
			Timestamp: started.Add(time.Duration(e.PTSUs) * time.Microsecond).UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, frames)
}

// handleAviFrame serves one JPEG frame out of an AVI recording by synthetic
// filename (f%06d.jpg — see handleAviFrames).

// handleAviFrame serves one JPEG frame out of an AVI recording by synthetic
// filename (f%06d.jpg — see handleAviFrames).
func (h *Handler) handleAviFrame(w http.ResponseWriter, r *http.Request, rec *model.Recording, filename string) {
	var idx int
	if _, err := fmt.Sscanf(filename, "f%d.jpg", &idx); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid AVI frame filename")
		return
	}
	f, err := os.Open(rec.FilePath)
	if err != nil {
		WriteError(w, http.StatusNotFound, "AVI file not found")
		return
	}
	defer f.Close()
	dmx, err := avi.NewDemuxer(f)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "parse AVI: "+err.Error())
		return
	}
	entries, err := dmx.VideoFrameIndex()
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "index AVI frames: "+err.Error())
		return
	}
	if idx < 0 || idx >= len(entries) {
		WriteError(w, http.StatusNotFound, "AVI frame out of range")
		return
	}
	e := entries[idx]
	if _, err := f.Seek(e.Offset, io.SeekStart); err != nil {
		WriteError(w, http.StatusInternalServerError, "seek AVI frame: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.FormatInt(int64(e.Size), 10))
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	if _, err := io.CopyN(w, f, int64(e.Size)); err != nil {
		return // client went away
	}
}

// handleTimelapseFrame handles GET /api/recordings/{id}/timelapse-frames/{filename}.
// Serves an individual JPEG frame from a timelapse recording.

// handleTimelapseFrame handles GET /api/recordings/{id}/timelapse-frames/{filename}.
// Serves an individual JPEG frame from a timelapse recording.
func (h *Handler) handleTimelapseFrame(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	filename := chi.URLParam(r, "filename")

	// Validate filename — only allow alphanumeric, underscore, dash, dot
	for _, c := range filename {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.') {
			WriteError(w, http.StatusBadRequest, "invalid filename")
			return
		}
	}
	if filename == "" || filename == "." || filename == ".." {
		WriteError(w, http.StatusBadRequest, "invalid filename")
		return
	}

	rec, err := h.db.GetRecording(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		WriteError(w, http.StatusNotFound, "recording not found")
		return
	}
	if rec.Format == model.FormatAVI {
		h.handleAviFrame(w, r, rec, filename)
		return
	}
	if rec.Format != model.FormatTimelapse && rec.Format != model.FormatMJPEG {
		WriteError(w, http.StatusNotFound, "not a timelapse or MJPEG recording")
		return
	}

	filePath := filepath.Join(rec.FilePath, filename)
	http.ServeFile(w, r, filePath)
}

// handleMergedRecording handles GET /api/recordings/{id}/merged.
// Serves the merged MP4 file for a timelapse recording if it has been merged.
// Returns 404 if the merged MP4 is not available — the frontend falls back to
// the JPEG frame viewer on this 404 (via MEDIA_ERR_NETWORK in handleVideoError).
//
// Sets the X-Timelapse-Codec response header (h264/h265/mjpeg) so the frontend
// can decide whether to use <video> (H.264/H.265 are browser-playable) or fall
// back to the JPEG frame cycler (MJPEG-in-MP4 / mjpa is not). Probed via the
// pure-Go mediaprobe package — no ffprobe.

// handleMergedRecording handles GET /api/recordings/{id}/merged.
// Serves the merged MP4 file for a timelapse recording if it has been merged.
// Returns 404 if the merged MP4 is not available — the frontend falls back to
// the JPEG frame viewer on this 404 (via MEDIA_ERR_NETWORK in handleVideoError).
//
// Sets the X-Timelapse-Codec response header (h264/h265/mjpeg) so the frontend
// can decide whether to use <video> (H.264/H.265 are browser-playable) or fall
// back to the JPEG frame cycler (MJPEG-in-MP4 / mjpa is not). Probed via the
// pure-Go mediaprobe package — no ffprobe.
func (h *Handler) handleMergedRecording(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rec, err := h.db.GetRecording(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get recording")
		return
	}
	if rec == nil {
		WriteError(w, http.StatusNotFound, "recording not found")
		return
	}
	if rec.MergePath == "" {
		WriteError(w, http.StatusNotFound, "merged recording not available")
		return
	}
	// Verify the merged MP4 actually exists on disk
	if _, err := os.Stat(rec.MergePath); err != nil {
		logger.Warn("merged recording file missing on disk",
			"recording_id", id, "merge_path", rec.MergePath, "error", err)
		WriteError(w, http.StatusNotFound, "merged recording file not available")
		return
	}
	// Surface the codec so the frontend player can pick the right path.
	// HEAD requests also get the header (caller sets it before ServeFile).
	if codec := probeTimelapseCodecCached(rec.MergePath); codec != "" {
		w.Header().Set("X-Timelapse-Codec", codec)
	}
	http.ServeFile(w, r, rec.MergePath)
}

// timelapseCodecCache caches probe results by file path. Merge output files are
// immutable (never modified after creation), so the codec never changes — caching
// avoids re-parsing the MP4 box header on every playback request.
