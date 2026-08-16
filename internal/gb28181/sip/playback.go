package sip

// Device-side recording query + playback (#337):
//   - RecordInfo query/response correlation (paged, matched by SN);
//   - playback INVITE lifecycle (s=Playback SDP, SSRC leading 1) whose RTP
//     is muxed into a normal MiBee recording via a per-fetch sink;
//   - SIP INFO MANSRTSP playback control (pause/resume/seek/scale).

import (
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181/manscdp"
	gosip "github.com/ghettovoice/gosip"
	"github.com/ghettovoice/gosip/sip"
)

// recordQueryTimeout bounds how long QueryChannelRecords waits for the
// device's paged RecordInfo responses.
const recordQueryTimeout = 10 * time.Second

// playbackStallTimeout: after a fetch's stream has started, this much silence
// means the device finished (or died) — finalize the recording.
const playbackStallTimeout = 12 * time.Second

// playbackStartupTimeout: no RTP at all within this window after a successful
// INVITE means the fetch is dead — recycle.
const playbackStartupTimeout = 30 * time.Second

// pendingRecordQuery collects the paged responses of one RecordInfo query.
type pendingRecordQuery struct {
	sn      int
	sumNum  int
	items   []manscdp.RecordItem
	done    chan struct{}
	mu      sync.Mutex
	created time.Time
}

// countingSink wraps a playback AUWriter with progress counters.
type countingSink struct {
	inner   gb28181.AUWriter
	frames  atomic.Int64
	lastPTS atomic.Int64
}

// QueryChannelRecords sends a RecordInfo query to the device owning
// channelID and collects its paged responses (until SumNum items arrive or
// recordQueryTimeout elapses). Returns the accumulated items.
func (s *Server) QueryChannelRecords(deviceID, channelID string, start, end time.Time) ([]manscdp.RecordItem, error) {
	dev, ok := s.deviceMgr.Device(deviceID)
	if !ok || dev.Status.Load() != gb28181.DeviceOnline {
		return nil, fmt.Errorf("gb28181: device %q not online", deviceID)
	}

	sn := int(time.Now().UnixNano() % 1000000)
	q := &pendingRecordQuery{sn: sn, done: make(chan struct{}), created: time.Now()}
	// Devices answer RecordInfo with the CHANNEL ID in DeviceID (the queried
	// ID), not the device ID — register both keys so correlation works
	// whichever the firmware echoes.
	s.recMu.Lock()
	s.recordQueries[recordKey(deviceID, sn)] = q
	s.recordQueries[recordKey(channelID, sn)] = q
	s.recMu.Unlock()
	defer func() {
		s.recMu.Lock()
		delete(s.recordQueries, recordKey(deviceID, sn))
		delete(s.recordQueries, recordKey(channelID, sn))
		s.recMu.Unlock()
	}()

	// GB/T 28181 timestamps are naive device-local clock strings — format in
	// the configured GB zone (defaults to the platform's local timezone:
	// deployments run NVR and devices in one TZ). Sending UTC-shifted strings
	// on a UTC host skews every record window by the TZ offset once the device
	// echoes them back.
	query := manscdp.RecordInfoQuery{
		CmdType:   manscdp.CmdRecordInfo,
		SN:        sn,
		DeviceID:  channelID,
		StartTime: start.In(s.gbTZ()).Format("2006-01-02T15:04:05"),
		EndTime:   end.In(s.gbTZ()).Format("2006-01-02T15:04:05"),
		Type:      "all",
	}
	body, err := manscdp.Encode(query)
	if err != nil {
		return nil, fmt.Errorf("gb28181: encode RecordInfo query: %w", err)
	}
	if err := s.SendMessage(deviceID, body); err != nil {
		return nil, fmt.Errorf("gb28181: send RecordInfo query: %w", err)
	}

	timer := time.NewTimer(recordQueryTimeout)
	defer timer.Stop()
	select {
	case <-q.done:
	case <-timer.C:
	}
	q.mu.Lock()
	items := q.items
	q.mu.Unlock()
	return items, nil
}

