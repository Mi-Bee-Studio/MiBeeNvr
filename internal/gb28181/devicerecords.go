package gb28181

import (
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/mickeyzzc/gb28181-go/manscdp"
	"github.com/mickeyzzc/gb28181-go/platform"
)

// DeviceRecordService owns the device-side recording domain (RecordInfo
// queries, playback/download fetches, MANSRTSP control): range validation,
// channel→device resolution, and device-clock normalization live here so the
// HTTP layer only decodes, calls, and encodes (#1009). The media API and
// device registry are wired post-construction (mirroring how the app wires
// the HTTP handler), hence the Set* methods and the snapshot-on-use locking.
type DeviceRecordService struct {
	mu      sync.Mutex
	media   DeviceMediaAPI
	devices *platform.DeviceManager
	loc     *time.Location // GB naive-clock zone (nil → Local)
}

// DeviceMediaAPI is the device-side recording API implemented by the SIP
// server; a subset of api.GB28181DeviceMedia so the service stands alone
// without importing the api package.
type DeviceMediaAPI interface {
	// QueryChannelRecords sends a RecordInfo query and collects the paged
	// responses for a time range.
	QueryChannelRecords(deviceID, channelID string, start, end time.Time) ([]manscdp.RecordItem, error)
	// StartPlayback starts a fetch that muxes the device recording into the
	// normal recordings pipeline of the bound camera.
	StartPlayback(deviceID, channelID string, start, end time.Time) error
	// StartDownload starts an s=Download fetch (file-speed transfer).
	StartDownload(deviceID, channelID string, start, end time.Time) error
	// StopPlayback stops a fetch (SIP BYE + finalize).
	StopPlayback(channelID string) error
	// PlaybackStatusFor reports fetch progress (ok=false when idle).
	PlaybackStatusFor(channelID string) (platform.PlaybackInfo, bool)
	// PlaybackControl sends a MANSRTSP control (pause/resume/seek).
	PlaybackControl(channelID, action string, scale, position float64) error
}

// DeviceRecord is the normalized shape of one device-side recording index
// entry: device timestamps parsed in the configured GB zone and formatted
// UTC RFC3339.
type DeviceRecord struct {
	Name      string `json:"name"`
	FilePath  string `json:"file_path,omitempty"`
	StartTime string `json:"start_time"` // RFC3339
	EndTime   string `json:"end_time"`   // RFC3339
}

// NewDeviceRecordService creates an unwired service; the app wires media,
// device registry, and timezone as they become available.
func NewDeviceRecordService() *DeviceRecordService { return &DeviceRecordService{} }

// SetMedia wires the SIP server's device-media API.
func (s *DeviceRecordService) SetMedia(m DeviceMediaAPI) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.media = m
}

// SetDeviceManager wires the registered-device registry.
func (s *DeviceRecordService) SetDeviceManager(d *platform.DeviceManager) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.devices = d
}

// SetTimezone pins the zone used to interpret naive GB/T 28181 device-clock
// timestamps (RecordInfo entries). nil keeps time.Local.
func (s *DeviceRecordService) SetTimezone(loc *time.Location) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loc = loc
}

// snapshot captures the wiring under the lock; reads run lock-free after it.
func (s *DeviceRecordService) snapshot() (DeviceMediaAPI, *platform.DeviceManager, *time.Location) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.media, s.devices, s.loc
}

// ChannelNotFoundError indicates no registered device owns the channel.
type ChannelNotFoundError struct {
	ChannelID string
}

func (e *ChannelNotFoundError) Error() string { return "channel not found: " + e.ChannelID }
func (e *ChannelNotFoundError) Code() string  { return "GB_CHANNEL_NOT_FOUND" }

// MediaUnavailableError indicates the SIP device-media API is not wired.
type MediaUnavailableError struct{}

func (e *MediaUnavailableError) Error() string { return "GB28181 device media API not available" }
func (e *MediaUnavailableError) Code() string  { return "GB_MEDIA_UNAVAILABLE" }

