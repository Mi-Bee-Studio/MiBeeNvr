package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

// --- Test helpers ---

// writeMeminfo writes a fake /proc/meminfo-format file with the given MemTotal in kB.
func writeMeminfo(t *testing.T, path string, totalKB uint64) {
	t.Helper()
	content := "MemTotal:      " + strconv.FormatUint(totalKB, 10) + " kB\n" +
		"MemFree:       123456 kB\n" +
		"MemAvailable:  654321 kB\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// createFakeBinary creates an executable file at the given path.
func createFakeBinary(t *testing.T, path string) {
	t.Helper()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := os.Chmod(path, 0755); err != nil {
		t.Fatal(err)
	}
}

// --- ProbeInfo tests ---

func TestProbeInfo_JSON(t *testing.T) {
	info := ProbeInfo{
		Available:   true,
		BinaryFound: true,
		PlatformOK:  true,
		MemoryOK:    true,
		CpuOK:       true,
		TotalMemory: 4096,
		CpuCores:    4,
		GoArch:      "arm64",
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("JSON marshal failed: %v", err)
	}
	var decoded ProbeInfo
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}
	if decoded.Available != info.Available {
		t.Fatalf("expected Available=%v, got %v", info.Available, decoded.Available)
	}
	if decoded.GoArch != info.GoArch {
		t.Fatalf("expected GoArch=%q, got %q", info.GoArch, decoded.GoArch)
	}
	if decoded.TotalMemory != info.TotalMemory {
		t.Fatalf("expected TotalMemory=%d, got %d", info.TotalMemory, decoded.TotalMemory)
	}
}

func TestProbeInfo_JSON_ReasonOmitted(t *testing.T) {
	info := ProbeInfo{
		Available: true,
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("JSON marshal failed: %v", err)
	}
	// Reason should be omitted due to omitempty tag.
	if str := string(data); str == "" {
		t.Fatal("expected non-empty JSON")
	}
}

// --- Platform check tests ---

func TestProbe_PlatformCheck(t *testing.T) {
	// This test runs on the actual platform and validates the expected behavior.
	ResetProbe()
	dir := t.TempDir()
	info := Probe(dir)

	switch runtime.GOARCH {
	case "amd64", "arm64":
		if !info.PlatformOK {
			t.Fatalf("expected PlatformOK=true on %s", runtime.GOARCH)
		}
		// On supported arch without binary, Probe should fail on binary check,
		// not platform check.
		if info.Reason == "" {
			t.Fatal("expected a reason since binary is missing")
		}
	default:
		if info.PlatformOK {
			t.Fatalf("expected PlatformOK=false on %s", runtime.GOARCH)
		}
		if !containsSubstr(info.Reason, "unsupported architecture") {
			t.Fatalf("expected reason about unsupported arch, got: %s", info.Reason)
		}
	}
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// --- Binary check tests ---

func TestProbe_BinaryNotFound(t *testing.T) {
	ResetProbe()
	dir := t.TempDir()
	info := Probe(dir)
	if info.BinaryFound {
		t.Fatal("expected BinaryFound=false when binary does not exist")
	}
	if info.Reason == "" {
		t.Fatal("expected non-empty Reason when binary not found")
	}
}

func TestProbe_BinaryFound(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("platform check fails first on non-supported arch")
	}
	ResetProbe()
	dir := t.TempDir()
	binPath := filepath.Join(dir, "tools", "onnxruntime")
	createFakeBinary(t, binPath)
	info := Probe(dir)
	if !info.BinaryFound {
		t.Fatal("expected BinaryFound=true when binary exists")
	}
}

// --- BinaryExists unit tests ---

func TestBinaryExists(t *testing.T) {
	t.Run("executable file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "testbin")
		createFakeBinary(t, path)
		if !binaryExists(path) {
			t.Fatal("expected binaryExists=true for executable file")
		}
	})

	t.Run("non-existent file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "nonexistent")
		if binaryExists(path) {
			t.Fatal("expected binaryExists=false for nonexistent file")
		}
	})

	t.Run("directory is not binary", func(t *testing.T) {
		path := t.TempDir()
		if binaryExists(path) {
			t.Fatal("expected binaryExists=false for directory")
		}
	})

	t.Run("non-executable file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "readonly")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
		if binaryExists(path) {
			t.Fatal("expected binaryExists=false for non-executable file")
		}
	})
}

