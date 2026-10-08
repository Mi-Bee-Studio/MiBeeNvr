package credprobe

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// fakeLedger is an in-memory Ledger.
type fakeLedger struct {
	mu      sync.Mutex
	claimed map[string]string // cameraID → status
	fail    bool
}

func (f *fakeLedger) ClaimCredentialProbe(ctx context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return false, errors.New("db down")
	}
	if _, ok := f.claimed[id]; ok {
		return false, nil
	}
	if f.claimed == nil {
		f.claimed = map[string]string{}
	}
	f.claimed[id] = storage.CredProbeStatusAttempted
	return true, nil
}

func (f *fakeLedger) SetCredentialProbeResult(ctx context.Context, id, status, user string, rotated bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimed == nil {
		f.claimed = map[string]string{}
	}
	f.claimed[id] = status + "|" + user
	return nil
}

// fakeUpdater records UpdateCamera calls.
type fakeUpdater struct {
	mu    sync.Mutex
	calls []struct{ id, user, pass string }
	err   error
}

func (f *fakeUpdater) UpdateCamera(ctx context.Context, id string, u camera.CameraUpdate) (*config.CameraConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, struct{ id, user, pass string }{id, *u.Username, *u.Password})
	return nil, f.err
}

// fakeProber scripts verdicts per credential and records SetUserPassword.
type fakeProber struct {
	mu        sync.Mutex
	tries     []string // "user/pass" per attempt
	verdicts  map[string]onvif.CredentialVerdict
	rotErr    error
	rotations []struct{ auth, target, new string }
	rotVerify onvif.CredentialVerdict // verdict for the rotated credential
	endpoint  string
}

func (f *fakeProber) Try(ctx context.Context, u, p string) onvif.CredentialVerdict {
	f.mu.Lock()
	f.tries = append(f.tries, u+"/"+p)
	// Rotation verification: the rotated credential answers per rotVerify.
	rotatedOK := false
	if len(f.rotations) > 0 {
		last := f.rotations[len(f.rotations)-1]
		rotatedOK = u == last.target && p == last.new
	}
	rotVerify := f.rotVerify
	v, ok := f.verdicts[u+"/"+p]
	f.mu.Unlock()
	if rotatedOK {
		if rotVerify == onvif.CredentialAccepted {
			return onvif.CredentialAccepted
		}
		return onvif.CredentialRejected
	}
	if !ok {
		return onvif.CredentialRejected
	}
	return v
}

func (f *fakeProber) SetUserPassword(ctx context.Context, au, ap, tu, np string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rotations = append(f.rotations, struct{ auth, target, new string }{au, tu, np})
	return f.rotErr
}

func newTestService(t *testing.T, cfg config.CredentialProbeConfig, ledger *fakeLedger, updater *fakeUpdater, prober *fakeProber) *Service {
	t.Helper()
	s := New(cfg, ledger, updater)
	s.newProber = func(endpoint string) Prober { prober.endpoint = endpoint; return prober }
	s.sleep = func(ctx context.Context, d time.Duration) {} // no real waiting
	ctx := context.Background()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	return s
}

func onvifCam(id, pass string) config.CameraConfig {
	return config.CameraConfig{ID: id, Protocol: "onvif", ONVIFEndpoint: "http://127.0.0.1:8080/onvif/device_service", Username: "", Password: pass}
}

// probeSync runs ProbeEnrolled and waits for the goroutine to finish.
func probeSync(s *Service, id string, cam config.CameraConfig) {
	s.ProbeEnrolled(id, cam)
	s.wg.Wait()
}

func TestProbe_MatchFillsCredentials(t *testing.T) {
	t.Helper()
	cfg := config.CredentialProbeConfig{Candidates: []config.CredentialProbeCandidate{
		{Username: "admin", Password: "admin"},
		{Username: "admin", Password: "12345"},
	}}
	ledger := &fakeLedger{}
	updater := &fakeUpdater{}
	prober := &fakeProber{verdicts: map[string]onvif.CredentialVerdict{"admin/12345": onvif.CredentialAccepted}}
	s := newTestService(t, cfg, ledger, updater, prober)

	probeSync(s, "cam-1", onvifCam("cam-1", ""))

	if len(updater.calls) != 1 {
		t.Fatalf("UpdateCamera calls = %d, want 1", len(updater.calls))
	}
	if updater.calls[0].user != "admin" || updater.calls[0].pass != "12345" {
		t.Errorf("filled (%q,%q), want (admin,12345)", updater.calls[0].user, updater.calls[0].pass)
	}
	if got := ledger.claimed["cam-1"]; got != "matched|admin" {
		t.Errorf("ledger status = %q, want matched|admin", got)
	}
}

func TestProbe_RotatesToConfiguredPassword(t *testing.T) {
	t.Helper()
	pw := "MyNvrDefault!9"
	cfg := config.CredentialProbeConfig{
		Candidates:     []config.CredentialProbeCandidate{{Username: "admin", Password: "admin"}},
		RotatePassword: pw,
	}
	ledger := &fakeLedger{}
	updater := &fakeUpdater{}
	prober := &fakeProber{
		verdicts:  map[string]onvif.CredentialVerdict{"admin/admin": onvif.CredentialAccepted},
		rotVerify: onvif.CredentialAccepted,
	}
	s := newTestService(t, cfg, ledger, updater, prober)

	probeSync(s, "cam-1", onvifCam("cam-1", ""))

	if len(prober.rotations) != 1 {
		t.Fatalf("rotations = %d, want 1", len(prober.rotations))
	}
	if prober.rotations[0].new != pw {
		t.Errorf("rotated to %q, want %q", prober.rotations[0].new, pw)
	}
	if updater.calls[0].pass != pw {
		t.Errorf("filled password = %q, want rotated %q", updater.calls[0].pass, pw)
	}
	if got := ledger.claimed["cam-1"]; got != "rotated|admin" {
		t.Errorf("ledger = %q, want rotated|admin", got)
	}
}

