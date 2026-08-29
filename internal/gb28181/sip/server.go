package sip

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	gosip "github.com/ghettovoice/gosip"
	"github.com/ghettovoice/gosip/log"
	"github.com/ghettovoice/gosip/sip"
	"github.com/mickeyzzc/gb28181-go/manscdp"
	"github.com/mickeyzzc/gb28181-go/platform"
)

// SIP status codes emitted by this server. gosip's sip package defines no
// named constants, so they are declared here.
const (
	statusOK           sip.StatusCode = 200
	statusBadRequest   sip.StatusCode = 400
	statusUnauthorized sip.StatusCode = 401
	statusForbidden    sip.StatusCode = 403
	statusBusyHere     sip.StatusCode = 486
)

// errDeviceBusy marks a 486 Busy Here INVITE rejection (usually a stale
// dialog on single-stream devices) — triggers a dialog-reset BYE.
var errDeviceBusy = errors.New("device busy (stale dialog)")

// inviteResponseTimeout bounds how long InviteChannel waits for the device's
// answer to a SIP INVITE before tearing the half-open session down.
const inviteResponseTimeout = 32 * time.Second

// speculativeAckDelay is how long to wait for a transaction-matched INVITE
// response before sending a speculative ACK anyway (devices with Via-less
// responses deadlock without it).
const speculativeAckDelay = 2500 * time.Millisecond

// Session-watchdog tuning: recycle a session when the stream stalls for
// streamStaleAfter or no keyframe has been seen for idrStaleAfter (checked
// every idrWatchInterval). idrStaleAfter must comfortably exceed real
// devices' GOP lengths — IPCs with 2-4 minute GOPs are common, and a
// threshold inside the GOP recycles healthy streams forever (observed: a
// ~3min-GOP source recycled every ~80s, breaking every live view each cycle).
const (
	idrStaleAfter    = 10 * time.Minute
	streamStaleAfter = 45 * time.Second
	idrWatchInterval = 15 * time.Second
)

// CameraEnroller auto-creates a camera in the main cameras list when a GB28181
// device first registers. The camera manager implements this; the SIP server
// calls it from handleRegister so GB28181 cameras appear alongside ONVIF/RTSP
// cameras without manual setup — matching the ONVIF auto-discover pattern.
type CameraEnroller interface {
	// EnsureGB28181Camera creates a camera bound to the device/channel pair
	// (idempotent). name is the human-readable channel name (may be empty).
	// sourceIP is the device's SIP source host ("" unknown) — auto-enroll is
	// skipped when another camera already streams from that IP (dual-protocol
	// dedup; manual creation bypasses it).
	EnsureGB28181Camera(deviceID, channelID, name, sourceIP string) error
	// GB28181CameraIDByChannel resolves the MiBee camera ID bound to a
	// device/channel pair, independent of the camera-ID naming convention.
	GB28181CameraIDByChannel(deviceID, channelID string) (string, bool)
	// GB28181NALUWriter returns the recorder's AU callback for a camera, or
	// nil if the camera doesn't exist or isn't a GB28181 camera. Used to
	// bridge RTP receiver output directly into the recorder pipeline.
	GB28181NALUWriter(cameraID string) func(au [][]byte, ptsTicks int64, isIDR bool)
	// GB28181AudioWriter returns the recorder's audio-frame callback for a
	// camera, or nil. Bridges demuxed PS audio (G.711/AAC) into the recorder
	// for MP4 muxing and live hub broadcast.
	GB28181AudioWriter(cameraID string) func(codec string, data, config []byte, ptsTicks int64, samples int)
	// OnGB28181Invite transitions the recorder to Recording state.
	OnGB28181Invite(cameraID string)
	// OnGB28181Bye transitions the recorder to Reconnecting state.
	OnGB28181Bye(cameraID string)
	// NewGB28181PlaybackSink creates a sink that muxes a fetched device
	// recording into the normal recordings pipeline for cameraID — used by
	// playback INVITEs (#337). The sink must also implement Stopper so the
	// session teardown finalizes the recording.
	NewGB28181PlaybackSink(cameraID string) (platform.AUWriter, error)
	// GB28181PlaybackAudioWriter returns the playback sink's audio writer
	// (nil when the camera has audio disabled).
	GB28181PlaybackAudioWriter(cameraID string) func(codec string, data, config []byte, ptsTicks int64, samples int)
	// UpdateGB28181DeviceMeta backfills Brand/Model on the cameras bound to
	// a device from its DeviceInfo response (empty fields only).
	UpdateGB28181DeviceMeta(deviceID, manufacturer, model string) error
	// ArchiveGB28181Camera soft-removes the camera auto-enrolled for a
	// channel (device-self pseudo-channel superseded by a real catalog,
	// #352). No-op when no camera is bound to the channel.
	ArchiveGB28181Camera(deviceID, channelID string) error
	// GB28181RecordingWanted reports whether the camera bound to a channel
	// wants recording (alarm linkage leaves those sessions to the record
	// loop, #355).
	GB28181RecordingWanted(deviceID, channelID string) bool
	// GB28181SubChannelID returns the persisted sub-channel code bound to a
	// main channel's camera ("" = none), #560.
	GB28181SubChannelID(deviceID, channelID string) string
	// SetGB28181SubChannel persists the probed sub-channel code on the
	// camera bound to a main channel (fill-once — never overwrites a
	// non-empty value), #560.
	SetGB28181SubChannel(deviceID, channelID, subChannelID string) error
}

// inviteDialog remembers the INVITE request and its final response for an
// active channel session so an in-dialog SIP BYE can be built later.
type inviteDialog struct {
	req  sip.Request
	resp sip.Response
}

// Server implements the GB/T 28181 SIP platform (UAS) side. It owns the gosip
// SIP stack lifecycle: UDP/TCP listening, REGISTER digest authentication, and
// keepalive/catalog/device-info MESSAGE handling. Media sessions (INVITE/BYE)
// are delegated to the hooks installed by the session manager.
type Server struct {
	cfg        config.GB28181ServerConfig
	deviceMgr  *platform.DeviceManager
	sessionMgr *platform.SessionManager
	db         *storage.DB // nil in tests

	gosipSrv gosip.Server
	cancel   context.CancelFunc

	mu      sync.Mutex
	started bool

	onInvite    func(deviceID, channelID string) // Hook for SessionManager
	onBye       func(deviceID, channelID string) // Hook for SessionManager
	camEnroller CameraEnroller                   // Auto-create camera on first REGISTER

	dialogs map[string]*inviteDialog // channelID -> active INVITE dialog

	// Playback fetch state (#337): one per channel, separate from live
	// sessions/dialogs so live streaming and device-recording fetching can
	// run concurrently on the same channel.
	pbMu          sync.Mutex
	playbacks     map[string]*playbackState // channelID -> running fetch
	pbDialogs     map[string]*inviteDialog  // channelID -> playback INVITE dialog
	recMu         sync.Mutex
	recordQueries map[string]*pendingRecordQuery // deviceID|sn -> pending RecordInfo

	// Subscription state (#341): SUBSCRIBE Catalog/Alarm/MobilePosition,
	// refresh deadlines, and the alarm / mobile-position rings.
	subMu         sync.Mutex
	subscriptions map[string]*gbSubscription           // deviceID|subject -> active SUBSCRIBE
	alarmRing     map[string][]event.GB28181AlarmEvent // deviceID -> latest-first alarms
	posRing       map[string][]platform.GBPosition     // deviceID -> latest-first positions
	eventBus      *event.EventBus

	// Talk sessions (#341): channelID -> active voice intercom.
	talkMu sync.Mutex
	talks  map[string]*talkSession
	// alarmLinkage drives alarm-triggered streaming (#355).
	alarmLinkage *alarmLinkage
	talkSeq      int

	perDeviceMu map[string]*sync.Mutex // serialize SIP handling per device

	// gbLoc pins the zone for naive GB/T 28181 device-clock timestamps
	// (RecordInfo query formatting). nil → time.Local.
	gbLoc atomic.Pointer[time.Location]
}

// SetGBTimezone pins the naive-clock zone for GB/T 28181 timestamps — set it
// when the host's system zone differs from the devices' (e.g. a UTC container
// fronting CST cameras), so record query windows align with device clocks.
func (s *Server) SetGBTimezone(loc *time.Location) {
	if loc != nil {
		s.gbLoc.Store(loc)
	}
}

func (s *Server) gbTZ() *time.Location {
	if loc := s.gbLoc.Load(); loc != nil {
		return loc
	}
	return time.Local
}

// NewServer creates a GB28181 SIP server bound to the given config. The db
// parameter persists device registrations and catalog data so the REST API
// (which reads from the DB) reflects live SIP state; pass nil to skip
// persistence (test-only).
func NewServer(cfg config.GB28181ServerConfig, deviceMgr *platform.DeviceManager, sessionMgr *platform.SessionManager, db *storage.DB) *Server {
	s := &Server{
		cfg:           cfg,
		deviceMgr:     deviceMgr,
		sessionMgr:    sessionMgr,
		db:            db,
		dialogs:       make(map[string]*inviteDialog),
		playbacks:     make(map[string]*playbackState),
		pbDialogs:     make(map[string]*inviteDialog),
		recordQueries: make(map[string]*pendingRecordQuery),
		subscriptions: make(map[string]*gbSubscription),
		alarmRing:     make(map[string][]event.GB28181AlarmEvent),
		posRing:       make(map[string][]platform.GBPosition),
		talks:         make(map[string]*talkSession),
		perDeviceMu:   make(map[string]*sync.Mutex),
	}
	s.alarmLinkage = newAlarmLinkage(
		s.InviteChannel, s.ByeChannel,
		func(channelID string) bool {
			rcv := s.sessionMgr.GetReceiver(channelID)
			return rcv != nil && rcv.Running()
		},
		func(deviceID, channelID string) bool {
			if enrol := s.enroller(); enrol != nil {
				return enrol.GB28181RecordingWanted(deviceID, channelID)
			}
			return false
		},
	)
	// Locally-stopped sessions transmit a SIP BYE to the device before the
	// RTP port is recycled, so stale streams never poison recycled ports.
	sessionMgr.SetByeSender(s.sendByeForChannel)
	// First RTP media confirms a dialog end-to-end — even when the device's
	// INVITE response could not be transaction-matched (missing Via header).
	sessionMgr.SetFirstRTPHook(s.onFirstRTP)
	// Media transport + TCP framing resolve live from the startup config
	// snapshot (udp | tcp-passive | tcp-active — GB28181 media_transport).
	sessionMgr.SetMediaTransport(func() string { return s.cfg.MediaTransport })
	sessionMgr.SetTCPFraming(func() string { return s.cfg.TCPFraming })
	// Playback fetch sessions BYE on their own dialog store.
	sessionMgr.SetPlaybackByeSender(s.sendByeForPlayback)
	return s
}

