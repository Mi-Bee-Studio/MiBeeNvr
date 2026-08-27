package config

// Package-level camera configuration types. See config.go for the
// top-level Config aggregate and Validate/ApplyDefaults entry points.

type CameraConfig struct {
	ID             string `yaml:"id"`
	Name           string `yaml:"name"`
	Protocol       string `yaml:"protocol"` // rtsp_h264, rtsp_mjpeg, http_jpeg
	Encoding       string `yaml:"encoding"` // h264, h265, mjpeg, jpeg (independent of protocol)
	URL            string `yaml:"url"`
	Username       string `yaml:"username"`
	Password       string `yaml:"password"`
	ONVIFEndpoint  string `yaml:"onvif_endpoint"`
	ProfileToken   string `yaml:"profile_token"`
	StreamEncoding string `yaml:"stream_encoding"` // H264 or H265, for ONVIF cameras. Empty = auto-detect.
	// Sub-stream fields (#512, phase 1): the sub stream is a lower-resolution
	// secondary feed for future consumers (grid preview, cascade, external AI
	// push). Neither field affects the main recording pipeline.
	// SubStreamURL is a manual rtsp:// URL to the camera's sub stream
	// (protocol-agnostic fallback; also consumed by HLS low-res streaming).
	SubStreamURL string `yaml:"sub_stream_url,omitempty" json:"sub_stream_url,omitempty"`
	// SubProfileToken is the ONVIF secondary profile token, auto-discovered
	// once the recorder is online (highest-pixel profile strictly below the
	// main profile's resolution) and persisted. Fill-once: an existing value
	// (manual or discovered) is not overwritten — clear it to re-trigger
	// discovery. Stays empty on single-profile cameras.
	SubProfileToken string                   `yaml:"sub_profile_token,omitempty" json:"sub_profile_token,omitempty"`
	SnapshotURL     string                   `yaml:"snapshot_url"`
	SampleInterval  int                      `yaml:"sample_interval"`
	HLSMaxFPS       int                      `yaml:"hls_max_fps"`
	Merge           *MergeConfig             `yaml:"merge"`
	Transcoding     *CameraTranscodingConfig `yaml:"transcoding,omitempty"`
	Timelapse       *CameraTimelapseConfig   `yaml:"timelapse,omitempty" json:"timelapse,omitempty"`
	AudioEnabled    bool                     `yaml:"audio_enabled"`
	// AudioInRecordings keeps the camera's real audio track in recorded
	// segments (event spans in merged products; live preview and the audio
	// trigger are unaffected). Default false — recordings are video-only
	// unless explicitly enabled per camera.
	AudioInRecordings    bool            `yaml:"audio_in_recordings,omitempty" json:"audio_in_recordings,omitempty"`
	HealthOverrides      HealthOverrides `yaml:"health_overrides,omitempty"`
	FrameWatchdogTimeout string          `yaml:"frame_watchdog_timeout,omitempty"` // default "30s" (per-camera frame watchdog)
	HTTPJPEGAVI          bool            `yaml:"http_jpeg_avi"`                    // write AVI single-file instead of MJPEG directory

	// StableID is a hardware-level stable identifier (ONVIF serial number) used to
	// re-acquire the SAME camera after its IP changes (e.g. after an AP reboot when
	// cameras roam across subnets with per-subnet DHCP). Empty = IP self-healing
	// disabled. ONVIF cameras auto-populate this on first successful connection.
	// See internal/rediscovery/ for the re-discovery engine.
	StableID string `yaml:"stable_id,omitempty" json:"stable_id,omitempty"`
	// SubnetHints are candidate CIDRs (e.g. "192.168.63.0/24") where the camera may
	// appear after roaming. The re-discovery scanner probes these in addition to the
	// last-known host and the NVR's own interface subnets. Empty = scan last-known +
	// local subnets only.
	SubnetHints []string `yaml:"subnet_hints,omitempty" json:"subnet_hints,omitempty"`

	// Xiaomi-specific camera fields (only used when protocol is "xiaomi")
	DID     string `yaml:"did,omitempty"`     // Xiaomi Device ID
	Vendor  string `yaml:"vendor,omitempty"`  // Transport vendor: "cs2" (default)
	Channel string `yaml:"channel,omitempty"` // Xiaomi dual-lens channel ("" or "0" = main, "1" = secondary)
	Quality string `yaml:"quality,omitempty"` // Xiaomi stream quality: "" or "auto" (HD→SD fallback), "hd", "sd"

	// Push/ingest camera fields (only used when protocol is "srt" or "rtmp").
	// For these cameras the publisher connects TO the NVR; the URL field is
	// not used. RTMP uses StreamKey (the last path segment of rtmp://host/live/{key}).
	// SRT uses SRTPassphrase (AES encryption) and SRTStreamID (the streamid query).
	StreamKey     string `yaml:"stream_key,omitempty" json:"stream_key,omitempty"`
	SRTPassphrase string `yaml:"srt_passphrase,omitempty" json:"srt_passphrase,omitempty"`
	SRTStreamID   string `yaml:"srt_stream_id,omitempty" json:"srt_stream_id,omitempty"`

	// GB28181-specific camera fields (only used when protocol is "gb28181").
	// The camera is SIP-pull: the NVR INVITEs the channel by device/channel GB code
	// and ingests its RTP/PS stream. URL is not used.
	GB28181 GB28181ChannelConfig `yaml:"gb28181,omitempty" json:"gb28181,omitempty"`

	// Dark frame filtering: skip recording segments that are too dark to be useful
	// (night without IR capability). Only applies to MJPEG/AVI cameras.
	// When enabled, each segment is brightness-checked at close time; dark segments
	// are marked merge_status='dark' and excluded from merge + cleaned up early.
	DarkFrameFilterEnabled bool `yaml:"dark_frame_filter_enabled,omitempty" json:"dark_frame_filter_enabled,omitempty"`
	DarkFrameThreshold     int  `yaml:"dark_frame_threshold,omitempty" json:"dark_frame_threshold,omitempty"` // 0-255, default 15

	// RecordingEnabled gates whether this camera writes segments to disk.
	// nil or true (default) = record normally. false = "live-only" mode: the
	// recorder stays connected and feeds the StreamHub (live preview, relay,
	// health all work) but writes NO segments — useful when the NVR is used
	// purely as a live/relay gateway and SD-card writes must be avoided.
	// Issue #36.
	RecordingEnabled *bool `yaml:"recording_enabled,omitempty" json:"recording_enabled,omitempty"`

	// RecordingMode selects the write-density strategy (issue #435):
	//   "" | "continuous" — record every frame (default, current behavior)
	//   "adaptive"        — dynamic timelapse: while the compressed-domain
	//                       activity signal (P-frame size baseline) stays calm
	//                       for CalmThreshold, only one keyframe per
	//                       TimelapseInterval is written; any activity spike
	//                       flushes the retained GOP pre-buffer and resumes
	//                       full-rate recording. H.264/H.265 cameras only.
	RecordingMode string `yaml:"recording_mode,omitempty" json:"recording_mode,omitempty"`

	// Adaptive holds the tuning knobs for recording_mode: adaptive. Nil uses
	// the defaults in recorder.DefaultAdaptiveConfig.
	Adaptive *AdaptiveRecordingConfig `yaml:"adaptive,omitempty" json:"adaptive,omitempty"`

	// AudioTrigger arms loudness-triggered recording (issue #478) for
	// recording_mode: adaptive cameras: a loud 1-second G.711 window defers
	// timelapse entry and exits timelapse with a GOP + pre-trigger-audio
	// back-fill, so an abnormal sound on a static picture is recorded with
	// audio. G.711 (µ-law/A-law) cameras only — AAC/Opus have no decoder in
	// the static build (the recorder logs that the trigger stays inactive).
	AudioTrigger *CameraAudioTriggerConfig `yaml:"audio_trigger,omitempty" json:"audio_trigger,omitempty"`

	// Recording schedule: restrict recording to specific time ranges (e.g. daytime only).
	// When nil or disabled, records 24/7. Uses the same TimeRange/ScheduleConfig
	// pattern as timelapse scheduling.
	RecordingSchedule *ScheduleConfig `yaml:"recording_schedule,omitempty" json:"recording_schedule,omitempty"`

	// ActivationState gates recorder startup. "" or "active" (default) = the camera
	// connects and records normally. "pending_activation" = the camera is persisted
	// (DB + config) and visible in the UI, but its recorder is NOT started and no
	// connection attempts are made — used by auto-discover for ONVIF devices that
	// require credentials the NVR does not yet have. The user supplies credentials
	// via the "activate" action, which flips this to "active" and starts the recorder.
	// See internal/autodiscover/ for the discovery engine.
	ActivationState string `yaml:"activation_state,omitempty" json:"activation_state,omitempty"`

	// CascadeEnabled gates this camera's participation in the GB28181 cascade
	// catalog (lower-platform role): nil or true (default) = advertised to the
	// upper platform and forwardable on INVITE; false = hidden from the
	// aggregated catalog and INVITEs for its channel are refused ("目录收敛" —
	// expose only a chosen subset of cameras to the upper platform). The
	// persisted channel-ID allocation is kept, so re-enabling restores the
	// same channel code and the upper's bindings survive.
	CascadeEnabled *bool `yaml:"cascade_enabled,omitempty" json:"cascade_enabled,omitempty"`

	// CascadeSubStream forwards the camera's on-demand SUB-stream to the
	// GB28181 upper platform instead of the main stream (#512/#513): the
	// low-res tier keeps the uplink bandwidth bounded for platforms that only
	// need a preview. INVITE falls back to main when the camera has no
	// sub-stream or its pull fails. Cameras without one are unaffected.
	CascadeSubStream bool `yaml:"cascade_sub_stream,omitempty" json:"cascade_sub_stream,omitempty"`

	// Push-out targets (relay): forward this camera's live stream to remote
	// destinations (another NVR's RTMP/SRT ingest, a live platform, a backup).
	// Applies to ANY camera protocol — the engine subscribes to the camera's
	// StreamHub, so no re-pull happens. Each entry is one independent target.
	PushTargets []PushTargetConfig `yaml:"push_targets,omitempty" json:"push_targets,omitempty"`
	// Per-camera push-in retention override. nil = follow global retention,
	// 0 = live-only (no recording), N = keep N days. Only meaningful for srt/rtmp.
	PushRetentionDays *int `yaml:"push_retention_days,omitempty" json:"push_retention_days,omitempty"`
}

