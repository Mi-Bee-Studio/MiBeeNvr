package fadvise

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSequentialAndDontNeed_IssueHintsAndSwallowErrors(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "src.mp4"))
	require.NoError(t, err)
	defer f.Close()
	_, werr := f.Write(make([]byte, 4096))
	require.NoError(t, werr)

	type call struct {
		fd     int
		advice int
	}
	var calls []call
	restore := SwapForTest(func(fd, advice int) error {
		calls = append(calls, call{fd, advice})
		return nil
	})
	defer restore()

	Sequential(f)
	DontNeed(f)
	require.Len(t, calls, 2)
	require.Equal(t, AdviceSequential, calls[0].advice)
	require.Equal(t, AdviceDontNeed, calls[1].advice)
	require.Equal(t, calls[0].fd, calls[1].fd, "hints target the same file")
}

func TestAdvise_SwallowsPlatformErrors(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "x.mp4"))
	require.NoError(t, err)
	defer f.Close()

	restore := SwapForTest(func(fd, advice int) error {
		return errors.New("ESRCH: hint not supported")
	})
	defer restore()
	require.NotPanics(t, func() { Sequential(f); DontNeed(f) })
}

func TestAdvise_NilFileIsNoop(t *testing.T) {
	restore := SwapForTest(func(fd, advice int) error {
		t.Error("nil file must not reach the platform call")
		return nil
	})
	defer restore()
	require.NotPanics(t, func() { Sequential(nil); DontNeed(nil) })
}

// On Linux the shared advice constants must match the kernel values —
// SwapForTest hides them from the wiring tests, so assert once here.
func TestAdviceConstantsMatchLinux(t *testing.T) {
	require.Equal(t, 2, AdviceSequential)
	require.Equal(t, 4, AdviceDontNeed)
}
