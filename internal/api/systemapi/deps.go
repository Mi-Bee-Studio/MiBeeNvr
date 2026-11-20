// Package systemapi serves the system domain: stats (storage/trends/system),
// settings (global + streaming/transcoding/auto-discover sections), storage
// candidates and the background migration control surface, API-key
// management, backups, protocol capability listing, feature toggles, and the
// transcoding subsystem endpoints (check/status/tasks/backfill).
//
// Extraction pattern shared by the api domain packages: a local Deps
// holding what the handlers touch, plus Register (authed group, flat — chi
// forbids mounting the same pattern twice).
package systemapi

import (
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// Deps is what this domain needs from the API assembly. Optional capabilities
// (migrator, transcode manager, downloader, API-key store) are nil-tolerant —
// handlers keep their existing nil checks and degrade.
type Deps struct {
	DB         *storage.DB
	Store      *storage.Manager
	Config     *config.Config
	ConfigPath string
	Cam        *camera.CameraManager

	// APIKeyStore backs /api/settings/api-keys bookkeeping (last-used stamps
	// + hot key-set sync, #335). Nil in tests.
	APIKeyStore *middleware.APIKeyStore
	// MigrationMgr drives background storage migrations (#395); nil = the
	// endpoints degrade (idle status / no-op enqueue).
	MigrationMgr StorageMigrator
	// TranscodeMgr + Downloader are the transcoding subsystem surface; nil =
	// transcoding disabled (the handlers answer their documented shapes).
	TranscodeMgr TranscodeManagerAPI
	Downloader   TranscodeDownloader

	// UpSince returns the process start time (uptime origin). Always wired
	// from the api root (appStartTime); nil = fall back to time.Now().
	UpSince func() time.Time
}

// server carries the Deps for the handler methods, plus the /api/stats/system
// rate-window state (previous-sample DB txn counters, single-writer per HTTP
// poll) that used to live on the api root Handler.
type server struct {
	deps Deps

	dbTxnMu   sync.Mutex
	dbTxnPrev *dbTxnSample
}

// upSince resolves the uptime origin.
func (s *server) upSince() time.Time {
	if s.deps.UpSince != nil {
		return s.deps.UpSince()
	}
	return time.Now()
}
