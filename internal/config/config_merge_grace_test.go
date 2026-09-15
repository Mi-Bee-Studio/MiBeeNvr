package config

// merge.transcode_grace (#815 review red line): the transcode-hold age rail
// is an operator value, not a semantic constant — it sets how long a
// transcode camera's fresh segments defer folding (task-creation latency
// varies with slow disks / big fleets), and "0s"/"off" must give an exit
// back to pre-#811 folding behavior.

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyDefaults_TranscodeGrace(t *testing.T) {
	var cfg Config
	cfg.ApplyDefaults()
	require.Equal(t, "90s", cfg.Merge.TranscodeGrace)
}

func TestValidate_TranscodeGrace(t *testing.T) {
	for _, v := range []string{"off", "0s", "90s", "2m", "250ms"} {
		cfg := &Config{}
		cfg.ApplyDefaults()
		cfg.Merge.TranscodeGrace = v
		require.NoError(t, Validate(cfg), "value %q must validate", v)
	}
	for _, v := range []string{"banana", "-3s", "10"} {
		cfg := &Config{}
		cfg.ApplyDefaults()
		cfg.Merge.TranscodeGrace = v
		require.Error(t, Validate(cfg), "value %q must be rejected", v)
	}
}

func TestResolveMergeConfig_TranscodeGraceOverride(t *testing.T) {
	global := MergeConfig{TranscodeGrace: "90s"}
	camera := MergeConfig{TranscodeGrace: "5m"}
	require.Equal(t, "5m", ResolveMergeConfig(global, &camera).TranscodeGrace,
		"per-camera override wins")
	camera.TranscodeGrace = ""
	require.Equal(t, "90s", ResolveMergeConfig(global, &camera).TranscodeGrace,
		"empty per-camera inherits the global")
}
