package api

import (
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// maxONVIFNotifyBody caps a wsnt:Notify POST. Real notifications carry a
// handful of messages; the cap exists so a misbehaving sender can't pin
// memory with an unbounded body.
const maxONVIFNotifyBody = 1 << 20 // 1 MiB

// handleONVIFNotify is the WS-BaseNotification consumer endpoint: ONVIF
// devices supporting push subscriptions POST wsnt:Notify SOAP bodies here
// after the NVR subscribed them with a ConsumerReference pointing at this
// path. The per-camera random token in the path is the credential — devices
// cannot BasicAuth (#922), the same public+rate-limited shape as the GB28181
// snapshot upload callback.
//
// POST /api/onvif/notify/{cameraID}/{token}
func (h *Handler) handleONVIFNotify(w http.ResponseWriter, r *http.Request) {
	if h.camMgr == nil {
		http.NotFound(w, r)
		return
	}
	cameraID := chi.URLParam(r, "cameraID")
	token := chi.URLParam(r, "token")

	body, err := io.ReadAll(io.LimitReader(r.Body, maxONVIFNotifyBody))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	code, msg := h.camMgr.HandleOnvifNotify(cameraID, token, body)
	if msg != "" && code != http.StatusOK {
		http.Error(w, msg, code)
		return
	}
	w.WriteHeader(code)
}
