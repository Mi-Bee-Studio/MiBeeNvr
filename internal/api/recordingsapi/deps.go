// Package recordingsapi serves the recording domain: the recordings list /
// detail / frames / playback / repair surface, merged-output serving, the
// merge + timelapse (periodic merge) management endpoints, archives, audio
// WAV serving with its access audit, and the media download paths.
//
// Extraction pattern per docs/architecture/api-package-split.md: a local Deps
// holding what the handlers touch, plus Register (authed group) and
// RegisterPublic (the anonymous timelapse-merge download pair).
package recordingsapi

import (
	"net/http"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recframes"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/vod"
)

// Deps is what this domain needs from the API assembly. Structural managers
// are concrete pointers (always wired by NewHandler; handlers keep their
// existing nil checks); optional capabilities are func fields.
type Deps struct {
	DB         *storage.DB
	Store      *storage.Manager
	Config     *config.Config
	ConfigPath string
	Cam        *camera.CameraManager

	Merge          *merge.MergeManager
	RollingMerge   *merge.RollingMergeCoordinator
	TimelapseMerge *timelapse.RollingMergeManager
	MergeScheduler *timelapse.MergeScheduler

	RecFrames *recframes.Service
	VOD       *vod.Manager

	// ServeFileBudgeted serves a file through the shared I/O budget's
	// "playback" tenant (#886) — the limiter lives on the api Handler.
	ServeFileBudgeted func(w http.ResponseWriter, r *http.Request, path string)
	// RecordTimelineSeek feeds the api metrics shim (timeline seek counter).
	// Nil = metrics not wired.
	RecordTimelineSeek func(cameraID, seekType string)
	// TriggerManualMerge / TriggerBatchMerge drive the manual periodic-merge
	// trigger (#1009 batch 3) whose goroutine lifecycle lives on the api
	// Handler (Close joins them). Nil = manual merges unavailable (503).
	TriggerManualMerge func(cameraID, durationStr, dateStr string) (time.Time, error)
	TriggerBatchMerge  func(cameraIDs []string, durationStr, dateStr string) ([]timelapse.BatchResult, error)
	// ShutdownSignaled reports the process-wide stream-shutdown channel.
	ShutdownSignaled func() <-chan struct{}
}

// server carries the Deps for the handler methods.
type server struct {
	deps Deps
}
