package relay

import (
	"context"
	"log/slog"
	"sort"
	"sync"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/transcoding"
)

var managerLogger = slog.Default().With("component", "relay-manager")

// CameraHubProvider returns the StreamHub for a camera id (or nil). Backed by
// CameraManager.GetHub in production.
type CameraHubProvider func(cameraID string) *streamhub.StreamHub

// SPSCameraProvider returns the source camera's current SPS/PPS + H.264 flag,
// looked up by camera id. The Manager adapts this per-target into the
// zero-arg SPSProvider that PushTarget expects.
type SPSCameraProvider func(cameraID string) (sps, pps []byte, isH264 bool)

// CodecInfoProvider returns the source camera's current codec parameters
// (video SPS/PPS/VPS + audio codec info). The Manager adapts this per-target
// into the zero-arg codecInfoProvider that PushTarget expects.
// Backed by CameraManager.GetCodecInfo in production.
type CodecInfoProvider func(cameraID string) model.CodecInfo

// SourceCameraCodecProvider returns the source camera's current video encoding
// ("h264"/"h265"/"mjpeg"/"jpeg"; "" = unknown) by camera id. Backed by
// CameraManager.GetSourceCodec in production; used by push targets to fail
// fast on sources the transcode path cannot handle (#423).
type SourceCameraCodecProvider func(cameraID string) string

// Manager owns the lifecycle of all push-out targets across all cameras.
// Each (cameraID, targetID) pair maps to at most one running *PushTarget.
// Manager is nil-safe at the call sites: when no relays are configured, main.go
// passes a no-op manager so camera Add/Update/Remove don't need nil checks.
type Manager struct {
	hubProvider         CameraHubProvider
	spsProvider         SPSCameraProvider
	codecInfoProvider   CodecInfoProvider         // optional, for audio-aware targets
	sourceCodecProvider SourceCameraCodecProvider // optional, for JPEG fail-fast (#423)

	mu      sync.Mutex
	targets map[string]*runningTarget // key = cameraID + "/" + targetID
	ctx     context.Context

	// Transcode dependencies (optional, wired via setters).
	presetRegistry    *PresetRegistry
	hardwareCap       *transcoding.HardwareCapabilities
	ffmpegPath        string
	streamURLProvider StreamURLProvider // optional, resolves camera stream URL for FFmpeg relay
}

type runningTarget struct {
	target *PushTarget
	cancel context.CancelFunc
}

// NewManager constructs a Manager. hubProvider and spsProvider are required.
// NewManager constructs a Manager. hubProvider and spsProvider are required.
func NewManager(hubProvider CameraHubProvider, spsProvider SPSCameraProvider) *Manager {
	return &Manager{
		hubProvider: hubProvider,
		spsProvider: spsProvider,
		targets:     make(map[string]*runningTarget),
	}
}

// SetCodecInfoProvider wires an optional CodecInfoProvider for use by
// audio-aware push targets. Should be set before Start.
func (m *Manager) SetCodecInfoProvider(p CodecInfoProvider) {
	m.mu.Lock()
	m.codecInfoProvider = p
	m.mu.Unlock()
}

// SetSourceCodecProvider wires an optional SourceCameraCodecProvider for use by
// push targets (JPEG fail-fast, #423). Should be set before Start.
func (m *Manager) SetSourceCodecProvider(p SourceCameraCodecProvider) {
	m.mu.Lock()
	m.sourceCodecProvider = p
	m.mu.Unlock()
}

// SetPresetRegistry wires an optional PresetRegistry for transcode resolution.
// Should be set before Start (targets created after this call will use it).
func (m *Manager) SetPresetRegistry(r *PresetRegistry) {
	m.mu.Lock()
	m.presetRegistry = r
	m.mu.Unlock()
}

// SetHardwareCap wires HardwareCapabilities for transcoder encoder selection.
// Should be set before Start.
func (m *Manager) SetHardwareCap(hwCap *transcoding.HardwareCapabilities) {
	m.mu.Lock()
	m.hardwareCap = hwCap
	m.mu.Unlock()
}

