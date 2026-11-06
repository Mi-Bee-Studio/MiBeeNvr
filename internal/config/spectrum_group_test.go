package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestValidateSpectrumGroup covers the spectrum_group allowlist shared by the
// startup config validation and the camera create/update API boundaries:
// empty (no explicit channel link) or 1-64 runes of [A-Za-z0-9._-].
func TestValidateSpectrumGroup(t *testing.T) {
	for _, v := range []string{"", "rig-7f", "dualcam.a_1", strings.Repeat("a", 64)} {
		require.True(t, ValidSpectrumGroup(v), "expected %q to be valid", v)
	}
	for _, v := range []string{"has space", "半角のみ", "sl/ash", strings.Repeat("a", 65)} {
		require.False(t, ValidSpectrumGroup(v), "expected %q to be invalid", v)
	}

	// Rejected through the shared per-camera validator (the wire-facing path).
	err := ValidateCameraRecordingMode(CameraConfig{ID: "cam-1", Protocol: "gb28181", SpectrumGroup: "bad group"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "spectrum_group")
}