// InvalidRangeError indicates end ≤ start or a span beyond the operation cap.
type InvalidRangeError struct {
	Op  string // "record query" | "playback" | "download"
	Max time.Duration
}

func (e *InvalidRangeError) Error() string {
	if e.Max >= 48*time.Hour && e.Max%(24*time.Hour) == 0 {
		return "invalid range: end must be after start, max " + strconv.FormatInt(int64(e.Max/(24*time.Hour)), 10) + " days"
	}
	return "invalid range: end must be after start, max " + strconv.FormatInt(int64(e.Max/time.Hour), 10) + " hours"
}
func (e *InvalidRangeError) Code() string { return "GB_INVALID_RANGE" }

// DeviceCommandError wraps a device-side failure (query, INVITE, BYE,
// control) so callers can map it to a gateway-level status while keeping the
// cause for logs and unwrapping.
type DeviceCommandError struct {
	Op  string
	Err error
}

func (e *DeviceCommandError) Error() string { return e.Op + " failed: " + e.Err.Error() }
func (e *DeviceCommandError) Code() string  { return "GB_DEVICE_ERROR" }
func (e *DeviceCommandError) Unwrap() error { return e.Err }

// Range caps: record queries and downloads cover up to 7 days per request,
// playback fetches 24 hours (#337, #378).
const (
	recordsMaxSpan  = 7 * 24 * time.Hour
	playbackMaxSpan = 24 * time.Hour
	downloadMaxSpan = 7 * 24 * time.Hour
)

// validateRange enforces end-after-start and the per-operation cap.
func validateRange(op string, start, end time.Time, max time.Duration) error {
	if !end.After(start) || end.Sub(start) > max {
		return &InvalidRangeError{Op: op, Max: max}
	}
	return nil
}

