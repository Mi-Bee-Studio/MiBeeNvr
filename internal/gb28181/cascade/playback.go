package cascade

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181/psmux"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/ghettovoice/gosip/sip"
)

// Device-recording playback (#375 follow-up): the upper platform's s=Playback
// INVITE is answered by streaming the NVR's own recordings for the requested
// window as RTP/PS — the mirror of the platform role's fetch (sip/playback.go).
// Media comes straight from the fMP4 segments on disk (merge.ParseSegment
// sample tables); gaps between recordings are compressed out of the RTP
// timeline; pacing follows the original frame durations at 1x (adjustable via
// MANSRTSP Scale), and the dialog is BYEd when the window is exhausted.

// ntpEpochDelta converts NTP-era seconds (since 1900) to Unix seconds.
const ntpEpochDelta = 2208988800

// pbMaxWindow bounds one playback dialog (the platform-side handler enforces
// the same cap on fetch requests).
const pbMaxWindow = 24 * time.Hour

// playbackSession is one s=Playback / s=Download dialog: local recordings →
// psmux → RTP/UDP toward the upper platform's receive address.
type playbackSession struct {
	svc     *Service
	callID  string
	channel string // GB channel ID the upper platform INVITEd
	camera  string // local camera ID
	start   time.Time
	end     time.Time
	download bool // s=Download: send at file speed, no 1x pacing (#378)

	conn    *net.UDPConn
	dst     *net.UDPAddr
	ssrc    uint32
	rtp     *psmux.RTPPacketizer
	mux     *psmux.Muxer
	sdpBody string

	ctrl chan pbCtrl // MANSRTSP controls (paused 1-bit channel, buffered)
	done chan struct{}

	closed atomic.Bool

	frames atomic.Int64
	bytes  atomic.Int64
}

type pbCtrl struct {
	action string // "pause" | "resume" | "seek"
	pos    float64
	scale  float64
}

// onPlaybackInvite answers an s=Playback (or s=Download, #378) INVITE:
// resolve the channel, look up recordings for the SDP time range, 200 OK with
// a sendonly s= answer, and pump the media. Downloads skip the 1x pacing and
// stream at file speed. 404 when the channel is unknown or the window holds
// no recordings (the platform surfaces that as a fetch error).
func (s *Service) onPlaybackInvite(req sip.Request, callID, channelID string, sd inviteSDP) {
	cameraID, ok := s.cameraOfChannel(channelID)
	if !ok {
		slog.Warn("gb28181-cascade: playback INVITE for unknown channel", "channel", channelID)
		_, _ = s.srv.RespondOnRequest(req, 404, "Unknown Channel", "", nil)
		return
	}
	if !sd.hasT {
		_, _ = s.srv.RespondOnRequest(req, 400, "Playback without time range", "", nil)
		return
	}
	start := time.Unix(sd.t0, 0)
	end := time.Unix(sd.t1, 0)
	if !end.After(start) {
		_, _ = s.srv.RespondOnRequest(req, 400, "Bad time range", "", nil)
		return
	}
	if end.Sub(start) > pbMaxWindow {
		end = start.Add(pbMaxWindow)
	}
	if recs, err := s.playbackRecordings(cameraID, start, end); err == nil && len(recs) == 0 {
		slog.Info("gb28181-cascade: playback INVITE for empty window",
			"channel", channelID, "start", start, "end", end)
		_, _ = s.srv.RespondOnRequest(req, 404, "No Records", "", nil)
		return
	}

	dst := &net.UDPAddr{IP: net.ParseIP(sd.host), Port: sd.port}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		_, _ = s.srv.RespondOnRequest(req, 500, "Internal Error", "", nil)
		return
	}

	sdpName := "Playback"
	if strings.EqualFold(sd.name, "Download") {
		sdpName = "Download"
	}
	ps := &playbackSession{
		svc: s, callID: callID, channel: channelID, camera: cameraID,
		start: start, end: end, download: sdpName == "Download",
		conn: conn, dst: dst, ssrc: sd.ssrc,
		mux:  psmux.New(),
		rtp:  psmux.NewRTPPacketizer(conn, dst, sd.ssrc, uint16(time.Now().UnixNano()&0xFFFF)),
		ctrl: make(chan pbCtrl, 8),
		done: make(chan struct{}),
	}
	ps.sdpBody = fmt.Sprintf(
		"v=0\r\no=- 0 0 IN IP4 %s\r\ns=%s\r\nc=IN IP4 %s\r\nt=%s %s\r\n"+
			"m=video %d RTP/AVP 96\r\na=sendonly\r\na=rtpmap:96 PS/90000\r\ny=%d\r\n",
		ps.localHost(), sdpName, ps.localHost(), sd.rawT0, sd.rawT1, ps.localPort(), sd.ssrc)

	s.mu.Lock()
	s.playbacks[callID] = ps
	s.mu.Unlock()

	_, _ = s.srv.RespondOnRequest(req, 200, "OK", ps.sdpBody, nil)
	go ps.pump()
	slog.Info("gb28181-cascade: fetch INVITE accepted — streaming",
		"kind", sdpName, "channel", channelID, "camera", cameraID, "start", start, "end", end,
		"to", dst.String(), "ssrc", sd.ssrc)
}

