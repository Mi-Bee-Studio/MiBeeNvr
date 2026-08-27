package migration

// Migrator tests (#570): the Store/DB surfaces are faked; real files move
// inside t.TempDir() so the copy/rewrite/delete transactionality is exercised
// end-to-end. Deterministic — no wall-clock waits (window parsing is tested
// against injected instants).

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeStore struct {
	roots  []string
	usage  map[string][2]int64 // total, free
	getErr error
}

func (s *fakeStore) Roots() []string { return s.roots }

func (s *fakeStore) GetRootUsage(root string) (int64, int64, error) {
	if s.getErr != nil {
		return 0, 0, s.getErr
	}
	u := s.usage[root]
	return u[0], u[1], nil
}

type fakeDB struct {
	recs     []MigratableRecording
	listErr  error
	rewrites []string // recording IDs rewritten
}

func (d *fakeDB) ListMigratableRecordings(_ context.Context, _, _ string) ([]MigratableRecording, error) {
	return d.recs, d.listErr
}

func (d *fakeDB) RewriteRecordingPaths(_ context.Context, id, newFile string, _ sql.NullString) error {
	d.rewrites = append(d.rewrites, id)
	return nil
}

func writeRecFile(t *testing.T, dir, rel string, size int) string {
	t.Helper()
	p := filepath.Join(dir, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, make([]byte, size), 0o644))
	return p
}

func newTestMigrator(t *testing.T, db *fakeDB, store *fakeStore) *Migrator {
	t.Helper()
	return New(db, store, func() int { return 10 << 20 }, func() string { return "" })
}

// A full job: files outside the target are copied, DB rows rewritten, and
// (with deleteSource) sources removed.
func TestMigratorFullJob(t *testing.T) {
	srcRoot := t.TempDir()
	dstRoot := t.TempDir()

	rec1 := writeRecFile(t, srcRoot, "cam1/seg1.mp4", 4096)
	rec2 := writeRecFile(t, srcRoot, "cam1/seg2.mp4", 8192)

	db := &fakeDB{recs: []MigratableRecording{
		{ID: "r1", FilePath: rec1, FileSize: 4096},
		{ID: "r2", FilePath: rec2, MergePath: "", FileSize: 8192},
	}}
	store := &fakeStore{roots: []string{srcRoot, dstRoot}, usage: map[string][2]int64{
		srcRoot: {1 << 30, 1 << 30},
		dstRoot: {1 << 30, 1 << 30},
	}}
	m := newTestMigrator(t, db, store)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	defer m.Stop()

	j := m.Enqueue("cam1", dstRoot, true)
	require.NotNil(t, j)

	require.Eventually(t, func() bool {
		state, jobs := m.Status()
		return len(jobs) == 1 && jobs[0].State == "done" && state != ""
	}, 10*time.Second, 50*time.Millisecond, "job must complete")

	require.Equal(t, []string{"r1", "r2"}, db.rewrites, "both rows must be rewritten")
	_, err := os.Stat(filepath.Join(dstRoot, "cam1", "seg1.mp4"))
	require.NoError(t, err, "file copied into the target root")
	_, err = os.Stat(rec1)
	require.True(t, os.IsNotExist(err), "source removed with deleteSource")
}

// Capacity gate: a target that cannot fit the remaining bytes fails the job
// before any copy.
func TestMigratorCapacityGate(t *testing.T) {
	srcRoot := t.TempDir()
	dstRoot := t.TempDir()

	rec := writeRecFile(t, srcRoot, "cam1/seg1.mp4", 4096)
	db := &fakeDB{recs: []MigratableRecording{{ID: "r1", FilePath: rec, FileSize: 4096}}}
	// Free space far below 4096 * 1.2.
	store := &fakeStore{roots: []string{srcRoot, dstRoot}, usage: map[string][2]int64{
		srcRoot: {1 << 30, 1 << 30},
		dstRoot: {1 << 30, 100},
	}}
	m := newTestMigrator(t, db, store)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	defer m.Stop()

	m.Enqueue("cam1", dstRoot, false)

	require.Eventually(t, func() bool {
		_, jobs := m.Status()
		return len(jobs) == 1 && jobs[0].State == "failed"
	}, 10*time.Second, 50*time.Millisecond, "insufficient target capacity must fail the job")
	require.Empty(t, db.rewrites, "nothing rewritten when gated")
}

func TestInWindow(t *testing.T) {
	night := time.Date(2026, 8, 27, 23, 0, 0, 0, time.UTC)
	morning := time.Date(2026, 8, 27, 3, 0, 0, 0, time.UTC)
	noon := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	// Overnight window wraps midnight.
	require.True(t, inWindow("22:00-06:00", night))
	require.True(t, inWindow("22:00-06:00", morning))
	require.False(t, inWindow("22:00-06:00", noon))

	// Same-day window.
	require.True(t, inWindow("09:00-18:00", noon))
	require.False(t, inWindow("09:00-18:00", night))

	// Empty / malformed spec = always allowed.
	require.True(t, inWindow("", noon))
	require.True(t, inWindow("garbage", noon))
}

func TestHumanBytes(t *testing.T) {
	require.Equal(t, "0 B", humanBytes(0))
	require.Equal(t, "512 B", humanBytes(512))
	require.Equal(t, "1.0 MB", humanBytes(1<<20))
	require.Equal(t, "1.5 GB", humanBytes(3<<29))
}

func TestRootOf(t *testing.T) {
	db := &fakeDB{}
	store := &fakeStore{roots: []string{"/data/a", "/data/b"}}
	m := newTestMigrator(t, db, store)
	require.Equal(t, "/data/a", m.rootOf("/data/a/cam1/seg.mp4"))
	require.Equal(t, "/data/b", m.rootOf("/data/b/x.mp4"))
	require.Empty(t, m.rootOf("/elsewhere/x.mp4"), "unknown root → empty")
}
