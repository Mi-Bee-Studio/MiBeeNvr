package peer

import (
	"errors"
	"fmt"
	"time"

	"github.com/pion/webrtc/v4"
)

// iceRestartGracePeriod is the time to wait after ICE Disconnected
// before triggering an ICE restart via Failed handling.
const iceRestartGracePeriod = 5 * time.Second

// maxICERestartFailures is the number of consecutive failed ICE restart
// rounds after which the session is abandoned: an offer that goes
// unanswered for this long means the peer is gone, so the session is torn
// down instead of retrying (and error-logging) forever.
const maxICERestartFailures = 3

// iceRestartRetryInterval is how long after a restart round (sent or
// deferred) the next round is attempted. ICE restart is otherwise purely
// event-driven, and a dead peer produces exactly one Disconnected→Failed
// transition — without this periodic driver the failure counter would
// stall and the session would never be reaped.
var iceRestartRetryInterval = 30 * time.Second

// beginGracePeriod arms the disconnected→restart grace timer: a transient
// disconnection heals itself; only one persisting past the grace window
// escalates to a restart offer.
func (p *p2pSession) beginGracePeriod() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.iceConnected = false
	p.recoveryStart = time.Now()
	if p.graceTimer != nil {
		p.graceTimer.Stop()
	}
	p.graceTimer = time.AfterFunc(iceRestartGracePeriod, p.onGraceExpired)
}

// markICERecovered clears every restart bookkeeping surface and signals
// watchTransport that the direct path is usable (the DataChannel OnOpen
// callback also closes iceReady, but on some pion versions the offerer's
// OnOpen never fires — SCTP association race — so ICE connected is the
// authoritative signal).
func (p *p2pSession) markICERecovered() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.iceConnected = true
	if p.graceTimer != nil {
		p.graceTimer.Stop()
		p.graceTimer = nil
	}
	if p.restartTimer != nil {
		p.restartTimer.Stop()
		p.restartTimer = nil
	}
	p.restartFailures = 0
	if !p.recoveryStart.IsZero() {
		elapsed := time.Since(p.recoveryStart)
		p.log().Info("ICE recovered", "duration_seconds", elapsed.Seconds())
		p.recoveryStart = time.Time{}
	}
	if p.iceReady != nil {
		select {
		case <-p.iceReady:
		default:
			close(p.iceReady)
		}
	}
}

// handleICEStateChange processes ICE connection state transitions for
// automatic ICE restart (session resilience). Called from pion's ICE
// management goroutine; must be goroutine-safe.
//
// Only the offerer initiates restarts to prevent glare — both sides
// racing with conflicting offers.
func (p *p2pSession) handleICEStateChange(state webrtc.ICEConnectionState) {
	switch state {
	case webrtc.ICEConnectionStateDisconnected:
		p.beginGracePeriod()

	case webrtc.ICEConnectionStateFailed:
		p.mu.Lock()
		p.iceConnected = false
		if p.graceTimer != nil {
			p.graceTimer.Stop()
			p.graceTimer = nil
		}
		isOfferer := p.isOfferer
		p.mu.Unlock()

		if !isOfferer {
			p.log().Debug("ICE failed, waiting for offerer restart")
			return
		}
		// relay-adopted sessions do not restart ICE (the transport is the
		// shared relay WSS, not this PC): logging "triggering restart" here
		// promised action that attemptICERestart then silently skipped —
		// dead viewer sessions looked like restart machinery was running
		// while the only reaper left was the idle sweep (2026-10-14:
		// screen-off phone sessions stacked up to the client cap).
		if p.relayActiveLocked() {
			p.log().Debug("ICE failed on relay-adopted session; idle sweep owns reaping")
			return
		}
		p.log().Info("ICE failed, triggering restart")
		p.attemptICERestart()

	case webrtc.ICEConnectionStateConnected:
		p.markICERecovered()
	case webrtc.ICEConnectionStateUnknown,
		webrtc.ICEConnectionStateNew,
		webrtc.ICEConnectionStateChecking,
		webrtc.ICEConnectionStateCompleted,
		webrtc.ICEConnectionStateClosed:
		// No action needed for these states.
	}
}

// onGraceExpired is called by the grace timer after 5s in Disconnected state.
// The offerer triggers a restart; the answerer waits for the offerer's offer.
func (p *p2pSession) onGraceExpired() {
	p.mu.Lock()
	isOfferer := p.isOfferer
	relayActive := p.relayActive
	p.graceTimer = nil
	p.mu.Unlock()

	if !isOfferer {
		p.log().Debug("ICE disconnected grace expired, waiting for offerer restart")
		return
	}
	if relayActive {
		p.log().Debug("ICE grace expired on relay-adopted session; idle sweep owns reaping")
		return
	}
	p.log().Info("ICE disconnected grace expired, triggering restart")
	p.attemptICERestart()
}

// relayActiveLocked reports whether this session adopted the relay transport.
// Callers hold p.mu.
func (p *p2pSession) relayActiveLocked() bool { return p.relayActive }

