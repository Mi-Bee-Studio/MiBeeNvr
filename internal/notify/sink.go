package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"
)

// Defaults for a webhook sink. All overridable via SinkOptions; zero values
// fall back to these (hand-edited configs may carry them).
const (
	defaultSendTimeout = 5 * time.Second
	defaultBurstPerMin = 30
	defaultQueueSize   = 256
)

// defaultRetryDelays is the bounded retry schedule for transient failures
// (connection errors, timeouts, 5xx, 429). Three retries spanning ~2.5 min:
// enough to ride out a receiver restart, short enough that a dead receiver
// does not pin the worker for long.
var defaultRetryDelays = []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute}

// SinkOptions configures one webhook receiver.
type SinkOptions struct {
	// Name labels the sink in logs and the status endpoint.
	Name string
	// URL is the receiver's ingest base URL; the token is appended as the
	// final path segment (receivers commonly identify the source by a path
	// token). Example: "http://host:9080/api/ingest/webhook".
	URL string
	// Token is the path credential. Kept separate from URL so it can be
	// encrypted at rest (ENC:) like other secrets.
	Token string
	// Events filters by alert type; empty = all types.
	Events []string
	// SeverityMin drops alerts below this severity ("info" = no filter).
	// default "info".
	SeverityMin string
	// TimeoutS is the per-request timeout. default 5.
	TimeoutS int
	// BurstPerMin caps outbound alert posts per minute. default 30.
	BurstPerMin int
	// QueueSize is the bounded queue depth. default 256.
	QueueSize int
	// RetryDelays overrides the retry schedule (tests only).
	RetryDelays []time.Duration
}

func (o SinkOptions) timeout() time.Duration {
	if o.TimeoutS <= 0 {
		return defaultSendTimeout
	}
	return time.Duration(o.TimeoutS) * time.Second
}

func (o SinkOptions) burstPerMin() int {
	if o.BurstPerMin <= 0 {
		return defaultBurstPerMin
	}
	return o.BurstPerMin
}

func (o SinkOptions) queueSize() int {
	if o.QueueSize <= 0 {
		return defaultQueueSize
	}
	return o.QueueSize
}

func (o SinkOptions) retryDelays() []time.Duration {
	if len(o.RetryDelays) > 0 {
		return o.RetryDelays
	}
	return defaultRetryDelays
}

func (o SinkOptions) severityMin() string {
	if !IsValidSeverity(o.SeverityMin) {
		return SeverityInfo
	}
	return o.SeverityMin
}

// SinkStats is the counters snapshot exposed via the status endpoint.
type SinkStats struct {
	Name          string `json:"name"`
	URL           string `json:"url"`
	SentTotal     uint64 `json:"sent_total"`
	FailedTotal   uint64 `json:"failed_total"`
	RetriedTotal  uint64 `json:"retried_total"`
	DroppedQueue  uint64 `json:"dropped_queue"`
	DroppedRate   uint64 `json:"dropped_rate"`
	DroppedFilter uint64 `json:"dropped_filter"`
	QueueDepth    int    `json:"queue_depth"`
}

// WebhookSink delivers alerts to one HTTP receiver. Each sink owns a bounded
// queue and a single delivery worker: a slow or dead receiver can only ever
// stall its own queue, and overflow drops the oldest alert (newest state
// matters more for notifications).
type WebhookSink struct {
	opts   SinkOptions
	events map[string]bool

	client *http.Client

	queue    chan Alert
	endpoint string
	log      *slog.Logger

	// fixed-window rate limiter state (worker goroutine only)
	windowStart time.Time
	windowCount int

	sent     atomic.Uint64
	failed   atomic.Uint64
	retried  atomic.Uint64
	dropQ    atomic.Uint64
	dropRate atomic.Uint64
	dropFilt atomic.Uint64

	cancel    context.CancelFunc
	done      chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
}

// NewWebhookSink validates the options and builds the sink. Errors here are
// config-load-time problems (bad URL, unknown event type) — callers should
// surface them, not limp along.
func NewWebhookSink(opts SinkOptions) (*WebhookSink, error) {
	u, err := url.Parse(opts.URL)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("notifications sink %q: url must be an absolute http(s) URL, got %q", opts.Name, opts.URL)
	}
	if strings.TrimSpace(opts.Token) == "" {
		return nil, fmt.Errorf("notifications sink %q: token is empty", opts.Name)
	}
	events := map[string]bool{}
	for _, t := range opts.Events {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if !IsValidType(t) {
			return nil, fmt.Errorf("notifications sink %q: unknown event type %q (known: %s)", opts.Name, t, strings.Join(KnownTypes(), ", "))
		}
		events[t] = true
	}
	endpoint := strings.TrimRight(u.String(), "/") + "/" + url.PathEscape(opts.Token)
	return &WebhookSink{
		opts:     opts,
		events:   events,
		client:   &http.Client{Timeout: opts.timeout()},
		queue:    make(chan Alert, opts.queueSize()),
		endpoint: endpoint,
		log:      slogx.Component("notify").With("sink", displaySinkName(opts)),
	}, nil
}

// displaySinkName prefers the configured name; the masked URL is a fallback
// so a nameless sink is still identifiable in logs without leaking the token.
func displaySinkName(o SinkOptions) string {
	if o.Name != "" {
		return o.Name
	}
	return strings.TrimRight(o.URL, "/") + "/***"
}

