package health

import (
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/stretchr/testify/require"
)

// TestCollectorLowFPSOverrideIsTheThermalFloor pins the dual-spectrum
// low-frame-rate contract: a thermal channel streams at 2-8fps (e.g. the
// MLX-class 32x24 module line), which the global Layer2 MinFPS=5 default
// would flag as "Low FPS detected" and feed auto-remediation restart loops.
// The per-camera health_overrides.min_fps=1 override must lower the floor so
// 2fps is healthy on the overridden camera while the default still flags an
// unoverridden camera fed the same cadence.
//
// Recorder-side note: segment finalization at sparse cadences is already
// pinned by the timelapse tests (one keyframe per 30s still rotates
// segments), which is far sparser than 2fps — the health floor is the only
// 2fps-sensitive threshold.
func TestCollectorLowFPSOverrideIsTheThermalFloor(t *testing.T) {
	windowSize := 1 * time.Second
	c, events := newCollector(t, windowSize)

	// The thermal channel's per-camera override: min_fps=1 (0 fields = keep
	// the global threshold for those axes).
	c.SetCameraOverride("cam-thermal", 0, 1.0, 0)

	thermal := c.OnFrame("cam-thermal")
	plain := c.OnFrame("cam-plain")

	// Two check windows, both cameras fed 2 frames per window (= 2fps):
	// the anomaly path needs a consecutive streak before emitting, so
	// drive both windows exactly like TestCollectorLowFPSAnomaly.
	for round := range 2 {
		for i := range 2 {
			au := makeH264Frame(t, 1, 500)
			thermal(int64(round*10+i), au)
			plain(int64(round*10+i), au)
		}
		c.CheckAndReset()
	}

	evts := getCollectorEvents(t, events)
	thermalFlagged, plainFlagged := false, false
	for _, e := range evts {
		if e.EventType != string(model.HealthEventStreamAnomaly) || e.Message != "Low FPS detected" {
			continue
		}
		switch e.CameraID {
		case "cam-thermal":
			thermalFlagged = true
		case "cam-plain":
			plainFlagged = true
		}
	}
	require.False(t, thermalFlagged,
		"2fps must be healthy under the thermal camera's min_fps=1 override")
	require.True(t, plainFlagged,
		"the same 2fps cadence must still flag a camera on the global MinFPS=5 default — the override is per-camera, not global")
}
