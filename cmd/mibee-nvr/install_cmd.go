package main

import (
	"fmt"
	"os"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/install"
)

// cmdInstall / cmdUninstall — desktop (windows/darwin) per-user install
// guidance: mibee-nvr install | mibee-nvr uninstall [--purge].
func cmdInstall() {
	install.SetupConsole()
	res, err := install.Install(install.Options{Version: appVersion})
	if err != nil {
		fmt.Fprintf(os.Stderr, "安装失败: %v\n", err)
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
	pauseIfInteractive()
	os.Exit(0)
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
