package camera

import (
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/recorder"
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
		t.Fatal("initStreamHub did not set the hub via model.HubHost")
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
	_ model.HubHost = (*recorder.H264Recorder)(nil)
	_ model.HubHost = (*recorder.H265Recorder)(nil)
	_ model.HubHost = (*recorder.ONVIFRecorder)(nil)
	_ model.HubHost = (*recorder.MJPEGRecorder)(nil)
	_ model.HubHost = (*recorder.HTTPJPEGRecorder)(nil)
	_ model.HubHost = (*recorder.TimelapseRecorder)(nil)
	_ model.HubHost = (*recorder.StubRecorder)(nil)
	_ model.HubHost = (*recorder.IngestRecorder)(nil)
	_ model.HubHost = (*recorder.GB28181Recorder)(nil)
	_ model.HubHost = (*xiaomi.XiaomiRecorder)(nil)
)
