package p2p

import (
	"os"
	"strings"
	"testing"
)

// TestGoModHasNoPrivateP2PModule 钉死 SDK 依赖形态（#15）：SDK 一律走仓内
// internal/p2psdk 快照，go.mod 不得出现私有模块路径——否则公开 CI 会因
// 无法解析而构建失败，修复会被锁回只能走内网的分支。
func TestGoModHasNoPrivateP2PModule(t *testing.T) {
	raw, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	for _, ln := range strings.Split(string(raw), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "//") {
			continue
		}
		if strings.Contains(ln, "MiBeeP2PServer") {
			t.Errorf("go.mod references private P2P module (%q) — SDK changes must be diff-copied into internal/p2psdk", ln)
		}
	}
}

// TestManagerImportsVendoredSDK 确保 internal/p2p 引用的是仓内快照路径。
func TestManagerImportsVendoredSDK(t *testing.T) {
	raw, err := os.ReadFile("manager.go")
	if err != nil {
		t.Fatalf("read manager.go: %v", err)
	}
	if strings.Contains(string(raw), "MiBeeP2PServer/sdk/go") {
		t.Error("manager.go imports the private SDK module path — use internal/p2psdk")
	}
	if !strings.Contains(string(raw), "internal/p2psdk") {
		t.Error("manager.go does not reference internal/p2psdk")
	}
}
