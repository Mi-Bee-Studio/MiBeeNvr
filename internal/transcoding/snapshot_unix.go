//go:build !windows

package transcoding

import (
	"os/exec"
	"syscall"
)

// prepareSnapshotProcessGroup puts the ffmpeg child into its own process
// group so a cleanup kill reaches ffmpeg's own children too (shell wrapper
// fixtures spawn pipelines — killing only the shell leaves grandchildren
// holding the stdio pipes, deadlocking Wait).
func prepareSnapshotProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killSnapshotProcessGroup kills the whole child process group, then the
// leader as a belt-and-braces fallback.
func killSnapshotProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}
