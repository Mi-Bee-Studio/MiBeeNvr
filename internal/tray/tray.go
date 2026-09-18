// Package tray gives the NVR process a desktop management entry: a system
// tray icon (windows builds) with 打开 Web 界面 / 修改密码 / 监听地址 / 退出 —
// without it a desktop run has no discoverable way to reach the UI or stop
// the server (user request 2026-09-18). All other platforms are a silent
// no-op: the tray must never gate server startup.
package tray

import (
	"errors"
	"log/slog"
	"net"
	"strconv"
	"strings"
)

// Options configures the tray.
type Options struct {
	// Tooltip is the hover text (keep short; the shell truncates at 128 chars).
	Tooltip string
	// OpenURL is opened by the menu entry / single click. Empty hides the entry.
	OpenURL string
	// Version decorates the menu header (e.g. "MiBee NVR  v0.12.0…").
	Version string
	// ListenAddr prefills the 监听地址 dialog and shows in the menu header.
	ListenAddr string
	// OnChangeListen, when non-nil, enables the 监听地址… menu entry. It is
	// called on the tray thread with the validated address and must perform
	// the actual listener swap (main binds the new address first, so an error
	// leaves the current server untouched).
	OnChangeListen func(addr string) error
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

// ValidateListenAddr checks the shape of an address typed into the 监听地址
// dialog: optional host + mandatory port ("127.0.0.1:9090", "0.0.0.0:9090",
// ":9090", "[::1]:9090"). Error messages are user-facing (zh) — the dialog
// shows them verbatim.
func ValidateListenAddr(addr string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return errors.New("监听地址不能为空")
	}
	if len(addr) > 64 {
		return errors.New("地址过长")
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return errors.New("格式应为 [IP:]端口，例如 127.0.0.1:9090")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return errors.New("端口无效（需 1-65535）")
	}
	return nil
}
