// Package offload uploads merged recordings to S3-compatible object storage
// (issue #874, batch 1). Local recording is untouched — this is a strictly
// side-channel consumer of the recordings table.
//
// Trigger model: the issue's design doc speaks of hooking "merge completion",
// but rolling merge (the default, quasi-real-time path) has NO completion
// moment — every segment append commits the row and the window bucket keeps
// growing until the camera leaves the window. The equivalent, deterministic
// predicate is "window elapsed + grace": a merged recording whose ended_at is
// older than MinAge is immutable in practice, so a periodic scan enqueues it.
// The rare late backfill append that slips past the grace is caught by the
// stale check (recording.file_size vs outbox.uploaded_size) and re-uploaded —
// an idempotent overwrite of the same object key.
//
// Crash safety: the offload_outbox table is the single source of truth.
// Startup requeues rows stranded in 'uploading'; PutObject to a fixed key is
// idempotent, so recovery neither duplicates objects nor loses segments.
package offload

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/iobudget"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/objectstore"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// claimBatch is how many outbox rows one worker claims per round trip.
const claimBatch = 2

// discoverBatch bounds candidate discovery per scan.
const discoverBatch = 500

// Options wires the manager. Store is the only required field; everything
// else has production defaults that tests override.
type Options struct {
	Store objectstore.Store

	// Budget paces upload bytes against recording/cleanup I/O (iobudget
	// tenant "offload"). nil = unbudgeted.
	Budget iobudget.Limiter

	// Prefix is the object-key root (config storage.remote.prefix).
	Prefix string

	// ScanInterval is the discovery sweep cadence. MinAge is how long a
	// merged recording must be closed before it is eligible (covers the
	// rolling-merge debounce + backfill latency).
	ScanInterval time.Duration
	MinAge       time.Duration

	// Workers is the upload concurrency (config upload.max_concurrency).
	Workers int

	// BacklogLimit caps pending+uploading rows; 0 = unlimited. At the cap,
	// enqueueing stops and a warning fires (uplink slower than production —
	// fail loudly, never drop silently).
	BacklogLimit int

	// UploadPause paces consecutive PUTs from one worker (vision repush
	// pattern: don't burst the uplink). Seamed for tests.
	UploadPause time.Duration

	// Logger override (defaults to the offload component logger).
	Logger *slog.Logger
}

func (o *Options) normalize() {
	if o.ScanInterval <= 0 {
		o.ScanInterval = time.Minute
	}
	if o.MinAge < 0 {
		o.MinAge = 0
	}
	if o.Workers <= 0 {
		o.Workers = 1
	}
	if o.UploadPause <= 0 {
		o.UploadPause = 200 * time.Millisecond
	}
	if o.Prefix == "" {
		o.Prefix = "recordings"
	}
	if o.Logger == nil {
		o.Logger = slogx.Component("offload")
	}
}

// Manager is the offload service: scan loop + upload workers.
type Manager struct {
	db  *storage.DB
	opt Options
	log *slog.Logger

	cancel context.CancelFunc
	wg     sync.WaitGroup

	// wake nudges workers after a scan enqueued rows (buffered 1 — a scan
	// while a worker is already draining is a no-op).
	wake chan struct{}

	// backlogWarnAt throttles the backlog-cap warning to once per interval.
	backlogWarnMu   sync.Mutex
	backlogWarnLast time.Time
}

// NewManager builds the offload manager. It does nothing until Start.
func NewManager(db *storage.DB, opt Options) *Manager {
	opt.normalize()
	return &Manager{db: db, opt: opt, log: opt.Logger, wake: make(chan struct{}, 1)}
}

// Name implements the app.Service contract.
func (m *Manager) Name() string { return "offload" }

// Start runs the crash-recovery sweep and launches the scan loop + workers.
func (m *Manager) Start(ctx context.Context) error {
	if n, err := m.db.RequeueUploadingOffload(ctx); err != nil {
		m.log.Warn("offload: recovery sweep failed", "error", err)
	} else if n > 0 {
		m.log.Info("offload: recovered interrupted uploads", "rows", n)
	}

	ctx, m.cancel = context.WithCancel(ctx)
	// wg.Add before every go (sync.WaitGroup contract).
	m.wg.Add(1 + m.opt.Workers)
	go m.scanLoop(ctx)
	for i := range m.opt.Workers {
		go m.worker(ctx, i)
	}
	m.log.Info("offload uploader started",
		"workers", m.opt.Workers, "scan_interval", m.opt.ScanInterval, "min_age", m.opt.MinAge)
	return nil
}

