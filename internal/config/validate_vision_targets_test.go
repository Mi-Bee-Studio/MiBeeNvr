package config

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestValidateDanglingVisionTargetWarnsAndDrops is the regression guard for
// the 2026-09-11 M5 outage: removing a vision instance from the YAML while a
// camera still routes to it made Validate() fail fatally, bricking the NVR
// into a systemd restart loop (~2 min of recording downtime until the
// reference was fixed by hand). Load-time validation MUST tolerate dangling
// camera.vision_targets references — warn and drop them in place — mirroring
// the #216 stable_id precedent. Strict rejection stays at the API write
// boundary (camera create/update) and at the settings PUT (stranded-camera
// check); a hand-edited YAML is exactly the path those cannot cover.
func TestValidateDanglingVisionTargetWarnsAndDrops(t *testing.T) {
	cfg := &Config{
		Vision: VisionConfig{
			Enabled: true,
			Instances: []VisionInstance{
				{Name: "default", URL: "http://192.0.2.1:9091"},
				{Name: "gpu", URL: "http://192.0.2.2:9092"},
			},
		},
		Cameras: []CameraConfig{{
			ID:            "cam-test",
			URL:           "rtsp://x",
			Protocol:      "rtsp",
			VisionTargets: []string{"default", "ghost", "gpu"},
		}},
	}
	cfg.ApplyDefaults()

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate() must not brick startup on a dangling vision_targets reference: %v", err)
	}
	got := cfg.Cameras[0].VisionTargets
	if len(got) != 2 || got[0] != "default" || got[1] != "gpu" {
		t.Fatalf("dangling reference must be dropped in place, known ones kept: got %v, want [default gpu]", got)
	}
	if !strings.Contains(buf.String(), "ghost") {
		t.Fatalf("dropping must leave a warning trail naming the instance, got log: %q", buf.String())
	}
}

// TestValidateAllVisionTargetsDanglingFallsBackToBroadcast covers the edge of
// the drop rule: when every reference dangles, the camera's target list
// becomes empty, which RouteFor treats as broadcast-to-all-enabled-instances.
// That is the pre-existing runtime semantic for an empty list — the load-time
// drop must not invent a third "routes nowhere" state, but it must warn
// loudly because the routing scope silently widens.
func TestValidateAllVisionTargetsDanglingFallsBackToBroadcast(t *testing.T) {
	cfg := &Config{
		Vision: VisionConfig{
			Enabled: true,
			Instances: []VisionInstance{
				{Name: "gpu", URL: "http://192.0.2.2:9092"},
			},
		},
		Cameras: []CameraConfig{{
			ID:            "cam-test",
			URL:           "rtsp://x",
			Protocol:      "rtsp",
			VisionTargets: []string{"ghost"},
		}},
	}
	cfg.ApplyDefaults()

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate() must not brick startup when all vision_targets dangle: %v", err)
	}
	if n := len(cfg.Cameras[0].VisionTargets); n != 0 {
		t.Fatalf("fully dangling targets must drop to empty (= broadcast), got %d left", n)
	}
	if !strings.Contains(buf.String(), "broadcast") {
		t.Fatalf("broadcast fallback must be called out in the warning, got log: %q", buf.String())
	}
}

// TestValidateKnownVisionTargetsUntouched ensures the drop path is not
// trigger-happy: fully valid references survive Validate() untouched and
// without a warning.
func TestValidateKnownVisionTargetsUntouched(t *testing.T) {
	targets := []string{"gpu"}
	cfg := &Config{
		Vision: VisionConfig{
			Enabled: true,
			Instances: []VisionInstance{
				{Name: "gpu", URL: "http://192.0.2.2:9092"},
			},
		},
		Cameras: []CameraConfig{{
			ID:            "cam-test",
			URL:           "rtsp://x",
			Protocol:      "rtsp",
			VisionTargets: targets,
		}},
	}
	cfg.ApplyDefaults()

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate() errored on a fully valid config: %v", err)
	}
	if got := cfg.Cameras[0].VisionTargets; len(got) != 1 || got[0] != "gpu" {
		t.Fatalf("valid targets must survive untouched: got %v", got)
	}
	if s := buf.String(); strings.Contains(s, "vision") {
		t.Fatalf("no vision warning expected for valid targets, got log: %q", s)
	}
}
