package model

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseLegacyProtocol(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input       string
		wantProto   string
		wantEnc     string
		wantErr     bool
	}{
		{"rtsp_h264", "rtsp", "h264", false},
		{"rtsp_h265", "rtsp", "h265", false},
		{"rtsp_mjpeg", "rtsp", "mjpeg", false},
		{"http_jpeg", "http", "jpeg", false},
		{"onvif", "onvif", "", false},
		{"unknown", "", "", true},
		{"", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			proto, enc, err := ParseLegacyProtocol(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantProto, proto)
			require.Equal(t, tt.wantEnc, enc)
		})
	}
}

func TestValidateProtocolEncoding(t *testing.T) {
	t.Parallel()
	validCombos := []struct{ proto, enc string }{
		{"rtsp", "h264"},
		{"rtsp", "h265"},
		{"rtsp", "mjpeg"},
		{"http", "jpeg"},
		{"onvif", "h264"},
		{"onvif", "h265"},
	}
	for _, c := range validCombos {
		t.Run("valid_"+c.proto+"_"+c.enc, func(t *testing.T) {
			require.NoError(t, ValidateProtocolEncoding(c.proto, c.enc))
		})
	}

	invalidCombos := []struct{ proto, enc string }{
		{"http", "h264"},
		{"rtsp", "jpeg"},
		{"onvif", "jpeg"},
		{"http", "h265"},
		{"", ""},
		{"foo", "bar"},
	}
	for _, c := range invalidCombos {
		t.Run("invalid_"+c.proto+"_"+c.enc, func(t *testing.T) {
			require.Error(t, ValidateProtocolEncoding(c.proto, c.enc))
		})
	}
}

func TestHealthStatusConstants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value HealthStatus
		want  string
	}{
		{"healthy", HealthStatusHealthy, "healthy"},
		{"warning", HealthStatusWarning, "warning"},
		{"error", HealthStatusError, "error"},
		{"unknown", HealthStatusUnknown, "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, string(tt.value))
		})
	}
}

func TestHealthEventTypeConstants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value HealthEventType
		want  string
	}{
		{"connection_lost", HealthEventConnectionLost, "connection_lost"},
		{"connection_restored", HealthEventConnectionRestored, "connection_restored"},
		{"stream_anomaly", HealthEventStreamAnomaly, "stream_anomaly"},
		{"freeze_detected", HealthEventFreezeDetected, "freeze_detected"},
		{"freeze_recovered", HealthEventFreezeRecovered, "freeze_recovered"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, string(tt.value))
		})
	}
}

func TestHealthEventJSONTags(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	event := HealthEvent{
		ID:        1,
		CameraID:  "cam-01",
		EventType: string(HealthEventConnectionLost),
		Status:    string(HealthStatusError),
		Message:   "connection timeout",
		Metadata:  `{"reason":"timeout"}`,
		CreatedAt: now,
	}
	data, err := json.Marshal(event)
	require.NoError(t, err)

	got := string(data)
	require.Contains(t, got, `"id":1`)
	require.Contains(t, got, `"camera_id":"cam-01"`)
	require.Contains(t, got, `"event_type":"connection_lost"`)
	require.Contains(t, got, `"status":"error"`)
	require.Contains(t, got, `"message":"connection timeout"`)
	require.Contains(t, got, `"metadata"`)
	require.Contains(t, got, `"created_at"`)
}

func TestCameraHealthStruct(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	ch := CameraHealth{
		CameraID:      "cam-01",
		LatestStatus:  string(HealthStatusHealthy),
		LatestEvent:   string(HealthEventConnectionRestored),
		LatestMessage: "reconnected",
		LastEventAt:   now,
	}

	data, err := json.Marshal(ch)
	require.NoError(t, err)
	got := string(data)
	require.Contains(t, got, `"camera_id":"cam-01"`)
	require.Contains(t, got, `"latest_status":"healthy"`)
	require.Contains(t, got, `"latest_event":"connection_restored"`)
	require.Contains(t, got, `"latest_message":"reconnected"`)
	require.Contains(t, got, `"last_event_at"`)
}
