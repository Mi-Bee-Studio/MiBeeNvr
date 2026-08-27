package autodiscover

// Coverage for the Adder end-to-end flow (#580): dedup, ignore scopes,
// enrichment failure fallback, classification, and enrollment — using a
// fake CameraEnroller and dead endpoints (EnrichDevice fails fast against
// 127.0.0.1 ports that are closed; the device fields set BEFORE enrichment
// survive, exercising both classification branches).

import (
	"context"
	"sync"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/camera"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// recordingEnroller records enrollment calls (unlike the existing
// fakeEnroller, whose AddCamera discards the config).
type recordingEnroller struct {
	mu       sync.Mutex
	added    []config.CameraConfig
	updated  []string
	restarts int
	addErr   error
}

func (f *recordingEnroller) AddCamera(ctx context.Context, cam config.CameraConfig) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.added = append(f.added, cam)
	if f.addErr != nil {
		return "", f.addErr
	}
	return "cam-" + cam.Name, nil
}

func (f *recordingEnroller) UpdateCamera(ctx context.Context, cameraID string, updates camera.CameraUpdate) (*config.CameraConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updated = append(f.updated, cameraID)
	return &config.CameraConfig{ID: cameraID}, nil
}

func (f *recordingEnroller) RestartRecorder(ctx context.Context, cameraID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restarts++
	return nil
}

func (f *recordingEnroller) snapshot() []config.CameraConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]config.CameraConfig(nil), f.added...)
}

// deadEndpoint points at a loopback port nothing listens on.
const deadEndpoint = "http://127.0.0.1:1/onvif/device_service"

func newExtraTestDB(t *testing.T) *storage.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.New(dir + "/test.db")
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	t.Cleanup(func() { db.Close() })
	return db
}

func TestHandleDiscovered_GuardsAndDedup(t *testing.T) {
	t.Parallel()
	db := newExtraTestDB(t)
	enroller := &recordingEnroller{}
	cfg := &config.AutoDiscoverConfig{}
	adder := NewAdder(cfg, enroller, db, nil)

	// No endpoint at all → early return, nothing enrolled.
	adder.HandleDiscovered(context.Background(), onvif.DiscoveredDevice{Name: "nowhere"})
	require.Empty(t, enroller.snapshot())

	// Ignore-scope match → skipped (own endpoint so the dedup window from
	// the no-endpoint case cannot mask the check).
	cfg.IgnoreScopes = []string{"legacyline"}
	adder.HandleDiscovered(context.Background(), onvif.DiscoveredDevice{
		Endpoint: "http://127.0.0.1:9/onvif/device_service",
		Scopes:   []string{"onvif://www.onvif.org/hardware/LegacyLine"},
	})
	require.Empty(t, enroller.snapshot())
	cfg.IgnoreScopes = nil

	// Dead endpoint, no pre-filled identity, no default creds →
	// pending_activation enrollment (enrich fails, classify falls back).
	adder.HandleDiscovered(context.Background(), onvif.DiscoveredDevice{
		Endpoint: deadEndpoint,
		Name:     "Front Porch",
	})
	added := enroller.snapshot()
	require.Len(t, added, 1)
	require.Equal(t, "pending_activation", added[0].ActivationState)
	require.Equal(t, "Front Porch", added[0].Name)
	require.Equal(t, deadEndpoint, added[0].ONVIFEndpoint)
	require.Equal(t, "onvif", added[0].Protocol)

	// The in-memory dedup window suppresses a chatty re-announcement.
	adder.HandleDiscovered(context.Background(), onvif.DiscoveredDevice{
		Endpoint: deadEndpoint,
		Name:     "Front Porch",
	})
	require.Len(t, enroller.snapshot(), 1)
}

func TestHandleDiscovered_EnrichedDeviceActivates(t *testing.T) {
	t.Parallel()
	db := newExtraTestDB(t)
	enroller := &recordingEnroller{}
	cfg := &config.AutoDiscoverConfig{}
	adder := NewAdder(cfg, enroller, db, nil)

	// Identity fields survive the (failing) enrichment → open-device
	// classification: active without credentials.
	adder.HandleDiscovered(context.Background(), onvif.DiscoveredDevice{
		Endpoint:     deadEndpoint,
		Name:         "MiBeeCam",
		Manufacturer: "MiBee",
		Serial:       "SER-42",
	})
	added := enroller.snapshot()
	require.Len(t, added, 1)
	require.Equal(t, "active", added[0].ActivationState)
	require.Equal(t, "SER-42", added[0].StableID)
	require.Empty(t, added[0].Username, "open device must not carry credentials")

	// Persisted dedup: the same serial arriving at a NEW endpoint must not
	// create a second camera (the DB row from the first enrollment matches by
	// stable_id once flushed via the storage layer).
	adder.HandleDiscovered(context.Background(), onvif.DiscoveredDevice{
		Endpoint: "http://127.0.0.1:2/onvif/device_service",
		Serial:   "SER-42",
	})
	require.Len(t, enroller.snapshot(), 1)
}

func TestHandleDiscovered_DefaultCredsProbeFails(t *testing.T) {
	t.Parallel()
	db := newExtraTestDB(t)
	enroller := &recordingEnroller{}
	cfg := &config.AutoDiscoverConfig{DefaultUsername: "admin", DefaultPassword: "secret"}
	adder := NewAdder(cfg, enroller, db, nil)

	adder.HandleDiscovered(context.Background(), onvif.DiscoveredDevice{
		Endpoint: deadEndpoint,
		Serial:   "SER-7",
	})
	added := enroller.snapshot()
	require.Len(t, added, 1)
	// Creds configured but the probe against a dead endpoint fails → pending.
	require.Equal(t, "pending_activation", added[0].ActivationState)
	require.Empty(t, added[0].Password, "pending camera must not persist the password")
}

func TestServiceAdderForTestAccessor(t *testing.T) {
	t.Parallel()
	db := newExtraTestDB(t)
	svc := New(&config.AutoDiscoverConfig{}, &recordingEnroller{}, db, nil)
	require.Equal(t, "autodiscover", svc.Name())
	require.NotNil(t, svc.AdderForTest())
}