// onFirstRTP confirms a session as playing when its first media packet
// arrives and flips the bound camera's recorder to Recording.
func (s *Server) onFirstRTP(channelID string) {
	slog.Info("gb28181: first RTP received — session confirmed", "channel", channelID)
	_ = s.sessionMgr.MarkPlaying(channelID)
	deviceID := s.deviceOfChannel(channelID)
	enrol := s.enroller()
	if enrol == nil {
		return
	}
	if cameraID, ok := enrol.GB28181CameraIDByChannel(deviceID, channelID); ok {
		enrol.OnGB28181Invite(cameraID)
	}
}

// Name returns the service name (pkg/app.Service interface).
func (s *Server) Name() string {
	return "gb28181"
}

// Start launches the SIP stack. It is idempotent and returns promptly after
// the listeners are up.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil
	}
	srvCtx, cancel := context.WithCancel(ctx)
	s.started = true
	s.cancel = cancel
	s.mu.Unlock()

	host, port, err := parseSIPListen(s.cfg.SIPListen)
	if err != nil {
		s.startFailed(cancel)
		return err
	}
	// An empty host must mean "all interfaces": this gosip version folds an
	// empty Host into a loopback-only bind, silently making the documented
	// ":5060" default unreachable for devices (observed on a live server).
	if host == "" {
		host = "0.0.0.0"
	}
	// gosip panics when Host is set to a non-IP value (e.g. a 20-digit
	// GB28181 server ID).
	if net.ParseIP(host) == nil {
		s.startFailed(cancel)
		return fmt.Errorf("gb28181: invalid SIP listen host %q", host)
	}

	logger := slogAdapter{slog.Default().With("component", "gb28181_sip")}
	srv := gosip.NewServer(gosip.ServerConfig{
		Host:      host,
		UserAgent: "MiBeeNvr-GB28181/1.0",
	}, nil, nil, logger)
	s.gosipSrv = srv

	_ = srv.OnRequest(sip.REGISTER, s.handleRegister)
	_ = srv.OnRequest(sip.MESSAGE, s.handleMessage)
	_ = srv.OnRequest(sip.INVITE, s.handleInvite)
	_ = srv.OnRequest(sip.BYE, s.handleBye)
	// INFO carries device→platform media notifications: a MANSRTSP MediaStatus
	// body ("playback/download finished") ends the active fetch (#378).
	_ = srv.OnRequest(sip.INFO, s.handleInfo)
	// NOTIFY delivers subscription payloads (catalog changes, alarms,
	// mobile positions) after our SUBSCRIBE (#341).
	_ = srv.OnRequest(sip.NOTIFY, s.handleNotify)
	// Some devices (and interop probes) use OPTIONS for liveness; answering
	// it with our method set avoids 405-driven fallbacks on older firmwares.
	_ = srv.OnRequest(sip.OPTIONS, s.handleOptions)

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	if err := srv.Listen("UDP", addr); err != nil {
		s.startFailed(cancel)
		return fmt.Errorf("gb28181: listen UDP %s: %w", addr, err)
	}
	// SIP-over-TCP signaling listener (gb28181.sip_transport). UDP always
	// stays up — devices pick whichever transport they speak.
	if s.cfg.SIPTransport == "tcp" || s.cfg.TCPMode {
		if err := srv.Listen("TCP", addr); err != nil {
			s.startFailed(cancel)
			return fmt.Errorf("gb28181: listen TCP %s: %w", addr, err)
		}
	}
	s.deviceMgr.Start(srvCtx)
	// Periodic catalog refresh (cfg.CatalogInterval, default 30m) keeps
	// channel lists current: newly added channels on a device get discovered
	// and enrolled as cameras without waiting for its next REGISTER cycle.
	go s.catalogLoop(srvCtx)
	// Subscription refresh (SUBSCRIBE Catalog/Alarm/MobilePosition) renews
	// before expiry so device-initiated pushes keep flowing (#341).
	go s.subscribeLoop(srvCtx)
	return nil
}

// catalogLoop queries every online device's catalog on the configured
// interval until ctx is cancelled (Stop cancels the server context).
func (s *Server) catalogLoop(ctx context.Context) {
	interval := 30 * time.Minute
	if d, err := time.ParseDuration(s.cfg.CatalogInterval); err == nil && d > 0 {
		interval = d
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, dev := range s.deviceMgr.AllDevices() {
				if dev.Status.Load() != platform.DeviceOnline {
					continue
				}
				if err := s.requestCatalog(dev.ID); err != nil {
					slog.Debug("gb28181: periodic catalog refresh", "device", dev.ID, "error", err)
				}
			}
		}
	}
}

// startFailed rolls back the started state so a later Start can retry after
// an aborted startup (otherwise the server would claim to be running with a
// nil gosipSrv).
func (s *Server) startFailed(cancel context.CancelFunc) {
	cancel()
	s.mu.Lock()
	s.started = false
	s.cancel = nil
	s.gosipSrv = nil
	s.mu.Unlock()
}

// Stop shuts down the SIP stack. It is idempotent.
func (s *Server) Stop() error {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	s.started = false
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	srv := s.gosipSrv
	s.gosipSrv = nil
	s.mu.Unlock()

	s.alarmLinkage.Stop()

	if srv != nil {
		srv.Shutdown()
	}
	s.deviceMgr.Stop()
	return nil
}

// SetInviteHook installs the INVITE handler used by the session manager.
func (s *Server) SetInviteHook(hook func(deviceID, channelID string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onInvite = hook
}

// SetByeHook installs the BYE handler used by the session manager.
func (s *Server) SetByeHook(hook func(deviceID, channelID string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onBye = hook
}

// SetCameraEnroller wires the auto-camera-creation callback. When a device
// first registers, the server calls EnsureGB28181Camera so a camera entry
// appears in the main Cameras list — matching ONVIF auto-discover behavior.
func (s *Server) SetCameraEnroller(enroller CameraEnroller) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.camEnroller = enroller
}

// SetEventBus wires the publish bus for GB28181 alarm events (SSE surface).
// Optional — without a bus the alarm ring + logs still work.
func (s *Server) SetEventBus(bus *event.EventBus) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	s.eventBus = bus
}

// enroller snapshots the camera enroller under lock.
func (s *Server) enroller() CameraEnroller {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.camEnroller
}

// SendMessage sends a SIP MESSAGE request carrying the given MANSCDP body to
// deviceID. It implements platform.MessageSender so the PTZ controller can push
// DeviceControl commands to a registered device.
func (s *Server) SendMessage(deviceID string, body []byte) error {
	s.mu.Lock()
	srv := s.gosipSrv
	s.mu.Unlock()
	if srv == nil {
		return fmt.Errorf("gb28181: SIP server not started")
	}

	dev, ok := s.deviceMgr.Device(deviceID)
	if !ok {
		return fmt.Errorf("gb28181: device %q not registered", deviceID)
	}
	dev.Mu.RLock()
	netAddr := dev.NetAddr
	dev.Mu.RUnlock()
	devHost, devPortStr, err := net.SplitHostPort(netAddr)
	if err != nil {
		return fmt.Errorf("gb28181: invalid device address %q: %w", netAddr, err)
	}
	devPort, err := strconv.Atoi(devPortStr)
	if err != nil {
		return fmt.Errorf("gb28181: invalid device port %q: %w", devPortStr, err)
	}
	portVal := sip.Port(devPort)

	serverHost := s.localIPFor(netAddr)

	from := &sip.Address{
		DisplayName: sip.String{Str: s.cfg.ServerID},
		Uri: &sip.SipUri{
			FUser: sip.String{Str: s.cfg.ServerID},
			FHost: serverHost,
		},
	}
	to := &sip.Address{
		DisplayName: sip.String{Str: deviceID},
		Uri: &sip.SipUri{
			FUser: sip.String{Str: deviceID},
			FHost: devHost,
			FPort: &portVal,
		},
	}
	recipient := &sip.SipUri{
		FUser: sip.String{Str: deviceID},
		FHost: devHost,
		FPort: &portVal,
	}

	req, err := s.buildRequest(sip.MESSAGE, serverHost, from, to, recipient, "", "Application/MANSCDP+xml", string(body))
	if err != nil {
		return err
	}

	if _, err := srv.Request(req); err != nil {
		return fmt.Errorf("gb28181: send MESSAGE to %s: %w", deviceID, err)
	}
	return nil
}

