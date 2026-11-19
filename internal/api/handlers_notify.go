package api

import (
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/api/notifyapi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/notify"
)

// NotifyService is the notifications dispatcher surface the API needs.
// Satisfied by *notify.Service; interface-typed for handler tests.
type NotifyService = notifyapi.Service

// SetNotifyService wires the outbound alert dispatcher. nil (notifications
// disabled) leaves the endpoints reporting disabled instead of 404-ing, so
// the settings panel can explain itself.
func (h *Handler) SetNotifyService(svc NotifyService) {
	h.notifySvc = svc
}

// notifyDeps adapts the stored service to the extracted package's Deps,
// turning a nil interface into nil (not a typed nil).
func notifyDeps(svc NotifyService) notifyapi.Service {
	if svc == nil {
		return nil
	}
	return svc
}

var _ = notify.KnownTypes
