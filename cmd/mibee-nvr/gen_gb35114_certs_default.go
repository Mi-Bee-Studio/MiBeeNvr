//go:build !gb35114

package main

import (
	"fmt"
	"os"
)

// cmdGenGB35114Certs is the default-build stub: the certificate generator
// needs the gb35114-tagged dependency tree, so plain builds explain instead
// of failing cryptically.
func cmdGenGB35114Certs() {
	fmt.Fprintln(os.Stderr,
		"Error: gen-gb35114-certs requires a -tags gb35114 build:\n"+
			"  CGO_ENABLED=0 go build -tags gb35114 ./cmd/mibee-nvr")
	os.Exit(1)
}
