package staticguard

// Static guard (#691 defense-in-depth): every non-test function that spawns
// a child process via exec.Command/exec.CommandContext must, within the same
// function, call one of the wait-family methods (Wait/Run/Output/CombinedOutput).
// exec.CommandContext's watchCtx goroutine only retires via cmd.Wait(); a new
// call site that forgets it leaks one goroutine per invocation (the M5 field
// leak was exactly this). Cross-function lifecycles (Start in one method,
// Wait in another) go through waitFamilyAllowlist with a reviewed reason —
// adding an entry forces a conscious decision in code review.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// waitFamilyAllowlist permits exec sites whose Wait lives in another
// function (session-style lifecycles). Key: "path/from/repo/root.go:FuncName".
var waitFamilyAllowlist = map[string]string{
	"internal/livetranscode/transcoder.go:Start": "session lifecycle: Start() spawns ffmpeg, Stop() calls cmd.Wait() (transcoder.go)",
}

func scanForUnwaitedExec(src []byte, filename string) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		return []string{filename + ": parse error: " + err.Error()}
	}

	var violations []string
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		hasExec, hasWait := false, false
		ast.Inspect(fn.Body, func(m ast.Node) bool {
			call, ok := m.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "exec" &&
				(sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext") {
				hasExec = true
			}
			switch sel.Sel.Name {
			case "Wait":
				// Only cmd.Wait() retires watchCtx. cmd.Process.Wait() is a
				// two-level selector (X.Y.Wait) and does NOT — the exact #691 trap.
				if _, single := sel.X.(*ast.Ident); single {
					hasWait = true
				}
			case "Run", "Output", "CombinedOutput":
				hasWait = true
			}
			return true
		})
		if hasExec && !hasWait {
			pos := fset.Position(fn.Pos())
			key := filename + ":" + fn.Name.Name
			if _, allowed := waitFamilyAllowlist[key]; !allowed {
				violations = append(violations,
					pos.String()+" ("+fn.Name.Name+") spawns exec.Command* without Wait/Run/Output/CombinedOutput in the same function")
			}
		}
		return true
	})
	return violations
}

func TestScanner_FlagsUnwaitedExecFixture(t *testing.T) {
	bad := []byte(`package x
import ("os/exec"; "context")
func badSpawn(ctx context.Context) {
	cmd := exec.CommandContext(ctx, "ffmpeg")
	_ = cmd.Start()
	_ = cmd.Process.Wait()
}`)
	if v := scanForUnwaitedExec(bad, "fixtures/bad.go"); len(v) != 1 {
		t.Fatalf("scanner must flag the os-level-reap-only pattern, got %v", v)
	}

	good := []byte(`package x
import ("os/exec"; "context")
func goodSpawn(ctx context.Context) {
	cmd := exec.CommandContext(ctx, "ffmpeg")
	_ = cmd.Start()
	defer func() { _ = cmd.Wait() }()
}`)
	if v := scanForUnwaitedExec(good, "fixtures/good.go"); len(v) != 0 {
		t.Fatalf("scanner must accept cmd.Wait cleanup, got %v", v)
	}

	runner := []byte(`package x
import "os/exec"
func runIt() { _ = exec.Command("true").Run() }`)
	if v := scanForUnwaitedExec(runner, "fixtures/run.go"); len(v) != 0 {
		t.Fatalf("scanner must accept Run(), got %v", v)
	}
}

func TestRepo_ExecSitesAreWaited(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))

	var violations []string
	for _, dir := range []string{"internal", "pkg", "cmd"} {
		root := filepath.Join(repoRoot, dir)
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			violations = append(violations, scanForUnwaitedExec(src, rel)...)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}

	for _, v := range violations {
		t.Errorf("unwaited exec site: %s", v)
	}
	if len(violations) == 0 {
		t.Log("all exec.Command* sites call Wait/Run/Output/CombinedOutput (or are reviewed allowlist entries)")
	}
}
