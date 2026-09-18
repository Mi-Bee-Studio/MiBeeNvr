//go:build !windows

package procctl

import (
	"os/exec"
	"syscall"
)

// SetProcessGroup puts the child into its own process group so a cleanup
// kill reaches the child's own children too (shell wrapper fixtures spawn
// pipelines — killing only the shell leaves grandchildren holding the stdio
// pipes, deadlocking Wait).
func SetProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// KillProcessGroup sends SIGKILL to the entire child process group to ensure
// the helper and any child processes it spawned are terminated.
func KillProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// LowerPriority drops the child to nice 10 so software transcoding never
// starves the recording pipeline.
func LowerPriority(pid int) error {
	return syscall.Setpriority(syscall.PRIO_PROCESS, pid, 10)
}

// ProcessAlive reports whether a process with the given pid exists
// (signal-0 probe — does not disturb the target).
func ProcessAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