// AdaptiveRecordingConfig tunes recording_mode: adaptive (issue #435).
// Durations are strings ("60s") parsed at wire-up, mirroring the
// frame_watchdog_timeout convention.
type AdaptiveRecordingConfig struct {
	// CalmThreshold is how long the activity signal must stay calm before the
	// recorder drops to sparse keyframe writing. Default "60s". Range 10s–30m.
	CalmThreshold string `yaml:"calm_threshold,omitempty" json:"calm_threshold,omitempty"`
	// TimelapseInterval is the keyframe cadence while in sparse mode.
	// Default "30s". Range 5s–10m.
	TimelapseInterval string `yaml:"timelapse_interval,omitempty" json:"timelapse_interval,omitempty"`
	// SpikeFactor is how many (MAD-floored) deviations above the P-frame size
	// baseline count as an activity spike. Default 5.0 (real-camera noise
	// calibration, issue #466). Range 1.5–20 — values above ~10 are for
	// high-noise scenes (e.g. constant cloud movement) where anything lower
	// never goes sparse (issue #475 field data).
	SpikeFactor float64 `yaml:"spike_factor,omitempty" json:"spike_factor,omitempty"`
	// GOPBufferBytes caps the in-memory GOP pre-buffer that makes the
	// timelapse→normal transition seamless. Default 32MB (must hold one full
	// camera GOP — 2K cameras with IDR intervals near the 30s timelapse
	// cadence overflow 16MB and lose the flush, issue #485). Range 1–64MB.
	GOPBufferBytes int64 `yaml:"gop_buffer_bytes,omitempty" json:"gop_buffer_bytes,omitempty"`
	// AmbientAudio keeps recording the audio track continuously while sparse
	// (#496 audio phase): the merge compresses the ambient span into a quiet
	// continuous atmosphere bed under the timelapse video (event spans keep
	// real audio). G.711 cameras only; ~28.8MB/h storage while sparse.
	AmbientAudio bool `yaml:"ambient_audio,omitempty" json:"ambient_audio,omitempty"`
	// TimelapseFrameMs is the compressed-timeline cadence the merge writes
	// sparse dwell samples at: preset 100 / 300 / 500 ms, 0/unset = 100.
	TimelapseFrameMs int `yaml:"timelapse_frame_ms,omitempty" json:"timelapse_frame_ms,omitempty"`
	// AmbientArchive additionally keeps the raw continuous ambient audio as a
	// sidecar file (<segment>.g711) beside the recording for post-production
	// (the merged product still only carries the atmosphere bed). Only
	// meaningful with ambient_audio; default false.
	AmbientArchive bool `yaml:"ambient_archive,omitempty" json:"ambient_archive,omitempty"`
}

