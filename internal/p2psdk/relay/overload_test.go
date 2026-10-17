package relay

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestConnectOverloadClassified (#95): server_auth 0x03 surfaces as
// ErrOverloaded — capacity, not an auth failure.
func TestConnectOverloadClassified(t *testing.T) {
	mock := newMockRelayServer(t)
	mock.authOverload = true

	client := NewClient()
	err := client.Connect(context.Background(), mock.url, hex.EncodeToString(mock.pubKey[:]))
	require.ErrorIs(t, err, ErrOverloaded)
}
