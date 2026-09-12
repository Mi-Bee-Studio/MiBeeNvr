package memlimit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestComputeLimit_OverrideWins(t *testing.T) {
	if got := ComputeLimit(4<<30, 2<<30, 512<<20); got != 512<<20 {
		t.Errorf("override: got %d, want 512MiB", got)
	}
}

func TestComputeLimit_PhysicalTiers(t *testing.T) {
	cases := []struct {
		name     string
		physical int64
		want     int64
	}{
		{"512MiB board → 45% (below the 1GiB cap)", 512 << 20, int64(512<<20) * 45 / 100},
		{"1GiB board → 45%", 1 << 30, int64(1<<30) * 45 / 100},
		{"4GiB board → capped at 1GiB", 4 << 30, 1 << 30},
		{"16GiB server → capped at 1GiB", 16 << 30, 1 << 30},
	}
	for _, tc := range cases {
		if got := ComputeLimit(tc.physical, 0, 0); got != tc.want {
			t.Errorf("%s: physical=%d got %d, want %d", tc.name, tc.physical, got, tc.want)
		}
	}
}

func TestComputeLimit_PhysicalUnknownSkips(t *testing.T) {
	if got := ComputeLimit(0, 0, 0); got != 0 {
		t.Errorf("physical+cgroup unknown: got %d, want 0 (leave Go default)", got)
	}
}

func TestComputeLimit_CgroupEightyPercent(t *testing.T) {
	want80 := int64(512<<20) * 8 / 10 // 429496729 ≈ 409.6MiB
	if got := ComputeLimit(8<<30, 512<<20, 0); got != want80 {
		t.Errorf("cgroup 512MiB on 8GiB host: got %d, want 80%% = %d", got, want80)
	}
	// Cgroup larger than physical (misconfigured host): physical tier wins.
	want45 := int64(1<<30) * 45 / 100 // 483183820 = 460.8MiB
	if got := ComputeLimit(1<<30, 8<<30, 0); got != want45 {
		t.Errorf("cgroup 8GiB on 1GiB host: got %d, want physical 45%% = %d", got, want45)
	}
}

func TestComputeLimit_SmallCgroupFloor(t *testing.T) {
	// A 128MiB cgroup on any host must not round to zero — Go needs a floor.
	if got := ComputeLimit(4<<30, 128<<20, 0); got < minLimitBytes {
		t.Errorf("tiny cgroup: got %d, want >= floor %d", got, minLimitBytes)
	}
}

func TestReadCgroupLimitV2(t *testing.T) {
	dir := t.TempDir()
	// cgroup v2: memory.max holds "max" (unlimited) or a byte count.
	if err := os.WriteFile(filepath.Join(dir, "memory.max"), []byte("536870912\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readCgroupV2(filepath.Join(dir, "memory.max")); got != 512<<20 {
		t.Errorf("v2 numeric: got %d, want 512MiB", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.max"), []byte("max\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readCgroupV2(filepath.Join(dir, "memory.max")); got != 0 {
		t.Errorf("v2 max: got %d, want 0 (unlimited)", got)
	}
	if got := readCgroupV2(filepath.Join(dir, "missing")); got != 0 {
		t.Errorf("v2 missing: got %d, want 0", got)
	}
}

func TestReadCgroupLimitV1(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.limit_in_bytes")
	if err := os.WriteFile(path, []byte("536870912\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readCgroupV1(path); got != 512<<20 {
		t.Errorf("v1 numeric: got %d, want 512MiB", got)
	}
	// v1 "unlimited" sentinel: an absurdly huge page-count-derived value.
	if err := os.WriteFile(path, []byte("9223372036854771712\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readCgroupV1(path); got != 0 {
		t.Errorf("v1 unlimited sentinel: got %d, want 0", got)
	}
}

func TestCgroupLimitPrefersV2(t *testing.T) {
	dir := t.TempDir()
	prevV2, prevV1 := cgroupV2Path, cgroupV1Path
	cgroupV2Path = filepath.Join(dir, "memory.max")
	cgroupV1Path = filepath.Join(dir, "memory", "memory.limit_in_bytes")
	t.Cleanup(func() { cgroupV2Path, cgroupV1Path = prevV2, prevV1 })

	os.WriteFile(cgroupV2Path, []byte("1048576\n"), 0o644) // 1MiB
	os.MkdirAll(filepath.Dir(cgroupV1Path), 0o755)
	os.WriteFile(cgroupV1Path, []byte("2097152\n"), 0o644) // 2MiB

	if got := CgroupLimit(); got != 1<<20 {
		t.Errorf("CgroupLimit: got %d, want v2 value 1MiB", got)
	}

	// v2 absent → v1 fallback.
	os.Remove(cgroupV2Path)
	if got := CgroupLimit(); got != 2<<20 {
		t.Errorf("CgroupLimit v1 fallback: got %d, want 2MiB", got)
	}
}

func TestPhysicalMemParsesMeminfo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meminfo")
	if err := os.WriteFile(path, []byte("MemTotal:       3992 kB\nMemFree: 100 kB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := meminfoPath
	meminfoPath = path
	t.Cleanup(func() { meminfoPath = prev })

	// 3992 kB = 4087808 bytes.
	if got := Physical(); got != 3992*1024 {
		t.Errorf("Physical: got %d, want %d", got, 3992*1024)
	}
}

func TestPhysicalMemMissingFile(t *testing.T) {
	prev := meminfoPath
	meminfoPath = "/nonexistent/meminfo"
	t.Cleanup(func() { meminfoPath = prev })
	if got := Physical(); got != 0 {
		t.Errorf("Physical missing file: got %d, want 0", got)
	}
}
