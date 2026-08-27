package substream

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model/nalutil"
	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph264"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph265"
	"github.com/pion/rtp"
)

// errNoVideo marks an SDP that carries neither H.264 nor H.265 — permanent
// for this target, the pull gives up instead of retrying.
var errNoVideo = errors.New("no H.264/H.265 video track in sub-stream SDP")

// sessionGapPTS is the timeline gap (90 kHz units, 1s) inserted between RTP
// sessions when rebasing timestamps, so a reconnect never emits a backwards
// or collapsing timestamp (see pullOnce).
const sessionGapPTS = 90000

// pull runs the reconnect loop for one source until the entry is cancelled.
// Exits when ctx is done, or after a permanent failure (leaving the source in
// StateFailed and scheduling a self-recycle so a later Acquire re-resolves
// the target from scratch instead of erroring forever).
func (m *Manager) pull(ctx context.Context, e *entry) {
	defer close(e.done)

	cameraID := e.src.cameraID
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		var err error
		if e.target.Kind == KindGB28181 {
			err = m.pullGBOnce(ctx, e, &backoff)
		} else {
			err = m.pullOnce(ctx, e, &backoff)
		}
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errNoVideo) {
			e.src.state.Store(StateFailed)
			subLogger.Error("sub-stream pull failed permanently", "camera_id", cameraID, "error", err)
			go func() {
				select {
				case <-ctx.Done():
				case <-time.After(m.cfg.IdleTimeout):
				}
				m.recycle(cameraID, "failed")
			}()
			return
		}
		e.src.state.Store(StateReconnecting)
		subLogger.Warn("sub-stream pull error, reconnecting", "camera_id", cameraID,
			"error", err, "backoff", backoff.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > 5*time.Second {
			backoff = 5 * time.Second
		}
	}
}

// pullOnce dials the target, plays the video track, and blocks until the
// session fails, stalls, or ctx is done. backoff is reset once the session
// reaches PLAY.
//
// RTP timestamps are per-session random bases; downstream muxers
// (FLV/HLS/WS) require a monotonic timeline, so each session is REBASED to
// continue 1s after the previous session's high-water mark (entry.lastPTS —
// same failure class as #506's dts collapse otherwise).
func (m *Manager) pullOnce(ctx context.Context, e *entry, backoff *time.Duration) error {
	rawURL := e.target.URL
	if e.target.Username != "" {
		rawURL = injectCredentials(rawURL, e.target.Username, e.target.Password)
	}
	u, err := base.ParseURL(rawURL)
	if err != nil {
		return fmt.Errorf("invalid sub-stream URL: %w", err)
	}

	tcp := gortsplib.ProtocolTCP
	client := &gortsplib.Client{
		Scheme:      u.Scheme,
		Host:        u.Host,
		Protocol:    &tcp,
		ReadTimeout: m.cfg.DialTimeout,
		// Keep writes lenient: slow cameras with large IDRs can exceed the
		// dial budget on the RTCP feedback path.
		WriteTimeout: m.cfg.DialTimeout * 2,
	}
	// gortsplib's Client.Close() dereferences a nil ctxCancel when Start was
	// never reached (same class as the server-side Close panic fixed in
	// #524) — every Close goes through this guard. Start's dial is bounded
	// by ReadTimeout (= DialTimeout) via connOpen's context, so the window
	// where the guard suppresses a Close cannot hang the session.
	started := &atomic.Bool{}
	closeClient := func() {
		if started.Load() {
			client.Close()
		}
	}
	defer closeClient()

	// Bound the whole setup sequence regardless of gortsplib's internal
	// handshake behavior (e.g. a camera that accepts TCP then never answers
	// DESCRIBE). Closing the client unblocks any in-flight handshake call.
	setupTimer := time.AfterFunc(m.cfg.DialTimeout*2, closeClient)

	if err = client.Start(); err != nil {
		setupTimer.Stop()
		return fmt.Errorf("sub-stream connect: %w", err)
	}
	started.Store(true)

	desc, _, err := client.Describe(u)
	if err != nil {
		setupTimer.Stop()
		return fmt.Errorf("sub-stream DESCRIBE: %w", err)
	}

	var (
		medi   *description.Media
		isH265 bool
		codec  model.Format
		h264f  *format.H264
		h265f  *format.H265
	)
	// SDP parameter sets are an ACCELERATOR, not a requirement: cameras that
	// emit them only in-band present an SDP without sprop-parameter-sets, and
	// rejecting those here misclassified a perfectly good sub stream as
	// "no video track" (observed on an M5 ONVIF camera, #513 field test).
	// Without SDP params the source becomes ready on the first in-band
	// parameter set (refreshParamSets publishes and closes ready).
	if m264 := desc.FindFormat(&h264f); m264 != nil {
		medi, isH265, codec = m264, false, model.FormatH264
	} else if m265 := desc.FindFormat(&h265f); m265 != nil {
		medi, isH265, codec = m265, true, model.FormatH265
	}
	if medi == nil {
		setupTimer.Stop()
		return errNoVideo
	}

	if isH265 {
		if len(h265f.SPS) > 0 && len(h265f.PPS) > 0 && len(h265f.VPS) > 0 {
			e.src.publishParams(codec, h265f.SPS, h265f.PPS, h265f.VPS)
		}
	} else if len(h264f.SPS) > 0 && len(h264f.PPS) > 0 {
		e.src.publishParams(codec, h264f.SPS, h264f.PPS, nil)
	}

	if _, err = client.Setup(desc.BaseURL, medi, 0, 0); err != nil {
		setupTimer.Stop()
		return fmt.Errorf("sub-stream SETUP: %w", err)
	}

	src := e.src
	src.lastFrameAt.Store(time.Now().UnixNano())

	// Session-local rebase state: the first frame of this session continues
	// the cross-session timeline (entry.lastPTS, updated atomically below).
	var tsOffset int64
	haveOffset := false
	handleAU := func(au [][]byte, pktTS uint32) {
		ts := int64(pktTS)
		if !haveOffset {
			haveOffset = true
			if prev := e.lastPTS.Load(); prev > 0 {
				tsOffset = prev + sessionGapPTS - ts
			}
		}
		pts := ts + tsOffset
		if minGap := e.lastPTS.Load() + 3600; pts < minGap { // ~40ms floor
			pts = minGap
		}
		e.lastPTS.Store(pts)
		refreshParamSets(src, codec, au)
		src.lastFrameAt.Store(time.Now().UnixNano())
		src.hub.Broadcast(pts, au, nalutil.IsIDR(au, isH265))
	}

	if isH265 {
		rtpDec, derr := h265f.CreateDecoder()
		if derr != nil {
			setupTimer.Stop()
			return fmt.Errorf("sub-stream RTP decoder: %w", derr)
		}
		client.OnPacketRTP(medi, h265f, func(pkt *rtp.Packet) {
			if ctx.Err() != nil {
				return
			}
			au, decErr := rtpDec.Decode(pkt)
			if decErr != nil {
				if !errors.Is(decErr, rtph265.ErrNonStartingPacketAndNoPrevious) &&
					!errors.Is(decErr, rtph265.ErrMorePacketsNeeded) {
					subLogger.Warn("sub-stream RTP decode error", "camera_id", src.cameraID, "error", decErr)
				}
				return
			}
			handleAU(au, pkt.Timestamp)
		})
	} else {
		rtpDec, derr := h264f.CreateDecoder()
		if derr != nil {
			setupTimer.Stop()
			return fmt.Errorf("sub-stream RTP decoder: %w", derr)
		}
		client.OnPacketRTP(medi, h264f, func(pkt *rtp.Packet) {
			if ctx.Err() != nil {
				return
			}
			au, decErr := rtpDec.Decode(pkt)
			if decErr != nil {
				if !errors.Is(decErr, rtph264.ErrNonStartingPacketAndNoPrevious) &&
					!errors.Is(decErr, rtph264.ErrMorePacketsNeeded) {
					subLogger.Warn("sub-stream RTP decode error", "camera_id", src.cameraID, "error", decErr)
				}
				return
			}
			handleAU(au, pkt.Timestamp)
		})
	}

	if _, err = client.Play(nil); err != nil {
		setupTimer.Stop()
		return fmt.Errorf("sub-stream PLAY: %w", err)
	}
	setupTimer.Stop()
	*backoff = time.Second
	src.state.Store(StateLive)
	subLogger.Info("sub-stream live", "camera_id", src.cameraID, "codec", string(codec), "url", redactURL(rawURL))

	// Stall watchdog: closing the client forces Wait() to return, which the
	// reconnect loop turns into a fresh session.
	watchStop := make(chan struct{})
	defer close(watchStop)
	go func() {
		t := time.NewTicker(m.cfg.FrameStallTimeout / 3)
		defer t.Stop()
		for {
			select {
			case <-watchStop:
				return
			case <-ctx.Done():
				closeClient()
				return
			case <-t.C:
				if since := time.Since(time.Unix(0, src.lastFrameAt.Load())); since > m.cfg.FrameStallTimeout {
					subLogger.Warn("sub-stream stalled, reconnecting", "camera_id", src.cameraID,
						"silent_for", since.Round(time.Second).String())
					closeClient()
					return
				}
			}
		}
	}()

	waitErr := client.Wait()
	if ctx.Err() != nil {
		return nil
	}
	return waitErr
}