// parseDeviceTime parses a device-supplied timestamp. GB/T 28181 uses
// naive local time (devices lack TZ info); the configured zone is applied,
// defaulting to the NVR's local zone.
func parseDeviceTime(v string, loc *time.Location) (time.Time, bool) {
	if loc == nil {
		loc = time.Local
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05", time.RFC3339} {
		if t, err := time.ParseInLocation(layout, v, loc); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// Records queries the device-side recording index for a time range and
// normalizes the entries. Malformed device entries are dropped, not fatal.
func (s *DeviceRecordService) Records(channelID string, start, end time.Time) ([]DeviceRecord, error) {
	if err := validateRange("record query", start, end, recordsMaxSpan); err != nil {
		return nil, err
	}
	media, devices, loc := s.snapshot()
	if devices == nil {
		return nil, &ChannelNotFoundError{ChannelID: channelID}
	}
	deviceID, ok := resolveChannelDeviceOn(devices, channelID)
	if !ok {
		return nil, &ChannelNotFoundError{ChannelID: channelID}
	}
	if media == nil {
		return nil, &MediaUnavailableError{}
	}
	items, err := media.QueryChannelRecords(deviceID, channelID, start, end)
	if err != nil {
		slog.Warn("GB28181 record query failed", "channel_id", channelID, "error", err)
		return nil, &DeviceCommandError{Op: "device record query", Err: err}
	}
	records := make([]DeviceRecord, 0, len(items))
	for _, it := range items {
		st, okSt := parseDeviceTime(it.StartTime, loc)
		et, okEt := parseDeviceTime(it.EndTime, loc)
		if !okSt || !okEt {
			continue
		}
		records = append(records, DeviceRecord{
			Name:      it.Name,
			FilePath:  it.FilePath,
			StartTime: st.UTC().Format(time.RFC3339),
			EndTime:   et.UTC().Format(time.RFC3339),
		})
	}
	return records, nil
}

// resolveChannelDeviceOn is resolveChannelDevice against an explicit
// registry (snapshot already taken).
func resolveChannelDeviceOn(devices *platform.DeviceManager, channelID string) (string, bool) {
	for _, dev := range devices.AllDevices() {
		if _, ok := devices.FindChannel(dev.ID, channelID); ok {
			return dev.ID, true
		}
	}
	return "", false
}

// StartPlayback starts a device-recording fetch: a playback INVITE whose
// RTP/PS stream is muxed into a normal MiBee recording for the bound camera.
// One fetch per channel — a running fetch is replaced.
func (s *DeviceRecordService) StartPlayback(channelID string, start, end time.Time) error {
	if err := validateRange("playback", start, end, playbackMaxSpan); err != nil {
		return err
	}
	deviceID, media, err := s.resolveWired(channelID)
	if err != nil {
		return err
	}
	slog.Info("GB28181 channel playback requested", "channel_id", channelID, "start", start.Format(time.RFC3339), "end", end.Format(time.RFC3339))
	if err := media.StartPlayback(deviceID, channelID, start, end); err != nil {
		slog.Warn("GB28181 playback start failed", "channel_id", channelID, "error", err)
		return &DeviceCommandError{Op: "playback start", Err: err}
	}
	return nil
}

// StartDownload starts a device-recording download (#378): an s=Download
// INVITE — the device streams at file speed and the media is muxed into the
// bound camera's recordings. One fetch per channel.
func (s *DeviceRecordService) StartDownload(channelID string, start, end time.Time) error {
	if err := validateRange("download", start, end, downloadMaxSpan); err != nil {
		return err
	}
	deviceID, media, err := s.resolveWired(channelID)
	if err != nil {
		return err
	}
	slog.Info("GB28181 channel download requested", "channel_id", channelID, "start", start.Format(time.RFC3339), "end", end.Format(time.RFC3339))
	if err := media.StartDownload(deviceID, channelID, start, end); err != nil {
		slog.Warn("GB28181 download start failed", "channel_id", channelID, "error", err)
		return &DeviceCommandError{Op: "download start", Err: err}
	}
	return nil
}

// resolveWired validates the channel and media wiring for a fetch start.
func (s *DeviceRecordService) resolveWired(channelID string) (deviceID string, media DeviceMediaAPI, err error) {
	media, devices, _ := s.snapshot()
	if devices == nil {
		return "", nil, &ChannelNotFoundError{ChannelID: channelID}
	}
	deviceID, ok := resolveChannelDeviceOn(devices, channelID)
	if !ok {
		return "", nil, &ChannelNotFoundError{ChannelID: channelID}
	}
	if media == nil {
		return "", nil, &MediaUnavailableError{}
	}
	return deviceID, media, nil
}

// PlaybackStatus reports the running fetch (ok=false when idle); a non-nil
// error means the media API is not wired.
func (s *DeviceRecordService) PlaybackStatus(channelID string) (platform.PlaybackInfo, bool, error) {
	media, _, _ := s.snapshot()
	if media == nil {
		return platform.PlaybackInfo{}, false, &MediaUnavailableError{}
	}
	st, ok := media.PlaybackStatusFor(channelID)
	return st, ok, nil
}

// StopPlayback stops a running fetch (SIP BYE + finalize the partially
// fetched recording).
func (s *DeviceRecordService) StopPlayback(channelID string) error {
	media, _, _ := s.snapshot()
	if media == nil {
		return &MediaUnavailableError{}
	}
	if err := media.StopPlayback(channelID); err != nil {
		return &DeviceCommandError{Op: "playback stop", Err: err}
	}
	return nil
}

// PlaybackControl drives a running fetch via SIP INFO MANSRTSP: pause /
// resume / seek (optional scale + position seconds).
func (s *DeviceRecordService) PlaybackControl(channelID, action string, scale, position float64) error {
	media, _, _ := s.snapshot()
	if media == nil {
		return &MediaUnavailableError{}
	}
	if err := media.PlaybackControl(channelID, action, scale, position); err != nil {
		return &DeviceCommandError{Op: "playback control", Err: err}
	}
	return nil
}