// buildRequest assembles a request with the headers GB28181 devices expect:
// Contact (mandatory per RFC 3261 §8.1.1.8 — devices route in-dialog BYE on
// it), Via carrying the platform's SIP port (not the default 5060), and a
// fresh branch/CSeq. contentType/body are set when non-empty; subject adds
// the GB28181 Subject header ("<channelID>:<ssrc>,<serverID>:0" on INVITE).
func (s *Server) buildRequest(method sip.RequestMethod, serverHost string, from, to *sip.Address, recipient *sip.SipUri, subject, contentType, body string) (sip.Request, error) {
	_, sipPort, err := parseSIPListen(s.cfg.SIPListen)
	if err != nil {
		sipPort = 5060
	}
	portVal := sip.Port(sipPort)

	rb := sip.NewRequestBuilder()
	rb.SetMethod(method)
	rb.SetFrom(from)
	rb.SetTo(to)
	rb.SetRecipient(recipient)
	rb.SetHost(serverHost)
	rb.SetContact(&sip.Address{
		Uri: &sip.SipUri{
			FUser: sip.String{Str: s.cfg.ServerID},
			FHost: serverHost,
			FPort: &portVal,
		},
	})
	rb.AddVia(&sip.ViaHop{
		Host: serverHost,
		Port: &portVal,
		Params: sip.NewParams().
			Add("branch", sip.String{Str: sip.GenerateBranch()}).
			Add("rport", sip.String{}),
	})
	rb.SetSeqNo(1)
	if subject != "" {
		rb.AddHeader(&sip.GenericHeader{HeaderName: "Subject", Contents: subject})
	}
	if contentType != "" {
		ct := sip.ContentType(contentType)
		rb.SetContentType(&ct)
	}
	if body != "" {
		rb.SetBody(body)
	}
	return rb.Build()
}

// InviteChannel sends a SIP INVITE to channelID on deviceID, starting a live
// media session. It allocates an RTP receive port via the SessionManager,
// builds a GB28181 SDP offer (s=Play, PS/90000, recvonly), sends the INVITE,
// and completes the 3-way handshake: on a 2xx answer it transmits the ACK and
// marks the session playing; on failure/timeout it tears the half-open
// session down (port recycled, recorder rolled back).
//
// Idempotent: when the channel already has an active session it returns nil
// without sending anything (auto-INVITE fires on every device re-REGISTER).
func (s *Server) InviteChannel(deviceID, channelID string) error {
	s.mu.Lock()
	srv := s.gosipSrv
	s.mu.Unlock()
	if srv == nil {
		return fmt.Errorf("gb28181: SIP server not started")
	}

	// Idempotency: an active receiver means the session is already up.
	if rcv := s.sessionMgr.GetReceiver(channelID); rcv != nil {
		return nil
	}

	dev, ok := s.deviceMgr.Device(deviceID)
	if !ok {
		return fmt.Errorf("gb28181: device %q not registered", deviceID)
	}
	ch, ok := s.deviceMgr.FindChannel(deviceID, channelID)
	if !ok {
		return fmt.Errorf("gb28181: channel %q not found on device %q", channelID, deviceID)
	}

	dev.Mu.RLock()
	netAddr := dev.NetAddr
	dev.Mu.RUnlock()

	serverHost := s.localIPFor(netAddr)

	// Look up the recorder's AU callback so RTP frames flow directly into
	// the recorder pipeline (codec detection, MP4 segments, hub broadcast).
	// The lookup is by the camera's configured channel binding — NOT a
	// "gb-<channelID>" string guess (that only matches auto-enrolled
	// cameras; manually created cameras have arbitrary IDs).
	var onAU func(au [][]byte, ptsTicks int64, isIDR bool)
	var onAudio platform.AudioFrameHandler
	var cameraID string
	if enrol := s.enroller(); enrol != nil {
		if id, ok := enrol.GB28181CameraIDByChannel(deviceID, channelID); ok {
			cameraID = id
			onAU = enrol.GB28181NALUWriter(cameraID)
			if w := enrol.GB28181AudioWriter(cameraID); w != nil {
				onAudio = func(frame platform.AudioFrame) {
					w(frame.Codec, frame.Data, frame.Config, frame.PTSTicks, frame.Samples)
				}
			}
		}
	}

	// A bound camera whose recorder isn't running must not be INVITE'd: the
	// media would feed an orphan hub nobody consumes, and since the receiver
	// keeps draining packets the stall watchdog never fires — the session
	// wedges with zero frames ever reaching the recorder. That is exactly the
	// boot race where a lower platform hammering re-REGISTER gets its
	// catalog-driven INVITE in before camera-manager startup finishes. Defer
	// instead: the recorder-start auto-INVITE establishes the session (and
	// recycles any wedged one) once the recorder exists.
	if cameraID != "" && onAU == nil {
		return fmt.Errorf("gb28181: recorder for camera %q not running — deferring INVITE for channel %s", cameraID, channelID)
	}

	if err := s.inviteCore(deviceID, ch, netAddr, serverHost, onAU, onAudio); err != nil {
		return err
	}

	_ = s.sessionMgr.MarkPlaying(channelID)
	if cameraID != "" {
		if enrol := s.enroller(); enrol != nil {
			enrol.OnGB28181Invite(cameraID)
		}
	}
	// Session watchdog: some device firmwares emit SPS/PPS+IDR only at
	// stream start and never again (recordings open segments on IDR), and
	// some stall their stream mid-session — a zombie receiver then blocks
	// every future auto-INVITE. Recycling the session forces a fresh stream
	// (and a fresh IDR) from the device.
	go s.watchSession(deviceID, channelID)
	slog.Info("gb28181: INVITE answered, ACK sent", "channel", channelID, "device", deviceID)
	return nil
}

// inviteCore establishes one media session for ch: allocates the RTP port and
// receiver (building the SDP answer), sends the SIP INVITE, completes the
// 2xx/ACK handshake and records the dialog. It is the shared machinery of the
// recorder-oriented InviteChannel and the sub-stream puller's InviteSubChannel
// (#560) — the caller supplies the AU/audio callbacks and owns post-answer
// policy (watchdog, recorder notification, teardown).
func (s *Server) inviteCore(deviceID string, ch *platform.Channel, netAddr, serverHost string, onAU func(au [][]byte, ptsTicks int64, isIDR bool), onAudio platform.AudioFrameHandler) error {
	channelID := ch.ID
	sdp, err := s.sessionMgr.Invite(ch, serverHost, netAddr, nil, onAU, onAudio)
	if err != nil {
		return fmt.Errorf("gb28181: session setup for %s: %w", channelID, err)
	}

	devHost, devPortStr, err := net.SplitHostPort(netAddr)
	if err != nil {
		_ = s.sessionMgr.Bye(channelID)
		return fmt.Errorf("gb28181: invalid device address %q: %w", netAddr, err)
	}
	devPort, _ := strconv.Atoi(devPortStr)
	portVal := sip.Port(devPort)

	from := &sip.Address{
		DisplayName: sip.String{Str: s.cfg.ServerID},
		Uri:         &sip.SipUri{FUser: sip.String{Str: s.cfg.ServerID}, FHost: serverHost},
	}
	to := &sip.Address{
		DisplayName: sip.String{Str: channelID},
		Uri:         &sip.SipUri{FUser: sip.String{Str: channelID}, FHost: devHost, FPort: &portVal},
	}
	recipient := &sip.SipUri{FUser: sip.String{Str: channelID}, FHost: devHost, FPort: &portVal}

	// GB28181 convention: Subject "<channelID>:<ssrc>,<serverID>:0".
	subject := fmt.Sprintf("%s:%s,%s:0", channelID, sdpSSRC(sdp), s.cfg.ServerID)

	req, err := s.buildRequest(sip.INVITE, serverHost, from, to, recipient, subject, "application/sdp", string(sdp))
	if err != nil {
		_ = s.sessionMgr.Bye(channelID)
		return fmt.Errorf("gb28181: build INVITE request: %w", err)
	}

	s.mu.Lock()
	srv2 := s.gosipSrv
	s.mu.Unlock()
	if srv2 == nil {
		_ = s.sessionMgr.Bye(channelID)
		return fmt.Errorf("gb28181: SIP server not started")
	}

	tx, err := srv2.Request(req)
	if err != nil {
		_ = s.sessionMgr.Bye(channelID)
		return fmt.Errorf("gb28181: send INVITE to %s: %w", channelID, err)
	}

	resp, err := s.awaitInviteAnswer(srv2, tx, req)
	if err != nil {
		_ = s.sessionMgr.Bye(channelID)
		// 486 Busy usually means the device still holds a dialog from before
		// an NVR restart (single-stream firmwares never give it up on their
		// own). Send a dialog-reset BYE — loosely-keyed firmware accepts it
		// and frees the stream for the next auto-INVITE cycle.
		if errors.Is(err, errDeviceBusy) {
			s.sendDialogReset(deviceID, channelID, netAddr)
		}
		return fmt.Errorf("gb28181: INVITE to %s: %w", channelID, err)
	}
	if resp != nil {
		// Complete the 3-way handshake with the matched 2xx: without the ACK
		// most GB28181 devices never start the RTP stream (RFC 3261 §13.2.2.4).
		ack := sip.NewAckRequest("", req, resp, "", nil)
		if err := srv2.Send(ack); err != nil {
			slog.Warn("gb28181: send ACK failed", "channel", channelID, "error", err)
		}
		s.mu.Lock()
		s.dialogs[channelID] = &inviteDialog{req: req, resp: resp}
		s.mu.Unlock()
		// Seed the no-PSM audio fallback from the answer SDP: devices that
		// mux audio into the PS stream without ever sending a Program Stream
		// Map are otherwise undecodable (the codec is unknowable from raw
		// G.711 bytes). The hint yields to any later PSM declaration.
		if rc := s.sessionMgr.GetReceiver(channelID); rc != nil {
			if codec := sdpAudioCodec([]byte(resp.Body())); codec != "" {
				rc.SetAudioCodecHint(codec)
			}
		}
		// tcp-active: the device's answer SDP carries its media address —
		// dial it now that the dialog is confirmed.
		if s.cfg.MediaTransport == platform.MediaTCPActive {
			if err := s.sessionMgr.ConnectActiveTCP(channelID, []byte(resp.Body())); err != nil {
				slog.Warn("gb28181: tcp-active media connect failed", "channel", channelID, "error", err)
				_ = s.sessionMgr.Bye(channelID)
				return fmt.Errorf("gb28181: INVITE to %s: %w", channelID, err)
			}
		}
	} else {
		// No transaction-matched 2xx, but RTP arrived after the speculative
		// ACK — the dialog demonstrably works (device streams); keep it.
		slog.Info("gb28181: INVITE confirmed via first RTP (non-compliant device response)", "channel", channelID, "device", deviceID)
	}

	slog.Info("gb28181: INVITE answered, ACK sent", "channel", channelID, "device", deviceID)
	return nil
}

