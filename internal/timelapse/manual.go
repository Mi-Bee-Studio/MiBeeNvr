package timelapse

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// ManualTrigger assembles and launches on-demand periodic merges (single
// camera or batch) with the same wiring the scheduled path uses (#1009
// batch 3): display-timezone and reference-date resolution, per-camera merge
// settings (output FPS, intermediate retention, extraction interval,
// delete-after), PeriodicMergeManager construction, per-camera dedup of
// concurrent merges, and the merge goroutine lifecycle (cancel + join on
// Close). The HTTP layer keeps request decoding and response shaping only.
type ManualTrigger struct {
	db *storage.DB
	// config returns the CURRENT config — a getter, not a captured pointer,
	// because the handler's config field is assigned post-construction in
	// tests and hot paths must observe the latest value.
	config func() *config.Config
	cams   CameraSettings

	// recordingEnabled gates video frames into manual merges exactly like the
	// scheduled path (nil/unknown camera ⇒ true). Wired as a closure because
	// it consults live app state (RecordingGate).
	recordingEnabled func(cameraID string) bool

	sourceDeleter SourceRecordingDeleter

	mu     sync.Mutex
	closed bool
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	active sync.Map // cameraID → struct{} — one manual merge per camera
}

// CameraSettings is the per-camera config lookup the trigger needs (the
// camera manager at the app boundary); an interface so this package doesn't
// depend on the camera package.
type CameraSettings interface {
	GetCameraConfig(cameraID string) *config.CameraConfig
}

// NewManualTrigger creates the trigger. cfg is a getter so late-bound config
// replacements are observed; nil wiring states surface via TriggerUnwiredError.
func NewManualTrigger(db *storage.DB, cfg func() *config.Config, cams CameraSettings, recordingEnabled func(cameraID string) bool) *ManualTrigger {
	return &ManualTrigger{db: db, config: cfg, cams: cams, recordingEnabled: recordingEnabled}
}

// cfg returns the current config (nil-safe).
func (t *ManualTrigger) cfg() *config.Config {
	if t.config == nil {
		return nil
	}
	return t.config()
}

// SetSourceRecordingDeleter wires the opt-in delete_recordings_after_merge
// behavior. Wired post-construction because the cleanup manager is built
// after the trigger in app assembly; nil = deletion disabled.
func (t *ManualTrigger) SetSourceRecordingDeleter(d SourceRecordingDeleter) {
	t.sourceDeleter = d
}

// HasSourceRecordingDeleter reports whether the deleter is wired.
func (t *ManualTrigger) HasSourceRecordingDeleter() bool { return t.sourceDeleter != nil }

// --- typed errors (coded-error track) ---

// MergeAlreadyActiveError indicates a manual merge is already running for the
// camera (one at a time).
type MergeAlreadyActiveError struct {
	CameraID string
}

func (e *MergeAlreadyActiveError) Error() string {
	return "a merge is already in progress for this camera"
}
func (e *MergeAlreadyActiveError) Code() string { return "TIMELAPSE_MERGE_ACTIVE" }

// InvalidDurationError indicates the duration label didn't parse.
type InvalidDurationError struct {
	Input string
	Err   error
}

func (e *InvalidDurationError) Error() string { return "invalid duration: " + e.Err.Error() }
func (e *InvalidDurationError) Code() string  { return "TIMELAPSE_INVALID_DURATION" }
func (e *InvalidDurationError) Unwrap() error { return e.Err }

// TriggerShuttingDownError indicates the trigger is closed and refused the
// launch (server shutdown).
type TriggerShuttingDownError struct{}

func (e *TriggerShuttingDownError) Error() string { return "server is shutting down" }
func (e *TriggerShuttingDownError) Code() string  { return "TIMELAPSE_SHUTTING_DOWN" }

// TriggerUnwiredError indicates a wiring dependency (database/config) is
// missing — only reachable in unwired test setups.
type TriggerUnwiredError struct {
	What string // "database" | "config"
}

func (e *TriggerUnwiredError) Error() string { return e.What + " not available" }
func (e *TriggerUnwiredError) Code() string  { return "TIMELAPSE_TRIGGER_UNWIRED" }

