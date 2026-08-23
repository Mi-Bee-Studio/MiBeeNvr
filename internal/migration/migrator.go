// Package migration implements the background idle-time recording migrator.
//
// Per-camera storage switching is HOT: new segments go to the camera's new
// root immediately, while historical files move in the background:
//
//   - rate-limited copy (default 15 MB/s) so recording IO is never starved;
//   - optional local-time window (storage.migration_window, e.g. "22:00-06:00");
//   - per-file transactionality: copy → rewrite that recording's DB row →
//     (optionally) delete the source — any crash leaves both copies and the
//     row still pointing at the intact original;
//   - capacity gate before start and periodically during the run: the target
//     must fit the remaining bytes with a 20% safety margin;
//   - the database itself never moves (it lives on the data volume).
package migration

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Store is the storage-manager surface the migrator needs.
type Store interface {
	Roots() []string
	GetRootUsage(root string) (total, free int64, err error)
}

// DB is the database surface the migrator needs (backed by *storage.DB).
type DB interface {
	ListMigratableRecordings(ctx context.Context, cameraID, keepUnder string) ([]MigratableRecording, error)
	RewriteRecordingPaths(ctx context.Context, id, newFile string, newMerge sql.NullString) error
}

// MigratableRecording is one row whose files still live outside the target.
// Declared as an ALIAS of the anonymous struct so *storage.DB's identically
// shaped MigratableRec satisfies the DB interface without an import cycle.
type MigratableRecording = struct {
	ID        string
	FilePath  string
	MergePath string
	FileSize  int64
}

const (
	defaultRateBytes = 15 * 1024 * 1024 // 15 MB/s
	copyChunk        = 1 << 20          // 1 MB
	freeMargin       = 0.8              // require free*0.8 >= needed
	// capacity recheck cadence and window-poll granularity
	capacityEveryFiles = 200
	windowPoll         = 30 * time.Second
)