// Stop cancels the loops and waits for all goroutines (app.Service contract:
// nothing outlives Stop).
func (m *Manager) Stop() error {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
	return nil
}

// scanLoop periodically discovers upload candidates and re-checks stale
// uploads, then nudges the workers.
func (m *Manager) scanLoop(ctx context.Context) {
	defer m.wg.Done()
	m.runScan(ctx)
	ticker := time.NewTicker(m.opt.ScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.runScan(ctx)
		}
	}
}

func (m *Manager) runScan(ctx context.Context) {
	if ctx.Err() != nil {
		return // shutting down: skip quietly (canceled queries are not failures)
	}
	if n, err := m.db.RequeueStaleUploadedOffload(ctx); err != nil {
		m.log.Warn("offload: stale requeue failed", "error", err)
	} else if n > 0 {
		m.log.Info("offload: re-queueing grown recordings for re-upload", "rows", n)
	}

	backlogBefore := 0
	if m.opt.BacklogLimit > 0 {
		var err error
		backlogBefore, err = m.db.CountOffloadBacklog(ctx)
		if err != nil {
			m.log.Warn("offload: backlog count failed", "error", err)
			return
		}
		if backlogBefore >= m.opt.BacklogLimit {
			m.warnBacklogOnce(backlogBefore)
			return
		}
	}

	cutoff := time.Now().UTC().Add(-m.opt.MinAge)
	cands, err := m.db.ListOffloadCandidates(ctx, cutoff, discoverBatch)
	if err != nil {
		m.log.Warn("offload: candidate discovery failed", "error", err)
		return
	}
	enqueued := 0
	for _, c := range cands {
		if ctx.Err() != nil {
			return
		}
		// Meter against the cap DURING the batch too — a single scan must not
		// overshoot the backlog limit by a whole discoverBatch.
		if m.opt.BacklogLimit > 0 && backlogBefore+enqueued >= m.opt.BacklogLimit {
			break
		}
		inserted, err := m.db.EnqueueOffload(ctx, storage.OffloadItem{
			RecordingID: c.RecordingID,
			CameraID:    c.CameraID,
			ObjectKey:   ObjectKey(m.opt.Prefix, c.CameraID, c.StartedAt, c.RecordingID, filepath.Ext(c.FilePath)),
			LocalPath:   c.FilePath,
			FileSize:    c.FileSize,
		})
		if err != nil {
			m.log.Warn("offload: enqueue failed", "recording", c.RecordingID, "error", err)
			continue
		}
		if inserted {
			enqueued++
		}
	}
	if enqueued > 0 {
		m.log.Info("offload: enqueued recordings", "count", enqueued)
		select {
		case m.wake <- struct{}{}:
		default: // a worker is already draining
		}
	}
}

// warnBacklogOnce fires the backlog warning at most once per scan interval —
// the condition persists for hours by nature; per-scan spam helps nobody.
func (m *Manager) warnBacklogOnce(backlog int) {
	m.backlogWarnMu.Lock()
	defer m.backlogWarnMu.Unlock()
	if time.Since(m.backlogWarnLast) < m.opt.ScanInterval {
		return
	}
	m.backlogWarnLast = time.Now()
	m.log.Warn("offload: upload backlog at cap — enqueueing paused (uplink slower than recording production; NOT dropping data, local retention still applies)",
		"backlog", backlog, "cap", m.opt.BacklogLimit)
}

// worker claims pending rows and uploads them until the outbox drains.
func (m *Manager) worker(ctx context.Context, idx int) {
	defer m.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		items, err := m.db.ClaimPendingOffload(ctx, claimBatch)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			m.log.Warn("offload: claim failed", "worker", idx, "error", err)
			m.waitWake(ctx)
			continue
		}
		if len(items) == 0 {
			m.waitWake(ctx)
			continue
		}
		for _, it := range items {
			if ctx.Err() != nil {
				return
			}
			m.uploadOne(ctx, it)
			if m.opt.UploadPause > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(m.opt.UploadPause):
				}
			}
		}
	}
}

