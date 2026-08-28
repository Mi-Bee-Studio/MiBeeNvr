// SPDX-License-Identifier: MIT
//
// DeviceManager coverage: user CRUD, network interfaces, reboot — through a
// connected onvif-go client backed by the mock SOAP server, plus the
// PasswordText raw-SOAP fallback GetUsers uses when WS-Security is rejected.

package onvif

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeviceManagerOperations(t *testing.T) {
	client, mock, _ := connectMockClient(t)
	mock.setHandler("SystemReboot", soapSystemRebootResponse)
	mock.setHandler("GetNetworkInterfaces", soapGetNetworkInterfacesResponse)
	mock.setHandler("GetUsers", soapGetUsersResponse)
	mock.setHandler("CreateUsers", `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><tds:CreateUsersResponse xmlns:tds="http://www.onvif.org/ver10/device/wsdl"/></s:Body></s:Envelope>`)
	mock.setHandler("DeleteUsers", `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><tds:DeleteUsersResponse xmlns:tds="http://www.onvif.org/ver10/device/wsdl"/></s:Body></s:Envelope>`)
	mock.setHandler("SetUser", `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><tds:SetUserResponse xmlns:tds="http://www.onvif.org/ver10/device/wsdl"/></s:Body></s:Envelope>`)

	dm := client.NewDeviceManager()
	require.NotNil(t, dm)
	ctx := context.Background()

	require.NoError(t, dm.SystemReboot(ctx))
	require.Equal(t, 1, mock.callCount("SystemReboot"))

	ifaces, err := dm.GetNetworkInterfaces(ctx)
	require.NoError(t, err)
	require.Len(t, ifaces, 1)
	require.Equal(t, "eth0", ifaces[0].Name)
	require.True(t, ifaces[0].Enabled)
	require.True(t, ifaces[0].IPv4.Enabled)
	require.Equal(t, "192.168.1.100", ifaces[0].IPv4.Address)
	require.Equal(t, "ffffff00", ifaces[0].IPv4.Netmask) // hex form, per formatPrefixMask
	require.False(t, ifaces[0].IPv4.DHCP)

	users, err := dm.GetUsers(ctx)
	require.NoError(t, err)
	require.Len(t, users, 2)
	require.Equal(t, "admin", users[0].Username)
	require.Equal(t, "Administrator", users[0].Level)
	require.Equal(t, "operator", users[1].Username)

	require.NoError(t, dm.CreateUsers(ctx, []ONVIFUser{{Username: "newuser", Password: "pw123", Level: "User"}}))
	require.Equal(t, 1, mock.callCount("CreateUsers"))
	require.NoError(t, dm.DeleteUsers(ctx, []string{"newuser"}))
	require.Equal(t, 1, mock.callCount("DeleteUsers"))
	require.NoError(t, dm.SetUser(ctx, "admin", "newpw"))
	require.Equal(t, 1, mock.callCount("SetUser"))
}

// TestDeviceManagerGetUsersPasswordTextFallback proves the fallback chain: when
// the digest-authenticated call is rejected with 401, GetUsers retries via raw
// SOAP with PasswordText WS-Security and succeeds.
func TestDeviceManagerGetUsersPasswordTextFallback(t *testing.T) {
	var rawFallbackCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body := string(b)
		switch {
		case strings.Contains(body, "GetCapabilities"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(soapGetCapabilitiesResponse))
		case strings.Contains(body, "GetUsers") && strings.Contains(body, "PasswordText"):
			rawFallbackCalls.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(soapGetUsersResponse))
		default:
			// Everything digest-authenticated is rejected — the camera-firmware
			// behavior that motivates the fallback (device_mgmt.go header comment).
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("denied"))
		}
	}))
	t.Cleanup(srv.Close)

	client := NewClient(srv.URL, "admin", "pw")
	require.NoError(t, client.Connect(context.Background()))
	dm := client.NewDeviceManager()

	users, err := dm.GetUsers(context.Background())
	require.NoError(t, err)
	require.Len(t, users, 2)
	require.Equal(t, int32(1), rawFallbackCalls.Load())
}

func TestDeviceManagerOperationErrors(t *testing.T) {
	// No handlers registered: every op receives a 500 SOAP fault.
	client, _, _ := connectMockClient(t)
	dm := client.NewDeviceManager()
	ctx := context.Background()

	require.Error(t, dm.SystemReboot(ctx))
	_, err := dm.GetNetworkInterfaces(ctx)
	require.Error(t, err)
	require.Error(t, dm.CreateUsers(ctx, nil))
	require.Error(t, dm.DeleteUsers(ctx, nil))
	require.Error(t, dm.SetUser(ctx, "u", "p"))
}

// redirectProbePorts points ProbeSerial's port list at a local httptest server
// (single attempt, no real-network fallback).
func redirectProbePorts(t *testing.T, srv *httptest.Server) {
	t.Helper()
	_, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	origPorts := ProbePorts
	ProbePorts = []string{port}
	t.Cleanup(func() { ProbePorts = origPorts })
}

func TestProbeSerialViaHTTP(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(soapGetDeviceInformationResponse))
	}))
	t.Cleanup(good.Close)
	redirectProbePorts(t, good)

	serial, ok := ProbeSerial(context.Background(), "127.0.0.1")
	require.True(t, ok)
	require.Equal(t, "SN12345", serial)

	// Empty IP short-circuits without touching the network.
	_, ok = ProbeSerial(context.Background(), "  ")
	require.False(t, ok)

	// Cancelled context stops the port loop.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, ok = ProbeSerial(ctx, "127.0.0.1")
	require.False(t, ok)
}

func TestProbeSerialNoSerial(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<no-serial-here/>"))
	}))
	t.Cleanup(bad.Close)
	redirectProbePorts(t, bad)

	_, ok := ProbeSerial(context.Background(), "127.0.0.1")
	require.False(t, ok)
}
