package xiaomi

// Coverage for the plugin surface + recorder pure accessors (#585): plugin
// wiring (NewRecorder config mapping incl. adaptive/audio-trigger gates),
// DID extraction, codec accessors on a constructed recorder, vendor-error
// reporting, and MotorControl guards. Hermetic — constructed, never dialed.

import (
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
	"github.com/stretchr/testify/require"
)

func newXiaomiTestStore(t *testing.T) *storage.Manager {
	t.Helper()
	m, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	return m
}

func TestXiaomiPluginSurface(t *testing.T) {
	t.Parallel()
	p := &XiaomiPlugin{}
	bus := event.NewEventBus(2)
	p.SetEventBus(bus)
	require.Equal(t, "xiaomi", p.Name())
	require.Equal(t, []string{"xiaomi"}, p.Protocols())
	require.Equal(t, config.XiaomiConfig{}, p.ConfigSchema())
	p.RegisterRoutes(nil) // routes live in api/handler.go; no-op here

	require.Equal(t, "655448418", extractDID("xiaomi://655448418"))
	require.Equal(t, "plain", extractDID("plain"))
}

func TestPluginNewRecorderConfigMapping(t *testing.T) {
	t.Parallel()
	p := &XiaomiPlugin{}
	store := newXiaomiTestStore(t)

	rec := p.NewRecorder(config.CameraConfig{
		ID: "cam-x", Protocol: "xiaomi", URL: "xiaomi://12345", AudioEnabled: true,
	}, store, nil)
	xr, ok := rec.(*XiaomiRecorder)
	require.True(t, ok)
	require.Equal(t, "cam-x", xr.cfg.CameraID)
	require.Equal(t, "12345", xr.cfg.DID)
	require.True(t, xr.cfg.AudioEnabled)

	// Explicit DID wins over the URL form.
	rec = p.NewRecorder(config.CameraConfig{ID: "c2", Protocol: "xiaomi", DID: "999", URL: "xiaomi://1"}, store, nil)
	xr = rec.(*XiaomiRecorder)
	require.Equal(t, "999", xr.cfg.DID)
}

func TestPluginNewRecorderAdaptiveGates(t *testing.T) {
	t.Parallel()
	p := &XiaomiPlugin{}
	store := newXiaomiTestStore(t)
	m := metrics.NewMetrics()

	// recording_mode: adaptive maps the adaptive config through.
	adaptive := &config.AdaptiveRecordingConfig{
		CalmThreshold: "5m", TimelapseInterval: "2s", SpikeFactor: 1.5, GOPBufferBytes: 4096,
	}
	rec := p.NewRecorder(config.CameraConfig{
		ID: "ad", Protocol: "xiaomi", DID: "1",
		RecordingMode: "adaptive", Adaptive: adaptive,
		AudioTrigger: &config.CameraAudioTriggerConfig{Enabled: true, MinDBFS: -35, PreCaptureS: 5},
	}, store, nil, m)
	xr := rec.(*XiaomiRecorder)
	require.NotNil(t, xr.cfg.Adaptive)
	require.NotNil(t, xr.cfg.AudioTrigger)
	require.Equal(t, -35.0, xr.cfg.AudioTrigger.MinDBFS)

	// Non-adaptive cameras get neither.
	rec = p.NewRecorder(config.CameraConfig{ID: "plain", Protocol: "xiaomi", DID: "2"}, store, nil)
	xr = rec.(*XiaomiRecorder)
	require.Nil(t, xr.cfg.Adaptive)
	require.Nil(t, xr.cfg.AudioTrigger)
}

func TestXiaomiRecorderAccessors(t *testing.T) {
	t.Parallel()
	rec := NewXiaomiRecorder(XiaomiRecorderConfig{CameraID: "c", DID: "1"}, newXiaomiTestStore(t))

	rec.Hub = streamhub.New() // wired by camera.initStreamHub in production
	require.NotNil(t, rec.GetHub())
	require.Nil(t, rec.SPS())
	require.Nil(t, rec.PPS())
	require.Nil(t, rec.VPS())
	require.Equal(t, "", rec.AudioCodec())
	require.Equal(t, 0, rec.AudioSampleRate())
	require.Equal(t, 0, rec.AudioChannels())

	// Error reporter wiring (TUTK vendor-error detection).
	rec.SetErrorReporter(nil)
	rec.reportVendorError(errFakeVendor) // nil reporter must be tolerated
}

func TestXiaomiMotorControlGuards(t *testing.T) {
	t.Parallel()
	rec := NewXiaomiRecorder(XiaomiRecorderConfig{CameraID: "c", DID: "1"}, newXiaomiTestStore(t))

	// No connection → explicit error, no panic.
	require.Error(t, rec.MotorControl("left", 5))
	require.Error(t, rec.MotorControl("stop", 0))
}

func TestSetCloudConfigPackages(t *testing.T) {
	// NOT parallel: SetCloudConfig writes the package-level cloudCfg that
	// concurrently-constructed recorders read — a parallel write raced them
	// under -race. Serialize against the other tests in this package.
	SetCloudConfig(config.XiaomiConfig{UserID: "u", Token: "t", Region: "cn"})
	require.Equal(t, "u", cloudCfg.UserID)
	require.Equal(t, "cn", cloudCfg.Region)
	SetCloudConfig(config.XiaomiConfig{})
}

var (
	_ = model.FormatH264 // keep model import if assertions evolve
	_ = time.Second
)

// errFakeVendor stands in for a TUTK/CS2 vendor error.
var errFakeVendor = &vendorErrorFake{}

type vendorErrorFake struct{}

func (e *vendorErrorFake) Error() string { return "vendor: boom" }
