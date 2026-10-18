package app

// builders.go contains buildAppDeps — the construction orchestrator of
// RunFree. Each domain lives in its own phase file and is called below in the
// historical construction order; registerServices (register.go) then
// registers the managers as App services in start/stop order.
//
// The split is purely structural: no logic reordering, no behavioral change.

import (
	"context"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/cleanup"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// triggerDispatcherFunc is the onAction callback built by
// mqtt.NewActionDispatcher and shared by the MQTT client and the HTTP webhook
// trigger (#709).
type triggerDispatcherFunc = func(cameraID, action string, duration time.Duration)

// buildAppDeps constructs every service dependency and returns it in an
// appDeps struct. The returned cleanup func is the error-path teardown
// (cancel startup-bg goroutines + close the DB) the caller must invoke if it
// bails out before App.Start — mirroring RunFree's historical `return nil, err`
// cleanup at each construction step.
func buildAppDeps(cfg *config.Config, configPath string) (*appDeps, func(), error) {
	deps := &appDeps{cfg: cfg, configPath: configPath}

	if err := buildCoreDeps(deps); err != nil {
		return nil, nil, err
	}
	buildRecordingDeps(deps)
	flvMgr, gbLibEvents, err := buildStreamingDeps(deps)
	if err != nil {
		return nil, nil, err
	}
	tlSourceDeleter, snapCapturer, triggerDispatcher, err := buildMaintenanceDeps(deps)
	if err != nil {
		deps.startupBgCancel()
		deps.db.Close()
		return nil, nil, err
	}
	if err := buildHTTPDeps(deps, flvMgr, gbLibEvents, tlSourceDeleter, snapCapturer, triggerDispatcher); err != nil {
		deps.startupBgCancel()
		return nil, nil, err
	}

	cleanup := func() {
		deps.startupBgCancel()
		deps.db.Close()
	}
	return deps, cleanup, nil
}

// timelapseSourceDeleter adapts cleanup.CleanupManager to the timelapse
// package's SourceRecordingDeleter interface. Used by the opt-in
// delete_recordings_after_merge behavior for periodic merges (scheduled path
// and the API manual-merge path).
type timelapseSourceDeleter struct{ cm *cleanup.CleanupManager }

func (d timelapseSourceDeleter) DeleteRecordings(ctx context.Context, recordings []model.Recording, reason string) ([]string, error) {
	return d.cm.BatchDeleteRecordingsWithFiles(ctx, recordings, reason)
}
