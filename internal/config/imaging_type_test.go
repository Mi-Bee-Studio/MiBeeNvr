package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestValidateCameraImagingType covers the imaging_type allowlist shared by
// the startup validation and the camera create/update API boundaries.
func TestValidateCameraImagingType(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		v       string
		wantErr bool
	}{
		{"empty defaults to visible", "", false},
		{"explicit visible", "visible", false},
		{"thermal", "thermal", false},
		{"garbage value rejected", "infrared", true},
		{"case-sensitive", "Thermal", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cam := CameraConfig{ID: "cam-1", Protocol: "rtsp", ImagingType: tc.v}
			err := ValidateCameraRecordingMode(cam)
			if tc.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), "imaging_type")
			} else {
				require.NoError(t, err)
			}
		})
	}
}
