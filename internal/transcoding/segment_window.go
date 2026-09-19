package transcoding

import (
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
)

// segmentTimeLayouts lists the timestamp formats seen on SegmentCompleted
// events: RFC3339Nano from live recorders, and the SQLite
// "2006-01-02 15:04:05.999999999" shape (with or without the fractional
// part) from merge/replay paths.
var segmentTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
}

// parseSegmentWindow returns the wall-clock duration between a segment's
// started and ended timestamps. ok is false when either timestamp is empty,
// unparseable, or the window is non-positive — callers must fail open
// (transcode as before) on unknown data rather than silently dropping work.
func parseSegmentWindow(started, ended string) (time.Duration, bool) {
	if started == "" || ended == "" {
		return 0, false
	}
	s, ok := parseSegmentTime(started)
	if !ok {
		return 0, false
	}
	e, ok := parseSegmentTime(ended)
	if !ok {
		return 0, false
	}
	if !e.After(s) {
		return 0, false
	}
	return e.Sub(s), true
}

func parseSegmentTime(v string) (time.Time, bool) {
	for _, layout := range segmentTimeLayouts {
		if t, err := time.Parse(layout, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// segmentBelowFloor reports whether the completed segment's wall duration is
// shorter than the configured transcode floor (#848 min_segment_duration_s).
// Flapping-camera reconnect cycles produce ~7s fragments whose per-task
// transcode overhead (~3-6s of encoding plus a full read/write cycle)
// approaches the content duration itself. Skipped fragments still fold into
// rolling-merge buckets, but merged output does not fire SegmentCompleted,
// so bucket content stays in its original codec — enable the floor only when
// that trade-off is acceptable. A floor of 0 (the default) disables gating.
func (m *TranscodeManager) segmentBelowFloor(seg event.SegmentCompleted) bool {
	floor := m.cfg.Transcoding.MinSegmentDurationS
	if floor <= 0 {
		return false
	}
	d, ok := parseSegmentWindow(seg.StartedAt, seg.EndedAt)
	if !ok {
		return false
	}
	return d < time.Duration(floor)*time.Second
}
