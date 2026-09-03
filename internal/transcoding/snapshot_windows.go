//go:build windows

package transcoding

import (
	"os/exec"
)

// Windows has no process groups in the POSIX sense; Kill terminates the
// direct child, which is all the snapshot decoder spawns there.
func prepareSnapshotProcessGroup(*exec.Cmd) {}

func killSnapshotProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
