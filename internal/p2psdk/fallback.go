package peer

import (
	"context"
	"errors"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/relay"
)

// ErrDirectICEConnected is returned when ICE connects directly within the
// timeout. The caller should use a DataChannelTransport from the active
// DataChannel instead of a relay transport.
var ErrDirectICEConnected = errors.New("ICE connected directly")

// EstablishTransport determines which Transport to use for a P2P session.
// It waits for ICE to connect (signaled by iceReady closing) or a timeout.
//
// Returns:
//   - (nil, ErrDirectICEConnected) if ICE connects within the timeout — caller creates a
//     DataChannelTransport from the active DataChannel.
//   - (relayClient, nil) if ICE times out and a relay client is available.
//   - (nil, error) if both ICE and relay are unavailable or ctx is cancelled.
//
// This is NOT mid-stream switching. The transport decision is made once
// during session establishment.
func EstablishTransport(
	ctx context.Context,
	iceReady <-chan struct{},
	relayClient *relay.Client,
	timeout time.Duration,
) (Transport, error) {
	return EstablishTransportKicked(ctx, iceReady, nil, relayClient, timeout)
}

// EstablishTransportKicked is EstablishTransport with an optional relayKick
// channel: closed by the peer's session_open announcement (see
// p2pSession.kickRelay) to cut the ICE wait short when the peer is already on
// relay. nil kick = plain timeout arbitration (original behavior).
func EstablishTransportKicked(
	ctx context.Context,
	iceReady <-chan struct{},
	relayKick <-chan struct{},
	relayClient *relay.Client,
	timeout time.Duration,
) (Transport, error) {
	select {
	case <-iceReady:
		// ICE connected — caller provides DataChannelTransport.
		return nil, ErrDirectICEConnected

	case <-relayKick:
		// Peer announced relay via signaling — follow it now. If relay is
		// somehow unavailable fall through to the timeout semantics below.
		if relayClient != nil {
			return relayClient, nil
		}
		return nil, ErrICETimeout
	case <-time.After(timeout):
		if relayClient != nil {
			return relayClient, nil
		}
		return nil, ErrICETimeout

	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
