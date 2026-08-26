package frametrace

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureHandler collects records (with attrs preserved from WithAttrs so the
// trace logger's component=frame-trace is visible on captured records).
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

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &prefixedHandler{inner: h, attrs: attrs}
}
func (h *captureHandler) WithGroup(string) slog.Handler { return h }

func (h *captureHandler) all() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.records
}

// prefixedHandler forwards to the root capture, injecting stored attrs.
type prefixedHandler struct {
	inner *captureHandler
	attrs []slog.Attr
}

func (p *prefixedHandler) Enabled(_ context.Context, l slog.Level) bool { return true }
func (p *prefixedHandler) Handle(_ context.Context, r slog.Record) error {
	r.AddAttrs(p.attrs...)
	return p.inner.Handle(context.Background(), r)
}

func (p *prefixedHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &prefixedHandler{inner: p.inner, attrs: append(append([]slog.Attr{}, p.attrs...), attrs...)}
}
func (p *prefixedHandler) WithGroup(string) slog.Handler { return p }

func withCapture(t *testing.T) *captureHandler {
	t.Helper()
	h := &captureHandler{}
	capture := slog.New(h)
	old := slog.Default()
	slog.SetDefault(capture) // unsampled path (plain slog.Debug/Warn)
	SetLogger(capture)       // sampled path (dedicated trace logger)
	t.Cleanup(func() {
		slog.SetDefault(old)
		SetLogger(old)
	})
	return h
}

func hasAttr(t *testing.T, r slog.Record, key, val string) bool {
	t.Helper()
	found := false
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key && a.Value.String() == val {
			found = true
		}
		return true
	})
	return found
}

func TestSamplingWindow(t *testing.T) {
	assert.False(t, Active("cam1"), "no window before Enable")

	until := Enable("cam1", 2*time.Second)
	assert.True(t, Active("cam1"))
	assert.InDelta(t, time.Now().Add(2*time.Second).Unix(), until.Unix(), 1.0)
	assert.False(t, Active("cam2"), "sampling is per-camera")

	Disable("cam1")
	assert.False(t, Active("cam1"), "Disable ends the window")
}

func TestEnableClampsAndDefaults(t *testing.T) {
	until := Enable("cam1", time.Hour)
	assert.LessOrEqual(t, time.Until(until), MaxDuration, "duration clamped to MaxDuration")

	until2 := Enable("cam1", 0) // zero → default
	assert.True(t, Active("cam1"))
	assert.InDelta(t, time.Now().Add(DefaultDuration).Unix(), until2.Unix(), 1.0)
	Disable("cam1")
}

func TestLogEscalation(t *testing.T) {
	h := withCapture(t)

	Log("cam-off", "stage", "ingest")
	recs := h.all()
	require.NotEmpty(t, recs)
	last := recs[len(recs)-1]
	assert.Equal(t, slog.LevelDebug, last.Level, "unsampled camera logs at Debug")
	assert.Equal(t, "frame_trace", last.Message)

	Enable("cam-on", 5*time.Second)
	t.Cleanup(func() { Disable("cam-on") })
	Log("cam-on", "stage", "ingest")
	LogDrop("cam-on", "stage", "ws_drop")
	recs = h.all()
	require.GreaterOrEqual(t, len(recs), 2)
	var infoCount int
	for _, r := range recs[len(recs)-2:] {
		if r.Level == slog.LevelInfo {
			infoCount++
			assert.True(t, hasAttr(t, r, "component", "frame-trace"),
				"sampled breadcrumbs carry component=frame-trace")
			assert.True(t, hasAttr(t, r, "stage", "ingest") || hasAttr(t, r, "stage", "ws_drop"))
		}
	}
	assert.Equal(t, 2, infoCount, "both Log and LogDrop escalate to Info while sampled")
}

func TestLogDropStaysWarnUnsampled(t *testing.T) {
	h := withCapture(t)

	LogDrop("cam-off", "stage", "ws_drop")
	recs := h.all()
	require.NotEmpty(t, recs)
	r := recs[len(recs)-1]
	assert.Equal(t, slog.LevelWarn, r.Level, "drops stay Warn when unsampled")
	assert.False(t, hasAttr(t, r, "component", "frame-trace"))
}

func TestTraceIDConvention(t *testing.T) {
	h := withCapture(t)
	Enable("cam-x", 5*time.Second)
	t.Cleanup(func() { Disable("cam-x") })

	Log("cam-x", "trace_id", "cam-x-123", "stage", "ingest")
	recs := h.all()
	require.NotEmpty(t, recs)
	assert.True(t, strings.Contains(recs[len(recs)-1].Message, "frame_trace"))
}
