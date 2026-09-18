//go:build windows

package procctl

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestSetProcessGroupNoop(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "exit", "0")
	SetProcessGroup(cmd)
	if cmd.SysProcAttr != nil {
		t.Fatalf("SysProcAttr must stay nil on windows, got %+v", cmd.SysProcAttr)
	}
}

func TestKillProcessGroupTerminatesChild(t *testing.T) {
	cmd := exec.Command("ping", "-n", "30", "127.0.0.1")
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
	case <-time.After(10 * time.Second):
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
	if err := LowerPriority(os.Getpid()); err != nil {
		t.Fatalf("lower own priority: %v", err)
	}
}

func TestProcessAlive(t *testing.T) {
	cmd := exec.Command("ping", "-n", "30", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !ProcessAlive(cmd.Process.Pid) {
		t.Fatalf("child %d should be alive", cmd.Process.Pid)
	}
	_ = KillProcessGroup(cmd)
	_ = cmd.Wait()
	deadline := time.Now().Add(10 * time.Second)
	for ProcessAlive(cmd.Process.Pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if ProcessAlive(cmd.Process.Pid) {
		t.Fatalf("child %d should be gone after kill+wait", cmd.Process.Pid)
	}
}
