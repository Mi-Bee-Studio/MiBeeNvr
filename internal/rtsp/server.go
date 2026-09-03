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
	"math/rand"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"

	"github.com/pion/rtp"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtpmjpeg"
	"github.com/bluenviron/gortsplib/v5/pkg/liberrors"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

var rtspLogger = slogx.Component("rtsp-server")

// StreamInfo is the current live-stream state of one camera, as resolved by
// the StreamProvider on demand.
type StreamInfo struct {
	Codec model.Format
	SPS   []byte
	PPS   []byte
	VPS   []byte // H.265 only
	Hub   *streamhub.StreamHub
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
	case model.FormatMJPEG:
		return true // every JPEG frame is standalone — no parameter sets
	default:
		return false
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

// mjpegAUEncoder adapts rtpmjpeg.Encoder (single image) onto the AU-shaped
// rtpEncoder contract: MJPEG AUs are one-element slices holding a whole JPEG.
type mjpegAUEncoder struct {
	enc *rtpmjpeg.Encoder
}

func (m *mjpegAUEncoder) Encode(au [][]byte) ([]*rtp.Packet, error) {
	if len(au) == 0 {
		return nil, fmt.Errorf("mjpeg: empty AU")
	}
	return m.enc.Encode(au[0])
}

// rtspReader is one attached RTSP session's private egress. Each reader owns
// its own ServerStream so mid-GOP joins can be gated per reader (#524 phase
// 2): the shared-stream fan-out wrote P-frames to sessions that never saw the
// GOP head, and pullers' decoders logged reference-missing warnings until the
// next IDR.
type rtspReader struct {
	stream *gortsplib.ServerStream
	media  *description.Media
	// seq numbers this reader's stream emits. gortsplib sends the packet's
	// own SequenceNumber (assigned by the shared encoder at Encode time), so
	// without per-reader renumbering a replayed GOP (old seqs) followed by
	// live frames (current seqs) — or an injected parameter AU — arrives with
	// sequence jumps that strict RTP clients score as massive loss. Cloning
	// the header and renumbering per reader also keeps one stream's SSRC
	// rewrite (gortsplib mutates pkt.SSRC per stream) from clobbering the
	// shared packet other readers are writing.
	seq uint16
	// active flips in OnPlay; frames are only written to PLAYing readers —
	// a SETUP→TEARDOWN without PLAY must not receive or hold traffic.
	active bool
	// skipUntilIDR gates a reader that joined with no replayable GOP: it
	// stays silent until the next keyframe instead of starting mid-GOP.
	skipUntilIDR bool
	// replayPending marks a freshly PLAYing reader that should receive the
	// cached GOP before its first live frame. The replay cannot run inside
	// OnPlay — gortsplib drops stream writes until the session has finished
	// the PLAY handshake — so the first post-PLAY deliver performs it.
	replayPending bool
}

// gopEntry is one encoded access unit in the replay buffer.
type gopEntry struct {
	pkts []*rtp.Packet
	ts   uint32
}

// GOP replay buffer caps: beyond these (pathological long-GOP cameras) the
// buffer invalidates itself and new readers fall back to skip-until-IDR —
// replaying a truncated GOP would reintroduce the reference-missing frames.
const (
	gopMaxFrames = 512
	gopMaxBytes  = 8 << 20
)

// cameraStream is the per-camera serving state: per-reader ServerStreams fed
// by one StreamHub subscription, plus a GOP cache so joining readers get an
// instant, clean picture instead of waiting out the current GOP.
type cameraStream struct {
	cameraID string
	codec    model.Format
	sps      []byte
	pps      []byte
	vps      []byte
	hub      *streamhub.StreamHub
	subID    string

	stream *gortsplib.ServerStream // template stream: DESCRIBE/SDP source, never written
	media  *description.Media
	enc    rtpEncoder

	// rmu guards readers/gop. deliver takes the read side; reader
	// attach/activate/detach take the write side.
	rmu         sync.RWMutex
	sessReaders map[*gortsplib.ServerSession]*rtspReader
	// gop caches the encoded packets since the last keyframe; nil while no
	// complete GOP head has been seen (or after a cap overflow).
	gop      []gopEntry
	gopBytes int

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
	}

	cs.rmu.RLock()
	for _, rs := range cs.sessReaders {
		if !rs.active {
			continue
		}
		// Fresh reader: replay the cached GOP so its decode starts at a GOP
		// head. A keyframe arriving right now makes the replay redundant —
		// the live AU alone is a clean start. Both rs flags are written only
		// by this goroutine (the hub's single drain), so mutation under the
		// read lock is race-free.
		injectParams := false
		if rs.replayPending {
			rs.replayPending = false
			if !m.IsKeyframe && len(cs.gop) > 0 {
				for i, e := range cs.gop {
					if i == 0 {
						// In-band parameter sets ahead of any picture data:
						// pullers don't reliably consume the SDP's sprop, and
						// hub AUs don't consistently carry params per-IDR
						// (recorder-dependent) — without this a replayed GOP
						// head decodes as reference-missing noise.
						cs.writeParamSets(rs, e.ts)
					}
					cs.writePkts(rs, e.pkts)
				}
			} else {
				injectParams = true // replay skipped; the live AU opens the stream
			}
		}
		if rs.skipUntilIDR {
			if !m.IsKeyframe {
				continue // mid-GOP join with no replayable GOP: wait for the IDR
			}
			rs.skipUntilIDR = false
			injectParams = true
		}
		if injectParams {
			cs.writeParamSets(rs, ts)
		}
		cs.writePkts(rs, pkts)
	}
	cs.rmu.RUnlock()

	cs.rmu.Lock()
	// GOP cache maintenance: a keyframe opens a fresh GOP; P-frames append.
	// Overflow (frames or bytes) invalidates the buffer until the next IDR —
	// a truncated replay is exactly the corruption the gating exists to fix.
	if m.IsKeyframe {
		cs.gop = append(cs.gop[:0], gopEntry{pkts: pkts, ts: ts})
		cs.gopBytes = pktBytes(pkts)
	} else if cs.gop != nil {
		if len(cs.gop) >= gopMaxFrames || cs.gopBytes+pktBytes(pkts) > gopMaxBytes {
			cs.gop = nil
			cs.gopBytes = 0
		} else {
			cs.gop = append(cs.gop, gopEntry{pkts: pkts, ts: ts})
			cs.gopBytes += pktBytes(pkts)
		}
	}
	cs.rmu.Unlock()
}