// feedRecordQuery folds one decoded RecordInfo response into its pending
// query (called from handleMessage).
func (s *Server) feedRecordQuery(deviceID string, resp manscdp.RecordInfo) {
	s.recMu.Lock()
	q, ok := s.recordQueries[recordKey(deviceID, resp.SN)]
	s.recMu.Unlock()
	if !ok {
		return
	}
	q.mu.Lock()
	if resp.SumNum > q.sumNum {
		q.sumNum = resp.SumNum
	}
	if len(resp.RecordList) > 0 {
		q.items = append(q.items, resp.RecordList...)
	}
	complete := q.sumNum > 0 && len(q.items) >= q.sumNum
	q.mu.Unlock()
	if complete {
		select {
		case <-q.done:
		default:
			close(q.done)
		}
	}
}

func recordKey(deviceID string, sn int) string {
	return deviceID + "|" + strconv.Itoa(sn)
}

// StartPlayback begins a device-recording fetch: a playback INVITE whose
// incoming RTP/PS is muxed into a normal MiBee recording (visible in the
// recordings UI like any local recording). One fetch per channel — a running
// fetch is stopped first.
func (s *Server) StartPlayback(deviceID, channelID string, start, end time.Time) error {
	if _, ok := s.deviceMgr.Device(deviceID); !ok {
		return fmt.Errorf("gb28181: device %q not registered", deviceID)
	}
	ch, ok := s.deviceMgr.FindChannel(deviceID, channelID)
	if !ok {
		return fmt.Errorf("gb28181: channel %q not found on device %q", channelID, deviceID)
	}

	// Resolve the bound camera so the fetched recording attaches to it.
	var cameraID string
	var sink gb28181.AUWriter
	var audioSink func(codec string, data, config []byte, ptsTicks int64, samples int)
	enrol := s.enroller()
	if enrol != nil {
		if id, ok := enrol.GB28181CameraIDByChannel(deviceID, channelID); ok {
			cameraID = id
			if sw, err := enrol.NewGB28181PlaybackSink(id); err == nil && sw != nil {
				sink = sw
				// The sink recorder muxes audio itself when audio_enabled —
				// GB28181Recorder.WriteAudio satisfies this directly.
				if aw, ok2 := sw.(interface {
					WriteAudio(codec string, data, config []byte, ptsTicks int64, samples int)
				}); ok2 {
					audioSink = aw.WriteAudio
				}
			}
		}
	}
	if sink == nil {
		return fmt.Errorf("gb28181: no camera bound to channel %q — cannot persist fetched recording", channelID)
	}

	// Stop any prior fetch for this channel.
	_ = s.StopPlayback(channelID)

	dev, _ := s.deviceMgr.Device(deviceID)
	dev.Mu.RLock()
	netAddr := dev.NetAddr
	dev.Mu.RUnlock()
	serverHost := s.localIPFor(netAddr)

	counter := &countingSink{inner: sink}
	var onAudio gb28181.AudioFrameHandler
	if audioSink != nil {
		onAudio = func(frame gb28181.AudioFrame) {
			audioSink(frame.Codec, frame.Data, frame.Config, frame.PTSTicks, frame.Samples)
		}
	}
	sdp, err := s.sessionMgr.InvitePlayback(ch, serverHost, start, end, counter, onAudio)
	if err != nil {
		return fmt.Errorf("gb28181: playback session for %s: %w", channelID, err)
	}

	st := &playbackState{
		channelID: channelID,
		deviceID:  deviceID,
		cameraID:  cameraID,
		start:     start,
		end:       end,
		startedAt: time.Now(),
		scale:     1.0,
		counter:   counter,
	}
	s.pbMu.Lock()
	s.playbacks[channelID] = st
	s.pbMu.Unlock()

	// INVITE the device for the playback dialog.
	if err := s.sendPlaybackInvite(deviceID, channelID, netAddr, sdp); err != nil {
		s.pbMu.Lock()
		delete(s.playbacks, channelID)
		s.pbMu.Unlock()
		_ = s.sessionMgr.ByePlayback(channelID)
		return err
	}

	go s.watchPlayback(st)
	slog.Info("gb28181: playback fetch started", "channel", channelID, "device", deviceID,
		"camera", cameraID, "start", start, "end", end)
	return nil
}

