package transcoding

import (
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/stretchr/testify/require"
)

func TestParseSegmentWindow(t *testing.T) {
	cases := []struct {
		name    string
		started string
		ended   string
		want    time.Duration
		ok      bool
	}{
		{"rfc3339nano", "2026-09-19T07:00:00.123456789Z", "2026-09-19T07:00:07.5Z", 7376543211 * time.Nanosecond, true},
		{"rfc3339", "2026-09-19T07:00:00Z", "2026-09-19T07:01:00Z", time.Minute, true},
		{"db format with nanos", "2026-09-19 15:00:00.123456789", "2026-09-19 15:00:45.9", 45776543211 * time.Nanosecond, true},
		{"db format plain", "2026-09-19 15:00:00", "2026-09-19 15:00:45", 45 * time.Second, true},
		{"empty started", "", "2026-09-19 15:00:45", 0, false},
		{"empty ended", "2026-09-19 15:00:00", "", 0, false},
		{"garbage", "not-a-time", "2026-09-19 15:00:45", 0, false},
		{"reversed window", "2026-09-19 15:00:45", "2026-09-19 15:00:00", 0, false},
		{"zero window", "2026-09-19 15:00:00", "2026-09-19 15:00:00", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseSegmentWindow(tc.started, tc.ended)
			require.Equal(t, tc.ok, ok)
			if tc.ok {
				require.Equal(t, tc.want, got)
			}
		})
	}
}

func TestSegmentBelowFloor(t *testing.T) {
	m := &TranscodeManager{cfg: &config.Config{}}
	sevenSec := event.SegmentCompleted{
		CameraID:  "cam-test",
		StartedAt: "2026-09-19T07:00:00Z",
		EndedAt:   "2026-09-19T07:00:07Z",
	}

	// Floor 0 (default) = gating off — fragments transcode as before.
	require.False(t, m.segmentBelowFloor(sevenSec))

	m.cfg.Transcoding.MinSegmentDurationS = 30
	// 7s flapping fragment < 30s floor → skipped.
	require.True(t, m.segmentBelowFloor(sevenSec))

	// Full-length segment passes the floor.
	full := sevenSec
	full.EndedAt = "2026-09-19T07:01:00Z"
	require.False(t, m.segmentBelowFloor(full))

	// Unparseable timestamps fail open — never drop work on bad data.
	bad := event.SegmentCompleted{StartedAt: "garbage", EndedAt: "2026-09-19T07:00:07Z"}
	require.False(t, m.segmentBelowFloor(bad))
}