// playbackRecordings lists the fMP4 recordings of cameraID overlapping the
// window (oldest first). AVI/MJPEG recordings are not PS-muxable. The query
// start is padded — ListRecordings filters on "started_at within window", so
// a recording straddling the window start would otherwise be missed; the
// sample-level wall-time trim in playOnce aligns playback to the exact edge.
func (s *Service) playbackRecordings(cameraID string, start, end time.Time) ([]model.Recording, error) {
	recs, err := s.db.ListRecordings(context.Background(), model.RecordingFilter{
		CameraID:  cameraID,
		StartTime: start.Add(-2 * time.Hour),
		EndTime:   end,
		Limit:     2000,
		SortBy:    "started_at",
		SortOrder: "asc",
	})
	if err != nil {
		return nil, err
	}
	out := recs[:0]
	for _, r := range recs {
		if r.Format == model.FormatH264 || r.Format == model.FormatH265 {
			out = append(out, r)
		}
	}
	return out, nil
}

func (ps *playbackSession) localHost() string {
	h, _ := ps.svc.localHostPort()
	return h
}

func (ps *playbackSession) localPort() int {
	_, p := ps.svc.localHostPort()
	return p
}

// pump drives playOnce passes until the window is exhausted (then BYE) or a
// seek restarts the pass. Errors tear the dialog down with a BYE.
func (ps *playbackSession) pump() {
	seekNPT := 0.0
	for !ps.closed.Load() {
		done, nextSeek, err := ps.playOnce(seekNPT)
		if err != nil {
			ps.finish("send error: "+err.Error(), true)
			return
		}
		if ps.closed.Load() {
			return
		}
		if nextSeek != nil {
			seekNPT = *nextSeek
			continue
		}
		if done {
			break
		}
	}
	ps.finish("end of media", true)
}

