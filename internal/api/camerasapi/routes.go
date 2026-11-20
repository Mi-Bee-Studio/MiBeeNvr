package camerasapi

import (
	"context"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/go-chi/chi/v5"
)

// Register mounts the camera-management domain on the authed router.
// The /api/cameras mount carries CRUD + groups + the {id} management
// children that belong to this domain; the ptz/onvif/gb-talk children are
// registered flat by the gb domain, and the recordings/streaming domains own
// their flat children (merge-config/timelapse config, live egress, stills).
func Register(r chi.Router, deps Deps) {
	s := &server{deps: deps}

	r.Route("/api/cameras", func(r chi.Router) {
		r.Get("/", s.handleListCameras)
		r.Post("/", s.handleCreateCamera)
		r.Post("/test-connection", s.handleTestConnection)
		// Group registry (v37) — empty-group persistence + rename/delete that
		// walk the member cameras. /groups/order (v38) is registered before
		// the /{name} param route; chi prefers the literal.
		r.Route("/groups", func(r chi.Router) {
			r.Get("/", s.handleListCameraGroups)
			r.Post("/", s.handleCreateCameraGroup)
			r.Put("/order", s.handleSetCameraGroupsOrder)
			r.Put("/{name}", s.handleRenameCameraGroup)
			r.Delete("/{name}", s.handleDeleteCameraGroup)
		})
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", s.handleGetCamera)
			r.Put("/", s.handleUpdateCamera)
			r.Delete("/", s.handleDeleteCamera)
			// Dual-spectrum spot-temperature grid (proxied device readout)
			r.Get("/thermal/frame", s.handleThermalFrame)
			// P4 temperature history (minute-aggregated, playback curve)
			r.Get("/thermal/history", s.handleThermalHistory)
			// Per-camera frame-trace sampling window (#482)
			r.Get("/trace", s.handleCameraTrace)
			r.Post("/trace", s.handleCameraTrace)
			r.Delete("/trace", s.handleCameraTrace)
			// Camera time-sync (#time-sync): status read is credential-free;
			// the two write endpoints need camera admin credentials.
			r.Get("/time", s.handleCameraTimeStatus)
			r.Post("/time/sync", s.handleCameraTimeSync)
			r.Post("/time/ntp", s.handleCameraTimeNTP)
			r.Get("/stats", s.handleCameraRecordingStats)
			// Camera-specific events (SSE)
			r.Get("/events", s.handleCameraEvents)
			r.Post("/start", s.handleStartCamera)
			r.Post("/stop", s.handleStopCamera)
			// Activate a pending_activation camera: supply credentials and start
			// its recorder. Used by the auto-discover flow.
			r.Post("/activate", s.handleActivateCamera)
			// Manually trigger IP self-healing for a camera whose address changed.
			r.Post("/rediscover", s.handleRediscoverCamera)
			// External adaptive-recording trigger (issue #478).
			r.Post("/adaptive/trigger", s.handleAdaptiveTrigger)
			// Camera-side ONVIF motion subscription diagnostics (#711).
			r.Get("/onvif-events", s.handleONVIFEventsStatus)
			// Xiaomi-specific PTZ and device info endpoints
			r.Route("/xiaomi", func(r chi.Router) {
				r.Post("/ptz/move", s.handleXiaomiPTZMove)
				r.Post("/ptz/stop", s.handleXiaomiPTZStop)
				r.Get("/device-info", s.handleXiaomiDeviceInfo)
				// Xiaomi two-way audio endpoints
				r.Post("/two-way-audio/start", s.handleStartTwoWayAudio)
				r.Post("/two-way-audio/stop", s.handleStopTwoWayAudio)
			})
		})
	})
	// Push-out relay status (per-camera) — flat route, kept with cameras
	r.Get("/api/cameras/{id}/push-status", s.handleCameraPushStatus)
	// Per-camera health (flat route)
	r.Get("/api/cameras/{id}/health", s.handleGetCameraHealth)

	// Xiaomi cloud devices
	r.Route("/api/xiaomi", func(r chi.Router) {
		r.Post("/auth", s.handleXiaomiAuth)
		r.Post("/captcha", s.handleXiaomiCaptcha)
		r.Post("/verify", s.handleXiaomiVerify)
		r.Get("/devices", s.handleXiaomiDevices)
		r.Post("/sync", s.handleXiaomiSync)
		r.Get("/check-vendor", s.handleCheckVendor)
	})
	// Xiaomi audio upstream WS
	r.Get("/api/ws/camera/{id}/audio-upstream", s.handleAudioUpstreamWS)

	// Live listening: per-IP rate-limited group (mirrors the pre-extraction
	// media group — the WAV transcode is CPU work; sessions are long-lived
	// and one request each, so the limiter never bites mid-stream).
	r.Group(func(r chi.Router) {
		rl := middleware.NewRateLimiter(context.Background(), middleware.RateLimiterConfig{
			MaxRequests: 60,
			Window:      time.Minute,
		})
		r.Use(rl.Handler)
		r.Get("/api/cameras/{cameraID}/audio/live.wav", s.handleAudioLiveWav)
		r.Get("/api/cameras/{cameraID}/audio/live", s.handleAudioLive)
	})

	// Zero-touch onboarding
	r.Get("/api/onboarding/status", s.handleOnboardingStatus)
	r.Post("/api/onboarding/scan", s.handleOnboardingScan)
	r.Post("/api/onboarding/adopt", s.handleOnboardingAdopt)
	r.Get("/api/onboarding/jobs/{id}", s.handleOnboardingJob)
}

// RegisterPublic mounts the webhook trigger (public group, rate-limited —
// HMAC signature is the credential, #709). No-op when disabled/unconfigured.
func RegisterPublic(r chi.Router, deps Deps) {
	if deps.Config == nil || !deps.Config.Trigger.Webhook.Enabled {
		return
	}
	if len(deps.Config.Trigger.Webhook.Secret) == 0 {
		// Validate() rejects this combination at load; guard anyway so a
		// hand-wired Deps in tests can't mount an unverifiable endpoint.
		return
	}
	s := &server{deps: deps}
	r.Post("/api/trigger/webhook/{cameraID}", s.handleTriggerWebhook)
}
