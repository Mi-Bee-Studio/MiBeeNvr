package onvif

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// classifyAuthKind reports which WS-Security shape a request carried:
// "digest", "text" (UsernameToken PasswordText) or "none".
func classifyAuthKind(body string) string {
	switch {
	case strings.Contains(body, "#PasswordDigest"):
		return "digest"
	case strings.Contains(body, "#PasswordText"):
		return "text"
	default:
		return "none"
	}
}

// authQuirkMode selects the fake device's empty-body behavior.
type authQuirkMode string

const (
	// quirkDigestEmpty: only digest requests get the empty body (#1051).
	quirkDigestEmpty authQuirkMode = "digest-empty"
	// quirkAllEmpty: every request gets an empty body.
	quirkAllEmpty authQuirkMode = "all-empty"
)

// newAuthQuirkServer fakes an old-firmware device (#1051): a request the
// firmware cannot handle gets HTTP 200 with an empty body — no fault, no
// status hint — while servable shapes are answered normally.
func newAuthQuirkServer(t *testing.T, mode authQuirkMode) (*httptest.Server, func() []string) {
	t.Helper()

	var mu sync.Mutex
	var kinds []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s := string(body)

		kind := classifyAuthKind(s)
		mu.Lock()
		kinds = append(kinds, kind)
		action := clientExtractSOAPAction(s)
		mu.Unlock()

		if mode == quirkAllEmpty || (mode == quirkDigestEmpty && kind == "digest") {
			w.WriteHeader(http.StatusOK)

			return
		}

		var response string
		switch action {
		case "GetCapabilities":
			response = soapGetCapabilitiesResponse
		case "GetDeviceInformation":
			response = soapGetDeviceInformationResponse
		default:
			// GetServices probe and skew queries tolerate empty answers.
			w.WriteHeader(http.StatusOK)

			return
		}

		w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, response)
	}))
	t.Cleanup(srv.Close)

	snapshot := func() []string {
		mu.Lock()
		defer mu.Unlock()

		return append([]string(nil), kinds...)
	}

	return srv, snapshot
}

func TestClientConnectAuthFallbackEmptyDigestBody(t *testing.T) {
	srv, snapshot := newAuthQuirkServer(t, quirkDigestEmpty)

	client := NewClient(srv.URL, "admin", "secret")
	require.NoError(t, client.Connect(context.Background()))

	kinds := snapshot()
	require.GreaterOrEqual(t, len(kinds), 2, "expected at least digest + password-text attempts: %v", kinds)
	require.Equal(t, "digest", kinds[0])
	require.Equal(t, "text", kinds[1], "empty-bodied digest must advance the ladder to password-text")

	info, err := client.GetDeviceInformation(context.Background())
	require.NoError(t, err)
	require.Equal(t, "TestMfg", info.Manufacturer)

	digestAttempts := 0
	for _, k := range snapshot() {
		if k == "digest" {
			digestAttempts++
		}
	}
	require.Equal(t, 1, digestAttempts, "sticky auth must keep later calls on the working mode")
}

func TestClientConnectAllModesEmptyBodySurfacesOriginalError(t *testing.T) {
	// Every auth shape gets 200 + empty body: the exhausted ladder must fail
	// Connect with the empty-body cause, not a misleading auth error.
	srv, _ := newAuthQuirkServer(t, quirkAllEmpty)

	client := NewClient(srv.URL, "admin", "secret")
	err := client.Connect(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "received empty response body")
}
