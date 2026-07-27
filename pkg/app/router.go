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
) (http.Handler, error) {
	r := chi.NewRouter()
	r.Use(authmw.RequestLogger(slog.Default(), "/api/health", "/api/readyz"))
	r.Use(chimiddleware.Recoverer)
	r.Use(authmw.SecurityHeaders)
	r.Use(authmw.COOPHeaders)
	// Streaming gzip compression for all JSON/HTML/text responses.
	// SSE (text/event-stream) is also compressed but flushed per-event.
	// Already-compressed content (video, images) is auto-skipped.
	r.Use(authmw.StreamingGzip(5))

	// API Key middleware — validates Bearer mbv_* tokens for MiBeeVision.
	// Runs before authMW: if the request has an API Key Bearer token, it's
	// authenticated here; otherwise it falls through to BasicAuth.
	if len(cfg.APIKeys) > 0 {
		validKeys := make(map[string]string)
		for _, k := range cfg.APIKeys {
			if !k.Revoked && k.Key != "" {
				validKeys[k.Key] = k.Name
			}
		}
		if len(validKeys) > 0 {
			r.Use(func(next http.Handler) http.Handler {
				return authmw.APIKeyAuthMiddleware(validKeys, next)
			})
			slog.Info("API Key authentication enabled", "keys", len(validKeys))
		}
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
	// Static files served without auth — SPA handles login flow client-side.
	// All sensitive data is protected via API endpoints in handler.Routes().
	// Cache: index.html must not be cached (always fresh after deploy).
	// Assets have content-hash filenames — safe to cache long-term.
	r.NotFound(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" || path == "/index.html" {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		} else if strings.HasPrefix(path, "/assets/") {
			// Vite produces content-hash filenames (e.g. Cameras-CjnyKwd-.js).
			// Content changes → filename changes → safe to cache immutably.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		fileServer.ServeHTTP(w, r)
	}))

	return r, nil
}
