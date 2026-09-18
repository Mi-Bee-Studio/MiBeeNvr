// Package tray gives the NVR process a desktop management entry: a system
// tray icon (windows builds) with 打开 Web 界面 / 退出 — without it a desktop
// run has no discoverable way to reach the UI or stop the server (user
// request 2026-09-18). All other platforms are a silent no-op: the tray must
// never gate server startup.
package tray

import (
	"log/slog"
	"net"
	"strings"
)

// Options configures the tray.
type Options struct {
	// Tooltip is the hover text (keep short; the shell truncates at 128 chars).
	Tooltip string
	// OpenURL is opened by the menu entry / double-click. Empty hides the entry.
	OpenURL string
}

// Start shows the tray icon and returns a stop function (removes the icon —
// always call it on shutdown) and a quit channel (closed when the user picks
// 退出 in the menu). On any failure, or on non-windows platforms, it returns
// a no-op stop and a never-closed channel — the server keeps running.
func Start(opts Options) (stop func(), quit <-chan struct{}) {
	stop, quit, err := startPlatform(opts)
	if err != nil {
		slog.Info("tray unavailable, server unaffected", "error", err)
		never := make(chan struct{})
		return func() {}, never
	}
	return stop, quit
}

// ListenURL turns a server listen address into the URL a desktop browser on
// the same machine should open: wildcard binds map to loopback, everything
// else is used verbatim.
func ListenURL(listen string) string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(listen))
	if err != nil {
		return "http://127.0.0.1:9090"
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}