// playbackState tracks one running fetch.
type playbackState struct {
	channelID string
	deviceID  string
	cameraID  string
	start     time.Time
	end       time.Time
	startedAt time.Time
	paused    bool
	resumedAt time.Time // last resume instant — stall watchdog grace anchor
	scale     float64
	counter   *countingSink
}

// sendPlaybackInvite transmits the s=Playback INVITE and completes the
// handshake, storing the dialog for INFO control and the final BYE.
func (s *Server) sendPlaybackInvite(deviceID, channelID, netAddr string, sdp []byte) error {
	s.mu.Lock()
	srv := s.gosipSrv
	s.mu.Unlock()
	if srv == nil {
		return fmt.Errorf("gb28181: SIP server not started")
	}

	devHost, devPortStr, err := net.SplitHostPort(netAddr)
	if err != nil {
		return fmt.Errorf("gb28181: invalid device address %q: %w", netAddr, err)
	}
	devPort, _ := strconv.Atoi(devPortStr)
	portVal := sip.Port(devPort)
	serverHost := s.localIPFor(netAddr)

	from := &sip.Address{
		DisplayName: sip.String{Str: s.cfg.ServerID},
		Uri:         &sip.SipUri{FUser: sip.String{Str: s.cfg.ServerID}, FHost: serverHost},
	}
	to := &sip.Address{
		DisplayName: sip.String{Str: channelID},
		Uri:         &sip.SipUri{FUser: sip.String{Str: channelID}, FHost: devHost, FPort: &portVal},
	}
	recipient := &sip.SipUri{FUser: sip.String{Str: channelID}, FHost: devHost, FPort: &portVal}
	subject := fmt.Sprintf("%s:%s,%s:0", channelID, sdpSSRC(sdp), s.cfg.ServerID)

	req, err := s.buildRequest(sip.INVITE, serverHost, from, to, recipient, subject, "application/sdp", string(sdp))
	if err != nil {
		return fmt.Errorf("gb28181: build playback INVITE: %w", err)
	}

	tx, err := srv.Request(req)
	if err != nil {
		return fmt.Errorf("gb28181: send playback INVITE: %w", err)
	}
	resp, err := s.awaitInviteAnswer(srv, tx, req)
	if err != nil {
		return fmt.Errorf("gb28181: playback INVITE to %s: %w", channelID, err)
	}
	if resp != nil {
		ack := sip.NewAckRequest("", req, resp, "", nil)
		if err := srv.Send(ack); err != nil {
			slog.Warn("gb28181: playback ACK send failed", "channel", channelID, "error", err)
		}
		s.mu.Lock()
		s.pbDialogs[channelID] = &inviteDialog{req: req, resp: resp}
		s.mu.Unlock()
	}
	return nil
}

// watchPlayback finalizes the fetch when the device's stream ends: silence
// after data (playbackStallTimeout) or a dead stream (playbackStartupTimeout).
func (s *Server) watchPlayback(st *playbackState) {
	for range time.NewTicker(3 * time.Second).C {
		rcv := s.sessionMgr.GetPlaybackReceiver(st.channelID)
		if rcv == nil {
			return // fetch stopped explicitly
		}
		s.pbMu.Lock()
		cur, active := s.playbacks[st.channelID]
		s.pbMu.Unlock()
		if !active || cur != st {
			return
		}
		if !rcv.HasReceivedRTP() {
			if time.Since(st.startedAt) > playbackStartupTimeout {
				slog.Warn("gb28181: playback fetch produced no media — recycling", "channel", st.channelID)
				_ = s.StopPlayback(st.channelID)
			}
			continue
		}
		// A MANSRTSP-paused fetch sends nothing by design — the stall
		// watchdog must not recycle it (only an explicit stop / user stop
		// ends a paused fetch).
		if cur.paused {
			continue
		}
		// After a resume the stall clock restarts: the device gets a full
		// timeout to deliver post-pause media however deep the pause was.
		idle := rcv.SinceLastPacket()
		if !cur.resumedAt.IsZero() {
			if since := time.Since(cur.resumedAt); since < idle {
				idle = since
			}
		}
		if idle > playbackStallTimeout {
			slog.Info("gb28181: playback fetch stream ended", "channel", st.channelID,
				"frames", st.counter.frames.Load())
			_ = s.StopPlayback(st.channelID)
			return
		}
	}
}

