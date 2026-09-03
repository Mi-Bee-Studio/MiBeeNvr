// Package whip implements the ingest side of WHIP (WebRTC-HTTP Ingest
// Protocol, draft-ietf-wish-whip): browsers (getUserMedia), phones, and OBS
// 30+ push H.264 + Opus into the NVR over WebRTC — no app, no FFmpeg, and
// ICE/STUN/TURN traversal for remote contributors that SRT/RTMP can't do
// without port exposure (#369).
//
// Endpoint shape mirrors the RTMP ingest's key→camera mapping:
//
//	POST   /whip/{streamKey}             SDP offer → 201 + SDP answer + Location
//	DELETE /whip/{streamKey}/{session}   tear the session down
//
// The stream key is the credential (same threat model as RTMP push keys and
// SRT streamid): the endpoint is intentionally mounted in the anonymous route
// group because WHIP clients like OBS send the key inside the URL, not as an
// auth header.
package whip

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/streamhub"
)

var logger = slogx.Component("whip-server")

const (
	// offerMaxSize bounds the request body (SDP offers are a few KB).
	offerMaxSize = 1 << 20
	// iceGatherTimeout bounds candidate collection before answering.
	iceGatherTimeout = 10 * time.Second
	// noMediaTimeout: sessions that never deliver an RTP packet are torn down
	// (a publisher whose PC connected but sends nothing would otherwise hold
	// the camera's single-publisher slot forever).
	noMediaTimeout = 30 * time.Second
	// idleTimeout: no RTP for this long during streaming → session teardown.
	// PeerConnection state changes usually fire first; this is the backstop.
	idleTimeout = 60 * time.Second
)

// StreamKeyResolver maps a WHIP stream key to a camera ID.
type StreamKeyResolver func(streamKey string) (cameraID string, ok bool)

// CameraHubProvider returns the StreamHub for a camera (nil = create fresh).
type CameraHubProvider func(cameraID string) *streamhub.StreamHub

// NALUCallback forwards an assembled H.264 access unit to the IngestRecorder.
// ptsTicks is a 90 kHz value; isIDR marks keyframes.
type NALUCallback func(au [][]byte, ptsTicks int64, isIDR bool)

// AudioCallback forwards one raw Opus frame. ptsTicks is on the 48 kHz Opus
// RTP clock; dur is the frame duration.
type AudioCallback func(codec string, ptsTicks int64, data []byte, dur time.Duration)

// Server accepts WHIP publisher sessions over the main HTTP listener.
type Server struct {
	api *webrtc.API

	resolv StreamKeyResolver
	hubFn  CameraHubProvider
	// onConn/onDisc mirror the RTMP server lifecycle hooks.
	onConn func(cameraID string, hub *streamhub.StreamHub)
	onDisc func(cameraID string)
	// NALUProvider returns the recording callback for a camera (nil = record
	// nothing, live-only passthrough). AudioProvider likewise for audio.
	NALUProvider func(cameraID string) NALUCallback
	// AudioFormatter notifies the recorder of the negotiated audio format
	// (maps to IngestRecorder.SetAudioFormat); nil = audio not recorded.
	AudioFormatter func(cameraID string, codec string, sampleRate, channels int)
	AudioProvider  func(cameraID string) AudioCallback

	mu       sync.Mutex
	sessions map[string]*session // sessionID → session
	byCamera map[string]*session // cameraID → active session (single publisher)
	stopped  bool
	// runCtx is the service lifecycle context (set by Start). Sessions MUST
	// NOT derive from an HTTP request context — the request ctx cancels when
	// the 201 response is written, which would instantly kill every track
	// handler goroutine.
	runCtx     context.Context
	runCtxOnce sync.Once
}

