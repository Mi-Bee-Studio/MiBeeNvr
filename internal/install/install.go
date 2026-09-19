// Package install implements per-user desktop installation for the NVR
// (`mibee-nvr install` / `mibee-nvr uninstall`, windows + darwin): copy the
// binary to a per-user location, wire OS integration (Add/Remove Programs
// entry, Start Menu shortcut, login autostart on windows; LaunchAgent on
// darwin), and provide the matching removal path. Everything is per-user —
// no admin rights are needed or used on either platform.
//
// Design rules:
//   - data (config + storage + logs) lives OUTSIDE the program dir so an
//     uninstall keeps it unless --purge is given;
//   - the starter config leaves auth empty on purpose — the web UI's
//     first-run setup wizard then walks the user through setting the admin
//     password;
//   - install is idempotent (re-running refreshes files and integration).
package install

import (
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// Options carries values only the command layer knows.
type Options struct {
	// Version is the binary's appVersion (used in the ARP entry / summary).
	Version string
}

// SetupConsole makes the install/uninstall console output readable where the
// command layer prints (windows: switch the console to UTF-8; others: no-op).
func SetupConsole() { setupConsole() }

// Result describes what one install/uninstall run did, for the command layer
// to print.
type Result struct {
	Exe      string   // installed binary path
	Config   string   // config file path
	DataDir  string   // data directory (config + storage + logs)
	Started  bool     // whether the NVR was started as part of the run
	Notes    []string // platform hints (URL, logs location, autostart)
	KeptData bool     // uninstall: data dir was intentionally kept
}

// StarterConfig renders the first-run config written when none exists. Auth
// stays empty → setup wizard on first web visit. local_bypass is on: the
// desktop password is for NON-local (LAN) logins — a browser on the machine
// itself skips auth (loopback + loopback Host only, never behind a proxy).
// The listener binds loopback ONLY: passwordless local login must not be
// reachable from the LAN by default — the tray 菜单栏「监听地址…」 entry opens
// it up (0.0.0.0) when the user wants that.
func StarterConfig(dataDir string) string {
	return fmt.Sprintf(`# MiBee NVR — 初始配置（由 mibee-nvr install 生成）
# 本机浏览器免密直入；密码用于局域网/远程登录（可用托盘菜单「修改密码」设置）。
# 默认仅监听本机（127.0.0.1）；如需局域网访问，用托盘/菜单栏「监听地址…」改为 0.0.0.0:9090。
server:
  listen: "127.0.0.1:9090"
storage:
  root_dir: '%s'
auth:
  local_bypass: true
cameras: []
`, yamlSingle(filepath.Join(dataDir, "data")))
}

// yamlSingle single-quotes a scalar for YAML: single-quoted style keeps
// backslashes verbatim (windows paths), only ' needs doubling.
func yamlSingle(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// healthURLForConfig resolves the loopback /api/health URL from the config's
// listen address (wildcards probed on loopback).
//
//nolint:unused // called only from the windows/darwin install files — on the linux lint platform both are build-tagged out
func healthURLForConfig(cfgPath string) string {
	addr := "127.0.0.1:9090"
	if cfg, err := config.Load(cfgPath); err == nil && cfg.Server.Listen != "" {
		if host, port, err := net.SplitHostPort(strings.TrimSpace(cfg.Server.Listen)); err == nil {
			switch host {
			case "", "0.0.0.0", "::", "[::]":
				host = "127.0.0.1"
			}
			addr = net.JoinHostPort(host, port)
		}
	}
	return "http://" + addr + "/api/health"
}

// waitForHTTP polls url until it answers 2xx or the timeout lapses — the
// honest "Started" behind every install summary (an agent killed at exec
// surfaces here instead of as a silent non-start).
//
//nolint:unused // called only from the windows/darwin install files — on the linux lint platform both are build-tagged out
func waitForHTTP(url string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return true
			}
		}
		time.Sleep(400 * time.Millisecond)
	}
	return false
}

// LaunchAgentPlist renders the per-user launchd agent keeping the NVR
// running (RunAtLoad + KeepAlive), with logs appended to a file in the data
// dir. Pure string rendering so it stays unit-testable on every platform.
func LaunchAgentPlist(exe, cfg, log string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")
	b.WriteString("\t<key>Label</key><string>com.mibee-nvr</string>\n")
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	fmt.Fprintf(&b, "\t\t<string>%s</string>\n", exe)
	b.WriteString("\t\t<string>-config</string>\n")
	fmt.Fprintf(&b, "\t\t<string>%s</string>\n", cfg)
	b.WriteString("\t</array>\n")
	b.WriteString("\t<key>RunAtLoad</key><true/>\n")
	b.WriteString("\t<key>KeepAlive</key><true/>\n")
	fmt.Fprintf(&b, "\t<key>StandardOutPath</key><string>%s</string>\n", log)
	fmt.Fprintf(&b, "\t<key>StandardErrorPath</key><string>%s</string>\n", log)
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}
