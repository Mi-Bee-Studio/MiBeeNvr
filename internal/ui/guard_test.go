package ui

import (
	"strings"
	"testing"
	"testing/fstest"
)

// VerifyEmbeddedSPA is the startup gate against empty-UI binaries: a checkout
// (or linked worktree) that never built the frontend must not compile into a
// binary that silently serves a directory listing as its web UI. The negative
// cases pin the two shapes seen in the wild: no index.html at all, and a
// source template embedded instead of the build output.
func TestVerifyEmbeddedSPA(t *testing.T) {
	built := `<html><body><div id="app"></div>` +
		`<script type="module" crossorigin src="./assets/index-D-klLM4w.js"></script></body></html>`

	cases := []struct {
		name string
		fsys fstest.MapFS
		want string // "" = expect success
	}{
		{
			name: "built SPA passes",
			fsys: fstest.MapFS{
				"static/index.html":        {Data: []byte(built)},
				"static/favicon.svg":       {Data: []byte("<svg/>")},
				"static/assets/index-x.js": {Data: []byte("console.log(1)")},
			},
		},
		{
			name: "no index.html (fresh checkout / worktree)",
			fsys: fstest.MapFS{
				"static/favicon.svg": {Data: []byte("<svg/>")},
				"static/sw.js":       {Data: []byte("// cache")},
			},
			want: "not embedded",
		},
		{
			name: "source template instead of build output",
			fsys: fstest.MapFS{
				"static/index.html": {Data: []byte(`<html><body><div id="app"></div>` +
					`<script type="module" src="/src/main.js"></script></body></html>`)},
			},
			want: "does not look like a built SPA",
		},
		{
			name: "index.html without app mount",
			fsys: fstest.MapFS{
				"static/index.html": {Data: []byte(`<html><script src="assets/index-x.js"></script></html>`)},
			},
			want: "does not look like a built SPA",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := VerifyEmbeddedSPA(c.fsys)
			if c.want == "" {
				if err != nil {
					t.Fatalf("VerifyEmbeddedSPA = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("VerifyEmbeddedSPA = nil, want error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), c.want)
			}
		})
	}
}

// The production embed must satisfy its own gate whenever the SPA has been
// built into the tree. Skipped when index.html is absent (a bare `go test
// ./...` checkout) — the runtime refusal in cmd/mibee-nvr covers that shape.
func TestVerifyEmbeddedSPAProductionEmbed(t *testing.T) {
	if _, err := StaticFS.ReadFile("static/index.html"); err != nil {
		t.Skip("no SPA built into this checkout (index.html absent)")
	}
	if err := VerifyEmbeddedSPA(StaticFS); err != nil {
		t.Fatalf("production embedded static tree failed its own gate: %v", err)
	}
}
