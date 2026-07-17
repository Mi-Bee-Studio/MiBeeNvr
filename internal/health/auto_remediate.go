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
// internal/camera. It is invoked when a camera is blacklisted — i.e. after
// persistent reconnection failure — which is the signal that the camera's IP has
// likely changed and the recorder cannot recover on its own.
//
// Returns found=true when the camera was located at a new address AND its
// recorder was successfully restarted. found=false with a nil error means the
// scan completed but the device was not on the candidate subnets (the camera is
// genuinely offline or on an unrouted network). A non-nil error means the
// attempt itself failed (e.g. context cancelled, DB error).
type RediscoverFunc func(ctx context.Context, cameraID string) (found bool, err error)

// cameraRestartState tracks per-camera restart history and blacklist status.
type cameraRestartState struct {
	attempts            []time.Time
	blacklistedSince    time.Time
	lastRediscoveryScan time.Time // last time a periodic blacklist rescan was dispatched
	// consecutiveScanMisses counts consecutive "device not found" results
	// from periodic blacklist rescans. Each miss increases the rescan interval
	// (exponential backoff), so permanently-dead cameras stop hammering the
	// disk/network with a full-/24 scan every few minutes.
	consecutiveScanMisses int
}

// AutoRemediator decides whether to automatically restart a failed camera recorder.
// It enforces safety rules: triggers on StatusError immediately, and on
// StatusReconnecting only after the recorder has been stuck for
// ReconnectingTimeoutMinutes (a recorder's own reconnect loop never escalates to
// StatusError, so without this gate a camera whose IP changed would loop forever
// and IP rediscovery would never fire). Includes per-camera rate limiting,
// cooldown, global rate limiting, and blacklisting.
type AutoRemediator struct {
	cfg         config.HealthAutoRemediationConfig
	restartFn   RestartRecorderFunc
	isEnabledFn IsCameraEnabledFunc
	// rediscoverFn is invoked once when a camera is blacklisted (persistent
	// failure). Optional: nil = no IP self-healing. The camera manager decides
	// whether a given camera supports it (ONVIF only, must have a stable_id).
	rediscoverFn RediscoverFunc
	// offlineDurationFn returns how long a camera has been in an offline state
	// (error/reconnecting). Injected so Check can gate reconnecting-triggered
	// restarts on a minimum offline duration. Nil = treat reconnecting like error
	// (legacy behavior, but not recommended).
	offlineDurationFn func(cameraID string) time.Duration

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

// SetOfflineDurationFn registers a function that returns how long a camera has
// been offline (error/reconnecting). Used to gate reconnecting-triggered
// restarts on ReconnectingTimeoutMinutes so brief reconnect blips don't cause a
// hard restart. Safe to call before or after Start.
func (r *AutoRemediator) SetOfflineDurationFn(fn func(cameraID string) time.Duration) {
	r.mu.Lock()
	r.offlineDurationFn = fn
	r.mu.Unlock()
}

// rescanInterval computes the exponential-backoff interval for periodic
// blacklist rescans. The base interval is RediscoveryRescanMinutes; after
// each consecutive "device not found" miss it is multiplied by
// RediscoveryRescanBackoff, capped at RediscoveryRescanMaxMinutes.
// This prevents permanently-dead cameras from sustaining a full-/24 scan
// every few minutes indefinitely (the root cause of the production IO
// storm: 3 dead cameras × 5-min fixed rescan = continuous subnet scanning).
func (r *AutoRemediator) rescanInterval(st *cameraRestartState) time.Duration {
	base := time.Duration(r.cfg.RediscoveryRescanMinutes) * time.Minute
	if base <= 0 || st == nil {
		return base
	}
	backoff := r.cfg.RediscoveryRescanBackoff
	if backoff < 1.0 {
		backoff = 1.0
	}
	interval := float64(base)
	for range st.consecutiveScanMisses {
		interval *= backoff
	}
	if maxM := time.Duration(r.cfg.RediscoveryRescanMaxMinutes) * time.Minute; maxM > 0 && time.Duration(interval) > maxM {
		return maxM
	}
	return time.Duration(interval)
}

// Check evaluates whether a camera should be auto-restarted based on its status.
// Returns nil if restart was triggered, or an error explaining why it was not.
func (r *AutoRemediator) Check(cameraID string, status string) error {
	// Safety check 0: feature must be enabled.
	if !r.cfg.Enabled {
		return nil
	}

	// Safety check 1: trigger on StatusError immediately, or on StatusReconnecting
	// only after the recorder has been stuck beyond ReconnectingTimeoutMinutes.
	// A recorder's own reconnect loop never escalates to StatusError — it loops
	// forever at "reconnecting" — so without admitting reconnecting (after a
	// timeout), a camera whose IP changed would never be restarted, never
	// blacklisted, and IP rediscovery would never fire.
	if status == string(model.StatusReconnecting) {
		// Need offline-duration info to gate on a timeout. Without it, be
		// conservative and don't restart (avoids restarting on every brief
		// reconnect blip when the manager hasn't wired the lookup).
		if r.offlineDurationFn == nil {
			return nil
		}
		timeout := time.Duration(r.cfg.ReconnectingTimeoutMinutes) * time.Minute
		if timeout <= 0 {
			timeout = 10 * time.Minute
		}
		offline := r.offlineDurationFn(cameraID)
		if offline < timeout {
			return nil // brief reconnect blip — let the recorder's own backoff handle it
		}
		// Stuck in reconnect long enough — fall through to restart logic.
		slog.Info("auto-remediate: recorder stuck in reconnecting, triggering restart",
			"camera_id", cameraID, "offline_duration", offline, "threshold", timeout)
	} else if status != string(model.StatusError) {
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
			// While blacklisted, periodically re-attempt IP rediscovery so a camera
			// that comes back online (e.g. power restored) is recovered within
			// RediscoveryRescanMinutes instead of waiting the full BlacklistHours.
			// This does NOT restart the recorder directly — rediscovery's own
			// startRecorder runs on success, and we clear the blacklist so the next
			// Check cycle resumes normal remediation. 0 = disabled (legacy: scan
			// only once at the moment of blacklisting).
			rescanInterval := r.rescanInterval(state)
			maxMisses := r.cfg.RediscoveryMaxScanMisses
			// Determine whether to dispatch a periodic rescan. Three conditions:
			//   1. Interval is positive and a rediscoverer is wired.
			//   2. Enough time has elapsed since the last scan.
			//   3. We haven't exceeded the hard-stop miss limit (0 = unlimited).
			shouldRescan := rescanInterval > 0 && r.rediscoverFn != nil &&
				now.Sub(state.lastRediscoveryScan) >= rescanInterval &&
				(maxMisses <= 0 || state.consecutiveScanMisses < maxMisses)
			if shouldRescan {
				state.lastRediscoveryScan = now
				cameraIDLocal := cameraID
				rediscoverFnLocal := r.rediscoverFn
				r.mu.Unlock()
				go func() {
					found, rerr := rediscoverFnLocal(context.Background(), cameraIDLocal)
					if rerr != nil {
						slog.Warn("blacklist rescan: rediscovery error", "camera_id", cameraIDLocal, "error", rerr)
						return // transient failure — don't increment miss counter
					}
					r.mu.Lock()
					st := r.cameraStates[cameraIDLocal]
					if st != nil {
						if found {
							slog.Info("blacklist rescan: camera relocated, clearing blacklist", "camera_id", cameraIDLocal)
							st.blacklistedSince = time.Time{}
							st.attempts = nil
							st.consecutiveScanMisses = 0
						} else {
							st.consecutiveScanMisses++ // genuinely offline → back off
						}
					}
					r.mu.Unlock()
				}()
				r.mu.Lock()
			}
			return fmt.Errorf("camera %s is blacklisted until %s", cameraID, blacklistExpiry.Format(time.RFC3339))
		}
		// Blacklist expired — reset state.
		state.blacklistedSince = time.Time{}
		state.attempts = nil
		state.consecutiveScanMisses = 0
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
	// Also record the scan timestamp so the periodic blacklist rescan (above)
	// counts this initial attempt and waits RediscoveryRescanMinutes before the
	// next one.
	if justBlacklisted && rediscoverFn != nil {
		// Set lastRediscoveryScan while we still hold the lock (deferred unlock
		// hasn't run yet, so this is safe — no re-entrancy).
		if st := r.cameraStates[cameraID]; st != nil {
			st.lastRediscoveryScan = time.Now()
		}
		go func() {
			found, rerr := rediscoverFn(context.Background(), cameraID)
			if rerr != nil {
				slog.Warn("rediscovery failed for blacklisted camera", "camera_id", cameraID, "error", rerr)
				return
			}
			if found {
				slog.Info("rediscovery located camera at blacklist moment, clearing blacklist",
					"camera_id", cameraID)
				r.mu.Lock()
				if st := r.cameraStates[cameraID]; st != nil {
					st.blacklistedSince = time.Time{}
					st.attempts = nil
				}
				r.mu.Unlock()
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
