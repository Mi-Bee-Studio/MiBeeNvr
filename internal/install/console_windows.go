//go:build windows

package install

import (
	"os"
	"syscall"
)

// GUI-subsystem console plumbing. The windows release binary is built with
// `-H windowsgui`: Windows never allocates a console for it, so a desktop
// launch (double-click, Start Menu shortcut, Run-key autostart) cannot
// show — let alone leave — a cmd window, and there is nothing the user
// could close that would kill the server. Console ergonomics are rebuilt
// here instead:
//
//   - EnsureParentConsole: launched FROM a terminal → attach to the
//     parent's console and redirect the standard handles, so CLI
//     subcommands (and serve-for-debugging) print exactly as before.
//   - EnsureOwnedConsole: interactive flows with no terminal behind them
//     (Setup-exe double-click install, Add/Remove-Programs uninstall) →
//     allocate a fresh console so the summary stays readable.
//
// The installer's auto-started server child is spawned with
// MIBEE_DESKTOP_SPAWN=1 and therefore skips the attach: attaching it to
// the installer's console would keep that console alive — and closable,
// killing the server — for the server's whole lifetime, reintroducing the
// exact bug class this closes. (SW_HIDE arrived too early while the
// installer still held the shared console, so the hide check saw n>1 and
// never fired.)

const desktopSpawnEnv = "MIBEE_DESKTOP_SPAWN"

var (
	consoleDLL             = syscall.NewLazyDLL("kernel32.dll")
	procAttachConsole      = consoleDLL.NewProc("AttachConsole")
	procAllocConsole       = consoleDLL.NewProc("AllocConsole")
	procGetConsoleWindowPC = consoleDLL.NewProc("GetConsoleWindow")
	procSetConsoleOutputCP = consoleDLL.NewProc("SetConsoleOutputCP")
)

// attachParentProcess is ATTACH_PARENT_PROCESS ((DWORD)-1).
const attachParentProcess = ^uintptr(0)

func consolePresent() bool {
	hwnd, _, _ := procGetConsoleWindowPC.Call()
	return hwnd != 0
}

// EnsureParentConsole attaches to the launching terminal when one exists.
// Safe on every path; a no-op when a console is already present, so
// console-subsystem dev builds keep today's behavior.
func EnsureParentConsole() {
	if os.Getenv(desktopSpawnEnv) == "1" {
		return // installer-spawned server: stays console-less by design
	}
	if consolePresent() {
		return
	}
	if r, _, _ := procAttachConsole.Call(attachParentProcess); r == 0 {
		return // desktop launch, no parent console — intended
	}
	redirectStdHandles()
}

// EnsureOwnedConsole guarantees a visible console for interactive flows
// that print a summary and pause (install/uninstall). No-op when a console
// is already attached (terminal run).
func EnsureOwnedConsole() {
	if consolePresent() {
		return
	}
	if r, _, _ := procAllocConsole.Call(); r == 0 {
		return
	}
	_, _, _ = procSetConsoleOutputCP.Call(65001)
	redirectStdHandles()
}

// redirectStdHandles reopens invalid standard handles onto the console.
// A GUI-subsystem process launched without a terminal starts with invalid
// std handles — fmt/os writes silently vanish. Handles that ARE valid
// (terminals pass them to children, enabling `mibee-nvr … > file` and
// pipes) are left alone so redirection keeps working. os.Stdout/os.Stderr
// are the shared stdlib variables; fmt.Print* and slog pick the new
// targets up at call time.
func redirectStdHandles() {
	if !stdHandleValid(os.Stdout) {
		if f, err := os.OpenFile("CONOUT$", os.O_RDWR, 0); err == nil {
			os.Stdout = f
		}
	}
	if !stdHandleValid(os.Stderr) {
		if f, err := os.OpenFile("CONOUT$", os.O_RDWR, 0); err == nil {
			os.Stderr = f
		}
	}
	if !stdHandleValid(os.Stdin) {
		if f, err := os.OpenFile("CONIN$", os.O_RDWR, 0); err == nil {
			os.Stdin = f
		}
	}
}

// stdHandleValid reports whether the handle has a usable Stat (an inherited
// terminal/pipe/file handle does; a GUI launch's null handle does not).
func stdHandleValid(f *os.File) bool {
	if f == nil {
		return false
	}
	_, err := f.Stat()
	return err == nil
}
