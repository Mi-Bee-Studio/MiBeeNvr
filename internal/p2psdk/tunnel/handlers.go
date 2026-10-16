package tunnel

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"time"

	peer "github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk"
)

func (t *Tunnel) handleOpen(payload string) {
	var open TunnelOpen
	if err := json.Unmarshal([]byte(payload), &open); err != nil {
		t.logger.Warn("invalid tunnel open", "error", err)
		return
	}

	t.mu.Lock()
	addr, ok := t.services[open.TargetService]
	t.mu.Unlock()

	if !ok {
		t.logger.Warn("unknown service", "service", open.TargetService)
		_ = t.sendMsg(TunnelMessage{
			Type:    TunnelMessageTypeClose,
			Payload: mustJSON(TunnelClose{StreamID: open.StreamID}),
		})
		return
	}

	if err := t.registry.OpenStream(open.StreamID); err != nil {
		t.logger.Warn("duplicate stream", "stream_id", open.StreamID)
		return
	}

	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.registry.CloseStream(open.StreamID)
		t.logger.Warn("dial service failed", "service", open.TargetService, "error", err)
		_ = t.sendMsg(TunnelMessage{
			Type:    TunnelMessageTypeClose,
			Payload: mustJSON(TunnelClose{StreamID: open.StreamID}),
		})
		return
	}

	sc := newStreamConn(t, open.StreamID, open.TargetService, t.logger)
	sc.sealed = open.E2EE && t.e2eeCipher != nil
	sc.setConn(conn)
	t.streams.Store(open.StreamID, sc)

	// #88①: data that overtook this open across the two channels.
	for _, early := range t.drainEarlyData(open.StreamID) {
		sc.pushData(early)
	}

	if sc.sealed {
		t.sendE2EEAck()
	}

	t.logger.Debug("stream opened", "stream_id", open.StreamID,
		"service", open.TargetService, "e2ee", sc.sealed)

	t.wg.Add(2)
	go func() {
		defer t.wg.Done()
		t.pipeTCPToTunnel(conn, sc)
	}()
	go func() {
		defer t.wg.Done()
		t.pipeTunnelToTCP(conn, sc)
	}()
}

func (t *Tunnel) handleData(payload string) {
	var data TunnelData
	if err := json.Unmarshal([]byte(payload), &data); err != nil {
		t.logger.Warn("invalid tunnel data", "error", err)
		return
	}
	if data.Enc {
		if t.e2eeCipher == nil {
			// Sealed frame with no local key: the peer negotiated E2EE we
			// cannot serve — never deliver ciphertext to the stream.
			t.logger.Warn("sealed frame without e2ee key — dropping",
				"stream_id", data.StreamID)
			return
		}
		if err := t.openFragment(&data); err != nil {
			t.logger.Warn("e2ee decrypt failed — dropping frame",
				"stream_id", data.StreamID, "seq", data.Seq, "error", err)
			return
		}
		// First sealed frame doubles as the peer's E2EE confirmation:
		// the dialer starts sealing its own frames from here on.
		t.remoteConfirmed.Store(true)
	}
	t.logger.Debug(
		"handleData",
		"stream", data.StreamID, "seq", data.Seq,
		"total", data.Total, "payload_len", len(data.Payload),
	)

	result, err := t.reasm.AddFragment(data)
	if err != nil {
		t.logger.Warn("reassembly error", "stream_id", data.StreamID, "error", err)
		_ = t.sendMsg(TunnelMessage{
			Type:    TunnelMessageTypeClose,
			Payload: mustJSON(TunnelClose{StreamID: data.StreamID}),
		})
		return
	}
	if result == nil {
		return
	}

	v, ok := t.streams.Load(data.StreamID)
	if !ok {
		// #88①: on the dual-channel path the open rides the lossy channel
		// while data rides the ordered one — SCTP gives no cross-stream
		// ordering, so data can legitimately arrive first. Buffer briefly
		// (bounded); the open delivers, a lost open ages out.
		t.bufferEarlyData(data.StreamID, result)
		return
	}
	sc, ok := v.(*streamConn)
	if !ok {
		return
	}
	sc.pushData(result)
}

