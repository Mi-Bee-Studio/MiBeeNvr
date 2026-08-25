package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/hls"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/substream"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/xiaomi"
	"github.com/go-chi/chi/v5"
)

// --- HLS streaming endpoints ---

// subscribeHLS registers an HLS consumer on the recorder's StreamHub.
// It uses Hub.Subscribe with consumer ID "hls" so that the HLS manager
// receives frames via the fan-out architecture.
// It first unsubscribes any stale "hls" consumer left over from a previous
// session (e.g. after idle eviction), then subscribes with the new callback.
func subscribeHLS(hub *model.StreamHub, cameraID string, hlsMgr *hls.Manager, isH265 bool) error {
	if hub == nil {
		return nil // no hub, no subscription (shouldn't happen in practice)
	}
	hub.Unsubscribe("hls") // clean up stale consumer from previous session
	return hlsMgr.SubscribeToHub(cameraID, hub, isH265)
}

// hlsSubPathPrefix is the URL path marker selecting the sub-stream for HLS.
// HLS is served under a path wildcard (/stream/*) and playlists reference
// segments with RELATIVE URLs — the segment requests must resolve to the same
// entry the playlist came from, so quality rides in the PATH
// (/api/cameras/{id}/stream/sub/index.m3u8), not a query parameter that
// segment fetches would drop.
const hlsSubPathPrefix = "/stream/sub/"

