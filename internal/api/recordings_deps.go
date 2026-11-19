package api

import (
	"errors"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/api/recordingsapi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/timelapse"
)

// recordingsDeps adapts the Handler's fields to the recordings domain Deps.
// All managers are concrete pointers here — the domain handlers carry the
// same nil checks the pre-extraction code had.
func recordingsDeps(h *Handler) recordingsapi.Deps {
	return recordingsapi.Deps{
		DB:                h.db,
		Store:             h.store,
		Config:            h.config,
		ConfigPath:        h.configPath,
		Cam:               h.camMgr,
		Merge:             h.mergeMgr,
		RollingMerge:      h.rollingMergeMgr,
		TimelapseMerge:    h.timelapseMergeMgr,
		MergeScheduler:    h.mergeScheduler,
		RecFrames:         h.recFrames,
		VOD:               h.vodMgr,
		ServeFileBudgeted: h.serveFileBudgeted,
		RecordTimelineSeek: func(cameraID, seekType string) {
			if m := currentAPIMetrics(); m != nil {
				m.TimelineSeeksTotal.WithLabelValues(cameraID, seekType).Inc()
			}
		},
		TriggerManualMerge: func(cameraID, durationStr, dateStr string) (time.Time, error) {
			if h.tlTrigger == nil {
				return time.Time{}, errors.New("manual merge trigger not available")
			}
			return h.tlTrigger.Trigger(cameraID, durationStr, dateStr)
		},
		TriggerBatchMerge: func(cameraIDs []string, durationStr, dateStr string) ([]timelapse.BatchResult, error) {
			if h.tlTrigger == nil {
				return nil, errors.New("manual merge trigger not available")
			}
			return h.tlTrigger.TriggerBatch(cameraIDs, durationStr, dateStr)
		},
		ShutdownSignaled: func() <-chan struct{} { return streamShutdown },
	}
}
