package peer

// Relay session-ticket refresh (#143, the v3 prerequisite).
//
// A relay ticket is minted at welcome time and admits exactly ONE relay
// connection: its jti may be used twice (the data and the ctrl leg of the
// original dial), and it expires relayticket.DefaultTTL (15 minutes) after
// the welcome. A long-lived session outlives both facts, so the welcome
// ticket cannot re-admit a rebuilt relay connection — with the relay gate
// on, every rebuild after a relay drop would be closed with 4011 and the
// session's relay fallback would stay dead until signaling reconnected.
//
// So a rebuild asks signaling for a fresh ticket first, mirroring the #132
// ice_refresh pattern: the request rides the hello-authenticated link, and
// the server mints only for the relay that connection was welcomed with.
// Nothing about the peer's entitlement is re-evaluated here — the ticket
// says "signaling admitted this connection", which the live link proves.

import (
	"context"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/relay"
)

// relayTicketReplyWait bounds how long a rebuild waits for the answer. The
// link is alive (the request just went out on it), so this only covers a
// slow or pre-#143 server; past it the dial goes ahead without a ticket.
const relayTicketReplyWait = 3 * time.Second

// relayTicketWait is one outstanding relay_ticket request: the relay the
// ticket must be bound to, and where the matching answer is delivered.
type relayTicketWait struct {
	answer      chan string
	relayPubKey string
}

// armFreshRelayTicket equips rc for a relay dial of w's relay with a valid
// ticket (#143): a fresh one from signaling when the welcome carried one.
// Skipped entirely when the welcome carried no ticket — the feature is off
// or the server predates it — so gate-off deployments keep the exact
// ticket-less dial they had. Best-effort: without an answer rc dials
// ticket-less, which is the legacy path while the relay gate is off.
func (s *Session) armFreshRelayTicket(ctx context.Context, rc *relay.Client, w *Welcome) {
	if w.RelayTicket == "" {
		return
	}
	ticket, ok := s.requestRelayTicket(ctx, w.RelayURL, w.RelayPubKey)
	if !ok {
		return
	}
	rc.SetTicket(ticket)
}

// requestRelayTicket asks signaling for a ticket bound to relayPubKey and
// waits for the matching answer. ok=false when none arrived in time, the
// grant was empty, or the session is closing — the caller then dials
// without a ticket.
func (s *Session) requestRelayTicket(ctx context.Context, relayURL, relayPubKey string) ([]byte, bool) {
	wait := &relayTicketWait{relayPubKey: relayPubKey, answer: make(chan string, 1)}
	s.mu.Lock()
	// A previous ladder's waiter (timed out, or its session moved on) is
	// stale by definition — the rebuild ladder is single-flight, so this
	// request is the only one that can be current.
	s.relayTicketWait = wait
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.relayTicketWait == wait {
			s.relayTicketWait = nil
		}
		s.mu.Unlock()
	}()

	req := RelayTicketRequest{RelayURL: relayURL, RelayPubKey: relayPubKey}
	if err := s.signal.sendRaw(MessageTypeRelayTicketRequest, req); err != nil {
		s.logger.WarnContext(ctx, "relay_ticket_request failed — dialing without a ticket",
			relayURLKey, relayURL, errorKey, err)
		return nil, false
	}
	timer := time.NewTimer(relayTicketReplyWait)
	defer timer.Stop()
	select {
	case grant := <-wait.answer:
		if grant == "" {
			s.logger.DebugContext(ctx, "relay ticket not granted — dialing without a ticket", relayURLKey, relayURL)
			return nil, false
		}
		ticket, err := relay.TicketFromBase64(grant)
		if err != nil || len(ticket) == 0 {
			s.logger.WarnContext(ctx, "relay ticket undecodable — dialing without a ticket",
				relayURLKey, relayURL, errorKey, err)
			return nil, false
		}
		return ticket, true
	case <-timer.C:
		s.logger.WarnContext(ctx, "relay ticket answer not received in time — dialing without a ticket",
			relayURLKey, relayURL)
		return nil, false
	case <-s.closeCh:
		return nil, false
	case <-ctx.Done():
		return nil, false
	}
}

// handleRelayTicket delivers a relay_ticket answer to the rebuild waiting
// for it. An answer nobody is waiting for, or one for another relay (a
// timed-out request's late reply), is dropped — never handed to the next
// waiter, whose dial it could not admit.
func (s *Session) handleRelayTicket(msg *Message) {
	var rt RelayTicket
	if err := UnmarshalPayload(msg, &rt); err != nil {
		s.logger.Warn("bad relay_ticket", errorKey, err)
		return
	}
	s.mu.RLock()
	wait := s.relayTicketWait
	s.mu.RUnlock()
	if wait == nil {
		s.logger.Debug("relay_ticket with no pending request — ignored", relayURLKey, rt.RelayURL)
		return
	}
	if rt.RelayPubKey != wait.relayPubKey {
		s.logger.Debug("relay_ticket for another relay than requested — ignored", relayURLKey, rt.RelayURL)
		return
	}
	select {
	case wait.answer <- rt.RelayTicket:
	default: // the waiter already left (timeout / close)
	}
}