// writeParamSets pushes the camera's cached parameter sets as their own AU.
// Cheap (three small NALs → one aggregation packet) and idempotent for
// decoders when the following AU also carries them in-band.
func (cs *cameraStream) writeParamSets(rs *rtspReader, ts uint32) {
	var au [][]byte
	switch cs.codec {
	case model.FormatH264:
		au = [][]byte{cs.sps, cs.pps}
	case model.FormatH265:
		au = [][]byte{cs.vps, cs.sps, cs.pps}
	default:
		return
	}
	pkts, err := cs.enc.Encode(au)
	if err != nil {
		return
	}
	for _, p := range pkts {
		p.Timestamp = ts
		if err := rs.write(p); err != nil {
			return
		}
	}
}

// writePkts pushes one AU's packets to a reader stream via rs.write (header
// clone + per-reader sequence renumbering). A write error aborts that
// reader's AU (the session is closing); other readers are unaffected.
func (cs *cameraStream) writePkts(rs *rtspReader, pkts []*rtp.Packet) {
	for _, p := range pkts {
		if err := rs.write(p); err != nil {
			rtspLogger.Warn("rtsp write failed", "camera_id", cs.cameraID, "error", err)
			return
		}
	}
}

// write sends one packet on the reader's private stream with a locally
// contiguous sequence number. The payload slice stays shared (read-only in
// the write path); only the header is cloned.
func (rs *rtspReader) write(pkt *rtp.Packet) error {
	c := *pkt
	c.SequenceNumber = rs.seq
	rs.seq++
	return rs.stream.WritePacketRTP(rs.media, &c)
}

func pktBytes(pkts []*rtp.Packet) int {
	n := 0
	for _, p := range pkts {
		n += p.MarshalSize()
	}
	return n
}

// addReader creates the session's private stream (own SDP, same params as the
// template). Inactive until PLAY.
func (cs *cameraStream) addReader(sess *gortsplib.ServerSession) (*rtspReader, error) {
	media, err := cs.buildMedia()
	if err != nil {
		return nil, err
	}
	stream := &gortsplib.ServerStream{Server: cs.stream.Server, Desc: &description.Session{Medias: []*description.Media{media}}}
	if err := stream.Initialize(); err != nil {
		return nil, err
	}
	rs := &rtspReader{stream: stream, media: media, seq: uint16(rand.Uint32())}
	cs.rmu.Lock()
	cs.sessReaders[sess] = rs
	cs.rmu.Unlock()
	return rs, nil
}

// activateReader runs in OnPlay: arm the freshly attached reader for a GOP
// replay (#524) — or skip-until-IDR when no replayable GOP exists. The replay
// itself happens on the reader's first post-PLAY frame inside deliver: writes
// issued during OnPlay are dropped by gortsplib (the session is still
// completing the PLAY handshake). The write lock orders the flag against the
// in-flight deliver fan-out.
func (cs *cameraStream) activateReader(sess *gortsplib.ServerSession) {
	cs.rmu.Lock()
	defer cs.rmu.Unlock()
	rs, ok := cs.sessReaders[sess]
	if !ok || rs.active {
		return
	}
	if len(cs.gop) > 0 {
		rs.replayPending = true
	} else {
		rs.skipUntilIDR = true
	}
	rs.active = true
}

