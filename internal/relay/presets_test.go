package relay

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Built-in presets
// ---------------------------------------------------------------------------

func TestPreset_AllBuiltinsPresent(t *testing.T) {
	r := NewPresetRegistry()
	names := r.List()
	require.Len(t, names, 5)

	expected := map[string]bool{
		"bilibili": false, "douyin": false, "youtube": false,
		"kuaishou": false, "generic": false,
	}
	for _, n := range names {
		if _, ok := expected[n]; ok {
			expected[n] = true
		}
	}
	for name, found := range expected {
		assert.True(t, found, "builtin preset %q not found", name)
	}

	// Spot-check preset values.
	p, ok := r.Get("bilibili")
	require.True(t, ok)
	assert.Equal(t, "Bilibili live streaming", p.Description)
	assert.Equal(t, 4000, p.VideoBitrateKbps)
	assert.Equal(t, "aac", p.AudioCodecRequired)
	assert.Equal(t, 2, p.GopSeconds)

	p, ok = r.Get("douyin")
	require.True(t, ok)
	assert.Equal(t, "1080x1920", p.Resolution, "douyin should be vertical")
	assert.Equal(t, 3500, p.VideoBitrateKbps)

	p, ok = r.Get("youtube")
	require.True(t, ok)
	assert.Equal(t, "high", p.Profile)
	assert.Equal(t, 4500, p.VideoBitrateKbps)

	p, ok = r.Get("kuaishou")
	require.True(t, ok)
	assert.Equal(t, 2, p.Bframes)

	p, ok = r.Get("generic")
	require.True(t, ok)
	assert.Equal(t, "any", p.AudioCodecRequired)
	assert.Equal(t, 3000, p.VideoBitrateKbps)
	assert.Equal(t, "", p.URLHint)
}

func TestPreset_GetNonExistent(t *testing.T) {
	r := NewPresetRegistry()
	_, ok := r.Get("nonexistent")
	assert.False(t, ok)
}

// ---------------------------------------------------------------------------
// YAML loading
// ---------------------------------------------------------------------------

func TestPreset_LoadValidYAML(t *testing.T) {
	yamlContent := []byte(`
presets:
  custom:
    name: "custom"
    description: "Custom test preset"
    url_hint: "rtmp://push.example.com/live/stream"
    gop_seconds: 3
    video_bitrate_kbps: 5000
    audio_bitrate_kbps: 192
    resolution: "1280x720"
    framerate: 60
    profile: "high"
    bframes: 2
    audio_codec_required: "aac"
`)
	f := tempYAMLFile(t, yamlContent)

	r := NewPresetRegistry()
	err := r.Load(f)
	require.NoError(t, err)

	p, ok := r.Get("custom")
	require.True(t, ok)
	assert.Equal(t, "Custom test preset", p.Description)
	assert.Equal(t, 5000, p.VideoBitrateKbps)
	assert.Equal(t, "1280x720", p.Resolution)
	assert.Equal(t, 60, p.Framerate)
	assert.Equal(t, 3, p.GopSeconds)
	assert.Equal(t, "aac", p.AudioCodecRequired)
	assert.Equal(t, "rtmp://push.example.com/live/stream", p.URLHint)
}

func TestPreset_RejectInvalidURLScheme(t *testing.T) {
	yamlContent := []byte(`
presets:
  bad:
    name: "bad"
    url_hint: "http://evil.com/stream"
    video_bitrate_kbps: 1000
`)
	f := tempYAMLFile(t, yamlContent)

	r := NewPresetRegistry()
	err := r.Load(f)
	require.NoError(t, err)

	_, ok := r.Get("bad")
	assert.False(t, ok, "preset with invalid URL scheme should be rejected")

	// Builtins must remain after fallback.
	_, ok = r.Get("generic")
	assert.True(t, ok)
}

func TestPreset_RejectEnvVarExpansion(t *testing.T) {
	yamlContent := []byte(`
presets:
  evil:
    name: "evil"
    url_hint: "rtmp://${HOME}/live/stream"
    video_bitrate_kbps: 1000
`)
	f := tempYAMLFile(t, yamlContent)

	r := NewPresetRegistry()
	err := r.Load(f)
	require.NoError(t, err)

	_, ok := r.Get("evil")
	assert.False(t, ok, "preset with env-var in URL should be rejected")

	_, ok = r.Get("generic")
	assert.True(t, ok, "builtins should remain after env-var rejection")
}