func (h *Handler) handleHLSStream(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Validate request parameters before service availability. quality
	// negotiation (#513): the path form (/stream/sub/…) selects the
	// sub-stream; an explicit ?quality= on HLS is rejected because segments
	// cannot carry it (they would fall back to the main entry and desync the
	// playlist).
	if q := r.URL.Query().Get("quality"); q != "" {
		if _, err := parseQuality(r); err != nil {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		WriteError(w, http.StatusBadRequest,
			"HLS quality selection uses the path form /stream/sub/index.m3u8 (segments cannot carry a query parameter)")
		return
	}

	if h.hlsMgr == nil || h.camMgr == nil {
		WriteError(w, http.StatusInternalServerError, "HLS not available")
		return
	}

	// Get camera to check protocol
	cam, err := h.db.GetCamera(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to get camera")
		return
	}
	if cam == nil {
		WriteError(w, http.StatusNotFound, "camera not found")
		return
	}

	key := id
	var subSrc *substream.Source
	if strings.Contains(r.URL.Path, hlsSubPathPrefix) {
		// Hold the reference for this request only. Playlist polling re-
		// acquires well inside the idle timeout, so an active player keeps
		// the puller warm; once polling stops, the idle timeout recycles it.
		if src := h.acquireSub(w, r, id); src != nil {
			subSrc = src
			key = subKey(id)
			defer h.camMgr.ReleaseSubStream(id)
		}
		if strings.HasSuffix(r.URL.Path, ".m3u8") {
			// (acquireSub already set the header; re-affirm for playlists
			// because hls.js inspects only these responses.)
			if subSrc == nil {
				w.Header().Set("X-Stream-Quality", qualityMain)
			}
		}
	}

	// If stream not active, start it
	if !h.hlsMgr.IsActive(key) {
		// Get camera config for HLS options
		camCfg := h.camMgr.GetCameraConfig(id)
		hlsMaxFPS := 0
		if camCfg != nil {
			hlsMaxFPS = camCfg.HLSMaxFPS
		}

		var codec model.Format
		var sps, pps, vps []byte
		var hub *model.StreamHub
		if subSrc != nil {
			// Sub-stream: parameters from the pull's SDP/in-band snapshot;
			// the main recorder need not be running at all.
			codec, sps, pps, vps = subSrc.CodecParams()
			hub = subSrc.Hub()
		} else {
			rec := h.camMgr.GetRecorder(id)
			if rec == nil {
				WriteError(w, http.StatusBadRequest, "camera recorder not running")
				return
			}
			codec, sps, pps, vps = getCodecParams(rec)
			hub = getStreamHub(rec)
		}

		switch codec {
		case model.FormatH264:
			if sps == nil || pps == nil {
				WriteError(w, http.StatusServiceUnavailable, "SPS/PPS not available yet, waiting for video stream")
				return
			}
			if err := h.hlsMgr.StartStream(key, sps, pps, hlsMaxFPS); err != nil {
				if errors.Is(err, hls.ErrMaxStreamsReached) {
					writeAPIError(w, http.StatusServiceUnavailable, &model.HLSMaxStreamsError{})
				} else {
					logger.Error("failed to start HLS stream", "camera_id", id, "key", key, "error", err)
					WriteError(w, http.StatusInternalServerError, "failed to start HLS stream")
				}
				return
			}
			_ = subscribeHLS(hub, key, h.hlsMgr, false)
		case model.FormatH265:
			if sps == nil || pps == nil || vps == nil {
				WriteError(w, http.StatusServiceUnavailable, "VPS/SPS/PPS not available yet, waiting for video stream")
				return
			}
			if err := h.hlsMgr.StartStreamH265(key, vps, sps, pps, hlsMaxFPS); err != nil {
				if errors.Is(err, hls.ErrMaxStreamsReached) {
					writeAPIError(w, http.StatusServiceUnavailable, &model.HLSMaxStreamsError{})
				} else {
					logger.Error("failed to start HLS H265 stream", "camera_id", id, "key", key, "error", err)
					WriteError(w, http.StatusInternalServerError, "failed to start HLS stream")
				}
				return
			}
			_ = subscribeHLS(hub, key, h.hlsMgr, true)
		default:
			writeAPIError(w, http.StatusBadRequest, &model.HLSSupportedCodecError{CameraID: id})
			return
		}
	} else if subSrc != nil {
		// Active entry, but it may be subscribed to a hub from a previous
		// puller generation (recycled + restarted since) — rebind, mirroring
		// the WS endpoint's recovery.
		codec, _, _, _ := subSrc.CodecParams()
		if h.hlsMgr.ActiveHub(key) != subSrc.Hub() {
			h.hlsMgr.RebindHub(key, subSrc.Hub(), codec == model.FormatH265)
		}
	}
	// Issue the stream cookie on playlist fetches (#331): media players that
	// cannot attach headers to every request (iOS AVPlayer on HLS segments)
	// need a cookie to authenticate the relative segment URLs, which do not
	// inherit the playlist's query token per RFC 3986. HttpOnly + SameSite=Lax
	// + the method/suffix gate in bearerSessionToken keep the cookie from
	// ever authenticating anything but read-only media fetches. Re-issued on
	// every playlist fetch, so players polling the playlist stay fresh.
	setStreamCookieOnPlaylist(w, r, id)
	// Proxy to muxer handler
	if !h.hlsMgr.Handle(key, w, r) {
		WriteError(w, http.StatusServiceUnavailable, "HLS stream not available")
		return
	}
}

// setStreamCookieOnPlaylist sets the mbs_session cookie when the request is
// for an HLS playlist (path ends in .m3u8) and was authenticated with a
// session token. A freshly-renewed token (X-Renewed-Token) takes precedence
// so the cookie outlives the original token's remaining lifetime.
func setStreamCookieOnPlaylist(w http.ResponseWriter, r *http.Request, cameraID string) {
	if !strings.HasSuffix(r.URL.Path, ".m3u8") {
		return
	}
	tok := middleware.SessionTokenFromRequest(r)
	if tok == "" {
		return // BasicAuth/API-key callers: nothing to hand out
	}
	if renewed := w.Header().Get(middleware.RenewedTokenHeader); renewed != "" {
		tok = renewed
	}
	http.SetCookie(w, &http.Cookie{
		Name:     middleware.StreamCookieName,
		Value:    tok,
		Path:     "/api/cameras/" + cameraID + "/",
		MaxAge:   int(middleware.TokenTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})
}

func (h *Handler) handleStopHLSStream(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if h.hlsMgr == nil {
		WriteError(w, http.StatusInternalServerError, "HLS not available")
		return
	}

	if !h.hlsMgr.IsActive(id) && !h.hlsMgr.IsActive(subKey(id)) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "not active"})
		return
	}

	// Unsubscribe HLS consumer from StreamHub before stopping the stream
	if h.camMgr != nil {
		if rec := h.camMgr.GetRecorder(id); rec != nil {
			hub := getRecorderHub(rec)
			if hub != nil {
				hub.Unsubscribe("hls")
			}
		}
	}

	h.hlsMgr.StopStream(id)
	// Stop the sub-stream entry too and release its pull reference (#513) —
	// the on-demand puller otherwise idles out up to idle_timeout_s later.
	if h.hlsMgr.IsActive(subKey(id)) {
		if hub := h.hlsMgr.ActiveHub(subKey(id)); hub != nil {
			hub.Unsubscribe("hls")
		}
		h.hlsMgr.StopStream(subKey(id))
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

// getRecorderHub extracts the StreamHub from any recorder type.
// Returns nil if the recorder doesn't have a Hub.
func getRecorderHub(rec model.Recorder) *model.StreamHub {
	switch r := rec.(type) {
	case *recorder.H264Recorder:
		return r.Hub
	case *recorder.H265Recorder:
		return r.Hub
	case *recorder.ONVIFRecorder:
		// ONVIF passes Hub to delegate, but we unsubscribe from the delegate's hub
		if delegate := r.Delegate(); delegate != nil {
			return getRecorderHub(delegate)
		}
		return r.Hub
	case *recorder.MJPEGRecorder:
		return r.Hub
	case *recorder.HTTPJPEGRecorder:
		return r.Hub
	case *xiaomi.XiaomiRecorder:
		return r.Hub
	case *recorder.IngestRecorder:
		// Push cameras (srt/rtmp): the hub is set by camera.initStreamHub,
		// same as every other recorder. Without this case, HLS would subscribe
		// to a nil hub and never receive the published frames.
		return r.Hub
	default:
		return nil
	}
}

// --- Snapshot endpoint ---

func (h *Handler) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "id")

	// Find camera config to get SnapshotURL + credentials
	var snapshotURL, username, password string
	if h.config != nil {
		for _, cam := range h.config.Cameras {
			if cam.ID == cameraID {
				snapshotURL = cam.SnapshotURL
				username = cam.Username
				password = cam.Password
				break
			}
		}
	}
	if snapshotURL == "" {
		WriteError(w, http.StatusNotFound, "Snapshot URL not configured")
		return
	}

	// Check cache (10 second TTL)
	const cacheTTL = 10 * time.Second
	h.snapshotMu.RLock()
	cached, ok := h.snapshots[cameraID]
	h.snapshotMu.RUnlock()

	if ok && time.Since(cached.timestamp) < cacheTTL {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "max-age=5")
		w.Write(cached.data)
		return
	}

	// Fetch from camera. Many cameras (esp. ONVIF ones whose snapshot URI was
	// auto-populated via GetSnapshotUri) require HTTP Basic auth on the snapshot
	// endpoint even when the ONVIF service itself is unauthenticated — so we
	// attach the camera credentials when present. client.Get cannot set headers,
	// hence NewRequestWithContext + client.Do.
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, snapshotURL, nil)
	if err != nil {
		serveStaleOrError(w, cached, ok, cameraID, fmt.Errorf("build snapshot request: %w", err))
		return
	}
	if username != "" {
		req.SetBasicAuth(username, password)
	}
	resp, err := client.Do(req)
	if err != nil {
		serveStaleOrError(w, cached, ok, cameraID, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		logger.Warn("snapshot fetch unauthorized — check camera credentials",
			"camera_id", cameraID, "status", resp.StatusCode)
		serveStaleOrError(w, cached, ok, cameraID, fmt.Errorf("snapshot unauthorized (check camera username/password)"))
		return
	}
	if resp.StatusCode != http.StatusOK {
		serveStaleOrError(w, cached, ok, cameraID, fmt.Errorf("camera returned status %d", resp.StatusCode))
		return
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024)) // 10MB max
	if err != nil || len(data) == 0 {
		serveStaleOrError(w, cached, ok, cameraID, fmt.Errorf("read snapshot body"))
		return
	}

	// Update cache
	h.snapshotMu.Lock()
	h.snapshots[cameraID] = &snapshotCache{data: data, timestamp: time.Now()}
	h.snapshotMu.Unlock()

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "max-age=5")
	w.Write(data)
}

// serveStaleOrError returns the stale cached snapshot when available, otherwise
// responds with the given error as a 502 Bad Gateway. Used by handleSnapshot on
// any fetch/read failure so a transient camera hiccup doesn't blank the UI.
func serveStaleOrError(w http.ResponseWriter, cached *snapshotCache, ok bool, cameraID string, err error) {
	if ok {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("X-Cache", "stale")
		w.Write(cached.data)
		return
	}
	logger.Warn("failed to fetch snapshot", "camera_id", cameraID, "error", err)
	WriteError(w, http.StatusBadGateway, "Failed to fetch snapshot")
}