// CameraAudioTriggerConfig tunes audio_trigger (issue #478).
type CameraAudioTriggerConfig struct {
	// Enabled arms the loudness input. When false the recorder behaves
	// exactly as before #478.
	Enabled bool `yaml:"enabled" json:"enabled"`
	// MinDBFS is the loudness threshold over a 1-second window, in dBFS
	// (20·log10(rms/32768)). Default -45. Range -90–0 (0 = default).
	MinDBFS float64 `yaml:"min_dbfs,omitempty" json:"min_dbfs,omitempty"`
	// PreCaptureS is how many seconds of pre-trigger audio a timelapse-exit
	// flush back-fills into the segment. Default 3. Range 0–30.
	PreCaptureS int `yaml:"pre_capture_s,omitempty" json:"pre_capture_s,omitempty"`
}

// PushTargetConfig defines one push-out (relay) destination for a camera.
type PushTargetConfig struct {
	ID                  string                `yaml:"id" json:"id"` // stable id within the camera (kebab/uuid)
	Name                string                `yaml:"name,omitempty" json:"name,omitempty"`
	Protocol            string                `yaml:"protocol" json:"protocol"` // "rtmp" or "rtsp"
	URL                 string                `yaml:"url" json:"url"`           // rtmp://host[:port]/app/key | rtsp://host[:port]/path
	Enabled             bool                  `yaml:"enabled" json:"enabled"`
	Platform            string                `yaml:"platform,omitempty" json:"platform,omitempty"`                 // preset name (bilibili/douyin/youtube/kuaishou/generic/empty)
	TranscodePolicy     string                `yaml:"transcode_policy,omitempty" json:"transcode_policy,omitempty"` // auto/force_sw/off
	VideoPresetOverride *VideoPresetOverrides `yaml:"video_preset_override,omitempty" json:"video_preset_override,omitempty"`
	SourceURL           string                `yaml:"source_url,omitempty" json:"source_url,omitempty"` // optional: if set, relay uses FFmpeg to pull from this URL instead of hub
	UseFFmpeg           bool                  `yaml:"use_ffmpeg,omitempty" json:"use_ffmpeg,omitempty"` // if true, use FFmpeg subprocess for relay (compatibility mode)
}