// playOnce streams the window once (skipping to seekNPT seconds past the
// window start). Returns (windowDone, seekRequest, fatalErr).
func (ps *playbackSession) playOnce(seekNPT float64) (bool, *float64, error) {
	recs, err := ps.svc.playbackRecordings(ps.camera, ps.start, ps.end)
	if err != nil {
		slog.Warn("gb28181-cascade: playback recordings query failed",
			"camera", ps.camera, "error", err)
		return true, nil, nil
	}

	var (
		started    bool
		firstCum90 int64 // cum90 at the first sent sample (RTP timeline zero)
		cum90      int64 // accumulated 90kHz ticks across ALL samples (incl. skipped)
		num90      int64 // sub-tick remainder for timescale ≠ 90k
		pausedAt   time.Time
		paused     bool
		scale      = 1.0
		base       time.Time // wall time that maps to RTP tick 0
	)

	for _, rec := range recs {
		if ps.closed.Load() {
			return false, nil, nil
		}
		if !rec.EndedAt.After(ps.start) || rec.StartedAt.After(ps.end) {
			continue
		}
		seg, err := merge.ParseSegment(rec.FilePath)
		if err != nil {
			// In-progress segments have no moov yet; unparseable ones can't be
			// streamed — skip rather than fail the whole dialog.
			slog.Debug("gb28181-cascade: playback skips unparseable recording",
				"path", rec.FilePath, "error", err)
			continue
		}
		if seg.Codec != "h264" && seg.Codec != "h265" {
			continue
		}
		f, err := os.Open(rec.FilePath)
		if err != nil {
			slog.Debug("gb28181-cascade: playback cannot open recording",
				"path", rec.FilePath, "error", err)
			continue
		}
		ps.mux.SetVideoCodec(seg.Codec)

		var durAcc uint64 // sample-time units since recording start
		for _, smp := range seg.Samples {
			wall := rec.StartedAt.Add(time.Duration(durAcc * uint64(time.Second) / uint64(seg.Timescale)))
			durAcc += uint64(smp.Duration)

			num90 += int64(smp.Duration) * 90000
			cum90 += num90 / int64(seg.Timescale)
			num90 %= int64(seg.Timescale)

			if !started {
				if wall.Before(ps.start) {
					continue
				}
				if wall.Sub(ps.start).Seconds() < seekNPT {
					continue
				}
				if !smp.IsKeyFrame {
					continue
				}
				started = true
				firstCum90 = cum90
				base = time.Now()
			} else if wall.After(ps.end) {
				_ = f.Close()
				return true, nil, nil
			}

			rel := cum90 - firstCum90

			// Pace + controls. s=Download dialogs send at file speed — the
			// pacing wait collapses to just the pause/select drain.
			for {
				if ps.closed.Load() {
					_ = f.Close()
					return false, nil, nil
				}
				if paused {
					select {
					case c := <-ps.ctrl:
						if seek, stop := ps.applyCtrl(c, &paused, &pausedAt, &scale, &base, rel); stop || seek != nil {
							_ = f.Close()
							return false, seek, nil
						}
						continue
					case <-ps.done:
						_ = f.Close()
						return false, nil, nil
					}
				}
				if ps.download {
					select {
					case c := <-ps.ctrl:
						if seek, stop := ps.applyCtrl(c, &paused, &pausedAt, &scale, &base, rel); stop || seek != nil {
							_ = f.Close()
							return false, seek, nil
						}
					default:
					}
					break
				}
				target := base.Add(time.Duration(float64(rel) / 90 / scale * float64(time.Millisecond)))
				dLeft := time.Until(target)
				if dLeft <= 0 {
					break
				}
				sleep := dLeft
				if sleep > 200*time.Millisecond {
					sleep = 200 * time.Millisecond
				}
				select {
				case c := <-ps.ctrl:
					if seek, stop := ps.applyCtrl(c, &paused, &pausedAt, &scale, &base, rel); stop || seek != nil {
						_ = f.Close()
						return false, seek, nil
					}
				case <-time.After(sleep):
				case <-ps.done:
					_ = f.Close()
					return false, nil, nil
				}
			}

			buf := make([]byte, smp.Size)
			if _, err := f.ReadAt(buf, int64(smp.Offset)); err != nil {
				break // truncated tail — jump to the next recording
			}
			annexB := avccToAnnexB(nil, buf)
			if smp.IsKeyFrame {
				annexB = prependParamSets(annexB, seg)
			}
			psTS := ps.mux.WriteAU(annexB, rel, smp.IsKeyFrame)
			if err := ps.rtp.Send(psTS, rel); err != nil {
				_ = f.Close()
				return false, nil, err
			}
			ps.frames.Add(1)
			ps.bytes.Add(int64(len(psTS)))
		}
		_ = f.Close()
	}
	return true, nil, nil
}

// applyCtrl folds one MANSRTSP control into the pacing state. rel is the
// current sample's RTP tick. base maps rel → due time (due = base +
// rel/90/scale); a resume (with or without a scale change) re-anchors base so
// the CURRENT frame is due immediately — computing that in two steps (re-anchor
// for scale, then shift by the paused span) stacks both offsets and defers
// media by the whole pause length.
// Returns (seekTarget, stop).
func (ps *playbackSession) applyCtrl(c pbCtrl, paused *bool, pausedAt *time.Time, scale *float64, base *time.Time, rel int64) (*float64, bool) {
	switch c.action {
	case "pause":
		if !*paused {
			*paused = true
			*pausedAt = time.Now()
		}
	case "resume":
		if c.scale > 0 {
			*scale = c.scale
		}
		*paused = false
		*base = time.Now().Add(-time.Duration(float64(rel) / 90 / *scale * float64(time.Millisecond)))
	case "seek":
		npt := c.pos
		return &npt, false
	}
	return nil, false
}

// finish closes the dialog. reason is logged; bye sends an in-dialog BYE
// (natural end + errors — not the service Stop path).
func (ps *playbackSession) finish(reason string, bye bool) {
	if !ps.closed.CompareAndSwap(false, true) {
		return
	}
	close(ps.done)
	ps.svc.mu.Lock()
	delete(ps.svc.playbacks, ps.callID)
	ps.svc.mu.Unlock()
	_ = ps.conn.Close()
	if bye {
		ps.svc.sendBye(ps.callID, ps.channel)
	}
	slog.Info("gb28181-cascade: playback ended",
		"channel", ps.channel, "reason", reason,
		"frames", ps.frames.Load(), "bytes", ps.bytes.Load())
}

// stop is the service Stop()-path teardown (blanket unregister, no BYE).
func (ps *playbackSession) stop() {
	if !ps.closed.CompareAndSwap(false, true) {
		return
	}
	close(ps.done)
	_ = ps.conn.Close()
}

// ---- SIP INFO (MANSRTSP) ----

