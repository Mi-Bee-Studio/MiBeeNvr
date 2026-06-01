// Package remotelog implements an slog.Handler that batches and ships logs
// to VictoriaLogs (or any JSON-lines ingestion endpoint).
package remotelog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
)

const (
	defaultBufferSize    = 200
	defaultFlushInterval = 5 * time.Second
	defaultHTTPTimeout   = 5 * time.Second
	maxPendingEntries    = 10000
)

// bufferedEntry holds a parsed log record ready for NDJSON serialization.
type bufferedEntry struct {
	Time    time.Time
	Level   string
	Message string
	Fields  map[string]any
}

// sharedBuffer holds the mutex-protected buffer and flush channels.
// Cloned handlers (WithAttrs/WithGroup) share the same *sharedBuffer.
type sharedBuffer struct {
	mu       sync.Mutex
	buf      []bufferedEntry
	flushCh  chan struct{}
	stopOnce sync.Once
	stopCh   chan struct{}
}

// Handler implements slog.Handler. It batches log records and periodically
// flushes them to a remote endpoint via HTTP POST (NDJSON).
// WithAttrs/WithGroup return lightweight wrappers sharing the same buffer.
type Handler struct {
	endpoint string
	format   string
	level    slog.Level
	client   *http.Client
	metrics  *metrics.Metrics
	shared   *sharedBuffer

	groupPrefix string
	attrs       []slog.Attr
}

func New(endpoint, format string, level slog.Level, m *metrics.Metrics) *Handler {
	s := &sharedBuffer{
		buf:     make([]bufferedEntry, 0, defaultBufferSize),
		flushCh: make(chan struct{}, 1),
		stopCh:  make(chan struct{}),
	}
	h := &Handler{
		endpoint: endpoint,
		format:   format,
		level:    level,
		client:   &http.Client{Timeout: defaultHTTPTimeout},
		metrics:  m,
		shared:   s,
	}
	go h.flushLoop()
	return h
}

// Enabled reports whether the handler should handle records at the given level.
func (h *Handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

// Handle appends the record to the shared buffer. Non-blocking.
func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	entry := h.recordToEntry(r)

	h.shared.mu.Lock()
	h.shared.buf = append(h.shared.buf, entry)
	shouldFlush := len(h.shared.buf) >= defaultBufferSize
	h.shared.mu.Unlock()

	if shouldFlush {
		h.signalFlush()
	}
	return nil
}

// WithAttrs returns a new handler sharing the same buffer with extra attributes.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(merged, h.attrs)
	copy(merged[len(h.attrs):], attrs)
	return &Handler{
		endpoint:    h.endpoint,
		format:      h.format,
		level:       h.level,
		client:      h.client,
		metrics:     h.metrics,
		shared:      h.shared,
		groupPrefix: h.groupPrefix,
		attrs:       merged,
	}
}

// WithGroup returns a new handler sharing the same buffer with a group prefix.
func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &Handler{
		endpoint:    h.endpoint,
		format:      h.format,
		level:       h.level,
		client:      h.client,
		metrics:     h.metrics,
		shared:      h.shared,
		groupPrefix: h.groupPrefix + name + ".",
		attrs:       h.attrs,
	}
}

// Close flushes remaining buffered logs and stops the background goroutine.
// Safe to call multiple times.
func (h *Handler) Close() {
	h.shared.stopOnce.Do(func() { close(h.shared.stopCh) })
	h.signalFlush()
}

// --- internal ---

func (h *Handler) recordToEntry(r slog.Record) bufferedEntry {
	fields := make(map[string]any, 4+r.NumAttrs()+len(h.attrs))
	prefix := h.groupPrefix
	r.Attrs(func(a slog.Attr) bool {
		flattenAttr(prefix+a.Key, a.Value, fields)
		return true
	})
	for _, a := range h.attrs {
		flattenAttr(prefix+a.Key, a.Value, fields)
	}
	return bufferedEntry{Time: r.Time, Level: r.Level.String(), Message: r.Message, Fields: fields}
}

