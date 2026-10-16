package tunnel_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	peer "github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2psdk/tunnel"
)

// TestRequireE2EEFailsDialOnSilentPeer (#36③): with the strict policy
// armed, a peer that never confirms E2EE fails the Dial instead of
// degrading to plaintext. The opportunistic default is covered by the
// legacy-compat tests (plaintext Dial keeps working).
func TestRequireE2EEFailsDialOnSilentPeer(t *testing.T) {
	devDC, clientDC := linkedPair()

	// The device side is a LEGACY tunnel: no key, no ack, data plaintext.
	devTun := tunnel.New(peer.NewDataChannelTransport(devDC))
	clientTun := tunnel.New(
		peer.NewDataChannelTransport(clientDC),
		tunnel.WithRequireE2EE(),
	)
	go devTun.Serve()    //nolint:errcheck
	go clientTun.Serve() //nolint:errcheck
	t.Cleanup(func() {
		_ = clientTun.Close()
		_ = devTun.Close()
	})

	// Arm E2EE on the client; the device has no key → never acks.
	require.NoError(t, clientTun.EnableE2EE(make([]byte, 32)))

	_, err := clientTun.Dial("svc")
	require.Error(t, err)
	assert.True(t, errors.Is(err, tunnel.ErrE2EERequired),
		"dial must fail with ErrE2EERequired, got %v", err)
}
