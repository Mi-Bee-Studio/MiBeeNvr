// Package gbapi serves the GB28181 + ONVIF domain: the GB platform
// management endpoints (devices/channels/records/playback/snapshot/talk),
// the unified PTZ surface (ONVIF/GB/Xiaomi dispatch), ONVIF device
// management (imaging/users/network/reboot), and the public snapshot-upload
// callback.
//
// Extraction pattern shared by the api domain packages: a local Deps
// holding what the handlers touch, plus Register (authed group) and
// RegisterPublic (the snapshot-upload callback, whose URL session ID is the
// credential).
package gbapi

import (
	"net/http"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/mickeyzzc/gb28181-go/platform"
)

// Deps is what this domain needs from the API assembly. The platform
// managers are concrete pointers (always wired when GB is enabled; handlers
// keep their existing nil checks).
type Deps struct {
	DB     *storage.DB
	Config *config.Config
	Cam    *camera.CameraManager

	DeviceMgr *platform.DeviceManager
	Records   *gb28181.DeviceRecordService
	SnapMgr   *gb28181.SnapshotSessionManager

	PTZ     *platform.PTZController
	Catalog *platform.CatalogController

	Inviter   GB28181InviteSender
	Bye       GB28181ByeSender
	Cascade   GB28181CascadeStatus
	Commander GBCommander
	Media     GB28181DeviceMedia

	// PTZSuppress blinds the pixgate pixel gate while a PTZ command moves
	// the camera. Nil = no suppression hook.
	PTZSuppress func(cameraID string, window time.Duration)
	// Xiaomi dispatch hooks — the unified PTZ surface routes Xiaomi cameras
	// into the cameras domain (wired from the api root). Nil = no xiaomi
	// support wired.
	XiaomiVectorMove    func(w http.ResponseWriter, cameraID string, pan, tilt, zoom float64)
	XiaomiMotorCommand  func(cameraID, direction string, speed int) error
	XiaomiPTZStatusCode func(err error) int
}

// server carries the Deps for the handler methods.
type server struct {
	deps Deps
}