// attemptICERestart runs one ICE restart round with failure accounting,
// and keeps rounds coming while the outcome is unresolved.
//
// A round fails when the offer cannot be created/sent, or when the previous
// (re)start offer is still unanswered. After maxICERestartFailures
// consecutive failed rounds the session is abandoned — the peer is presumed
// gone — instead of looping forever. A successful round (offer sent) or an
// ICE recovery resets the counter.
//
// Because ICE state changes are event-driven and a dead peer produces only
// one Disconnected→Failed transition, every round also (re)arms a retry
// timer: if the offer is still unanswered and ICE has not recovered when
// it fires, another round runs. The timer is stopped on ICE recovery and
// session close.
func (p *p2pSession) attemptICERestart() {
	p.mu.Lock()
	if !p.isOfferer || p.relayActive {
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()

	err := p.triggerICERestart()

	p.mu.Lock()
	if err == nil {
		p.restartFailures = 0
	} else {
		p.restartFailures++
		if p.restartFailures >= maxICERestartFailures {
			p.stopRestartTimerLocked()
			failures := p.restartFailures
			p.mu.Unlock()
			p.log().Error("ICE restart failed", "error", err, "consecutive_failures", failures)
			// Async: we may be on pion's ICE-state callback goroutine, and
			// close() joins the ICE agent — calling it inline would deadlock.
			go p.abandonSession("ice_restart_failed")
			return
		}
	}
	if p.restartTimer == nil {
		p.restartTimer = time.AfterFunc(iceRestartRetryInterval, p.onRestartRetry)
	}
	failures := p.restartFailures
	p.mu.Unlock()

	if err != nil {
		p.log().Error("ICE restart failed", "error", err, "consecutive_failures", failures)
	}
}

// stopRestartTimerLocked stops and clears the restart retry timer. The
// caller holds p.mu.
func (p *p2pSession) stopRestartTimerLocked() {
	if p.restartTimer != nil {
		p.restartTimer.Stop()
		p.restartTimer = nil
	}
}

// onRestartRetry is the periodic driver behind attemptICERestart: it runs
// the next round unless ICE has recovered in the meantime.
func (p *p2pSession) onRestartRetry() {
	p.mu.Lock()
	p.restartTimer = nil
	if p.iceConnected {
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	p.attemptICERestart()
}

// abandonSession tears the session down locally and notifies the remote peer,
// mirroring remote session_close handling (registry delete + close). Used
// once repeated ICE restart failures make the peer's disappearance
// conclusive. Caller must not hold p.mu.
func (p *p2pSession) abandonSession(reason string) {
	p.log().Warn(
		"abandoning P2P session after repeated ICE restart failures",
		"session_id", p.sessionID, "reason", reason,
	)
	p.teardownWithReason(reason)
}

// triggerICERestart creates a new SDP offer with ICERestart flag set and
// sends it via the existing sdp_offer signaling message.
//
// Only the offerer should call this method. Caller MUST NOT hold p.mu.
func (p *p2pSession) triggerICERestart() error {
	p.mu.Lock()

	if !p.isOfferer {
		p.mu.Unlock()
		return nil
	}
	if p.relayActive {
		// The session runs on the relay fallback transport; direct-path ICE
		// restarts are moot and must not count toward session abandonment.
		p.mu.Unlock()
		p.log().Debug("ICE restart skipped: relay transport active")
		return nil
	}
	if p.pc == nil {
		p.mu.Unlock()
		return errors.New("no peer connection for restart")
	}
	if state := p.pc.SignalingState(); state != webrtc.SignalingStateStable {
		// The previous (re)start offer is still unanswered. SetLocal(offer)
		// from have-local-offer is an illegal transition
		// (InvalidModificationError), and the pending offer already carries
		// our best effort — count this round as failed and let the failure
		// cap decide the session's fate.
		p.mu.Unlock()
		return fmt.Errorf("restart deferred: previous offer unanswered (signaling state %s)", state)
	}

	p.log().Info(
		"triggering ICE restart",
		"session_id", p.sessionID,
	)

	offer, err := p.pc.CreateOffer(&webrtc.OfferOptions{ICERestart: true})
	if err != nil {
		p.mu.Unlock()
		return fmt.Errorf("create restart offer: %w", err)
	}

	if err := p.pc.SetLocalDescription(offer); err != nil {
		p.mu.Unlock()
		return fmt.Errorf("set local desc for restart: %w", err)
	}

	// Release lock while ICE candidates gather.
	p.mu.Unlock()
	<-webrtc.GatheringCompletePromise(p.pc)
	p.mu.Lock()

	finalOffer := p.pc.LocalDescription()
	if finalOffer == nil {
		p.mu.Unlock()
		return errors.New("no local description after ICE restart")
	}

	err = p.signal.sendRaw(MessageTypeSDPOffer, SDPOffer{
		SessionID: p.sessionID,
		SDP:       finalOffer.SDP,
	})
	p.mu.Unlock()
	return err
}
