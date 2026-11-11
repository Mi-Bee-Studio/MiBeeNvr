//go:build ignore

// ffstub — a minimal ffmpeg stand-in for the transcode-unify tests: parses
// the concat-list file passed via -i and copies the FIRST listed file to the
// output path (the argument following -y). Deterministic, no real encoding —
// it exercises the unify plumbing (list building, argument wiring, output
// validation) on hosts without ffmpeg.
//
// Built by transcode_unify_test.go via `go build` into a temp executable.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	args := os.Args[1:]
	listPath, outPath := "", ""
	for i, a := range args {
		if a == "-i" && i+1 < len(args) {
			listPath = args[i+1]
		}
		if a == "-y" && i+1 < len(args) {
			outPath = args[i+1]
		}
	}
	if listPath == "" || outPath == "" {
		fmt.Fprintln(os.Stderr, "ffstub: missing -i or -y")
		os.Exit(1)
	}
	raw, err := os.ReadFile(listPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ffstub:", err)
		os.Exit(1)
	}
	src := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "file '") && strings.HasSuffix(line, "'") {
			src = strings.TrimSuffix(strings.TrimPrefix(line, "file '"), "'")
			break
		}
	}
	if src == "" {
		fmt.Fprintln(os.Stderr, "ffstub: empty concat list")
		os.Exit(1)
	}
	in, err := os.Open(src)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ffstub:", err)
		os.Exit(1)
	}
	defer in.Close()
	out, err := os.Create(outPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ffstub:", err)
		os.Exit(1)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		fmt.Fprintln(os.Stderr, "ffstub:", err)
		os.Exit(1)
	}
}
