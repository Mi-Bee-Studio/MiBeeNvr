package recorder

import (
	"sync/atomic"
	"time"
)

// manualRecordWindow is the timed forced-recording override for live-only
// cameras (#660): MQTT `{"action":"record","duration":"60s"}` (or any caller
// of ArmManualRecording) opens a bounded window during which the
// RecordEnabled gate passes and segments ARE written — the documented
// "start recording for a while" semantics that plain `record` could not
// deliver on recording_enabled=false cameras (it only started the fetcher).
// When the window lapses the in-flight segment is closed and the recorder
// returns to live-only draining.
//
// Arm is monotonic: repeated triggers extend to the LATER deadline, matching
// the hold-extension semantics of the audio/pixel triggers.
type manualRecordWindow struct {
	until atomic.Int64 // unix nanos; 0 = never armed
}

// Arm opens (or extends) the window for d from now.
func (w *manualRecordWindow) Arm(d time.Duration) {
	if d <= 0 {
		return
	}
	deadline := time.Now().Add(d).UnixNano()
	for {
		cur := w.until.Load()
		if cur >= deadline || w.until.CompareAndSwap(cur, deadline) {
			return
		}
	}
}

// Active reports whether t is inside an armed window.
func (w *manualRecordWindow) Active(t time.Time) bool {
	until := w.until.Load()
	return until != 0 && t.UnixNano() < until
}