// watchSession monitors an established session for as long as it lives and
// recycles it (BYE + re-INVITE) when the stream goes stale:
//
//   - streamStaleAfter without any RTP packet → the device stalled (firmware
//     hangs mid-dialog); a zombie receiver would otherwise block every future
//     auto-INVITE via the idempotency check;
//   - idrStaleAfter without a keyframe → recordings can never open a new
//     segment (they start on IDR) and live view never syncs.
//
// Each recycle re-INVITEs, which starts a fresh watchdog for the new session.
func (s *Server) watchSession(deviceID, channelID string) {
	for {
		time.Sleep(idrWatchInterval)
		rcv := s.sessionMgr.GetReceiver(channelID)
		if rcv == nil {
			return // session replaced or gone
		}
		if !rcv.HasReceivedRTP() {
			continue // stream not started yet (or device is quiet)
		}
		var why string
		sincePkt := rcv.SinceLastPacket()
		sinceIDR, hasIDR := rcv.SinceLastIDR()
		switch {
		case sincePkt > streamStaleAfter:
			why = "stream stalled"
		case !hasIDR || sinceIDR > idrStaleAfter:
			why = "no keyframe"
		default:
			continue
		}
		slog.Warn("gb28181: recycling stale session",
			"channel", channelID, "device", deviceID, "reason", why,
			"since_last_packet", sincePkt.Round(time.Second),
			"since_last_idr", sinceIDR.Round(time.Second))
		_ = s.ByeChannel(deviceID, channelID)
		time.Sleep(2 * time.Second)
		if err := s.InviteChannel(deviceID, channelID); err != nil {
			slog.Warn("gb28181: re-INVITE after session recycle failed", "channel", channelID, "error", err)
			return
		}
		return // the new session's watchdog takes over
	}
}

// awaitInviteAnswer waits for the device's answer to an INVITE. Returns the
// matched 2xx response, or nil when no compliant response arrived BUT RTP
// media started anyway (confirmed via the receiver), or an error when the
// dialog definitively failed (matched non-2xx / no response and no media).
//
// Compatibility: some device firmwares answer 200 OK without a Via header —
// unmatchable by any SIP transaction layer. At speculativeAckDelay a
// speculative ACK (built from the INVITE, no To-tag) is sent anyway: for
// compliant stacks a stray/duplicate ACK is ignored, for loose firmware it
// completes the handshake and the stream starts — confirmed by first RTP.
func (s *Server) awaitInviteAnswer(srv gosip.Server, tx sip.ClientTransaction, inviteReq sip.Request) (sip.Response, error) {
	responses := tx.Responses()
	deadline := time.NewTimer(inviteResponseTimeout)
	defer deadline.Stop()
	speculative := time.NewTimer(speculativeAckDelay)
	defer speculative.Stop()

	for {
		select {
		case resp, ok := <-responses:
			if !ok {
				return nil, s.checkRTPConfirmed(tx)
			}
			if resp.IsSuccess() {
				return resp, nil
			}
			if !resp.IsProvisional() {
				if resp.StatusCode() == statusBusyHere {
					return nil, fmt.Errorf("device rejected: status %d (%s): %w", resp.StatusCode(), resp.Reason(), errDeviceBusy)
				}
				return nil, fmt.Errorf("device rejected: status %d (%s)", resp.StatusCode(), resp.Reason())
			}
			// 1xx provisional — keep waiting.

		case <-speculative.C:
			speculative.Stop()
			if err := srv.Send(buildSpeculativeAck(inviteReq)); err != nil {
				slog.Debug("gb28181: speculative ACK send failed", "error", err)
			}

		case <-deadline.C:
			return nil, s.checkRTPConfirmed(tx)
		}
	}
}

// checkRTPConfirmed treats "media started" as dialog success, else a timeout.
func (s *Server) checkRTPConfirmed(tx sip.ClientTransaction) error {
	_ = tx.Cancel()
	return fmt.Errorf("unanswered (no matched response and no RTP media)")
}

// buildSpeculativeAck constructs an ACK for an INVITE without a matched
// response: identical dialog identifiers (Via/From/To/Call-ID), CSeq with
// method ACK. Compliant stacks drop it as a stray re-ACK; loose firmware
// that keyed the dialog on Call-ID accepts it and starts streaming.
func buildSpeculativeAck(inviteReq sip.Request) sip.Request {
	ack := sip.NewRequest(
		sip.NextMessageID(),
		sip.ACK,
		inviteReq.Recipient(),
		inviteReq.SipVersion(),
		[]sip.Header{},
		"",
		inviteReq.Fields(),
	)
	for _, name := range []string{"Via", "From", "To", "Call-ID", "Max-Forwards", "Route"} {
		sip.CopyHeaders(name, inviteReq, ack)
	}
	if cseq, ok := inviteReq.CSeq(); ok {
		ack.AppendHeader(&sip.CSeq{SeqNo: cseq.SeqNo, MethodName: sip.ACK})
	}
	return ack
}

// ByeChannel stops a channel's media session: transmits an in-dialog SIP BYE
// to the device, tears down the local receiver, and transitions the bound
// camera's recorder back to Reconnecting.
func (s *Server) ByeChannel(deviceID, channelID string) error {
	// SessionManager.Bye invokes the registered bye sender (sendByeForChannel)
	// before local teardown.
	if err := s.sessionMgr.Bye(channelID); err != nil {
		return err
	}
	s.notifyCameraBye(deviceID, channelID)
	return nil
}

// sendDialogReset transmits a best-effort BYE with fresh dialog identifiers
// for a channel the platform has no stored dialog for (486 recovery). Loose
// firmware keys BYE on the From/To users and accepts it; compliant stacks
// that track dialogs properly reply 481 and are unaffected.
func (s *Server) sendDialogReset(deviceID, channelID, deviceAddr string) {
	s.mu.Lock()
	srv := s.gosipSrv
	s.mu.Unlock()
	if srv == nil {
		return
	}
	devHost, devPortStr, err := net.SplitHostPort(deviceAddr)
	if err != nil {
		return
	}
	devPort, _ := strconv.Atoi(devPortStr)
	portVal := sip.Port(devPort)
	serverHost := s.localIPFor(deviceAddr)

	from := &sip.Address{Uri: &sip.SipUri{FUser: sip.String{Str: s.cfg.ServerID}, FHost: serverHost}}
	to := &sip.Address{Uri: &sip.SipUri{FUser: sip.String{Str: channelID}, FHost: devHost, FPort: &portVal}}
	recipient := &sip.SipUri{FUser: sip.String{Str: channelID}, FHost: devHost, FPort: &portVal}
	bye, err := s.buildRequest(sip.BYE, serverHost, from, to, recipient, "", "", "")
	if err != nil {
		return
	}
	if _, err := srv.Request(bye); err != nil {
		slog.Debug("gb28181: dialog-reset BYE send failed", "channel", channelID, "error", err)
		return
	}
	slog.Info("gb28181: dialog-reset BYE sent (486 recovery)", "channel", channelID, "device", deviceID)
}

// ByeAllSessions sends a SIP BYE for every active media session (graceful
// shutdown). Devices like mibee-eye keep streaming to the old port AND stop
// re-registering while their dialog lives — without the BYE they wedge until
// manually restarted, and the stale stream poisons the recycled port.
func (s *Server) ByeAllSessions() {
	for _, channelID := range s.sessionMgr.ChannelIDs() {
		_ = s.sessionMgr.Bye(channelID) // Bye → byeSender → SIP BYE first
	}
}

// ByeChannelByID stops a channel's media session, resolving the owning
// device automatically (implements the api.GB28181ByeSender contract).
func (s *Server) ByeChannelByID(channelID string) error {
	deviceID := s.deviceOfChannel(channelID)
	return s.ByeChannel(deviceID, channelID)
}

// sendByeForChannel transmits the in-dialog SIP BYE for a channel session
// (best-effort). Implements the SessionManager bye sender contract.
func (s *Server) sendByeForChannel(channelID string) error {
	s.mu.Lock()
	srv := s.gosipSrv
	dialog := s.dialogs[channelID]
	delete(s.dialogs, channelID)
	s.mu.Unlock()
	if srv == nil || dialog == nil {
		return nil
	}

	fromHdr, hasFrom := dialog.resp.From()
	toHdr, hasTo := dialog.resp.To()
	if !hasFrom || !hasTo {
		return nil
	}
	callID, hasCallID := dialog.resp.CallID()
	seq := uint(1)
	if cseq, ok := dialog.resp.CSeq(); ok {
		seq = uint(cseq.SeqNo) + 1
	}

	serverHost := s.localIPFor(dialog.req.Recipient().String())

	rb := sip.NewRequestBuilder()
	rb.SetMethod(sip.BYE)
	rb.SetFrom(&sip.Address{DisplayName: fromHdr.DisplayName, Uri: fromHdr.Address})
	rb.SetTo(&sip.Address{DisplayName: toHdr.DisplayName, Uri: toHdr.Address})
	if hasCallID {
		rb.SetCallID(callID)
	}
	rb.SetRecipient(dialog.req.Recipient())
	rb.SetHost(serverHost)
	_, sipPort, err := parseSIPListen(s.cfg.SIPListen)
	if err != nil {
		sipPort = 5060
	}
	sipPortVal := sip.Port(sipPort)
	rb.AddVia(&sip.ViaHop{
		Host:   serverHost,
		Port:   &sipPortVal,
		Params: sip.NewParams().Add("branch", sip.String{Str: sip.GenerateBranch()}),
	})
	rb.SetSeqNo(seq)
	byeReq, err := rb.Build()
	if err != nil {
		return fmt.Errorf("gb28181: build BYE request: %w", err)
	}
	if _, err := srv.Request(byeReq); err != nil {
		return fmt.Errorf("gb28181: send BYE for %s: %w", channelID, err)
	}
	slog.Info("gb28181: BYE sent", "channel", channelID)
	return nil
}

