package api

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
)

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	// JSON-body credentials: behind a unified gateway (fnOS "/app/mibee-nvr")
	// the gateway claims ANY Authorization header as its own session credential
	// and refuses the request (HTTP 200 + "invalid token"), so the SPA cannot
	// send Basic auth there — after an explicit logout the login form would be
	// dead-ended with a gateway error. When the header is absent, accept
	// {username,password} from the body by synthesizing the Basic header;
	// validation still runs through the same auth middleware below (bcrypt,
	// failure lockout, SETUP_REQUIRED), so nothing about the checks changes.
	if r.Header.Get("Authorization") == "" && r.Body != nil {
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err == nil && body.Username != "" {
			r.Header.Set("Authorization", "Basic "+
				base64.StdEncoding.EncodeToString([]byte(body.Username+":"+body.Password)))
		}
	}

	// Validate credentials by running through the auth middleware.
	// If auth is disabled, any request succeeds; otherwise BasicAuth is checked.
	// Use httptest.ResponseRecorder to capture middleware output without writing to client w.
	done := make(chan int, 1)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		done <- http.StatusOK
	})
	rec := httptest.NewRecorder()
	h.authMW(inner).ServeHTTP(rec, r)

	select {
	case status := <-done:
		if status == http.StatusOK {
			// Credentials validated by the middleware (BasicAuth path). Mint a
			// stateless signed session token so the browser can stop carrying the
			// reversible base64(user:pass). The username comes from the request's
			// BasicAuth header (just validated); the bcrypt hash from config drives
			// the HMAC key, so a later password change invalidates this token.
			// Tests use a nil-config handler, so guard against that here.
			if h.config == nil {
				writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
				return
			}
			username := h.config.Auth.Username
			if u, _, ok := r.BasicAuth(); ok && u != "" {
				username = u
			}
			hash := h.config.Auth.PasswordHash
			token, expiresAt := middleware.SignSessionToken(username, hash, time.Now())
			writeJSON(w, http.StatusOK, map[string]string{
				"status":     "ok",
				"token":      token,
				"expires_at": expiresAt.UTC().Format(time.RFC3339),
			})
		}
	default:
		// Forward the middleware's captured response (503 SETUP_REQUIRED, 401, etc.)
		// without double-writing to the client.
		for k, vv := range rec.Header() {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(rec.Code)
		w.Write(rec.Body.Bytes())
	}
}

// handleGatewaySession mints an NVR session token for a request that arrived
// through the fnOS unified gateway with a verified ADMIN identity (issue #394).
// The SPA calls this on boot when it has no stored token: inside the fnOS
// desktop (gateway iframe) it gets a token and skips the login page; accessed
// directly on :9090 there is no gateway identity context, so it always 401s
// and the SPA shows the normal login form.

// handleGatewaySession mints an NVR session token for a request that arrived
// through the fnOS unified gateway with a verified ADMIN identity (issue #394).
// The SPA calls this on boot when it has no stored token: inside the fnOS
// desktop (gateway iframe) it gets a token and skips the login page; accessed
// directly on :9090 there is no gateway identity context, so it always 401s
// and the SPA shows the normal login form.
func (h *Handler) handleGatewaySession(w http.ResponseWriter, r *http.Request) {
	gi := middleware.GatewayIdentityFromContext(r.Context())
	if gi == nil || !gi.Admin {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not available on this listener"})
		return
	}
	if h.config == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no config"})
		return
	}
	hash := h.config.Auth.PasswordHash
	if hash == "" {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("WWW-Authenticate", `Basic realm="MiBee NVR"`)
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"setup required","code":"SETUP_REQUIRED"}`))
		return
	}
	token, expiresAt := middleware.SignSessionToken(h.config.Auth.Username, hash, time.Now())
	writeJSON(w, http.StatusOK, map[string]string{
		"status":       "ok",
		"token":        token,
		"expires_at":   expiresAt.UTC().Format(time.RFC3339),
		"gateway_user": gi.Username,
	})
}
