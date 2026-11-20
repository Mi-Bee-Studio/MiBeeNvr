package api

import (
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/api/systemapi"
)

// systemDeps adapts the Handler's fields to the system domain Deps.
func systemDeps(h *Handler) systemapi.Deps {
	return systemapi.Deps{
		DB:           h.db,
		Store:        h.store,
		Config:       h.config,
		ConfigPath:   h.configPath,
		Cam:          h.camMgr,
		APIKeyStore:  h.apiKeyStore,
		MigrationMgr: h.migrationMgr,
		TranscodeMgr: h.transcodeMgr,
		Downloader:   h.downloader,
		UpSince:      func() time.Time { return appStartTime },
	}
}

// Domain interface aliases: root fields, setters, and tests keep the
// pre-extraction names.
type (
	TranscodeManagerAPI = systemapi.TranscodeManagerAPI
	TranscodeDownloader = systemapi.TranscodeDownloader
	StorageMigrator     = systemapi.StorageMigrator
)

// SetTranscodeManager and SetStorageMigrator are app-layer wiring setters
// (builders_http.go); they stay on Handler after the system shim cleanup.
func (h *Handler) SetTranscodeManager(mgr TranscodeManagerAPI) { h.transcodeMgr = mgr }

func (h *Handler) SetStorageMigrator(m StorageMigrator) { h.migrationMgr = m }
