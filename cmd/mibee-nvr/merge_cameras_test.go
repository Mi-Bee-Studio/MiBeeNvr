package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestMergeDiskDirectories_Manifest(t *testing.T) {
	t.Helper()
	// Create temp directories for source and target
	srcDir, err := os.MkdirTemp("", "merge-src-*")
	if err != nil {
		t.Fatalf("failed to create src temp dir: %v", err)
	}
	defer os.RemoveAll(srcDir)

	dstDir, err := os.MkdirTemp("", "merge-dst-*")
	if err != nil {
		t.Fatalf("failed to create dst temp dir: %v", err)
	}
	defer os.RemoveAll(dstDir)

	// Create test files in source directory with sourceID prefix
	sourceID := "cam-source"
	targetID := "cam-target"

	// Create subdirectories to test structure preservation
	subDir := filepath.Join(srcDir, "2024", "01")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}

	// Create test files with sourceID prefix
	testFiles := []string{
		filepath.Join(srcDir, "cam-source_20240101_100000_abc123.mp4"),
		filepath.Join(srcDir, "cam-source_20240101_110000_def456.mp4"),
		filepath.Join(subDir, "cam-source_20240101_120000_ghi789.mp4"),
	}

	for _, f := range testFiles {
		if err := os.WriteFile(f, []byte("test content"), 0o644); err != nil {
			t.Fatalf("failed to create test file %s: %v", f, err)
		}
	}

	ctx := context.Background()

	// Test forward move with rename
	movedCount, manifest, err := mergeDiskDirectories(ctx, srcDir, dstDir, "", sourceID, targetID)
	if err != nil {
		t.Fatalf("mergeDiskDirectories failed: %v", err)
	}

	if movedCount != 3 {
		t.Errorf("expected 3 files moved, got %d", movedCount)
	}

	if len(manifest) != 3 {
		t.Fatalf("expected 3 manifest entries, got %d", len(manifest))
	}

	// Verify manifest contains targetID-prefixed paths
	for _, dstPath := range manifest {
		if !contains(dstPath, targetID) {
			t.Errorf("manifest entry %q should contain targetID %q", dstPath, targetID)
		}
		// Verify file exists at destination
		if _, err := os.Stat(dstPath); os.IsNotExist(err) {
			t.Errorf("manifest file %q does not exist at destination", dstPath)
		}
	}

	// Verify source files no longer exist
	for _, f := range testFiles {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("source file %q should not exist after move", f)
		}
	}

	// Verify target files exist with targetID prefix
	expectedTargetFiles := []string{
		filepath.Join(dstDir, "cam-target_20240101_100000_abc123.mp4"),
		filepath.Join(dstDir, "cam-target_20240101_110000_def456.mp4"),
		filepath.Join(dstDir, "2024", "01", "cam-target_20240101_120000_ghi789.mp4"),
	}

	for _, f := range expectedTargetFiles {
		if _, err := os.Stat(f); os.IsNotExist(err) {
			t.Errorf("expected target file %q does not exist", f)
		}
	}

	// Test rollback using manifest
	movedBack := 0
	for _, dstPath := range manifest {
		rel, err := filepath.Rel(dstDir, dstPath)
		if err != nil {
			t.Errorf("failed to compute relative path for %q: %v", dstPath, err)
			continue
		}
		// Reverse the rename: targetID → sourceID
		rel = replaceAll(rel, targetID, sourceID)
		srcPath := filepath.Join(srcDir, rel)
		if err := os.MkdirAll(filepath.Dir(srcPath), 0o755); err != nil {
			t.Errorf("failed to create parent dir for %q: %v", srcPath, err)
			continue
		}
		if err := os.Rename(dstPath, srcPath); err != nil {
			t.Errorf("failed to move back %q → %q: %v", dstPath, srcPath, err)
			continue
		}
		movedBack++
	}

	if movedBack != 3 {
		t.Errorf("expected 3 files moved back, got %d", movedBack)
	}

	// Verify files are back in source with original names
	for _, f := range testFiles {
		if _, err := os.Stat(f); os.IsNotExist(err) {
			t.Errorf("source file %q should exist after rollback", f)
		}
	}

	// Verify target files no longer exist
	for _, f := range expectedTargetFiles {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("target file %q should not exist after rollback", f)
		}
	}
}

func TestMergeDiskDirectories_EmptySource(t *testing.T) {
	t.Helper()
	// Test with non-existent source directory
	srcDir := "/nonexistent/path"
	dstDir, err := os.MkdirTemp("", "merge-dst-*")
	if err != nil {
		t.Fatalf("failed to create dst temp dir: %v", err)
	}
	defer os.RemoveAll(dstDir)

	ctx := context.Background()

	movedCount, manifest, err := mergeDiskDirectories(ctx, srcDir, dstDir, "", "src", "dst")
	if err != nil {
		t.Fatalf("mergeDiskDirectories should not error on non-existent source: %v", err)
	}

	if movedCount != 0 {
		t.Errorf("expected 0 files moved for non-existent source, got %d", movedCount)
	}

	if manifest != nil {
		t.Errorf("expected nil manifest for non-existent source, got %v", manifest)
	}
}

