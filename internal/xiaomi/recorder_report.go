package xiaomi

// Metrics and health-event reporting helpers for the Xiaomi recorder: the
// counter wrappers, vendor connect-error classification and the SSE/DB quality
// transition records. Split out of recorder.go — these never touch the packet
// processing path, they only observe it.

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// recordSegmentCreated increments the segments created counter if metrics is available.
func (r *XiaomiRecorder) recordSegmentCreated() {
	if r.metrics != nil {
		r.metrics.SegmentsCreated.WithLabelValues(r.cfg.CameraID, string(r.codec)).Inc()
	}
}

// recordBytes adds to the recording bytes counter if metrics is available.
func (r *XiaomiRecorder) recordBytes(b int64) {
	if r.metrics != nil {
		r.metrics.RecordingBytesTotal.WithLabelValues(r.cfg.CameraID, string(r.codec)).Add(float64(b))
	}
}

// recordError increments the camera errors counter if metrics is available.
func (r *XiaomiRecorder) recordError(errorType string) {
	if r.metrics != nil {
		r.metrics.CameraErrors.WithLabelValues(r.cfg.CameraID, errorType).Inc()
	}
}

// classifyDisconnectReason maps an error to a disconnect reason label.
func classifyDisconnectReason(err error) string {
	if err == nil {
		return "network"
	}
	msg := err.Error()
	if strings.Contains(msg, "no data") {
		return "idle_timeout"
	}
	if strings.Contains(msg, "EOF") || strings.Contains(msg, "connection closed") {
		return "eof"
	}
	if strings.Contains(msg, "cloud") || strings.Contains(msg, "resolve") {
		return "cloud_resolve"
	}
	return "network"
}

// recordXiaomiDisconnect increments the Xiaomi disconnect counter if metrics is available.
func (r *XiaomiRecorder) recordXiaomiDisconnect(reason string) {
	if r.metrics != nil && r.metrics.XiaomiDisconnects != nil {
		r.metrics.XiaomiDisconnects.WithLabelValues(r.cfg.CameraID, reason).Inc()
	}
}

// recordXiaomiReconnect increments the Xiaomi reconnect counter if metrics is available.
func (r *XiaomiRecorder) recordXiaomiReconnect() {
	if r.metrics != nil && r.metrics.XiaomiReconnects != nil {
		r.metrics.XiaomiReconnects.WithLabelValues(r.cfg.CameraID).Inc()
	}
}

// reportVendorError checks if the error indicates an unsupported TUTK vendor
// and, if so, reports a detailed CameraErrorDetail via the ErrorReporter.
// This fires on every reconnect attempt so the frontend always has current state.
func (r *XiaomiRecorder) reportVendorError(err error) {
	if r.cfg.ErrReporter == nil || err == nil {
		return
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "unsupported vendor") {
		return
	}
	// Extract vendor name from error: "miss: unsupported vendor \"foo\"".
	// Note: as of v0.9.0 both "cs2" and "tutk" are supported, so this only fires
	// for genuinely unknown vendors — the message is intentionally generic.
	vendor := extractQuotedValue(errMsg)
	msg := fmt.Sprintf("Camera uses an unsupported transport vendor %q. This camera model may not be compatible.", vendor)
	r.cfg.ErrReporter.SetErrorDetail(r.cfg.CameraID, &model.CameraErrorDetail{
		Type:       "tutk_incompatible",
		Message:    msg,
		DetectedAt: time.Now(),
	})
}

// reportConnectError surfaces an actionable CameraErrorDetail when the recorder
// repeatedly fails to reach the camera (e.g. UDP handshake timeout to the LAN IP,
// which looks like "miss connect: read udp i/o timeout"). Without this, the user
// sees "reconnecting" forever with no hint that the camera is unreachable on the
// network — issue #48. After connectFailThreshold consecutive failures, we set a
// "connect_failed" detail so the frontend can show guidance ("check the camera is
// online and on the same network") instead of a bare spinner.
const connectFailThreshold = 5

func (r *XiaomiRecorder) reportConnectError(err error) {
	if r.cfg.ErrReporter == nil || err == nil {
		return
	}
	r.connectFailCount++
	if r.connectFailCount < connectFailThreshold {
		return
	}
	// Extract the host being dialed from the MISS URL for the message.
	host := r.cfg.DID
	if u, perr := url.Parse(r.lastMissURL); perr == nil && u.Host != "" {
		host = u.Host
	}
	msg := fmt.Sprintf(
		"Cannot reach the camera at %s after %d connection attempts. "+
			"Check that the camera is powered on, connected to the same network as the NVR, "+
			"and not blocked by a firewall. (Last error: %s)",
		host, r.connectFailCount, err.Error(),
	)
	r.cfg.ErrReporter.SetErrorDetail(r.cfg.CameraID, &model.CameraErrorDetail{
		Type:       "connect_failed",
		Message:    msg,
		DetectedAt: time.Now(),
	})
}

// clearConnectError resets the connect-failure counter and clears any
// "connect_failed" error detail when the recorder successfully connects.
func (r *XiaomiRecorder) clearConnectError() {
	if r.connectFailCount > 0 {
		r.connectFailCount = 0
	}
	if r.cfg.ErrReporter != nil {
		r.cfg.ErrReporter.SetErrorDetail(r.cfg.CameraID, nil)
	}
}

// extractQuotedValue extracts the content between the first pair of double quotes in s.
// Returns empty string if no quotes are found.
func extractQuotedValue(s string) string {
	start := strings.Index(s, `"`)
	if start < 0 {
		return ""
	}
	end := strings.Index(s[start+1:], `"`)
	if end < 0 {
		return ""
	}
	return s[start+1 : start+1+end]
}
