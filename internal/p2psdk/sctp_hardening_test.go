package peer

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSCTPHardeningEnvParsing (#109): MIBEE_SCTP_RTO_MAX_MS overrides the
// default cap, "0" means stock pion behavior (no cap applied), and garbage
// falls back to the default.
func TestSCTPHardeningEnvParsing(t *testing.T) {
	require.Equal(t, 600*time.Millisecond, sctpRTOMaxDefault,
		"default RTO cap must stay pinned to the bench-validated value")

	t.Setenv("MIBEE_SCTP_RTO_MAX_MS", "250")
	require.Equal(t, 250*time.Millisecond, envDurationMs("MIBEE_SCTP_RTO_MAX_MS"))

	t.Setenv("MIBEE_SCTP_RTO_MAX_MS", "0")
	require.Equal(t, time.Duration(0), envDurationMs("MIBEE_SCTP_RTO_MAX_MS"))

	t.Setenv("MIBEE_SCTP_RTO_MAX_MS", "not-a-number")
	require.Equal(t, time.Duration(-1), envDurationMs("MIBEE_SCTP_RTO_MAX_MS"))

	t.Setenv("MIBEE_SCTP_MIN_CWND", "262144")
	n, ok := envUint32("MIBEE_SCTP_MIN_CWND")
	require.True(t, ok)
	require.EqualValues(t, 262144, n)

	t.Setenv("MIBEE_SCTP_MIN_CWND", "-5")
	_, ok = envUint32("MIBEE_SCTP_MIN_CWND")
	require.False(t, ok)
}
