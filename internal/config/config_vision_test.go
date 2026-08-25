package config

import "testing"

func TestShouldSkipCamera(t *testing.T) {
	cfg := VisionConfig{SkipCameras: []string{"cam-a", "cam-b"}}
	if !cfg.ShouldSkipCamera("cam-a") || !cfg.ShouldSkipCamera("cam-b") {
		t.Fatal("listed cameras must be skipped")
	}
	if cfg.ShouldSkipCamera("cam-c") || cfg.ShouldSkipCamera("") {
		t.Fatal("unlisted cameras must not be skipped")
	}
	empty := VisionConfig{}
	if empty.ShouldSkipCamera("cam-a") {
		t.Fatal("nil/empty list skips nothing")
	}
}
