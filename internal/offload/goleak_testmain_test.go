package offload

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package if any test leaves goroutines behind (#691
// defense-in-depth; offload is a lifecycle package — scan loop + workers must
// all return on Stop).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
