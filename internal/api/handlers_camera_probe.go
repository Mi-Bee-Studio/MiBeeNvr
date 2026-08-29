package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
)

// handleTestConnection attempts to connect to a camera URL with a short timeout.
// Returns success/failure, a human-readable message, and the latency in milliseconds.
func (h *Handler) handleTestConnection(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Protocol      string `json:"protocol"`
		URL           string `json:"url"`
		Username      string `json:"username"`
		Password      string `json:"password"`
		Encoding      string `json:"encoding"`
		ONVIFEndpoint string `json:"onvif_endpoint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.URL == "" && body.ONVIFEndpoint == "" {
		WriteError(w, http.StatusBadRequest, "url is required")
		return
	}

	target := body.URL
	if body.Protocol == "onvif" && body.ONVIFEndpoint != "" {
		target = body.ONVIFEndpoint
	}

	startTime := time.Now()

	// ONVIF cameras get a real stream-access probe: the old HTTP-HEAD check could
	// report success while the RTSP stream was unreachable or credentials were
	// wrong for GetStreamUri — the root of the "test passes but no image" reports
	// (issues #29/#30). The probe distinguishes reachable / stream-ok / codec-lie.
	if body.Protocol == "onvif" {
		probe := probeONVIFStream(r.Context(), target, body.Username, body.Password)
		writeJSON(w, http.StatusOK, map[string]any{
			"success":    probe.StreamOK,
			"reachable":  probe.Reachable,
			"stream_ok":  probe.StreamOK,
			"encoding":   probe.Encoding,
			"codec_lie":  probe.CodecLie,
			"message":    probe.reasonOrOK(),
			"latency_ms": time.Since(startTime).Milliseconds(),
		})
		return
	}

	switch {
	case strings.HasPrefix(target, "rtsp://"):
		// RTSP: try TCP connection to the host:port
		conn, err := net.DialTimeout("tcp", stripScheme(target), 3*time.Second)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"success":    false,
				"message":    fmt.Sprintf("connection refused: %v", err),
				"latency_ms": time.Since(startTime).Milliseconds(),
			})
			return
		}
		conn.Close()

	default:
		// HTTP/ONVIF: try HEAD/GET request with timeout
		client := &http.Client{Timeout: 3 * time.Second}
		// For URLs with credentials, inject them
		req, err := http.NewRequestWithContext(r.Context(), http.MethodHead, target, nil)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"success":    false,
				"message":    fmt.Sprintf("invalid URL: %v", err),
				"latency_ms": time.Since(startTime).Milliseconds(),
			})
			return
		}
		if body.Username != "" {
			req.SetBasicAuth(body.Username, body.Password)
		} else {
			// Extract credentials from URL if present (e.g., http://admin:pass@host)
			if parsed, err := url.Parse(target); err == nil && parsed.User != nil {
				if u := parsed.User.Username(); u != "" {
					p, _ := parsed.User.Password()
					req.SetBasicAuth(u, p)
				}
			}
		}
		resp, err := client.Do(req)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"success":    false,
				"message":    fmt.Sprintf("connection failed: %v", err),
				"latency_ms": time.Since(startTime).Milliseconds(),
			})
			return
		}
		resp.Body.Close()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"message":    "connection successful",
		"latency_ms": time.Since(startTime).Milliseconds(),
	})
}

// stripScheme extracts host:port from a URL string for TCP dialing.
// Handles URLs with credentials (user:pass@host) by stripping userinfo.

// stripScheme extracts host:port from a URL string for TCP dialing.
// Handles URLs with credentials (user:pass@host) by stripping userinfo.
func stripScheme(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL // fallback
	}
	host := parsed.Hostname()
	port := parsed.Port()
	if port == "" {
		switch parsed.Scheme {
		case "rtsp":
			port = "554"
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			port = "554"
		}
	}
	return net.JoinHostPort(host, port)
}

// probeONVIFEncoding connects to an ONVIF device and retrieves the encoding
// from the first media profile. Returns "H264" or "H265", or empty string on failure.
// A bounded timeout is applied so a stuck device (e.g. ESP32 MiBeeCam with very
// limited concurrent HTTP capacity) cannot block the camera-create request — the
// caller context may outlive both the user's patience and the frontend fetch timeout.
// probeONVIFEncoding returns the best-known video encoding ("H264" or "H265") for
// an ONVIF device. It starts from the ONVIF profile declaration but verifies it
// against the actual RTSP stream, because some HiSilicon-OEM cameras advertise H264
// in their ONVIF profile while streaming H.265 (see ONVIFRecorder.detectEncoding).
// If the RTSP DESCRIBE probe succeeds, its result is authoritative; otherwise the
// ONVIF-declared encoding is returned as-is.
// onvifStreamProbe is the structured result of probing an ONVIF device for real
// stream access. It distinguishes "device reachable" from "stream actually
// playable" — the old test-connection endpoint conflated the two (an HTTP HEAD
// to the device_service URL can return 200 while the RTSP stream is unreachable
// or the credentials are wrong for GetStreamUri), producing the "test passes but
// no image" reports in issues #29/#30.

// probeONVIFEncoding connects to an ONVIF device and retrieves the encoding
// from the first media profile. Returns "H264" or "H265", or empty string on failure.
// A bounded timeout is applied so a stuck device (e.g. ESP32 MiBeeCam with very
// limited concurrent HTTP capacity) cannot block the camera-create request — the
// caller context may outlive both the user's patience and the frontend fetch timeout.
// probeONVIFEncoding returns the best-known video encoding ("H264" or "H265") for
// an ONVIF device. It starts from the ONVIF profile declaration but verifies it
// against the actual RTSP stream, because some HiSilicon-OEM cameras advertise H264
// in their ONVIF profile while streaming H.265 (see ONVIFRecorder.detectEncoding).
// If the RTSP DESCRIBE probe succeeds, its result is authoritative; otherwise the
// ONVIF-declared encoding is returned as-is.
// onvifStreamProbe is the structured result of probing an ONVIF device for real
// stream access. It distinguishes "device reachable" from "stream actually
// playable" — the old test-connection endpoint conflated the two (an HTTP HEAD
// to the device_service URL can return 200 while the RTSP stream is unreachable
// or the credentials are wrong for GetStreamUri), producing the "test passes but
// no image" reports in issues #29/#30.
type onvifStreamProbe struct {
	Reachable bool   // ONVIF device_service responded to GetDeviceInformation/GetProfiles
	StreamOK  bool   // an RTSP stream URI was resolved AND a DESCRIBE succeeded
	Encoding  string // the REAL codec (RTSP DESCRIBE is authoritative; may differ from declared)
	CodecLie  bool   // the ONVIF-declared codec disagrees with the real stream
	Reason    string // human-readable explanation when Reachable/StreamOK is false
}

// onvifLooksLikeAuthError reports whether an ONVIF error smells like a
// WS-Security rejection (the trigger for running the time-skew diagnosis).
// Mirrors the onvif package's internal isAuthError but lives here to avoid
// widening the onvif package's API surface.

// onvifLooksLikeAuthError reports whether an ONVIF error smells like a
// WS-Security rejection (the trigger for running the time-skew diagnosis).
// Mirrors the onvif package's internal isAuthError but lives here to avoid
// widening the onvif package's API surface.
func onvifLooksLikeAuthError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "NotAuthorized") ||
		strings.Contains(s, "status 401") ||
		strings.Contains(s, "status 403") ||
		strings.Contains(s, "status 400")
}

// reasonOrOK returns the failure reason, or a success message when the stream
// is healthy (used by the test-connection response so the frontend always has a
// sensible message to show).

// reasonOrOK returns the failure reason, or a success message when the stream
// is healthy (used by the test-connection response so the frontend always has a
// sensible message to show).
func (p onvifStreamProbe) reasonOrOK() string {
	if p.StreamOK {
		if p.CodecLie {
			return "stream accessible (declared codec corrected by RTSP probe)"
		}
		return "connection successful"
	}
	return p.Reason
}

// probeONVIFStream connects to an ONVIF device, resolves the stream URI, and
// verifies the stream actually plays via an RTSP DESCRIBE. This is the
// "does it really work?" check that the test-connection flow needs. It is also
// the engine behind probeONVIFEncoding (which discards everything but encoding).

// probeONVIFStream connects to an ONVIF device, resolves the stream URI, and
// verifies the stream actually plays via an RTSP DESCRIBE. This is the
// "does it really work?" check that the test-connection flow needs. It is also
// the engine behind probeONVIFEncoding (which discards everything but encoding).
func probeONVIFStream(ctx context.Context, endpoint, username, password string) onvifStreamProbe {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	client := onvif.NewClient(endpoint, username, password)
	if err := client.Connect(ctx); err != nil {
		reason := fmt.Sprintf("could not connect to ONVIF service: %v", err)
		// If it looks like an auth rejection, run the time-skew diagnosis so the
		// user gets an actionable "sync the camera's clock" hint instead of a
		// generic failure (Hikvision cameras reject digest auth on clock skew).
		if onvifLooksLikeAuthError(err) {
			if diag := client.DiagnoseAuth(ctx); diag.SkewDetected {
				reason = diag.Diagnosis
			}
		}
		return onvifStreamProbe{Reason: reason}
	}
	profiles, err := client.GetProfiles(ctx)
	if err != nil {
		reason := fmt.Sprintf("device responded but GetProfiles failed (credentials may be wrong, or ONVIF may be limited): %v", err)
		if onvifLooksLikeAuthError(err) {
			if diag := client.DiagnoseAuth(ctx); diag.SkewDetected {
				reason = diag.Diagnosis
			}
		}
		return onvifStreamProbe{
			Reachable: true,
			Reason:    reason,
		}
	}
	if len(profiles) == 0 {
		return onvifStreamProbe{
			Reachable: true,
			Reason:    "device responded but exposed no media profiles — check the camera's ONVIF/stream configuration",
		}
	}
	declared := profiles[0].Encoding

	si, err := client.GetStreamURI(ctx, profiles[0].Token)
	if err != nil || si.URI == "" {
		return onvifStreamProbe{
			Reachable: true,
			Reason:    "device responded but GetStreamUri failed — credentials may lack stream access, or the camera does not expose RTSP",
		}
	}

	// RTSP DESCRIBE is authoritative for the codec (corrects cameras that lie —
	// e.g. HiSilicon OEMs advertising H264 while streaming H265) AND confirms the
	// stream is actually playable with the supplied credentials.
	actual := recorder.ProbeRTSPEncoding(si.URI, username, password)
	if actual == "" {
		return onvifStreamProbe{
			Reachable: true,
			Reason:    "stream URI resolved but RTSP DESCRIBE failed — check that the RTSP port is reachable and credentials are correct",
		}
	}
	codecLie := declared != "" && !strings.EqualFold(actual, declared)
	if codecLie {
		logger.Info("ONVIF-declared encoding corrected by RTSP probe",
			"endpoint", endpoint, "declared", declared, "actual", actual)
	}
	return onvifStreamProbe{
		Reachable: true,
		StreamOK:  true,
		Encoding:  actual,
		CodecLie:  codecLie,
	}
}

// probeONVIFEncoding returns just the resolved encoding (empty on failure),
// preserving the original contract of the create-camera path. It now delegates
// to probeONVIFStream so both paths share one probing implementation.

// probeONVIFEncoding returns just the resolved encoding (empty on failure),
// preserving the original contract of the create-camera path. It now delegates
// to probeONVIFStream so both paths share one probing implementation.
func probeONVIFEncoding(ctx context.Context, endpoint, username, password string) string {
	return probeONVIFStream(ctx, endpoint, username, password).Encoding
}

// registerCameraRoutes registers all /api/cameras* routes (including nested
// stream, ONVIF, PTZ, snapshot, merge-config, timelapse config, events, and
// Xiaomi sub-routes) on the given (already auth-protected) router.
// handleAdaptiveTrigger handles POST /api/cameras/{id}/adaptive/trigger
// (issue #478): an external audio-activity event (e.g. a semantic classifier
// running outside the NVR) forces the camera's adaptive gate out of timelapse
// with the same GOP + pre-trigger back-fill as a loud window. Body:
// {"source": "...", "hold": "10s", "dbfs": -23.4} — source/dbfs are
// diagnostics only; hold (optional duration, 0–10m) extends how long
// timelapse entry stays deferred after the event.
