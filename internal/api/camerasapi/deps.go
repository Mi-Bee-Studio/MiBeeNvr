// Package camerasapi serves the camera-management domain: CRUD + probe +
// lifecycle (start/stop/activate/rediscover/adaptive trigger), grouping,
// thermal readouts, trace sampling, time sync, xiaomi devices (incl. the
// audio-upstream WS), zero-touch onboarding, the webhook trigger, and the
// per-camera SSE event stream.
//
// Extraction pattern per docs/architecture/api-package-split.md: a local Deps
// holding what the handlers touch, plus Register. The ptz/onvif/gb-talk
// children of the /api/cameras/{id} tree belong to the gb domain and are
// registered flat from there (chi forbids mounting the same pattern twice);
// the recordings/streaming domains already own their flat children.
package camerasapi

import (
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/wsstream"
)

// Deps is what this domain needs from the API assembly.
type Deps struct {
	DB     *storage.DB
	Config *config.Config
	Cam    *camera.CameraManager
	// MergeScheduler re-registers per-camera periodic merges after camera
	// create/update/delete (#478 batch).
	MergeScheduler *timelapse.MergeScheduler
	// WS backs the xiaomi audio-upstream endpoint.
	WS *wsstream.Manager
	// EventBus feeds the per-camera SSE stream. Nil → 503.
	EventBus *event.EventBus
	// CloudProxy is the Xiaomi cloud auth proxy. Nil → xiaomi auth endpoints
	// degrade.
	CloudProxy CloudAuthProxy
	// Onboarding is the zero-touch onboarding service. Nil → 409.
	Onboarding OnboardingService
	// TimeSync is the camera time-sync service. Nil → 503.
	TimeSync TimeSyncOps
	// TriggerDispatcher maps a trigger action (record/stop/snapshot) to
	// camera lifecycle ops (#709) — shared with the MQTT client.
	TriggerDispatcher func(cameraID, action string, duration time.Duration)
	// XiaomiPTZMotor overrides Xiaomi motor dispatch when set (tests).
	XiaomiPTZMotor func(cameraID, direction string, speed int) error
	// ShutdownSignaled reports the process-wide stream-shutdown channel
	// (SSE loops stop on process teardown).
	ShutdownSignaled func() <-chan struct{}
	// GBDeviceAddrs snapshots the connected GB28181 devices' network
	// identities (create-time cross-protocol dedup). Nil = GB disabled.
	GBDeviceAddrs func() []GBDeviceNet
	// CameraHealth snapshots one camera's health record. Nil → 404.
	CameraHealth func(cameraID string) any
	// ConfigPath backs the xiaomi token persistence path.
	ConfigPath string
}

// GBDeviceNet is the per-device network identity the create-time dedup needs.
type GBDeviceNet struct {
	ID      string
	NetAddr string
}

// server carries the Deps for the handler methods.
type server struct {
	deps Deps
}
