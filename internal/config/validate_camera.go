package config

// Camera-level validation: imaging type / spectrum pairing, recording mode and
// adaptive tuning, pixgate, recording schedules. Split out of validate.go.

import (
	"fmt"
	"time"
)

// ValidImagingType reports whether v is a legal imaging_type value.
func ValidImagingType(v string) bool {
	switch v {
	case "", "visible", ImagingTypeThermal:
		return true
	}
	return false
}

// ValidSpectrumGroup reports whether v is a legal spectrum_group value:
// empty (no explicit channel link) or 1-64 runes of [A-Za-z0-9._-]. The
// group key is only compared for equality between camera entries, but it
// lands in URLs/UI as a join key, so keep the charset conservative.
func ValidSpectrumGroup(v string) bool {
	if v == "" {
		return true
	}
	r := []rune(v)
	if len(r) > 64 {
		return false
	}
	for _, c := range r {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-':
		default:
			return false
		}
	}
	return true
}

// MJPEG segment form values (#761): MJPEGFormAVI (also "") is the default
// single-file AVI container per segment; MJPEGFormDir is the legacy
// one-JPEG-file-per-frame directory opt-out.
const (
	MJPEGFormAVI = "avi"
	MJPEGFormDir = "dir"
)

// ValidateCameraRecordingMode checks one camera's recording_mode + adaptive
// tuning block (issue #435) + audio_trigger block (issue #478). Shared by the
// startup config validation and the camera create/update API boundaries so an
// invalid value is rejected at the wire instead of bricking the next restart.
func ValidateCameraRecordingMode(cam CameraConfig) error {
	switch cam.MotionSource {
	case "", "nvr", MotionSourceCameraONVIF:
	default:
		return fmt.Errorf("cameras.%s.motion_source must be \"nvr\" or \"camera:onvif\" (got %q)", cam.ID, cam.MotionSource)
	}
	if !ValidImagingType(cam.ImagingType) {
		return fmt.Errorf("cameras.%s.imaging_type must be \"visible\" or \"thermal\" (got %q)", cam.ID, cam.ImagingType)
	}
	if !ValidSpectrumGroup(cam.SpectrumGroup) {
		return fmt.Errorf("cameras.%s.spectrum_group must be empty or 1-64 chars of [A-Za-z0-9._-] (got %q)", cam.ID, cam.SpectrumGroup)
	}
	if tr := cam.ThermalReadout; tr != nil {
		switch tr.Kind {
		case "", ThermalReadoutKindHikvision:
		default:
			return fmt.Errorf("cameras.%s.thermal_readout.kind must be empty or %q (got %q)", cam.ID, ThermalReadoutKindHikvision, tr.Kind)
		}
		if tr.Channel < 0 || tr.Channel > 64 {
			return fmt.Errorf("cameras.%s.thermal_readout.channel must be 0-64 (got %d)", cam.ID, tr.Channel)
		}
		if r := tr.VisibleRect; r != nil && (r.X < 0 || r.Y < 0 || r.W <= 0 || r.H <= 0 || r.X+r.W > 1.0001 || r.Y+r.H > 1.0001) {
			return fmt.Errorf("cameras.%s.thermal_readout.visible_rect must lie within 0..1 with positive w/h", cam.ID)
		}
	}
	if cam.MotionSource == MotionSourceCameraONVIF && cam.Protocol != "onvif" {
		return fmt.Errorf("cameras.%s.motion_source=camera:onvif requires an onvif protocol camera (got %q)", cam.ID, cam.Protocol)
	}
	switch cam.MJPEGForm {
	case "", MJPEGFormAVI, MJPEGFormDir:
	default:
		return fmt.Errorf("cameras.%s.mjpeg_form must be \"avi\" or \"dir\" (got %q)", cam.ID, cam.MJPEGForm)
	}
	switch cam.RecordingMode {
	case "", "continuous", "adaptive":
	default:
		return fmt.Errorf("cameras.%s.recording_mode must be \"continuous\" or \"adaptive\" (got %q)", cam.ID, cam.RecordingMode)
	}
	switch cam.RecordingTier {
	case "", "single", "tiered":
	default:
		return fmt.Errorf("cameras.%s.recording_tier must be \"tiered\" (got %q)", cam.ID, cam.RecordingTier)
	}
	if cam.RecordingTier == "tiered" {
		switch cam.Protocol {
		case "rtsp", "onvif", "gb28181":
		default:
			return fmt.Errorf("cameras.%s.recording_tier: tiered requires an rtsp/onvif/gb28181 camera with a sub-stream (got protocol %q)", cam.ID, cam.Protocol)
		}
	}
	if cam.TierSegmentDuration != "" {
		d, err := time.ParseDuration(cam.TierSegmentDuration)
		if err != nil || d < 10*time.Second || d > 6*time.Hour {
			return fmt.Errorf("cameras.%s.tier_segment_duration must be a duration 10s–6h, got %q", cam.ID, cam.TierSegmentDuration)
		}
	}
	if cam.AudioTrigger != nil && cam.AudioTrigger.Enabled {
		if cam.RecordingMode != "adaptive" {
			return fmt.Errorf("cameras.%s.audio_trigger requires recording_mode: adaptive (got %q)", cam.ID, cam.RecordingMode)
		}
		at := cam.AudioTrigger
		if at.MinDBFS != 0 && (at.MinDBFS < -90 || at.MinDBFS > 0) {
			return fmt.Errorf("cameras.%s.audio_trigger.min_dbfs must be -90–0 dBFS, got %v", cam.ID, at.MinDBFS)
		}
		if at.PreCaptureS < 0 || at.PreCaptureS > 30 {
			return fmt.Errorf("cameras.%s.audio_trigger.pre_capture_s must be 0–30, got %d", cam.ID, at.PreCaptureS)
		}
	}
	if err := validatePixgate(cam); err != nil {
		return err
	}
	if cam.RecordingMode != "adaptive" {
		return nil
	}
	if cam.Encoding != "h264" && cam.Encoding != "h265" {
		return fmt.Errorf("cameras.%s.recording_mode=adaptive requires differential encoding (h264/h265), got encoding %q", cam.ID, cam.Encoding)
	}
	a := cam.Adaptive
	if a == nil {
		return nil
	}
	if a.CalmThreshold != "" {
		d, err := time.ParseDuration(a.CalmThreshold)
		if err != nil {
			return fmt.Errorf("cameras.%s.adaptive.calm_threshold invalid duration: %w", cam.ID, err)
		}
		if d < 10*time.Second || d > 30*time.Minute {
			return fmt.Errorf("cameras.%s.adaptive.calm_threshold must be 10s–30m, got %s", cam.ID, a.CalmThreshold)
		}
	}
	if a.TimelapseInterval != "" {
		d, err := time.ParseDuration(a.TimelapseInterval)
		if err != nil {
			return fmt.Errorf("cameras.%s.adaptive.timelapse_interval invalid duration: %w", cam.ID, err)
		}
		if d < 5*time.Second || d > 10*time.Minute {
			return fmt.Errorf("cameras.%s.adaptive.timelapse_interval must be 5s–10m, got %s", cam.ID, a.TimelapseInterval)
		}
	}
	if a.SpikeFactor != 0 && (a.SpikeFactor < 1.5 || a.SpikeFactor > 20) {
		return fmt.Errorf("cameras.%s.adaptive.spike_factor must be 1.5–20, got %v", cam.ID, a.SpikeFactor)
	}
	if a.GOPBufferBytes != 0 && (a.GOPBufferBytes < 1<<20 || a.GOPBufferBytes > 64<<20) {
		return fmt.Errorf("cameras.%s.adaptive.gop_buffer_bytes must be 1MB–64MB, got %d", cam.ID, a.GOPBufferBytes)
	}
	switch a.TimelapseFrameMs {
	case 0, 100, 300, 500:
	default:
		return fmt.Errorf("cameras.%s.adaptive.timelapse_frame_ms must be one of 100/300/500, got %d", cam.ID, a.TimelapseFrameMs)
	}
	if a.AmbientArchive && !a.AmbientAudio {
		return fmt.Errorf("cameras.%s.adaptive.ambient_archive requires ambient_audio", cam.ID)
	}
	if a.NoiseFloorBytes != 0 && (a.NoiseFloorBytes < 1 || a.NoiseFloorBytes > 8<<20) {
		return fmt.Errorf("cameras.%s.adaptive.noise_floor_bytes must be 1–8388608 (bytes), got %v", cam.ID, a.NoiseFloorBytes)
	}
	return nil
}

