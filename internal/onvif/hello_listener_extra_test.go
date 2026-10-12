// SPDX-License-Identifier: MIT
//
// HelloListener adapter long tail: constructor interface lookup and
// EnrichDevice against a mock ONVIF endpoint. The socket loop, panic
// recovery, Stop idempotence and stop/restart semantics are owned (and
// tested) by onvif-go's discovery.Listener.

package onvif

import (
	"context"
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

	// A bogus interface name must fail construction.
	_, err = NewHelloListener("no-such-iface-xyz", func(DiscoveredDevice) {})
	require.Error(t, err)

	// Stop before Start and Stop twice are both safe on the wrapper.
	l.Stop()
	l.Stop()
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
