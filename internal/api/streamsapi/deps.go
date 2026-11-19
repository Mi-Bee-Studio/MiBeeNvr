// Package streamsapi serves the live-media egress domain: per-camera live
// streams (WS/WebCodecs, HTTP-FLV, HLS, WHEP/WebRTC, MJPEG), camera stills
// (snapshot / latest-frame), the protocol-capability and flow observability
// endpoints, and the StreamRegistry that maps codecs to available protocols.
//
// Extraction pattern per docs/architecture/api-package-split.md: a local Deps
// holding what the handlers touch, plus Register. The API root (internal/api)
// keeps aggregation + dependency injection; this package owns the handlers.
package streamsapi

import (
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/flv"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/hls"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/webrtc"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/wsstream"
)

// Deps is what this domain needs from the API assembly. Structural managers
// are concrete pointers (they are always wired by NewHandler and the handlers
// nil-check them directly, preserving today's semantics); optional capabilities
// are func/interface fields (nil = feature absent, endpoints degrade).
type Deps struct {
	DB     *storage.DB
	Config *config.Config
	Cam    *camera.CameraManager

	HLS    *hls.Manager
	FLV    *flv.Manager
	WS     *wsstream.Manager
	WebRTC *webrtc.Manager

	// Merge feeds the flow view's per-camera rolling-merge backlog. Nil = no
	// merge manager (counts omitted).
	Merge *merge.MergeManager
	// Registry answers the protocol-capability queries. Nil = no protocols
	// advertised (empty list).
	Registry *StreamRegistry
	// SnapStore owns the shared snapshot/latest-frame TTL cache. Nil = a
	// private empty store is created at registration.
	SnapStore *SnapshotStore
	// GB28181Sessions feeds the flow view's GB28181 section (live sessions +
	// watchdog recycle count). Nil = section omitted.
	GB28181Sessions func() ([]GB28181SessionStatus, int64)
}

// server carries the Deps plus the domain-owned mutable state (the shared
// snapshot cache) for the handler methods.
type server struct {
	deps      Deps
	snapStore *SnapshotStore
}

// GB28181SessionStatus mirrors the GB library's session observability for the
// flow view. Lives here (flow-response data); the GB domain and the API root
// alias it.
// GB28181SessionStatus mirrors the GB library's session observability for the
// flow view (live sessions + stall watchdog recycles). Lives here — flow
// response data; the GB domain and the API root alias it.
type GB28181SessionStatus struct {
	ChannelID       string `json:"channel_id"`
	Classification  string `json:"classification"`
	PacketsReceived int64  `json:"packets_received"`
	AUEmitted       int64  `json:"au_emitted"`
	AudioFrames     int64  `json:"audio_frames_emitted"`
	Stalled         bool   `json:"stalled"`
}
