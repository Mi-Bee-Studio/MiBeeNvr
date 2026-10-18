package gb28181

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/mickeyzzc/gb28181-go/manscdp"
	"github.com/mickeyzzc/gb28181-go/platform"
	"github.com/stretchr/testify/require"
)

// fakeDeviceMedia implements DeviceMediaAPI with call capture.
type fakeDeviceMedia struct {
	mu sync.Mutex

	records   []manscdp.RecordItem
	queryErr  error
	queried   bool
	queryGot  [2]time.Time
	startErr  error
	started   bool
	dlErr     error
	downladed bool
	stopErr   error
	stopped   bool
	ctlErr    error
	status    *platform.PlaybackInfo
}

func (f *fakeDeviceMedia) QueryChannelRecords(_, _ string, start, end time.Time) ([]manscdp.RecordItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queried = true
	f.queryGot = [2]time.Time{start, end}
	return f.records, f.queryErr
}

func (f *fakeDeviceMedia) StartPlayback(_, _ string, _, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = true
	return f.startErr
}

func (f *fakeDeviceMedia) StartDownload(_, _ string, _, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.downladed = true
	return f.dlErr
}

func (f *fakeDeviceMedia) StopPlayback(string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
	return f.stopErr
}

func (f *fakeDeviceMedia) PlaybackStatusFor(string) (platform.PlaybackInfo, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.status == nil {
		return platform.PlaybackInfo{}, false
	}
	return *f.status, true
}

func (f *fakeDeviceMedia) PlaybackControl(string, string, float64, float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ctlErr
}

const (
	svcDevA     = "34020000001310000001"
	svcDevB     = "34020000001310000002"
	svcChannel  = "34020000001320000001"
	svcChannelB = "34020000001320000002"
)

// newSvcEnv wires a service with a registered two-device registry and the
// media fake; CST (UTC+8) pins the GB timezone deterministically.
func newSvcEnv(t *testing.T) (*DeviceRecordService, *fakeDeviceMedia) {
	t.Helper()
	media := &fakeDeviceMedia{}
	devices := platform.NewDeviceManager(time.Minute)
	devices.Register(&platform.Device{ID: svcDevA, Name: "A"})
	devices.RegisterChannel(svcDevA, &platform.Channel{ID: svcChannel, Name: "ChA"})
	devices.Register(&platform.Device{ID: svcDevB, Name: "B"})
	devices.RegisterChannel(svcDevB, &platform.Channel{ID: svcChannelB, Name: "ChB"})
	s := NewDeviceRecordService()
	s.SetMedia(media)
	s.SetDeviceManager(devices)
	s.SetTimezone(time.FixedZone("CST", 8*3600))
	return s, media
}

var svcWin = [2]time.Time{
	time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC),
	time.Date(2025, 6, 2, 0, 0, 0, 0, time.UTC),
}

func TestDeviceRecords_Normalization(t *testing.T) {
	t.Parallel()
	s, media := newSvcEnv(t)
	media.records = []manscdp.RecordItem{
		{Name: "r1", FilePath: "/dvr/1.mp4", StartTime: "2025-06-01T10:00:00", EndTime: "2025-06-01T10:10:00"}, // naive T
		{Name: "r2", StartTime: "2025-06-01 11:00:00", EndTime: "2025-06-01 11:05:00"},                         // space separator
		{Name: "r3", StartTime: "2025-06-01T04:00:00Z", EndTime: "2025-06-01T04:05:00Z"},                       // RFC3339 passthrough
		{Name: "bad", StartTime: "not-a-time", EndTime: "2025-06-01T12:00:00"},                                 // dropped
	}

	recs, err := s.Records(svcChannel, svcWin[0], svcWin[1])
	require.NoError(t, err)
	require.Len(t, recs, 3, "malformed entries dropped, not fatal")
	// Naive device clock read in CST (UTC+8): 10:00 local = 02:00 UTC.
	require.Equal(t, "2025-06-01T02:00:00Z", recs[0].StartTime)
	require.Equal(t, "2025-06-01T03:00:00Z", recs[1].StartTime)
	// RFC3339 (already zoned) survives untouched.
	require.Equal(t, "2025-06-01T04:00:00Z", recs[2].StartTime)
}