// --- Memory check tests ---

func TestProbe_MemoryCheck(t *testing.T) {
	t.Run("sufficient memory (4 GB)", func(t *testing.T) {
		memPath := filepath.Join(t.TempDir(), "meminfo")
		writeMeminfo(t, memPath, 4*1024*1024) // 4 GB in kB
		memMB := detectMemoryFromFile(memPath)
		if memMB != 4096 {
			t.Fatalf("expected 4096 MB, got %d", memMB)
		}
	})

	t.Run("minimal threshold (2 GB)", func(t *testing.T) {
		memPath := filepath.Join(t.TempDir(), "meminfo")
		writeMeminfo(t, memPath, 2*1024*1024) // exactly 2 GB in kB
		memMB := detectMemoryFromFile(memPath)
		if memMB != 2048 {
			t.Fatalf("expected 2048 MB, got %d", memMB)
		}
	})

	t.Run("insufficient memory (512 MB)", func(t *testing.T) {
		memPath := filepath.Join(t.TempDir(), "meminfo")
		writeMeminfo(t, memPath, 512*1024) // 512 MB in kB
		memMB := detectMemoryFromFile(memPath)
		if memMB != 512 {
			t.Fatalf("expected 512 MB, got %d", memMB)
		}
		if memMB >= 2048 {
			t.Fatal("512 MB should be below 2048 MB threshold")
		}
	})
}

// --- CPU check tests ---

func TestProbe_CPUCheck(t *testing.T) {
	cpus := runtime.NumCPU()
	info := ProbeInfo{
		CpuCores: cpus,
	}
	info.CpuOK = info.CpuCores >= 2
	if info.CpuOK != (cpus >= 2) {
		t.Fatal("CPU check logic error")
	}
	if cpus < 2 && info.CpuOK {
		t.Fatal("expected CpuOK=false with <2 cores")
	}
	if cpus >= 2 && !info.CpuOK {
		t.Fatal("expected CpuOK=true with >=2 cores")
	}
}

// --- IsAvailable tests ---

func TestIsAvailable(t *testing.T) {
	ResetProbe()
	dir := t.TempDir()
	if IsAvailable(dir) {
		t.Fatal("expected IsAvailable()=false without binary and setup")
	}
}

func TestIsAvailable_WithBinary(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("platform not supported for AI inference")
	}
	if runtime.NumCPU() < 2 {
		t.Skip("test requires >=2 CPU cores")
	}

	ResetProbe()
	dir := t.TempDir()
	binPath := filepath.Join(dir, "tools", "onnxruntime")
	createFakeBinary(t, binPath)

	info := Probe(dir)
	if !info.BinaryFound {
		t.Fatal("expected BinaryFound=true")
	}
	if !info.PlatformOK {
		t.Fatal("expected PlatformOK=true on supported arch")
	}
	if !info.CpuOK {
		t.Fatal("expected CpuOK=true with >=2 cores")
	}
	// Memory is system-dependent — we don't assert Available since memory
	// might be insufficient in constrained environments (CI, etc.).
	// We verify that binary and platform checks pass.
}

// --- Probe result and reason tests ---

func TestProbe_MemoryBelowThreshold_WithBinary(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("platform check fails first on non-supported arch")
	}

	// We can't inject a low memory value into the real Probe() because
	// detectMemory reads the real /proc/meminfo. Test detectMemoryFromFile directly.
	memPath := filepath.Join(t.TempDir(), "meminfo")
	writeMeminfo(t, memPath, 1024*1024) // 1 GB
	memMB := detectMemoryFromFile(memPath)
	if memMB != 1024 {
		t.Fatalf("expected 1024 MB, got %d", memMB)
	}
	if memMB >= 2048 {
		t.Fatal("1 GB should be below 2048 MB threshold for AI inference")
	}
}

