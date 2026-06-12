package config

import (
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/ai"
	"github.com/stretchr/testify/require"
)

func TestValidateAIConfig_ValidDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	// With AI disabled, validation should pass
	err := Validate(cfg)
	require.NoError(t, err)

	// Now enable AI with defaults — should also pass
	cfg.AI.Enabled = true
	err = Validate(cfg)
	require.NoError(t, err)
}

func TestValidateAIConfig_InvalidConfidence(t *testing.T) {
	cfg := &Config{AI: AIConfig{Enabled: true, FrameSkipRate: 10, ConfidenceThreshold: 1.5}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "confidence_threshold")
}

func TestValidateAIConfig_NegativeConfidence(t *testing.T) {
	cfg := &Config{AI: AIConfig{Enabled: true, FrameSkipRate: 10}}
	cfg.ApplyDefaults()
	cfg.AI.ConfidenceThreshold = -0.1 // override default
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "confidence_threshold")
}

func TestValidateAIConfig_ZeroFrameSkipRate(t *testing.T) {
	cfg := &Config{AI: AIConfig{Enabled: true, FrameSkipRate: 0, ConfidenceThreshold: 0.5}}
	cfg.ApplyDefaults()
	cfg.AI.FrameSkipRate = 0 // override default
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "frame_skip_rate")
}

func TestValidateAIConfig_NegativeFrameSkipRate(t *testing.T) {
	cfg := &Config{AI: AIConfig{Enabled: true, ConfidenceThreshold: 0.5}}
	cfg.ApplyDefaults()
	cfg.AI.FrameSkipRate = -1 // override default
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "frame_skip_rate")
}

func TestValidateAIConfig_InvalidZonePoints(t *testing.T) {
	cfg := &Config{AI: AIConfig{
		Enabled:             true,
		FrameSkipRate:       10,
		ConfidenceThreshold: 0.5,
		Zones: map[string][]ai.ROI{
			"cam1": {
				{Name: "zone1", Points: [][2]float64{{0.1, 0.1}, {0.2, 0.2}}}, // only 2 points
			},
		},
	}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "at least 3 points")
}

func TestValidateAIConfig_EmptyZoneName(t *testing.T) {
	cfg := &Config{AI: AIConfig{
		Enabled:             true,
		FrameSkipRate:       10,
		ConfidenceThreshold: 0.5,
		Zones: map[string][]ai.ROI{
			"cam1": {
				{Name: "", Points: [][2]float64{{0.1, 0.1}, {0.2, 0.2}, {0.3, 0.3}}},
			},
		},
	}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "name must not be empty")
}

func TestValidateAIConfig_InvalidZoneCoordinates(t *testing.T) {
	cfg := &Config{AI: AIConfig{
		Enabled:             true,
		FrameSkipRate:       10,
		ConfidenceThreshold: 0.5,
		Zones: map[string][]ai.ROI{
			"cam1": {
				{Name: "zone1", Points: [][2]float64{{0.1, 0.1}, {0.2, 0.2}, {1.5, 0.3}}}, // 1.5 > 1
			},
		},
	}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "outside [0,1] range")
}

func TestValidateAIConfig_NegativeZoneCoordinate(t *testing.T) {
	cfg := &Config{AI: AIConfig{
		Enabled:             true,
		FrameSkipRate:       10,
		ConfidenceThreshold: 0.5,
		Zones: map[string][]ai.ROI{
			"cam1": {
				{Name: "zone1", Points: [][2]float64{{-0.1, 0.1}, {0.2, 0.2}, {0.3, 0.3}}}, // -0.1 < 0
			},
		},
	}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "outside [0,1] range")
}

func TestValidateAIConfig_EmptyCameraID(t *testing.T) {
	cfg := &Config{AI: AIConfig{
		Enabled:             true,
		FrameSkipRate:       10,
		ConfidenceThreshold: 0.5,
		Zones: map[string][]ai.ROI{
			"": {
				{Name: "zone1", Points: [][2]float64{{0.1, 0.1}, {0.2, 0.2}, {0.3, 0.3}}},
			},
		},
	}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "camera ID must not be empty")
}

func TestValidateAIConfig_EmptyEnabledCamera(t *testing.T) {
	cfg := &Config{AI: AIConfig{
		Enabled:             true,
		FrameSkipRate:       10,
		ConfidenceThreshold: 0.5,
		EnabledCameras:      []string{"cam1", ""},
	}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "enabled_cameras[1] must be non-empty")
}

func TestValidateAIConfig_DisabledSkipsValidation(t *testing.T) {
	cfg := &Config{AI: AIConfig{
		Enabled:             false,
		FrameSkipRate:       0,
		ConfidenceThreshold: 1.5, // invalid if enabled
		Zones: map[string][]ai.ROI{
			"cam1": {
				{Name: "", Points: [][2]float64{{0.1, 0.1}}}, // invalid if enabled
			},
		},
	}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.NoError(t, err, "validation should be skipped when AI is disabled")
}

func TestApplyDefaults_AI(t *testing.T) {
	cfg := &Config{}
	cfg.ApplyDefaults()
	require.Equal(t, 0.5, cfg.AI.ConfidenceThreshold)
	require.Equal(t, 10, cfg.AI.FrameSkipRate)
	require.NotNil(t, cfg.AI.Zones)
	require.Empty(t, cfg.AI.Zones)
	require.Empty(t, cfg.AI.EnabledCameras)
	require.False(t, cfg.AI.Enabled)
}

func TestApplyDefaults_AI_PreservesExplicitValues(t *testing.T) {
	cfg := &Config{AI: AIConfig{
		ConfidenceThreshold: 0.8,
		FrameSkipRate:       20,
		Enabled:             true,
	}}
	cfg.ApplyDefaults()
	require.Equal(t, 0.8, cfg.AI.ConfidenceThreshold, "should preserve explicit value")
	require.Equal(t, 20, cfg.AI.FrameSkipRate, "should preserve explicit value")
	require.True(t, cfg.AI.Enabled)
}

func TestValidateAIConfig_ValidZones(t *testing.T) {
	cfg := &Config{AI: AIConfig{
		Enabled:             true,
		FrameSkipRate:       10,
		ConfidenceThreshold: 0.5,
		Zones: map[string][]ai.ROI{
			"cam1": {
				{
					Name:   "driveway",
					Points: [][2]float64{{0.1, 0.1}, {0.5, 0.1}, {0.5, 0.5}, {0.1, 0.5}},
				},
				{
					Name:   "front-door",
					Points: [][2]float64{{0.6, 0.6}, {0.9, 0.6}, {0.9, 0.9}, {0.6, 0.9}},
				},
			},
			"cam2": {
				{
					Name:   "entrance",
					Points: [][2]float64{{0.2, 0.2}, {0.8, 0.2}, {0.8, 0.8}, {0.2, 0.8}},
				},
			},
		},
	}}
	cfg.ApplyDefaults()
	err := Validate(cfg)
	require.NoError(t, err)
}
