package app

import (
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/api"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/metrics"
	authmw "github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	ui "github.com/Mi-Bee-Studio/MiBeeNvr/internal/ui"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/upload"
)

// buildRouter constructs the chi HTTP router with all middleware, routes, mounts,
// and the SPA static file handler. Called by RunFree.
func buildRouter(
	cfg *config.Config,
	authMW func(http.Handler) http.Handler,
	handler *api.Handler,
	metrics *metrics.Metrics,
	davHandler http.Handler,
	uploadHandler *upload.Handler,
	apiKeyStore *authmw.APIKeyStore,
) (http.Handler, error) {
	r := chi.NewRouter()
	// Reverse-proxy / unified-gateway base path (#394): strip the configured
	// prefix (e.g. /app/mibee-nvr) before anything else — routing, logging and
	// auth all see the stripped path. Unprefixed paths pass through unchanged,
	// so the same listener serves direct access at "/" too.
	basePath := config.NormalizeBasePath(cfg.Server.BasePath)
	if basePath != "" {
		r.Use(authmw.StripBasePath(basePath))
		slog.Info("base-path prefix stripping enabled", "prefix", basePath)
	}
	r.Use(authmw.RequestLogger(slog.Default(), "/api/health", "/api/readyz"))
	r.Use(chimiddleware.Recoverer)
	r.Use(authmw.SecurityHeaders(cfg.Security.FrameAncestors))
	r.Use(authmw.COOPHeaders)
	// Streaming gzip compression for all JSON/HTML/text responses.
	// SSE (text/event-stream) is also compressed but flushed per-event.
	// Already-compressed content (video, images) is auto-skipped.
	r.Use(authmw.StreamingGzip(5))

	// API Key middleware — validates Bearer mbv_* tokens for MiBeeVision and
	// per-device app tokens. Runs before authMW: if the request has an API Key
	// Bearer token, it's authenticated here; otherwise it falls through to
	// BasicAuth. Mounted unconditionally (when a store is wired) so keys minted
	// at runtime work without a restart (#335).
	if apiKeyStore != nil {
		r.Use(func(next http.Handler) http.Handler {
			return authmw.APIKeyAuthMiddleware(apiKeyStore, next)
		})
		slog.Info("API Key authentication enabled")
	}

	// Prometheus metrics — independent auth when configured, public otherwise
	if cfg.MetricsAuth.IsConfigured() {
		metricsAuthMW, _ := authmw.NewAuthMiddleware(authmw.AuthProvider{
			GetUsername: func() string { return cfg.MetricsAuth.Username },
			GetHash:     func() string { return cfg.MetricsAuth.PasswordHash },
		}, cfg.MetricsAuth.Password, authmw.AuthRateLimitConfig{})
		r.With(metricsAuthMW).Handle("/metrics", promhttp.HandlerFor(metrics.Registry, promhttp.HandlerOpts{ErrorHandling: promhttp.ContinueOnError}))
	} else {
		r.Handle("/metrics", promhttp.HandlerFor(metrics.Registry, promhttp.HandlerOpts{ErrorHandling: promhttp.ContinueOnError}))
	}

	r.Mount("/", handler.Routes())

	// WebDAV
	if davHandler != nil {
		r.Mount(cfg.WebDAV.PathPrefix, davHandler)
	}

	// Upload routes (authenticated)
	r.Group(func(r chi.Router) {
		r.Use(authMW)
		uploadHandler.RegisterRoutes(r)
	})

	// Static UI — serve from embedded filesystem
	staticContent, err := fs.Sub(ui.StaticFS, "static")
	if err != nil {
		return nil, fmt.Errorf("static fs: %w", err)
	}
	fileServer := http.FileServer(http.FS(staticContent))
	// When served under a base path (gateway/proxy), index.html must tell the
	// SPA the prefix so it can build absolute asset/API/stream URLs. Precompute
	// the injected copy once; nil = serve the original untouched.
	var indexBytes []byte
	if basePath != "" {
		raw, err := fs.ReadFile(staticContent, "index.html")
		if err != nil {
			return nil, fmt.Errorf("read index.html: %w", err)
		}
		inject := `<script>window.__NVR_BASE__="` + basePath + `";</script>`
		replaced := strings.Replace(string(raw), "</title>", "</title>"+inject, 1)
		if replaced == string(raw) {
			// No </title> anchor — fall back to injecting right after <head>.
			replaced = strings.Replace(string(raw), "<head>", "<head>"+inject, 1)
		}
		indexBytes = []byte(replaced)
	}
	// Static files served without auth — SPA handles login flow client-side.
	// All sensitive data is protected via API endpoints in handler.Routes().
	// Cache: index.html must not be cached (always fresh after deploy).
	// Assets have content-hash filenames — safe to cache long-term.
	r.NotFound(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" || path == "/index.html" {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			if indexBytes != nil {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Write(indexBytes)
				return
			}
		} else if strings.HasPrefix(path, "/assets/") {
			// Vite produces content-hash filenames (e.g. Cameras-CjnyKwd-.js).
			// Content changes → filename changes → safe to cache immutably.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		fileServer.ServeHTTP(w, r)
	}))

	return r, nil
}