// StopPlayback ends the fetch: SIP BYE to the device, teardown (finalizing
// the recording), and state cleanup.
func (s *Server) StopPlayback(channelID string) error {
	s.pbMu.Lock()
	_, active := s.playbacks[channelID]
	if active {
		delete(s.playbacks, channelID)
	}
	s.pbMu.Unlock()
	if !active {
		// Still drain the session in case of a half-open INVITE.
		_ = s.sessionMgr.ByePlayback(channelID)
		return nil
	}
	return s.sessionMgr.ByePlayback(channelID)
}

// PlaybackStatusFor reports the fetch state for a channel.
func (s *Server) PlaybackStatusFor(channelID string) (gb28181.PlaybackInfo, bool) {
	s.pbMu.Lock()
	st, ok := s.playbacks[channelID]
	s.pbMu.Unlock()
	if !ok {
		return gb28181.PlaybackInfo{Active: false, ChannelID: channelID}, false
	}
	var status gb28181.PlaybackInfo
	s.pbMu.Lock()
	status = gb28181.PlaybackInfo{
		Active:       true,
		ChannelID:    channelID,
		DeviceID:     st.deviceID,
		CameraID:     st.cameraID,
		Start:        st.start,
		End:          st.end,
		Frames:       st.counter.frames.Load(),
		LastPTSTicks: st.counter.lastPTS.Load(),
		StartedAt:    st.startedAt,
		Paused:       st.paused,
		Scale:        st.scale,
	}
	s.pbMu.Unlock()
	rcv := s.sessionMgr.GetPlaybackReceiver(channelID)
	if rcv != nil {
		status.Frames = rcv.Metrics()["au_emitted"]
	}
	return status, true
}

// PlaybackControl drives an active fetch via SIP INFO MANSRTSP
// (GB/T 28181-2016 playback control): action pause|resume|seek, optional
// scale (0.25-16) and position (seconds into the recording range).
func (s *Server) PlaybackControl(channelID, action string, scale, position float64) error {
	var body string
	cseq := int(time.Now().UnixNano() % 100000)
	s.pbMu.Lock()
	st, ok := s.playbacks[channelID]
	if !ok {
		s.pbMu.Unlock()
		return fmt.Errorf("gb28181: no active playback for channel %q", channelID)
	}
	switch action {
	case "pause":
		body = fmt.Sprintf("PAUSE MANSRTSP/1.0\r\nCSeq: %d\r\nRange: npt=now-\r\n\r\n", cseq)
		st.paused = true
	case "resume":
		if scale > 0 {
			st.scale = scale
		}
		body = fmt.Sprintf("PLAY MANSRTSP/1.0\r\nCSeq: %d\r\nScale: %.2f\r\nRange: npt=%.3f-\r\n\r\n",
			cseq, st.scale, position)
		st.paused = false
		st.resumedAt = time.Now()
	case "seek":
		npt := position
		if npt < 0 {
			npt = 0
		}
		body = fmt.Sprintf("PLAY MANSRTSP/1.0\r\nCSeq: %d\r\nScale: %.2f\r\nRange: npt=%.3f-\r\n\r\n",
			cseq, st.scale, npt)
	default:
		s.pbMu.Unlock()
		return fmt.Errorf("gb28181: unknown playback action %q (pause|resume|seek)", action)
	}
	s.pbMu.Unlock()
	return s.sendPlaybackInfo(channelID, body)
}

// sendPlaybackInfo transmits an in-dialog SIP INFO with a MANSRTSP body.
func (s *Server) sendPlaybackInfo(channelID string, body string) error {
	s.mu.Lock()
	srv := s.gosipSrv
	dialog := s.pbDialogs[channelID]
	s.mu.Unlock()
	if srv == nil || dialog == nil {
		return fmt.Errorf("gb28181: no playback dialog for channel %q", channelID)
	}
	return s.sendInDialogInfo(srv, dialog, body)
}