func TestDeviceRecords_EmptyResultIsNonNullSlice(t *testing.T) {
	t.Parallel()
	s, _ := newSvcEnv(t)
	recs, err := s.Records(svcChannel, svcWin[0], svcWin[1])
	require.NoError(t, err)
	require.NotNil(t, recs)
	require.Empty(t, recs)
}

func TestDeviceRecords_RangeValidation(t *testing.T) {
	t.Parallel()
	s, _ := newSvcEnv(t)
	day := 24 * time.Hour

	cases := []struct {
		name      string
		op        string
		start     time.Time
		end       time.Time
		wantMsg   string
		wantCode  string
		wantNoErr bool
	}{
		{"records reversed", "records", svcWin[1], svcWin[0], "invalid range: end must be after start, max 7 days", "GB_INVALID_RANGE", false},
		{"records 8d", "records", svcWin[0], svcWin[0].Add(8 * day), "invalid range: end must be after start, max 7 days", "GB_INVALID_RANGE", false},
		{"playback 25h", "playback", svcWin[0], svcWin[0].Add(25 * time.Hour), "invalid range: end must be after start, max 24 hours", "GB_INVALID_RANGE", false},
		{"download 8d", "download", svcWin[0], svcWin[0].Add(8 * day), "invalid range: end must be after start, max 7 days", "GB_INVALID_RANGE", false},
		{"playback 24h boundary ok", "playback", svcWin[0], svcWin[0].Add(24 * time.Hour), "", "", true},
		{"download 7d boundary ok", "download", svcWin[0], svcWin[0].Add(7 * day), "", "", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var err error
			switch tc.op {
			case "records":
				_, err = s.Records(svcChannel, tc.start, tc.end)
			case "playback":
				err = s.StartPlayback(svcChannel, tc.start, tc.end)
			case "download":
				err = s.StartDownload(svcChannel, tc.start, tc.end)
			}
			if tc.wantNoErr {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, tc.wantMsg)
			require.Equal(t, tc.wantCode, model.ErrorCode(err))
		})
	}
}

func TestDeviceRecords_ChannelResolution(t *testing.T) {
	t.Parallel()
	s, media := newSvcEnv(t)

	// Unknown channel → typed 404-shape error.
	_, err := s.Records("999", svcWin[0], svcWin[1])
	var nf *ChannelNotFoundError
	require.ErrorAs(t, err, &nf)
	require.Equal(t, "GB_CHANNEL_NOT_FOUND", model.ErrorCode(err))
	media.mu.Lock()
	queried := media.queried
	media.mu.Unlock()
	require.False(t, queried, "unknown channel must not reach the device")

	// Resolution across devices: channel B lives on device B.
	media.mu.Lock()
	media.records = []manscdp.RecordItem{{Name: "b1", StartTime: "2025-06-01T10:00:00", EndTime: "2025-06-01T10:01:00"}}
	media.mu.Unlock()
	_, err = s.Records(svcChannelB, svcWin[0], svcWin[1])
	require.NoError(t, err)
}

