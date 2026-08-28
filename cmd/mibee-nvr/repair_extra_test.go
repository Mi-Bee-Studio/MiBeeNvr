package main

// Run-body coverage for the repair subcommands (#594): fragments
// (report/retry/force-delete), delete-by-format (kept/in-flight/AI-protected
// guards), prune-intermediate-mp4, reclaim-orphan-merges, normalize-endpoints,
// merge-status execute, the dispatcher, and the pure helpers — all against a
// real temp SQLite DB seeded through the storage package.

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// seedRepairDB opens <dir>/mibee-nvr.db, applies mut (extra SQL per test),
// inserts the recordings, and closes — leaving the DB on disk for the run
// function to reopen via openDBFromConfig.
func seedRepairDB(t *testing.T, dir string, mut func(*sql.DB), recs ...*model.Recording) {
	t.Helper()
	dbPath := filepath.Join(dir, "mibee-nvr.db")
	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	db, err := storage.New(dbPath)
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	ctx := context.Background()
	for _, r := range recs {
		require.NoError(t, db.InsertRecording(ctx, r))
	}
	if mut != nil {
		mut(raw)
	}
	require.NoError(t, db.Close())
	require.NoError(t, raw.Close())
}

func repairRec(id, camID, format, status string, ended time.Time) *model.Recording {
	return &model.Recording{
		ID: id, CameraID: camID, FilePath: "", Format: model.Format(format),
		StartedAt: ended.Add(-time.Minute), EndedAt: ended, Duration: 60, FileSize: 1024,
		MergeStatus: status,
	}
}

func recordingExists(t *testing.T, dbPath, id string) (found bool, mergeStatus string) {
	t.Helper()
	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer raw.Close()
	err = raw.QueryRow("SELECT COALESCE(merge_status,'') FROM recordings WHERE id=?", id).Scan(&mergeStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ""
	}
	require.NoError(t, err)
	return true, mergeStatus
}

func TestRunRepairDispatch(t *testing.T) {
	var rc int
	withArgs([]string{"mibee-nvr", "repair"}, func() { rc = runRepair() })
	require.Equal(t, 1, rc)

	withArgs([]string{"mibee-nvr", "repair", "--help"}, func() { rc = runRepair() })
	require.Equal(t, 0, rc)

	withArgs([]string{"mibee-nvr", "repair", "bogus-sub"}, func() { rc = runRepair() })
	require.Equal(t, 1, rc)
}

func TestRunRepairFragmentsFlows(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeRepairConfig(t, dir)

	// Validation errors before any DB work.
	var rc int
	withArgs([]string{"bin", "repair", "fragments", "--status", "pending", "--config", cfgPath}, func() { rc = runRepairFragments() })
	require.Equal(t, 1, rc, "live merge status must be refused")

	withArgs([]string{"bin", "repair", "fragments", "--retry", "--force-delete", "--config", cfgPath}, func() { rc = runRepairFragments() })
	require.Equal(t, 1, rc, "--retry + --force-delete are mutually exclusive")

	withArgs([]string{"bin", "repair", "fragments", "--help"}, func() { rc = runRepairFragments() })
	require.Equal(t, 0, rc)

	// Empty DB → nothing to do.
	withArgs([]string{"bin", "repair", "fragments", "--config", cfgPath}, func() { rc = runRepairFragments() })
	require.Equal(t, 0, rc)

	// Seed one incompatible fragment with an on-disk file.
	fragFile := filepath.Join(dir, "cam1_frag.mp4")
	require.NoError(t, os.WriteFile(fragFile, []byte("fragment"), 0o644))
	rec := repairRec("frag-1", "cam1", "h264", model.MergeStatusIncompatible, time.Now().UTC().Add(-time.Hour))
	rec.FilePath = fragFile
	seedRepairDB(t, dir, nil, rec)
	dbPath := filepath.Join(dir, "mibee-nvr.db")

	// Dry run → report only, row and file untouched.
	withArgs([]string{"bin", "repair", "fragments", "--config", cfgPath}, func() { rc = runRepairFragments() })
	require.Equal(t, 0, rc)
	found, status := recordingExists(t, dbPath, "frag-1")
	require.True(t, found)
	require.Equal(t, model.MergeStatusIncompatible, status)
	require.FileExists(t, fragFile)

	// Execute + retry → reset to pending.
	withArgs([]string{"bin", "repair", "fragments", "--retry", "--execute", "--config", cfgPath}, func() { rc = runRepairFragments() })
	require.Equal(t, 0, rc)
	found, status = recordingExists(t, dbPath, "frag-1")
	require.True(t, found)
	require.Equal(t, model.MergeStatusPending, status)
	require.FileExists(t, fragFile)

	// Reset back to incompatible, then execute + force-delete → row + file gone.
	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = raw.Exec("UPDATE recordings SET merge_status=? WHERE id=?", model.MergeStatusIncompatible, "frag-1")
	require.NoError(t, err)
	require.NoError(t, raw.Close())
	withArgs([]string{"bin", "repair", "fragments", "--force-delete", "--execute", "--config", cfgPath}, func() { rc = runRepairFragments() })
	require.Equal(t, 0, rc)
	found, _ = recordingExists(t, dbPath, "frag-1")
	require.False(t, found)
	require.NoFileExists(t, fragFile)

	// Missing config → error.
	withArgs([]string{"bin", "repair", "fragments", "--config", filepath.Join(dir, "nope.yaml")}, func() { rc = runRepairFragments() })
	require.Equal(t, 1, rc)
}

