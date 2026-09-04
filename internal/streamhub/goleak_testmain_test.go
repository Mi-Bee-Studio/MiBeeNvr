package streamhub

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package if any test leaves goroutines behind (#691
// defense-in-depth: goleak catches leaks on EVERY test, not just dedicated
// guard tests). If a goroutine is legitimately long-lived for the whole
// binary, add a targeted goleak.IgnoreTopFunction here with a reason.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