// earlyDataTTL bounds how long pre-open data waits for its open before the
// buffered bytes are discarded (a lost open on the lossy channel means the
// dialer-side stream times out anyway — the buffer is only for reorder).
var earlyDataTTL = 2 * time.Second

// bufferEarlyData parks one reassembled payload for a stream whose open has
// not arrived yet. Bounded per stream (8 payloads) and globally (64); the
// TTL sweep runs opportunistically on insert.
func (t *Tunnel) bufferEarlyData(streamID uint32, payload []byte) {
	t.earlyMu.Lock()
	defer t.earlyMu.Unlock()
	now := time.Now()
	for id := range t.early {
		if now.Sub(t.earlyAt[id]) > earlyDataTTL {
			delete(t.early, id)
			delete(t.earlyAt, id)
		}
	}
	if len(t.early) >= 64 {
		return // bounded — a flood without opens is bogus traffic
	}
	if len(t.early[streamID]) >= 8 {
		return
	}
	if t.early == nil {
		t.early = make(map[uint32][][]byte)
		t.earlyAt = make(map[uint32]time.Time)
	}
	if _, seen := t.earlyAt[streamID]; !seen {
		t.earlyAt[streamID] = now
	}
	cp := make([]byte, len(payload))
	copy(cp, payload)
	t.early[streamID] = append(t.early[streamID], cp)
}

// EarlyDataTTLForTest exposes the pre-open buffer TTL to the external test
// package (aging-out assertions need to know how long to wait).
func EarlyDataTTLForTest() time.Duration { return earlyDataTTL }

// drainEarlyData returns and clears the buffered pre-open payloads for a
// stream (called right after the open registers the stream).
func (t *Tunnel) drainEarlyData(streamID uint32) [][]byte {
	t.earlyMu.Lock()
	defer t.earlyMu.Unlock()
	out := t.early[streamID]
	delete(t.early, streamID)
	delete(t.earlyAt, streamID)
	return out
}

// e2eeAckPlaintext is the sealed constant inside an e2ee_ack frame.
const e2eeAckPlaintext = "mibee-e2ee-ack-v1"

// sendE2EEAck confirms E2EE to the dialer with a sealed ack so it can
// encrypt from its very first data frame (legacy dialers never see this —
// they did not set open.E2EE).
func (t *Tunnel) sendE2EEAck() {
	sealed, err := t.e2eeCipher.Seal([]byte(e2eeAckPlaintext), []byte("e2ee-ack"))
	if err != nil {
		t.logger.Warn("e2ee ack seal failed", "error", err)
		return
	}
	_ = t.sendMsg(TunnelMessage{
		Type:    TunnelMessageTypeE2EEAck,
		Payload: base64.StdEncoding.EncodeToString(sealed),
	})
}

// sendFragments seals (when in force) and sends tunnel data fragments.
func (t *Tunnel) sendFragments(fragments []TunnelData, streamSealed bool) error {
	for i := range fragments {
		if streamSealed || t.e2eeActive() {
			if err := t.sealFragment(&fragments[i]); err != nil {
				return fmt.Errorf("seal: %w", err)
			}
		}
		if err := t.sendMsg(TunnelMessage{
			Type: TunnelMessageTypeData, Payload: mustJSON(fragments[i]),
		}); err != nil {
			return fmt.Errorf("send: %w", err)
		}
	}
	return nil
}

// handleE2EEAck verifies the device's sealed ack and flips the tunnel into
// sealed-sending mode. Legacy peers never send this frame type.
func (t *Tunnel) handleE2EEAck(payload string) {
	if t.e2eeCipher == nil {
		return // sealed traffic from a peer we cannot serve — ignore
	}
	sealed, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.logger.Warn("invalid e2ee_ack encoding", "error", err)
		return
	}
	pt, err := t.e2eeCipher.Open(sealed, []byte("e2ee-ack"))
	if err != nil || string(pt) != e2eeAckPlaintext {
		t.logger.Warn("e2ee_ack verification failed", "error", err)
		return
	}
	t.remoteConfirmed.Store(true)
	t.e2eeAckOnce.Do(func() {
		if t.e2eeAckCh != nil {
			close(t.e2eeAckCh)
		}
	})
}

