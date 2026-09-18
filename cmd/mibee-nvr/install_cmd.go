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
		// A freshly started instance needs a beat to bind; an already-running
		// one answers immediately.
		if !res.Started {
			time.Sleep(1500 * time.Millisecond)
		}
		install.OpenBrowser(webUIURL(res.Config))
		install.NotifyDialog("MiBee NVR 已安装并启动。\n任务栏托盘/菜单栏出现管理图标，浏览器即将打开管理界面。")
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
	install.SetupConsole()
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

// pauseIfInteractive keeps the console open when launched from Explorer
// (Add/Remove Programs runs the uninstaller with no terminal to read the
// summary from otherwise). Terminal/piped invocations flow through.
func pauseIfInteractive() {
	stat, _ := os.Stdin.Stat()
	if stat.Mode()&os.ModeCharDevice == 0 {
		return
	}
	fmt.Print("\n按 Enter 键关闭…")
	buf := make([]byte, 1)
	_, _ = os.Stdin.Read(buf)
}