// refreshParamSets publishes in-band SPS/PPS(/VPS) when they differ from the
// current snapshot (encoder reconfigured mid-session — resolution change,
// profile change). Missing pieces carry forward from the current snapshot:
// some cameras emit the parameter sets in SEPARATE access units ([SPS][PPS]
// [IDR]), so each AU only has to complete the set eventually. AUs are passed
// through unfiltered, matching main recorders: in-band parameter sets inside
// IDR AUs reach consumers as-is.
func refreshParamSets(src *Source, codec model.Format, au [][]byte) {
	var sps, pps, vps []byte
	if codec == model.FormatH265 {
		vps, sps, pps = nalutil.ExtractParamSetsH265(au)
	} else {
		sps, pps = nalutil.ExtractParamSetsH264(au)
	}
	cur := src.params.Load()
	if cur != nil {
		if len(vps) == 0 {
			vps = cur.vps
		}
		if len(sps) == 0 {
			sps = cur.sps
		}
		if len(pps) == 0 {
			pps = cur.pps
		}
	}
	if len(sps) == 0 || len(pps) == 0 || (codec == model.FormatH265 && len(vps) == 0) {
		return
	}
	if cur != nil &&
		nalutil.EqualParamSets(cur.sps, sps) && nalutil.EqualParamSets(cur.pps, pps) &&
		nalutil.EqualParamSets(cur.vps, vps) {
		return
	}
	src.publishParams(codec, sps, pps, vps)
}

// injectCredentials embeds user:pass userinfo into an rtsp:// URL that lacks
// it. gortsplib answers WWW-Authenticate challenges from the URL userinfo;
// ONVIF GetStreamUri frequently returns credential-less URIs.
func injectCredentials(rawURL, user, pass string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.User != nil {
		return rawURL
	}
	u.User = url.UserPassword(user, pass)
	return u.String()
}

// redactURL strips userinfo for logging.
func redactURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.User == nil {
		return rawURL
	}
	u.User = nil
	return u.String()
}
