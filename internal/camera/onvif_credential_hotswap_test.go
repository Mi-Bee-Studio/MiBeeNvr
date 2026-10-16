package camera

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/onvif"
)

// newManagerWithONVIFCamera builds a manager with one ONVIF-protocol camera
// (unreachable endpoint is fine — none of these tests dial).
func newManagerWithONVIFCamera(t *testing.T) (*CameraManager, string) {
	t.Helper()
	cfg := testConfig()
	cfg.Cameras = nil
	mgr, _ := newManagerWithBus(t, cfg)
	id, err := mgr.AddCamera(context.Background(), config.CameraConfig{
		ID: "onvif-cam", Name: "Onvif", Protocol: "onvif", Encoding: "h264",
		URL: "http://127.0.0.1:1/stream",
	})
	require.NoError(t, err)
	return mgr, id
}

// TestUpdateCameraEvictsCachedONVIFClientOnCredentialChange: the client cache
// keys on camera_id alone, so a credential-only update must evict the cached
// client — otherwise every SOAP call keeps the OLD password in memory until
// restart (found during the #922 push joint-debug: PUT updated credentials on
// disk but Subscribe kept 401-ing under the stale secret).
func TestUpdateCameraEvictsCachedONVIFClientOnCredentialChange(t *testing.T) {
	mgr, camID := newManagerWithONVIFCamera(t)

	// Simulate the established cached client (pre-update credentials). The
	// map is guarded by onvifMu — background goroutines from the update path
	// (recorder restart, subscription reconcile) access it concurrently.
	stale := onvif.NewClient("http://192.168.62.104:8080/onvif/device_service", "admin", "old-secret")
	mgr.onvifMu.Lock()
	mgr.onvifClients[camID] = stale
	mgr.onvifMu.Unlock()

	pw := "new-rotated-secret"
	_, err := mgr.UpdateCamera(context.Background(), camID, CameraUpdate{Password: &pw})
	require.NoError(t, err)

	// The OLD client must be gone. The cache may legitimately hold a FRESH
	// entry already (the recorder restart re-creates one with the new
	// secret) — what matters is the stale instance never survives.
	mgr.onvifMu.Lock()
	cur, ok := mgr.onvifClients[camID]
	mgr.onvifMu.Unlock()
	if ok {
		assert.NotSame(t, stale, cur,
			"credential-only update must evict the stale ONVIF client")
	}
}

// TestUpdateCameraKeepsCachedONVIFClientOnUnrelatedChange: name-only edits
// must NOT churn the cache (client reuse is deliberate — ESP32-class devices
// block under redundant capability handshakes).
func TestUpdateCameraKeepsCachedONVIFClientOnUnrelatedChange(t *testing.T) {
	mgr, camID := newManagerWithONVIFCamera(t)

	mgr.onvifMu.Lock()
	mgr.onvifClients[camID] = onvif.NewClient("http://192.168.62.104:8080/onvif/device_service", "admin", "secret")
	cached := mgr.onvifClients[camID]
	mgr.onvifMu.Unlock()

	name := "renamed"
	_, err := mgr.UpdateCamera(context.Background(), camID, CameraUpdate{Name: &name})
	require.NoError(t, err)

	mgr.onvifMu.Lock()
	cur := mgr.onvifClients[camID]
	mgr.onvifMu.Unlock()
	assert.Same(t, cached, cur,
		"unrelated update must keep the cached client")
}