func (t *Tunnel) handleClose(payload string) {
	var closeMsg TunnelClose
	if err := json.Unmarshal([]byte(payload), &closeMsg); err != nil {
		t.logger.Warn("invalid tunnel close", "error", err)
		return
	}
	t.registry.CloseStream(closeMsg.StreamID)
	v, ok := t.streams.LoadAndDelete(closeMsg.StreamID)
	if ok {
		if sc, ok := v.(*streamConn); ok {
			sc.closeRemote()
		}
	}
}

func (t *Tunnel) pipeTCPToTunnel(tcpConn net.Conn, sc *streamConn) {
	defer func() {
		sc.closeRemote() // wake up pipeTunnelToTCP before removing stream
		_ = tcpConn.Close()
		_ = t.sendMsg(TunnelMessage{
			Type:    TunnelMessageTypeClose,
			Payload: mustJSON(TunnelClose{StreamID: sc.streamID}),
		})
		t.registry.CloseStream(sc.streamID)
		t.streams.Delete(sc.streamID)
	}()

	fragmenter := NewFragmenter(sc.streamID, t.fragmentSize())
	buf := make([]byte, t.fragmentSize())
	for {
		n, err := tcpConn.Read(buf)
		if n > 0 {
			fragments, fragErr := fragmenter.Fragment(buf[:n])
			if fragErr != nil {
				t.logger.Warn("fragment error", "stream_id", sc.streamID, "error", fragErr)
				return
			}
			if sendErr := t.sendFragments(fragments, sc.sealed); sendErr != nil {
				t.logger.Warn("send data error", "stream_id", sc.streamID, "error", sendErr)
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (t *Tunnel) pipeTunnelToTCP(tcpConn net.Conn, sc *streamConn) {
	defer func() {
		if err := tcpConn.Close(); err != nil {
			t.logger.Warn("pipeTunnelToTCP close error", "err", err)
		}
	}()
	buf := make([]byte, MaxPayloadBytes)
	for {
		n, err := sc.Read(buf)
		if n > 0 {
			if _, writeErr := tcpConn.Write(buf[:n]); writeErr != nil {
				t.logger.Warn("pipeTunnelToTCP write error", "stream_id", sc.streamID, "err", writeErr)
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (t *Tunnel) sendMsg(msg TunnelMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal tunnel msg: %w", err)
	}
	// #88①: lifecycle frames ride the paired lossy ctrl channel when the
	// transport has one — losing one costs a stream retry, while queueing
	// them behind bulk-data retransmissions head-of-line blocks everything.
	if isTunnelLifecycleType(msg.Type) {
		if cs, ok := t.transport.(interface{ SendControl([]byte) error }); ok {
			return cs.SendControl(data)
		}
	}
	return t.transport.Send(data)
}

// isTunnelLifecycleType selects the frames that may ride the lossy ctrl
// channel: stream lifecycle + the E2EE ack (echo frames keep their own
// measured-lane semantics, see echo.go).
func isTunnelLifecycleType(typ TunnelMessageType) bool {
	switch typ {
	case TunnelMessageTypeOpen, TunnelMessageTypeClose, TunnelMessageTypeE2EEAck:
		return true
	case TunnelMessageTypeData, TunnelMessageTypeEcho, TunnelMessageTypeEchoResp:
		return false
	}
	return false
}

// sendEgress routes one already-marshaled tunnel message, honoring the
// stream's high-lane claim (control frames like open/close always ride the
// normal lane via sendMsg).
func (t *Tunnel) sendEgress(data []byte, high bool) error {
	if high {
		return peer.SendPriority(t.transport, data)
	}
	return t.transport.Send(data)
}