// sdpSSRC extracts the y= SSRC line value from an SDP body ("" when absent).
func sdpSSRC(sdp []byte) string {
	for _, line := range splitCRLF(string(sdp)) {
		if len(line) > 2 && line[0] == 'y' && line[1] == '=' {
			return line[2:]
		}
	}
	return ""
}

func splitCRLF(s string) []string {
	var out []string
	start := 0
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '\r' && s[i+1] == '\n' {
			out = append(out, s[start:i])
			start = i + 2
			i++
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// notifyCameraBye flips the camera recorder bound to a channel back to
// Reconnecting (no-op when no camera is bound).
func (s *Server) notifyCameraBye(deviceID, channelID string) {
	enrol := s.enroller()
	if enrol == nil {
		return
	}
	if cameraID, ok := enrol.GB28181CameraIDByChannel(deviceID, channelID); ok {
		enrol.OnGB28181Bye(cameraID)
	}
}

// OnDeviceOffline tears down every media session of a device that went
// offline (heartbeat timeout) and marks the bound cameras' recorders as
// reconnecting. Called from the DeviceManager offline callback.
func (s *Server) OnDeviceOffline(deviceID string) {
	s.sessionMgr.ByeDevice(deviceID)
	for _, ch := range s.deviceMgr.Channels(deviceID) {
		s.notifyCameraBye(deviceID, ch.ID)
	}
}

// autoInviteDevice INVITEs every channel of the device that has a bound
// camera and no active session. Runs after each successful REGISTER so
// streams recover after NVR restarts (cameras exist; the device re-registers)
// without waiting for a manual invite.
func (s *Server) autoInviteDevice(deviceID string) {
	enrol := s.enroller()
	if enrol == nil {
		return
	}
	for _, ch := range s.deviceMgr.Channels(deviceID) {
		if s.sessionMgr.GetReceiver(ch.ID) != nil {
			continue
		}
		if _, ok := enrol.GB28181CameraIDByChannel(deviceID, ch.ID); !ok {
			continue
		}
		if err := s.InviteChannel(deviceID, ch.ID); err != nil {
			slog.Debug("gb28181: auto-INVITE on register", "device", deviceID, "channel", ch.ID, "error", err)
		}
	}
}

// handleRegister processes a device REGISTER: digest challenge → validate →
// mark the device online (or unregister it when Expires is 0).
func (s *Server) handleRegister(req sip.Request, tx sip.ServerTransaction) {
	from, ok := req.From()
	if !ok {
		slog.Warn("gb28181: REGISTER without From header", "source", req.Source())
		s.respond(req, tx, statusBadRequest, "Missing From header", nil)
		return
	}
	deviceID := from.Address.User().String()
	if deviceID == "" {
		slog.Warn("gb28181: REGISTER with empty device ID", "source", req.Source())
		s.respond(req, tx, statusBadRequest, "Missing device ID", nil)
		return
	}

	if !s.isAllowedDevice(deviceID) {
		slog.Warn("gb28181: REGISTER rejected — device not in allowlist", "device", deviceID, "source", req.Source())
		s.respond(req, tx, statusForbidden, "Device not allowed", nil)
		return
	}

	if s.cfg.Password != "" {
		auth := s.getAuthHeader(req)
		if auth == nil {
			slog.Info("gb28181: REGISTER challenge sent", "device", deviceID, "source", req.Source())
			s.send401Challenge(req, tx)
			return
		}
		if !s.validateDigest(auth, deviceID, req) {
			slog.Warn("gb28181: REGISTER auth failed", "device", deviceID, "source", req.Source())
			s.respond(req, tx, statusForbidden, "Invalid credentials", nil)
			return
		}
	}

	// Serialize per device so REGISTER/keepalive ordering is preserved.
	mu := s.getDeviceMu(deviceID)
	mu.Lock()
	defer mu.Unlock()

	expires := 3600
	if h := s.requestExpires(req); h >= 0 {
		expires = h
	}

	if expires == 0 {
		slog.Info("gb28181: device unregistered", "device", deviceID, "source", req.Source())
		s.teardownDevice(deviceID)
		s.deviceMgr.Unregister(deviceID)
		if s.db != nil {
			if err := s.db.DeleteGB28181Device(context.Background(), deviceID); err != nil {
				slog.Warn("gb28181: failed to delete device from DB", "device", deviceID, "error", err)
			}
		}
	} else {
		slog.Info("gb28181: device registered", "device", deviceID, "source", req.Source(), "expires", expires)
		// A re-REGISTER from a different IP (device reboot, DHCP change, NAT
		// rebind) invalidates the old media sessions: their INVITE dialogs
		// pointed at the old address. Tear them down so the auto-INVITE below
		// rebuilds fresh sessions. The source PORT is not compared: SIP-over-
		// UDP stacks may open a fresh socket per REGISTER, and media sessions
		// are anchored by SDP addresses, not the REGISTER source port.
		if existing, ok := s.deviceMgr.Device(deviceID); ok {
			existing.Mu.RLock()
			addrChanged := hostOfAddr(existing.NetAddr) != hostOfAddr(req.Source())
			existing.Mu.RUnlock()
			if addrChanged {
				slog.Info("gb28181: device address changed — recycling sessions", "device", deviceID, "new_source", req.Source())
				s.sessionMgr.ByeDevice(deviceID)
			}
		}
		s.deviceMgr.Register(&platform.Device{
			ID:      deviceID,
			NetAddr: req.Source(),
		})
		// Auto-register the device itself as a channel — but ONLY on first
		// registration, not on periodic re-REGISTERs. Re-registering would
		// overwrite the channel's Status (resetting inviting/playing to idle).
		//
		// The device-self pseudo-channel is a pre-catalog fallback for
		// single-channel devices without Catalog support. When the DB already
		// holds OTHER channels for this device (persisted from an earlier
		// catalog), the pseudo-channel would never stream and its camera would
		// respawn on every NVR restart — skip it entirely (#352).
		_, channelExists := s.deviceMgr.FindChannel(deviceID, deviceID)
		selfSuperseded := !channelExists && s.deviceHasCatalogChannels(deviceID)
		if !channelExists && !selfSuperseded {
			s.deviceMgr.RegisterChannel(deviceID, &platform.Channel{
				ID:       deviceID,
				DeviceID: deviceID,
				Name:     "",
			})
			slog.Info("gb28181: device auto-registered as channel", "device", deviceID)
		} else if selfSuperseded {
			slog.Info("gb28181: device-self pseudo-channel skipped — catalog channels known", "device", deviceID)
		}
		if s.db != nil {
			now := time.Now()
			if err := s.db.UpsertGB28181Device(context.Background(), storage.GB28181Device{
				ID:            deviceID,
				Status:        "online",
				LastKeepalive: now,
				RegisteredAt:  now,
			}); err != nil {
				slog.Warn("gb28181: failed to persist device to DB", "device", deviceID, "error", err)
			}
			if !channelExists && !selfSuperseded {
				if err := s.db.UpsertGB28181Channel(context.Background(), storage.GB28181Channel{
					ID:        deviceID,
					DeviceID:  deviceID,
					Status:    "idle",
					UpdatedAt: now,
				}); err != nil {
					slog.Warn("gb28181: failed to persist auto-channel to DB", "device", deviceID, "error", err)
				}
			}
		}

		// Auto-create a camera in the main Cameras list on first registration,
		// so GB28181 cameras appear alongside ONVIF/RTSP cameras without manual
		// setup. Runs in a goroutine to avoid blocking the SIP response.
		if !channelExists && !selfSuperseded {
			if enrol := s.enroller(); enrol != nil {
				srcHost := hostOfAddr(req.Source())
				go func() {
					if err := enrol.EnsureGB28181Camera(deviceID, deviceID, "", srcHost); err != nil {
						slog.Warn("gb28181: auto-enroll camera failed", "device", deviceID, "error", err)
					}
				}()
			}
		}
	}

	// 200 OK first — the device must see its REGISTER accepted before any
	// follow-up request (catalog query, INVITE) arrives.
	exp := sip.Expires(expires)
	s.respond(req, tx, statusOK, "OK", []sip.Header{&exp})

	if expires != 0 {
		// Ask the device for its catalog so real video channels (whose IDs
		// differ from the device ID on multi-channel devices) are discovered
		// and enrolled as cameras. Async — the response arrives as a MESSAGE.
		go func() {
			if err := s.requestCatalog(deviceID); err != nil {
				slog.Debug("gb28181: catalog query after register", "device", deviceID, "error", err)
			}
			// Auto-populate device metadata (manufacturer/model) and probe
			// its status — fills camera Brand/Model on first registration.
			s.queryDeviceDetails(deviceID)
			// Refresh subscriptions (catalog changes, alarms, mobile
			// positions) per config — idempotent on re-REGISTER.
			s.subscribeDevice(deviceID)
		}()
		// Auto-INVITE channels that have a camera but no active session
		// (covers NVR restarts: cameras persist, sessions do not).
		go s.autoInviteDevice(deviceID)
	}
}

// queryDeviceDetails sends DeviceInfo + DeviceStatus queries to a device.
// Responses arrive as MESSAGEs and land in handleMessage's DeviceInfo /
// DeviceStatus cases. Failures are non-fatal (metadata stays empty).
func (s *Server) queryDeviceDetails(deviceID string) {
	sn := time.Now().UnixNano() % 100000
	infoBody := []byte(fmt.Sprintf(`<Query><CmdType>DeviceInfo</CmdType><SN>%d</SN><DeviceID>%s</DeviceID></Query>`, sn, deviceID))
	if err := s.SendMessage(deviceID, infoBody); err != nil {
		slog.Debug("gb28181: device info query", "device", deviceID, "error", err)
	}
	sn = time.Now().UnixNano() % 100000
	statusBody := []byte(fmt.Sprintf(`<Query><CmdType>DeviceStatus</CmdType><SN>%d</SN><DeviceID>%s</DeviceID></Query>`, sn, deviceID))
	if err := s.SendMessage(deviceID, statusBody); err != nil {
		slog.Debug("gb28181: device status query", "device", deviceID, "error", err)
	}
}

// requestCatalog sends a MANSCDP Catalog query to an online device.
func (s *Server) requestCatalog(deviceID string) error {
	dev, ok := s.deviceMgr.Device(deviceID)
	if !ok || dev.Status.Load() != platform.DeviceOnline {
		return platform.ErrDeviceOffline
	}
	sn := time.Now().UnixNano() % 100000
	body := []byte(fmt.Sprintf(`<Query><CmdType>Catalog</CmdType><SN>%d</SN><DeviceID>%s</DeviceID></Query>`, sn, deviceID))
	return s.SendMessage(deviceID, body)
}

// teardownDevice stops every session of a device and notifies the bound
// cameras (used on unregister / keepalive OFF).
func (s *Server) teardownDevice(deviceID string) {
	s.sessionMgr.ByeDevice(deviceID)
	s.unsubscribeDevice(deviceID)
	for _, ch := range s.deviceMgr.Channels(deviceID) {
		s.notifyCameraBye(deviceID, ch.ID)
	}
}

// handleMessage processes device MESSAGE bodies (keepalive, catalog,
// device-info) via manscdp.
func (s *Server) handleMessage(req sip.Request, tx sip.ServerTransaction) {
	body := req.Body()
	if body == "" {
		s.respond(req, tx, statusBadRequest, "Empty body", nil)
		return
	}

	ct, payload, err := manscdp.Decode([]byte(body))
	if err != nil {
		s.respond(req, tx, statusBadRequest, "Invalid MANSCDP body", nil)
		return
	}

	// Sender identity for spoof checks ("" when From is absent).
	fromUser := ""
	if from, ok := req.From(); ok {
		fromUser = from.Address.User().String()
	}

	switch ct {
	case manscdp.CmdKeepalive:
		p := payload.(manscdp.Keepalive)
		// A keepalive must come from the device it vouches for — otherwise a
		// spoofed MESSAGE keeps a dead device "online" forever.
		if fromUser != "" && fromUser != p.DeviceID {
			slog.Warn("gb28181: keepalive sender mismatch", "from", fromUser, "body_device", p.DeviceID, "source", req.Source())
			s.respond(req, tx, statusForbidden, "Keepalive sender mismatch", nil)
			return
		}
		if _, ok := s.deviceMgr.Device(p.DeviceID); !ok {
			slog.Warn("gb28181: keepalive from unregistered device", "device", p.DeviceID, "source", req.Source())
			s.respond(req, tx, statusForbidden, "Device not registered", nil)
			return
		}
		// Status OFF announces the device is going down: mark offline and
		// stop its sessions instead of waiting for the heartbeat timeout.
		if p.Status != "" && p.Status != "OK" {
			slog.Info("gb28181: keepalive reports device down", "device", p.DeviceID, "status", p.Status)
			s.teardownDevice(p.DeviceID)
			s.deviceMgr.MarkOffline(p.DeviceID)
			if s.db != nil {
				if err := s.db.MarkDeviceOffline(context.Background(), p.DeviceID); err != nil {
					slog.Warn("gb28181: failed to mark device offline in DB", "device", p.DeviceID, "error", err)
				}
			}
			s.respond(req, tx, statusOK, "OK", nil)
			return
		}
		s.deviceMgr.Touch(p.DeviceID)
		if s.db != nil {
			if err := s.db.UpsertGB28181Device(context.Background(), storage.GB28181Device{
				ID:            p.DeviceID,
				Status:        "online",
				LastKeepalive: time.Now(),
			}); err != nil {
				slog.Warn("gb28181: failed to update keepalive in DB", "device", p.DeviceID, "error", err)
			}
		}
	case manscdp.CmdCatalog:
		// Response-root over MESSAGE; Notify-root normally rides a NOTIFY
		// request (handleNotify) but tolerate it here rather than panic.
		if n, ok := payload.(manscdp.CatalogNotify); ok {
			slog.Info("gb28181: catalog received (notify form)", "device", n.DeviceID, "channels", len(n.Item))
			s.mergeCatalogChannels(n.DeviceID, n.Item)
			break
		}
		p, ok := payload.(manscdp.Catalog)
		if !ok {
			break
		}
		slog.Info("gb28181: catalog received", "device", p.DeviceID, "channels", len(p.Item))
		s.mergeCatalogChannels(p.DeviceID, p.Item)
	case manscdp.CmdRecordInfo:
		p := payload.(manscdp.RecordInfo)
		slog.Info("gb28181: record info received", "device", p.DeviceID,
			"sn", p.SN, "sum_num", p.SumNum, "items", len(p.RecordList))
		s.feedRecordQuery(p.DeviceID, p)
	case manscdp.CmdDeviceInfo:
		p := payload.(manscdp.DeviceInfo)
		slog.Info("gb28181: device info received", "device", p.DeviceID, "name", p.DeviceName, "manufacturer", p.Manufacturer, "model", p.Model)
		if d, ok := s.deviceMgr.Device(p.DeviceID); ok {
			d.Mu.Lock()
			if p.DeviceName != "" {
				d.Name = p.DeviceName
			}
			if p.Manufacturer != "" {
				d.Manufacturer = p.Manufacturer
			}
			if p.Model != "" {
				d.Model = p.Model
			}
			d.Mu.Unlock()
		}
		if s.db != nil {
			if err := s.db.UpsertGB28181Device(context.Background(), storage.GB28181Device{
				ID:            p.DeviceID,
				Name:          p.DeviceName,
				Manufacturer:  p.Manufacturer,
				Model:         p.Model,
				Status:        "online",
				LastKeepalive: time.Now(),
			}); err != nil {
				slog.Warn("gb28181: failed to update device info in DB", "device", p.DeviceID, "error", err)
			}
		}
		// Backfill Brand/Model on the device's bound cameras (empty fields
		// only — user-entered values win). Firmware is kept on the device
		// record (no camera column for it).
		if enrol := s.enroller(); enrol != nil {
			if p.Manufacturer != "" || p.Model != "" {
				if err := enrol.UpdateGB28181DeviceMeta(p.DeviceID, p.Manufacturer, p.Model); err != nil {
					slog.Debug("gb28181: camera meta backfill", "device", p.DeviceID, "error", err)
				}
			}
		}
	case manscdp.CmdDeviceStatus:
		p := payload.(manscdp.DeviceStatus)
		slog.Info("gb28181: device status received", "device", p.DeviceID, "status", p.Status, "time", p.Time)
		if p.Status != "" && p.Status != "OK" && p.Status != "ON" {
			slog.Warn("gb28181: device reports abnormal status", "device", p.DeviceID, "status", p.Status)
		}
	case manscdp.CmdAlarm:
		// Some firmwares deliver alarms as MESSAGE instead of NOTIFY —
		// route both into the same pipeline.
		s.handleAlarm(payload.(manscdp.Alarm))
	case manscdp.CmdTimeSync:
		// Device clock query (GB/T 28181-2016 § 9.6): answer with the
		// platform wall clock so device-side timestamps (and RecordInfo
		// ranges) stay aligned. The query may arrive with a Query root.
		p := payload.(manscdp.TimeSyncQuery)
		slog.Info("gb28181: time sync query", "device", p.DeviceID, "sn", p.SN)
		go func(devID string, sn int) {
			body, err := manscdp.Encode(manscdp.TimeSyncResponse{
				CmdType:  manscdp.CmdTimeSync,
				SN:       sn,
				DeviceID: devID,
				Time:     time.Now().Format("2006-01-02T15:04:05"),
			})
			if err != nil {
				slog.Warn("gb28181: encode time sync response", "error", err)
				return
			}
			if err := s.SendMessage(devID, body); err != nil {
				slog.Debug("gb28181: send time sync response", "device", devID, "error", err)
			}
		}(p.DeviceID, p.SN)
	}

	s.respond(req, tx, statusOK, "OK", nil)
}

// mergeCatalogChannels ingests a channel list from a catalog response or a
// catalog-change NOTIFY: registers channels, persists them, enrolls cameras
// for new ones, and auto-INVITEs channels that have a camera but no session.
func (s *Server) mergeCatalogChannels(deviceID string, items []manscdp.Item) {
	// Source host for cross-protocol auto-enroll dedup (dual-protocol cameras
	// that already stream via ONVIF/RTSP from the same IP).
	srcHost := ""
	if d, ok := s.deviceMgr.Device(deviceID); ok {
		d.Mu.RLock()
		srcHost = hostOfAddr(d.NetAddr)
		d.Mu.RUnlock()
	}
	for _, item := range items {
		// Parental=1 entries are organization/group nodes, not video
		// channels — registering them as INVITEable channels breaks
		// media setup on tree-shaped catalogs (Hikvision/Dahua NVRs).
		if item.Parental == 1 {
			continue
		}
		// Merge into the existing channel in place where possible so a
		// catalog refresh updates names/PTZ without resetting the
		// session status of an inviting/playing channel.
		status := int32(platform.ChannelIdle)
		if existing, ok := s.deviceMgr.FindChannel(deviceID, item.DeviceID); ok {
			status = existing.Status.Load()
		}
		ch := &platform.Channel{
			ID:       item.DeviceID,
			Name:     item.Name,
			Parental: item.Parental,
			PTZType:  item.PTZType,
		}
		ch.Status.Store(status)
		s.deviceMgr.RegisterChannel(deviceID, ch)
		cameraID := ""
		if enrol := s.enroller(); enrol != nil {
			if id, ok := enrol.GB28181CameraIDByChannel(deviceID, item.DeviceID); ok {
				cameraID = id
			}
		}
		if s.db != nil {
			if err := s.db.UpsertGB28181Channel(context.Background(), storage.GB28181Channel{
				ID:        item.DeviceID,
				DeviceID:  deviceID,
				Name:      item.Name,
				Parental:  item.Parental,
				Status:    channelStatusString(status),
				CameraID:  cameraID,
				UpdatedAt: time.Now(),
			}); err != nil {
				slog.Warn("gb28181: failed to persist channel to DB", "channel", item.DeviceID, "error", err)
			}
		}
		// Enroll a camera per real video channel so multi-channel
		// devices (NVRs) get one camera per channel. Auto-INVITE follows
		// from the recorder start path.
		if cameraID == "" {
			if enrol := s.enroller(); enrol != nil {
				name := item.Name
				devID, chID := deviceID, item.DeviceID
				go func() {
					if err := enrol.EnsureGB28181Camera(devID, chID, name, srcHost); err != nil {
						// "camera already exists" fires on every catalog
						// refresh for enrolled channels (an idempotency
						// signal, not an error) — Debug keeps the log
						// readable; real failures (DB errors) stay at Warn.
						if strings.Contains(err.Error(), "already exists") {
							slog.Debug("gb28181: channel already enrolled", "device", devID, "channel", chID)
						} else {
							slog.Warn("gb28181: auto-enroll channel camera failed", "device", devID, "channel", chID, "error", err)
						}
					}
				}()
			}
		}
	}
	// A catalog with real (non-parental) channels supersedes the device-self
	// pseudo-channel created speculatively at first REGISTER — UNLESS the
	// catalog lists the device ID itself (single-channel devices whose channel
	// equals the device ID) or the pseudo-channel is actively streaming (#352).
	s.retireDeviceSelfChannel(deviceID, items)
	// Channels may have been discovered by this catalog (or a prior one):
	// INVITE every channel that now has a bound camera and no active
	// session. Also covers NVR restarts — cameras persist, sessions do
	// not, and catalog channels are only known after this response.
	go s.autoInviteDevice(deviceID)
	// Sub-channel probing (#560): after the main sessions settle, probe the
	// vendor-convention sub candidate per bound channel (silent on failure;
	// re-registers persisted codes after an NVR restart).
	go s.maybeProbeSubChannels(deviceID)

	// Channel-level Manufacturer/Model backfill: the DeviceInfo response
	// often races (and loses to) async auto-enrollment, but the catalog
	// that CREATED the camera carries the same metadata. Async — the
	// backfill only fills empty Brand/Model fields, never overwrites.
	mfr, mdl := "", ""
	for _, item := range items {
		if item.Parental == 1 {
			continue // organization nodes carry placeholder metadata
		}
		if mfr == "" && item.Manufacturer != "" {
			mfr = item.Manufacturer
		}
		if mdl == "" && item.Model != "" {
			mdl = item.Model
		}
	}
	if mfr != "" || mdl != "" {
		if enrol := s.enroller(); enrol != nil {
			devID := deviceID
			go func() {
				if err := enrol.UpdateGB28181DeviceMeta(devID, mfr, mdl); err != nil {
					slog.Debug("gb28181: camera meta backfill from catalog", "device", devID, "error", err)
				}
			}()
		}
	}
}

// channelStatusString maps a Channel status atom to its DB/API string.
func channelStatusString(status int32) string {
	switch status {
	case platform.ChannelInviting:
		return "inviting"
	case platform.ChannelPlaying:
		return "playing"
	default:
		return "idle"
	}
}

// retireDeviceSelfChannel removes the device-self pseudo-channel once a
// catalog proves the device's real channels (#352). Guards:
//   - the catalog must contain at least one real (non-parental) channel
//   - the device ID itself must NOT be among the catalog items (single-channel
//     devices whose channel equals the device ID keep it)
//   - the pseudo-channel must never have streamed (idle) — a playing
//     device-self session means some firmware actually streams on it
//
// Removal covers the in-memory registry, the DB row, and the auto-enrolled
// camera (archived, preserving its recordings).
func (s *Server) retireDeviceSelfChannel(deviceID string, items []manscdp.Item) {
	realCount := 0
	for _, item := range items {
		if item.Parental != 1 {
			realCount++
			if item.DeviceID == deviceID {
				return // device-self IS a catalog channel — keep it
			}
		}
	}
	if realCount == 0 {
		return
	}
	ch, ok := s.deviceMgr.FindChannel(deviceID, deviceID)
	if !ok {
		return
	}
	if ch.Status.Load() != int32(platform.ChannelIdle) {
		return // streaming (or mid-invite): leave it alone
	}
	s.deviceMgr.UnregisterChannel(deviceID, deviceID)
	if s.db != nil {
		if err := s.db.DeleteGB28181Channel(context.Background(), deviceID); err != nil {
			slog.Warn("gb28181: failed to delete device-self channel row", "device", deviceID, "error", err)
		}
	}
	if enrol := s.enroller(); enrol != nil {
		if err := enrol.ArchiveGB28181Camera(deviceID, deviceID); err != nil {
			slog.Warn("gb28181: failed to archive device-self camera", "device", deviceID, "error", err)
		}
	}
	slog.Info("gb28181: device-self pseudo-channel retired — catalog provides real channels", "device", deviceID)
}

// handleOptions answers OPTIONS probes (GB/T 28181-2007/2011-era devices and
// generic SIP checkers use them for keepalive/liveness).
func (s *Server) handleOptions(req sip.Request, tx sip.ServerTransaction) {
	allow := sip.AllowHeader{sip.REGISTER, sip.MESSAGE, sip.INVITE, sip.ACK, sip.BYE, sip.CANCEL, sip.OPTIONS}
	s.respond(req, tx, statusOK, "OK", []sip.Header{&allow})
}

// handleInvite delegates to the session manager hook, or rejects with 486
// when no hook is installed (media sessions not yet wired).
func (s *Server) handleInvite(req sip.Request, tx sip.ServerTransaction) {
	deviceID, channelID := s.requestIDs(req)
	s.mu.Lock()
	hook := s.onInvite
	s.mu.Unlock()
	if hook == nil {
		s.respond(req, tx, statusBusyHere, "Busy Here", nil)
		return
	}
	hook(deviceID, channelID)
}

// handleBye processes a device-initiated BYE: the local session is torn down,
// the channel returns to idle, and the bound camera's recorder goes back to
// Reconnecting. When a hook is installed it takes precedence.
func (s *Server) handleBye(req sip.Request, tx sip.ServerTransaction) {
	deviceID, channelID := s.requestIDs(req)

	// A BYE whose Call-ID matches an active talk session ends the intercom
	// (#341) — the device hung up on the caller.
	if s.stopTalkOnBye(req) {
		s.respond(req, tx, statusOK, "OK", nil)
		return
	}

	// A BYE whose Call-ID matches a playback dialog ends that fetch (the
	// device finished streaming the requested range) — distinct from the
	// live dialog on the same channel (#337).
	if callID, ok := req.CallID(); ok {
		s.mu.Lock()
		pbDlg := s.pbDialogs[channelID]
		s.mu.Unlock()
		if pbDlg != nil {
			if dlgCallID, ok2 := pbDlg.resp.CallID(); ok2 && dlgCallID.String() == callID.String() {
				slog.Info("gb28181: playback BYE received", "channel", channelID)
				s.mu.Lock()
				delete(s.pbDialogs, channelID)
				s.mu.Unlock()
				_ = s.StopPlayback(channelID)
				s.respond(req, tx, statusOK, "OK", nil)
				return
			}
		}
	}

	s.mu.Lock()
	hook := s.onBye
	s.mu.Unlock()
	if hook != nil {
		hook(deviceID, channelID)
		s.respond(req, tx, statusOK, "OK", nil)
		return
	}

	// The device is the callee of our INVITE, so its in-dialog BYE carries
	// the channel in From. Some devices (and our tests) put it in To —
	// resolve both.
	byeChannel := channelID
	if from, ok := req.From(); ok {
		if u := from.Address.User().String(); u != "" && u != s.cfg.ServerID {
			byeChannel = u
		}
	}
	byeDevice := deviceID
	if byeChannel != channelID {
		// From held the channel: locate its owning device.
		byeDevice = s.deviceOfChannel(byeChannel)
	} else if byeDevice == s.cfg.ServerID {
		byeDevice = s.deviceOfChannel(byeChannel)
	}

	s.mu.Lock()
	delete(s.dialogs, byeChannel)
	s.mu.Unlock()
	_ = s.sessionMgr.Bye(byeChannel)
	s.notifyCameraBye(byeDevice, byeChannel)
	s.respond(req, tx, statusOK, "OK", nil)
}

// handleInfo answers device→platform SIP INFO. Devices report media end via a
// MANSRTSP-style MediaStatus body on the fetch dialog (GB/T 28181-2016
// §9.4.2: "MediaStatus: Download Finished" / "Play Finished") — finalize the
// fetch immediately instead of waiting out the stall watchdog.
func (s *Server) handleInfo(req sip.Request, tx sip.ServerTransaction) {
	s.respond(req, tx, statusOK, "OK", nil)
	body := string(req.Body())
	if !strings.Contains(strings.ToLower(body), "mediastatus") {
		return
	}
	finished := strings.Contains(strings.ToLower(body), "finished")
	// In-dialog INFO from the device mirrors our fetch INVITE: From = the
	// INVITEd channel, To = this server.
	channelID := ""
	if from, ok := req.From(); ok {
		channelID = from.Address.User().String()
	}
	if channelID == "" || channelID == s.cfg.ServerID {
		return
	}
	// Only trust the notification when it arrives on the channel's fetch
	// dialog (matched by Call-ID), mirroring handleBye's dialog check.
	callID, hasCallID := req.CallID()
	s.mu.Lock()
	pbDlg := s.pbDialogs[channelID]
	s.mu.Unlock()
	if pbDlg == nil || !hasCallID {
		return
	}
	if dlgCallID, ok := pbDlg.resp.CallID(); !ok || dlgCallID.String() != callID.String() {
		return
	}
	if finished {
		slog.Info("gb28181: fetch MediaStatus finished received", "channel", channelID)
		_ = s.StopPlayback(channelID)
	}
}

// deviceOfChannel finds the device owning channelID ("" when unknown).
func (s *Server) deviceOfChannel(channelID string) string {
	for _, dev := range s.deviceMgr.AllDevices() {
		if _, ok := s.deviceMgr.FindChannel(dev.ID, channelID); ok {
			return dev.ID
		}
	}
	return ""
}

// requestIDs extracts the device ID (From user) and channel ID (To user) from
// an INVITE/BYE request.
func (s *Server) requestIDs(req sip.Request) (deviceID, channelID string) {
	if from, ok := req.From(); ok {
		deviceID = from.Address.User().String()
	}
	if to, ok := req.To(); ok {
		channelID = to.Address.User().String()
	}
	return deviceID, channelID
}

// isAllowedDevice reports whether deviceID may register. An empty allowlist
// permits any device.
func (s *Server) isAllowedDevice(deviceID string) bool {
	if len(s.cfg.AllowedDeviceIDs) == 0 {
		return true
	}
	for _, id := range s.cfg.AllowedDeviceIDs {
		if id == deviceID {
			return true
		}
	}
	return false
}

// send401Challenge replies with a Digest challenge (RFC 3261 § 22.2).
func (s *Server) send401Challenge(req sip.Request, tx sip.ServerTransaction) {
	realm := s.cfg.Realm
	if realm == "" {
		realm = "gb28181"
	}
	value := fmt.Sprintf(`Digest realm="%s", nonce="%s", algorithm=MD5`, realm, generateNonce())
	headers := []sip.Header{&sip.GenericHeader{HeaderName: "WWW-Authenticate", Contents: value}}
	s.respond(req, tx, statusUnauthorized, "Unauthorized", headers)
}

// hostOfAddr extracts the IP from a "IP:port" source address. Used to detect
// device address changes while tolerating source-port rotation (see
// handleRegister).
func hostOfAddr(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// deviceHasCatalogChannels reports whether the DB holds any channel for the
// device other than the device-self pseudo-channel — i.e., a catalog was
// received at some point and persisted its real channels. Used to suppress
// the device-self fallback after NVR restarts (#352). DB-less deployments
// (tests) report false, preserving the legacy behavior.
func (s *Server) deviceHasCatalogChannels(deviceID string) bool {
	if s.db == nil {
		return false
	}
	channels, err := s.db.ListGB28181Channels(context.Background(), deviceID)
	if err != nil {
		return false
	}
	for _, ch := range channels {
		if ch.ID != deviceID {
			return true
		}
	}
	return false
}

// getAuthHeader extracts the Authorization header from a request. gosip's
// parser produces a GenericHeader for Authorization, so it is re-parsed.
func (s *Server) getAuthHeader(req sip.Request) *sip.Authorization {
	for _, h := range req.GetHeaders("Authorization") {
		if gh, ok := h.(*sip.GenericHeader); ok {
			return sip.AuthFromValue(gh.Contents)
		}
	}
	return nil
}

// validateDigest verifies the digest response against the configured password.
func (s *Server) validateDigest(auth *sip.Authorization, deviceID string, req sip.Request) bool {
	if auth == nil || auth.Username() != deviceID {
		return false
	}
	auth.SetPassword(s.cfg.Password)
	auth.SetMethod(string(req.Method()))
	return auth.CalcResponse() == auth.Response()
}

// requestExpires returns the request's Expires header value, or -1 when the
// header is absent.
func (s *Server) requestExpires(req sip.Request) int {
	for _, h := range req.GetHeaders("Expires") {
		if exp, ok := h.(*sip.Expires); ok {
			return int(*exp)
		}
	}
	return -1
}

// respond sends a response for the given request. Safe to call after Stop —
// the write is skipped once the server is gone.
func (s *Server) respond(req sip.Request, tx sip.ServerTransaction, status sip.StatusCode, reason string, headers []sip.Header) {
	s.mu.Lock()
	srv := s.gosipSrv
	s.mu.Unlock()
	if srv == nil {
		return
	}
	if _, err := srv.RespondOnRequest(req, status, reason, "", headers); err != nil {
		slog.Warn("gb28181: respond failed", "method", req.Method(), "status", status, "error", err)
	}
}

// getDeviceMu returns (creating if needed) the per-device mutex.
func (s *Server) getDeviceMu(deviceID string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	mu, ok := s.perDeviceMu[deviceID]
	if !ok {
		mu = &sync.Mutex{}
		s.perDeviceMu[deviceID] = mu
	}
	return mu
}

// parseSIPListen splits a "host:port" listen address, defaulting to :5060.
func parseSIPListen(listen string) (host string, port int, err error) {
	if listen == "" {
		return "", 5060, nil
	}
	host, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		return "", 0, fmt.Errorf("gb28181: invalid sip_listen %q: %w", listen, err)
	}
	port, err = strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("gb28181: invalid sip_listen port %q", portStr)
	}
	return host, port, nil
}

// localIPFor returns the NVR's source IP for reaching remoteAddr (an
// "ip:port" string). It performs a UDP dial to let the kernel pick the
// correct local address for the route, which handles multi-homed hosts
// and cross-subnet devices correctly. Falls back to localIP() on error.
func (s *Server) localIPFor(remoteAddr string) string {
	if conn, err := net.Dial("udp", remoteAddr); err == nil {
		defer conn.Close()
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok && addr.IP.To4() != nil {
			return addr.IP.String()
		}
	}
	return s.localIP()
}

func (s *Server) localIP() string {
	host, _, err := parseSIPListen(s.cfg.SIPListen)
	if err == nil && host != "" && host != "0.0.0.0" {
		return host
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok {
			if ip4 := ipnet.IP.To4(); ip4 != nil && !ip4.IsLoopback() {
				return ip4.String()
			}
		}
	}
	return "127.0.0.1"
}

// generateNonce returns a random hex nonce for digest challenges.
func generateNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return hex.EncodeToString(b)
}

