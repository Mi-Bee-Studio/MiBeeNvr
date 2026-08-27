package substream

// GB28181 sub-channel pull path (#560): a camera whose probed sub-channel
// code is persisted pulls its sub stream over a GB media session (SIP INVITE
// + RTP/PS) instead of RTSP. The AU callback mirrors pullOnce's contract —
// rebase RTP timestamps onto the cross-session monotonic timeline, refresh
// in-band parameter sets, broadcast on the source hub — but the session's
// lifecycle (stall detection, reconnect) is owned here rather than by the
// recorder-oriented watchSession.

import (
	"context"
	"errors"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model/nalutil"
)

// pullGBOnce runs one GB session to exhaustion. It blocks until the session
// stalls, errors, or ctx is done; the reconnect loop in pull() re-invokes it.
func (m *Manager) pullGBOnce(ctx context.Context, e *entry, backoff *time.Duration) error {
	m.mu.Lock()
	puller := m.gbPuller
	m.mu.Unlock()
	if puller == nil {
		return errors.New("gb28181 sub-stream puller not wired")
	}

	src := e.src
	cameraID := src.cameraID
	if err := puller.EnsureSubChannelRegistered(e.target.GBDeviceID, e.target.GBChannelID); err != nil {
		return err
	}

	// Codec resolution from in-band parameter sets only — the GB SDP answer
	// carries no codec information worth parsing (PS streams declare in-band).
	var codec model.Format
	isH265 := false
	var tsOffset int64
	haveOffset := false
	handleAU := func(au [][]byte, pktTS int64, _ bool) {
		if ctx.Err() != nil {
			return
		}
		if codec == "" {
			switch detectCodecGB(au) {
			case model.FormatH264:
				codec = model.FormatH264
			case model.FormatH265:
				codec = model.FormatH265
				isH265 = true
			}
			if codec == "" {
				return // no parameter-set NALU yet — cannot publish or broadcast
			}
		}
		ts := pktTS
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

	release, err := puller.InviteSubChannel(e.target.GBDeviceID, e.target.GBChannelID, handleAU)
	if err != nil {
		return err
	}

	*backoff = time.Second
	if src.State() != StateLive && codec != "" {
		src.state.Store(StateLive)
	}
	if codec == "" {
		src.state.Store(StateStarting)
	}
	subLogger.Info("gb28181 sub-stream session up", "camera_id", cameraID,
		"device", e.target.GBDeviceID, "channel", e.target.GBChannelID)

	// Stall watchdog: recycle the session when frames stop, so the reconnect
	// loop re-INVITEs (fresh IDR from the device).
	stalled := make(chan struct{})
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
				return
			case <-t.C:
				if since := time.Since(time.Unix(0, src.lastFrameAt.Load())); since > m.cfg.FrameStallTimeout {
					close(stalled)
					return
				}
			}
		}
	}()

	select {
	case <-ctx.Done():
		release()
		return nil
	case <-stalled:
		release()
		return errors.New("gb28181 sub-stream stalled")
	}
}

// detectCodecGB identifies the codec from parameter-set NALUs only (H.264
// SPS/PPS/AUD vs H.265 VPS/SPS/PPS) — the same ambiguity rules as the
// recorder's detectCodec: H.264 slice NALUs collide with H.265 param-set
// slots under the 6-bit shift (0x41→VPS 32), so slices never decide. Returns
// "" until a parameter set arrives.
func detectCodecGB(au [][]byte) model.Format {
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		firstByte := nalu[0]
		if firstByte&0x80 != 0 {
			continue // forbidden_zero bit set — corrupt NALU
		}
		t264 := firstByte & 0x1F
		if t264 == 7 || t264 == 8 || t264 == 9 {
			return model.FormatH264
		}
		if t264 == 1 || t264 == 5 {
			// H.264 slices — ambiguous under the H.265 6-bit shift; wait for
			// a real parameter-set NALU.
			continue
		}
		t265 := (firstByte >> 1) & 0x3F
		if t265 == 32 || t265 == 33 || t265 == 34 {
			return model.FormatH265
		}
	}
	return ""
}