func TestRunRepairDeleteByFormatFlows(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeRepairConfig(t, dir)

	var rc int
	withArgs([]string{"bin", "repair", "delete-by-format", "--config", cfgPath}, func() { rc = runRepairDeleteByFormat() })
	require.Equal(t, 1, rc, "--camera is required")

	withArgs([]string{"bin", "repair", "delete-by-format", "--camera", "cam1", "--config", cfgPath}, func() { rc = runRepairDeleteByFormat() })
	require.Equal(t, 1, rc, "--keep-format is required")

	withArgs([]string{"bin", "repair", "delete-by-format", "--help"}, func() { rc = runRepairDeleteByFormat() })
	require.Equal(t, 0, rc)

	old := time.Now().UTC().Add(-48 * time.Hour)
	tlFile := filepath.Join(dir, "keep_tl.mp4")
	h264File := filepath.Join(dir, "del_h264.mp4")
	require.NoError(t, os.WriteFile(tlFile, []byte("tl"), 0o644))
	require.NoError(t, os.WriteFile(h264File, []byte("h264"), 0o644))

	tl := repairRec("rec-tl", "cam1", "timelapse", model.MergeStatusPending, old)
	tl.FilePath = tlFile
	del := repairRec("rec-h264", "cam1", "h264", model.MergeStatusPending, old)
	del.FilePath = h264File
	inflight := repairRec("rec-inflight", "cam1", "h264", model.MergeStatusPending, time.Time{})
	fresh := repairRec("rec-fresh", "cam1", "h264", model.MergeStatusPending, time.Now().UTC())
	protected := repairRec("rec-ai", "cam1", "h264", model.MergeStatusPending, old)
	dbPath := filepath.Join(dir, "mibee-nvr.db")
	seedRepairDB(t, dir, func(raw *sql.DB) {
		_, err := raw.Exec("UPDATE recordings SET ai_status='processing' WHERE id=?", protected.ID)
		require.NoError(t, err)
	}, tl, del, inflight, fresh, protected)

	// Dry run → nothing deleted.
	withArgs([]string{"bin", "repair", "delete-by-format", "--camera", "cam1", "--keep-format", "timelapse", "--older-than", "24h", "--config", cfgPath}, func() { rc = runRepairDeleteByFormat() })
	require.Equal(t, 0, rc)
	for _, id := range []string{"rec-tl", "rec-h264", "rec-inflight", "rec-fresh", "rec-ai"} {
		found, _ := recordingExists(t, dbPath, id)
		require.True(t, found, "dry run must not delete %s", id)
	}

	// Execute → the aged h264 row is deleted; kept / in-flight / fresh /
	// AI-protected rows survive.
	withArgs([]string{"bin", "repair", "delete-by-format", "--camera", "cam1", "--keep-format", "timelapse", "--older-than", "24h", "--execute", "--config", cfgPath}, func() { rc = runRepairDeleteByFormat() })
	require.Equal(t, 0, rc)
	found, _ := recordingExists(t, dbPath, "rec-h264")
	require.False(t, found)
	require.NoFileExists(t, h264File)
	for _, id := range []string{"rec-tl", "rec-inflight", "rec-fresh", "rec-ai"} {
		found, _ = recordingExists(t, dbPath, id)
		require.True(t, found, "%s must survive the delete", id)
	}
	require.FileExists(t, tlFile)
}

