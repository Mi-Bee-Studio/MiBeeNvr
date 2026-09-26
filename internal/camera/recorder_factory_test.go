package camera

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/xiaomi"
)

// knownProtocols is every protocol the config layer accepts. When a new
// model.Proto* constant is added, extend this list — the test then forces a
// matching entry in recorderBuilders so createRecorder can never silently
// return nil for a valid protocol.
var knownProtocols = []model.Protocol{
	model.ProtoONVIF,
	model.ProtoXiaomi,
	model.ProtoTimelapse,
	model.ProtoRTSP,
	model.ProtoHTTP,
	model.ProtoSRT,
	model.ProtoRTMP,
	model.ProtoWHIP,
	model.ProtoGB28181,
}

func TestRecorderBuildersCoverAllProtocols(t *testing.T) {
	for _, p := range knownProtocols {
		if _, ok := recorderBuilders[string(p)]; !ok {
			t.Errorf("protocol %q has no recorder builder registered in recorderBuilders", p)
		}
	}
	// No stale entries either: every registered key is a known protocol.
	known := make(map[string]bool, len(knownProtocols))
	for _, p := range knownProtocols {
		known[string(p)] = true
	}
	for key := range recorderBuilders {
		if !known[key] {
			t.Errorf("recorderBuilders has entry %q which is not a known model protocol", key)
		}
	}
}

func TestInitStreamHubViaHubHost(t *testing.T) {
	rec := &recorder.StubRecorder{}
	initStreamHub(rec, "test-cam", nil)
	hub := rec.GetHub()
	if hub == nil {
		t.Fatal("initStreamHub did not set the hub via streamhub.HubHost")
	}
	// The recorder's own HubSource must label the hub (flow-path view).
	if got := rec.HubSource(); got != "stub" {
		t.Errorf("HubSource() = %q, want %q", got, "stub")
	}
	if got := getRecorderHub(rec); got != hub {
		t.Errorf("getRecorderHub returned %p, want the hub set by initStreamHub (%p)", got, hub)
	}
}

// Compile-time guards: every recorder type used by the builders must satisfy
// the hub interfaces the camera manager relies on.
var (
	_ streamhub.HubHost = (*recorder.H264Recorder)(nil)
	_ streamhub.HubHost = (*recorder.H265Recorder)(nil)
	_ streamhub.HubHost = (*recorder.ONVIFRecorder)(nil)
	_ streamhub.HubHost = (*recorder.MJPEGRecorder)(nil)
	_ streamhub.HubHost = (*recorder.HTTPJPEGRecorder)(nil)
	_ streamhub.HubHost = (*recorder.TimelapseRecorder)(nil)
	_ streamhub.HubHost = (*recorder.StubRecorder)(nil)
	_ streamhub.HubHost = (*recorder.IngestRecorder)(nil)
	_ streamhub.HubHost = (*recorder.GB28181Recorder)(nil)
	_ streamhub.HubHost = (*xiaomi.XiaomiRecorder)(nil)
)

// TestBuildGB28181Recorder_AdaptiveWiring guards recording_mode=adaptive
// reaching the GB28181 recorder's write gate: armed when set, unarmed
// otherwise. A silent miss here would show "adaptive" in the UI while the
// camera keeps recording at full rate.
func TestBuildGB28181Recorder_AdaptiveWiring(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{
			RootDir:         filepath.Join(tmpDir, "storage"),
			SegmentDuration: "1m",
		},
	}
	store, err := storage.NewManager(cfg.Storage.RootDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.CleanupTempFiles()
	cm := NewCameraManager(cfg, store, nil, "")

	armed := cm.buildGB28181Recorder(config.CameraConfig{
		ID:            "gb-armed",
		Protocol:      string(model.ProtoGB28181),
		Encoding:      "h264",
		RecordingMode: "adaptive",
	}, time.Minute)
	gb, ok := armed.(*recorder.GB28181Recorder)
	if !ok {
		t.Fatalf("want *recorder.GB28181Recorder, got %T", armed)
	}
	if !gb.AdaptiveArmed() {
		t.Fatal("recording_mode=adaptive must arm the write gate")
	}

	plain := cm.buildGB28181Recorder(config.CameraConfig{
		ID:       "gb-plain",
		Protocol: string(model.ProtoGB28181),
		Encoding: "h264",
	}, time.Minute)
	gb2, ok := plain.(*recorder.GB28181Recorder)
	if !ok {
		t.Fatalf("want *recorder.GB28181Recorder, got %T", plain)
	}
	if gb2.AdaptiveArmed() {
		t.Fatal("continuous mode must not arm the write gate")
	}
}
