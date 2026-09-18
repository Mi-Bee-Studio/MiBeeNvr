package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/install"
)

// Desktop single-file installer entry (user request 2026-09-18): on windows
// and darwin a BARE run of the binary — what a double-click of the
// downloaded Setup exe / DMG app does — performs the install instead of
// trying to run a server out of the Downloads folder. Any argv (flags like
// -config, or subcommands like install/uninstall) and every non-desktop
// platform keep the classic behavior, so servers and the fnOS/Docker
// packaging are untouched.
//
// The one carve-out: running the ALREADY-INSTALLED exe bare starts the
// server with the installed config (the shortcut/autostart entries pass
// -config explicitly and never hit this path; without the carve-out a bare
// double-click of the installed exe would auto-init a stray config in its
// own directory).

// maybeInteractiveInstall routes a bare desktop double-click into the
// installer. Returns without doing anything when the classic server path
// should run. The actual install goes through cmdInstallDesktopFn so tests
// can stub it — a unit test must never install for real.
func maybeInteractiveInstall(args []string) {
	if !wantsDesktopInstall(runtime.GOOS, len(args), selfExePath(), install.InstalledExePath) {
		return
	}
	cmdInstallDesktopFn()
}

// wantsDesktopInstall is the testable predicate behind the desktop entry:
// bare argv on windows/darwin and the running binary NOT the installed one
// (the installed one serves instead). The installed-location lookup is
// injected so tests stay hermetic.
func wantsDesktopInstall(goos string, argsLen int, selfExe string, installedExe func() string) bool {
	if argsLen != 1 {
		return false
	}
	if goos != "windows" && goos != "darwin" {
		return false
	}
	if selfExe == "" {
		return false
	}
	installed := installedExe()
	if installed == "" {
		return false
	}
	return !sameExePath(selfExe, installed)
}

// desktopInstalledConfig returns the installed config path when the process
// is the installed desktop binary run bare — main uses it to resolve the
// default -config. Empty when not applicable.
func desktopInstalledConfig() string {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		return ""
	}
	self := selfExePath()
	if self == "" || !sameExePath(self, install.InstalledExePath()) {
		return ""
	}
	_, _, cfgPath, err := install.Paths()
	if err != nil {
		return ""
	}
	return cfgPath
}

func selfExePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	exe, _ = filepath.EvalSymlinks(exe)
	return exe
}

// sameExePath compares two exe paths (windows paths are case-insensitive).
func sameExePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}
