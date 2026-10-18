package prealloc

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// The platform implementations differ (fallocate on Linux, unsupported
// elsewhere), so these tests pin the seam contract only: Preallocate
// delegates to preallocFn, SwapForTest swaps and restores atomically.
func TestPreallocateDelegatesThroughSeam(t *testing.T) {
	var gotSize int64
	restore := SwapForTest(func(_ *os.File, size int64) error {
		gotSize = size
		return nil
	})
	defer restore()

	f, err := os.CreateTemp(t.TempDir(), "prealloc-*")
	require.NoError(t, err)
	defer f.Close()

	require.NoError(t, Preallocate(f, 1<<20))
	require.Equal(t, int64(1<<20), gotSize, "size must pass through the seam unchanged")
}

func TestSwapForTestRestoresPrevious(t *testing.T) {
	sentinel := errors.New("sentinel")
	restore := SwapForTest(func(_ *os.File, _ int64) error { return sentinel })
	restore() // immediately restore — preallocFn must be back to the platform impl

	f, err := os.CreateTemp(t.TempDir(), "prealloc-*")
	require.NoError(t, err)
	defer f.Close()

	err = Preallocate(f, 4096)
	if err == nil {
		return // Linux: real fallocate succeeded
	}
	require.NotErrorIs(t, err, sentinel, "after restore the swapped stub must no longer be active")
}