// SetFFmpegPath sets an explicit FFmpeg binary path for the transcoder.
// If empty, the PushTarget will probe via exec.LookPath at runtime.
func (m *Manager) SetFFmpegPath(path string) {
	m.mu.Lock()
	m.ffmpegPath = path
	m.mu.Unlock()
}

// SetStreamURLProvider wires a function that resolves a camera's stream URL
// (e.g. rtsp://...) for FFmpeg relay mode.
func (m *Manager) SetStreamURLProvider(p StreamURLProvider) {
	m.mu.Lock()
	m.streamURLProvider = p
	m.mu.Unlock()
}

// FFmpegAvailable returns whether the FFmpeg binary is available for relay use.
func (m *Manager) FFmpegAvailable() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hardwareCap != nil {
		return m.hardwareCap.FFmpegAvailable
	}
	return m.ffmpegPath != ""
}

// Start sets the root context used for all target goroutines.
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	m.ctx = ctx
	m.mu.Unlock()
}

// Stop cancels every running target and waits for them to exit.
func (m *Manager) Stop() {
	m.mu.Lock()
	for _, rt := range m.targets {
		rt.cancel()
	}
	wg := sync.WaitGroup{}
	for _, rt := range m.targets {
		wg.Add(1)
		go func(rt *runningTarget) {
			defer wg.Done()
			<-rt.target.done
		}(rt)
	}
	m.targets = make(map[string]*runningTarget)
	m.mu.Unlock()
	wg.Wait()
}

// SetCameraTargets reconciles the running targets for one camera against the
// given config list: stops removed/changed targets, starts new/changed ones.
// Idempotent — safe to call with the same config on every camera update.
// Accepts config.PushTargetConfig (the persisted type) and adapts internally.
func (m *Manager) SetCameraTargets(cameraID string, cfgs []config.PushTargetConfig) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	// Adapt config types to the engine's local type.
	local := make([]PushTargetConfig, len(cfgs))
	for i, c := range cfgs {
		var vpo *VideoPresetOverrides
		if c.VideoPresetOverride != nil {
			vpo = &VideoPresetOverrides{
				Resolution:       c.VideoPresetOverride.Resolution,
				Framerate:        c.VideoPresetOverride.Framerate,
				VideoBitrateKbps: c.VideoPresetOverride.VideoBitrateKbps,
				GopSeconds:       c.VideoPresetOverride.GopSeconds,
				Profile:          c.VideoPresetOverride.Profile,
				Bframes:          c.VideoPresetOverride.Bframes,
			}
		}
		local[i] = PushTargetConfig{
			ID: c.ID, Name: c.Name, Protocol: c.Protocol, URL: c.URL, Enabled: c.Enabled,
			Platform: c.Platform, TranscodePolicy: c.TranscodePolicy,
			VideoPresetOverride: vpo, SourceURL: c.SourceURL, UseFFmpeg: c.UseFFmpeg,
		}
	}

	// Index desired targets by their ID.
	desired := make(map[string]PushTargetConfig, len(local))
	for _, c := range local {
		desired[c.ID] = c
	}

	// Stop + remove targets that are gone or whose config changed.
	for key, rt := range m.targets {
		if rt.target.CameraID != cameraID {
			continue
		}
		want, ok := desired[rt.target.Config.ID]
		if !ok || !targetConfigEqual(want, rt.target.Config) {
			rt.cancel()
			delete(m.targets, key)
			managerLogger.Info("relay target stopped",
				"camera_id", cameraID, "target_id", rt.target.Config.ID, "reason", ternary(ok, "config changed", "removed"))
		}
	}

	if m.ctx == nil {
		// Manager not started yet (e.g. config load before Start). Targets will
		// be started when Start runs and SetCameraTargets is replayed.
		return
	}

	// Start new targets.
	for _, c := range local {
		if !c.Enabled {
			continue
		}
		key := cameraID + "/" + c.ID
		if _, exists := m.targets[key]; exists {
			continue
		}
		hub := m.hubProvider(cameraID)
		sps := func() ([]byte, []byte, bool) { return m.spsProvider(cameraID) }
		t := NewPushTarget(cameraID, c, hub, sps)
		if m.codecInfoProvider != nil {
			t.SetCodecInfoProvider(func() model.CodecInfo { return m.codecInfoProvider(cameraID) })
		}
		if m.sourceCodecProvider != nil {
			t.SetSourceCodecProvider(func() string { return m.sourceCodecProvider(cameraID) })
		}
		if m.presetRegistry != nil {
			t.SetPresetRegistry(m.presetRegistry)
		}
		if m.hardwareCap != nil {
			t.SetHardwareCap(m.hardwareCap)
		}
		if m.ffmpegPath != "" {
			t.SetFFmpegPath(m.ffmpegPath)
		}
		if m.streamURLProvider != nil {
			t.SetStreamURLProvider(func(cameraID string) string { return m.streamURLProvider(cameraID) })
		}
		ctx, cancel := context.WithCancel(m.ctx)
		t.done = make(chan struct{})
		rt := &runningTarget{target: t, cancel: cancel}
		m.targets[key] = rt
		go func() {
			defer close(t.done)
			t.Run(ctx)
		}()
		managerLogger.Info("relay target started",
			"camera_id", cameraID, "target_id", c.ID, "protocol", c.Protocol, "url", c.URL)
	}
}

