package config

// Transcoding configuration types and resolution helpers.

type TranscodingConfig struct {
	Enabled    bool   `yaml:"enabled" json:"enabled"`                             // default false
	FFmpegPath string `yaml:"ffmpeg_path,omitempty" json:"ffmpeg_path,omitempty"` // auto-detected or user-specified
	MaxWorkers int    `yaml:"max_workers,omitempty" json:"max_workers,omitempty"` // default 1, max 4
	// DownloadMirror overrides the FFmpeg static-build host (base URL) —
	// same semantics as update.download_mirror; the per-platform filename is
	// appended. Empty = the official build site.
	DownloadMirror   string `yaml:"download_mirror,omitempty" json:"download_mirror,omitempty"`
	JobTimeout       string `yaml:"job_timeout,omitempty" json:"job_timeout,omitempty"`             // per-job timeout, default "30m", max 4h
	HistoryRetention string `yaml:"history_retention,omitempty" json:"history_retention,omitempty"` // e.g. "168h" (7d), "720h" (30d), ""=never

	// MinSegmentDurationS skips auto-enqueueing transcode tasks for segments
	// whose wall duration is below this floor (#848) — flapping-camera
	// reconnect fragments. 0 (default) = gating off. Skipped fragments fold
	// into rolling-merge buckets whose output is NOT re-enqueued, so their
	// content keeps the original codec.
	MinSegmentDurationS int `yaml:"min_segment_duration_s,omitempty" json:"min_segment_duration_s,omitempty"`
}

type CameraTranscodingConfig struct {
	Enabled     bool   `yaml:"enabled" json:"enabled"`                     // default false
	TargetCodec string `yaml:"target_codec,omitempty" json:"target_codec"` // h264, h265
	Preset      string `yaml:"preset,omitempty" json:"preset"`             // ultrafast, faster, medium
	Bitrate     string `yaml:"bitrate,omitempty" json:"bitrate"`           // e.g. "2M"
	CRF         int    `yaml:"crf,omitempty" json:"crf"`                   // 0=default(23/28), 1-51 quality
}

// ResolveTranscodingConfig returns the effective transcoding config for a camera.
// If per-camera config is nil, the global enabled state is used.
// If per-camera config is set, its fields override the global enabled state.
func (c *Config) ResolveTranscodingConfig(cameraID string) *CameraTranscodingConfig {
	result := &CameraTranscodingConfig{
		Enabled: c.Transcoding.Enabled,
	}
	for i := range c.Cameras {
		cam := &c.Cameras[i]
		if cam.ID == cameraID && cam.Transcoding != nil {
			result.Enabled = cam.Transcoding.Enabled
			if cam.Transcoding.TargetCodec != "" {
				result.TargetCodec = cam.Transcoding.TargetCodec
			}
			if cam.Transcoding.Preset != "" {
				result.Preset = cam.Transcoding.Preset
			}
			if cam.Transcoding.Bitrate != "" {
				result.Bitrate = cam.Transcoding.Bitrate
			}
		}
	}
	return result
}
