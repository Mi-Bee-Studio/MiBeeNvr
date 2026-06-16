package relay

import (
	"context"
	"log/slog"
	"net/url"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/backoff"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"

	"github.com/bluenviron/gortmplib"
	"github.com/bluenviron/gortmplib/pkg/codecs"
	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
)

var engineLogger = slog.Default().With("component", "relay-engine")

// PushTargetConfig is the user-facing configuration for one push-out target.
// Mirrors config.PushTargetConfig but kept local to avoid a config<->relay
// import cycle (the manager maps between them).
type PushTargetConfig struct {
	ID       string // stable id within the camera
	Name     string
	Protocol string // "rtmp" or "rtsp"
	URL      string // rtmp://host[:port]/app/key  |  rtsp://host[:port]/path
	Enabled  bool
}

// SPSProvider returns the source camera's current SPS/PPS (raw NALUs, no start
// code) so an RTMP target can initialize its track, and reports whether the
// source is H.264 (RTMP targets require H.264; H.265 sources are rejected).
type SPSProvider func() (sps, pps []byte, isH264 bool)

// PushTarget is one push-out destination: it subscribes to a camera's StreamHub
// and forwards each access unit to the target (RTMP or RTSP) over a dedicated
// connection. Each target runs in its own goroutine with independent reconnect.
type PushTarget struct {
	CameraID string
	Config   PushTargetConfig

	hub         *model.StreamHub
	spsProvider SPSProvider

	mu        sync.RWMutex
	status    RelayStatus
	errMsg    string
	since     time.Time // status-effective time (connect/stream start)
	cancel    context.CancelFunc
	done      chan struct{}

	// bitrate accounting (atomic, sampled by status())
	bytesSent   atomic.Int64
	lastSample  time.Time
	sampleBytes int64
	sampleKbps  atomic.Int64
}

// NewPushTarget constructs an idle target. It does not connect until Run.
func NewPushTarget(cameraID string, cfg PushTargetConfig, hub *model.StreamHub, sps SPSProvider) *PushTarget {
	return &PushTarget{
		CameraID:    cameraID,
		Config:      cfg,
		hub:         hub,
		spsProvider: sps,
		status:      StatusIdle,
	}
}

// Status returns a snapshot of the target's runtime status for the API/UI.
func (t *PushTarget) Status() TargetStatus {
	t.mu.RLock()
	st := t.status
	errMsg := t.errMsg
	since := t.since
	t.mu.RUnlock()

	// Sample outbound bitrate over the last interval.
	now := time.Now()
	var kbps float64
	if !t.lastSample.IsZero() {
		elapsed := now.Sub(t.lastSample).Seconds()
		if elapsed > 0.1 {
			cur := t.bytesSent.Load()
			kbps = float64(cur-t.sampleBytes) / elapsed / 1024.0 * 8.0
			t.lastSample = now
			t.sampleBytes = cur
		} else {
			kbps = float64(t.sampleKbps.Load())
		}
	} else {
		t.lastSample = now
		t.sampleBytes = t.bytesSent.Load()
	}

	uptime := ""
	if (st == StatusStreaming || st == StatusReconnecting) && !since.IsZero() {
		uptime = time.Since(since).Round(time.Second).String()
	}
	return TargetStatus{
		ID:        t.Config.ID,
		Name:      t.Config.Name,
		Protocol:  t.Config.Protocol,
		URL:       t.Config.URL,
		Status:    st,
		Kbps:      kbps,
		Enabled:   t.Config.Enabled,
		Uptime:    uptime,
		Error:     errMsg,
		UpdatedAt: now,
	}
}

// Run starts the target and blocks until ctx is canceled or the hub is gone.
// It owns the reconnect loop. Safe to call via `go t.Run(ctx)`.
func (t *PushTarget) Run(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 4096)
			buf = buf[:runtime.Stack(buf, false)]
			engineLogger.Error("PANIC recovered in PushTarget.Run",
				"camera_id", t.CameraID, "target_id", t.Config.ID, "panic", r, "stack", string(buf))
		}
	}()
	if t.hub == nil {
		t.setStatus(StatusError, "source camera has no stream hub")
		return
	}
	var attempt int
	for {
		if ctx.Err() != nil {
			return
		}
		err := t.connectAndStream(ctx)
		if ctx.Err() != nil {
			return
		}
		attempt++
		bo := backoff.TieredBackoffWithJitter(attempt)
		engineLogger.Warn("relay target disconnected, retrying",
			"camera_id", t.CameraID, "target_id", t.Config.ID,
			"protocol", t.Config.Protocol, "error", err, "attempt", attempt, "backoff", bo)
		t.setStatus(StatusReconnecting, err.Error())
		select {
		case <-ctx.Done():
			return
		case <-time.After(bo):
		}
	}
}

// connectAndStream establishes the target connection, subscribes to the hub,
// and blocks while streaming. Returns a non-nil error when the stream ends.
func (t *PushTarget) connectAndStream(ctx context.Context) error {
	switch t.Config.Protocol {
	case "rtmp":
		return t.connectRTMP(ctx)
	case "rtsp":
		return t.connectRTSP(ctx)
	default:
		t.setStatus(StatusError, "unsupported protocol: "+t.Config.Protocol)
		return errPermanent
	}
}

// --- RTMP target ---