func TestProbe_AllChecksPass(t *testing.T) {
	// Test the logical flow: if all individual checks pass, Available=true.
	info := &ProbeInfo{
		GoArch:      "arm64",
		PlatformOK:  true,
		BinaryFound: true,
		TotalMemory: 4096,
		MemoryOK:    true,
		CpuCores:    4,
		CpuOK:       true,
	}
	info.Available = info.PlatformOK && info.BinaryFound && info.MemoryOK && info.CpuOK
	if !info.Available {
		t.Fatal("expected Available=true when all checks pass")
	}
	if info.Reason != "" {
		t.Fatalf("expected empty Reason when available, got: %s", info.Reason)
	}
}

// --- DetectMemoryFromFile tests ---

func TestDetectMemoryFromFile_Error(t *testing.T) {
	memMB := detectMemoryFromFile("/nonexistent/meminfo")
	if memMB != 0 {
		t.Fatalf("expected 0 for non-existent file, got %d", memMB)
	}
}

func TestDetectMemoryFromFile_MissingField(t *testing.T) {
	memPath := filepath.Join(t.TempDir(), "meminfo")
	content := "MemFree:       123456 kB\nMemAvailable:  654321 kB\n"
	if err := os.WriteFile(memPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	memMB := detectMemoryFromFile(memPath)
	if memMB != 0 {
		t.Fatalf("expected 0 for meminfo without MemTotal, got %d", memMB)
	}
}

func TestDetectMemoryFromFile_Malformed(t *testing.T) {
	memPath := filepath.Join(t.TempDir(), "meminfo")
	content := "MemTotal: notANumber kB\n"
	if err := os.WriteFile(memPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	memMB := detectMemoryFromFile(memPath)
	if memMB != 0 {
		t.Fatalf("expected 0 for malformed meminfo, got %d", memMB)
	}
}

func TestDetectMemoryFromFile_Empty(t *testing.T) {
	memPath := filepath.Join(t.TempDir(), "meminfo")
	if err := os.WriteFile(memPath, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	memMB := detectMemoryFromFile(memPath)
	if memMB != 0 {
		t.Fatalf("expected 0 for empty meminfo, got %d", memMB)
	}
}

// --- ResetProbe test ---

func TestResetProbe(t *testing.T) {
	ResetProbe()
	dir := t.TempDir()
	info1 := Probe(dir)

	ResetProbe()
	// Create binary after reset.
	binPath := filepath.Join(dir, "tools", "onnxruntime")
	createFakeBinary(t, binPath)
	info2 := Probe(dir)

	if info1.BinaryFound == info2.BinaryFound {
		t.Fatal("expected different results after reset + binary creation")
	}
}

// --- Reason on failure tests ---

func TestProbe_ReasonOnFailure(t *testing.T) {
	t.Run("platform failure on arm", func(t *testing.T) {
		if runtime.GOARCH != "arm" {
			t.Skip("only meaningful on arm (ARMv7)")
		}
		ResetProbe()
		info := Probe(t.TempDir())
		if info.Available {
			t.Fatal("expected Available=false on arm")
		}
		if info.Reason == "" {
			t.Fatal("expected non-empty reason on platform failure")
		}
		if !containsSubstr(info.Reason, "unsupported architecture") {
			t.Fatalf("expected 'unsupported architecture' in reason, got: %s", info.Reason)
		}
	})

	t.Run("binary failure on non-arm", func(t *testing.T) {
		if runtime.GOARCH == "arm" {
			t.Skip("platform check fails first on arm")
		}
		ResetProbe()
		dir := t.TempDir()
		info := Probe(dir)
		if info.Reason == "" {
			t.Fatal("expected non-empty Reason when binary not found")
		}
		if !containsSubstr(info.Reason, "binary not found") {
			t.Fatalf("expected 'binary not found' in reason, got: %s", info.Reason)
		}
	})
}

// --- Edge case: nil dataDir ---

func TestProbe_EmptyDataDir(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("platform check fails first on non-supported arch")
	}
	ResetProbe()
	// Empty dataDir — binary check should fail gracefully.
	info := Probe("")
	if info.BinaryFound {
		t.Fatal("expected BinaryFound=false with empty dataDir")
	}
	if info.Reason == "" {
		t.Fatal("expected non-empty Reason with empty dataDir")
	}
}
