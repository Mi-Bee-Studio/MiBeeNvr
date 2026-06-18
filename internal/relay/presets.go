package relay

import (
	"log/slog"
	"net/url"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Structures
// ---------------------------------------------------------------------------

// Preset defines encoding parameters for a live streaming platform.
// Fields are populated either from YAML or from built-in defaults.
type Preset struct {
	Name               string `json:"name"`
	Description        string `json:"description"`
	URLHint            string `json:"url_hint"`
	GopSeconds         int    `json:"gop_seconds"`
	VideoBitrateKbps   int    `json:"video_bitrate_kbps"`
	AudioBitrateKbps   int    `json:"audio_bitrate_kbps"`
	Resolution         string `json:"resolution"`
	Framerate          int    `json:"framerate"`
	Profile            string `json:"profile"`
	Bframes            int    `json:"bframes"`
	AudioCodecRequired string `json:"audio_codec_required"`
}

// ResolvedPreset is the fully-resolved set of encoding parameters after
// merging a platform preset with per-target overrides.
type ResolvedPreset struct {
	Name             string
	GopSeconds       int
	VideoBitrateKbps int
	AudioBitrateKbps int
	Resolution       string
	Framerate        int
	Profile          string
	Bframes          int
}

// PresetRegistry loads platform presets from YAML and provides 5 built-in
// defaults.  On Load failure the registry falls back to the built-in set
// rather than returning an error.
type PresetRegistry struct {
	presets map[string]Preset
}

// presetYAML mirrors Preset with YAML struct tags for strict file decoding.
type presetYAML struct {
	Name               string `yaml:"name"`
	Description        string `yaml:"description"`
	URLHint            string `yaml:"url_hint"`
	GopSeconds         int    `yaml:"gop_seconds"`
	VideoBitrateKbps   int    `yaml:"video_bitrate_kbps"`
	AudioBitrateKbps   int    `yaml:"audio_bitrate_kbps"`
	Resolution         string `yaml:"resolution"`
	Framerate          int    `yaml:"framerate"`
	Profile            string `yaml:"profile"`
	Bframes            int    `yaml:"bframes"`
	AudioCodecRequired string `yaml:"audio_codec_required"`
}

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

// NewPresetRegistry creates a registry initialised with the 5 built-in presets.
func NewPresetRegistry() *PresetRegistry {
	return &PresetRegistry{
		presets: copyBuiltinPresets(),
	}
}

// ---------------------------------------------------------------------------
// Load
// ---------------------------------------------------------------------------

// Load reads platform presets from a YAML file.  On any failure (read, parse,
// validation) it logs a warning and falls back to built-in defaults.
func (r *PresetRegistry) Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Warn("relay/presets: cannot load preset file, using built-in defaults",
			"path", path, "error", err)
		r.presets = copyBuiltinPresets()
		return nil
	}

	var raw struct {
		Presets map[string]presetYAML `yaml:"presets"`
	}

	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		slog.Warn("relay/presets: invalid preset YAML, using built-in defaults",
			"path", path, "error", err)
		r.presets = copyBuiltinPresets()
		return nil
	}

	loaded := make(map[string]Preset, len(raw.Presets))
	for key, py := range raw.Presets {
		p, ok := py.validateAndConvert(key)
		if !ok {
			slog.Warn("relay/presets: skipping invalid preset", "key", key)
			continue
		}
		loaded[key] = p
	}

	if len(loaded) == 0 {
		slog.Warn("relay/presets: no valid presets in file, using built-in defaults")
		r.presets = copyBuiltinPresets()
		return nil
	}

	r.presets = loaded
	return nil
}

// ---------------------------------------------------------------------------
// Get / List / Resolve
// ---------------------------------------------------------------------------

// Get returns the preset with the given name and a boolean indicating
// whether it exists.
func (r *PresetRegistry) Get(name string) (Preset, bool) {
	p, ok := r.presets[name]
	return p, ok
}

// List returns all preset names in the registry (order is not guaranteed).
func (r *PresetRegistry) List() []string {
	names := make([]string, 0, len(r.presets))
	for n := range r.presets {
		names = append(names, n)
	}
	return names
}

