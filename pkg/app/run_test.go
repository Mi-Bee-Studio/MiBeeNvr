package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

func TestServiceFunc_NonBlockingStart(t *testing.T) {
	t.Helper()
	a := New()
	s := &serviceFunc{
		name: "slowpoke",
		startFunc: func(ctx context.Context) error {
			// Simulate a long-running background goroutine that returns
			// immediately from Start (the goroutine is the long part).
			go func() {
				select {
				case <-time.After(2 * time.Second):
				case <-ctx.Done():
				}
			}()
			return nil
		},
		stopFunc: func() error {
			return nil
		},
	}
	if err := a.Register(s); err != nil {
		t.Fatalf("Register: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		if err := a.Start(ctx); err != nil {
			t.Errorf("Start: %v", err)
		}
		close(done)
	}()

	select {
	case <-done:
		// Start returned within 500ms — good.
	case <-ctx.Done():
		t.Fatal("Start blocked for more than 500ms — serviceFunc.Start must not block on long-running goroutines")
	}
}

func TestServiceFunc_CtxCancelPropagates(t *testing.T) {
	t.Helper()
	a := New()

	unblocked := make(chan struct{})
	var capturedCancel context.CancelFunc

	s := &serviceFunc{
		name: "cancellable",
		startFunc: func(ctx context.Context) error {
			var runCtx context.Context
			runCtx, capturedCancel = context.WithCancel(ctx)
			go func() {
				<-runCtx.Done()
				close(unblocked)
			}()
			return nil
		},
		stopFunc: func() error {
			if capturedCancel != nil {
				capturedCancel()
			}
			return nil
		},
	}
	if err := a.Register(s); err != nil {
		t.Fatalf("Register: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := a.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Stop the service — this should cancel the goroutine via capturedCancel.
	if err := a.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	select {
	case <-unblocked:
		// Goroutine unblocked after Stop — correct.
	case <-time.After(time.Second):
		t.Fatal("goroutine did not unblock within 1s after Stop — context cancellation did not propagate")
	}
}

// minimalConfig returns a config with all optional services disabled and a
// temporary storage root.
func minimalConfig(t *testing.T) (*config.Config, string) {
	t.Helper()
	dir := t.TempDir()

	cfg := &config.Config{
		Server:        config.ServerConfig{Listen: ":0"},
		Storage:       config.StorageConfig{RootDir: dir, SegmentDuration: "30s"},
		Auth:          config.AuthConfig{Username: "admin", PasswordHash: "$2a$10$dummyhashfortesting"},
		Cameras:       []config.CameraConfig{},
		Cleanup:       config.CleanupConfig{RetentionDays: 30, CheckInterval: "1h", DiskThresholdPercent: 95},
		FTP:           config.FTPConfig{Port: 2121, PassivePortRange: "2122-2140"},
		WebDAV:        config.WebDAVConfig{PathPrefix: "/dav"},
		Observability: config.ObservabilityConfig{LogLevel: "info", LogFormat: "text"},
		Version:       "1.0",
		AI: config.AIConfig{
			Enabled:             false,
			ConfidenceThreshold: 0.5,
			FrameSkipRate:       10,
			EnabledCameras:      []string{},
		},
	}
	cfg.ApplyDefaults()

	// Disable all optional services
	falseVal := false
	cfg.MQTT.Enabled = false
	cfg.FTP.Enabled = &falseVal
	cfg.RTMP.Enabled = &falseVal
	cfg.SRT.Enabled = &falseVal
	cfg.Streaming.WebRTC.Enabled = &falseVal
	cfg.Streaming.FLV.Enabled = &falseVal
	cfg.Transcoding.Enabled = false
	cfg.RemoteLog.Enabled = false
	cfg.Health.Enabled = false

	return cfg, dir
}

func TestRunFree_ServiceOrder(t *testing.T) {
	t.Helper()
	cfg, _ := minimalConfig(t)

	a, err := RunFree(cfg, filepath.Join(cfg.Storage.RootDir, "mibee-nvr.yaml"))
	if err != nil {
		t.Fatalf("RunFree: %v", err)
	}

	svcs := a.Services()
	t.Logf("observed Services() = %v", svcs)
	t.Logf("service count = %d", len(svcs))

	// With all optionals disabled, expected core services:
	// db, camera, health, merge, mergeScheduler, cleanup, ws, hls
	// (health is always created even when Health.Enabled=false)
	expected := []string{"db", "camera", "health", "merge", "mergeScheduler", "cleanup", "ws", "hls"}
	if len(svcs) != len(expected) {
		t.Errorf("Services() count = %d, want %d", len(svcs), len(expected))
	}
	for i, want := range expected {
		if i >= len(svcs) {
			t.Errorf("Services()[%d] missing, want %q", i, want)
			continue
		}
		if svcs[i] != want {
			t.Errorf("Services()[%d] = %q, want %q", i, svcs[i], want)
		}
	}
}

func TestRunFree_SmokeStartStop(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping smoke test in short mode")
	}

	cfg, dir := minimalConfig(t)
	configPath := filepath.Join(dir, "mibee-nvr.yaml")
	// Write a minimal config file so config.Save doesn't fail
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatalf("config.Save: %v", err)
	}

	a, err := RunFree(cfg, configPath)
	if err != nil {
		t.Fatalf("RunFree: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := a.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := a.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