// sendInDialogInfo builds and sends an INFO request on an established dialog.
func (s *Server) sendInDialogInfo(srv gosip.Server, dialog *inviteDialog, body string) error {
	fromHdr, hasFrom := dialog.resp.From()
	toHdr, hasTo := dialog.resp.To()
	if !hasFrom || !hasTo {
		return fmt.Errorf("gb28181: dialog missing From/To for INFO")
	}
	callID, hasCallID := dialog.resp.CallID()
	seq := uint(1)
	if cseq, ok := dialog.resp.CSeq(); ok {
		seq = uint(cseq.SeqNo) + 1
	}
	serverHost := s.localIPFor(dialog.req.Recipient().String())
	_, sipPort, err := parseSIPListen(s.cfg.SIPListen)
	if err != nil {
		sipPort = 5060
	}
	sipPortVal := sip.Port(sipPort)

	rb := sip.NewRequestBuilder()
	rb.SetMethod(sip.INFO)
	rb.SetFrom(&sip.Address{DisplayName: fromHdr.DisplayName, Uri: fromHdr.Address})
	rb.SetTo(&sip.Address{DisplayName: toHdr.DisplayName, Uri: toHdr.Address})
	if hasCallID {
		rb.SetCallID(callID)
	}
	rb.SetRecipient(dialog.req.Recipient())
	rb.SetHost(serverHost)
	rb.AddVia(&sip.ViaHop{
		Host:   serverHost,
		Port:   &sipPortVal,
		Params: sip.NewParams().Add("branch", sip.String{Str: sip.GenerateBranch()}),
	})
	rb.SetSeqNo(seq)
	ct := sip.ContentType("Application/MANSRTSP")
	rb.SetContentType(&ct)
	rb.SetBody(body)
	req, err := rb.Build()
	if err != nil {
		return fmt.Errorf("gb28181: build INFO: %w", err)
	}
	if _, err := srv.Request(req); err != nil {
		return fmt.Errorf("gb28181: send INFO: %w", err)
	}
	return nil
}

// sendByeForPlayback transmits the in-dialog SIP BYE for a playback fetch
// (best-effort). Implements the SessionManager playback bye sender contract.
func (s *Server) sendByeForPlayback(channelID string) error {
	s.mu.Lock()
	srv := s.gosipSrv
	dialog := s.pbDialogs[channelID]
	delete(s.pbDialogs, channelID)
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
	_, sipPort, err := parseSIPListen(s.cfg.SIPListen)
	if err != nil {
		sipPort = 5060
	}
	sipPortVal := sip.Port(sipPort)

	rb := sip.NewRequestBuilder()
	rb.SetMethod(sip.BYE)
	rb.SetFrom(&sip.Address{DisplayName: fromHdr.DisplayName, Uri: fromHdr.Address})
	rb.SetTo(&sip.Address{DisplayName: toHdr.DisplayName, Uri: toHdr.Address})
	if hasCallID {
		rb.SetCallID(callID)
	}
	rb.SetRecipient(dialog.req.Recipient())
	rb.SetHost(serverHost)
	rb.AddVia(&sip.ViaHop{
		Host:   serverHost,
		Port:   &sipPortVal,
		Params: sip.NewParams().Add("branch", sip.String{Str: sip.GenerateBranch()}),
	})
	rb.SetSeqNo(seq)
	byeReq, err := rb.Build()
	if err != nil {
		return fmt.Errorf("gb28181: build playback BYE: %w", err)
	}
	if _, err := srv.Request(byeReq); err != nil {
		return fmt.Errorf("gb28181: send playback BYE for %s: %w", channelID, err)
	}
	slog.Info("gb28181: playback BYE sent", "channel", channelID)
	return nil
}

// WriteNALU implements the AUWriter contract with progress counting.
func (c *countingSink) WriteNALU(au [][]byte, ptsTicks int64, isIDR bool) {
	c.inner.WriteNALU(au, ptsTicks, isIDR)
	c.frames.Add(1)
	c.lastPTS.Store(ptsTicks)
}
