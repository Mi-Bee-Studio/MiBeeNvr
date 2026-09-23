package api

// handlers_offload.go — remote object-storage offload endpoints (issue #874
// batch 2):
//
//	GET  /api/offload/recordings        (authed) evicted/remote archive listing
//	GET  /api/offload/objects/{id}      (authed, Range) playback proxy
//	HEAD /api/offload/objects/{id}      (authed) <video> size probe
//
// The object endpoints sit in the protected media group with the recording
// downloads (#893, tracking #879/#882): <video> element fetches reach them
// via ?token= links minted by the SPA (appendAuthToken), the same mechanism
// as /api/recordings/{id}/download.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/go-chi/chi/v5"
)

// OffloadPlaybackProxy is the remote-object read path implemented by
// internal/offload.Proxy (range coalescing + block cache, batch-3 presign).
// The interface keeps api free of the offload import.
type OffloadPlaybackProxy interface {
	ServeRange(ctx context.Context, bucket, key string, start, end, total int64) (io.ReadCloser, error)
	// PresignGet produces a direct-GET URL for the object. Callers fall back
	// to ServeRange when it errors.
	PresignGet(ctx context.Context, bucket, key string, ttl time.Duration) (string, error)
}

// SetOffloadPlayback wires the remote playback proxy (nil = remote offload
// disabled; the endpoints degrade to 404).
func (h *Handler) SetOffloadPlayback(p OffloadPlaybackProxy) {
	h.offloadPlayback = p
}

// handleListOffloadRecordings handles GET /api/offload/recordings.
// Returns evicted (remote-only) archive items by default; ?status= (repeatable
// or comma-separated) overrides.
func (h *Handler) handleListOffloadRecordings(w http.ResponseWriter, r *http.Request) {
	limit, offset := parsePagination(r, 50, 500)
	filter := storage.OffloadRemoteFilter{CameraID: r.URL.Query().Get("camera_id")}
	if v := r.URL.Query().Get("start"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.StartAt = t.UTC()
		} else {
			WriteError(w, http.StatusBadRequest, "start must be RFC3339")
			return
		}
	}
	if v := r.URL.Query().Get("end"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.EndAt = t.UTC()
		} else {
			WriteError(w, http.StatusBadRequest, "end must be RFC3339")
			return
		}
	}
	if statuses := r.URL.Query()["status"]; len(statuses) > 0 {
		for _, csv := range statuses {
			for _, st := range strings.Split(csv, ",") {
				if st = strings.TrimSpace(st); st != "" {
					filter.Statuses = append(filter.Statuses, st)
				}
			}
		}
	}
	items, err := h.db.ListOffloadRemote(r.Context(), filter, limit, offset)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to list offload recordings")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// handleOffloadObject handles GET/HEAD /api/offload/objects/{id} — ranged
