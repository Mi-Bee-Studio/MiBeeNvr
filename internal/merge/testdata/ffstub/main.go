//go:build ignore

// ffstub — a minimal ffmpeg stand-in for the transcode-unify tests: copies
// the FIRST -i input file to the output path (the argument following -y).
// Deterministic, no real encoding — it exercises the unify plumbing
// (argument wiring, output validation) on hosts without ffmpeg. The real
// ffmpeg decodes and joins all inputs; the stub's single-input output is
// what lets the duration-guard test reproduce the "dropped inputs" failure
// class.
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
	// FFSTUB_ARGV_FILE, when set, records the full argv — lets tests assert
	// exact flag wiring (e.g. the output muxer pin) without real ffmpeg.
	if argvFile := os.Getenv("FFSTUB_ARGV_FILE"); argvFile != "" {
		_ = os.WriteFile(argvFile, []byte(strings.Join(args, "\n")), 0o644)
	}
	var inputs []string
	outPath := ""
	for i, a := range args {
		if a == "-i" && i+1 < len(args) {
			inputs = append(inputs, args[i+1])
		}
		if a == "-y" && i+1 < len(args) {
			outPath = args[i+1]
		}
	}
	if len(inputs) == 0 || outPath == "" {
		fmt.Fprintln(os.Stderr, "ffstub: missing -i or -y")
		os.Exit(1)
	}
	src := inputs[0]
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