func TestRunRepairPruneIntermediateMP4Flows(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeRepairConfig(t, dir)

	var rc int
	withArgs([]string{"bin", "repair", "prune-intermediate-mp4", "--help"}, func() { rc = runRepairPruneIntermediateMP4() })
	require.Equal(t, 0, rc)

	withArgs([]string{"bin", "repair", "prune-intermediate-mp4", "--before", "not-a-date", "--config", cfgPath}, func() { rc = runRepairPruneIntermediateMP4() })
	require.Equal(t, 1, rc)

	// One daily_merged timelapse with a real intermediate file, one plain
	// merged (must be skipped), one without merge_path (must be skipped).
	interFile := filepath.Join(dir, "intermediate.mp4")
	require.NoError(t, os.WriteFile(interFile, []byte("merged-output"), 0o644))
	old := time.Now().UTC().Add(-72 * time.Hour)
	daily := repairRec("rec-daily", "cam1", "timelapse", model.MergeStatusDailyMerged, old)
	plain := repairRec("rec-plain", "cam1", "timelapse", model.MergeStatusMerged, old)
	noPath := repairRec("rec-nopath", "cam1", "timelapse", model.MergeStatusDailyMerged, old)
	dbPath := filepath.Join(dir, "mibee-nvr.db")
	seedRepairDB(t, dir, func(raw *sql.DB) {
		_, err := raw.Exec("UPDATE recordings SET merge_path=? WHERE id='rec-daily'", interFile)
		require.NoError(t, err)
		_, err = raw.Exec("UPDATE recordings SET merge_path=? WHERE id='rec-plain'", filepath.Join(dir, "plain.mp4"))
		require.NoError(t, err)
	}, daily, plain, noPath)

	// Dry run → file kept.
	withArgs([]string{"bin", "repair", "prune-intermediate-mp4", "--config", cfgPath}, func() { rc = runRepairPruneIntermediateMP4() })
	require.Equal(t, 0, rc)
	require.FileExists(t, interFile)

	// Execute → intermediate removed, merge_path cleared, plain/no-path skipped.
	withArgs([]string{"bin", "repair", "prune-intermediate-mp4", "--execute", "--config", cfgPath}, func() { rc = runRepairPruneIntermediateMP4() })
	require.Equal(t, 0, rc)
	require.NoFileExists(t, interFile)
	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	var mergePath string
	require.NoError(t, raw.QueryRow("SELECT COALESCE(merge_path,'') FROM recordings WHERE id='rec-daily'").Scan(&mergePath))
	require.Equal(t, "", mergePath, "merge_path pointer must be cleared after pruning")
	var plainPath string
	require.NoError(t, raw.QueryRow("SELECT COALESCE(merge_path,'') FROM recordings WHERE id='rec-plain'").Scan(&plainPath))
	require.NotEqual(t, "", plainPath, "non-daily_merged rows are preserved")
	require.NoError(t, raw.Close())
}

