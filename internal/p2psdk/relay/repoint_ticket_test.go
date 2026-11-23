package relay

// #143 v2: a re-pointed session must present a ticket bound to the NEW relay.
// The welcome ticket cannot be reused (different relay key), so the SDK must
// accept a per-repoint ticket and install it before client_auth.

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSetTicketBeforeConnectIsUsedInAuth: a ticket installed via SetTicket
// must reach the client_auth frame builder.
func TestSetTicketBeforeConnectIsUsedInAuth(t *testing.T) {
	c := NewClient()

	var ticket [61]byte
	for i := range ticket {
		ticket[i] = byte(i)
	}
	c.SetTicket(ticket[:])

	// Both legs read from the same field; verify the data leg encoding.
	legacy := make([]byte, 109)
	got := appendAuthSuffix(legacy, 0x00, c.ticket)
	require.Greater(t, len(got), len(legacy),
		"a ticket must extend the auth frame with its suffix")
	// [109B legacy][1B conn_type][2B len BE][ticket]
	assert.Equal(t, byte(0x00), got[109], "data leg = conn_type 0")
	assert.Equal(t, byte(0), got[110])
	assert.Equal(t, byte(len(ticket)), got[111])
	assert.Equal(t, ticket[:], got[112:])
}

// TestEmptyRepointTicketLeavesFrameUnchanged: when the injector is a node
// without the fleet secret (feature off), no ticket is attached and the
// frame stays byte-identical to the legacy one.
func TestEmptyRepointTicketLeavesFrameUnchanged(t *testing.T) {
	tb, err := TicketFromBase64("")
	require.NoError(t, err)
	require.Nil(t, tb, "empty base64 must decode to nil, not an empty ticket")

	legacy := make([]byte, 109)
	got := appendAuthSuffix(legacy, 0x00, tb)
	assert.Equal(t, legacy, got)
}

// TestRepointTicketReplacesWelcomeTicket: the newest ticket wins, so a
// session that moved nodes does not keep presenting the stale welcome grant.
func TestRepointTicketReplacesWelcomeTicket(t *testing.T) {
	c := NewClient()

	welcomeTicket := make([]byte, 61)
	for i := range welcomeTicket {
		welcomeTicket[i] = 0xAA
	}
	c.SetTicket(welcomeTicket)

	repointTicket := make([]byte, 61)
	for i := range repointTicket {
		repointTicket[i] = 0xBB
	}
	c.SetTicket(repointTicket)

	legacy := make([]byte, 109)
	got := appendAuthSuffix(legacy, 0x00, c.ticket)
	require.Greater(t, len(got), 112)
	assert.Equal(t, repointTicket, got[112:],
		"the re-point ticket must replace the welcome one")
}

// TestTicketFromBase64RoundTripForRepoint: the exact helper the handlers use
// to decode the protocol field.
func TestTicketFromBase64RoundTripForRepoint(t *testing.T) {
	raw := make([]byte, 61)
	for i := range raw {
		raw[i] = byte(i * 3)
	}
	got, err := TicketFromBase64(base64.StdEncoding.EncodeToString(raw))
	require.NoError(t, err)
	assert.Equal(t, raw, got)
}