func flattenAttr(key string, v slog.Value, m map[string]any) {
	switch v.Kind() {
	case slog.KindBool:
		m[key] = v.Bool()
	case slog.KindFloat64:
		m[key] = v.Float64()
	case slog.KindInt64:
		m[key] = v.Int64()
	case slog.KindUint64:
		m[key] = v.Uint64()
	case slog.KindString:
		m[key] = v.String()
	case slog.KindDuration:
		m[key] = v.Duration().String()
	case slog.KindTime:
		m[key] = v.Time().UTC().Format(time.RFC3339Nano)
	case slog.KindGroup:
		for _, ga := range v.Group() {
			flattenAttr(key+"."+ga.Key, ga.Value, m)
		}
	case slog.KindAny:
		m[key] = v.Any()
	}
}

func (h *Handler) signalFlush() {
	select {
	case h.shared.flushCh <- struct{}{}:
	default:
	}
}

func (h *Handler) flushLoop() {
	ticker := time.NewTicker(defaultFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			h.flush()
		case <-h.shared.flushCh:
			h.flush()
		case <-h.shared.stopCh:
			h.flush()
			return
		}
	}
}

func (h *Handler) flush() {
	h.shared.mu.Lock()
	if len(h.shared.buf) == 0 {
		h.shared.mu.Unlock()
		return
	}
	batch := h.shared.buf
	n := len(batch)
	if n > maxPendingEntries {
		h.shared.mu.Unlock()
		return
	}
	h.shared.buf = make([]bufferedEntry, 0, defaultBufferSize)
	h.shared.mu.Unlock()

	if h.metrics != nil {
		h.metrics.RemoteLogBatchSize.Observe(float64(n))
	}
	if err := h.send(batch); err != nil {
		if h.metrics != nil {
			h.metrics.RemoteLogDroppedTotal.Inc()
		}
		return
	}
	if h.metrics != nil {
		h.metrics.RemoteLogSentTotal.Inc()
	}
}

func (h *Handler) send(batch []bufferedEntry) error {
	buf := new(bytes.Buffer)
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	for _, e := range batch {
		obj := make(map[string]any, len(e.Fields)+4)
		obj["_time"] = e.Time.UTC().Format(time.RFC3339Nano)
		obj["_msg"] = e.Message
		obj["level"] = e.Level
		for k, v := range e.Fields {
			obj[k] = v
		}
		// Extract key=value pairs embedded in message text (e.g. "camera_id=xxx")
		// so VictoriaLogs can index them as separate fields for filtering.
		extractFields(e.Message, obj)
		if err := enc.Encode(obj); err != nil {
			continue
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.endpoint, buf)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("remote log: HTTP %d", resp.StatusCode)
	}
	return nil
}

// MultiHandler returns a slog.Handler that fans out to all given handlers.
// If a handler is nil, it is skipped. If all are nil, returns nil.
func MultiHandler(handlers ...slog.Handler) slog.Handler {
	var valid []slog.Handler
	for _, h := range handlers {
		if h != nil {
			valid = append(valid, h)
		}
	}
	if len(valid) == 0 {
		return nil
	}
	return &multiHandler{handlers: valid}
}

type multiHandler struct {
	handlers []slog.Handler
}

func (m *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (m *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range m.handlers {
		if h.Enabled(ctx, r.Level) {
			_ = h.Handle(ctx, r) // never block on one handler failing
		}
	}
	return nil
}

func (m *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	var clones []slog.Handler
	for _, h := range m.handlers {
		clones = append(clones, h.WithAttrs(attrs))
	}
	return MultiHandler(clones...)
}

func (m *multiHandler) WithGroup(name string) slog.Handler {
	var clones []slog.Handler
	for _, h := range m.handlers {
		clones = append(clones, h.WithGroup(name))
	}
	return MultiHandler(clones...)
}

// kvPat matches key=value pairs in log message text.
// Handles: camera_id=xxx  error="quoted value"  backoff=1m30s  attempt=42
// Skips: _msg= (VictoriaLogs internal), keys starting with digit.
var kvPat = regexp.MustCompile(`(?:^|\s)([a-zA-Z_][a-zA-Z0-9_]*)=("[^"]*"|\S+)`)

// extractFields parses key=value pairs from msg and adds them to obj
// only if the key is not already present (explicit slog attrs take precedence).
func extractFields(msg string, obj map[string]any) {
	matches := kvPat.FindAllStringSubmatch(msg, -1)
	for _, m := range matches {
		key := m[1]
		val := m[2]
		// Strip surrounding quotes from quoted values
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}
		// Don't overwrite explicit slog attrs
		if _, exists := obj[key]; !exists {
			obj[key] = val
		}
	}
}
