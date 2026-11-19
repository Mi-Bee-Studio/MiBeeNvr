package api

// Golden route-table test (#repo-governance Phase 3.2 prerequisite).
//
// Before internal/api is carved into per-domain subpackages, the public route
// surface must be frozen: a refactor that silently drops, renames or changes
// the method of a route is a breaking API change. This test walks the real
// chi router built by Handler.Routes() and compares the sorted
// "METHOD /path" set against the golden list below.
//
// Updating the golden list is a deliberate act: when a route is intentionally
// added/removed, update this file in the same PR and say so in the description.

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

func collectRoutes(t *testing.T, h *Handler) []string {
	t.Helper()
	router, ok := h.Routes().(chi.Routes)
	require.True(t, ok, "Routes() must return a chi.Routes")

	var out []string
	err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.TrimSuffix(route, "/")
		if route == "" {
			route = "/"
		}
		out = append(out, method+" "+route)
		return nil
	})
	require.NoError(t, err)
	sort.Strings(out)
	return out
}

// TestRoutesGolden pins the complete method+path surface. Sub-router mounts
// (application middleware excluded) are included as walked.
func TestRoutesGolden(t *testing.T) {
	t.Parallel()
	h, _, _ := newCameraAPIEnv(t)

	got := collectRoutes(t, h)
	require.NotEmpty(t, got, "route table must not be empty")

	// Snapshot maintainers: replace `want` verbatim when a route change is
	// intentional. The count assertion below catches a mass-migration error
	// (e.g. a domain package failing to register at all).
	want := goldenRoutes()
	require.Equal(t, len(want), len(got), "route count changed: %d → %d\nnew routes: %s",
		len(want), len(got), diffRoutes(want, got))
	require.Equal(t, want, got, "route table drifted from golden snapshot:\n%s", diffRoutes(want, got))
}

func diffRoutes(want, got []string) string {
	inWant := make(map[string]bool, len(want))
	for _, r := range want {
		inWant[r] = true
	}
	inGot := make(map[string]bool, len(got))
	for _, r := range got {
		inGot[r] = true
	}
	var b strings.Builder
	for _, r := range want {
		if !inGot[r] {
			b.WriteString("  - missing: " + r + "\n")
		}
	}
	for _, r := range got {
		if !inWant[r] {
			b.WriteString("  + added:   " + r + "\n")
		}
	}
	if b.Len() == 0 {
		return "(same set, ordering/duplication differs)"
	}
	return b.String()
}
