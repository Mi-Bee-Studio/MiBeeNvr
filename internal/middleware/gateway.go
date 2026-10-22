package middleware

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// fnOS unified-gateway integration (issue #394).
//
// fnOS fronts third-party apps with a unified gateway: it validates the NAS
// user's login session, then forwards the request to a Unix socket inside the
// app's install directory and injects the verified identity as headers:
//
//	X-Trim-Userid:   1000
//	X-Trim-Isadmin:  true | false
//	X-Trim-Username: admin
//
// The NVR mounts GatewayAuthMiddleware ONLY on the Unix-socket listener, so
// these headers are only trusted where the fnOS gateway is the only reachable
// client. On the TCP listener the same headers are ignored — a browser hitting
// :9090 directly still needs the normal NVR login, which is exactly the
// "desktop embed = no login, direct access = login" contract (#394).

// GatewayIdentity is the fnOS-gateway-verified user context of a request.
type GatewayIdentity struct {
	Username string
	UserID   string
	Admin    bool
}

type gatewayContextKey struct{}

// GatewayAuthMiddleware extracts the trusted X-Trim-* identity headers into
// the request context, and marks the request as arrived via the gateway
// listener (viaGatewayKey) — unconditionally, because the fnOS gateway does
// not inject X-Trim-* on every forwarded request type: its WebSocket Upgrade
// forwarding drops them (observed in production fnOS, 2026-10-22 — every
// live-view tile 403ed in Upgrader.CheckOrigin). Mount it on the gateway
// (Unix socket) listener only — see the package comment. Requests without
// the headers pass through without an identity (they then fall back to
// normal token/BasicAuth) but still carry the listener marker.
func GatewayAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), viaGatewayKey{}, true)
		id := &GatewayIdentity{
			UserID:   strings.TrimSpace(r.Header.Get("X-Trim-Userid")),
			Username: strings.TrimSpace(r.Header.Get("X-Trim-Username")),
			Admin:    strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Trim-Isadmin")), "true"),
		}
		if id.Username == "" && id.UserID == "" {
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, gatewayContextKey{}, id)))
	})
}

type viaGatewayKey struct{}

// viaGateway reports whether the request arrived through the gateway
// (Unix-socket) listener. The marker is set by GatewayAuthMiddleware, which
// is mounted ONLY on that listener; the socket file is 0o660 inside a
// root-owned app directory, so no browser or LAN peer can reach it — the
// same transport trust the X-Trim-* headers rest on. The TCP listener never
// produces it.
func viaGateway(ctx context.Context) bool {
	v, _ := ctx.Value(viaGatewayKey{}).(bool)
	return v
}

// GatewayIdentityFromContext returns the gateway-verified identity, or nil when
// the request did not arrive (verified) through the gateway listener.
func GatewayIdentityFromContext(ctx context.Context) *GatewayIdentity {
	id, _ := ctx.Value(gatewayContextKey{}).(*GatewayIdentity)
	return id
}

// WithGatewayIdentity returns a derived context carrying a gateway identity,
// mirroring what GatewayAuthMiddleware sets. Exported so tests can exercise
// gateway-authenticated handlers without standing up a Unix-socket listener.
// Production code authenticates via the middleware, never this helper.
func WithGatewayIdentity(ctx context.Context, id *GatewayIdentity) context.Context {
	return context.WithValue(ctx, gatewayContextKey{}, id)
}

// StripBasePath returns middleware that strips a URL prefix (e.g.
// "/app/mibee-nvr") from incoming request paths before routing. Used when the
// app is served behind a reverse proxy / unified gateway under a fixed prefix
// (server.base_path). Paths without the prefix pass through unchanged, so the
// same listener also serves the app at "/" for direct access.
func StripBasePath(prefix string) func(http.Handler) http.Handler {
	prefix = strings.TrimRight(prefix, "/")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if prefix != "" && (r.URL.Path == prefix || strings.HasPrefix(r.URL.Path, prefix+"/")) {
				r2 := r.Clone(r.Context())
				r2.URL.Path = strings.TrimPrefix(r.URL.Path, prefix)
				if r2.URL.Path == "" {
					r2.URL.Path = "/"
				}
				next.ServeHTTP(w, r2)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// WSOriginAllowed is the shared Origin gate for every WebSocket upgrader
// (live streams, AVI playback, GB28181 talk-back).
//
// Non-browser clients send no Origin and pass. Browser origins must match the
// request Host — a cross-site page must not open NVR WebSockets. Exceptions:
// requests that arrived through the fnOS unified gateway. Their Host header
// is the gateway's forwarding host, not the browser origin, so the strict
// comparison would reject the app's own SPA — the exact breakage that killed
// H.265 live playback behind the gateway (WASM player rides the WS transport;
// gorilla's default CheckOrigin 403s, every H.265 tile goes "offline" while
// H.264/WebRTC still works). Two signals count as "via gateway":
//
//   - the verified X-Trim-* identity (GatewayAuthMiddleware), and
//   - the listener marker set unconditionally on the Unix-socket listener —
//     the fnOS gateway's WebSocket forwarding drops X-Trim-* headers but the
//     transport itself is still gateway-only (0o660 socket in a root-owned
//     app directory; a browser cannot forge either signal: no custom headers
//     on WebSocket, no reach to the socket; a forged X-Trim-* on the TCP
//     listener never becomes an identity or a marker).
func WSOriginAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if GatewayIdentityFromContext(r.Context()) != nil || viaGateway(r.Context()) {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}