func TestMergeDiskDirectories_NamePrefix(t *testing.T) {
	t.Helper()
	srcDir, err := os.MkdirTemp("", "merge-src-*")
	if err != nil {
		t.Fatalf("failed to create src temp dir: %v", err)
	}
	defer os.RemoveAll(srcDir)

	dstDir, err := os.MkdirTemp("", "merge-dst-*")
	if err != nil {
		t.Fatalf("failed to create dst temp dir: %v", err)
	}
	defer os.RemoveAll(dstDir)

	// Create files with different prefixes
	files := []string{
		filepath.Join(srcDir, "cam1_20240101_100000_abc.mp4"),
		filepath.Join(srcDir, "cam2_20240101_100000_def.mp4"),
		filepath.Join(srcDir, "cam1_20240101_110000_ghi.mp4"),
	}

	for _, f := range files {
		if err := os.WriteFile(f, []byte("test"), 0o644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}
	}

	ctx := context.Background()

	// Move only cam1 files
	movedCount, manifest, err := mergeDiskDirectories(ctx, srcDir, dstDir, "cam1_", "", "")
	if err != nil {
		t.Fatalf("mergeDiskDirectories failed: %v", err)
	}

	if movedCount != 2 {
		t.Errorf("expected 2 files moved (cam1 only), got %d", movedCount)
	}

	if len(manifest) != 2 {
		t.Errorf("expected 2 manifest entries, got %d", len(manifest))
	}

	// Verify cam2 file still exists in source
	cam2File := filepath.Join(srcDir, "cam2_20240101_100000_def.mp4")
	if _, err := os.Stat(cam2File); os.IsNotExist(err) {
		t.Errorf("cam2 file should still exist in source")
	}
}

// Helper functions
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func replaceAll(s, old, replacement string) string {
	result := make([]byte, 0, len(s))
	i := 0
	for i < len(s) {
		if i <= len(s)-len(old) && s[i:i+len(old)] == old {
			result = append(result, replacement...)
			i += len(old)
		} else {
			result = append(result, s[i])
			i++
		}
	}
	return string(result)
}

// TestMergeDiskDirectories_RollbackWithManifest verifies the complete
// forward move + rollback cycle using manifest-based approach.
func TestMergeDiskDirectories_RollbackWithManifest(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	// Setup: create source and target directories
	srcDir, err := os.MkdirTemp("", "merge-src-*")
	if err != nil {
		t.Fatalf("failed to create src dir: %v", err)
	}
	defer os.RemoveAll(srcDir)

	dstDir, err := os.MkdirTemp("", "merge-dst-*")
	if err != nil {
		t.Fatalf("failed to create dst dir: %v", err)
	}
	defer os.RemoveAll(dstDir)

	sourceID := "front-door"
	targetID := "back-yard"

	// Create test files in source
	testFiles := []string{
		"front-door_20240101_100000_abc123.mp4",
		"front-door_20240101_110000_def456.mp4",
		"front-door_20240101_120000_ghi789.mp4",
	}

	var originalPaths []string
	for _, name := range testFiles {
		path := filepath.Join(srcDir, name)
		if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
			t.Fatalf("failed to create %s: %v", path, err)
		}
		originalPaths = append(originalPaths, path)
	}

	// Forward move
	movedCount, manifest, err := mergeDiskDirectories(ctx, srcDir, dstDir, "", sourceID, targetID)
	if err != nil {
		t.Fatalf("forward move failed: %v", err)
	}

	if movedCount != 3 {
		t.Errorf("expected 3 files moved, got %d", movedCount)
	}

	// Verify manifest is populated correctly
	if len(manifest) != 3 {
		t.Fatalf("expected 3 manifest entries, got %d", len(manifest))
	}

	// Sort manifest for consistent verification
	sort.Strings(manifest)

	// Rollback using manifest
	movedBack := 0
	for _, dstPath := range manifest {
		rel, err := filepath.Rel(dstDir, dstPath)
		if err != nil {
			t.Errorf("failed to get relative path: %v", err)
			continue
		}
		// Reverse rename
		rel = replaceAll(rel, targetID, sourceID)
		srcPath := filepath.Join(srcDir, rel)
		if err := os.MkdirAll(filepath.Dir(srcPath), 0o755); err != nil {
			t.Errorf("failed to create dir: %v", err)
			continue
		}
		if err := os.Rename(dstPath, srcPath); err != nil {
			t.Errorf("failed to move back: %v", err)
			continue
		}
		movedBack++
	}

	if movedBack != 3 {
		t.Errorf("expected 3 files moved back, got %d", movedBack)
	}

	// Verify all original files are back
	for _, path := range originalPaths {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("file %s should exist after rollback", path)
		}
	}

	// Verify target directory is empty
	entries, err := os.ReadDir(dstDir)
	if err != nil {
		t.Fatalf("failed to read dst dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("target directory should be empty after rollback, has %d entries", len(entries))
	}
}
