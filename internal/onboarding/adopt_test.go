package onboarding

// #1120 adopt-flow tests: seams swapped for fakes — no network, no netlink
// (the httptest-backed DHCP sweep is the one real-HTTP piece).
//
// The flow's contract: alias up → ONVIF change → find new IP (static poll /
// DHCP serial sweep) → enroll with stable_id → alias down (even on failure).

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/stretchr/testify/require"
)

func enabledCfg() *config.Config {
	cfg := &config.Config{}
	cfg.CameraOnboarding = config.CameraOnboardingConfig{
		Enabled:       true,
		DefaultSubnet: "192.168.1.0/24",
	}
	return cfg
}

// traceAlias counts calls (interface takes net.IP per AliasManager).
type traceAlias struct {
	upCalls, downCalls int
}

func (a *traceAlias) Up(_ net.IP, _ int) error { a.upCalls++; return nil }
func (a *traceAlias) Down(_ net.IP) error      { a.downCalls++; return nil }
func (a *traceAlias) UpHeld(_ net.IP) bool     { return a.upCalls > a.downCalls }

// fakeEnroller captures the AddCamera call.
type fakeEnroller struct {
	captured config.CameraConfig
	id       string
	err      error
}

func (f *fakeEnroller) AddCamera(_ context.Context, cam config.CameraConfig) (string, error) {
	f.captured = cam
	return f.id, f.err
}

func TestAdopt_StaticMode_HappyPath(t *testing.T) {
	alias := &traceAlias{}
	setNetCalled := false
	en := &fakeEnroller{id: "cam-adopted-1"}

	m := NewManager(enabledCfg, nil)
	m.alias = alias
	m.enroller = en
	m.setNet = func(ctx context.Context, ip, user, pass string, dhcp bool, staticIP string, prefix int) (bool, error) {
		setNetCalled = true
		require.False(t, dhcp, "static mode must not send DHCP")
		require.Equal(t, "192.168.63.77", staticIP)
		return false, nil
	}
	m.serialAt = func(ctx context.Context, ip string) string {
		if ip == "192.168.63.77" {
			return "DS-SERIAL-1"
		}
		return ""
	}

	res, err := m.Adopt(context.Background(), AdoptRequest{
		IP:       "192.168.1.64",
		Username: "admin",
		Password: "pass",
		Mode:     "static",
		StaticIP: "192.168.63.77",
	})
	require.NoError(t, err)
	require.True(t, setNetCalled)
	require.Equal(t, "DS-SERIAL-1", res.Serial)
	require.Equal(t, "192.168.63.77", res.NewIP)
	require.Equal(t, "cam-adopted-1", res.CameraID)
	require.False(t, res.RebootNeeded)
	require.Equal(t, "DS-SERIAL-1", en.captured.StableID, "the serial must ride as stable_id (IP self-healing binds immediately)")
	require.Contains(t, en.captured.URL, "192.168.63.77/onvif/device_service")
	require.Equal(t, 1, alias.upCalls, "alias bound once")
	require.Equal(t, 1, alias.downCalls, "alias released after the flow")
}

func TestAdopt_DisabledConfig_Rejected(t *testing.T) {
	cfg := enabledCfg()
	cfg.CameraOnboarding.Enabled = false
	m := NewManager(func() *config.Config { return cfg }, nil)
	_, err := m.Adopt(context.Background(), AdoptRequest{IP: "192.168.1.64"})
	require.ErrorContains(t, err, "disabled")
}

func TestAdopt_InvalidIP_Rejected(t *testing.T) {
	m := NewManager(enabledCfg, nil)
	m.alias = &traceAlias{}
	_, err := m.Adopt(context.Background(), AdoptRequest{IP: "not-an-ip"})
	require.ErrorContains(t, err, "invalid camera address")
}

func TestAdopt_StaticMode_NeedsTargetAddress(t *testing.T) {
	m := NewManager(enabledCfg, nil)
	m.alias = &traceAlias{}
	_, err := m.Adopt(context.Background(), AdoptRequest{IP: "192.168.1.64", Mode: "static"})
	require.ErrorContains(t, err, "static mode needs a target address")
}

func TestAdopt_StaticNeverReachable_FailsAfterBudget(t *testing.T) {
	m := NewManager(enabledCfg, nil)
	m.alias = &traceAlias{}
	m.setNet = func(ctx context.Context, ip, user, pass string, dhcp bool, staticIP string, prefix int) (bool, error) {
		return false, nil
	}
	m.serialAt = func(ctx context.Context, ip string) string { return "" }

	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	_, err := m.Adopt(ctx, AdoptRequest{IP: "192.168.1.64", Mode: "static", StaticIP: "192.168.63.99"})
	require.Error(t, err)
	require.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled),
		"budget expiry must surface the ctx error, got %v", err)
}