func TestDeviceRecords_UnwiredStates(t *testing.T) {
	t.Parallel()
	// No media wired: validation and resolution still run, media ops report
	// MediaUnavailableError.
	bare := NewDeviceRecordService()
	devices := platform.NewDeviceManager(time.Minute)
	devices.Register(&platform.Device{ID: svcDevA})
	devices.RegisterChannel(svcDevA, &platform.Channel{ID: svcChannel})
	bare.SetDeviceManager(devices)

	var una *MediaUnavailableError
	require.ErrorAs(t, func() error { _, err := bare.Records(svcChannel, svcWin[0], svcWin[1]); return err }(), &una)
	require.ErrorAs(t, bare.StartPlayback(svcChannel, svcWin[0], svcWin[1]), &una)
	require.ErrorAs(t, bare.StopPlayback(svcChannel), &una)
	_, _, err := bare.PlaybackStatus(svcChannel)
	require.ErrorAs(t, err, &una)
	require.ErrorAs(t, bare.PlaybackControl(svcChannel, "pause", 0, 0), &una)
	require.Equal(t, "GB_MEDIA_UNAVAILABLE", model.ErrorCode(err))

	// No device registry wired: everything resolves to ChannelNotFoundError.
	bare2 := NewDeviceRecordService()
	bare2.SetMedia(&fakeDeviceMedia{})
	var nf *ChannelNotFoundError
	require.ErrorAs(t, bare2.StartPlayback(svcChannel, svcWin[0], svcWin[1]), &nf)
}

func TestDeviceRecords_DeviceFailuresWrap(t *testing.T) {
	t.Parallel()
	s, media := newSvcEnv(t)
	boom := errors.New("device busy")

	media.mu.Lock()
	media.queryErr = boom
	media.mu.Unlock()
	_, err := s.Records(svcChannel, svcWin[0], svcWin[1])
	var dce *DeviceCommandError
	require.ErrorAs(t, err, &dce)
	require.ErrorIs(t, err, boom, "cause preserved for unwrapping")
	require.Contains(t, err.Error(), "device record query failed")
	require.Equal(t, "GB_DEVICE_ERROR", model.ErrorCode(err))

	media.mu.Lock()
	media.queryErr = nil
	media.startErr = errors.New("invite timeout")
	media.mu.Unlock()
	require.ErrorContains(t, s.StartPlayback(svcChannel, svcWin[0], svcWin[1]), "playback start failed")
	media.mu.Lock()
	media.startErr = nil
	media.dlErr = errors.New("refused")
	media.mu.Unlock()
	require.ErrorContains(t, s.StartDownload(svcChannel, svcWin[0], svcWin[1]), "download start failed")
	media.mu.Lock()
	media.dlErr = nil
	media.stopErr = errors.New("bye failed")
	media.mu.Unlock()
	require.ErrorContains(t, s.StopPlayback(svcChannel), "playback stop failed")
	media.mu.Lock()
	media.stopErr = nil
	media.ctlErr = errors.New("not playing")
	media.mu.Unlock()
	require.ErrorContains(t, s.PlaybackControl(svcChannel, "resume", 0, 0), "playback control failed")
}

func TestDeviceRecords_PlaybackStatus(t *testing.T) {
	t.Parallel()
	s, media := newSvcEnv(t)

	st, ok, err := s.PlaybackStatus(svcChannel)
	require.NoError(t, err)
	require.False(t, ok)
	require.False(t, st.Active)

	media.mu.Lock()
	media.status = &platform.PlaybackInfo{Active: true, Kind: "download", ChannelID: svcChannel, Frames: 7}
	media.mu.Unlock()
	st, ok, err = s.PlaybackStatus(svcChannel)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, st.Active)
	require.EqualValues(t, 7, st.Frames)
}

// TestDeviceRecords_ConcurrentWiring pins the snapshot discipline: setters
// racing readers must not race (CI runs -race).
func TestDeviceRecords_ConcurrentWiring(t *testing.T) {
	t.Parallel()
	s, _ := newSvcEnv(t)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				_, _ = s.Records(svcChannel, svcWin[0], svcWin[1])
				s.SetTimezone(time.UTC)
				s.SetMedia(&fakeDeviceMedia{})
				_ = s.StartPlayback(svcChannel, svcWin[0], svcWin[1])
				s.SetTimezone(time.FixedZone("CST", 8*3600))
			}
		}()
	}
	wg.Wait()
}