// VideoPresetOverrides allows overriding individual encoding parameters
// for a push target. Only non-zero/non-empty fields are applied.
type VideoPresetOverrides struct {
	Resolution       string `yaml:"resolution,omitempty" json:"resolution,omitempty"`
	Framerate        int    `yaml:"framerate,omitempty" json:"framerate,omitempty"`
	VideoBitrateKbps int    `yaml:"video_bitrate_kbps,omitempty" json:"video_bitrate_kbps,omitempty"`
	GopSeconds       int    `yaml:"gop_seconds,omitempty" json:"gop_seconds,omitempty"`
	Profile          string `yaml:"profile,omitempty" json:"profile,omitempty"`
	Bframes          int    `yaml:"bframes,omitempty" json:"bframes,omitempty"`
}

// HealthOverrides allows per-camera health monitoring threshold overrides.
// When set, non-zero values take precedence over global health config.
type HealthOverrides struct {
	MaxIDRInterval         string  `yaml:"max_idr_interval,omitempty"`
	BitrateChangeThreshold float64 `yaml:"bitrate_change_threshold,omitempty"`
	MinFPS                 int     `yaml:"min_fps,omitempty"`
	OfflineThreshold       string  `yaml:"offline_threshold,omitempty"`
	FreezeTimeout          string  `yaml:"freeze_timeout,omitempty"`
}

// XiaomiConfig holds Xiaomi cloud authentication settings.
type XiaomiConfig struct {
	UserID string `yaml:"user_id"` // Xiaomi account user ID (from auth response)
	Token  string `yaml:"token"`   // Xiaomi passToken for API access
	Region string `yaml:"region"`  // Region code (e.g. "cn", "sg", "de")
}