// playback of a remote-only (evicted) archive object through the proxy.
func (h *Handler) handleOffloadObject(w http.ResponseWriter, r *http.Request) {
	if h.offloadPlayback == nil {
		WriteError(w, http.StatusNotFound, "remote offload is not enabled")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		WriteError(w, http.StatusNotFound, "object not found")
		return
	}
	item, err := h.db.GetOffloadItem(r.Context(), id)
	if err != nil || item == nil {
		WriteError(w, http.StatusNotFound, "object not found")
		return
	}
	// Only remote-EXCLUSIVE content rides this endpoint: an uploaded item's
	// local file still exists and plays through the normal local path.
	if item.Status != storage.OffloadStatusEvicted {
		WriteError(w, http.StatusNotFound, "object not available remotely")
		return
	}
	total := item.UploadedSize
	if total <= 0 {
		WriteError(w, http.StatusNotFound, "object metadata missing size")
		return
	}

	// Presigned direct playback (batch 3): 302 to a signed URL so media
	// bytes flow store→browser without transiting the NVR. Opt-in — the
	// configured endpoint is often not browser-reachable (Docker-internal
	// names), which is why proxying stays the default. Any presign failure
	// degrades to the proxy path below (a broken redirect would kill
	// playback; proxying only costs NVR bandwidth).
	if h.config != nil && h.config.Storage.Remote.Playback.Presigned {
		ttl := time.Duration(h.config.Storage.Remote.Playback.TTLS) * time.Second
		if ttl <= 0 {
			ttl = time.Hour
		}
		if url, err := h.offloadPlayback.PresignGet(r.Context(), item.Bucket, item.ObjectKey, ttl); err == nil {
			// Range headers ride along — the browser re-issues its range
			// against the signed URL.
			http.Redirect(w, r, url, http.StatusFound)
			return
		}
	}

	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Accept-Ranges", "bytes")

	start, end, ok := parseSingleRange(r.Header.Get("Range"), total)
	if !ok {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", total))
		WriteError(w, http.StatusRequestedRangeNotSatisfiable, "range not satisfiable")
		return
	}

	status := http.StatusOK
	serveStart, serveEnd := int64(0), total-1
	if start >= 0 { // a Range header was present
		status = http.StatusPartialContent
		serveStart, serveEnd = start, end
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
	}
	w.Header().Set("Content-Length", strconv.FormatInt(serveEnd-serveStart+1, 10))

	if r.Method == http.MethodHead {
		w.WriteHeader(status)
		return
	}
	rc, err := h.offloadPlayback.ServeRange(r.Context(), item.Bucket, item.ObjectKey, serveStart, serveEnd, total)
	if err != nil {
		// Headers are already set; the honest status at this point is 502
		// (upstream object store).
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	defer rc.Close()
	w.WriteHeader(status)
	_, _ = io.Copy(w, rc)
}

// parseSingleRange parses a single-range "bytes=a-b" header against a
// resource of the given total size. Returns start=-1 when no Range header is
// present (full-body semantics), ok=false for malformed or unsatisfiable
// ranges. end is clamped to total-1; suffix forms ("bytes=-N") and
// open-ended ("bytes=a-") are supported. Multi-range requests are treated as
// absent (full body) — browsers never send them for <video>, and re-assembly
// (multipart/byteranges) would multiply upstream requests, the exact thing
// the coalescing proxy exists to prevent.
func parseSingleRange(hdr string, total int64) (start, end int64, ok bool) {
	if hdr == "" {
		return -1, -1, true
	}
	const prefix = "bytes="
	if !strings.HasPrefix(hdr, prefix) || strings.Contains(hdr, ",") {
		return -1, -1, true // not a range we serve singly → full body
	}
	spec := strings.TrimSpace(strings.TrimPrefix(hdr, prefix))
	dash := strings.IndexByte(spec, '-')
	if dash < 0 {
		return 0, 0, false
	}
	left, right := strings.TrimSpace(spec[:dash]), strings.TrimSpace(spec[dash+1:])

	switch {
	case left == "" && right != "": // suffix form: bytes=-N
		n, err := strconv.ParseInt(right, 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, false
		}
		if n > total {
			n = total
		}
		return total - n, total - 1, true
	case left != "" && right == "": // open-ended: bytes=a-
		s, err := strconv.ParseInt(left, 10, 64)
		if err != nil || s < 0 || s >= total {
			return 0, 0, false
		}
		return s, total - 1, true
	default:
		s, err1 := strconv.ParseInt(left, 10, 64)
		e, err2 := strconv.ParseInt(right, 10, 64)
		if err1 != nil || err2 != nil || s < 0 || e < s || s >= total {
			return 0, 0, false
		}
		if e >= total {
			e = total - 1
		}
		return s, e, true
	}
}

// registerOffloadRoutes registers the authed offload routes.
func (h *Handler) registerOffloadRoutes(r chi.Router) {
	r.Get("/api/offload/recordings", h.handleListOffloadRecordings)
	r.Get("/api/offload/status", h.handleOffloadStatus)
}

// handleOffloadStatus handles GET /api/offload/status — outbox counters for
// the Settings observability card (backlog = pending+uploading).
func (h *Handler) handleOffloadStatus(w http.ResponseWriter, r *http.Request) {
	counts, err := h.db.CountOffloadByStatus(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to count offload outbox")
		return
	}
	backlog, err := h.db.CountOffloadBacklog(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to count offload backlog")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"counts":  counts,
		"backlog": backlog,
	})
}