// validatePixgate checks the per-camera pixel-gate config (#636).
func validatePixgate(cam CameraConfig) error {
	p := cam.Pixgate
	if p == nil || !p.Enabled {
		return nil
	}
	if p.SampleFPS != 0 && (p.SampleFPS < 0.2 || p.SampleFPS > 2) {
		return fmt.Errorf("cameras.%s.pixgate.sample_fps must be 0.2–2, got %v", cam.ID, p.SampleFPS)
	}
	if p.MinAreaPct != 0 && (p.MinAreaPct < 0.1 || p.MinAreaPct > 50) {
		return fmt.Errorf("cameras.%s.pixgate.min_area_pct must be 0.1–50, got %v", cam.ID, p.MinAreaPct)
	}
	if p.Persist != 0 && (p.Persist < 1 || p.Persist > 10) {
		return fmt.Errorf("cameras.%s.pixgate.persist must be 1–10, got %d", cam.ID, p.Persist)
	}
	if p.Hold != "" {
		d, err := time.ParseDuration(p.Hold)
		if err != nil || d < time.Second || d > 10*time.Minute {
			return fmt.Errorf("cameras.%s.pixgate.hold must be 1s–10m, got %q", cam.ID, p.Hold)
		}
	}
	if p.GhostSecs != 0 && (p.GhostSecs < 0 || p.GhostSecs > 3600) {
		return fmt.Errorf("cameras.%s.pixgate.ghost_secs must be 0–3600, got %v", cam.ID, p.GhostSecs)
	}
	for i, m := range p.Masks {
		if len(m.Points) < 3 {
			return fmt.Errorf("cameras.%s.pixgate.masks[%d] needs ≥3 points", cam.ID, i)
		}
		for _, pt := range m.Points {
			if pt[0] < 0 || pt[0] > 1 || pt[1] < 0 || pt[1] > 1 {
				return fmt.Errorf("cameras.%s.pixgate.masks[%d] points must be in [0,1]", cam.ID, i)
			}
		}
	}
	// The gate samples the sub-stream: it needs one to exist (a manual URL or
	// a discoverable profile). The resolver failure at runtime is only a WARN,
	// but an obviously-impossible combination is rejected up front.
	if cam.Protocol == "srt" || cam.Protocol == "rtmp" || cam.Protocol == "whip" {
		return fmt.Errorf("cameras.%s.pixgate requires an RTSP-reachable sub-stream (rtsp/onvif/gb28181 cameras)", cam.ID)
	}
	return nil
}

