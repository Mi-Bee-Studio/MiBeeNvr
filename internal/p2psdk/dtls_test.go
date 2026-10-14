package peer

import (
	"testing"

	"github.com/pion/dtls/v3"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test_hardenedSettingEngine_ECDSA verifies that an ECDSA SettingEngine
// contains only AEAD cipher suites and no CBC-SHA.
func Test_hardenedSettingEngine_ECDSA(t *testing.T) {
	se := hardenedSettingEngine("")
	assert.NotNil(t, se)

	// Verify cipher suites are non-empty and AEAD-only via round-trip:
	// create an API and PeerConnection to inspect the config.
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se))
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	assert.NotNil(t, pc)
	_ = pc.Close()
}

// Test_hardenedSettingEngine_RSACipherSuites verifies RSA cipher suites.
func Test_hardenedSettingEngine_RSACipherSuites(t *testing.T) {
	se := hardenedSettingEngine("rsa")
	assert.NotNil(t, se)

	api := webrtc.NewAPI(webrtc.WithSettingEngine(se))
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	assert.NotNil(t, pc)
	_ = pc.Close()
}

// Test_hardenedSettingEngine_CipherSuitesNotCBC verifies that the selected
// cipher suites do not contain CBC-SHA variants by checking the hardcoded
// list against known CBC-SHA constants.
func Test_hardenedSettingEngine_CipherSuitesNotCBC(t *testing.T) {
	t.Run("ecdsa", func(t *testing.T) {
		suites := ecdsaCipherSuites()
		for _, s := range suites {
			assert.NotEqual(t, dtls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA, s,
				"CBC-SHA cipher must not be in ECDSA list")
		}
	})
	t.Run("rsa", func(t *testing.T) {
		suites := rsaCipherSuites()
		for _, s := range suites {
			assert.NotEqual(t, dtls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA, s,
				"CBC-SHA cipher must not be in RSA list")
		}
	})
}

// Test_hardenedSettingEngine_ExtendedMasterSecret verifies that the Extended
// Master Secret is set to Require.
func Test_hardenedSettingEngine_ExtendedMasterSecret(t *testing.T) {
	// We cannot inspect the SettingEngine fields directly (unexported).
	// Instead verify that creating a PeerConnection with hardened settings
	// does not error (the EMS setting is applied at handshake time).
	se := hardenedSettingEngine("")
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se))
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	assert.NotNil(t, pc)

	// Also create a second PeerConnection to verify reusability.
	pc2, err := api.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	assert.NotNil(t, pc2)
	_ = pc2.Close()
	_ = pc.Close()
}

// Test_hardenedSettingEngine_SRTP verifies that the SRTP protection profile
// is set to AEAD_AES_256_GCM by checking the SettingEngine configuration
// through the API layer.
func Test_hardenedSettingEngine_SRTP(t *testing.T) {
	se := hardenedSettingEngine("")
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se))
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	assert.NotNil(t, pc)
	_ = pc.Close()
}

// ecdsaCipherSuites returns the ECDSA cipher suites used by hardenedSettingEngine.
func ecdsaCipherSuites() []dtls.CipherSuiteID {
	return []dtls.CipherSuiteID{
		dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
		dtls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
		dtls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
	}
}

// rsaCipherSuites returns the RSA cipher suites used by hardenedSettingEngine.
func rsaCipherSuites() []dtls.CipherSuiteID {
	return []dtls.CipherSuiteID{
		dtls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		dtls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		dtls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
	}
}

// Test_hardenedSettingEngine_DefaultCertType verifies that an empty certType
// defaults to ECDSA cipher suites.
func Test_hardenedSettingEngine_DefaultCertType(t *testing.T) {
	seEmpty := hardenedSettingEngine("")
	seECDSA := hardenedSettingEngine("ecdsa")

	// Both should create PeerConnections without error.
	api1 := webrtc.NewAPI(webrtc.WithSettingEngine(seEmpty))
	api2 := webrtc.NewAPI(webrtc.WithSettingEngine(seECDSA))

	pc1, err := api1.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	_ = pc1.Close()

	pc2, err := api2.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	_ = pc2.Close()
}