func TestProbe_RotationFailureKeepsFactoryCredential(t *testing.T) {
	t.Helper()
	cfg := config.CredentialProbeConfig{
		Candidates:     []config.CredentialProbeCandidate{{Username: "admin", Password: "admin"}},
		RotatePassword: "newpw",
	}
	ledger := &fakeLedger{}
	updater := &fakeUpdater{}
	prober := &fakeProber{
		verdicts: map[string]onvif.CredentialVerdict{"admin/admin": onvif.CredentialAccepted},
		rotErr:   errors.New("device refused"),
	}
	s := newTestService(t, cfg, ledger, updater, prober)

	probeSync(s, "cam-1", onvifCam("cam-1", ""))

	if updater.calls[0].pass != "admin" {
		t.Errorf("filled password = %q, want factory admin", updater.calls[0].pass)
	}
	if got := ledger.claimed["cam-1"]; got != "matched|admin" {
		t.Errorf("ledger = %q, want matched|admin (rotation failed)", got)
	}
}

func TestProbe_OnceOnlyAcrossRuns(t *testing.T) {
	t.Helper()
	cfg := config.CredentialProbeConfig{Candidates: []config.CredentialProbeCandidate{{Username: "admin", Password: "admin"}}}
	ledger := &fakeLedger{}
	updater := &fakeUpdater{}
	prober := &fakeProber{} // nothing matches
	s := newTestService(t, cfg, ledger, updater, prober)

	probeSync(s, "cam-1", onvifCam("cam-1", ""))
	triesAfterFirst := len(prober.tries)
	probeSync(s, "cam-1", onvifCam("cam-1", ""))
	if n := len(prober.tries); n != triesAfterFirst {
		t.Errorf("second run made %d more attempts, want 0", n-triesAfterFirst)
	}
	if len(updater.calls) != 0 {
		t.Errorf("UpdateCamera calls = %d, want 0", len(updater.calls))
	}
	if got := ledger.claimed["cam-1"]; got != "failed|" {
		t.Errorf("ledger = %q, want failed|", got)
	}
}

func TestProbe_SkipsWhenPasswordProvided(t *testing.T) {
	t.Helper()
	ledger := &fakeLedger{}
	updater := &fakeUpdater{}
	prober := &fakeProber{}
	s := newTestService(t, config.CredentialProbeConfig{}, ledger, updater, prober)

	probeSync(s, "cam-1", onvifCam("cam-1", "user-set"))

	if len(prober.tries) != 0 {
		t.Errorf("attempts = %d, want 0 for camera with a password", len(prober.tries))
	}
	if len(ledger.claimed) != 0 {
		t.Errorf("ledger rows = %v, want none", ledger.claimed)
	}
}

func TestProbe_SkipsNonONVIFAndDisabled(t *testing.T) {
	t.Helper()
	ledger := &fakeLedger{}
	updater := &fakeUpdater{}
	prober := &fakeProber{}
	s := newTestService(t, config.CredentialProbeConfig{}, ledger, updater, prober)

	rtsp := config.CameraConfig{ID: "cam-1", Protocol: "rtsp_h264"}
	probeSync(s, "cam-1", rtsp)

	disabled := false
	s2 := newTestService(t, config.CredentialProbeConfig{Enabled: &disabled}, ledger, updater, prober)
	probeSync(s2, "cam-2", onvifCam("cam-2", ""))

	if len(prober.tries) != 0 {
		t.Errorf("attempts = %d, want 0", len(prober.tries))
	}
}

func TestProbe_InconclusiveAbortsRemainingCandidates(t *testing.T) {
	t.Helper()
	cfg := config.CredentialProbeConfig{Candidates: []config.CredentialProbeCandidate{
		{Username: "admin", Password: "admin"},
		{Username: "admin", Password: "12345"},
		{Username: "root", Password: "pass"},
	}}
	ledger := &fakeLedger{}
	updater := &fakeUpdater{}
	prober := &fakeProber{verdicts: map[string]onvif.CredentialVerdict{"admin/admin": onvif.CredentialInconclusive}}
	s := newTestService(t, cfg, ledger, updater, prober)

	probeSync(s, "cam-1", onvifCam("cam-1", ""))

	if n := len(prober.tries); n != 1 {
		t.Errorf("attempts = %d, want 1 (abort after inconclusive)", n)
	}
	if got := ledger.claimed["cam-1"]; got != "failed|" {
		t.Errorf("ledger = %q, want failed|", got)
	}
}

func TestProbe_LedgerFailureNeverProbes(t *testing.T) {
	t.Helper()
	ledger := &fakeLedger{fail: true}
	updater := &fakeUpdater{}
	prober := &fakeProber{verdicts: map[string]onvif.CredentialVerdict{"admin/admin": onvif.CredentialAccepted}}
	s := newTestService(t, config.CredentialProbeConfig{Candidates: []config.CredentialProbeCandidate{{Username: "admin", Password: "admin"}}}, ledger, updater, prober)

	probeSync(s, "cam-1", onvifCam("cam-1", ""))

	if n := len(prober.tries); n != 0 {
		t.Errorf("attempts = %d, want 0 when once-only cannot be enforced", n)
	}
}

func TestProbe_NotStartedIsNoOp(t *testing.T) {
	t.Helper()
	s := New(config.CredentialProbeConfig{}, &fakeLedger{}, &fakeUpdater{})
	// No Start() — ProbeEnrolled must be a no-op.
	s.ProbeEnrolled("cam-1", onvifCam("cam-1", ""))
	s.wg.Wait()
}