// NewServer creates a WHIP ingest server. The resolv/hubFn/onConn/onDisc
// wiring mirrors rtmp.NewServer.
func NewServer(
	resolv StreamKeyResolver,
	hubFn CameraHubProvider,
	onConn func(cameraID string, hub *streamhub.StreamHub),
	onDisc func(cameraID string),
	iceServers []webrtc.ICEServer,
) *Server {
	mediaEngine := &webrtc.MediaEngine{}
	// H.264 variants mirror the WHEP egress engine so whatever browsers/OBS
	// encode (Constrained Baseline … High) negotiates.
	for i, fmtp := range h264Profiles() {
		if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:    webrtc.MimeTypeH264,
				ClockRate:   90000,
				SDPFmtpLine: fmtp,
			},
			PayloadType: webrtc.PayloadType(96 + i),
		}, webrtc.RTPCodecTypeVideo); err != nil {
			logger.Error("failed to register H264 codec", "fmtp", fmtp, "error", err)
		}
	}
	// Audio: Opus (the WebRTC mandatory codec every publisher offers); G.711
	// registered too so minimal clients negotiate.
	for _, ac := range []struct {
		mime string
		rate uint32
		ch   uint16
		pt   webrtc.PayloadType
	}{
		{webrtc.MimeTypeOpus, 48000, 2, 111},
		{webrtc.MimeTypePCMU, 8000, 1, 0},
		{webrtc.MimeTypePCMA, 8000, 1, 8},
	} {
		if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType:  ac.mime,
				ClockRate: ac.rate,
				Channels:  ac.ch,
			},
			PayloadType: ac.pt,
		}, webrtc.RTPCodecTypeAudio); err != nil {
			logger.Error("failed to register audio codec", "mime", ac.mime, "error", err)
		}
	}

	interceptorRegistry := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(mediaEngine, interceptorRegistry); err != nil {
		logger.Error("failed to register default interceptors", "error", err)
	}

	return &Server{
		api: webrtc.NewAPI(
			webrtc.WithMediaEngine(mediaEngine),
			webrtc.WithInterceptorRegistry(interceptorRegistry),
		),
		resolv:   resolv,
		hubFn:    hubFn,
		onConn:   onConn,
		onDisc:   onDisc,
		sessions: make(map[string]*session),
		byCamera: make(map[string]*session),
	}
}

// h264Profiles mirrors internal/webrtc's egress profile list (keep in sync).
func h264Profiles() []string {
	return []string{
		"level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f",
		"level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
		"level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=4d001f",
		"level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=640028",
		"level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=640c1f",
	}
}

// session is one accepted WHIP publisher.
type session struct {
	id         string
	cameraID   string
	streamKey  string
	pc         *webrtc.PeerConnection
	cancel     context.CancelFunc
	lastPacket atomic.Int64 // unix nano of the last RTP packet
}

// Start captures the service lifecycle context. Called once by the app
// service registration; sessions derive their cancellation from it.
func (s *Server) Start(ctx context.Context) {
	s.runCtxOnce.Do(func() {
		s.runCtx = ctx
	})
}

// RegisterRoutes mounts the WHIP endpoints. The router must NOT apply auth
// middleware — the stream key is the credential (RTMP/SRT threat model).
func (s *Server) RegisterRoutes(r chi.Router) {
	r.Post("/whip/{streamKey}", s.handleOffer)
	r.Delete("/whip/{streamKey}/{session}", s.handleDelete)
}