// ValidateSchedule checks a ScheduleConfig (timelapse schedule or camera
// recording_schedule) and normalizes it in place: days_of_week (schedule- and
// range-level) must be 0-6, every time range must be HH:MM (end at or before
// start = overnight), and overlapping ranges are merged. field prefixes error
// messages (e.g. "cameras.x.timelapse.schedule"). A nil schedule is valid.
func ValidateSchedule(field string, sched *ScheduleConfig) error {
	if sched == nil {
		return nil
	}
	if err := validateDaysOfWeek(field+".days_of_week", sched.DaysOfWeek); err != nil {
		return err
	}
	var parsed []TimeRange
	for i, tr := range sched.TimeRanges {
		if _, _, err := parseHHMM(tr.Start); err != nil {
			return fmt.Errorf("%s.time_ranges[%d].start invalid time: %w", field, i, err)
		}
		if _, _, err := parseHHMM(tr.End); err != nil {
			return fmt.Errorf("%s.time_ranges[%d].end invalid time: %w", field, i, err)
		}
		if err := validateDaysOfWeek(fmt.Sprintf("%s.time_ranges[%d].days_of_week", field, i), tr.DaysOfWeek); err != nil {
			return err
		}
		parsed = append(parsed, TimeRange{Start: tr.Start, End: tr.End, DaysOfWeek: tr.DaysOfWeek})
	}
	sched.TimeRanges = mergeTimeRanges(parsed)
	return nil
}

func validateDaysOfWeek(field string, days []int) error {
	for i, d := range days {
		if d < 0 || d > 6 {
			return fmt.Errorf("%s[%d] must be between 0 (Sunday) and 6 (Saturday), got %d", field, i, d)
		}
	}
	return nil
}