// Job is one camera's background migration.
type Job struct {
	CameraID     string     `json:"camera_id"`
	ToRoot       string     `json:"to_root"`
	DeleteSource bool       `json:"delete_source"`
	State        string     `json:"state"` // queued | running | paused | done | failed
	Detail       string     `json:"detail,omitempty"`
	TotalFiles   int64      `json:"total_files"`
	DoneFiles    int64      `json:"done_files"`
	TotalBytes   int64      `json:"total_bytes"`
	DoneBytes    int64      `json:"done_bytes"`
	Error        string     `json:"error,omitempty"`
	EnqueuedAt   time.Time  `json:"enqueued_at"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
}

// Migrator runs the job queue on one worker goroutine.
type Migrator struct {
	db    DB
	store Store
	// rateBytes and window live behind the config getter so runtime config
	// changes apply without restart.
	rateBytes func() int
	window    func() string

	mu      sync.Mutex
	queue   []*Job
	history []*Job
	stopCh  chan struct{}
	wg      sync.WaitGroup
	started bool
}

// New creates a Migrator (call Start to launch the worker).
func New(db DB, store Store, rateBytes func() int, window func() string) *Migrator {
	return &Migrator{
		db:        db,
		store:     store,
		rateBytes: rateBytes,
		window:    window,
		stopCh:    make(chan struct{}),
	}
}

// Enqueue adds (or replaces) the background migration of one camera's
// recordings to toRoot. Returns a snapshot of the job — callers must never
// hold live pointers into the queue (the worker mutates jobs under m.mu).
func (m *Migrator) Enqueue(cameraID, toRoot string, deleteSource bool) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Replace any pending/running job for the same camera+target.
	for _, j := range m.queue {
		if j.CameraID == cameraID && j.ToRoot == toRoot && (j.State == "queued" || j.State == "running" || j.State == "paused") {
			cp := *j
			return &cp
		}
	}
	job := &Job{
		CameraID:     cameraID,
		ToRoot:       toRoot,
		DeleteSource: deleteSource,
		State:        "queued",
		EnqueuedAt:   time.Now(),
	}
	m.queue = append(m.queue, job)
	cp := *job
	return &cp
}

// Status returns the active queue plus the last finished jobs, as snapshot
// copies (see Enqueue). Newest last, history capped at 20.
func (m *Migrator) Status() (state string, jobs []*Job) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.history) > 20 {
		m.history = m.history[len(m.history)-20:]
	}
	state = "idle"
	all := make([]*Job, 0, len(m.queue)+len(m.history))
	for _, src := range [2][]*Job{m.queue, m.history} {
		for _, j := range src {
			cp := *j
			all = append(all, &cp)
			if j.State == "queued" || j.State == "running" || j.State == "paused" {
				state = "running"
			}
		}
	}
	return state, all
}

// Start launches the worker goroutine bound to ctx. Idempotent.
func (m *Migrator) Start(ctx context.Context) {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.mu.Unlock()
	m.wg.Add(1)
	go m.run(ctx)
}

// Stop cancels the worker and waits for the in-flight file to finish.
func (m *Migrator) Stop() {
	select {
	case <-m.stopCh:
	default:
		close(m.stopCh)
	}
	m.wg.Wait()
}

func (m *Migrator) run(ctx context.Context) {
	defer m.wg.Done()
	for {
		job := m.next()
		if job == nil {
			select {
			case <-m.stopCh:
				return
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		m.process(ctx, job)
	}
}

func (m *Migrator) next() *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.queue) == 0 {
		return nil
	}
	job := m.queue[0]
	m.queue = m.queue[1:]
	now := time.Now()
	job.State = "running"
	job.StartedAt = &now
	return job
}

func (m *Migrator) finish(job *Job, state, errStr string) {
	now := time.Now()
	m.mu.Lock()
	job.State = state
	job.Error = errStr
	job.FinishedAt = &now
	m.history = append(m.history, job)
	m.mu.Unlock()
	if state == "done" {
		slog.Info("storage migration finished", "camera_id", job.CameraID,
			"to", job.ToRoot, "files", job.DoneFiles, "bytes", job.DoneBytes)
	}
}

// rootOf returns the known storage root a path lives under ("" = none).
func (m *Migrator) rootOf(path string) string {
	roots := m.store.Roots()
	sort.Slice(roots, func(i, j int) bool { return len(roots[i]) > len(roots[j]) })
	for _, r := range roots {
		if strings.HasPrefix(path, r+"/") {
			return r
		}
	}
	return ""
}

// inWindow reports whether background migration is currently allowed.
func inWindow(spec string, now time.Time) bool {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return true
	}
	parse := func(s string) (int, bool) {
		var h, mm int
		if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d:%d", &h, &mm); err != nil {
			return 0, false
		}
		if h < 0 || h > 23 || mm < 0 || mm > 59 {
			return 0, false
		}
		return h*60 + mm, true
	}
	parts := strings.SplitN(spec, "-", 2)
	if len(parts) != 2 {
		return true
	}
	start, ok1 := parse(parts[0])
	end, ok2 := parse(parts[1])
	if !ok1 || !ok2 {
		return true
	}
	cur := now.Hour()*60 + now.Minute()
	if start <= end {
		return cur >= start && cur < end
	}
	return cur >= start || cur < end // crosses midnight
}

// waitForWindow blocks until the window opens or stop fires.
func (m *Migrator) waitForWindow(job *Job, ctx context.Context) bool {
	for {
		if !inWindow(m.window(), time.Now()) {
			m.setJobState(job, "paused", "outside migration window")
			select {
			case <-m.stopCh:
				return false
			case <-ctx.Done():
				return false
			case <-time.After(windowPoll):
			}
			continue
		}
		m.setJobState(job, "running", "")
		return true
	}
}

func (m *Migrator) setJobState(job *Job, state, detail string) {
	m.mu.Lock()
	job.State = state
	job.Detail = detail
	m.mu.Unlock()
}

// checkCapacity verifies the target can fit the remaining bytes.
func (m *Migrator) checkCapacity(job *Job, needed int64) error {
	if needed <= 0 {
		return nil
	}
	_, free, err := m.store.GetRootUsage(job.ToRoot)
	if err != nil {
		return fmt.Errorf("capacity check: %w", err)
	}
	if int64(float64(free)*freeMargin) < needed {
		return fmt.Errorf("insufficient space on %s: need %s, only %s free (20%% safety margin)",
			job.ToRoot, humanBytes(needed), humanBytes(free))
	}
	return nil
}

func humanBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func (m *Migrator) process(ctx context.Context, job *Job) {
	recs, err := m.db.ListMigratableRecordings(ctx, job.CameraID, job.ToRoot)
	if err != nil {
		m.finish(job, "failed", "list recordings: "+err.Error())
		return
	}
	// Pre-count + capacity gate.
	var needed int64
	for _, r := range recs {
		needed += r.FileSize
	}
	m.mu.Lock()
	job.TotalFiles = int64(len(recs))
	job.TotalBytes = needed
	m.mu.Unlock()
	if len(recs) == 0 {
		m.finish(job, "done", "")
		return
	}
	if err := m.checkCapacity(job, needed); err != nil {
		m.finish(job, "failed", err.Error())
		return
	}

	var doneFiles, doneBytes int64
	for i, r := range recs {
		select {
		case <-m.stopCh:
			m.finish(job, "paused", "stopped — re-enqueue to resume")
			return
		default:
		}
		if i%capacityEveryFiles == 0 && i > 0 {
			var remaining int64
			for _, rr := range recs[i:] {
				remaining += rr.FileSize
			}
			if err := m.checkCapacity(job, remaining); err != nil {
				m.finish(job, "failed", err.Error())
				return
			}
		}
		if !m.waitForWindow(job, ctx) {
			return
		}

		movedBytes, err := m.moveRecording(ctx, job, r)
		doneBytes += movedBytes
		if err != nil {
			slog.Warn("migration: file failed (row kept on source)", "id", r.ID, "error", err)
			continue // row still points at the intact source — safe to skip
		}
		if movedBytes > 0 || r.FilePath != "" {
			doneFiles++
		}
		m.mu.Lock()
		job.DoneFiles = doneFiles
		job.DoneBytes = doneBytes
		m.mu.Unlock()
	}
	m.finish(job, "done", "")
}

// moveRecording copies one recording's files, rewrites its row, then removes
// the sources. Returns the bytes copied.
func (m *Migrator) moveRecording(ctx context.Context, job *Job, r MigratableRecording) (int64, error) {
	srcRoot := m.rootOf(r.FilePath)
	if srcRoot == "" || srcRoot == job.ToRoot {
		return 0, nil // not under any known root / already there
	}
	newFile := filepath.Join(job.ToRoot, strings.TrimPrefix(r.FilePath, srcRoot+"/"))
	if err := m.copyRated(ctx, r.FilePath, newFile); err != nil {
		return 0, err
	}

	newMerge := sql.NullString{Valid: false}
	if r.MergePath != "" {
		mergeRoot := m.rootOf(r.MergePath)
		if mergeRoot != "" && mergeRoot != job.ToRoot {
			nm := filepath.Join(job.ToRoot, strings.TrimPrefix(r.MergePath, mergeRoot+"/"))
			if err := m.copyRated(ctx, r.MergePath, nm); err != nil {
				return 0, err
			}
			newMerge = sql.NullString{String: nm, Valid: true}
		} else {
			newMerge = sql.NullString{String: r.MergePath, Valid: true}
		}
	}

	if err := m.db.RewriteRecordingPaths(ctx, r.ID, newFile, newMerge); err != nil {
		return 0, fmt.Errorf("rewrite row: %w", err)
	}
	if job.DeleteSource {
		if r.MergePath != "" {
			os.Remove(r.MergePath)
		}
		os.Remove(r.FilePath)
	}
	return r.FileSize, nil
}

// copyRated copies src→dst with the configured rate limit (chunked sleeps).
func (m *Migrator) copyRated(ctx context.Context, src, dst string) error {
	if _, err := os.Stat(dst); err == nil {
		// already copied by a previous (interrupted) run — idempotent skip
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".migrating"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}

	rate := m.rateBytes()
	if rate <= 0 {
		rate = defaultRateBytes
	}
	buf := make([]byte, copyChunk)
	var written int64
	for {
		select {
		case <-ctx.Done():
			out.Close()
			os.Remove(tmp)
			return ctx.Err()
		default:
		}
		n, rerr := io.ReadFull(in, buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				os.Remove(tmp)
				return werr
			}
			written += int64(n)
			// sleep one chunk's worth of the rate budget
			time.Sleep(time.Duration(int64(time.Second) * int64(n) / int64(rate)))
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			out.Close()
			os.Remove(tmp)
			return rerr
		}
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