// CameraStatus returns the runtime status of every target for a camera.
func (m *Manager) CameraStatus(cameraID string) []TargetStatus {
	if m == nil {
		return []TargetStatus{}
	}
	m.mu.Lock()
	var out []TargetStatus
	for _, rt := range m.targets {
		if rt.target.CameraID == cameraID {
			out = append(out, rt.target.Status())
		}
	}
	m.mu.Unlock()
	if out == nil {
		return []TargetStatus{}
	}
	return out
}

// CameraStatusJSON returns the camera's target statuses as []any so the
// camera manager (which can't import relay) can pass them to the JSON API.
func (m *Manager) CameraStatusJSON(cameraID string) []any {
	statuses := m.CameraStatus(cameraID)
	out := make([]any, len(statuses))
	for i, s := range statuses {
		out[i] = s
	}
	return out
}

// RemoveCamera stops all targets for a camera (called on camera delete).
func (m *Manager) RemoveCamera(cameraID string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	for key, rt := range m.targets {
		if rt.target.CameraID == cameraID {
			rt.cancel()
			delete(m.targets, key)
		}
	}
	m.mu.Unlock()
}

// ListAllPresets returns all registered presets sorted by name.
// Returns nil when no preset registry is configured.
func (m *Manager) ListAllPresets() []Preset {
	if m == nil || m.presetRegistry == nil {
		return nil
	}
	names := m.presetRegistry.List()
	sort.Strings(names)
	presets := make([]Preset, 0, len(names))
	for _, name := range names {
		if p, ok := m.presetRegistry.Get(name); ok {
			presets = append(presets, p)
		}
	}
	return presets
}

// GetPreset returns a single preset by name.
// Returns false when the name is not found or no registry is configured.
func (m *Manager) GetPreset(name string) (Preset, bool) {
	if m == nil || m.presetRegistry == nil {
		return Preset{}, false
	}
	return m.presetRegistry.Get(name)
}

// targetConfigEqual reports whether two configs are equivalent (a change in any
// field warrants a reconnect).
func targetConfigEqual(a, b PushTargetConfig) bool {
	if a.ID != b.ID || a.Name != b.Name || a.Protocol != b.Protocol ||
		a.URL != b.URL || a.Enabled != b.Enabled ||
		a.Platform != b.Platform || a.TranscodePolicy != b.TranscodePolicy ||
		a.UseFFmpeg != b.UseFFmpeg || a.SourceURL != b.SourceURL {
		return false
	}
	return videoPresetOverrideEqual(a.VideoPresetOverride, b.VideoPresetOverride)
}

func videoPresetOverrideEqual(a, b *VideoPresetOverrides) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Resolution == b.Resolution && a.Framerate == b.Framerate &&
		a.VideoBitrateKbps == b.VideoBitrateKbps && a.GopSeconds == b.GopSeconds &&
		a.Profile == b.Profile && a.Bframes == b.Bframes
}

func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
