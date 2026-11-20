package api

// Transitional gb shims: the api root's gb tests call these Handler methods
// directly; they delegate into gbapi. Delete as the tests migrate (documented
// in api-package-split.md step 3).

import (
	"net/http"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/api/gbapi"
)

func (h *Handler) handleListGB28181Devices(w http.ResponseWriter, r *http.Request) {
	gbapi.Handle(gbDeps(h), "handleListGB28181Devices")(w, r)
}

func (h *Handler) handleListGB28181Channels(w http.ResponseWriter, r *http.Request) {
	gbapi.Handle(gbDeps(h), "handleListGB28181Channels")(w, r)
}

func (h *Handler) handleGB28181CascadeStatus(w http.ResponseWriter, r *http.Request) {
	gbapi.Handle(gbDeps(h), "handleGB28181CascadeStatus")(w, r)
}

func (h *Handler) handleGB28181DeviceAlarms(w http.ResponseWriter, r *http.Request) {
	gbapi.Handle(gbDeps(h), "handleGB28181DeviceAlarms")(w, r)
}

func (h *Handler) handleGB28181DevicePositions(w http.ResponseWriter, r *http.Request) {
	gbapi.Handle(gbDeps(h), "handleGB28181DevicePositions")(w, r)
}

func (h *Handler) handleCatalogRefresh(w http.ResponseWriter, r *http.Request) {
	gbapi.Handle(gbDeps(h), "handleCatalogRefresh")(w, r)
}

func (h *Handler) handleInviteChannel(w http.ResponseWriter, r *http.Request) {
	gbapi.Handle(gbDeps(h), "handleInviteChannel")(w, r)
}

func (h *Handler) handleByeChannel(w http.ResponseWriter, r *http.Request) {
	gbapi.Handle(gbDeps(h), "handleByeChannel")(w, r)
}

func (h *Handler) handlePTZChannel(w http.ResponseWriter, r *http.Request) {
	gbapi.Handle(gbDeps(h), "handlePTZChannel")(w, r)
}

func (h *Handler) handleChannelDeviceControl(w http.ResponseWriter, r *http.Request) {
	gbapi.Handle(gbDeps(h), "handleChannelDeviceControl")(w, r)
}

func (h *Handler) handleChannelRecords(w http.ResponseWriter, r *http.Request) {
	gbapi.Handle(gbDeps(h), "handleChannelRecords")(w, r)
}

func (h *Handler) handleChannelPlaybackStart(w http.ResponseWriter, r *http.Request) {
	gbapi.Handle(gbDeps(h), "handleChannelPlaybackStart")(w, r)
}

func (h *Handler) handleChannelPlaybackStatus(w http.ResponseWriter, r *http.Request) {
	gbapi.Handle(gbDeps(h), "handleChannelPlaybackStatus")(w, r)
}

func (h *Handler) handleChannelPlaybackStop(w http.ResponseWriter, r *http.Request) {
	gbapi.Handle(gbDeps(h), "handleChannelPlaybackStop")(w, r)
}

func (h *Handler) handleChannelPlaybackControl(w http.ResponseWriter, r *http.Request) {
	gbapi.Handle(gbDeps(h), "handleChannelPlaybackControl")(w, r)
}

func (h *Handler) handleGB28181TalkWS(w http.ResponseWriter, r *http.Request) {
	gbapi.Handle(gbDeps(h), "handleGB28181TalkWS")(w, r)
}

func (h *Handler) handleGB28181TalkStatus(w http.ResponseWriter, r *http.Request) {
	gbapi.Handle(gbDeps(h), "handleGB28181TalkStatus")(w, r)
}

func (h *Handler) mapGB28181PTZError(w http.ResponseWriter, err error) {
	gbapi.MapGB28181PTZError(gbDeps(h), w, err)
}

func handleONVIFPTZError(w http.ResponseWriter, cameraID string, err error) {
	gbapi.HandleONVIFPTZError(w, cameraID, err)
}

func handleONVIFImagingError(w http.ResponseWriter, cameraID string, err error) {
	gbapi.HandleONVIFImagingError(w, cameraID, err)
}

func handleONVIFSnapshotError(w http.ResponseWriter, cameraID string, err error) {
	gbapi.HandleONVIFSnapshotError(w, cameraID, err)
}

func handleONVIFDeviceMgmtError(w http.ResponseWriter, cameraID string, err error) {
	gbapi.HandleONVIFDeviceMgmtError(w, cameraID, err)
}

func (h *Handler) handleGB28181PTZGetPresets(w http.ResponseWriter, r *http.Request, cameraID string) {
	gbapi.HandleGB28181PTZGetPresets(gbDeps(h), w, r, cameraID)
}

func (h *Handler) handleGB28181PTZCreatePreset(w http.ResponseWriter, r *http.Request, cameraID string) {
	gbapi.HandleGB28181PTZCreatePreset(gbDeps(h), w, r, cameraID)
}

func (h *Handler) handleGB28181PTZGoToPreset(w http.ResponseWriter, r *http.Request, cameraID, token string) {
	gbapi.HandleGB28181PTZGoToPreset(gbDeps(h), w, r, cameraID, token)
}

func (h *Handler) handleGB28181PTZDeletePreset(w http.ResponseWriter, r *http.Request, cameraID, token string) {
	gbapi.HandleGB28181PTZDeletePreset(gbDeps(h), w, r, cameraID, token)
}

func gbPTZFromVector(pan, tilt, zoom float64) (string, byte) {
	return gbapi.GBPTZFromVector(pan, tilt, zoom)
}

// PTZ suppress windows mirrored for the remaining in-package tests (source of
// truth lives in gbapi).
const (
	ptzSuppressWindowMove  = 30 * time.Second
	ptzSuppressWindowShort = 8 * time.Second
)

func isSOAPFaultStatus(errStr string) bool {
	return gbapi.IsSOAPFaultStatus(errStr)
}
