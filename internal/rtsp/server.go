// Package rtsp implements the built-in RTSP output server (#522): the NVR
// serves rtsp://<host>:<port>/<camera_id> pull URLs so third-party platforms
// (Synology Surveillance Station etc.) can ingest cameras directly, without a
// MediaMTX intermediary (#499). PLAY direction only, video-only H.264/H.265
// natively (no transcoding), frames tapped from the recorders' StreamHub —
// the same source the FLV/HLS/WS live endpoints consume.
package rtsp

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/liberrors"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
)

var rtspLogger = slog.Default().With("component", "rtsp-server")

// StreamInfo is the current live-stream state of one camera, as resolved by
// the StreamProvider on demand.
type StreamInfo struct {
	Codec model.Format
	SPS   []byte
	PPS   []byte
	VPS   []byte // H.265 only
	Hub   *model.StreamHub
}

// Ready reports whether the stream has everything needed to build an SDP.
func (i StreamInfo) Ready() bool {
	if i.Hub == nil {
		return false
	}
	switch i.Codec {
	case model.FormatH264:
		return len(i.SPS) > 0 && len(i.PPS) > 0
	case model.FormatH265:
		return len(i.VPS) > 0 && len(i.SPS) > 0 && len(i.PPS) > 0
	default:
		return false // mjpeg/jpeg cameras are not served over RTSP yet
	}
}

// StreamProvider resolves a camera ID to its current live stream. Called on
// every DESCRIBE/SETUP so recorder restarts (a new hub) and parameter-set
// changes are picked up without restarting the server.
type StreamProvider func(cameraID string) (StreamInfo, bool)

// Config configures the RTSP output server.
type Config struct {
	// Addr is the RTSP listen address (e.g. ":8554").
	Addr string
	// Username/Password enable HTTP basic/digest auth when both semantics are
	// desired. Both empty = open access, matching the HTTP-FLV live endpoint's
	// LAN posture. Clients then use rtsp://user:pass@host/<camera>.
	Username string
	Password string
	// ListenFn overrides the TCP listener factory (tests bind a deterministic
	// port). Nil = net.Listen.
	ListenFn func(network, addr string) (net.Listener, error)
}

// rtpEncoder abstracts rtph264.Encoder / rtph265.Encoder.
type rtpEncoder interface {
	Encode(au [][]byte) ([]*rtp.Packet, error)
}

// cameraStream is the per-camera serving state: one gortsplib ServerStream
// fanned out to all its readers, fed by a StreamHub subscription.
type cameraStream struct {
	cameraID string
	codec    model.Format
	sps      []byte
	pps      []byte
	vps      []byte
	hub      *model.StreamHub
	subID    string

	stream *gortsplib.ServerStream
	media  *description.Media
	enc    rtpEncoder

	// tsOrigin is the IngestAt (unix ns) of the first delivered frame; RTP
	// timestamps derive from it at 90 kHz. Recorder hubs carry heterogeneous
	// pts units, so the hub-entry wallclock is the only consistent clock.
	tsOrigin int64
	// readers counts PLAYing sessions. Encoding is skipped at zero readers —
	// keeps an idle subscribed camera at one atomic load per frame.
	readers atomic.Int64
	closed  atomic.Bool
}

func (cs *cameraStream) deliver(m model.FrameMsg) {
	if cs.closed.Load() || cs.readers.Load() == 0 {
		return
	}
	at := m.IngestAt
	if at <= 0 {
		at = time.Now().UnixNano()
	}
	if cs.tsOrigin == 0 {
		cs.tsOrigin = at
	}
	ts := uint32(((at - cs.tsOrigin) / 1000) * 9 / 1000) // ns → 90 kHz
	pkts, err := cs.enc.Encode(m.AU)
	if err != nil {
		rtspLogger.Warn("rtsp encode failed", "camera_id", cs.cameraID, "error", err)
		return
	}
	for _, p := range pkts {
		p.Timestamp = ts
		if err := cs.stream.WritePacketRTP(cs.media, p); err != nil {
			rtspLogger.Warn("rtsp write failed", "camera_id", cs.cameraID, "error", err)
			return
		}
	}
}

// Server is the RTSP output server.
type Server struct {
	cfg      Config
	provider StreamProvider
	gs       *gortsplib.Server

	mu      sync.Mutex
	streams map[string]*cameraStream
	// sessions maps RTSP sessions to their camera stream for reader counting.
	sessions map[*gortsplib.ServerSession]*cameraStream
	// started/stopped (under mu) make Stop safe against a Stop that races a
	// Start still inside gortsplib's startup (fast shutdown after service
	// registration).
	started bool
	stopped bool
}

// NewServer creates an RTSP output server. Call Start to bind.
func NewServer(cfg Config, provider StreamProvider) *Server {
	s := &Server{
		cfg:      cfg,
		provider: provider,
		streams:  make(map[string]*cameraStream),
		sessions: make(map[*gortsplib.ServerSession]*cameraStream),
	}
	s.gs = &gortsplib.Server{
		Handler:     s,
		RTSPAddress: cfg.Addr,
	}
	if cfg.ListenFn != nil {
		s.gs.Listen = cfg.ListenFn
	}
	return s
}

