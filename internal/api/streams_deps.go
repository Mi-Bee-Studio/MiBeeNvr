package api

import (
	"net/http"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/api/streamsapi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/wsstream"
)

// streamsDeps adapts the Handler's fields to the streamsapi domain Deps. All
// managers are concrete pointers here — the domain handlers carry the same
// nil checks the pre-extraction code had, so a nil *camera.CameraManager must
// stay a nil pointer (it does: fields are concrete types, not interfaces).
func streamsDeps(h *Handler) streamsapi.Deps {
	return streamsapi.Deps{
		DB:              h.db,
		Config:          h.config,
		Cam:             h.camMgr,
		HLS:             h.hlsMgr,
		FLV:             h.flvMgr,
		WS:              h.wsMgr,
		WebRTC:          h.webrtcMgr,
		Merge:           h.mergeMgr,
		Registry:        h.streamRegistry,
		SnapStore:       h.snapStore,
		GB28181Sessions: h.gb28181Sessions,
	}
}

// Compat aliases for the streaming types that moved into streamsapi. The app
// layer (builders_http.go) and the remaining in-package route tests reference
// them via api.* — the aliases keep those call surfaces byte-stable. Delete
// as the callers migrate.
type (
	StreamRegistry          = streamsapi.StreamRegistry
	StreamHandler           = streamsapi.StreamHandler
	StreamStartOptions      = streamsapi.StreamStartOptions
	ConditionalHandler      = streamsapi.ConditionalHandler
	ProtocolDetail          = streamsapi.ProtocolDetail
	cameraProtocolsResponse = streamsapi.CameraProtocolsResponse
	HLSStreamHandler        = streamsapi.HLSStreamHandler
	LLHLSStreamHandler      = streamsapi.LLHLSStreamHandler
	WebRTCStreamHandler     = streamsapi.WebRTCStreamHandler
	FLVStreamHandler        = streamsapi.FLVStreamHandler
	WSStreamHandler         = streamsapi.WSStreamHandler
	MJPEGStreamHandler      = streamsapi.MJPEGStreamHandler
	SnapshotCapturer        = streamsapi.SnapshotCapturer
)

// NewStreamRegistry mirrors the moved constructor for the same compat reason.
func NewStreamRegistry() *StreamRegistry { return streamsapi.NewStreamRegistry() }

// SetStreamRegistry keeps the app-layer wiring surface (builders_http.go)
// unchanged after the streaming domain extraction; the registry itself lives
// in streamsapi.
func (h *Handler) SetStreamRegistry(reg *StreamRegistry) {
	h.streamRegistry = reg
}

// Transitional shims over the moved streaming helpers — the remaining
// in-package tests reference them; delete as those tests migrate to
// streamsapi (documented in api-package-split.md step 3).
func getCodecParams(rec model.Recorder) (model.Format, []byte, []byte, []byte) {
	return recorder.CodecParamsOf(rec)
}

func parseQuality(r *http.Request) (string, error) { return streamsapi.ParseQuality(r) }

func subKey(cameraID string) string { return streamsapi.SubKey(cameraID) }

// Quality negotiation constants mirrored for the remaining in-package tests
// (the source of truth lives in streamsapi).
const (
	qualityMain = "main"
	qualitySub  = "sub"
)

func setupAudioForWS(mgr *wsstream.Manager, id string, rec model.Recorder) {
	streamsapi.SetupAudioForWS(mgr, id, rec)
}