// checkWired guards the db/config wiring after request-level validation so
// invalid-duration requests still map to 400 (historical ordering).
func (t *ManualTrigger) checkWired() error {
	if t.db == nil {
		return &TriggerUnwiredError{What: "database"}
	}
	if t.cfg() == nil {
		return &TriggerUnwiredError{What: "config"}
	}
	return nil
}

// Trigger launches one manual merge for cameraID covering the window implied
// by durationStr (e.g. "natural-day", "24h") anchored at dateStr (YYYY-MM-DD
// in the display timezone; empty = now). It returns the resolved reference
// time for the response payload.
func (t *ManualTrigger) Trigger(cameraID, durationStr, dateStr string) (time.Time, error) {
	// Dedup first, mirroring the historical ordering: a concurrent request
	// with an invalid duration still sees the 409.
	if _, loaded := t.active.LoadOrStore(cameraID, struct{}{}); loaded {
		return time.Time{}, &MergeAlreadyActiveError{CameraID: cameraID}
	}
	release := true
	defer func() {
		if release {
			t.active.Delete(cameraID)
		}
	}()

	dur, err := config.ParseMergeDuration(durationStr)
	if err != nil {
		return time.Time{}, &InvalidDurationError{Input: durationStr, Err: err}
	}
	if err := t.checkWired(); err != nil {
		return time.Time{}, err
	}

	loc := t.displayTimezone()
	refTime := t.referenceTime(dateStr, loc)

	launched := t.launch(cameraID, durationStr, dur, loc, refTime, true)
	if !launched {
		return time.Time{}, &TriggerShuttingDownError{}
	}
	release = false // the goroutine now owns the dedup slot
	return refTime, nil
}

