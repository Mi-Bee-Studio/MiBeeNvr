package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/stretchr/testify/require"
)

// A recording_schedule sent with POST /api/cameras must be persisted (it used
// to be dropped, so the schedule only stuck after a follow-up PUT).
func TestHandleCreateCamera_PersistsRecordingSchedule(t *testing.T) {
	h := setupDedupHandler(t)

	body := `{"name":"Sched","protocol":"rtsp","url":"rtsp://192.0.2.9:554/stream","enabled":false,
		"recording_schedule":{"time_ranges":[{"start":"07:00","end":"23:00"}],"days_of_week":[0,6]}}`
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusCreated, rr.Code, "body: %s", rr.Body.String())

	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	cam := h.camMgr.GetCameraConfig(created.ID)
	require.NotNil(t, cam)
	require.Equal(t, &config.ScheduleConfig{
		TimeRanges: []config.TimeRange{{Start: "07:00", End: "23:00"}},
		DaysOfWeek: []int{0, 6},
	}, cam.RecordingSchedule)
}

func TestHandleCreateCamera_InvalidRecordingSchedule(t *testing.T) {
	h := setupDedupHandler(t)

	body := `{"name":"Sched","protocol":"rtsp","url":"rtsp://192.0.2.9:554/stream","enabled":false,
		"recording_schedule":{"time_ranges":[{"start":"23:00","end":"07:00"}]}}`
	rr := doRequest(t, h.Routes(), "POST", "/api/cameras/", bytes.NewReader([]byte(body)), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code, "body: %s", rr.Body.String())
	require.Contains(t, rr.Body.String(), "recording_schedule.time_ranges[0].end")
}
