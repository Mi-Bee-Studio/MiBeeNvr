package recordingsapi

import (
	"context"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/go-chi/chi/v5"
)

// Register mounts the recording domain on the authed router. Endpoints are
// registered flat except the /api/recordings and /api/archives trees, which
// no other domain mounts (chi forbids mounting the same pattern twice).
func Register(r chi.Router, deps Deps) {
	s := &server{deps: deps}

	// --- recordings ---
	r.Route("/api/recordings", func(r chi.Router) {
		r.Get("/", s.handleListRecordings)
		r.Get("/daily-summary", s.handleDailyRecordingSummary)
		r.Get("/timeline", s.handleTimelineSegments)
		r.Post("/", s.handleCreateRecording)
		r.Post("/timeline/seek-event", s.handleTimelineSeekEvent)
		r.Post("/batch-delete", s.handleBatchDeleteRecordings)
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", s.handleGetRecording)
			r.Delete("/", s.handleDeleteRecording)
			r.Patch("/", s.handleUpdateRecording)
			r.Patch("/ai-status", s.handleUpdateRecordingAIStatus)
			r.Get("/frames", s.handleListFrames)
			r.Get("/playback", s.handlePlayback)
			r.Get("/timelapse-frames", s.handleTimelapseFrames)
			// Batch endpoint registered BEFORE the {filename} wildcard.
			r.Get("/timelapse-frames/batch", s.handleTimelapseFramesBatch)
			r.Get("/timelapse-frames/{filename}", s.handleTimelapseFrame)
			r.Post("/retry-merge", s.handleRetryTimelapseMerge)
			r.Post("/repair", s.handleRepairRecording)
		})
	})
	// Recording gaps for the timeline (per-camera).
	r.Get("/api/cameras/{id}/timeline/gaps", s.handleTimelineGaps)

	// --- merge management ---
	r.Get("/api/merge/status", s.handleMergeStatus)
	r.Get("/api/merge/pending", s.handleMergePending)
	r.Post("/api/merge/reclassify", s.handleMergeReclassify)
	r.Post("/api/merge/backfill", s.handleMergeBackfillAll)
	r.Post("/api/merge/consolidate", s.handleMergeConsolidate)
	r.Post("/api/cameras/{id}/merge/backfill", s.handleMergeBackfillCamera)

	// --- timelapse / periodic merges ---
	// Periodic-merge outputs (timelapse_merges table). Registered BEFORE the
	// /api/timelapse/{id} wildcard routes so the static /merges paths win.
	r.Get("/api/timelapse", s.handleTimelapseList)
	r.Get("/api/timelapse/status", s.handleTimelapseStatus)
	r.Post("/api/timelapse/batch-merge", s.handleTimelapseBatchMerge)
	r.Get("/api/timelapse/merges", s.handleListTimelapseMerges)
	r.Get("/api/timelapse/merges/{id}", s.handleGetTimelapseMerge)
	r.Get("/api/timelapse/merges/{id}/frames", s.handleTimelapseMergeFrames)
	// Merge-output download: PROTECTED (recording/merge IDs are UnixNano-
	// predictable — unauthenticated media access must 401; the SPA reaches it
	// via ?token= links, HA via BasicAuth). The registerTimelapseRoutes NOTE
	// claiming anonymous placement was stale — the auth contract test is
	// authoritative.
	r.Get("/api/timelapse/merges/{id}/download", s.handleDownloadTimelapseMerge)
	r.Head("/api/timelapse/merges/{id}/download", s.handleDownloadTimelapseMerge)
	r.Delete("/api/timelapse/merges/{id}", s.handleDeleteTimelapseMerge)
	r.Post("/api/timelapse/{id}/merge", s.handleTimelapseMerge)
	r.Delete("/api/timelapse/{id}/merge", s.handleTimelapseMergeCancel)
	r.Post("/api/timelapse/{id}/pause", s.handleTimelapsePause)
	r.Post("/api/timelapse/{id}/resume", s.handleTimelapseResume)
	r.Get("/api/timelapse/{id}", s.handleTimelapseGet)
	r.Delete("/api/timelapse/{id}", s.handleTimelapseDelete)
	r.Post("/api/timelapse/{id}/download", s.handleTimelapseDownload)
	r.Get("/api/timelapse/merge/progress/{id}", s.handleTimelapseMergeProgress)

	// --- archives ---
	r.Route("/api/archives", func(r chi.Router) {
		r.Get("/", s.handleListArchives)
		r.Get("/{id}/recordings", s.handleListArchiveRecordings)
		r.Get("/cleanup-status", s.handleGetCleanupStatus)
		r.Delete("/{id}", s.handleDeleteArchiveGroup)
		r.Delete("/{id}/recordings/{recordingID}", s.handleDeleteArchiveRecording)
		r.Put("/{id}/retention", s.handleSetArchiveRetention)
	})

	// --- merge settings (system panel section backed by the merge domain) ---
	r.Get("/api/settings/merge", s.handleGetMergeSettings)
	r.Put("/api/settings/merge", s.handleUpdateMergeSettings)

	// --- per-camera merge config + timelapse config (recording-domain
	// semantics living under the camera tree) ---
	r.Get("/api/cameras/{id}/merge-config", s.handleGetCameraMergeConfig)
	r.Put("/api/cameras/{id}/merge-config", s.handleUpdateCameraMergeConfig)
	r.Delete("/api/cameras/{id}/merge-config", s.handleDeleteCameraMergeConfig)
	r.Get("/api/cameras/{id}/timelapse", s.handleGetCameraTimelapse)
	r.Put("/api/cameras/{id}/timelapse", s.handlePutCameraTimelapse)

	// --- media serving (same auth class as live HLS: bearer/basic/api-key/
	// stream cookie/?token=) ---
	r.Get("/api/recordings/{id}/download", s.handleDownloadRecording)
	r.Head("/api/recordings/{id}/download", s.handleDownloadRecording)
	// Audio-only recordings (v43): G.711 → WAV pure-Go transcode, per-IP
	// rate-limited on top (CPU work — a scrubbing client must not DoS
	// playback). The rate limiter mirrors the pre-extraction media group.
	r.Group(func(r chi.Router) {
		rl := middleware.NewRateLimiter(context.Background(), middleware.RateLimiterConfig{
			MaxRequests: 60,
			Window:      time.Minute,
		})
		r.Use(rl.Handler)
		r.Get("/api/recordings/{id}/audio.wav", s.handleAudioWav)
		r.Head("/api/recordings/{id}/audio.wav", s.handleAudioWav)
	})
	// Audio access audit trail (compliance).
	r.Get("/api/recordings/audio-audit", s.handleAudioAudit)
	r.Get("/api/recordings/{id}/merged", s.handleMergedRecording)
	r.Head("/api/recordings/{id}/merged", s.handleMergedRecording)
	r.Get("/api/cameras/{cameraID}/playback/playlist.m3u8", s.handlePlaybackPlaylist)
	r.Get("/api/cameras/{cameraID}/playback/{recordingID}/{segName}", s.handlePlaybackSegment)
}
