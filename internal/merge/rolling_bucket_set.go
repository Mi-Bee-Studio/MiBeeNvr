package merge

import (
	"sync"
	"time"
)

// cameraBucketSet holds the retained rolling buckets for one camera (#764).
//
// Before retention there was exactly one live bucket per camera: a camera
// oscillating between two parameter sets (xiaomi HD/SD quality flapping in a
// reconnect storm) finalized and re-created its bucket on every flip, ~every
// 30s-2min — a micro-merge storm of tiny output files and constant DB/rename
// churn that also burned the shared IO budget (#751). The set keeps the most
// recently used buckets keyed by parameter set so flipping BACK appends to
// the existing bucket instead of spawning a new one.
//
// Eviction policy (applied on each segment admission):
//   - idle TTL: a bucket with no append for cfg.BucketIdleTTL is finalized;
//   - capacity: at cfg.BucketRetain buckets, the least recently used one is
//     finalized to admit the new key;
//   - size limit: a matched bucket approaching the MP4 mdat cap (3 GiB) is
//     finalized and a fresh bucket starts within the same window.
//
// "Finalize" is in-memory only — every append already commits the row, so
// the DB/file state is final the moment the last append returned. Finalizing
// just drops the bucket from the retained set and accounts the event.
type cameraBucketSet struct {
	mu      sync.Mutex
	buckets []*bucketInfo // LRU order: least recently used first
}

// bucketSetFor loads or creates the retained-bucket set for a camera.
func (r *RollingMergeCoordinator) bucketSetFor(cameraID string) *cameraBucketSet {
	setAny, _ := r.buckets.LoadOrStore(cameraID, &cameraBucketSet{})
	return setAny.(*cameraBucketSet)
}

// now returns the coordinator's clock — a seam so retention TTL tests advance
// time deterministically instead of sleeping.
func (r *RollingMergeCoordinator) now() time.Time {
	if r.nowFn != nil {
		return r.nowFn()
	}
	return time.Now()
}

// selectBucket returns the bucket an incoming segment should append to,
// applying the retention policy. Callers must NOT hold set.mu or any
// bucket.mu; the returned bucket is still unlocked (the caller locks it for
// the merge itself).
//
// A bucket matches when parameter key, audio key AND window all agree — the
// same conditions the old single-bucket path checked before finalizing, now
// expressed as a lookup. A matched bucket at the size limit is finalized
// (reason "size_limit") and replaced by a fresh bucket in the same window,
// mirroring the pre-retention behavior.
func (r *RollingMergeCoordinator) selectBucket(
	cameraID string,
	spsKey, audioKey string,
	windowStart, windowEnd time.Time,
	newSegmentBytes int64,
	cfg RollingMergeConfig,
) *bucketInfo {
	now := r.now()
	set := r.bucketSetFor(cameraID)
	set.mu.Lock()
	defer set.mu.Unlock()

	// Match pass: resume the retained bucket with this exact key — unless it
	// has been idle past the TTL (the camera moved on and came back much
	// later; resuming across the gap would re-open an ancient bucket).
	if ttl := cfg.BucketIdleTTL; ttl > 0 {
		kept := set.buckets[:0]
		for _, b := range set.buckets {
			if now.Sub(b.lastAppend) > ttl {
				r.finalizeBucketLocked(cameraID, b, "idle_ttl", now)
				continue
			}
			kept = append(kept, b)
		}
		set.buckets = kept
	}
	for i, b := range set.buckets {
		if b.spsKey != spsKey || b.audioKey != audioKey || !b.windowStart.Equal(windowStart) {
			continue
		}
		// Size-roll a matched bucket that would cross the mdat cap: finalize
		// it and fall through to fresh-bucket creation below.
		if b.mergedFilePath != "" && b.mergedFileSize > 0 && b.mergedFileSize+newSegmentBytes > bucketSizeLimit {
			r.finalizeBucketLocked(cameraID, b, "size_limit", now)
			set.buckets = append(set.buckets[:i], set.buckets[i+1:]...)
			break
		}
		// Move to MRU position.
		set.buckets = append(set.buckets[:i], set.buckets[i+1:]...)
		set.buckets = append(set.buckets, b)
		return b
	}

	// Idle-TTL sweep already ran before the match pass (it applies to matched
	// and unmatched buckets alike) — capacity is the remaining constraint.
	retain := cfg.BucketRetain
	if retain < 1 {
		retain = 1
	}
	for len(set.buckets) >= retain {
		oldest := set.buckets[0]
		set.buckets = set.buckets[1:]
		r.finalizeBucketLocked(cameraID, oldest, "capacity_lru", now)
	}

	fresh := &bucketInfo{
		windowStart: windowStart,
		windowEnd:   windowEnd,
		createdAt:   now,
		lastAppend:  now,
	}
	set.buckets = append(set.buckets, fresh)
	return fresh
}

// finalizeBucketLocked accounts a bucket leaving the retained set. The row
// and file are already final (every append commits); this exists for the
// observability added in #764 — finalize frequency and bucket lifetime are
// the signals that verify retention is working on quality-oscillating cameras.
// Caller holds set.mu.
func (r *RollingMergeCoordinator) finalizeBucketLocked(cameraID string, b *bucketInfo, reason string, now time.Time) {
	lifetime := now.Sub(b.createdAt)
	if r.metrics != nil {
		r.metrics.RecordRollingBucketFinalized(reason, lifetime)
	}
	rollingLogger.Debug("rolling bucket finalized",
		"camera_id", cameraID,
		"reason", reason,
		"segments", b.segmentCount,
		"lifetime_s", lifetime.Seconds())
}

// dropBucketIfEmpty removes a bucket from its camera's set when a failed
// merge left it in the never-created state (no file, no row). Keeping it
// would waste a retention slot on a phantom bucket that can never match a
// real segment (empty keys).
func (r *RollingMergeCoordinator) dropBucketIfEmpty(cameraID string, b *bucketInfo) {
	setAny, ok := r.buckets.Load(cameraID)
	if !ok {
		return
	}
	set := setAny.(*cameraBucketSet)
	set.mu.Lock()
	defer set.mu.Unlock()
	if b.mergedFilePath != "" || b.mergedRecID != "" || b.segmentCount != 0 {
		return
	}
	for i, cand := range set.buckets {
		if cand == b {
			set.buckets = append(set.buckets[:i], set.buckets[i+1:]...)
			return
		}
	}
}

// newestBucket returns the camera's most recently used bucket, or nil when
// the camera has no set yet (disabled camera, non-MP4 format).
func (r *RollingMergeCoordinator) newestBucket(cameraID string) *bucketInfo {
	setAny, ok := r.buckets.Load(cameraID)
	if !ok {
		return nil
	}
	set := setAny.(*cameraBucketSet)
	set.mu.Lock()
	defer set.mu.Unlock()
	if len(set.buckets) == 0 {
		return nil
	}
	return set.buckets[len(set.buckets)-1]
}
