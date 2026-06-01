package model

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// captureHandler captures slog records for assertion.
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.records = append(h.records, r)
	h.mu.Unlock()
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler          { return h }
func (h *captureHandler) WithGroup(string) slog.Handler             { return h }

func (h *captureHandler) all() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.records
}

func attr(r slog.Record, key string) slog.Value {
	var val slog.Value
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			val = a.Value
			return false
		}
		return true
	})
	return val
}

func attrStr(r slog.Record, key string) string {
	v := attr(r, key)
	s := v.String()
	return s
}

func attrBool(r slog.Record, key string) bool {
	v := attr(r, key)
	b := v.Bool()
	return b
}

func attrInt(r slog.Record, key string) int64 {
	v := attr(r, key)
	i := v.Int64()
	return i
}

func newCaptureHub(t *testing.T, camID string) (*StreamHub, *captureHandler) {
	t.Helper()
	h := NewStreamHub()
	h.SetCameraID(camID)
	h.consumerBufferSize = 5
	ch := &captureHandler{}
	slog.SetDefault(slog.New(ch))
	return h, ch
}

// TestStreamHub_BroadcastLogsFrameTrace verifies that Broadcast produces
// frame_trace slog records with the expected fields.
func TestStreamHub_BroadcastLogsFrameTrace(t *testing.T) {
	t.Helper()
	hub, ch := newCaptureHub(t, "test-cam")
	defer slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	blockCh := make(chan struct{})
	var received atomic.Int32
	err := hub.Subscribe("sub", func(pts int64, au [][]byte) {
		received.Add(1)
			<-blockCh
	})
	require.NoError(t, err)

	// 1) Broadcast a non-IDR frame
	hub.Broadcast(100, [][]byte{{0x01}}, false)

	// 2) Broadcast an IDR frame
	hub.Broadcast(200, [][]byte{{0x05}}, true)

	// 3) Fill buffer to force a drop, then broadcast non-IDR (should trigger streamhub_drop)
	for i := 0; i < hub.consumerBufferSize+3; i++ {
		hub.Broadcast(int64(300+i), [][]byte{{byte(i)}}, false)
	}

	close(blockCh)
	hub.Unsubscribe("sub")

	records := ch.all()
	require.NotEmpty(t, records, "expected frame_trace log records")

	// Find streamhub_in records (one per Broadcast call: 1 non-IDR + 1 IDR + 8 in loop = 10)
	var inRecords []slog.Record
	for _, r := range records {
		if attrStr(r, "stage") == "streamhub_in" {
			inRecords = append(inRecords, r)
		}
	}
	require.Equal(t, 10, len(inRecords), "expected 10 streamhub_in records (one per Broadcast)")

	// Verify non-IDR in record 0
	require.Equal(t, "no-trace", attrStr(inRecords[0], "trace_id"), "non-IDR should have no-trace")
	require.Equal(t, "test-cam", attrStr(inRecords[0], "camera_id"))
	require.Equal(t, "streamhub_in", attrStr(inRecords[0], "stage"))
	require.False(t, attrBool(inRecords[0], "is_idr"))

	// Verify IDR in record 1
	require.Equal(t, "test-cam-200", attrStr(inRecords[1], "trace_id"), "IDR should have cameraID-pts trace_id")
	require.True(t, attrBool(inRecords[1], "is_idr"))

	// Verify non-IDR in record 2
	require.Equal(t, "no-trace", attrStr(inRecords[2], "trace_id"))

	// Find streamhub_drop records (from buffer-full drops)
	var dropRecords []slog.Record
	for _, r := range records {
		if attrStr(r, "stage") == "streamhub_drop" {
			dropRecords = append(dropRecords, r)
		}
	}
	require.NotEmpty(t, dropRecords, "expected streamhub_drop records from buffer overflow")

	// Verify drop record fields
	dr := dropRecords[0]
	require.Equal(t, "no-trace", attrStr(dr, "trace_id"))
	require.Equal(t, "test-cam", attrStr(dr, "camera_id"))
	require.Equal(t, "streamhub_drop", attrStr(dr, "stage"))
	require.Equal(t, "sub", attrStr(dr, "consumer"))
	require.Greater(t, attrInt(dr, "queue_depth"), int64(0), "queue_depth should be positive")
}
