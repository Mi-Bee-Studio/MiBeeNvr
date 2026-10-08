// Package credprobe tests newly auto-discovered cameras for factory-default
// credentials (#credprobe).
//
// Rationale: the NVR's admin-level camera operations (time-sync writes,
// device management) need credentials. Cameras enrolled by auto-discovery
// arrive with none, and in practice most are still on factory defaults. This
// service tests a SMALL, configured candidate list against each newly
// enrolled password-less camera — exactly ONCE per camera (enforced by the
// DB ledger, surviving restarts) — and on a match writes the working
// credential into the camera config. With credential_probe.rotate_password
// set, the matched account is immediately rotated to the operator's chosen
// password (ONVIF SetUser) so no camera stays on a factory default.
//
// Hard rules (by design, not configuration):
//   - once per camera, ever — success, failure, or crash in between;
//   - never runs when the camera was enrolled WITH a password;
//   - an inconclusive answer (device unreachable/unpredictable) aborts the
//     run immediately — never hammer a device that isn't answering cleanly;
//   - config-file only: no web UI, no API surface for this feature.
package credprobe

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
)

// Ledger is the persistence surface (satisfied by *storage.DB).
type Ledger interface {
	ClaimCredentialProbe(ctx context.Context, cameraID string) (bool, error)
	SetCredentialProbeResult(ctx context.Context, cameraID, status, matchedUsername string, rotated bool) error
}

// CameraUpdater applies credential updates to the camera config
// (satisfied by *camera.CameraManager).
type CameraUpdater interface {
	UpdateCamera(ctx context.Context, cameraID string, updates camera.CameraUpdate) (*config.CameraConfig, error)
}

// Prober is the ONVIF probe surface (satisfied by *onvif.CredentialProber;
// faked in tests).
type Prober interface {
	Try(ctx context.Context, username, password string) onvif.CredentialVerdict
	SetUserPassword(ctx context.Context, authUser, authPass, targetUser, newPassword string) error
}

// Service orchestrates the probe runs. ProbeEnrolled is called by the
// auto-discovery enroll path; the actual run happens on a tracked goroutine.
type Service struct {
	cfg  config.CredentialProbeConfig
	db   Ledger
	cams CameraUpdater

	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// injectable for tests
	newProber func(endpoint string) Prober
	sleep     func(ctx context.Context, d time.Duration)
}

// New constructs the service. db and cams must be non-nil.
func New(cfg config.CredentialProbeConfig, db Ledger, cams CameraUpdater) *Service {
	return &Service{
		cfg:  cfg,
		db:   db,
		cams: cams,
		newProber: func(endpoint string) Prober {
			return onvif.NewCredentialProber(endpoint)
		},
		sleep: func(ctx context.Context, d time.Duration) {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
			case <-t.C:
			}
		},
	}
}

// Name implements the app service interface.
func (s *Service) Name() string { return "credprobe" }

// Start stores the lifecycle context. Registered before autodiscover so it
// stops after it — enroll-time probes remain valid while discovery winds
// down, and the context cancellation gates any late arrivals.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx != nil {
		return nil
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.ctx, s.cancel = runCtx, cancel
	return nil
}

// Stop cancels in-flight probes and waits for them.
func (s *Service) Stop() error {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
	return nil
}

// ProbeEnrolled offers a freshly enrolled camera to the prober. All skip
// conditions are checked synchronously; the run itself is async (enrollment
// must not block on SOAP round trips). Safe to call with a nil receiver
// dependency chain broken (service not started) — the goroutine exits.
func (s *Service) ProbeEnrolled(cameraID string, cam config.CameraConfig) {
	s.mu.Lock()
	ctx := s.ctx
	s.mu.Unlock()
	if ctx == nil || ctx.Err() != nil {
		return
	}
	if cam.Protocol != "onvif" {
		return
	}
	// User-provided password (manual entry or autodiscover default creds):
	// nothing to probe.
	if cam.Password != "" {
		return
	}
	if !s.cfg.EnabledOrDefault() {
		return
	}

	s.wg.Add(1)
	go s.run(ctx, cameraID, cam)
}

