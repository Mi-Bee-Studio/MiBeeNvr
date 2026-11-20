package api

import (
	"net/http"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/api/gbapi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/api/streamsapi"
)

// gbDeps adapts the Handler's fields to the GB28181/ONVIF domain Deps.
func gbDeps(h *Handler) gbapi.Deps {
	return gbapi.Deps{
		DB:        h.db,
		Config:    h.config,
		Cam:       h.camMgr,
		DeviceMgr: h.gb28181DeviceMgr,
		Records:   h.gb28181Records,
		SnapMgr:   h.gb28181SnapMgr,
		PTZ:       h.gb28181PTZ,
		Catalog:   h.gb28181Catalog,
		Inviter:   h.gb28181Inviter,
		Bye:       h.gb28181Bye,
		Cascade:   h.gb28181Cascade,
		Commander: h.gb28181Commander,
		Media:     h.gb28181Media,
		PTZSuppress: func(cameraID string, window time.Duration) {
			h.suppressPTZMotion(cameraID, window)
		},
		XiaomiVectorMove: func(w http.ResponseWriter, cameraID string, pan, tilt, zoom float64) {
			h.handleXiaomiVectorPTZMove(w, cameraID, pan, tilt, zoom)
		},
		XiaomiMotorCommand: func(cameraID, direction string, speed int) error {
			return h.xiaomiMotorCommand(cameraID, direction, speed)
		},
		XiaomiPTZStatusCode: func(err error) int {
			return h.xiaomiPTZStatusCode(err)
		},
	}
}

// Compat aliases: the GB interfaces moved into gbapi; the api root keeps the
// names for its remaining wiring/tests (app setters + flow sessions).
type (
	GBCommander          = gbapi.GBCommander
	gbCommander          = gbapi.GBCommander
	GB28181InviteSender  = gbapi.GB28181InviteSender
	GB28181ByeSender     = gbapi.GB28181ByeSender
	GB28181CascadeStatus = gbapi.GB28181CascadeStatus
	GB28181DeviceMedia   = gbapi.GB28181DeviceMedia
	GB28181SessionStatus = streamsapi.GB28181SessionStatus
)
