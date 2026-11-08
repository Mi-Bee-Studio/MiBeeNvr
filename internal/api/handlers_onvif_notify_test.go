package api

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// The short push consumer path (/api/onvif/n/{token}) is public (devices
// cannot authenticate) and answers an unknown token with 404 — never a 401
// from the auth chain, which would make a misrouted device retry forever.
func TestONVIFNotifyShortPathUnknownToken(t *testing.T) {
	h := setupDedupHandler(t)
	body := []byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body/></s:Envelope>`)
	rr := doRequest(t, h.Routes(), "POST", "/api/onvif/n/0123456789abcdef0123456789abcdef", bytes.NewReader(body), "", "")
	require.Equal(t, http.StatusNotFound, rr.Code, "body: %s", rr.Body.String())
}