func TestAdopt_StaticMode_SetNetFailure_ReleasesAlias(t *testing.T) {
	alias := &traceAlias{}
	m := NewManager(enabledCfg, nil)
	m.alias = alias
	m.setNet = func(ctx context.Context, ip, user, pass string, dhcp bool, staticIP string, prefix int) (bool, error) {
		return false, errors.New("device 401: wrong credentials")
	}
	_, err := m.Adopt(context.Background(), AdoptRequest{IP: "192.168.1.64", Mode: "static", StaticIP: "192.168.63.77"})
	require.Error(t, err)
	require.Equal(t, 1, alias.downCalls, "alias must be released even when the flow fails")
}

func TestAdopt_DHCPMode_SerialMatchAcrossSubnet(t *testing.T) {
	// The camera answers unauthenticated ONVIF/ISAPI probes on the NVR's own
	// subnet after the DHCP move — emulated by an httptest server serving
	// the ISAPI deviceInfo face; the sweep target points at it.
	alias := &traceAlias{}
	en := &fakeEnroller{id: "cam-dhcp-1"}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ISAPI/System/deviceInfo" {
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<DeviceInfo version="2.0" xmlns="http://www.isapi.org/ver20/XMLSchema">
<deviceName>Moved</deviceName><manufacturer>Hikvision</manufacturer>
<serialNumber>DS-DHCP-1</serialNumber><activated>true</activated>
</DeviceInfo>`)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(ts.Close)

	_, port, _ := net.SplitHostPort(ts.Listener.Addr().String())
	target := "127.0.0.1:" + port

	m := NewManager(enabledCfg, nil)
	m.alias = alias
	m.enroller = en
	m.setNet = func(ctx context.Context, ip, user, pass string, dhcp bool, staticIP string, prefix int) (bool, error) {
		require.True(t, dhcp)
		return false, nil
	}
	m.serialAt = func(ctx context.Context, ip string) string {
		if ip == "192.168.1.64" {
			return "DS-DHCP-1" // captured at the factory address pre-move
		}
		return ""
	}
	m.sweepTargets = func() []string { return []string{target} }

	res, err := m.Adopt(context.Background(), AdoptRequest{IP: "192.168.1.64", Mode: "dhcp"})
	require.NoError(t, err)
	require.Equal(t, "DS-DHCP-1", res.Serial)
	require.Equal(t, "127.0.0.1", res.NewIP, "the sweep must locate the moved camera by serial")
	require.Equal(t, "cam-dhcp-1", res.CameraID)
	require.Contains(t, en.captured.StableID, "DS-DHCP-1")
}

func TestAdopt_DHCPMode_AmbiguousCandidates_FailsSafe(t *testing.T) {
	// Two devices on the target subnet: the flow must refuse to guess.
	ts1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0"?><DeviceInfo xmlns="http://www.isapi.org/ver20/XMLSchema"><serialNumber>S-1</serialNumber><activated>true</activated></DeviceInfo>`)
	}))
	t.Cleanup(ts1.Close)
	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0"?><DeviceInfo xmlns="http://www.isapi.org/ver20/XMLSchema"><serialNumber>S-2</serialNumber><activated>true</activated></DeviceInfo>`)
	}))
	t.Cleanup(ts2.Close)

	_, p1, _ := net.SplitHostPort(ts1.Listener.Addr().String())
	_, p2, _ := net.SplitHostPort(ts2.Listener.Addr().String())

	m := NewManager(enabledCfg, nil)
	m.alias = &traceAlias{}
	m.setNet = func(ctx context.Context, ip, user, pass string, dhcp bool, staticIP string, prefix int) (bool, error) {
		return false, nil
	}
	m.serialAt = func(ctx context.Context, ip string) string { return "" } // serial unknown pre-move
	m.sweepTargets = func() []string { return []string{"127.0.0.1:" + p1, "127.0.0.1:" + p2} }

	_, err := m.Adopt(context.Background(), AdoptRequest{IP: "192.168.1.64", Mode: "dhcp"})
	require.ErrorContains(t, err, "cannot pick safely")
}

func TestExpandSubnet_Bounded(t *testing.T) {
	_, ipnet, err := net.ParseCIDR("192.168.1.0/24")
	require.NoError(t, err)
	targets := expandSubnet(ipnet)
	require.Len(t, targets, 254)
	require.Equal(t, "192.168.1.1:80", targets[0])
	require.Equal(t, "192.168.1.254:80", targets[253])

	// A wider network collapses to its /24 slice.
	_, ip16, err := net.ParseCIDR("10.0.0.0/16")
	require.NoError(t, err)
	targets16 := expandSubnet(ip16)
	require.Len(t, targets16, 254)
	require.Equal(t, "10.0.0.1:80", targets16[0])
}