func TestPreset_RejectSubshellEnvVar(t *testing.T) {
	yamlContent := []byte(`
presets:
  subshell:
    name: "subshell"
    url_hint: "rtmp://$(HOME)/live/stream"
    video_bitrate_kbps: 1000
`)
	f := tempYAMLFile(t, yamlContent)

	r := NewPresetRegistry()
	err := r.Load(f)
	require.NoError(t, err)

	_, ok := r.Get("subshell")
	assert.False(t, ok, "preset with subshell env-var should be rejected")
}

func TestPreset_FallbackOnMissingFile(t *testing.T) {
	r := NewPresetRegistry()
	err := r.Load("/nonexistent/presets.yaml")
	require.NoError(t, err)

	_, ok := r.Get("generic")
	assert.True(t, ok, "builtins should remain after missing file")
	assert.Equal(t, 5, len(r.List()))
}

func TestPreset_FallbackOnInvalidYAML(t *testing.T) {
	yamlContent := []byte(`{invalid: yaml: broken`)
	f := tempYAMLFile(t, yamlContent)

	r := NewPresetRegistry()
	err := r.Load(f)
	require.NoError(t, err)

	assert.Equal(t, 5, len(r.List()), "should fall back to builtins on invalid YAML")
}

// ---------------------------------------------------------------------------
// Resolve
// ---------------------------------------------------------------------------

func TestPreset_ResolveMergePriority(t *testing.T) {
	r := NewPresetRegistry()

	cfg := PushTargetConfig{
		Platform: "youtube",
		VideoPresetOverride: &VideoPresetOverrides{
			Resolution:       "1280x720",
			Framerate:        60,
			VideoBitrateKbps: 6000,
			GopSeconds:       4,
			Profile:          "baseline",
			Bframes:          0, // zero = don't override
		},
	}
	result := r.Resolve(cfg)

	assert.Equal(t, "youtube", result.Name)
	assert.Equal(t, "1280x720", result.Resolution, "override should win")
	assert.Equal(t, 60, result.Framerate, "override should win")
	assert.Equal(t, 6000, result.VideoBitrateKbps, "override should win")
	assert.Equal(t, 4, result.GopSeconds, "override should win")
	assert.Equal(t, "baseline", result.Profile, "override should win")
	// Bframes=0 means "don't override" — preset value should remain.
	assert.Equal(t, 2, result.Bframes, "Bframes should come from youtube preset")
	// AudioBitrateKbps is not overridable — comes from preset.
	assert.Equal(t, 128, result.AudioBitrateKbps, "audio bitrate should come from preset")
}

func TestPreset_ResolveUnknownPlatform(t *testing.T) {
	r := NewPresetRegistry()
	cfg := PushTargetConfig{Platform: "nonexistent"}
	result := r.Resolve(cfg)

	assert.Equal(t, "generic", result.Name)
	assert.Equal(t, 3000, result.VideoBitrateKbps)
	assert.Equal(t, 128, result.AudioBitrateKbps)
	assert.Equal(t, 2, result.GopSeconds)
	assert.Equal(t, 30, result.Framerate)
}

func TestPreset_ResolveEmptyPlatform(t *testing.T) {
	r := NewPresetRegistry()
	cfg := PushTargetConfig{Platform: ""}
	result := r.Resolve(cfg)

	assert.Equal(t, "generic", result.Name, "empty platform should resolve to generic")
}

func TestPreset_ResolveNoOverride(t *testing.T) {
	r := NewPresetRegistry()
	cfg := PushTargetConfig{Platform: "bilibili"}
	result := r.Resolve(cfg)

	assert.Equal(t, "bilibili", result.Name)
	assert.Equal(t, 4000, result.VideoBitrateKbps)
	assert.Equal(t, 2, result.GopSeconds)
	assert.Equal(t, 128, result.AudioBitrateKbps)
	assert.Equal(t, "1920x1080", result.Resolution)
	assert.Equal(t, 30, result.Framerate)
	assert.Equal(t, "main", result.Profile)
	assert.Equal(t, 2, result.Bframes)
}

func TestPreset_ResolveOverrideBframesSet(t *testing.T) {
	r := NewPresetRegistry()
	cfg := PushTargetConfig{
		Platform: "bilibili",
		VideoPresetOverride: &VideoPresetOverrides{
			Bframes: 1,
		},
	}
	result := r.Resolve(cfg)
	assert.Equal(t, 1, result.Bframes, "Bframes=1 should override preset value of 2")
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// tempYAMLFile writes content to a temporary file and returns its path.
// The file and its temp directory are cleaned up at the end of the test.
func tempYAMLFile(t *testing.T, content []byte) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "presets-*.yaml")
	require.NoError(t, err)
	_, err = f.Write(content)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	return f.Name()
}
