// Package audioaudit owns the process-global audio-access audit ring: every
// recording-WAV fetch and live-listening session appends an entry, and the
// /api/recordings/audio-audit endpoint reads the newest-first trail
// (compliance requirement, v43 audio recordings).
//
// A leaf package (no internal deps beyond stdlib) because two domain
// packages — recordings (WAV fetch + audit query) and the camera live-audio
// path — both append to the same ring.
package audioaudit

import (
	"sync"
	"time"
)

// ringCap bounds memory: audio fetches are human-driven, 200 entries covers
// any realistic inspection window.
const ringCap = 200

// Entry is one audio-access audit record.
type Entry struct {
	At        time.Time `json:"at"`
	Kind      string    `json:"kind"` // "wav" | "live"
	Recording string    `json:"recording,omitempty"`
	CameraID  string    `json:"camera_id"`
	Remote    string    `json:"remote"`
	UA        string    `json:"ua"`
	Bytes     int64     `json:"bytes,omitempty"`
	DurationS float64   `json:"duration_s,omitempty"` // live sessions
}

var ring = struct {
	mu      sync.Mutex
	entries []Entry
}{}

// Record appends one entry, trimming the ring at the cap.
func Record(e Entry) {
	ring.mu.Lock()
	defer ring.mu.Unlock()
	ring.entries = append(ring.entries, e)
	if len(ring.entries) > ringCap {
		ring.entries = ring.entries[len(ring.entries)-ringCap:]
	}
}

// Recent returns up to limit newest entries, newest first.
func Recent(limit int) []Entry {
	ring.mu.Lock()
	defer ring.mu.Unlock()
	n := len(ring.entries)
	out := make([]Entry, 0, n)
	for i := n - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, ring.entries[i])
	}
	return out
}
