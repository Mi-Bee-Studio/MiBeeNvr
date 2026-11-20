package api

import (
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/api/camerasapi"
)

// camerasDeps adapts the Handler's fields to the camera-management domain.
func camerasDeps(h *Handler) camerasapi.Deps {
	return camerasapi.Deps{
		DB:                h.db,
		Config:            h.config,
		ConfigPath:        h.configPath,
		Cam:               h.camMgr,
		MergeScheduler:    h.mergeScheduler,
		WS:                h.wsMgr,
		EventBus:          h.eventBus,
		CloudProxy:        h.cloudProxy,
		Onboarding:        h.onboarding,
		TimeSync:          h.timeSync,
		TriggerDispatcher: h.triggerDispatcher,
		XiaomiPTZMotor:    h.xiaomiPTZMotor,
		ShutdownSignaled:  func() <-chan struct{} { return streamShutdown },
		CameraHealth: func(cameraID string) any {
			if h.healthMgr == nil {
				return nil
			}
			health := h.healthMgr.GetCameraHealth(cameraID)
			if health == nil {
				return nil // typed-nil guard: *model.CameraHealth nil must read as absent
			}
			return health
		},
		GBDeviceAddrs: func() []camerasapi.GBDeviceNet {
			if h.gb28181DeviceMgr == nil {
				return nil
			}
			devices := h.gb28181DeviceMgr.AllDevices()
			out := make([]camerasapi.GBDeviceNet, 0, len(devices))
			for _, d := range devices {
				d.Mu.RLock()
				id, addr := d.ID, d.NetAddr
				d.Mu.RUnlock()
				out = append(out, camerasapi.GBDeviceNet{ID: id, NetAddr: addr})
			}
			return out
		},
	}
}

// NewLocalXiaomiAuth mirrors the moved constructor (app wiring surface).
func NewLocalXiaomiAuth(cfg *config.Config) *LocalXiaomiAuth {
	return camerasapi.NewLocalXiaomiAuth(cfg)
}

// SetTriggerDispatcher wires the trigger action dispatcher (#709).
func (h *Handler) SetTriggerDispatcher(f func(cameraID, action string, duration time.Duration)) {
	h.triggerDispatcher = f
}

// Compat aliases: the interfaces moved into camerasapi; the api root keeps
// the names for its remaining wiring/tests.
type (
	LocalXiaomiAuth           = camerasapi.LocalXiaomiAuth
	CloudAuthResult           = camerasapi.CloudAuthResult
	CloudVerificationRequired = camerasapi.CloudVerificationRequired
	CloudDeviceInfo           = camerasapi.CloudDeviceInfo
	CloudAuthProxy            = camerasapi.CloudAuthProxy
	OnboardingService         = camerasapi.OnboardingService
	TimeSyncOps               = camerasapi.TimeSyncOps
)