// handleOffer implements POST /whip/{streamKey}.
func (s *Server) handleOffer(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/sdp") {
		http.Error(w, "Content-Type must be application/sdp", http.StatusUnsupportedMediaType)
		return
	}
	streamKey := pathParam(r, "streamKey")
	offerSDP, err := io.ReadAll(io.LimitReader(r.Body, offerMaxSize))
	if err != nil || len(offerSDP) == 0 {
		http.Error(w, "failed to read SDP offer", http.StatusBadRequest)
		return
	}

	cameraID, ok := s.resolv(streamKey)
	if !ok {
		http.Error(w, "unknown stream key", http.StatusNotFound)
		return
	}

	answerSDP, sessionID, err := s.createSession(cameraID, streamKey, offerSDP)
	if err != nil {
		if errors.Is(err, errPublisherExists) {
			http.Error(w, "camera already has an active publisher", http.StatusConflict)
			return
		}
		logger.Warn("WHIP session creation failed", "camera_id", cameraID, "error", err)
		http.Error(w, "failed to negotiate WHIP session", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/sdp")
	// Relative Location per the WHIP draft — resolves against the request URL
	// and survives reverse proxies without reconstructing absolute URIs.
	w.Header().Set("Location", "/whip/"+streamKey+"/"+sessionID)
	w.WriteHeader(http.StatusCreated)
	w.Write(answerSDP)
}

// handleDelete implements DELETE /whip/{streamKey}/{session}.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	sessionID := pathParam(r, "session")
	if err := s.deleteSession(sessionID); err != nil {
		if errors.Is(err, errSessionNotFound) {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		http.Error(w, "failed to delete session", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// Stop tears down all sessions (service shutdown).
func (s *Server) Stop() {
	s.mu.Lock()
	s.stopped = true
	sessions := make([]*session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.sessions = make(map[string]*session)
	s.byCamera = make(map[string]*session)
	s.mu.Unlock()
	for _, sess := range sessions {
		sess.cancel()
		_ = sess.pc.Close()
	}
}

// activeSessions returns the number of live publisher sessions (tests).
func (s *Server) activeSessions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

var (
	errPublisherExists = errors.New("camera already has an active publisher")
	errSessionNotFound = errors.New("session not found")
)

// createSession runs the SDP exchange and wires the media callbacks. The
// session lifetime is bound to the SERVER's context (Start), never the HTTP
// request's — see Start.
func (s *Server) createSession(cameraID, streamKey string, offerSDP []byte) ([]byte, string, error) {
	ctx := s.runCtx
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil, "", fmt.Errorf("server stopped")
	}
	if _, exists := s.byCamera[cameraID]; exists {
		s.mu.Unlock()
		return nil, "", errPublisherExists
	}
	s.mu.Unlock()

	pc, err := s.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, "", err
	}

	sessCtx, cancel := context.WithCancel(ctx)
	sid := uuid.New().String()
	sess := &session{
		id:        sid,
		cameraID:  cameraID,
		streamKey: streamKey,
		pc:        pc,
		cancel:    cancel,
	}
	sess.lastPacket.Store(time.Now().UnixNano())

	hub := s.hubFn(cameraID)
	if hub == nil {
		hub = streamhub.New()
	}

	var naluCB NALUCallback
	if s.NALUProvider != nil {
		naluCB = s.NALUProvider(cameraID)
	}

	// Register before signaling so no track event can race the registration.
	s.mu.Lock()
	s.sessions[sid] = sess
	s.byCamera[cameraID] = sess
	s.mu.Unlock()

	teardown := func(reason string) {
		if s.deleteSession(sid) == nil {
			logger.Info("WHIP publisher disconnected", "camera_id", cameraID, "session_id", sid, "reason", reason)
		}
	}

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateFailed,
			webrtc.PeerConnectionStateClosed,
			webrtc.PeerConnectionStateDisconnected:
			teardown(state.String())
		}
	})

	pc.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		switch track.Kind() {
		case webrtc.RTPCodecTypeVideo:
			s.handleVideoTrack(sessCtx, sess, hub, naluCB, track)
		case webrtc.RTPCodecTypeAudio:
			s.handleAudioTrack(sessCtx, sess, hub, track)
		}
	})

	if err := pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  string(offerSDP),
	}); err != nil {
		s.dropFailedSession(sid, cancel, pc)
		return nil, "", err
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		s.dropFailedSession(sid, cancel, pc)
		return nil, "", err
	}

	gatherCtx, gatherCancel := context.WithTimeout(sessCtx, iceGatherTimeout)
	defer gatherCancel()
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		s.dropFailedSession(sid, cancel, pc)
		return nil, "", err
	}
	select {
	case <-gatherComplete:
	case <-gatherCtx.Done():
		logger.Warn("WHIP ICE gathering timed out, proceeding with gathered candidates", "camera_id", cameraID)
	}

	// Publisher is live from the NVR's perspective (media may take a moment).
	if s.onConn != nil {
		s.onConn(cameraID, hub)
	}
	go s.idleWatchdog(sessCtx, sess)

	logger.Info("WHIP publisher connected", "camera_id", cameraID, "session_id", sid)
	return []byte(pc.LocalDescription().SDP), sid, nil
}

// dropFailedSession cleans up a session whose SDP negotiation failed before
// it ever went live — the not-found result from deleteSession is expected
// (the disconnect hook must not fire for a publisher that never streamed).
func (s *Server) dropFailedSession(sid string, cancel context.CancelFunc, pc *webrtc.PeerConnection) {
	_ = s.deleteSession(sid)
	cancel()
	_ = pc.Close()
}

// deleteSession removes the session and fires the disconnect hook exactly once.
func (s *Server) deleteSession(sessionID string) error {
	s.mu.Lock()
	sess, ok := s.sessions[sessionID]
	if !ok {
		s.mu.Unlock()
		return errSessionNotFound
	}
	delete(s.sessions, sessionID)
	if cur, still := s.byCamera[sess.cameraID]; still && cur.id == sessionID {
		delete(s.byCamera, sess.cameraID)
	}
	s.mu.Unlock()

	sess.cancel()
	_ = sess.pc.Close()
	if s.onDisc != nil {
		s.onDisc(sess.cameraID)
	}
	return nil
}

// idleWatchdog tears the session down when no RTP arrives within the
// no-media window (never-connected publisher) or the idle window (stalled
// stream the PeerConnection state machine didn't catch).
func (s *Server) idleWatchdog(ctx context.Context, sess *session) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			since := time.Since(time.Unix(0, sess.lastPacket.Load()))
			if since > idleTimeout || since > noMediaTimeout {
				_ = s.deleteSession(sess.id)
				logger.Warn("WHIP session torn down — no RTP",
					"camera_id", sess.cameraID, "session_id", sess.id, "silent_for", since.Round(time.Second))
				return
			}
		}
	}
}

// pathParam reads a chi URL param.
func pathParam(r *http.Request, key string) string {
	return chi.URLParam(r, key)
}