// Start binds the RTSP listener and blocks serving until Stop/Close or a
// fatal listener error. Run in a goroutine (mirrors the RTMP server shape).
func (s *Server) Start(_ context.Context) error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.started = true
	s.mu.Unlock()
	rtspLogger.Info("rtsp output server listening", "addr", s.cfg.Addr,
		"auth", s.cfg.Username != "" || s.cfg.Password != "")
	return s.gs.StartAndWait()
}

// Stop tears the server down: closes all camera streams (disconnecting their
// readers), unsubscribes from the hubs, and closes the listener. Safe to call
// even when Start was never reached (gortsplib panics on Close-before-Start).
func (s *Server) Stop() error {
	s.mu.Lock()
	streams := make([]*cameraStream, 0, len(s.streams))
	for _, cs := range s.streams {
		streams = append(streams, cs)
	}
	s.streams = make(map[string]*cameraStream)
	s.sessions = make(map[*gortsplib.ServerSession]*cameraStream)
	wasStarted := s.started
	s.stopped = true
	s.mu.Unlock()

	for _, cs := range streams {
		s.teardownStream(cs)
	}
	// gortsplib panics on Close-before-Start; a Stop that lost the race with
	// Start's goroutine just prevents the listener from ever coming up.
	if wasStarted {
		s.gs.Close()
	}
	return nil
}

func (s *Server) teardownStream(cs *cameraStream) {
	cs.closed.Store(true)
	cs.hub.Unsubscribe(cs.subID)
	cs.stream.Close()
}

// reloadParams hot-swaps the SDP parameter sets on a live stream (same hub):
// connected readers keep streaming; new DESCRIBEs see the updated SDP.
func (cs *cameraStream) reloadParams(info StreamInfo) {
	cs.sps = bytes.Clone(info.SPS)
	cs.pps = bytes.Clone(info.PPS)
	cs.vps = bytes.Clone(info.VPS)
	switch f := cs.media.Formats[0].(type) {
	case *format.H264:
		f.SPS = cs.sps
		f.PPS = cs.pps
	case *format.H265:
		f.VPS = cs.vps
		f.SPS = cs.sps
		f.PPS = cs.pps
	}
	cs.stream.ReloadDesc()
	rtspLogger.Info("rtsp stream parameters reloaded", "camera_id", cs.cameraID)
}

// streamFor returns the serving stream for the camera. Parameter-set changes
// on the same hub hot-reload the SDP (gortsplib ReloadDesc — readers keep
// streaming, they also receive the new sets in-band); a recorder restart (new
// hub) rebuilds the stream. Returns nil when the camera is unknown, not an
// H.264/H.265 live stream, or still warming up (readers naturally retry).
func (s *Server) streamFor(cameraID string) *cameraStream {
	if cameraID == "" || s.provider == nil {
		return nil
	}
	info, ok := s.provider(cameraID)
	if !ok || !info.Ready() {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if cs, ok := s.streams[cameraID]; ok {
		switch {
		case cs.hub != info.Hub:
			// Recorder restart: the old hub is dead — tear the stream down
			// (readers disconnect and re-DESCRIBE) and fall through to rebuild.
			s.teardownStream(cs)
			delete(s.streams, cameraID)
			rtspLogger.Info("rtsp stream rebuilt (recorder restart)",
				"camera_id", cameraID, "codec", string(info.Codec))
		case !bytes.Equal(cs.sps, info.SPS) || !bytes.Equal(cs.pps, info.PPS) || !bytes.Equal(cs.vps, info.VPS):
			// Parameter-set change (e.g. camera resolution switch): reload the
			// description without disconnecting readers.
			cs.reloadParams(info)
			return cs
		default:
			return cs
		}
	}

	cs := &cameraStream{
		cameraID: cameraID,
		codec:    info.Codec,
		sps:      bytes.Clone(info.SPS),
		pps:      bytes.Clone(info.PPS),
		vps:      bytes.Clone(info.VPS),
		hub:      info.Hub,
		subID:    "rtsp-" + cameraID,
	}

	var forma format.Format
	switch info.Codec {
	case model.FormatH264:
		f := &format.H264{
			PayloadTyp:        96,
			SPS:               cs.sps,
			PPS:               cs.pps,
			PacketizationMode: 1,
		}
		enc, err := f.CreateEncoder()
		if err != nil {
			rtspLogger.Error("rtsp h264 encoder init failed", "camera_id", cameraID, "error", err)
			return nil
		}
		forma = f
		cs.enc = enc
	case model.FormatH265:
		f := &format.H265{
			PayloadTyp: 96,
			VPS:        cs.vps,
			SPS:        cs.sps,
			PPS:        cs.pps,
		}
		enc, err := f.CreateEncoder()
		if err != nil {
			rtspLogger.Error("rtsp h265 encoder init failed", "camera_id", cameraID, "error", err)
			return nil
		}
		forma = f
		cs.enc = enc
	default:
		return nil
	}

	media := &description.Media{
		Type:    description.MediaTypeVideo,
		Control: "trackID=0",
		Formats: []format.Format{forma},
	}
	desc := &description.Session{
		Medias: []*description.Media{media},
	}
	stream := &gortsplib.ServerStream{Server: s.gs, Desc: desc}
	if err := stream.Initialize(); err != nil {
		rtspLogger.Error("rtsp stream init failed", "camera_id", cameraID, "error", err)
		return nil
	}
	cs.stream = stream
	cs.media = media

	if err := info.Hub.SubscribeMsg(cs.subID, func(m model.FrameMsg) { cs.deliver(m) }); err != nil {
		stream.Close()
		rtspLogger.Error("rtsp hub subscribe failed", "camera_id", cameraID, "error", err)
		return nil
	}

	s.streams[cameraID] = cs
	rtspLogger.Info("rtsp stream serving", "camera_id", cameraID, "codec", string(info.Codec))
	return cs
}

func (s *Server) authorized(conn *gortsplib.ServerConn, req *base.Request) bool {
	if s.cfg.Username == "" && s.cfg.Password == "" {
		return true
	}
	return conn.VerifyCredentials(req, s.cfg.Username, s.cfg.Password)
}

// --- gortsplib.ServerHandler ---

// OnConnOpen implements gortsplib.ServerHandler.
func (s *Server) OnConnOpen(*gortsplib.ServerHandlerOnConnOpenCtx) {}

// OnConnClose implements gortsplib.ServerHandlerOnConnClose.
func (s *Server) OnConnClose(ctx *gortsplib.ServerHandlerOnConnCloseCtx) {}

// OnSessionOpen implements gortsplib.ServerHandler.
func (s *Server) OnSessionOpen(*gortsplib.ServerHandlerOnSessionOpenCtx) {}

// OnSessionClose implements gortsplib.ServerHandlerOnSessionClose.
func (s *Server) OnSessionClose(ctx *gortsplib.ServerHandlerOnSessionCloseCtx) {
	s.mu.Lock()
	cs := s.sessions[ctx.Session]
	delete(s.sessions, ctx.Session)
	s.mu.Unlock()
	if cs != nil {
		cs.readers.Add(-1)
	}
}

// OnDescribe implements gortsplib.ServerHandlerOnDescribe.
func (s *Server) OnDescribe(ctx *gortsplib.ServerHandlerOnDescribeCtx) (*base.Response, *gortsplib.ServerStream, error) {
	if !s.authorized(ctx.Conn, ctx.Request) {
		return &base.Response{StatusCode: base.StatusUnauthorized}, nil, liberrors.ErrServerAuth{}
	}
	cs := s.streamFor(cameraIDFromPath(ctx.Request.URL.Path))
	if cs == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}
	return &base.Response{StatusCode: base.StatusOK}, cs.stream, nil
}

