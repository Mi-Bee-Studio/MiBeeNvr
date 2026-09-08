package gb28181

import "github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"

// gb35114Logger serves both halves of the GB 35114 seam (#707): the default
// build's missing-tag warning and the tagged build's boot diagnostics.
var gb35114Logger = slogx.Component("gb35114")