// slogAdapter adapts log/slog to gosip's log.Logger interface.
type slogAdapter struct {
	logger *slog.Logger
}

func (a slogAdapter) WithPrefix(prefix string) log.Logger {
	return slogAdapter{a.logger.With("prefix", prefix)}
}

func (a slogAdapter) Prefix() string { return "" }

func (a slogAdapter) WithFields(fields map[string]interface{}) log.Logger {
	attrs := make([]any, 0, len(fields)*2)
	for k, v := range fields {
		attrs = append(attrs, k, v)
	}
	return slogAdapter{a.logger.With(attrs...)}
}

func (a slogAdapter) Fields() log.Fields { return nil }

func (a slogAdapter) SetLevel(level uint32) {}

func (a slogAdapter) Fatal(args ...interface{}) { a.logger.Error(fmt.Sprint(args...)) }
func (a slogAdapter) Fatalf(format string, args ...interface{}) {
	a.logger.Error(fmt.Sprintf(format, args...))
}
func (a slogAdapter) Panic(args ...interface{}) { a.logger.Error(fmt.Sprint(args...)) }
func (a slogAdapter) Panicf(format string, args ...interface{}) {
	a.logger.Error(fmt.Sprintf(format, args...))
}
func (a slogAdapter) Trace(args ...interface{}) { a.logger.Debug(fmt.Sprint(args...)) }
func (a slogAdapter) Tracef(format string, args ...interface{}) {
	a.logger.Debug(fmt.Sprintf(format, args...))
}
func (a slogAdapter) Debug(args ...interface{}) { a.logger.Debug(fmt.Sprint(args...)) }
func (a slogAdapter) Debugf(format string, args ...interface{}) {
	a.logger.Debug(fmt.Sprintf(format, args...))
}
func (a slogAdapter) Print(args ...interface{}) { a.logger.Info(fmt.Sprint(args...)) }
func (a slogAdapter) Printf(format string, args ...interface{}) {
	a.logger.Info(fmt.Sprintf(format, args...))
}
func (a slogAdapter) Info(args ...interface{}) { a.logger.Info(fmt.Sprint(args...)) }
func (a slogAdapter) Infof(format string, args ...interface{}) {
	a.logger.Info(fmt.Sprintf(format, args...))
}
func (a slogAdapter) Warn(args ...interface{}) { a.logger.Warn(fmt.Sprint(args...)) }
func (a slogAdapter) Warnf(format string, args ...interface{}) {
	a.logger.Warn(fmt.Sprintf(format, args...))
}
func (a slogAdapter) Error(args ...interface{}) { a.logger.Error(fmt.Sprint(args...)) }
func (a slogAdapter) Errorf(format string, args ...interface{}) {
	a.logger.Error(fmt.Sprintf(format, args...))
}

// SlogLogger exposes the package's gosip log adapter to sibling packages
// (gb28181/cascade) so they share one adapter implementation.
func SlogLogger(l *slog.Logger) log.Logger { return slogAdapter{l} }
