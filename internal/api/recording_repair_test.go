package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
)

// buildRepairFixtureMP4 writes a real, small H.264 MP4 (SPS carries the
// visual dims) and returns its path — the same muxer the recorder merge path
// uses, so the header layout matches production output.
func buildRepairFixtureMP4(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	// A real 1920-wide SPS (same bytes as the merge package's repair tests):
	// RepairZeroDimensions restores dims from the SPS, so the fixture must
	// carry a parseable one.
	sps := []byte{0x67, 0x42, 0xc0, 0x28, 0xf4, 0x03, 0xc0, 0x11, 0x2f, 0x28}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	idr := []byte{0x65, 0x88, 0x80, 0x40}
	pNAL := []byte{0x41, 0x10, 0x00, 0x0c}
	m := muxer.NewMP4Muxer(path)
	trackID, err := m.AddH264Track(sps, pps)
	require.NoError(t, err)
	require.NoError(t, m.WriteSample(trackID, idr, 0, time.Second))
	require.NoError(t, m.WriteSample(trackID, pNAL, time.Second, time.Second))
	require.NoError(t, m.Close())
	return path
}

// zeroVisualDims replicates the #853-era bad bucket in place: both the tkhd
// 16.16 fixed-point dims and the stsd sample-entry u16 dims are zeroed via
// the offsets the probe reports.
func zeroVisualDims(t *testing.T, path string) {
	t.Helper()
	vt, err := merge.ProbeVideoTrack(path)
	require.NoError(t, err)
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	require.NoError(t, err)
	defer f.Close()
	require.Greater(t, vt.TkhdDimsOffset, int64(0))
	_, err = f.WriteAt([]byte{0, 0, 0, 0, 0, 0, 0, 0}, vt.TkhdDimsOffset)
	require.NoError(t, err)
	require.Greater(t, vt.SampleEntryDimsOffset, int64(0))
	_, err = f.WriteAt([]byte{0, 0, 0, 0}, vt.SampleEntryDimsOffset)
	require.NoError(t, err)
}

func decodeRepairBody(t *testing.T, rr *httptest.ResponseRecorder) repairResponse {
	t.Helper()
	var resp repairResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	return resp
}

// TestRepairRecording_DimsAndDuration is the end-to-end repair: a recording
// row with duration=0 whose MP4 carries zeroed stsd/tkhd dims gets both fixed
// in one request, the file becomes probeable again, and the row's duration is
// persisted.
func TestRepairRecording_DimsAndDuration(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	path := buildRepairFixtureMP4(t, t.TempDir(), "broken.mp4")
	zeroVisualDims(t, path)

	rec := makeRecording("repair-1", "cam-1", "h264", time.Now().UTC().Add(-time.Minute), true)
	rec.FilePath = path
	rec.Duration = 0
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "POST", "/api/recordings/repair-1/repair", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	resp := decodeRepairBody(t, rr)
	require.Equal(t, "repaired", resp.Status)
	require.True(t, resp.RetryPlayback)
	require.Len(t, filterHasPrefix(resp.Actions, "duration_updated="), 1)
	require.Len(t, filterHasPrefix(resp.Actions, "stsd_tkhd_dims="), 1)

	// The file's visual dims are restored from its own SPS.
	vt, err := merge.ProbeVideoTrack(path)
	require.NoError(t, err)
	require.Greater(t, vt.Width, uint16(0))
	require.Greater(t, vt.SampleEntryWidth, uint16(0))

	// The row's duration is persisted.
	fixed, err := db.GetRecording(context.Background(), "repair-1")
	require.NoError(t, err)
	require.InDelta(t, 2.0, fixed.Duration, 0.01)
}

// TestRepairRecording_NoProblem: a healthy row + file reports nothing to fix
// and suggests a retry (the play error was transient/codec, not data).
func TestRepairRecording_NoProblem(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	path := buildRepairFixtureMP4(t, t.TempDir(), "healthy.mp4")
	rec := makeRecording("repair-ok", "cam-1", "h264", time.Now().UTC().Add(-time.Minute), true)
	rec.FilePath = path
	rec.Duration = 2
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "POST", "/api/recordings/repair-ok/repair", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	resp := decodeRepairBody(t, rr)
	require.Equal(t, "no_problem_found", resp.Status)
	require.Empty(t, resp.Actions)
	require.True(t, resp.RetryPlayback)
}

// TestRepairRecording_StaleID: a fold-consumed ID resolves to the merged row
// (lineage/covering fallback) — repair reports where the content lives.
func TestRepairRecording_StaleID(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	base := time.Now().UTC().Truncate(time.Second)
	srcID := strconv.FormatInt(base.Add(-30*time.Second).UnixNano(), 10)
	src := makeRecording(srcID, "cam-1", "h264", base.Add(-40*time.Second), false)
	seedRecording(t, db, src)
	merged := makeRecording("merged-r", "cam-1", "h264", base.Add(-40*time.Second), true)
	merged.EndedAt = base.Add(20 * time.Second)
	require.NoError(t, db.MergeAndReplaceRecordings(context.Background(), merged, []string{srcID}))

	rr := doRequest(t, h.Routes(), "POST", "/api/recordings/"+srcID+"/repair", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	resp := decodeRepairBody(t, rr)
	require.Equal(t, "resolved_fallback", resp.Status)
	require.Equal(t, "merged-r", resp.TargetID)
	require.True(t, resp.RetryPlayback)
}

// TestRepairRecording_DeadTmpRow: a .tmp-pathed row whose file vanished (the
// #912 residue class) is swept like the startup orphan sweep; with no
// covering row the honest answer is file_missing.
func TestRepairRecording_DeadTmpRow(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := TestHandler(db, store)

	rec := makeRecording("repair-tmp", "cam-1", "h264", time.Now().UTC().Add(-2*time.Hour), true)
	rec.FilePath = filepath.Join(t.TempDir(), "vanished.tmp")
	seedRecording(t, db, rec)

	rr := doRequest(t, h.Routes(), "POST", "/api/recordings/repair-tmp/repair", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	resp := decodeRepairBody(t, rr)
	require.Equal(t, "file_missing", resp.Status)
	require.Contains(t, resp.Actions, "dead_tmp_row_swept")
	require.False(t, resp.RetryPlayback)

	gone, err := db.GetRecording(context.Background(), "repair-tmp")
	require.NoError(t, err)
	require.Nil(t, gone, "the dead .tmp row must be swept")
}

func filterHasPrefix(list []string, prefix string) []string {
	var out []string
	for _, s := range list {
		if strings.HasPrefix(s, prefix) {
			out = append(out, s)
		}
	}
	return out
}
