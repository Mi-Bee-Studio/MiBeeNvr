package api

import (
	"net/http"
	"sync"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
)

// shutdownFunc triggers the same graceful shutdown path as SIGINT/SIGTERM.
// Injected from main via SetShutdownFunc (the SetUpdateChecker package-var
// pattern). The macOS menu-bar helper is a SEPARATE process — unlike the
// windows tray it cannot close an in-process channel, so it calls this
// endpoint instead.
var (
	shutdownMu     sync.Mutex
	shutdownFunc   func()
	shutdownDone   = make(chan struct{})
	shutdownClosed sync.Once
)

// SetShutdownFunc wires the graceful-stop trigger. Called once from main.
func SetShutdownFunc(f func()) {
	shutdownMu.Lock()
	shutdownFunc = f
	shutdownMu.Unlock()
}

// streamShutdown unblocks the SSE/streaming handler loops so
// http.Server.Shutdown does not wait on never-idle connections (a tray quit
// with the web UI open used to stall the full shutdown timeout on the SPA's
// /api/events stream). main registers CloseStreams via
// http.Server.RegisterOnShutdown, which fires as soon as Shutdown begins.
var (
	streamShutdown     = make(chan struct{})
	streamShutdownOnce sync.Once
)

// CloseStreams signals every streaming handler loop to return. Idempotent.
func CloseStreams() {
	streamShutdownOnce.Do(func() { close(streamShutdown) })
}

// resetStreamShutdownForTest swaps in a fresh signal so one test firing
// CloseStreams cannot poison the rest of the suite (the SSE loops re-read
// the package var on every select iteration, so this takes effect
// immediately). Test-only.
func resetStreamShutdownForTest() {
	streamShutdown = make(chan struct{})
	streamShutdownOnce = sync.Once{}
}

// handleSystemShutdown handles POST /api/system/shutdown — graceful stop,
// loopback-local only (IsBypassEligible), the same locality/authorization
// model as POST /api/auth/password: whoever sits at the machine can already
// Ctrl+C the process. Remote callers get 403.
func handleSystemShutdown(w http.ResponseWriter, r *http.Request) {
	if !middleware.IsBypassEligible(r) {
		WriteError(w, http.StatusForbidden,
			"shutdown is only available from a local session on the NVR machine")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "shutting down"})

	// Serve the response, then fire once in the background — never block the
	// handler on the shutdown path.
	shutdownClosed.Do(func() {
		close(shutdownDone)
		go func() {
			shutdownMu.Lock()
			f := shutdownFunc
			shutdownMu.Unlock()
			if f != nil {
				f()
			}
		}()
	})
}
