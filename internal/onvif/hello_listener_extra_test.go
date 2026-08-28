// SPDX-License-Identifier: MIT
//
// HelloListener long tail: constructor, dispatch panic recovery, Stop
// idempotence, isTimeout classification, and EnrichDevice against a mock
// ONVIF endpoint.

package onvif

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewHelloListenerInterfaceLookup(t *testing.T) {
	l, err := NewHelloListener("", func(DiscoveredDevice) {})
	require.NoError(t, err)
	require.NotNil(t, l)

	// A real loopback interface resolves.
	lo, err := NewHelloListener("lo", func(DiscoveredDevice) {})
	require.NoError(t, err)
	require.NotNil(t, lo)
	require.NotNil(t, lo.iface)

	_, err = NewHelloListener("no-such-iface-xyz", func(DiscoveredDevice) {})
	require.Error(t, err)
}

func TestHelloListenerDispatch(t *testing.T) {
	got := make(chan DiscoveredDevice, 1)
	l, err := NewHelloListener("", func(d DiscoveredDevice) { got <- d })
	require.NoError(t, err)

	l.dispatch(DiscoveredDevice{UUID: "u1", Endpoint: "http://127.0.0.1/x"})
	select {
	case d := <-got:
		require.Equal(t, "u1", d.UUID)
	default:
		t.Fatal("handler not invoked")
	}

	// A panicking handler must not escape dispatch.
	l2, err := NewHelloListener("", func(DiscoveredDevice) { panic("handler bug") })
	require.NoError(t, err)
	require.NotPanics(t, func() { l2.dispatch(DiscoveredDevice{}) })
}

func TestHelloListenerStopIdempotent(t *testing.T) {
	l, err := NewHelloListener("", func(DiscoveredDevice) {})
	require.NoError(t, err)
	l.Stop() // before Start: only flips the stopped flag
	l.Stop() // second call is a no-op
	require.True(t, l.stopped)
}

func TestIsTimeoutClassification(t *testing.T) {
	require.True(t, isTimeout(&netTimeoutError{}))
	require.False(t, isTimeout(errors.New("connection refused")))
	require.False(t, isTimeout(&notTimeoutError{}))
}

type netTimeoutError struct{}

func (*netTimeoutError) Error() string { return "i/o timeout" }
func (*netTimeoutError) Timeout() bool { return true }

type notTimeoutError struct{}

func (*notTimeoutError) Error() string { return "reset" }
func (*notTimeoutError) Timeout() bool { return false }

func TestHelloListenerStartAfterStop(t *testing.T) {
	l, err := NewHelloListener("", func(DiscoveredDevice) {})
	require.NoError(t, err)
	l.Stop()
	// Start on a stopped listener is a documented no-op success.
	require.NoError(t, l.Start(context.Background()))
}

func TestEnrichDeviceViaMock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(soapGetDeviceInformationResponse))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)

	dev := &DiscoveredDevice{XAddrs: []string{srv.URL}}
	EnrichDevice(ctx, dev)
	require.Equal(t, "TestMfg", dev.Manufacturer)
	require.Equal(t, "CamModel-X", dev.Model)
	require.Equal(t, "2.1.0", dev.Firmware)
	require.Equal(t, "SN12345", dev.Serial)
	require.Equal(t, "TestMfg", dev.Name) // Name backfilled from Manufacturer

	// Empty endpoint → untouched.
	empty := &DiscoveredDevice{}
	EnrichDevice(ctx, empty)
	require.Equal(t, "", empty.Serial)

	// Existing fields are not overwritten; XAddrs fallback works.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv2.Close)
	keep := &DiscoveredDevice{Name: "custom", XAddrs: []string{srv2.URL}}
	EnrichDevice(ctx, keep)
	require.Equal(t, "custom", keep.Name)
	require.Equal(t, "", keep.Serial)
}