// BatchResult is the per-camera outcome of a batch trigger.
type BatchResult struct {
	CameraID string `json:"camera_id"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

// TriggerBatch launches merges for several cameras at once. Duration parsing
// is validated up front (an invalid label fails the whole batch); per-camera
// launches don't dedup against singles, matching the historical batch
// behavior.
func (t *ManualTrigger) TriggerBatch(cameraIDs []string, durationStr, dateStr string) ([]BatchResult, error) {
	dur, err := config.ParseMergeDuration(durationStr)
	if err != nil {
		return nil, &InvalidDurationError{Input: durationStr, Err: err}
	}
	if err := t.checkWired(); err != nil {
		return nil, err
	}
	loc := t.displayTimezone()
	refTime := t.referenceTime(dateStr, loc)

	results := make([]BatchResult, 0, len(cameraIDs))
	for _, cameraID := range cameraIDs {
		launched := t.launch(cameraID, durationStr, dur, loc, refTime, false)
		if !launched {
			results = append(results, BatchResult{CameraID: cameraID, Status: "rejected", Error: "server is shutting down"})
			continue
		}
		results = append(results, BatchResult{CameraID: cameraID, Status: "merge_initiated"})
	}
	return results, nil
}

// launch assembles the PeriodicMergeManager for one camera and runs it on a
// tracked goroutine. Returns false when the trigger is closed.
func (t *ManualTrigger) launch(cameraID, durationStr string, dur time.Duration, loc *time.Location, refTime time.Time, dedup bool) bool {
	mgr := t.newManager(cameraID, dur, durationStr, loc)

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return false
	}
	if t.ctx == nil {
		// Lazy-init: derive from Background so merges run independent of any
		// single HTTP request's lifetime, but remain cancellable via Close.
		t.ctx, t.cancel = context.WithCancel(context.Background())
	}
	ctx := t.ctx
	t.wg.Add(1)
	t.mu.Unlock()

	go func() {
		defer t.wg.Done()
		if dedup {
			defer t.active.Delete(cameraID)
		}
		if err := mgr.Run(ctx, cameraID, refTime); err != nil {
			slog.Warn("timelapse merge failed", "camera_id", cameraID, "duration", durationStr, "error", err)
		}
	}()
	return true
}

// displayTimezone resolves the configured display timezone ("Local" handled
// explicitly — time.LoadLocation("Local") fails, which would silently fall
// back to UTC and misalign natural-day windows).
func (t *ManualTrigger) displayTimezone() *time.Location {
	cfg := t.cfg()
	switch {
	case cfg == nil || cfg.Timezone == "" || cfg.Timezone == "UTC":
		return time.UTC
	case cfg.Timezone == "Local":
		return time.Local
	default:
		if l, err := time.LoadLocation(cfg.Timezone); err == nil {
			return l
		}
	}
	return time.UTC
}

// referenceTime anchors a merge window: dateStr (YYYY-MM-DD in loc) when
// given, else now-in-loc. Unparseable dates fall back to now (historical
// behavior).
func (t *ManualTrigger) referenceTime(dateStr string, loc *time.Location) time.Time {
	if dateStr != "" {
		if parsed, err := time.ParseInLocation("2006-01-02", dateStr, loc); err == nil {
			return parsed
		}
	}
	return time.Now().In(loc)
}

// newManager builds the PeriodicMergeManager for one manual merge, wiring the
// merge store (so the output is discoverable via /api/timelapse/merges), the
// recording-enabled gate (parity with the scheduled path), the intermediate
// pruner, and the camera's merge settings.
func (t *ManualTrigger) newManager(cameraID string, dur time.Duration, durationStr string, loc *time.Location) *PeriodicMergeManager {
	fps, retainMP4 := 10, false
	if cam := t.cameraConfig(cameraID); cam != nil && cam.Timelapse != nil {
		if cam.Timelapse.MergeOutputFPS > 0 {
			fps = cam.Timelapse.MergeOutputFPS
		}
		retainMP4 = cam.Timelapse.RetainIntermediateMP4Value()
	}

	dataDir := ""
	if cfg := t.cfg(); cfg != nil {
		dataDir = filepath.Join(cfg.Storage.RootDir, "periodic-merge")
	}
	mgr := NewPeriodicMergeManager(
		t.db, t.db, NewGoMerger(), fps, dataDir, dur, loc,
		WithMergeStore(t.db),
		WithDurationLabel(durationStr),
		WithRecordingEnabledProvider(func(id string) bool {
			if t.recordingEnabled != nil {
				return t.recordingEnabled(id)
			}
			return true
		}),
		WithRetainIntermediateMP4(retainMP4),
		WithIntermediateMP4Pruner(t.db),
		WithExtractionInterval(t.extractionInterval(cameraID)),
		WithDeleteRecordingsAfterMerge(t.deleteAfterMerge(cameraID)),
	)
	mgr.SetSourceRecordingDeleter(t.sourceDeleter)
	return mgr
}

// cameraConfig returns the live camera config (nil-safe).
func (t *ManualTrigger) cameraConfig(cameraID string) *config.CameraConfig {
	if t.cams == nil {
		return nil
	}
	return t.cams.GetCameraConfig(cameraID)
}

// extractionInterval returns the camera's timelapse.interval (default 30s).
func (t *ManualTrigger) extractionInterval(cameraID string) time.Duration {
	if cam := t.cameraConfig(cameraID); cam != nil && cam.Timelapse != nil {
		if d, err := time.ParseDuration(cam.Timelapse.Interval); err == nil && d > 0 {
			return d
		}
	}
	return 30 * time.Second
}

// deleteAfterMerge reports the camera's delete_recordings_after_merge flag
// (default false).
func (t *ManualTrigger) deleteAfterMerge(cameraID string) bool {
	if cam := t.cameraConfig(cameraID); cam != nil && cam.Timelapse != nil {
		return cam.Timelapse.DeleteRecordingsAfterMerge
	}
	return false
}

// Close cancels in-flight manual merges and joins their goroutines. It must
// be called during shutdown so merges don't outlive the storage tree.
// Idempotent.
func (t *ManualTrigger) Close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	cancel := t.cancel
	t.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	// Wait outside the lock: merges may still read trigger state that doesn't
	// take the lock, and holding it while waiting risks deadlock.
	t.wg.Wait()
}