// waitWake parks until the next scan signal or shutdown. A short idle re-poll
// covers rows enqueued between the claim and the park.
func (m *Manager) waitWake(ctx context.Context) {
	timer := time.NewTimer(m.opt.ScanInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-m.wake:
	case <-timer.C:
	}
}

// uploadOne runs the per-object pipeline: budget → PUT → Head-verify → mark.
// Every failure path keeps the outbox consistent; none blocks recording.
func (m *Manager) uploadOne(ctx context.Context, it storage.OffloadItem) {
	st, err := os.Stat(it.LocalPath)
	if err != nil {
		if os.IsNotExist(err) {
			m.log.Warn("offload: local file vanished before upload (retention won?) — skipping permanently",
				"recording", it.RecordingID, "path", it.LocalPath)
			if err := m.db.MarkOffloadSkipped(ctx, it.ID, "local file missing"); err != nil {
				m.log.Warn("offload: mark skipped failed", "id", it.ID, "error", err)
			}
			return
		}
		m.log.Warn("offload: stat failed", "path", it.LocalPath, "error", err)
		_ = m.db.MarkOffloadRetry(ctx, it.ID, fmt.Sprintf("stat: %v", err))
		return
	}
	size := st.Size()

	if m.opt.Budget != nil {
		if err := m.opt.Budget.Wait(ctx, iobudget.ConsumerOffload, size); err != nil {
			if ctx.Err() != nil {
				return // shutdown mid-wait; row stays 'uploading' → recovery requeues
			}
			m.log.Warn("offload: budget wait failed", "error", err)
			_ = m.db.MarkOffloadRetry(ctx, it.ID, fmt.Sprintf("budget: %v", err))
			return
		}
	}

	f, err := os.Open(it.LocalPath)
	if err != nil {
		_ = m.db.MarkOffloadRetry(ctx, it.ID, fmt.Sprintf("open: %v", err))
		return
	}
	etag, err := m.opt.Store.Put(ctx, it.ObjectKey, f, size)
	f.Close()
	if err != nil {
		if ctx.Err() != nil {
			return // shutdown mid-PUT; recovery requeues and re-uploads idempotently
		}
		m.log.Warn("offload: upload failed — will retry next scan",
			"recording", it.RecordingID, "key", it.ObjectKey, "attempt", it.Attempts+1, "error", err)
		_ = m.db.MarkOffloadRetry(ctx, it.ID, err.Error())
		return
	}

	// Upload confirmation: HEAD the object and compare sizes. This is the
	// gate for 'uploaded' (eviction eligibility) — a truncated/ghost success
	// must never look confirmed. (ETag equality is not portable: multipart
	// ETags are not content MD5s.)
	info, err := m.opt.Store.Head(ctx, it.ObjectKey)
	if err != nil || info.Size != size {
		verr := fmt.Sprintf("post-upload verify failed (head: %v, remote=%d local=%d)", err, info.Size, size)
		m.log.Warn("offload: "+verr, "recording", it.RecordingID, "key", it.ObjectKey)
		_ = m.db.MarkOffloadRetry(ctx, it.ID, verr)
		return
	}

	if err := m.db.MarkOffloadUploaded(ctx, it.ID, etag, size); err != nil {
		m.log.Warn("offload: mark uploaded failed", "id", it.ID, "error", err)
		return
	}
	m.log.Info("offload: uploaded",
		"recording", it.RecordingID, "camera", it.CameraID,
		"key", it.ObjectKey, "bytes", size, "attempt", it.Attempts+1)
}

// ObjectKey derives the deterministic object key for a recording:
// <prefix>/<camera>/<YYYY>/<MM>/<DD>/<recording-id><ext>. Keyed by recording
// ID (unique in the outbox) so re-uploads always overwrite the same object.
func ObjectKey(prefix, cameraID string, startedAt time.Time, recordingID, ext string) string {
	startedAt = startedAt.UTC()
	key := fmt.Sprintf("%s/%04d/%02d/%02d/%s%s",
		cameraID, startedAt.Year(), int(startedAt.Month()), startedAt.Day(), recordingID, ext)
	if prefix != "" {
		key = prefix + "/" + key
	}
	return key
}
