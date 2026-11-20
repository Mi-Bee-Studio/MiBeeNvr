package systemapi

import (
	"github.com/go-chi/chi/v5"
)

// Register mounts the system + transcoding domains on the authed router,
// flat (chi forbids mounting the same pattern twice). Paths whose handlers
// stayed at the api root (license/p2p/update/version/rtsp-output settings)
// remain registered there.
func Register(r chi.Router, deps Deps) {
	s := &server{deps: deps}

	r.Get("/api/stats", s.handleStats)
	r.Get("/api/stats/system", s.handleSystemStats)
	r.Get("/api/stats/trends", s.handleStatsTrends)
	r.Get("/api/stats/cameras", s.handleStatsCameras)
	r.Get("/api/settings", s.handleGetSettings)
	r.Put("/api/settings", s.handleUpdateSettings)
	r.Get("/api/storage/candidates", s.handleStorageCandidates)
	r.Post("/api/storage/candidates", s.handleAddStorageCandidate)
	r.Delete("/api/storage/candidates", s.handleRemoveStorageCandidate)
	r.Post("/api/storage/migrate", s.handleStartStorageMigrate)
	r.Get("/api/storage/migrate/status", s.handleStorageMigrateStatus)
	r.Get("/api/cameras/{id}/storage-root", s.handleGetCameraStorageRoot)
	r.Put("/api/cameras/{id}/storage-root", s.handleSetCameraStorageRoot)
	r.Post("/api/settings/api-keys", s.handleGenerateAPIKey)
	r.Delete("/api/settings/api-keys/{name}", s.handleRevokeAPIKey)
	// Streaming / auto-discover / transcoding sections (rtsp-output stays at
	// the root with its handler).
	r.Get("/api/settings/streaming", s.handleGetStreamingSettings)
	r.Put("/api/settings/streaming", s.handleUpdateStreamingSettings)
	r.Get("/api/settings/auto-discover", s.handleGetAutoDiscoverSettings)
	r.Put("/api/settings/auto-discover", s.handleUpdateAutoDiscoverSettings)
	r.Get("/api/settings/transcoding", s.handleGetTranscodingSettings)
	r.Put("/api/settings/transcoding", s.handleUpdateTranscodingSettings)
	r.Post("/api/backup", s.handleBackup)
	r.Get("/api/backups", s.handleListBackups)
	r.Get("/api/protocols", s.handleProtocols)
	r.Get("/api/features", s.handleGetFeatures)
	r.Put("/api/features", s.handleUpdateFeatures)

	// Transcoding subsystem (check/status/tasks/backfill/config listing).
	r.Get("/api/transcoding/check", s.handleTranscodingCheck)
	r.Get("/api/transcoding/ffmpeg/status", s.handleFFmpegStatus)
	r.Post("/api/transcoding/ffmpeg/download", s.handleFFmpegDownload)
	r.Post("/api/transcoding/ffmpeg/download/retry", s.handleFFmpegDownloadRetry)
	r.Get("/api/transcoding/status", s.handleTranscodingStatus)
	r.Get("/api/transcoding/tasks", s.handleTranscodingTasksList)
	r.Post("/api/transcoding/tasks", s.handleTranscodingTaskCreate)
	r.Delete("/api/transcoding/tasks/{id}", s.handleTranscodingTaskCancel)
	r.Post("/api/transcoding/tasks/{id}/retry", s.handleTranscodingTaskRetry)
	r.Post("/api/transcoding/backfill", s.handleTranscodingBackfill)
	r.Get("/api/transcoding/cameras", s.handleTranscodingCameraConfigs)
	r.Get("/api/transcoding/recordings-without-transcode", s.handleTranscodingRecordingsWithoutTranscode)
}
