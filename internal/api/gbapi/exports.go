package gbapi

import (
	"net/http"
)

// Handle runs the named domain handler against deps — the api root's GB tests
// call through it instead of the pre-extraction Handler methods. Names match
// the former method names.
func Handle(deps Deps, name string) http.HandlerFunc {
	s := &server{deps: deps}
	handlers := map[string]http.HandlerFunc{
		"handleListGB28181Devices":     s.handleListGB28181Devices,
		"handleListGB28181Channels":    s.handleListGB28181Channels,
		"handleGB28181CascadeStatus":   s.handleGB28181CascadeStatus,
		"handleGB28181DeviceAlarms":    s.handleGB28181DeviceAlarms,
		"handleGB28181DevicePositions": s.handleGB28181DevicePositions,
		"handleCatalogRefresh":         s.handleCatalogRefresh,
		"handleInviteChannel":          s.handleInviteChannel,
		"handleByeChannel":             s.handleByeChannel,
		"handlePTZChannel":             s.handlePTZChannel,
		"handleChannelDeviceControl":   s.handleChannelDeviceControl,
		"handleChannelRecords":         s.handleChannelRecords,
		"handleChannelPlaybackStatus":  s.handleChannelPlaybackStatus,
		"handleChannelPlaybackStop":    s.handleChannelPlaybackStop,
		"handleChannelPlaybackControl": s.handleChannelPlaybackControl,
		"handleGB28181TalkStatus":      s.handleGB28181TalkStatus,
	}
	return handlers[name]
}

// MapGB28181PTZError is the exported PTZ error mapper.
func MapGB28181PTZError(deps Deps, w http.ResponseWriter, err error) {
	(&server{deps: deps}).mapGB28181PTZError(w, err)
}

// HandleONVIFPTZError is the exported form of the package-level mapper.
func HandleONVIFPTZError(w http.ResponseWriter, cameraID string, err error) {
	handleONVIFPTZError(w, cameraID, err)
}

// HandleONVIFImagingError is the exported form of the package-level mapper.
func HandleONVIFImagingError(w http.ResponseWriter, cameraID string, err error) {
	handleONVIFImagingError(w, cameraID, err)
}

// HandleONVIFSnapshotError is the exported form of the package-level mapper.
func HandleONVIFSnapshotError(w http.ResponseWriter, cameraID string, err error) {
	handleONVIFSnapshotError(w, cameraID, err)
}

// HandleONVIFDeviceMgmtError is the exported form of the package-level mapper.
func HandleONVIFDeviceMgmtError(w http.ResponseWriter, cameraID string, err error) {
	handleONVIFDeviceMgmtError(w, cameraID, err)
}

// HandleGB28181PTZGetPresets is the exported preset-query variant (extra-arg
// signature — not reachable through Handle).
func HandleGB28181PTZGetPresets(deps Deps, w http.ResponseWriter, r *http.Request, cameraID string) {
	(&server{deps: deps}).handleGB28181PTZGetPresets(w, r, cameraID)
}

// HandleGB28181PTZCreatePreset is the exported preset-create variant.
func HandleGB28181PTZCreatePreset(deps Deps, w http.ResponseWriter, r *http.Request, cameraID string) {
	(&server{deps: deps}).handleGB28181PTZCreatePreset(w, r, cameraID)
}

// HandleGB28181PTZGoToPreset is the exported preset-goto variant.
func HandleGB28181PTZGoToPreset(deps Deps, w http.ResponseWriter, r *http.Request, cameraID, token string) {
	(&server{deps: deps}).handleGB28181PTZGoToPreset(w, r, cameraID, token)
}

// HandleGB28181PTZDeletePreset is the exported preset-delete variant.
func HandleGB28181PTZDeletePreset(deps Deps, w http.ResponseWriter, r *http.Request, cameraID, token string) {
	(&server{deps: deps}).handleGB28181PTZDeletePreset(w, r, cameraID, token)
}