func TestRunRepairReclaimOrphanMergesFlows(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeRepairConfig(t, dir)

	// Referenced file (via file_path) + an unreferenced orphan in the nested tree.
	referenced := filepath.Join(dir, "cam1", "202601", "01", "referenced.mp4")
	orphan := filepath.Join(dir, "cam1", "202601", "02", "orphan.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(referenced), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(orphan), 0o755))
	require.NoError(t, os.WriteFile(referenced, []byte("ref"), 0o644))
	require.NoError(t, os.WriteFile(orphan, []byte("orphan"), 0o644))

	rec := repairRec("rec-ref", "cam1", "h264", model.MergeStatusPending, time.Now().UTC().Add(-time.Hour))
	rec.FilePath = referenced
	seedRepairDB(t, dir, nil, rec)

	var rc int
	withArgs([]string{"bin", "repair", "reclaim-orphan-merges", "--help"}, func() { rc = runRepairReclaimOrphanMerges() })
	require.Equal(t, 0, rc)

	// Dry run → both files intact.
	withArgs([]string{"bin", "repair", "reclaim-orphan-merges", "--camera", "cam1", "--config", cfgPath}, func() { rc = runRepairReclaimOrphanMerges() })
	require.Equal(t, 0, rc)
	require.FileExists(t, referenced)
	require.FileExists(t, orphan)

	// Execute → only the unreferenced orphan is removed.
	withArgs([]string{"bin", "repair", "reclaim-orphan-merges", "--camera", "cam1", "--execute", "--config", cfgPath}, func() { rc = runRepairReclaimOrphanMerges() })
	require.Equal(t, 0, rc)
	require.FileExists(t, referenced, "referenced merge output must never be reclaimed")
	require.NoFileExists(t, orphan)
}

func TestRunRepairNormalizeEndpointsFlows(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeRepairConfig(t, dir)

	var rc int
	withArgs([]string{"bin", "repair", "normalize-endpoints", "--help"}, func() { rc = runRepairNormalizeEndpoints() })
	require.Equal(t, 0, rc)

	withArgs([]string{"bin", "repair", "normalize-endpoints", "--config", filepath.Join(dir, "nope.yaml")}, func() { rc = runRepairNormalizeEndpoints() })
	require.Equal(t, 1, rc)

	// Seed a camera row with a non-canonical endpoint (default :80 + uppercase
	// host + trailing slash).
	dbPath := filepath.Join(dir, "mibee-nvr.db")
	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	require.NoError(t, raw.Close())
	db, err := storage.New(dbPath)
	require.NoError(t, err)
	require.NoError(t, db.Init(context.Background()))
	require.NoError(t, db.UpsertCamera(context.Background(), "cam1", "Cam One", "onvif", "h264",
		"http://192.168.1.50:80/onvif/device_service", "", "", "HTTP://192.168.1.50:80/onvif/device_service/", "", "", ""))
	require.NoError(t, db.Close())

	// Dry run → unchanged.
	withArgs([]string{"bin", "repair", "normalize-endpoints", "--config", cfgPath}, func() { rc = runRepairNormalizeEndpoints() })
	require.Equal(t, 0, rc)

	// Execute → endpoint canonicalized.
	withArgs([]string{"bin", "repair", "normalize-endpoints", "--execute", "--config", cfgPath}, func() { rc = runRepairNormalizeEndpoints() })
	require.Equal(t, 0, rc)

	db2, err := storage.New(dbPath)
	require.NoError(t, err)
	rows, err := db2.ListCameraEndpointsForRepair(context.Background())
	require.NoError(t, err)
	require.NoError(t, db2.Close())
	require.Len(t, rows, 1)
	require.Equal(t, "http://192.168.1.50/onvif/device_service", rows[0].Endpoint)
}

func TestRunRepairMergeStatusExecute(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeRepairConfig(t, dir)

	// A merged recording whose merge_path points nowhere → stale.
	rec := repairRec("rec-stale", "cam1", "h264", model.MergeStatusMerged, time.Now().UTC().Add(-time.Hour))
	dbPath := filepath.Join(dir, "mibee-nvr.db")
	seedRepairDB(t, dir, func(raw *sql.DB) {
		_, err := raw.Exec("UPDATE recordings SET merge_path=? WHERE id='rec-stale'", filepath.Join(dir, "missing.mp4"))
		require.NoError(t, err)
	}, rec)

	var rc int
	withArgs([]string{"bin", "repair", "merge-status", "--config", cfgPath}, func() { rc = runRepairMergeStatus() })
	require.Equal(t, 0, rc)
	found, status := recordingExists(t, dbPath, "rec-stale")
	require.True(t, found)
	require.Equal(t, model.MergeStatusMerged, status, "dry run must not reset")

	withArgs([]string{"bin", "repair", "merge-status", "--execute", "--config", cfgPath}, func() { rc = runRepairMergeStatus() })
	require.Equal(t, 0, rc)
	found, status = recordingExists(t, dbPath, "rec-stale")
	require.True(t, found)
	require.Equal(t, "", status, "execute must reset the stale merge status to empty (re-merge eligible)")
}

func TestRepairPureHelpers(t *testing.T) {
	require.Equal(t, "0 B", humanBytes(0))
	require.Equal(t, "1.00 KB", humanBytes(1024))
	require.Equal(t, "1.50 MB", humanBytes(1536*1024))
	require.Equal(t, "2.00 GB", humanBytes(2*1024*1024*1024))

	require.Equal(t, []string{"a", "b", "c"}, splitCSV("a,b,c"))
	require.Equal(t, []string{"incompatible"}, splitCSV("incompatible"))
	require.Empty(t, splitCSV(""))

	require.True(t, hasSuffix("foo.mp4", ".mp4"))
	require.False(t, hasSuffix("foo.mp4", ".avi"))

	// MJPEG duration estimation: frame_count>0 short-circuits (no dir read);
	// frame_count=0 counts .jpg files in the directory.
	dur, err := estimateMJpegDirDuration("no-such-dir", 25)
	require.NoError(t, err)
	require.InDelta(t, 25*mjpegNominalFrameInterval, dur, 1e-9)

	mjpegDir := t.TempDir()
	for i := range 4 {
		require.NoError(t, os.WriteFile(filepath.Join(mjpegDir, string(rune('a'+i))+".jpg"), []byte("x"), 0o644))
	}
	dur, err = estimateMJpegDirDuration(mjpegDir, 0)
	require.NoError(t, err)
	require.InDelta(t, 4*mjpegNominalFrameInterval, dur, 1e-9)

	empty := t.TempDir()
	_, err = estimateMJpegDirDuration(empty, 0)
	require.Error(t, err, "a directory without jpeg frames is an error")
	_, err = estimateMJpegDirDuration(filepath.Join(empty, "missing"), 0)
	require.Error(t, err)
}
