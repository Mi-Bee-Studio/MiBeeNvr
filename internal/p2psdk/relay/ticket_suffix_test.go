package relay

// #143 client-side ticket suffix encoding tests.

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAppendAuthSuffix_NoTicket: without a ticket the payload is untouched —
// the exact legacy 109/110-byte frame.
func TestAppendAuthSuffix_NoTicket(t *testing.T) {
	base := []byte{1, 2, 3}
	out := appendAuthSuffix(base, 0x00, nil)
	assert.True(t, bytes.Equal(out, base), "legacy frame must stay byte-identical")
}

// TestAppendAuthSuffix_WithTicket: [conn_type][2B BE len][ticket] appended.
func TestAppendAuthSuffix_WithTicket(t *testing.T) {
	base := []byte{1, 2, 3}
	ticket := []byte{0xA0, 0xA1, 0xA2, 0xA3}
	out := appendAuthSuffix(base, 0x01, ticket)
	require.Len(t, out, len(base)+1+2+len(ticket))
	assert.Equal(t, byte(0x01), out[len(base)])
	assert.Equal(t, byte(0x00), out[len(base)+1], "len BE high byte")
	assert.Equal(t, byte(0x04), out[len(base)+2], "len BE low byte")
	assert.True(t, bytes.Equal(out[len(base)+3:], ticket))
}

// TestTicketFromBase64: round trip plus the empty-means-nil contract for
// servers without the feature.
func TestTicketFromBase64(t *testing.T) {
	raw := []byte{1, 2, 3, 4, 5}
	enc := "AQIDBAU="
	got, err := TicketFromBase64(enc)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(got, raw))

	got, err = TicketFromBase64("")
	require.NoError(t, err)
	assert.Nil(t, got)

	_, err = TicketFromBase64("!!!not-base64!!!")
	assert.Error(t, err)
}
