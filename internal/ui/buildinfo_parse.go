package ui

import (
	"fmt"
	"strings"
)

// parseBuildInfo extracts the one-line fingerprint from a build-info.json
// blob: {"git":"560087c","built_at":"2026-09-25T17:00:00+08:00"} →
// "560087c@2026-09-25T17:00:00+08:00". Kept dependency-free (no encoding/json)
// so the embed package stays minimal.
func parseBuildInfo(b []byte) (string, error) {
	s := string(b)
	git := jsonFlatString(s, "git")
	builtAt := jsonFlatString(s, "built_at")
	if git == "" && builtAt == "" {
		return "", fmt.Errorf("no git/built_at fields in build-info")
	}
	if builtAt == "" {
		return git, nil
	}
	if git == "" {
		return builtAt, nil
	}
	return git + "@" + builtAt, nil
}

// jsonFlatString pulls a flat string field out of a small JSON object. Only
// for the controlled, build-generated build-info.json — not general parsing.
func jsonFlatString(s, key string) string {
	needle := `"` + key + `"`
	i := strings.Index(s, needle)
	if i < 0 {
		return ""
	}
	rest := s[i+len(needle):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return ""
	}
	rest = rest[j+1:]
	k := strings.Index(rest, `"`)
	if k < 0 {
		return ""
	}
	return rest[:k]
}