func (t *PushTarget) connectRTMP(ctx context.Context) error {
	// RTMP only carries H.264. Reject H.265 / unknown sources up front.
	sps, pps, isH264 := t.spsProvider()
	if !isH264 || sps == nil || pps == nil {
		t.setStatus(StatusError, "source is not H.264 (RTMP target requires H.264)")
		return errPermanent
	}

	t.setStatus(StatusConnecting, "")
	u, err := url.Parse(t.Config.URL)
	if err != nil || (u.Scheme != "rtmp" && u.Scheme != "rtmps") {
		t.setStatus(StatusError, "invalid RTMP url")
		return errPermanent
	}

	client := &gortmplib.Client{URL: u, Publish: true}
	if err := client.Initialize(ctx); err != nil {
		return err
	}
	defer client.Close()

	track := &gortmplib.Track{Codec: &codecs.H264{
		SPS: append([]byte(nil), sps...),
		PPS: append([]byte(nil), pps...),
	}}
	writer := &gortmplib.Writer{Conn: client, Tracks: []*gortmplib.Track{track}}
	if err := writer.Initialize(); err != nil {
		return err
	}

	// Subscribe to the source hub; the callback runs in its own goroutine and
	// may block on the target write without affecting recording/live.
	start := time.Now()
	consumerID := "relay-rtmp-" + t.Config.ID
	cbErr := make(chan error, 1)
	if err := t.hub.Subscribe(consumerID, func(pts int64, au [][]byte) {
		// Use wall-clock relative PTS (avoids 90kHz wraparound; matches the
		// gortmplib publish example). dts == pts (assume no B-frame reorder).
		d := time.Since(start)
		if d < 0 {
			d = 0
		}
		if werr := writer.WriteH264(track, d, d, au); werr != nil {
			select {
			case cbErr <- werr:
			default:
			}
		} else {
			// Account bytes (sum of NALU lengths, ~ payload, good enough for kbps).
			var n int64
			for _, nalu := range au {
				n += int64(len(nalu))
			}
			t.bytesSent.Add(n)
		}
	}); err != nil {
		return err
	}
	defer t.hub.Unsubscribe(consumerID)

	t.setStatus(StatusStreaming, "")
	engineLogger.Info("relay target streaming",
		"camera_id", t.CameraID, "target_id", t.Config.ID, "protocol", "rtmp", "url", t.Config.URL)

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-cbErr:
		return err
	}
}

// --- RTSP target ---

func (t *PushTarget) connectRTSP(ctx context.Context) error {
	sps, pps, isH264 := t.spsProvider()
	// RTSP can carry H.265 in principle, but this relay path is H.264-only for
	// now (the hub delivers H.264 NALUs from the recorders). Reject H.265.
	if !isH264 || sps == nil || pps == nil {
		t.setStatus(StatusError, "source stream not ready (no SPS/PPS yet)")
		return errPermanent
	}

	t.setStatus(StatusConnecting, "")
	tcp := gortsplib.ProtocolTCP
	client := &gortsplib.Client{Protocol: &tcp, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}

	forma := &format.H264{
		PayloadTyp:        96,
		SPS:               append([]byte(nil), sps...),
		PPS:               append([]byte(nil), pps...),
		PacketizationMode: 1,
	}
	desc := &description.Session{Medias: []*description.Media{{
		Type:    description.MediaTypeVideo,
		Formats: []format.Format{forma},
	}}}
	if err := client.StartRecording(t.Config.URL, desc); err != nil {
		return err
	}
	defer client.Close()

	rtpEnc, err := forma.CreateEncoder()
	if err != nil {
		return err
	}
	targetMedia := desc.Medias[0]

	consumerID := "relay-rtsp-" + t.Config.ID
	cbErr := make(chan error, 1)
	if err := t.hub.Subscribe(consumerID, func(pts int64, au [][]byte) {
		// Re-encode the access unit into RTP packets, preserving the source
		// 90kHz PTS as the packet timestamp (relay is transparent remux).
		pkts, eerr := rtpEnc.Encode(au)
		if eerr != nil {
			return
		}
		base := uint32(pts)
		for i, pkt := range pkts {
			if i == 0 {
				pkt.Timestamp = base
			} else {
				pkt.Timestamp = base + pkt.Timestamp
			}
			if werr := client.WritePacketRTP(targetMedia, pkt); werr != nil {
				select {
				case cbErr <- werr:
				default:
				}
				return
			}
			t.bytesSent.Add(int64(pkt.MarshalSize()))
		}
	}); err != nil {
		return err
	}
	defer t.hub.Unsubscribe(consumerID)

	t.setStatus(StatusStreaming, "")
	engineLogger.Info("relay target streaming",
		"camera_id", t.CameraID, "target_id", t.Config.ID, "protocol", "rtsp", "url", t.Config.URL)

	// Drive the client read loop; it surfaces transport errors.
	waitErr := make(chan error, 1)
	go func() { waitErr <- client.Wait() }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-cbErr:
		return err
	case err := <-waitErr:
		return err
	}
}

func (t *PushTarget) setStatus(st RelayStatus, errMsg string) {
	t.mu.Lock()
	t.status = st
	t.errMsg = errMsg
	if st == StatusStreaming || st == StatusConnecting {
		t.since = time.Now()
	}
	t.mu.Unlock()
}

// Sentinel errors.
var (
	errPermanent = errPermanentDef{}
)

type errPermanentDef struct{}

func (errPermanentDef) Error() string { return "permanent relay error (no retry)" }
