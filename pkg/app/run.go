package app

// run.go is the thin orchestrator for RunFree. The construction phase lives in
// builders.go (buildAppDeps) and the registration phase lives in register.go
// (registerServices + registerValues). This file just wires them together and
// handles the error-path teardown. Split out of a former 1082-line single
// function for maintainability (#138) — behavior is identical, verified by
// TestRunFree_ServiceOrder.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	_ "net/http/pprof"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"

	_ "time/tzdata" // embed timezone database for minimal containers (scratch/alpine)

	_ "github.com/Mi-Bee-Studio/MiBeeNvr/internal/xiaomi"
)

// pprofServer exposes net/http/pprof on loopback only (localhost:6060). The
// blank import registers handlers on http.DefaultServeMux; nothing else
// serves that mux, so without a listener the profile endpoints are
// unreachable — missing exactly when diagnosing memory incidents (e.g. the
// 2026-08-17 OOM post-mortem).
func pprofServer() *http.Server {
	return &http.Server{
		Addr:              "127.0.0.1:6060",
		Handler:           http.DefaultServeMux,
		ReadHeaderTimeout: 5 * time.Second,
	}
}

// RunFree constructs and returns a configured *App with all open-source services
// registered in the correct start/stop order.
//
// The returned App has Start/Stop lifecycle management built in. Callers can
// Register() additional services (e.g. Pro/P2P extensions) before Start().
//
// Example:
//
//	a, err := app.RunFree(cfg, configPath)
//	if err != nil { return err }
//	if err := a.Start(ctx); err != nil { return err }
//
// Construction order (buildAppDeps) and start/stop order (registerServices)
// are deliberately separate concerns: managers must be built in dependency
// order (each needs its prerequisites constructed), while services must be
// registered in lifecycle order (Start = registration order, Stop = reverse).
// See internal AGENTS.md for the canonical service order.
func RunFree(cfg *config.Config, configPath string) (*App, error) {
	// Phase 1: construct every manager/handler/router. The returned cleanup
	// cancels the startup background goroutines + closes the DB; it must be
	// called if we bail out before App.Start owns the lifecycle.
	deps, cleanup, err := buildAppDeps(cfg, configPath)
	if err != nil {
		return nil, err
	}

	a := New()

	// Phase 2: register services in start/stop order. On failure, run the
	// construction cleanup so we don't leak the DB handle / background goroutines.
	if err := registerServices(a, deps); err != nil {
		cleanup()
		return nil, err
	}

	// Phase 3: expose typed value handles for out-of-module consumers.
	if err := registerValues(a, deps); err != nil {
		cleanup()
		return nil, err
	}

	// Phase 4: loopback diagnostics (pprof) — registered last, stopped first.
	if err := a.Register(&pprofService{srv: pprofServer()}); err != nil {
		cleanup()
		return nil, err
	}

	return a, nil
}

// pprofService adapts the loopback pprof server to the App service lifecycle.
type pprofService struct {
	srv *http.Server
}

func (p *pprofService) Name() string { return "pprof-loopback" }

func (p *pprofService) Start(_ context.Context) error {
	go func() {
		if err := p.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("pprof loopback server stopped", "error", err)
		}
	}()
	return nil
}

func (p *pprofService) Stop() error { return p.srv.Close() }
