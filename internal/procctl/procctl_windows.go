//go:build windows

package procctl

import (
	"os/exec"

	"golang.org/x/sys/windows"
)

// Windows has no POSIX process groups; killing the direct child is the
// closest equivalent (ffmpeg does not spawn helper processes there).
func SetProcessGroup(*exec.Cmd) {}

func KillProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// LowerPriority maps the unix "nice 10" intent to BELOW_NORMAL priority
// class.
func LowerPriority(pid int) error {
	h, err := windows.OpenProcess(
		windows.PROCESS_SET_INFORMATION|windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false, uint32(pid))
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	return windows.SetPriorityClass(h, windows.BELOW_NORMAL_PRIORITY_CLASS)
}

// ProcessAlive reports whether a process with the given pid exists — an
// OpenProcess probe standing in for the unix signal-0 idiom (the exit code
// is not consulted; good enough for test helpers).
func ProcessAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	_ = windows.CloseHandle(h)
	return true
}
