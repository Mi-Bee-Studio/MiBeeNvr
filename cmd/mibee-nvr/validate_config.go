package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

// cmdValidateConfig implements `mibee-nvr validate-config [--config <path>]`.
//
// Pre-deploy smoke check for hand-edited YAML: loads the config through the
// exact same Load → Validate pipeline the server uses at boot and reports
// pass/fail without starting anything. Exit code 0 = the NVR would boot on
// this file; 1 = it would crash-loop. Born from the 2026-09-11 M5 incident
// (removing a vision instance while a camera still referenced it bricked the
// service into a systemd restart loop for ~2 minutes).
func cmdValidateConfig() {
	os.Exit(runValidateConfig(os.Args))
}

// runValidateConfig is the testable core of cmdValidateConfig: returns the
// process exit code instead of calling os.Exit itself. defaultPath mirrors
// the serve path's flag default so a bare `mibee-nvr validate-config` checks
// the file the server would load from the working directory.
func runValidateConfig(args []string) int {
	fs := flag.NewFlagSet("validate-config", flag.ContinueOnError)
	configPath := fs.String("config", "mibee-nvr.yaml", "path to the config file to validate")
	if err := fs.Parse(args[2:]); err != nil {
		return 1
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %s: %v\n", *configPath, err)
		return 1
	}
	if err := config.Validate(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %s: config validation: %v\n", *configPath, err)
		return 1
	}
	fmt.Printf("OK %s — the NVR would boot on this config\n", *configPath)
	return 0
}
