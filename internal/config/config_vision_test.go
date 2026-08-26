package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

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

// SubLayerCameraSet: skip_cameras wins over sub_layer_cameras — a camera the
// consumer explicitly rejected must not grow an analysis layer (#514).
func TestSubLayerCameraSet(t *testing.T) {
	cfg := VisionConfig{
		SubLayerCameras: []string{"cam-a", "cam-b", "cam-c"},
		SkipCameras:     []string{"cam-b"},
	}
	set := cfg.SubLayerCameraSet()
	require.True(t, set["cam-a"])
	require.True(t, set["cam-c"])
	require.False(t, set["cam-b"], "skip_cameras must override sub_layer_cameras")
	require.Len(t, set, 2)

	require.Empty(t, VisionConfig{}.SubLayerCameraSet())
}
