package middleware

import (
	"context"
	"net/http"
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
// the request context. Mount it on the gateway (Unix socket) listener only —
// see the package comment. Requests without the headers pass through without
// an identity (they then fall back to normal token/BasicAuth).
func GatewayAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := &GatewayIdentity{
			UserID:   strings.TrimSpace(r.Header.Get("X-Trim-Userid")),
			Username: strings.TrimSpace(r.Header.Get("X-Trim-Username")),
			Admin:    strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Trim-Isadmin")), "true"),
		}
		if id.Username == "" && id.UserID == "" {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), gatewayContextKey{}, id)))
	})
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
