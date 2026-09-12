package main

import (
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/memlimit"
	"github.com/stretchr/testify/require"
)

// swapMemlimitSeams pins the host probes to fixed values.
func swapMemlimitSeams(t *testing.T, physical, cgroup int64) {
	t.Helper()
	prevPhys, prevCgroup := memlimitPhysicalFn, memlimitCgroupFn
	memlimitPhysicalFn = func() int64 { return physical }
	memlimitCgroupFn = func() int64 { return cgroup }
	t.Cleanup(func() { memlimitPhysicalFn, memlimitCgroupFn = prevPhys, prevCgroup })
}

func TestApplyMemoryLimit_AutoFromPhysical(t *testing.T) {
	swapMemlimitSeams(t, 4<<30, 0)
	t.Setenv("GOMEMLIMIT", "")
	restore := swapSetMemoryLimit(t)

	cfg := &config.Config{}
	applyMemoryLimit(cfg)

	want := int64(1 << 30) // 4GiB host → min(45%, 1GiB) cap
	require.True(t, restore.called)
	require.True(t, restore.called)
	require.Equal(t, want, restore.applied)
	require.Equal(t, want, memlimit.Applied())
}

func TestApplyMemoryLimit_CgroupTighterWins(t *testing.T) {
	swapMemlimitSeams(t, 8<<30, 512<<20)
	t.Setenv("GOMEMLIMIT", "")
	restore := swapSetMemoryLimit(t)

	cfg := &config.Config{}
	applyMemoryLimit(cfg)

	want := int64(512<<20) * 8 / 10
	require.True(t, restore.called)
	require.Equal(t, want, restore.applied)
}

func TestApplyMemoryLimit_YAMLOverrideWins(t *testing.T) {
	swapMemlimitSeams(t, 8<<30, 0)
	t.Setenv("GOMEMLIMIT", "")
	restore := swapSetMemoryLimit(t)

	cfg := &config.Config{}
	cfg.Memory.SoftLimitBytes = 256 << 20
	applyMemoryLimit(cfg)

	require.True(t, restore.called)
	require.Equal(t, int64(256<<20), restore.applied)
}

func TestApplyMemoryLimit_EnvWinsOverEverything(t *testing.T) {
	swapMemlimitSeams(t, 8<<30, 0)
	t.Setenv("GOMEMLIMIT", "123MiB") // runtime applied it before main
	restore := swapSetMemoryLimit(t)

	cfg := &config.Config{}
	cfg.Memory.SoftLimitBytes = 256 << 20
	applyMemoryLimit(cfg)

	require.False(t, restore.called, "native env var must keep the runtime's own limit untouched")
	require.Zero(t, memlimit.Applied())
}

func TestApplyMemoryLimit_Disabled(t *testing.T) {
	swapMemlimitSeams(t, 8<<30, 0)
	t.Setenv("GOMEMLIMIT", "")
	restore := swapSetMemoryLimit(t)

	cfg := &config.Config{}
	cfg.Memory.DisableAutoLimit = true
	applyMemoryLimit(cfg)

	require.False(t, restore.called)
}

func TestApplyMemoryLimit_UnknownHostSkips(t *testing.T) {
	swapMemlimitSeams(t, 0, 0)
	t.Setenv("GOMEMLIMIT", "")
	restore := swapSetMemoryLimit(t)

	applyMemoryLimit(&config.Config{})
	require.False(t, restore.called)
}

// memlimitRecorder captures debug.SetMemoryLimit calls.
type memlimitRecorder struct {
	called  bool
	applied int64
}

func swapSetMemoryLimit(t *testing.T) *memlimitRecorder {
	t.Helper()
	rec := &memlimitRecorder{}
	prev := setMemoryLimitFn
	setMemoryLimitFn = func(limit int64) int64 {
		rec.called = true
		rec.applied = limit
		return 0
	}
	t.Cleanup(func() { setMemoryLimitFn = prev })
	return rec
}
