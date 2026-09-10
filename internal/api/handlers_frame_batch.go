// Frame-batch endpoints: one multipart/mixed HTTP response carries a batch of
// JPEG frames, replacing the per-frame GET hot path of the JPEG cycler for
// MJPEG playback (merged timelapse outputs + raw recording dirs + AVI files).
// The frontend streams the response and decodes parts as Blobs — N frames per
// request instead of N requests.
package api

import (
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/avi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/go-chi/chi/v5"
)

const (
	frameBatchDefaultLimit = 120
	frameBatchMaxLimit     = 240
)

// parseFrameBatchParams parses ?offset=&limit= for the frame-batch endpoints.
// Defaults: offset 0, limit 120. limit is clamped to frameBatchMaxLimit;
// offset<0 / limit<=0 / non-numeric values are rejected.
func parseFrameBatchParams(r *http.Request) (offset, limit int, err error) {
	if s := r.URL.Query().Get("offset"); s != "" {
		v, perr := strconv.Atoi(s)
		if perr != nil || v < 0 {
			return 0, 0, fmt.Errorf("invalid offset %q", s)
		}
		offset = v
	}
	limit = frameBatchDefaultLimit
	if s := r.URL.Query().Get("limit"); s != "" {
		v, perr := strconv.Atoi(s)
		if perr != nil || v <= 0 {
			return 0, 0, fmt.Errorf("invalid limit %q", s)
		}
		if v > frameBatchMaxLimit {
			v = frameBatchMaxLimit
		}
		limit = v
	}
	return offset, limit, nil
}

// writeFrameBatch streams a multipart/mixed batch of frames [offset,
// offset+limit) as JPEG parts. count is clamped to the available range; an
// out-of-range offset yields an empty (but valid) multipart body so players
// stop cleanly. emit writes the i-th frame as one part.
func (h *Handler) writeFrameBatch(w http.ResponseWriter, total, offset, limit int, emit func(i int, mw *multipart.Writer) error) {
	count := limit
	if offset+count > total {
		count = total - offset
	}
	if count < 0 {
		count = 0
	}

	mw := multipart.NewWriter(w)
	w.Header().Set("Content-Type", "multipart/mixed; boundary="+mw.Boundary())
	w.Header().Set("X-Frame-Total", strconv.Itoa(total))
	w.Header().Set("X-Frame-Offset", strconv.Itoa(offset))
	w.Header().Set("X-Frame-Count", strconv.Itoa(count))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	if count == 0 {
		_ = mw.Close()
		return
	}
	for i := offset; i < offset+count; i++ {
		if err := emit(i, mw); err != nil {
			// Status already sent — abort the batch mid-stream; the player
			// sees a truncated part count and refetches the tail if needed.
			logger.Warn("frame batch: emit failed", "index", i, "error", err)
			break
		}
	}
	_ = mw.Close()
}

// writeJPEGPartHeader creates one image/jpeg part. Callers write the payload.
func writeJPEGPartHeader(mw *multipart.Writer, index int) (io.Writer, error) {
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Type", "image/jpeg")
	hdr.Set("X-Frame-Index", strconv.Itoa(index))
	return mw.CreatePart(hdr)
}

// handleTimelapseMergeFrames handles GET /api/timelapse/merges/{id}/frames.
// Slices JPEG frames out of an MJPEG (mjpa) periodic-merge output via the
// pure-Go MP4 sample table (merge.ParseSegment → stsz/stco), streaming one
// multipart part per frame. Only for codec=mjpeg merges — h264/h265 outputs
// play natively via <video>.
func (h *Handler) handleTimelapseMergeFrames(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		WriteError(w, http.StatusBadRequest, "invalid merge id")
		return
	}
	m, err := h.db.GetTimelapseMerge(r.Context(), id)
	if err != nil || m == nil {
		WriteError(w, http.StatusNotFound, "timelapse merge not found")
		return
	}
	if m.Status != model.TimelapseMergeStatusCompleted || m.OutputPath == "" {
		WriteError(w, http.StatusNotFound, "timelapse merge output not available")
		return
	}
	if m.Codec != "" && m.Codec != model.TimelapseMergeCodecMJPEG {
		WriteError(w, http.StatusNotFound, "frame batches only available for MJPEG merges")
		return
	}
	if _, err := os.Stat(m.OutputPath); err != nil {
		WriteError(w, http.StatusNotFound, "timelapse merge output file not available")
		return
	}
	offset, limit, err := parseFrameBatchParams(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	seg, err := merge.ParseSegment(m.OutputPath)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to index merge output frames")
		return
	}

	f, err := os.Open(m.OutputPath)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to open merge output")
		return
	}
	defer f.Close()

	if m.FPS > 0 {
		w.Header().Set("X-Frame-Fps", strconv.Itoa(m.FPS))
	}
	h.writeFrameBatch(w, len(seg.Samples), offset, limit, func(i int, mw *multipart.Writer) error {
		s := seg.Samples[i]
		part, err := writeJPEGPartHeader(mw, i)
		if err != nil {
			return err
		}
		buf := make([]byte, s.Size)
		if _, err := f.ReadAt(buf, s.Offset); err != nil {
			return fmt.Errorf("read sample %d: %w", i, err)
		}
		_, err = part.Write(buf)
		return err
	})
}

// handleTimelapseFramesBatch handles GET /api/recordings/{id}/timelapse-frames/batch.
// Serves a multipart/mixed JPEG batch from an MJPEG/timelapse recording
// directory (timestamped .jpg files, cached listing) or an AVI recording
// (frames indexed via the AVI movi chunk index).
func (h *Handler) handleTimelapseFramesBatch(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}
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

	switch rec.Format {
	case model.FormatMJPEG, model.FormatTimelapse:
		h.handleFramesBatchDir(w, r, rec)
	case model.FormatAVI:
		h.handleFramesBatchAVI(w, r, rec)
	default:
		WriteError(w, http.StatusNotFound, "not a timelapse or MJPEG recording")
	}
}

// handleFramesBatchDir streams a JPEG batch from an MJPEG/timelapse directory.
func (h *Handler) handleFramesBatchDir(w http.ResponseWriter, r *http.Request, rec *model.Recording) {
	offset, limit, err := parseFrameBatchParams(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	names, err := h.sortedImageFiles(rec.FilePath)
	if err != nil {
		WriteError(w, http.StatusNotFound, "timelapse directory not found")
		return
	}
	h.writeFrameBatch(w, len(names), offset, limit, func(i int, mw *multipart.Writer) error {
		part, err := writeJPEGPartHeader(mw, i)
		if err != nil {
			return err
		}
		f, err := os.Open(filepath.Join(rec.FilePath, names[i]))
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(part, f)
		return err
	})
}

// handleFramesBatchAVI streams a JPEG batch from an AVI recording using the
// demuxer's video frame index (one seek + copy per frame, no full demux).
func (h *Handler) handleFramesBatchAVI(w http.ResponseWriter, r *http.Request, rec *model.Recording) {
	offset, limit, err := parseFrameBatchParams(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
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

	h.writeFrameBatch(w, len(entries), offset, limit, func(i int, mw *multipart.Writer) error {
		part, err := writeJPEGPartHeader(mw, i)
		if err != nil {
			return err
		}
		if _, err := f.Seek(entries[i].Offset, io.SeekStart); err != nil {
			return err
		}
		_, err = io.CopyN(part, f, int64(entries[i].Size))
		return err
	})
}
