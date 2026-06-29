package health

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

// RestartRecorderFunc is the function signature for restarting a camera recorder.
// Injected to avoid circular dependency on internal/camera.
type RestartRecorderFunc func(ctx context.Context, cameraID string) error

// IsCameraEnabledFunc checks whether a camera is enabled for auto-remediation.
type IsCameraEnabledFunc func(cameraID string) bool

// RediscoverFunc re-discovers a camera by its stable hardware identifier and
// reconnects it. Injected (not imported) to avoid a circular dependency on
// internal/camera. It is invoked once when a camera is blacklisted — i.e. after
// persistent reconnection failure — which is the signal that the camera's IP has
// likely changed and the recorder cannot recover on its own. Returning nil does
// not necessarily mean the camera was found; it means the attempt was dispatched.
type RediscoverFunc func(ctx context.Context, cameraID string) error

// cameraRestartState tracks per-camera restart history and blacklist status.
type cameraRestartState struct {
	attempts         []time.Time
	blacklistedSince time.Time
}

// AutoRemediator decides whether to automatically restart a failed camera recorder.
// It enforces safety rules: only triggers on StatusError, never on StatusReconnecting,
// with per-camera rate limiting, cooldown, global rate limiting, and blacklisting.
type AutoRemediator struct {
	cfg         config.HealthAutoRemediationConfig
	restartFn   RestartRecorderFunc
	isEnabledFn IsCameraEnabledFunc
	// rediscoverFn is invoked once when a camera is blacklisted (persistent
	// failure). Optional: nil = no IP self-healing. The camera manager decides
	// whether a given camera supports it (ONVIF only, must have a stable_id).
	rediscoverFn RediscoverFunc

	mu             sync.Mutex
	cameraStates   map[string]*cameraRestartState
	globalRestarts []time.Time
}

// NewAutoRemediator creates a new AutoRemediator with the given config and injected functions.
func NewAutoRemediator(cfg config.HealthAutoRemediationConfig, restartFn RestartRecorderFunc, isEnabledFn IsCameraEnabledFunc) *AutoRemediator {
	return &AutoRemediator{
		cfg:            cfg,
		restartFn:      restartFn,
		isEnabledFn:    isEnabledFn,
		cameraStates:   make(map[string]*cameraRestartState),
		globalRestarts: make([]time.Time, 0),
	}
}

// SetRediscoverer registers the IP re-discovery callback. Optional — when unset,
// blacklisted cameras are not re-discovered (legacy behavior). Safe to call
// before or after Start.
func (r *AutoRemediator) SetRediscoverer(fn RediscoverFunc) {
	r.mu.Lock()
	r.rediscoverFn = fn
	r.mu.Unlock()
}

