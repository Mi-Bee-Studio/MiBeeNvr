package p2p

import (
	"os"
	"strings"
	"testing"
)

// TestGoModHasNoPrivateP2PModule 钉死 SDK 依赖形态（#15）：SDK 一律走仓内
// internal/p2psdk 快照，go.mod 不得出现外部 P2P SDK 模块路径——否则公开
// CI 会因无法解析私有模块而构建失败，修复会被锁回只能走内网的分支。
// 守卫按模块路径形状匹配（"/sdk/go" 子模块），不点名具体私有仓。
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
		if strings.Contains(ln, "/sdk/go") {
			t.Errorf("go.mod references an external P2P SDK module (%q) — SDK changes must be diff-copied into internal/p2psdk", ln)
		}
	}
}

// TestManagerImportsVendoredSDK 确保 internal/p2p 引用的是仓内快照路径。
func TestManagerImportsVendoredSDK(t *testing.T) {
	raw, err := os.ReadFile("manager.go")
	if err != nil {
		t.Fatalf("read manager.go: %v", err)
	}
	if strings.Contains(string(raw), "sdk/go") {
		t.Error("manager.go references an external SDK module path — use internal/p2psdk")
	}
	if !strings.Contains(string(raw), "internal/p2psdk") {
		t.Error("manager.go does not reference internal/p2psdk")
	}
}
