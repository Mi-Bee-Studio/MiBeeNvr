package thermal

import "time"

// MinuteRecorder aggregates the 2s readout stream into one sample per camera
// per closed minute (P4 temperature history): within a minute the scene
// extremes are folded (max of maxes, min of mins) and the last-seen spots are
// kept. Samples are emitted when the minute rolls over (a newer reading
// arrives) or when Due flushes a bucket whose minute ended a grace period ago
// (a device going quiet at the minute edge must not lose its last sample).
// Not safe for concurrent use — the caller drains events from one loop.
type MinuteRecorder struct {
	buckets map[string]*minuteBucket
}

type minuteBucket struct {
	minute time.Time // minute start (UTC, truncated)
	maxC   *float64
	minC   *float64
	spots  []SpotReading
}

// MinuteSample is one aggregated history row.
type MinuteSample struct {
	CameraID string
	Minute   time.Time // minute start, UTC
	MaxC     *float64  // scene max within the minute; nil when never reported
	MinC     *float64  // scene min within the minute; nil when never reported
	Spots    []SpotReading
}

// minuteGrace is how long a closed minute's bucket waits for stragglers
// before Due flushes it: readings arrive every ~2s, so a few seconds cover
// clock skew without holding rows open indefinitely.
const minuteGrace = 10 * time.Second

// NewMinuteRecorder returns an empty recorder.
func NewMinuteRecorder() *MinuteRecorder {
	return &MinuteRecorder{buckets: make(map[string]*minuteBucket)}
}

// Observe folds one reading into its camera's bucket. When the reading moves
// the camera into a newer minute, the closed bucket is returned as a sample
// (nil when the closed minute had no usable data — e.g. spot-only frames
// before any extreme was reported).
func (r *MinuteRecorder) Observe(now time.Time, rd Reading) *MinuteSample {
	ts := rd.Timestamp
	if ts.IsZero() {
		ts = now
	}
	ts = ts.UTC()
	minute := ts.Truncate(time.Minute)

	b, ok := r.buckets[rd.CameraID]
	if !ok {
		r.buckets[rd.CameraID] = &minuteBucket{minute: minute, maxC: rd.MaxC, minC: rd.MinC, spots: rd.Spots}
		return nil
	}
	if minute.After(b.minute) {
		sample := b.sample(rd.CameraID)
		r.buckets[rd.CameraID] = &minuteBucket{minute: minute, maxC: rd.MaxC, minC: rd.MinC, spots: rd.Spots}
		return sample
	}
	if minute.Before(b.minute) {
		// Straggler from a minute already closed — drop (its data was either
		// already flushed or belongs to a gap we no longer hold open).
		return nil
	}
	if rd.MaxC != nil {
		if b.maxC == nil || *rd.MaxC > *b.maxC {
			v := *rd.MaxC
			b.maxC = &v
		}
	}
	if rd.MinC != nil {
		if b.minC == nil || *rd.MinC < *b.minC {
			v := *rd.MinC
			b.minC = &v
		}
	}
	if len(rd.Spots) > 0 {
		b.spots = rd.Spots
	}
	return nil
}

// Due flushes every bucket whose minute ended more than minuteGrace ago.
// Called from a periodic ticker so a camera going silent still lands its
// last minute in the history.
func (r *MinuteRecorder) Due(now time.Time) []MinuteSample {
	var out []MinuteSample
	for cam, b := range r.buckets {
		if now.Sub(b.minute.Add(time.Minute)) >= minuteGrace {
			if s := b.sample(cam); s != nil {
				out = append(out, *s)
			}
			delete(r.buckets, cam)
		}
	}
	return out
}

// sample materializes a bucket; nil when neither extreme was ever reported
// (nothing to chart).
func (b *minuteBucket) sample(cameraID string) *MinuteSample {
	if b.maxC == nil && b.minC == nil {
		return nil
	}
	maxC, minC := b.maxC, b.minC
	if maxC != nil {
		v := *maxC
		maxC = &v
	}
	if minC != nil {
		v := *minC
		minC = &v
	}
	return &MinuteSample{CameraID: cameraID, Minute: b.minute, MaxC: maxC, MinC: minC, Spots: b.spots}
}
