package peer

import (
	"context"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/relay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEstablishTransport(t *testing.T) {
	t.Run("ICE connects first", func(t *testing.T) {
		iceReady := make(chan struct{})
		close(iceReady) // simulate ICE connecting immediately

		transport, err := EstablishTransport(
			context.Background(), iceReady, nil, 10*time.Second,
		)
		require.ErrorIs(t, err, ErrDirectICEConnected)
		assert.Nil(t, transport) // caller uses DataChannelTransport
	})

	t.Run("ICE times out with relay", func(t *testing.T) {
		iceReady := make(chan struct{}) // never closed
		rc := relay.NewClient()

		transport, err := EstablishTransport(
			context.Background(), iceReady, rc, time.Millisecond,
		)
		require.NoError(t, err)
		assert.NotNil(t, transport)
		assert.Equal(t, rc, transport)
	})

	t.Run("ICE times out without relay", func(t *testing.T) {
		iceReady := make(chan struct{}) // never closed

		_, err := EstablishTransport(
			context.Background(), iceReady, nil, time.Millisecond,
		)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no relay available")
	})

	t.Run("context cancelled", func(t *testing.T) {
		iceReady := make(chan struct{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately

		_, err := EstablishTransport(
			ctx, iceReady, nil, 10*time.Second,
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
	})
}

func TestEstablishTransportKicked(t *testing.T) {
	t.Run("peer relay announcement beats the timeout", func(t *testing.T) {
		iceReady := make(chan struct{}) // ICE never connects
		relayKick := make(chan struct{})
		rc := relay.NewClient()
		close(relayKick) // peer announced relay (session_open)

		start := time.Now()
		transport, err := EstablishTransportKicked(
			context.Background(), iceReady, relayKick, rc, 10*time.Second,
		)
		require.NoError(t, err)
		assert.Equal(t, rc, transport)
		// Must NOT wait out the 10s arbitration window.
		assert.Less(t, time.Since(start), time.Second)
	})

	t.Run("late kick after ICE won is a no-op", func(t *testing.T) {
		iceReady := make(chan struct{})
		close(iceReady)
		relayKick := make(chan struct{})

		transport, err := EstablishTransportKicked(
			context.Background(), iceReady, relayKick, relay.NewClient(), 10*time.Second,
		)
		require.ErrorIs(t, err, ErrDirectICEConnected)
		assert.Nil(t, transport)
		// Closing after the select returned must not panic.
		close(relayKick)
	})

	t.Run("nil kick keeps timeout semantics", func(t *testing.T) {
		iceReady := make(chan struct{})

		transport, err := EstablishTransportKicked(
			context.Background(), iceReady, nil, relay.NewClient(), time.Millisecond,
		)
		require.NoError(t, err)
		assert.NotNil(t, transport)
	})
}
