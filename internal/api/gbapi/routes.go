package gbapi

import (
	"github.com/go-chi/chi/v5"
)

// Register mounts the GB28181/ONVIF domain on the authed router: the GB
// platform tree, the ONVIF discovery/probe endpoints, and the camera-tree
// children that belong to this domain (ptz/onvif/gb-talk — registered flat;
// chi forbids mounting the same pattern twice).
func Register(r chi.Router, deps Deps) {
	s := &server{deps: deps}

	r.Route("/api/gb28181", func(r chi.Router) {
		r.Get("/devices", s.handleListGB28181Devices)
		r.Get("/cascade/status", s.handleGB28181CascadeStatus)
		r.Route("/devices/{id}", func(r chi.Router) {
			r.Get("/channels", s.handleListGB28181Channels)
			r.Post("/catalog-refresh", s.handleCatalogRefresh)
			r.Get("/alarms", s.handleGB28181DeviceAlarms)
			r.Get("/positions", s.handleGB28181DevicePositions)
		})
		r.Route("/channels/{id}", func(r chi.Router) {
			r.Post("/invite", s.handleInviteChannel)
			r.Post("/bye", s.handleByeChannel)
			r.Post("/ptz", s.handlePTZChannel)
			r.Post("/lens", s.handleChannelLensControl)
			r.Post("/aux-switch", s.handleChannelAuxSwitch)
			r.Post("/control", s.handleChannelDeviceControl)
			r.Get("/records", s.handleChannelRecords)
			r.Post("/playback", s.handleChannelPlaybackStart)
			r.Get("/playback", s.handleChannelPlaybackStatus)
			r.Delete("/playback", s.handleChannelPlaybackStop)
			r.Post("/playback/control", s.handleChannelPlaybackControl)
			r.Post("/download", s.handleChannelDownloadStart)
			r.Get("/download", s.handleChannelPlaybackStatus)
			r.Delete("/download", s.handleChannelPlaybackStop)
			// GB/T 28181-2022 on-demand capture (#708)
			r.Post("/snapshot", s.handleGB28181ChannelSnapshot)
			r.Post("/record", s.handleGB28181ChannelRecord)
		})
	})

	r.Post("/api/onvif/discover", s.handleONVIFDiscover)
	r.Get("/api/onvif/discover/{ip}", s.handleONVIFDeviceDetail)
	r.Post("/api/onvif/probe", s.handleONVIFProbe)

	// --- children of the /api/cameras/{id} tree owned by this domain ---
	// GB28181 voice intercom (talk): WS ingest + status (#341)
	r.Get("/api/cameras/{id}/gb28181/talk", s.handleGB28181TalkWS)
	r.Get("/api/cameras/{id}/gb28181/talk/status", s.handleGB28181TalkStatus)
	r.Get("/api/cameras/{id}/onvif/profiles", s.handleONVIFCameraProfiles)
	r.Get("/api/cameras/{id}/onvif/capabilities", s.handleONVIFCapabilities)
	r.Post("/api/cameras/{id}/ptz/move", s.handlePTZMove)
	r.Post("/api/cameras/{id}/ptz/stop", s.handlePTZStop)
	r.Get("/api/cameras/{id}/ptz/status", s.handlePTZStatus)
	r.Get("/api/cameras/{id}/ptz/presets", s.handlePTZGetPresets)
	r.Post("/api/cameras/{id}/ptz/presets", s.handlePTZCreatePreset)
	r.Post("/api/cameras/{id}/ptz/presets/{token}/goto", s.handlePTZGoToPreset)
	r.Delete("/api/cameras/{id}/ptz/presets/{token}", s.handlePTZDeletePreset)
	r.Get("/api/cameras/{id}/snapshot/uri", s.handleSnapshotGetUri)
	r.Get("/api/cameras/{id}/imaging/settings", s.handleImagingGetSettings)
	r.Put("/api/cameras/{id}/imaging/settings", s.handleImagingSetSettings)
	r.Get("/api/cameras/{id}/imaging/options", s.handleImagingGetOptions)
	// Device management
	r.Post("/api/cameras/{id}/onvif/reboot", s.handleONVIFReboot)
	r.Get("/api/cameras/{id}/onvif/network", s.handleONVIFGetNetwork)
	r.Put("/api/cameras/{id}/onvif/network", s.handleONVIFSetNetwork)
	r.Get("/api/cameras/{id}/onvif/users", s.handleONVIFGetUsers)
	r.Post("/api/cameras/{id}/onvif/users", s.handleONVIFCreateUsers)
	r.Delete("/api/cameras/{id}/onvif/users", s.handleONVIFDeleteUsers)
	r.Put("/api/cameras/{id}/onvif/users/{username}", s.handleONVIFSetUser)
}

// RegisterPublic mounts the GB28181 snapshot-upload callback (public group,
// rate-limited — the session ID in the URL is the credential; devices cannot
// BasicAuth, #708). No-op when the snapshot manager is absent.
func RegisterPublic(r chi.Router, deps Deps) {
	if deps.SnapMgr == nil {
		return
	}
	s := &server{deps: deps}
	r.Post("/api/gb28181/snapshot/upload", s.handleGB28181SnapshotUpload)
}
