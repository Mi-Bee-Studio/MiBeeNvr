// SPDX-License-Identifier: MIT

package storage

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ValidatePath resolves targetPath relative to baseDir and ensures the result
// is contained within baseDir. It returns the cleaned absolute path on success,
// or an error if the path escapes the base directory.
//
// targetPath may be absolute or relative:
//   - Absolute paths are checked directly against baseDir.
//   - Relative paths are joined with baseDir first.
//
// This is the canonical path validation function for all file access in MiBee NVR.
func ValidatePath(baseDir, targetPath string) (string, error) {
	baseDirAbs, err := filepath.Abs(baseDir)
	if err != nil {
		return "", fmt.Errorf("pathutil: failed to resolve base directory %q: %w", baseDir, err)
	}

	// If targetPath is already absolute, use it directly; otherwise join with baseDir.
	var resolvedPath string
	if filepath.IsAbs(targetPath) {
		resolvedPath = filepath.Clean(targetPath)
	} else {
		resolvedPath = filepath.Join(baseDirAbs, targetPath)
	}

	// Canonicalize to absolute path.
	absPath, err := filepath.Abs(resolvedPath)
	if err != nil {
		return "", fmt.Errorf("pathutil: failed to resolve path %q: %w", targetPath, err)
	}

	// If EvalSymlinks fails (e.g., path doesn't exist), fall through to
	// the containment check on the non-resolved path.
	baseEval, baseErr := filepath.EvalSymlinks(baseDirAbs)
	if baseErr == nil {
		baseDirAbs = baseEval
	}
	pathEval, pathErr := filepath.EvalSymlinks(absPath)
	if pathErr == nil {
		absPath = pathEval
	}

	// Verify containment: the resolved path must be within baseDirAbs.
	rel, err := filepath.Rel(baseDirAbs, absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("pathutil: path %q resolves outside storage root %q", targetPath, baseDirAbs)
	}

	return absPath, nil
}