// OnSetup implements gortsplib.ServerHandlerOnSetup.
func (s *Server) OnSetup(ctx *gortsplib.ServerHandlerOnSetupCtx) (*base.Response, *gortsplib.ServerStream, error) {
	if !s.authorized(ctx.Conn, ctx.Request) {
		return &base.Response{StatusCode: base.StatusUnauthorized}, nil, liberrors.ErrServerAuth{}
	}
	cs := s.streamFor(cameraIDFromPath(ctx.Request.URL.Path))
	if cs == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}
	// Count the session as a reader at SETUP (symmetric with the decrement in
	// OnSessionClose): gortsplib only delivers frames after PLAY, but a
	// SETUP→TEARDOWN without PLAY must not skew the count negative.
	s.mu.Lock()
	if _, ok := s.sessions[ctx.Session]; !ok {
		s.sessions[ctx.Session] = cs
		cs.readers.Add(1)
	}
	s.mu.Unlock()
	return &base.Response{StatusCode: base.StatusOK}, cs.stream, nil
}

// OnPlay implements gortsplib.ServerHandlerOnPlay.
func (s *Server) OnPlay(*gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	return &base.Response{StatusCode: base.StatusOK}, nil
}

// cameraIDFromPath extracts the camera ID from an RTSP request path. DESCRIBE
// targets /<camera_id>; SETUP targets the media control path /<camera_id>/trackID=0.
func cameraIDFromPath(p string) string {
	p = strings.Trim(p, "/")
	if p == "" {
		return ""
	}
	if i := strings.LastIndex(p, "/"); i >= 0 {
		last := p[i+1:]
		if strings.HasPrefix(last, "trackID=") || strings.HasPrefix(last, "streamid=") {
			p = p[:i]
		}
	}
	return p
}

// URLFor builds the pull URL advertised to clients (API/UI copy buttons).
// host is a host or host:port (the port is dropped — the RTSP server has its
// own); bare IPv6 hosts are bracketed.
func URLFor(host string, port int, cameraID string) string {
	if h, _, err := splitHostPort(host); err == nil {
		host = h
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("rtsp://%s:%d/%s", host, port, cameraID)
}

func splitHostPort(hostport string) (string, string, error) {
	i := strings.LastIndex(hostport, ":")
	if i < 0 {
		return hostport, "", fmt.Errorf("missing port")
	}
	return strings.Trim(hostport[:i], "[]"), hostport[i+1:], nil
}
