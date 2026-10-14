package peer

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestSignalingConfig_Timeouts validates the timeout configuration fields
// exist and have sensible zero-value semantics (zero = use defaults). Callers
// tune these to control handshake/read/ICE-wait deadlines without recompiling.
func TestSignalingConfig_Timeouts(t *testing.T) {
	cfg := SignalingConfig{}

	// Zero values must resolve to package defaults, not zero-duration
	// (which would make every operation instantly time out).
	assert.Equal(t, DefaultHandshakeTimeout, cfg.handshakeTimeout(),
		"zero HandshakeTimeout should fall back to DefaultHandshakeTimeout")
	assert.Equal(t, DefaultReadTimeout, cfg.readTimeout(),
		"zero ReadTimeout should fall back to DefaultReadTimeout")
	assert.Equal(t, DefaultTransportTimeout, cfg.transportTimeout(),
		"zero TransportTimeout should fall back to DefaultTransportTimeout")

	// Custom values are honored.
	cfg.HandshakeTimeout = 7 * time.Second
	cfg.ReadTimeout = 45 * time.Second
	cfg.TransportTimeout = 20 * time.Second
	assert.Equal(t, 7*time.Second, cfg.handshakeTimeout())
	assert.Equal(t, 45*time.Second, cfg.readTimeout())
	assert.Equal(t, 20*time.Second, cfg.transportTimeout())
}

// TestDefaultTimeouts_NonZero confirms the package-level defaults are usable
// (non-zero), so an unconfigured client does not instantly fail.
func TestDefaultTimeouts_NonZero(t *testing.T) {
	assert.Positive(t, int64(DefaultHandshakeTimeout))
	assert.Positive(t, int64(DefaultReadTimeout))
	assert.Positive(t, int64(DefaultTransportTimeout))
}
