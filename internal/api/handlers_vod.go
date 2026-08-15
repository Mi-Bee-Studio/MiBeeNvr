package api

import (
	"encoding/binary"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/vod"
)

// VOD HLS playback endpoints (#321 Phase 2). Same exposure class as
// /api/recordings/{id}/download: public (anonymous) because <video>/hls.js
// request them same-origin without auth headers, and the bytes they serve are
// the same media the download route already exposes.
//
//	GET /api/cameras/{cameraID}/playback/playlist.m3u8?start=&end=
//	GET /api/cameras/{cameraID}/playback/{recordingID}/init.mp4
//	GET /api/cameras/{cameraID}/playback/{recordingID}/f{first}-{end}.m4s

// maxVodSpan limits the playlist window. One day is the UI's unit; two days
// headroom for timezone edges without unbounded parse work.
const maxVodSpan = 48 * time.Hour

var vodSegmentName = regexp.MustCompile(`^f(\d+)-(\d+)\.m4s$`)

// handlePlaybackPlaylist renders the VOD M3U8 for a camera + time range.
func (h *Handler) handlePlaybackPlaylist(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "cameraID")
	q := r.URL.Query()
	startStr, endStr := q.Get("start"), q.Get("end")
	if startStr == "" || endStr == "" {
		WriteError(w, http.StatusBadRequest, "start and end (RFC3339) are required")
		return
	}
	start, err := time.Parse(time.RFC3339, startStr)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid start: "+err.Error())
		return
	}
	end, err := time.Parse(time.RFC3339, endStr)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid end: "+err.Error())
		return
	}
	if !end.After(start) {
		WriteError(w, http.StatusBadRequest, "end must be after start")
		return
	}
	if end.Sub(start) > maxVodSpan {
		WriteError(w, http.StatusBadRequest, "range exceeds 48h")
		return
	}

	recs, err := h.db.ListRecordings(r.Context(), model.RecordingFilter{
		CameraID:  cameraID,
		StartTime: start,
		EndTime:   end,
		Formats:   []model.Format{model.FormatH264, model.FormatH265},
		Limit:     200,
		SortBy:    "started_at",
		SortOrder: "asc",
	})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "list recordings: "+err.Error())
		return
	}
	if len(recs) == 0 {
		WriteError(w, http.StatusNotFound, "no video recordings in range")
		return
	}

	playlist, count, err := h.vodMgr.BuildPlaylist(cameraID, recs)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "build playlist: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	// Fragments/init are immutable per recording ID, but the playlist itself
	// shifts as rolling merges replace recordings — keep it revalidatable.
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Vod-Recordings", strconv.Itoa(count))
	_, _ = w.Write([]byte(playlist))
}

// handlePlaybackSegment serves a recording's init segment (init.mp4) or one
// media fragment (f{first}-{end}.m4s, half-open sample range).
func (h *Handler) handlePlaybackSegment(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "cameraID")
	recordingID := chi.URLParam(r, "recordingID")
	segName := chi.URLParam(r, "segName")

	rec, err := h.db.GetRecording(r.Context(), recordingID)
	if err != nil || rec == nil {
		WriteError(w, http.StatusNotFound, "recording not found")
		return
	}
	if rec.CameraID != cameraID {
		WriteError(w, http.StatusNotFound, "recording not found")
		return
	}
	if rec.Format != model.FormatH264 && rec.Format != model.FormatH265 {
		WriteError(w, http.StatusNotFound, "recording is not video-playable")
		return
	}

	info, err := h.vodMgr.GetInfo(*rec)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "parse recording: "+err.Error())
		return
	}

	if segName == "init.mp4" {
		initSeg, err := vod.BuildInitSegment(info, vod.IncludeAudio(info))
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "build init segment: "+err.Error())
			return
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("Content-Length", strconv.Itoa(len(initSeg)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(initSeg)
		return
	}

	m := vodSegmentName.FindStringSubmatch(segName)
	if m == nil {
		WriteError(w, http.StatusNotFound, "unknown segment")
		return
	}
	first, err1 := strconv.Atoi(m[1])
	end, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil {
		WriteError(w, http.StatusBadRequest, "bad segment range")
		return
	}

	// Re-derive the fragment plan and locate the requested fragment. The plan
	// is deterministic, so this both validates the range and reproduces the
	// exact audio alignment/timing the playlist was generated with.
	var frag *vod.Fragment
	for _, f := range vod.PlanFragments(info, h.vodMgr.TargetSec()) {
		if f.First == first && f.End == end {
			ff := f
			frag = &ff
			break
		}
	}
	if frag == nil {
		WriteError(w, http.StatusNotFound, "segment not in current playlist (playlist may be stale)")
		return
	}

	data, err := vod.BuildFragment(info, *frag, uint32(frag.First), vod.IncludeAudio(info))
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "build fragment: "+err.Error())
		return
	}

	src, err := os.Open(info.FilePath)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "open recording file: "+err.Error())
		return
	}
	defer src.Close()

	w.Header().Set("Content-Type", "video/mp4")
	// A fragment for a given recording ID + sample range is immutable.
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	w.Header().Set("Content-Length", strconv.FormatInt(data.TotalBytes, 10))
	w.WriteHeader(http.StatusOK)

	if _, err := w.Write(data.Moof); err != nil {
		return
	}
	mdatBytes := data.TotalBytes - int64(len(data.Moof)) - 8
	var mdatHeader [8]byte
	binary.BigEndian.PutUint32(mdatHeader[0:4], uint32(mdatBytes+8))
	copy(mdatHeader[4:8], "mdat")
	if _, err := w.Write(mdatHeader[:]); err != nil {
		return
	}

	buf := make([]byte, 1<<20)
	for _, ranges := range [][]vod.ByteRange{data.VideoRanges, data.AudioRanges} {
		for _, br := range ranges {
			if _, err := io.CopyBuffer(w, io.NewSectionReader(src, br.Offset, br.Size), buf); err != nil {
				return // client went away mid-stream
			}
		}
	}
}
