package camera

import (
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// TestSegmentDurFor (#758): the effective rotation duration resolves
// per-camera override first, global second, recorder default last; a bad
// override (only reachable via unvalidated hand-built configs) degrades to
// the global.
func TestSegmentDurFor(t *testing.T) {
	cfg := &config.Config{}
	cfg.Storage.SegmentDuration = "120s"
	mgr := NewCameraManager(cfg, nil, nil, "")

	cases := []struct {
		name string
		cam  config.CameraConfig
		want time.Duration
	}{
		{"no override → global", config.CameraConfig{ID: "a"}, 120 * time.Second},
		{"valid override wins", config.CameraConfig{ID: "b", SegmentDuration: "15s"}, 15 * time.Second},
		{"garbage override → global", config.CameraConfig{ID: "c", SegmentDuration: "banana"}, 120 * time.Second},
		{"negative override → global", config.CameraConfig{ID: "d", SegmentDuration: "-5s"}, 120 * time.Second},
	}
	for _, tc := range cases {
		if got := mgr.segmentDurFor(tc.cam); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}

	// Unparseable global (unvalidated config) → recorder default, never zero.
	bad := &config.Config{}
	bad.Storage.SegmentDuration = "???"
	mgr2 := NewCameraManager(bad, nil, nil, "")
	if got := mgr2.segmentDurFor(config.CameraConfig{ID: "e"}); got <= 0 {
		t.Errorf("unparseable global: got %v, want positive default", got)
	}
}
