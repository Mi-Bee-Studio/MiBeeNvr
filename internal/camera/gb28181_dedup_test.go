package camera

import (
	"context"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/stretchr/testify/require"
)

// stubProbe replaces the network serial probe for the test and returns a
// restore func (also clears the in-memory fingerprint cache).
func stubProbe(serial string, ok bool) func() {
	old := probeGBSerial
	probeGBSerial = func(context.Context, string) (string, bool) { return serial, ok }
	return func() {
		probeGBSerial = old
		clear(gbSerialCache)
	}
}

// gbDedupConfig builds a config with one ONVIF camera at a fixed IP.
func gbDedupConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := testConfig()
	cfg.Cameras = []config.CameraConfig{{
		ID:            "front-onvif",
		Name:          "Front ONVIF",
		Protocol:      "onvif",
		URL:           "",
		ONVIFEndpoint: "http://192.168.63.240/onvif/device_service",
		StableID:      "NC00000001",
	}}
	return cfg
}

func TestCameraIDByHostIP(t *testing.T) {
	mgr, _, _, _ := newTestManagerWithCfg(t, gbDedupConfig(t))

	id, ok := mgr.CameraIDByHostIP("192.168.63.240")
	require.True(t, ok)
	require.Equal(t, "front-onvif", id)

	_, ok = mgr.CameraIDByHostIP("192.168.63.999")
	require.False(t, ok)
	_, ok = mgr.CameraIDByHostIP("")
	require.False(t, ok, "empty IP must never match")
}

func TestCameraIDByHostIP_IgnoresGB28181Cameras(t *testing.T) {
	cfg := gbDedupConfig(t)
	cfg.Cameras = append(cfg.Cameras, config.CameraConfig{
		ID:       "gb-34020000001320000001",
		Protocol: "gb28181",
		GB28181:  config.GB28181ChannelConfig{DeviceID: "d", ChannelID: "34020000001320000001"},
	})
	mgr, _, _, _ := newTestManagerWithCfg(t, cfg)

	// GB cameras have no URL; they must not participate in IP matching.
	_, ok := mgr.CameraIDByHostIP("192.168.63.240")
	require.True(t, ok)
}

func TestCameraIDBySerial(t *testing.T) {
	mgr, _, _, _ := newTestManagerWithCfg(t, gbDedupConfig(t))

	id, ok := mgr.CameraIDBySerial("NC00000001")
	require.True(t, ok)
	require.Equal(t, "front-onvif", id)

	_, ok = mgr.CameraIDBySerial("")
	require.False(t, ok)
	_, ok = mgr.CameraIDBySerial("other")
	require.False(t, ok)
}

func TestEnsureGB28181Camera_SkipsWhenHostCameraExists(t *testing.T) {
	// Scenarios 1/3: the camera registered via GB28181 from the SAME IP it
	// streams ONVIF from — L1 catches it without probing.
	restore := stubProbe("", false)
	defer restore()

	mgr, _, _, _ := newTestManagerWithCfg(t, gbDedupConfig(t))

	require.NoError(t, mgr.EnsureGB28181Camera(
		"34020000001310000001", "34020000001320000001", "GB Channel", "192.168.63.240"))

	// No gb- camera created; the ONVIF camera is untouched.
	_, ok := mgr.GB28181CameraIDByChannel("34020000001310000001", "34020000001320000001")
	require.False(t, ok, "auto-enroll must be suppressed on host collision")
	snap := mgr.loadSnapshot()
	require.Len(t, snap.configs, 1)
}

func TestEnsureGB28181Camera_SkipsBySerialAcrossInterfaces(t *testing.T) {
	// Scenarios 2/4: GB28181 registers from interface .152 while the ONVIF
	// camera streams from .240 (dual-NIC device). L1 misses; L2 probes the
	// SIP source IP, gets the serial, and matches the camera's StableID.
	restore := stubProbe("NC00000001", true)
	defer restore()

	mgr, _, _, _ := newTestManagerWithCfg(t, gbDedupConfig(t))

	require.NoError(t, mgr.EnsureGB28181Camera(
		"34020000001310000001", "34020000001320000001", "GB Channel", "192.168.63.152"))

	_, ok := mgr.GB28181CameraIDByChannel("34020000001310000001", "34020000001320000001")
	require.False(t, ok, "auto-enroll must be suppressed on serial match")
	require.Len(t, mgr.loadSnapshot().configs, 1)
}

func TestEnsureGB28181Camera_EnrollsWhenNoCollision(t *testing.T) {
	restore := stubProbe("", false) // no ONVIF reachable: pure-GB camera
	defer restore()

	mgr, _, _, _ := newTestManagerWithCfg(t, gbDedupConfig(t))

	require.NoError(t, mgr.EnsureGB28181Camera(
		"34020000001310000009", "34020000001320000009", "", "192.168.63.9"))

	id, ok := mgr.GB28181CameraIDByChannel("34020000001310000009", "34020000001320000009")
	require.True(t, ok)
	require.Equal(t, "gb-34020000001320000009", id)
}

func TestEnsureGB28181Camera_EmptySourceIPStillEnrolls(t *testing.T) {
	// Unknown source ("" — e.g. device resolved before NetAddr recorded):
	// behave like today, enroll by channel identity.
	restore := stubProbe("", false)
	defer restore()

	mgr, _, _, _ := newTestManagerWithCfg(t, gbDedupConfig(t))

	require.NoError(t, mgr.EnsureGB28181Camera(
		"34020000001310000002", "34020000001320000002", "", ""))

	_, ok := mgr.GB28181CameraIDByChannel("34020000001310000002", "34020000001320000002")
	require.True(t, ok)
}

func TestEnsureGB28181Camera_SerialMismatchEnrolls(t *testing.T) {
	// Probe answers a DIFFERENT serial (another physical device behind the
	// same IP): no match → enroll normally.
	restore := stubProbe("OTHER-DEVICE-99", true)
	defer restore()

	mgr, _, _, _ := newTestManagerWithCfg(t, gbDedupConfig(t))

	require.NoError(t, mgr.EnsureGB28181Camera(
		"34020000001310000003", "34020000001320000003", "", "192.168.63.9"))

	_, ok := mgr.GB28181CameraIDByChannel("34020000001310000003", "34020000001320000003")
	require.True(t, ok)
}

func TestResolveGBDeviceSerial_CachesAndPersists(t *testing.T) {
	restore := stubProbe("NC00000001", true)
	defer restore()

	mgr, _, db, _ := newTestManagerWithCfg(t, gbDedupConfig(t))

	serial, ok := mgr.resolveGBDeviceSerial("34020000001310000077", "192.168.63.152")
	require.True(t, ok)
	require.Equal(t, "NC00000001", serial)

	fp, err := db.GetGB28181Fingerprint(context.Background(), "34020000001310000077")
	require.NoError(t, err)
	require.NotNil(t, fp, "fingerprint must be persisted for the reverse dedup path")
	require.Equal(t, "NC00000001", fp.Serial)
	require.Equal(t, "192.168.63.152", fp.SourceIP)

	// Second resolve must come from the in-memory cache (probe stays unused).
	calls := 0
	probeGBSerial = func(context.Context, string) (string, bool) {
		calls++
		return "NC00000001", true
	}
	serial, ok = mgr.resolveGBDeviceSerial("34020000001310000077", "192.168.63.152")
	require.True(t, ok)
	require.Equal(t, "NC00000001", serial)
	require.Zero(t, calls)
}