// removeReader closes and forgets one session's stream.
func (cs *cameraStream) removeReader(sess *gortsplib.ServerSession) {
	cs.rmu.Lock()
	rs := cs.sessReaders[sess]
	delete(cs.sessReaders, sess)
	cs.rmu.Unlock()
	if rs != nil {
		rs.stream.Close()
	}
}

// closeReaders tears down every reader stream (camera teardown / Stop).
func (cs *cameraStream) closeReaders() {
	cs.rmu.Lock()
	lst := make([]*rtspReader, 0, len(cs.sessReaders))
	for _, rs := range cs.sessReaders {
		lst = append(lst, rs)
	}
	cs.sessReaders = make(map[*gortsplib.ServerSession]*rtspReader)
	cs.rmu.Unlock()
	for _, rs := range lst {
		rs.stream.Close()
	}
}

// Server is the RTSP output server.
type Server struct {
	cfg      Config
	provider StreamProvider
	gs       *gortsplib.Server

	mu      sync.RWMutex
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

// Start binds the RTSP listener (blocking) and spawns the serve loop. The
// whole gortsplib Start runs under s.mu so a concurrent Stop either sees the
// server fully initialized (Close is safe) or not started at all — gortsplib
// panics on Close-during-Start, and App.Stop can race the service's start
// goroutine on fast shutdowns (CI TestRunFree_StopJoinsBackgroundGoroutines).
func (s *Server) Start(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return nil
	}
	if err := s.gs.Start(); err != nil {
		return err
	}
	s.started = true
	go func() {
		if err := s.gs.Wait(); err != nil {
			rtspLogger.Error("rtsp output server terminated", "error", err)
		}
	}()
	rtspLogger.Info("rtsp output server listening", "addr", s.cfg.Addr,
		"auth", s.cfg.Username != "" || s.cfg.Password != "")
	return nil
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
	cs.closeReaders()
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
		cameraID:    cameraID,
		codec:       info.Codec,
		sps:         bytes.Clone(info.SPS),
		pps:         bytes.Clone(info.PPS),
		vps:         bytes.Clone(info.VPS),
		hub:         info.Hub,
		subID:       "rtsp-" + cameraID,
		sessReaders: make(map[*gortsplib.ServerSession]*rtspReader),
	}

	media, err := cs.buildMedia()
	if err != nil {
		rtspLogger.Error("rtsp media init failed", "camera_id", cameraID, "error", err)
		return nil
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

// buildMedia constructs a fresh video media (SDP) from the camera's current
// parameter sets and, on first call, the shared per-camera RTP encoder.
func (cs *cameraStream) buildMedia() (*description.Media, error) {
	var forma format.Format
	switch cs.codec {
	case model.FormatH264:
		f := &format.H264{
			PayloadTyp:        96,
			SPS:               cs.sps,
			PPS:               cs.pps,
			PacketizationMode: 1,
		}
		if cs.enc == nil {
			enc, err := f.CreateEncoder()
			if err != nil {
				return nil, err
			}
			cs.enc = enc
		}
		forma = f
	case model.FormatH265:
		f := &format.H265{
			PayloadTyp: 96,
			VPS:        cs.vps,
			SPS:        cs.sps,
			PPS:        cs.pps,
		}
		if cs.enc == nil {
			enc, err := f.CreateEncoder()
			if err != nil {
				return nil, err
			}
			cs.enc = enc
		}
		forma = f
	case model.FormatMJPEG:
		f := &format.MJPEG{}
		if cs.enc == nil {
			enc, err := f.CreateEncoder()
			if err != nil {
				return nil, err
			}
			cs.enc = &mjpegAUEncoder{enc: enc}
		}
		forma = f
	default:
		return nil, fmt.Errorf("unsupported codec %s", cs.codec)
	}
	return &description.Media{
		Type:    description.MediaTypeVideo,
		Control: "trackID=0",
		Formats: []format.Format{forma},
	}, nil
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
		cs.removeReader(ctx.Session)
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
	rs, err := cs.addReader(ctx.Session)
	if err != nil {
		// The session cannot be served — uncount it so the reader gauge and
		// the idle fast-path stay honest.
		s.mu.Lock()
		if s.sessions[ctx.Session] == cs {
			delete(s.sessions, ctx.Session)
			cs.readers.Add(-1)
		}
		s.mu.Unlock()
		rtspLogger.Error("rtsp reader stream init failed", "camera_id", cs.cameraID, "error", err)
		return &base.Response{StatusCode: base.StatusInternalServerError}, nil, err
	}
	return &base.Response{StatusCode: base.StatusOK}, rs.stream, nil
}

// OnPlay implements gortsplib.ServerHandlerOnPlay.
func (s *Server) OnPlay(ctx *gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	s.mu.RLock()
	cs := s.sessions[ctx.Session]
	s.mu.RUnlock()
	if cs != nil {
		cs.activateReader(ctx.Session)
	}
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
