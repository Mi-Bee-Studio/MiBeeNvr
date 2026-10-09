package ui

import (
	"fmt"
	"io/fs"
	"strings"
)

// VerifyEmbeddedSPA reports whether an embedded static tree carries the BUILT
// SPA document — not just any files. The vite output (index.html referencing a
// hashed assets/index-*.js bundle, plus the assets/ tree) is gitignored, so a
// fresh checkout or a linked worktree only has the tracked loose files
// (favicon, icons, sw.js). Compiling there with a bare `go build` used to
// embed an effectively empty UI: the web root served a directory listing
// while the process looked perfectly healthy. Deployment flows that build the
// SPA first (`make build`, `make cross`, CI, Docker) are unaffected.
//
// fsys is a parameter so tests can exercise the negative cases with an
// in-memory FS instead of the compile-time embed.
func VerifyEmbeddedSPA(fsys fs.FS) error {
	b, err := fs.ReadFile(fsys, "static/index.html")
	if err != nil {
		return fmt.Errorf(
			"static/index.html is not embedded — the SPA build output was not copied into internal/ui/static before compiling (run `make build`, or `cd web && npm run build && cp -r web/dist/* internal/ui/static/`): %w",
			err,
		)
	}
	doc := string(b)
	// The built document mounts the app and references the hashed bundle; the
	// source template in web/index.html has the mount but no hashed assets, so
	// embedding the template instead of the build output also fails here.
	if !strings.Contains(doc, `id="app"`) || !strings.Contains(doc, "assets/index-") {
		return fmt.Errorf(
			"embedded static/index.html does not look like a built SPA (missing app mount or hashed assets/index-*.js reference) — embed the vite output from web/dist, not the source template",
		)
	}
	return nil
}