// Resolve merges a PushTargetConfig with the matching platform preset to
// produce a fully-populated ResolvedPreset.  Merge priority:
//
//	per-target override > preset value > generic default
//
// If the target's Platform field is empty or does not match any known preset,
// the "generic" preset is used as the base.
func (r *PresetRegistry) Resolve(cfg PushTargetConfig) ResolvedPreset {
	p, ok := r.Get(cfg.Platform)
	if !ok {
		p, ok = r.Get("generic")
		if !ok {
			// Last-resort hardcoded defaults (should never happen — generic
			// is always present in the built-in set).
			return ResolvedPreset{
				Name: "generic", GopSeconds: 2, VideoBitrateKbps: 3000,
				AudioBitrateKbps: 128, Resolution: "1920x1080",
				Framerate: 30, Profile: "main", Bframes: 0,
			}
		}
	}

	result := ResolvedPreset{
		Name:             p.Name,
		GopSeconds:       p.GopSeconds,
		VideoBitrateKbps: p.VideoBitrateKbps,
		AudioBitrateKbps: p.AudioBitrateKbps,
		Resolution:       p.Resolution,
		Framerate:        p.Framerate,
		Profile:          p.Profile,
		Bframes:          p.Bframes,
	}

	if cfg.VideoPresetOverride != nil {
		ov := cfg.VideoPresetOverride
		if ov.GopSeconds > 0 {
			result.GopSeconds = ov.GopSeconds
		}
		if ov.VideoBitrateKbps > 0 {
			result.VideoBitrateKbps = ov.VideoBitrateKbps
		}
		if ov.Resolution != "" {
			result.Resolution = ov.Resolution
		}
		if ov.Framerate > 0 {
			result.Framerate = ov.Framerate
		}
		if ov.Profile != "" {
			result.Profile = ov.Profile
		}
		// Bframes == 0 means "don't override" (zero-value = unset).
		// This is consistent with the ResolveTranscodingConfig pattern.
		if ov.Bframes > 0 {
			result.Bframes = ov.Bframes
		}
	}

	return result
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// validURLSchemes contains the only schemes allowed in preset URL hints.
var validURLSchemes = map[string]bool{
	"rtmp":  true,
	"rtmps": true,
	"rtsp":  true,
	"rtsps": true,
}

func isValidURLScheme(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return validURLSchemes[u.Scheme]
}

// containsEnvVar reports whether s contains an environment variable
// reference pattern ($(…) or ${…}), which is forbidden in preset files.
func containsEnvVar(s string) bool {
	return strings.Contains(s, "${") || strings.Contains(s, "$(")
}

// validateAndConvert checks a parsed YAML preset and converts it to a Preset.
// It returns (Preset, false) for invalid entries (bad URL, env-var, name mismatch).
func (py presetYAML) validateAndConvert(key string) (Preset, bool) {
	name := py.Name
	if name == "" {
		name = key
	}
	if py.Name != "" && py.Name != key {
		slog.Warn("relay/presets: preset name mismatch",
			"key", key, "name", py.Name)
		return Preset{}, false
	}

	if containsEnvVar(name) || containsEnvVar(py.Description) || containsEnvVar(py.URLHint) {
		return Preset{}, false
	}

	if py.URLHint != "" && !isValidURLScheme(py.URLHint) {
		return Preset{}, false
	}

	return Preset{
		Name:               name,
		Description:        py.Description,
		URLHint:            py.URLHint,
		GopSeconds:         py.GopSeconds,
		VideoBitrateKbps:   py.VideoBitrateKbps,
		AudioBitrateKbps:   py.AudioBitrateKbps,
		Resolution:         py.Resolution,
		Framerate:          py.Framerate,
		Profile:            py.Profile,
		Bframes:            py.Bframes,
		AudioCodecRequired: py.AudioCodecRequired,
	}, true
}

// ---------------------------------------------------------------------------
// Built-in presets
// ---------------------------------------------------------------------------

// builtinPresets provides default encoding presets for known live platforms.
var builtinPresets = map[string]Preset{
	"bilibili": {
		Name: "bilibili", Description: "Bilibili live streaming",
		URLHint:    "rtmp://live-push.bilivideo.com/live-bvc/",
		GopSeconds: 2, VideoBitrateKbps: 4000, AudioBitrateKbps: 128,
		Resolution: "1920x1080", Framerate: 30, Profile: "main",
		Bframes: 2, AudioCodecRequired: "aac",
	},
	"douyin": {
		Name: "douyin", Description: "Douyin/TikTok live",
		URLHint:    "rtmp://live-push.douyin.com/",
		GopSeconds: 2, VideoBitrateKbps: 3500, AudioBitrateKbps: 128,
		Resolution: "1080x1920", Framerate: 30, Profile: "main",
		Bframes: 0, AudioCodecRequired: "aac",
	},
	"youtube": {
		Name: "youtube", Description: "YouTube Live",
		URLHint:    "rtmp://a.youtube.com/live2/",
		GopSeconds: 2, VideoBitrateKbps: 4500, AudioBitrateKbps: 128,
		Resolution: "1920x1080", Framerate: 30, Profile: "high",
		Bframes: 2, AudioCodecRequired: "aac",
	},
	"kuaishou": {
		Name: "kuaishou", Description: "Kuaishou live",
		URLHint:    "rtmp://txyun-push.voip.yximgs.com/gifshow/",
		GopSeconds: 2, VideoBitrateKbps: 4000, AudioBitrateKbps: 128,
		Resolution: "1920x1080", Framerate: 30, Profile: "main",
		Bframes: 2, AudioCodecRequired: "aac",
	},
	"generic": {
		Name: "generic", Description: "Generic RTMP target",
		URLHint:    "",
		GopSeconds: 2, VideoBitrateKbps: 3000, AudioBitrateKbps: 128,
		Resolution: "1920x1080", Framerate: 30, Profile: "main",
		Bframes: 0, AudioCodecRequired: "any",
	},
}

// copyBuiltinPresets returns a shallow copy of the built-in preset map.
func copyBuiltinPresets() map[string]Preset {
	m := make(map[string]Preset, len(builtinPresets))
	for k, v := range builtinPresets {
		m[k] = v
	}
	return m
}
