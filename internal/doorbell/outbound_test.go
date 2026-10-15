package doorbell

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/event"
)

// TestOutbound_RegisterLoopback: mode B end-to-end against a mode A server
// in the same process — the outbound client's digest dance must register it
// into the registrar's binding table.
func TestOutbound_RegisterLoopback(t *testing.T) {
	// Registrar side: a server with the NVR-as-UA configured as a user.
	regSrv, _, regAddr := startTestServer(t, config.DoorbellUserConfig{
		Username: "nvr",
		Secret:   "pbx-pass",
		Name:     "NVR",
	})
	_ = regSrv

	// Outbound side: a second server instance whose only job is the client
	// REGISTER loop (its own listener serves nothing).
	outPort := freePort(t)
	outCfg := ConfigFrom(config.DoorbellConfig{
		Listen:    "127.0.0.1:" + strconv.Itoa(outPort),
		Realm:     "mibee-nvr",
		Transport: "udp",
		Outbound: &config.DoorbellOutboundConfig{
			Server:   regAddr,
			Username: "nvr",
			Secret:   "pbx-pass",
			Expires:  "300s",
		},
	})
	outSrv := NewServer(outCfg, event.NewEventBus(16), nil)
	require.NoError(t, outSrv.Start(context.Background()))
	t.Cleanup(outSrv.Stop)

	require.NoError(t, outSrv.sendOutboundRegister(context.Background(), outCfg.Outbound))

	// The registrar must now hold a live binding for "nvr".
	require.Eventually(t, func() bool {
		return regSrv.reg.registered("nvr", "127.0.0.1")
	}, 5*time.Second, 100*time.Millisecond, "outbound REGISTER must land in the registrar's binding table")
}

// TestOutbound_WrongSecretFails.
func TestOutbound_WrongSecretFails(t *testing.T) {
	regSrv, _, regAddr := startTestServer(t, config.DoorbellUserConfig{
		Username: "nvr",
		Secret:   "pbx-pass",
	})
	_ = regSrv

	outPort := freePort(t)
	outCfg := ConfigFrom(config.DoorbellConfig{
		Listen:    "127.0.0.1:" + strconv.Itoa(outPort),
		Realm:     "mibee-nvr",
		Transport: "udp",
		Outbound: &config.DoorbellOutboundConfig{
			Server:   regAddr,
			Username: "nvr",
			Secret:   "wrong",
			Expires:  "300s",
		},
	})
	outSrv := NewServer(outCfg, event.NewEventBus(16), nil)
	require.NoError(t, outSrv.Start(context.Background()))
	t.Cleanup(outSrv.Stop)

	require.Error(t, outSrv.sendOutboundRegister(context.Background(), outCfg.Outbound))
}