// Check evaluates whether a camera should be auto-restarted based on its status.
// Returns nil if restart was triggered, or an error explaining why it was not.
func (r *AutoRemediator) Check(cameraID string, status string) error {
	// Safety check 0: feature must be enabled.
	if !r.cfg.Enabled {
		return nil
	}

	// Safety check 1: only trigger on StatusError.
	if status != string(model.StatusError) {
		return nil
	}

	// Safety check 2: camera must be enabled.
	if r.isEnabledFn != nil && !r.isEnabledFn(cameraID) {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()

	state := r.getOrCreateState(cameraID)

	// Safety check 3: not blacklisted.
	if !state.blacklistedSince.IsZero() {
		blacklistExpiry := state.blacklistedSince.Add(time.Duration(r.cfg.BlacklistHours) * time.Hour)
		if now.Before(blacklistExpiry) {
			return fmt.Errorf("camera %s is blacklisted until %s", cameraID, blacklistExpiry.Format(time.RFC3339))
		}
		// Blacklist expired — reset state.
		state.blacklistedSince = time.Time{}
		state.attempts = nil
	}

	// Safety check 4: per-camera rate limit (count attempts in last hour).
	recentAttempts := filterRecent(state.attempts, now, time.Hour)
	if len(recentAttempts) >= r.cfg.MaxRestartsPerHour {
		return fmt.Errorf("camera %s exceeded max restarts per hour (%d)", cameraID, r.cfg.MaxRestartsPerHour)
	}

	// Safety check 5: cooldown after last attempt.
	if len(recentAttempts) > 0 {
		lastAttempt := recentAttempts[len(recentAttempts)-1]
		cooldownEnd := lastAttempt.Add(time.Duration(r.cfg.CooldownMinutes) * time.Minute)
		if now.Before(cooldownEnd) {
			return fmt.Errorf("camera %s is in cooldown until %s", cameraID, cooldownEnd.Format(time.RFC3339))
		}
	}

	// Safety check 6: global rate limit.
	recentGlobal := filterRecent(r.globalRestarts, now, time.Minute)
	if len(recentGlobal) >= r.cfg.GlobalMaxPerMin {
		return fmt.Errorf("global restart rate limit exceeded (%d/min)", r.cfg.GlobalMaxPerMin)
	}

	// All checks passed — record attempt and trigger restart.
	state.attempts = append(state.attempts, now)
	r.globalRestarts = append(r.globalRestarts, now)

	// Check if this attempt triggers blacklisting.
	updatedRecent := filterRecent(state.attempts, now, time.Hour)
	justBlacklisted := false
	if len(updatedRecent) >= r.cfg.MaxRestartsPerHour {
		state.blacklistedSince = now
		justBlacklisted = true
	}
	// Snapshot the rediscovery callback under the lock so we can invoke it after
	// releasing the lock (the callback may perform network scans).
	rediscoverFn := r.rediscoverFn

	// Release lock before calling restartFn (which may be slow).
	r.mu.Unlock()
	err := r.restartFn(context.Background(), cameraID)
	r.mu.Lock() // re-acquire for deferred unlock

	// When a camera is newly blacklisted, the recorder cannot recover on its own
	// — this is the moment to attempt IP re-discovery (the camera likely roamed to
	// a new address). Run it asynchronously so it never blocks the heal loop; it
	// only affects this one camera and a restart will follow if it succeeds.
	if justBlacklisted && rediscoverFn != nil {
		go func() {
			if rerr := rediscoverFn(context.Background(), cameraID); rerr != nil {
				slog.Warn("rediscovery failed for blacklisted camera", "camera_id", cameraID, "error", rerr)
			}
		}()
	}

	return err
}

// IsBlacklisted returns whether a camera is currently blacklisted from auto-remediation.
func (r *AutoRemediator) IsBlacklisted(cameraID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	state, ok := r.cameraStates[cameraID]
	if !ok || state.blacklistedSince.IsZero() {
		return false
	}

	blacklistExpiry := state.blacklistedSince.Add(time.Duration(r.cfg.BlacklistHours) * time.Hour)
	return time.Now().Before(blacklistExpiry)
}

// CheckAll evaluates all cameras in the given status map and attempts remediation
// for those that need it. Errors for individual cameras are logged but do not
// prevent processing of other cameras.
func (r *AutoRemediator) CheckAll(statuses map[string]string) {
	for cameraID, status := range statuses {
		if err := r.Check(cameraID, status); err != nil {
			slog.Warn("auto-remediate skipped", "camera_id", cameraID, "error", err)
		}
	}
}

// getOrCreateState returns the restart state for a camera, creating it if needed.
// Caller must hold r.mu.
func (r *AutoRemediator) getOrCreateState(cameraID string) *cameraRestartState {
	state, ok := r.cameraStates[cameraID]
	if !ok {
		state = &cameraRestartState{}
		r.cameraStates[cameraID] = state
	}
	return state
}

// filterRecent returns only timestamps within the given duration from now.
func filterRecent(times []time.Time, now time.Time, window time.Duration) []time.Time {
	cutoff := now.Add(-window)
	recent := make([]time.Time, 0, len(times))
	for _, t := range times {
		if !t.Before(cutoff) {
			recent = append(recent, t)
		}
	}
	return recent
}
