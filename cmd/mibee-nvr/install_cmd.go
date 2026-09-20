package main

import (
	"fmt"
	"os"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/install"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/tray"
)

// cmdInstall / cmdUninstall — desktop (windows/darwin) per-user install
// guidance: mibee-nvr install | mibee-nvr uninstall [--purge]. cmdInstall is
// also what a bare double-click of the Setup exe / DMG app lands in
// (desktop_entry.go) — with autoOpen, so the installer finishes by landing
// the user in the first-run wizard. Explicit CLI installs stay quiet.
func cmdInstall(autoOpen bool) {
	install.EnsureOwnedConsole() // GUI-subsystem: terminal-less launches (Setup double-click) get a visible console
	install.SetupConsole()
	res, err := install.Install(install.Options{Version: appVersion})
	if err != nil {
		fmt.Fprintf(os.Stderr, "安装失败: %v\n", err)
		install.NotifyDialog("MiBee NVR 安装失败：\n" + err.Error())
		pauseIfInteractive()
		os.Exit(1)
	}
	fmt.Println("✅ MiBee NVR 安装完成（仅当前用户，无需管理员权限）")
	fmt.Printf("  程序: %s\n  配置: %s\n  数据: %s\n", res.Exe, res.Config, res.DataDir)
	if res.Started {
		fmt.Println("  已启动 MiBee NVR")
	}
	for _, n := range res.Notes {
		fmt.Printf("  · %s\n", n)
	}
	if autoOpen {
		if res.Started {
			install.OpenBrowser(webUIURL(res.Config))
			install.NotifyDialog("MiBee NVR 已安装并启动。\n任务栏托盘/菜单栏出现管理图标，浏览器即将打开管理界面。")
		} else {
			install.NotifyDialog("MiBee NVR 已安装，但服务未能在 10 秒内就绪。\n请查看下列备注与日志（nvr.log）；确认服务已启动后，从菜单栏/托盘打开管理界面。")
		}
	}
	pauseIfInteractive()
	os.Exit(0)
}

// webUIURL derives the loopback URL from the installed config (best-effort:
// falls back to the desktop default port).
func webUIURL(cfgPath string) string {
	if cfg, err := config.Load(cfgPath); err == nil && cfg.Server.Listen != "" {
		return tray.ListenURL(cfg.Server.Listen)
	}
	return "http://127.0.0.1:9090"
}

func cmdUninstall() {
	install.EnsureOwnedConsole() // GUI-subsystem: the ARP uninstall path has no terminal to attach to
	install.SetupConsole()
	// darwin: the teardown this triggers (unload → NVR stop → agent bootout)
	// would kill an uninstaller still inside a launchd job's process group —
	// detach first so removal always runs to completion.
	install.DetachForUninstall()
	purge := false
	for _, a := range os.Args[2:] {
		if a == "--purge" {
			purge = true
		}
	}
	res, err := install.Uninstall(purge)
	if err != nil {
		fmt.Fprintf(os.Stderr, "卸载失败: %v\n", err)
		pauseIfInteractive()
		os.Exit(1)
	}
	fmt.Println("🗑️  MiBee NVR 卸载完成")
	for _, n := range res.Notes {
		fmt.Printf("  · %s\n", n)
	}
	pauseIfInteractive()
	os.Exit(0)
}

// pauseIfInteractive keeps the console open when launched from Explorer /
// Add/Remove Programs so the summary is readable — but BOUNDED: waiting for
// an Enter that never comes used to zombie the ARP uninstaller, which in
// turn locked the exe and silently sank the delayed self-delete (field
// report 2026-09-19). Terminal/piped invocations flow through instantly.
func pauseIfInteractive() {
	stat, err := os.Stdin.Stat()
	if err != nil || stat.Mode()&os.ModeCharDevice == 0 {
		return
	}
	fmt.Print("\n按 Enter 键关闭（10 秒后自动关闭）…")
	type done struct{}
	sel := make(chan done, 1)
	go func() {
		buf := make([]byte, 1)
		_, _ = os.Stdin.Read(buf)
		sel <- done{}
	}()
	select {
	case <-sel:
	case <-time.After(10 * time.Second):
	}
}
