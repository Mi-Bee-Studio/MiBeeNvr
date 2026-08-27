package legacy

// Constructor guard tests (#570): URL parsing and credential validation fail
// fast with clear errors — no network involved.

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewClient_BadURL(t *testing.T) {
	_, err := NewClient("://not-a-url")
	require.Error(t, err)
}

func TestNewClient_UnknownModelRejected(t *testing.T) {
	// A well-formed URL with an unregistered model must be rejected before
	// any dialing.
	_, err := NewClient("tutk://uid?model=isa.camera.unknown")
	require.Error(t, err, "unknown model has no credentials — constructor refuses")
}

func TestNewLegacyProducer_BadURL(t *testing.T) {
	_, err := NewLegacyProducer("://bad")
	require.Error(t, err, "producer construction fails before any media start")
}
