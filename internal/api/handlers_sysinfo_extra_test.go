package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/stretchr/testify/require"
)

// TestParseProcStatIowait pins the extended /proc/stat parser: iowait is the
// 5th counter (index 5 in Fields after the "cpu" label). The 2026-09-11 M5
// saturation was 73% iowait with LOW cpu busy — invisible in the dashboard
// because /api/stats/system never reported iowait at all.
func TestParseProcStatIowait(t *testing.T) {
	total, idle, iowait, err := parseProcStat([]byte(
		"cpu  100 20 30 400 50 5 10 0 0 0\n" +
			"cpu0 50 10 15 200 25 2 5 0 0 0\n" +
			"intr 12345\n"))
	require.NoError(t, err)
	require.Equal(t, uint64(615), total, "sum of all cpu counters")
	require.Equal(t, uint64(400), idle)
	require.Equal(t, uint64(50), iowait)
}

func TestParseProcStatMalformed(t *testing.T) {
	_, _, _, err := parseProcStat([]byte("cpu  1 2 3\n"))
	require.Error(t, err)
	_, _, _, err = parseProcStat(nil)
	require.Error(t, err)
}

// TestParseLoadAvg pins the /proc/loadavg parser ("0.74 2.39 4.92 3/421 12345").
func TestParseLoadAvg(t *testing.T) {
	one, five, fifteen, err := parseLoadAvg([]byte("0.74 2.39 4.92 3/421 12345\n"))
	require.NoError(t, err)
	require.InDelta(t, 0.74, one, 0.0001)
	require.InDelta(t, 2.39, five, 0.0001)
	require.InDelta(t, 4.92, fifteen, 0.0001)

	_, _, _, err = parseLoadAvg([]byte("garbage"))
	require.Error(t, err)
}

// TestHandleSystemStatsExtendedFields asserts the endpoint now carries the
// saturation signals: cpu.iowait (cumulative jiffies for client-side deltas),
// load{one,five,fifteen}, and a disk block with the recording-root watermark.
// On non-Linux dev machines the /proc reads return zeros — the fields must
// still be present and well-formed.
func TestHandleSystemStatsExtendedFields(t *testing.T) {
	h := &Handler{config: &config.Config{}}
	h.config.ApplyDefaults()

	req := httptest.NewRequest(http.MethodGet, "/api/stats/system", nil)
	rr := httptest.NewRecorder()
	h.handleSystemStats(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var body struct {
		CPU struct {
			Total  uint64 `json:"total"`
			Idle   uint64 `json:"idle"`
			Iowait uint64 `json:"iowait"`
		} `json:"cpu"`
		Load *struct {
			One      float64 `json:"one"`
			Five     float64 `json:"five"`
			Fifteen  float64 `json:"fifteen"`
		} `json:"load"`
		Disk *struct {
			Path         string  `json:"path"`
			TotalBytes   uint64  `json:"total_bytes"`
			FreeBytes    uint64  `json:"free_bytes"`
			UsedPct      float64 `json:"used_pct"`
			WatermarkPct float64 `json:"watermark_pct"`
			Status       string  `json:"status"`
		} `json:"disk"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	require.NotEmpty(t, body.Disk.Path, "disk block must report the recording root")
	require.Greater(t, body.Disk.WatermarkPct, 0.0)
	require.Contains(t, []string{"ok", "high", "unknown"}, body.Disk.Status)
}
