package offload

// evict.go — verified local eviction (issue #874 batch 1, CLI-first). "Evict"
// = delete the local file + recordings row after the remote object is
// CONFIRMED present (fresh HeadObject, exact uploaded size). The remote
// object is never deleted by the NVR — remote retention is the bucket's
// lifecycle policy (batch-3 stance).
//
// The pre-evict HeadObject is the only defense against an upload that lied
// (client-side success, server-side loss). A refused verification leaves
// everything untouched and reports loudly; the operator investigates.

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/objectstore"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// evictBatch bounds rows per RunEvict call (CLI re-runs to continue).
const evictBatch = 500

// EvictOptions selects the evict candidate set and the run posture.
type EvictOptions struct {
	// StoreFor resolves the Store for an outbox row's bucket ('' = the
	// default bucket) — per-camera routing means one evict run touches
	// several buckets.
	StoreFor func(bucket string) (objectstore.Store, error)

	// ConfirmedBefore: only items whose upload was confirmed (uploaded_at)
	// before this instant are eligible — the caller derives it from
	// evict.after_days (retention window) or --all-uploaded (now).
	ConfirmedBefore time.Time

	// CameraID restricts eviction to one camera ("" = all).
	CameraID string

	// Execute applies the eviction; false = dry-run report only (default
	// posture — validation before deletion, per the issue's batching).
	Execute bool
}

// EvictRefusal records why one candidate was NOT evicted.
type EvictRefusal struct {
	RecordingID string
	ObjectKey   string
	Reason      string
}

// EvictSummary is the run report.
type EvictSummary struct {
	Eligible       int // passed verification (dry-run would evict these)
	Evicted        int // actually evicted (Execute only)
	Refused        []EvictRefusal
	ReclaimedBytes int64 // bytes freed locally (Execute only)
}

// RunEvict walks the evictable set, verifies each remote object, and — only
// under Execute — removes the local file + recordings row and marks the
// outbox row evicted. Fail-stop per item: a refused or failed item never
// blocks the rest, but never loses data either.
func RunEvict(ctx context.Context, db *storage.DB, opt EvictOptions) (EvictSummary, error) {
	summary := EvictSummary{}
	items, err := db.ListOffloadEvictable(ctx, opt.ConfirmedBefore, evictBatch)
	if err != nil {
		return summary, fmt.Errorf("list evictable: %w", err)
	}
	for _, it := range items {
		if ctx.Err() != nil {
			return summary, ctx.Err()
		}
		if opt.CameraID != "" && it.CameraID != opt.CameraID {
			continue
		}

		store, serr := opt.StoreFor(it.Bucket)
		if serr != nil {
			summary.Refused = append(summary.Refused, EvictRefusal{
				RecordingID: it.RecordingID, ObjectKey: it.ObjectKey,
				Reason: fmt.Sprintf("store resolve: %v", serr),
			})
			continue
		}
		info, err := store.Head(ctx, it.ObjectKey)
		switch {
		case err != nil:
			summary.Refused = append(summary.Refused, EvictRefusal{
				RecordingID: it.RecordingID, ObjectKey: it.ObjectKey,
				Reason: fmt.Sprintf("remote verify failed: %v", err),
			})
			continue
		case info.Size != it.UploadedSize:
			summary.Refused = append(summary.Refused, EvictRefusal{
				RecordingID: it.RecordingID, ObjectKey: it.ObjectKey,
				Reason: fmt.Sprintf("size mismatch: remote=%d uploaded=%d — object was replaced?", info.Size, it.UploadedSize),
			})
			continue
		}

		summary.Eligible++
		if !opt.Execute {
			continue
		}

		// Order matters: delete the file first, then the row, then the
		// outbox transition. A crash mid-sequence leaves an 'uploaded' row
		// with a missing file — harmless (the uploader skips missing files)
		// and recoverable by re-running the evict.
		if err := os.Remove(it.LocalPath); err != nil && !os.IsNotExist(err) {
			summary.Refused = append(summary.Refused, EvictRefusal{
				RecordingID: it.RecordingID, ObjectKey: it.ObjectKey,
				Reason: fmt.Sprintf("local delete failed: %v", err),
			})
			summary.Eligible-- // verified but not evicted after all
			continue
		}
		if err := db.DeleteRecording(ctx, it.RecordingID); err != nil {
			summary.Refused = append(summary.Refused, EvictRefusal{
				RecordingID: it.RecordingID, ObjectKey: it.ObjectKey,
				Reason: fmt.Sprintf("recording row delete failed: %v", err),
			})
			continue
		}
		if err := db.MarkOffloadEvicted(ctx, it.ID); err != nil {
			return summary, fmt.Errorf("mark evicted (id=%d): %w", it.ID, err)
		}
		summary.Evicted++
		summary.ReclaimedBytes += it.UploadedSize
	}
	return summary, nil
}
