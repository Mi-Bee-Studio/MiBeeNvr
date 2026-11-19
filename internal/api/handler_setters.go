package api

// Dependency-injection setters on Handler. The handler is constructed with the
// core managers in NewHandler; optional subsystems (streaming managers, health,
// vision, relay, timelapse, API-key store, WHIP) are attached afterwards by the
// app wiring. Split out of handler.go so the constructor and route table stay
// readable.

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/flv"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/relay"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/vision"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/webrtc"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/whip"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/wsstream"
)

func (h *Handler) SetWebRTCManager(mgr *webrtc.Manager) {
	h.webrtcMgr = mgr
}

// SetFLVManager sets the FLV manager on the handler.
func (h *Handler) SetFLVManager(mgr *flv.Manager) {
	h.flvMgr = mgr
}

// SetWSManager sets the WebSocket stream manager on the handler.
func (h *Handler) SetWSManager(mgr *wsstream.Manager) {
	h.wsMgr = mgr
}

// ArmFirstBootSetup generates and stores the one-time setup code and returns
// it for the caller to print to the terminal/log (#879).
func (h *Handler) ArmFirstBootSetup() (string, error) {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	h.setupCode = hex.EncodeToString(buf)
	return h.setupCode, nil
}

// SetHealthManager sets the health manager on the handler.
func (h *Handler) SetHealthManager(mgr HealthManager) {
	h.healthMgr = mgr
}

// SetStabilityProvider sets the stability data provider on the handler.
func (h *Handler) SetStabilityProvider(p StabilityProvider) {
	h.stabilityProvider = p
}

// SetDownloader sets the FFmpeg downloader on the handler.
func (h *Handler) SetDownloader(d TranscodeDownloader) {
	h.downloader = d
}

// SetSnapshotCapturer wires the FFmpeg-gated snapshot capturer used by the
// latest-frame endpoint for H.264/H.265 cameras (#657). Without it the
// endpoint keeps its JPEG-recorder-only behavior.
func (h *Handler) SetSnapshotCapturer(c SnapshotCapturer) {
	h.snapCapturer = c
}

// HasSnapshotCapturer reports whether the snapshot capturer is wired —
// observable wiring guard for the pkg/app regression tests.
func (h *Handler) HasSnapshotCapturer() bool {
	return h.snapCapturer != nil
}

// SetEventBus sets the event bus on the handler.
func (h *Handler) SetEventBus(bus *event.EventBus) {
	h.eventBus = bus
}

// SetTimelapseMergeMgr sets the timelapse rolling merge manager on the handler.
func (h *Handler) SetTimelapseMergeMgr(mgr *timelapse.RollingMergeManager) {
	h.timelapseMergeMgr = mgr
}

// SetRollingMergeMgr sets the recording rolling merge coordinator on the handler.
func (h *Handler) SetRollingMergeMgr(mgr *merge.RollingMergeCoordinator) {
	h.rollingMergeMgr = mgr
}

// SetVisionCoordinator sets the Vision push coordinator on the handler.
func (h *Handler) SetVisionCoordinator(mgr *vision.Coordinator) {
	h.visionCoordinator = mgr
}

// SetWHIPServer wires the WHIP push-in ingest server. When set, the anonymous
// route group mounts /whip/{streamKey} and capabilities reports it (#369).
func (h *Handler) SetWHIPServer(s *whip.Server) {
	h.whipServer = s
}

// SetAPIKeyStore wires the live API key store. The generate/revoke handlers
// keep it in sync with the config so key changes apply on the next request
// without a service restart (#335); last-used timestamps surface in the key
// list API.
func (h *Handler) SetAPIKeyStore(s *middleware.APIKeyStore) {
	h.apiKeyStore = s
}

// SetAIHandler sets the AI handler on the Handler.
func (h *Handler) SetAIHandler(ah *AIHandler) {
	h.aiHandler = ah
}

// SetTimelapseSourceDeleter wires the deleter used by the opt-in
// delete_recordings_after_merge behavior in manual merges. Wired
// post-construction because the cleanup manager is built after the
// handler in app assembly.
func (h *Handler) SetTimelapseSourceDeleter(d timelapse.SourceRecordingDeleter) {
	h.tlTrigger.SetSourceRecordingDeleter(d)
}

// HasTimelapseSourceDeleter reports whether the timelapse source deleter is
// wired. Exposed for wiring regression tests.
func (h *Handler) HasTimelapseSourceDeleter() bool {
	return h.tlTrigger.HasSourceRecordingDeleter()
}

// SetRelayManager wires the relay manager for the relay-presets endpoints.
func (h *Handler) SetRelayManager(mgr *relay.Manager) {
	h.relayMgr = mgr
}

// --- Per-camera streaming protocols endpoint ---