// Stats snapshots the counters (safe from any goroutine).
func (s *WebhookSink) Stats() SinkStats {
	return SinkStats{
		Name:          s.opts.Name,
		URL:           strings.TrimRight(s.opts.URL, "/") + "/***",
		SentTotal:     s.sent.Load(),
		FailedTotal:   s.failed.Load(),
		RetriedTotal:  s.retried.Load(),
		DroppedQueue:  s.dropQ.Load(),
		DroppedRate:   s.dropRate.Load(),
		DroppedFilter: s.dropFilt.Load(),
		QueueDepth:    len(s.queue),
	}
}

// Enqueue offers an alert to the queue. Never blocks: on a full queue the
// oldest queued alert is dropped to make room. The bypassFilter flag is used
// by the test-alert path so a receiver setup can be verified even while the
// sink filters would exclude the test type.
func (s *WebhookSink) Enqueue(a Alert, bypassFilter bool) {
	if !bypassFilter {
		if len(s.events) > 0 && !s.events[a.Type] {
			s.dropFilt.Add(1)
			return
		}
		if SeverityRank(a.Severity) < SeverityRank(s.opts.severityMin()) {
			s.dropFilt.Add(1)
			return
		}
	}
	select {
	case s.queue <- a:
	default:
		// Full: drop the oldest, then retry the offer. A lost race just
		// falls through to the drop counter below.
		select {
		case <-s.queue:
			s.dropQ.Add(1)
		default:
		}
		select {
		case s.queue <- a:
		default:
			s.dropQ.Add(1)
		}
	}
}

// Start launches the delivery worker. Idempotent.
func (s *WebhookSink) Start(ctx context.Context) {
	s.startOnce.Do(func() {
		runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		s.cancel = cancel
		s.done = make(chan struct{})
		go func() {
			defer close(s.done)
			s.run(runCtx)
		}()
	})
}

// Stop cancels the worker and joins it. In-flight retries are abandoned;
// queued alerts are dropped (they were going to be dropped on overflow
// anyway — notifications are lossy by design).
func (s *WebhookSink) Stop() {
	s.stopOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
			<-s.done
		}
	})
}

func (s *WebhookSink) run(ctx context.Context) {
	s.windowStart = time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case a := <-s.queue:
			s.deliver(ctx, a)
		}
	}
}

// deliver applies the rate limit, then POSTs the alert with the bounded
// retry schedule.
func (s *WebhookSink) deliver(ctx context.Context, a Alert) {
	now := time.Now()
	if now.Sub(s.windowStart) >= time.Minute {
		s.windowStart = now
		s.windowCount = 0
	}
	if s.windowCount >= s.opts.burstPerMin() {
		s.dropRate.Add(1)
		s.log.Warn("notification dropped: rate limit", "type", a.Type, "burst_per_min", s.opts.burstPerMin())
		return
	}
	s.windowCount++

	delays := s.opts.retryDelays()
	for attempt := 0; ; attempt++ {
		retryAfter, err := s.post(ctx, a)
		if err == nil {
			s.sent.Add(1)
			return
		}
		if attempt >= len(delays) {
			s.failed.Add(1)
			s.log.Error("notification delivery failed", "type", a.Type, "attempts", attempt+1, "error", err)
			return
		}
		// 4xx (other than 429) is a configuration problem on the receiver
		// side — an auth failure or wrong path will not fix itself.
		if isPermanentHTTPError(err) {
			s.failed.Add(1)
			s.log.Error("notification rejected by receiver (not retrying)", "type", a.Type, "error", err)
			return
		}
		delay := delays[attempt]
		if retryAfter > delay {
			delay = retryAfter
		}
		s.retried.Add(1)
		s.log.Warn("notification delivery failed, retrying", "type", a.Type, "attempt", attempt+1, "delay", delay, "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// post sends one attempt. A non-2xx response returns an error carrying the
// status code; transport errors are returned as-is. Both are retryable
// except where isPermanentHTTPError says otherwise.
func (s *WebhookSink) post(ctx context.Context, a Alert) (retryAfter time.Duration, err error) {
	body, err := json.Marshal(a)
	if err != nil {
		return 0, fmt.Errorf("marshal alert: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "MiBeeNvr-notify/1")

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err // transport error / timeout / canceled
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return 0, nil
	}
	// 429: honor Retry-After (seconds; capped at 2 min so a broken receiver
	// cannot stall the worker indefinitely).
	if resp.StatusCode == http.StatusTooManyRequests {
		if ra := parseRetryAfter(resp.Header.Get("Retry-After")); ra > 0 {
			return ra, &httpStatusError{code: resp.StatusCode}
		}
	}
	return 0, &httpStatusError{code: resp.StatusCode}
}

// httpStatusError marks a non-2xx response.
type httpStatusError struct{ code int }

func (e *httpStatusError) Error() string { return fmt.Sprintf("receiver responded %d", e.code) }

// isPermanentHTTPError reports whether the failure is a receiver-side
// rejection that retrying cannot fix (auth/path/shape problems).
func isPermanentHTTPError(err error) bool {
	var he *httpStatusError
	if !errors.As(err, &he) {
		return false // transport error — retryable
	}
	return he.code != http.StatusTooManyRequests && he.code < 500
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs <= 0 {
		return 0
	}
	d := time.Duration(secs) * time.Second
	if d > 2*time.Minute {
		d = 2 * time.Minute
	}
	return d
}