// onInfo answers the upper platform's in-dialog INFO and routes MANSRTSP
// playback controls to the channel's playback session.
func (s *Service) onInfo(req sip.Request, _ sip.ServerTransaction) {
	_, _ = s.srv.RespondOnRequest(req, 200, "OK", "", nil)
	callID := ""
	if h, ok := req.CallID(); ok {
		callID = h.String()
	}
	s.mu.Lock()
	ps := s.playbacks[callID]
	s.mu.Unlock()
	if ps == nil {
		return // INFO on a live-forward dialog: nothing to control
	}

	m := parseMANSRTSP(string(req.Body()))
	switch pbActionFor(m) {
	case "pause":
		ps.postCtrl(pbCtrl{action: "pause"})
	case "resume":
		ps.postCtrl(pbCtrl{action: "resume", scale: m.scale})
	case "seek":
		ps.postCtrl(pbCtrl{action: "seek", pos: m.npt})
	}
}

// pbActionFor maps a parsed MANSRTSP message to a playback control action.
// Our own platform sends "Range: npt=0.000-" on plain resumes — only a
// materially different position is a real seek.
func pbActionFor(m mansrtspMsg) string {
	switch m.method {
	case "PAUSE":
		return "pause"
	case "PLAY":
		if m.hasNPT && m.npt > 1.0 {
			return "seek"
		}
		return "resume"
	}
	return ""
}

func (ps *playbackSession) postCtrl(c pbCtrl) {
	select {
	case ps.ctrl <- c:
	default: // a lost pause/resume is recoverable; never block SIP I/O
	}
}

// mansrtspMsg is a parsed MANSRTSP body.
type mansrtspMsg struct {
	method string // "PAUSE" | "PLAY" ("" unparseable)
	scale  float64
	npt    float64
	hasNPT bool
}

// parseMANSRTSP parses "PAUSE MANSRTSP/1.0\r\nCSeq: 1\r\nScale: 4.00\r\nRange:
// npt=12.500-\r\n\r\n" (GB/T 28181-2016 §9.4.3). npt=now / npt=0 are flagged
// hasNPT=false — callers treat them as "from here".
func parseMANSRTSP(body string) mansrtspMsg {
	var m mansrtspMsg
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	if len(lines) == 0 {
		return m
	}
	first := strings.Fields(strings.TrimSpace(lines[0]))
	if len(first) < 2 || !strings.HasPrefix(strings.ToUpper(first[1]), "MANSRTSP") {
		return m
	}
	switch strings.ToUpper(first[0]) {
	case "PAUSE":
		m.method = "PAUSE"
	case "PLAY":
		m.method = "PLAY"
	default:
		return mansrtspMsg{}
	}
	for _, ln := range lines[1:] {
		ln = strings.TrimSpace(ln)
		switch {
		case strings.HasPrefix(ln, "Scale:"):
			if v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(ln, "Scale:")), 64); err == nil {
				m.scale = v
			}
		case strings.HasPrefix(ln, "Range:"):
			r := strings.TrimSpace(strings.TrimPrefix(ln, "Range:"))
			if v, ok := strings.CutPrefix(r, "npt="); ok {
				sec := strings.TrimSpace(strings.TrimSuffix(v, "-"))
				if f, err := strconv.ParseFloat(sec, 64); err == nil && f > 0 {
					m.npt = f
					m.hasNPT = true
				}
			}
		}
	}
	return m
}

// ---- AVCC → Annex B ----

// avccToAnnexB converts MP4 sample bytes (4-byte big-endian length-prefixed
// NALUs) to Annex B (4-byte start codes), appending to dst.
func avccToAnnexB(dst, avcc []byte) []byte {
	for len(avcc) >= 4 {
		n := int(binary.BigEndian.Uint32(avcc[:4]))
		if n < 0 || 4+n > len(avcc) {
			break // malformed tail
		}
		dst = append(dst, 0, 0, 0, 1)
		dst = append(dst, avcc[4:4+n]...)
		avcc = avcc[4+n:]
	}
	return dst
}

// prependParamSets rebuilds an Annex B AU with the segment's parameter sets
// ahead of the payload (VPS/SPS/PPS for H.265, SPS/PPS for H.264) — decoders
// joining mid-stream resync on every IDR.
func prependParamSets(annexB []byte, seg *merge.SegmentInfo) []byte {
	var ps [][]byte
	if seg.Codec == "h265" {
		ps = append(ps, seg.VPS, seg.SPS, seg.PPS)
	} else {
		ps = append(ps, seg.SPS, seg.PPS)
	}
	out := make([]byte, 0, len(annexB)+64)
	for _, p := range ps {
		if len(p) == 0 {
			continue
		}
		out = append(out, 0, 0, 0, 1)
		out = append(out, p...)
	}
	return append(out, annexB...)
}
