//go:build !windows

package procctl

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestSetProcessGroupOwnsGroup(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("no sleep binary on PATH")
	}
	cmd := exec.Command("sleep", "1")
	SetProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer cmd.Wait()
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("getpgid: %v", err)
	}
	if pgid != cmd.Process.Pid {
		t.Fatalf("child pgid %d != pid %d — not in its own process group", pgid, cmd.Process.Pid)
	}
}

func TestKillProcessGroupTerminatesChild(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("no sleep binary on PATH")
	}
	cmd := exec.Command("sleep", "30")
	SetProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := KillProcessGroup(cmd); err != nil {
		t.Fatalf("kill: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("child did not exit after KillProcessGroup")
	}
}

func TestKillProcessGroupNilSafe(t *testing.T) {
	if err := KillProcessGroup(nil); err != nil {
		t.Fatalf("nil cmd: %v", err)
	}
	if err := KillProcessGroup(&exec.Cmd{}); err != nil {
		t.Fatalf("nil process: %v", err)
	}
}

func TestLowerPriority(t *testing.T) {
	t.Cleanup(func() { _ = syscall.Setpriority(syscall.PRIO_PROCESS, 0, 0) })
	if err := LowerPriority(os.Getpid()); err != nil {
		t.Fatalf("lower own priority: %v", err)
	}
}

func TestProcessAlive(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("no sleep binary on PATH")
	}
	cmd := exec.Command("sleep", "1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !ProcessAlive(cmd.Process.Pid) {
		t.Fatalf("child %d should be alive", cmd.Process.Pid)
	}
	_ = cmd.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for ProcessAlive(cmd.Process.Pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if ProcessAlive(cmd.Process.Pid) {
		t.Fatalf("child %d should be gone after Wait", cmd.Process.Pid)
	}
}