func (s *Service) run(ctx context.Context, cameraID string, cam config.CameraConfig) {
	defer s.wg.Done()

	claimed, err := s.db.ClaimCredentialProbe(ctx, cameraID)
	if err != nil {
		// A ledger failure means we CANNOT guarantee once-only — do not probe.
		slog.Warn("credential probe: ledger unavailable, skipping (once-only cannot be enforced)",
			"camera_id", cameraID, "error", err)
		return
	}
	if !claimed {
		slog.Debug("credential probe: camera already probed once, skipping", "camera_id", cameraID)
		return
	}

	endpoint := cam.ONVIFEndpoint
	if endpoint == "" {
		endpoint = cam.URL
	}
	prober := s.newProber(endpoint)
	candidates := s.cfg.EffectiveCandidates()
	interval := time.Duration(s.cfg.AttemptIntervalOrDefault()) * time.Millisecond
	status := "failed"
	matchedUser := ""
	rotated := false
	finalUser, finalPass := "", ""

	for i, cand := range candidates {
		if ctx.Err() != nil {
			break
		}
		verdict := prober.Try(ctx, cand.Username, cand.Password)
		if verdict == onvif.CredentialAccepted {
			matchedUser = cand.Username
			finalUser, finalPass = cand.Username, cand.Password
			status = "matched"
			break
		}
		if verdict == onvif.CredentialInconclusive {
			// Device unreachable or answering unpredictably — stop. Further
			// attempts risk a lockout without a chance of matching.
			slog.Info("credential probe: inconclusive answer, aborting run",
				"camera_id", cameraID, "tried", i+1, "of", len(candidates))
			break
		}
		if i < len(candidates)-1 {
			s.sleep(ctx, interval)
		}
	}

	if matchedUser == "" {
		if err := s.db.SetCredentialProbeResult(ctx, cameraID, status, "", false); err != nil {
			slog.Warn("credential probe: recording failure status failed", "camera_id", cameraID, "error", err)
		}
		slog.Info("credential probe: no factory default matched", "camera_id", cameraID)
		return
	}

	// Optional hardening: rotate the matched account to the operator's
	// password, then verify the new credential before committing it.
	if s.cfg.RotatePassword != "" {
		newPass := s.cfg.RotatePassword
		rotErr := prober.SetUserPassword(ctx, matchedUser, finalPass, matchedUser, newPass)
		if rotErr == nil && prober.Try(ctx, matchedUser, newPass) == onvif.CredentialAccepted {
			finalPass = newPass
			rotated = true
			status = "rotated"
		} else {
			// Keep the matched factory credential — it still unlocks admin
			// operations; the operator can rotate manually later.
			slog.Warn("credential probe: password rotation failed, keeping the factory credential",
				"camera_id", cameraID, "username", matchedUser, "error", rotErr)
		}
	}

	updates := camera.CameraUpdate{Username: &finalUser, Password: &finalPass}
	if _, err := s.cams.UpdateCamera(ctx, cameraID, updates); err != nil {
		slog.Warn("credential probe: writing credential to camera config failed",
			"camera_id", cameraID, "error", err)
		// The camera HAS been probed (and possibly rotated) — the once-only
		// rule stands. Record the outcome so a later manual fix can consult it.
		_ = s.db.SetCredentialProbeResult(ctx, cameraID, status, matchedUser, rotated)
		return
	}

	if err := s.db.SetCredentialProbeResult(ctx, cameraID, status, matchedUser, rotated); err != nil {
		slog.Warn("credential probe: recording result failed", "camera_id", cameraID, "error", err)
	}
	note := "consider changing the camera password"
	if rotated {
		note = "password rotated to the configured default"
	}
	slog.Info("credential probe: default credential detected and filled",
		"camera_id", cameraID, "username", matchedUser, "rotated", rotated, "note", note)
}
