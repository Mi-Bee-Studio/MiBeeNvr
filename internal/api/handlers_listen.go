package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
)

// listenChangeFn rebinds the plain-HTTP listener (bind the new address first,
// persist the config, drain the old server, then serve). Injected from main
// via SetListenChangeFunc — the windows tray calls it in-process; this
// endpoint exists for the macOS menu-bar helper, which is a separate process.
var (
	listenMu       sync.Mutex
	listenChangeFn func(addr string) error
)

// SetListenChangeFunc wires the listener-swap trigger. Called once from main.
func SetListenChangeFunc(f func(addr string) error) {
	listenMu.Lock()
	listenChangeFn = f
	listenMu.Unlock()
}

// listenChangeRequest is the JSON body for PUT /api/system/listen.
type listenChangeRequest struct {
	Listen string `json:"listen"`
}

// handleListenChange handles PUT /api/system/listen — rebind the HTTP
// listener (e.g. 127.0.0.1:9090 → 0.0.0.0:9090 to open LAN access).
// Loopback-local only (IsBypassEligible), the same locality/authorization
// model as POST /api/auth/password and POST /api/system/shutdown: whoever
// sits at the machine could equally edit the config file and restart.
func handleListenChange(w http.ResponseWriter, r *http.Request) {
	if !middleware.IsBypassEligible(r) {
		WriteError(w, http.StatusForbidden,
			"listen change is only available from a local session on the NVR machine")
		return
	}

	var req listenChangeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	addr := strings.TrimSpace(req.Listen)
	if addr == "" {
		WriteError(w, http.StatusBadRequest, "listen must not be empty")
		return
	}
	if err := validListenAddr(addr); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	listenMu.Lock()
	fn := listenChangeFn
	listenMu.Unlock()
	if fn == nil {
		WriteError(w, http.StatusServiceUnavailable, "listen change is not wired")
		return
	}

	// The swap tears down the very listener this response rides on — answer
	// first, give the response a beat to flush, then rebind in the background
	// (the shutdown-endpoint pattern). A failed swap is logged server-side;
	// the current listener keeps serving.
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "applying", "listen": addr})
	go func() {
		time.Sleep(300 * time.Millisecond)
		if err := fn(addr); err != nil {
			slog.Warn("listen change failed", "addr", addr, "error", err)
		}
	}()
}

// validListenAddr shape-checks a listen address: optional host + mandatory
// port ("127.0.0.1:9090", "0.0.0.0:9090", ":9090", "[::1]:9090"). Whether the
// address actually binds is decided by the swap itself.
func validListenAddr(addr string) error {
	if len(addr) > 64 {
		return errors.New("listen address too long")
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return errors.New("listen must be [host:]port, e.g. 127.0.0.1:9090")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return errors.New("listen port must be 1-65535")
	}
	return nil
}
