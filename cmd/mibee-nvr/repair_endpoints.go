package main

// repair normalize-endpoints: canonicalize every camera's onvif_endpoint column
// so that dedup queries (CameraIDByOnvifEndpoint / CameraExistsByOnvifEndpoint)
// match across discovery paths. Fixes legacy rows written before #175, where a
// row stored as "http://1.2.3.4:80/onvif/..." failed to match a later discovery
// of "http://1.2.3.4/onvif/..." (no default port) and got re-enrolled as a
// duplicate camera.
//
// Safe to run while the server is stopped (preferred) or running (WAL mode).
// Default is --dry-run; pass --execute to apply.

import (
	"fmt"
	"os"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

func runRepairNormalizeEndpoints() int {
	opts := parseRepairFlags(3) // subcommand is os.Args[2]; flags start at 3

	// Help flag short-circuit (parseRepairFlags does not handle --help).
	for _, a := range os.Args[3:] {
		if a == "--help" || a == "-h" {
			printRepairNormalizeEndpointsUsage()
			return 0
		}
	}

	db, _, err := openDBFromConfig(opts.configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	defer db.Close()

	ctx, cancel := setupSignalHandler()
	defer cancel()

	rows, err := db.ListCameraEndpointsForRepair(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: query cameras: %v\n", err)
		return 1
	}

	fmt.Println("REPAIR NORMALIZE-ENDPOINTS")
	fmt.Println("===========================")
	if opts.dryRun {
		fmt.Println("DRY RUN — no changes made. Run with --execute to apply.")
	}
	fmt.Println()

	changed := 0
	unchanged := 0
	for _, r := range rows {
		normalized := storage.NormalizeOnvifEndpoint(r.Endpoint)
		if normalized == r.Endpoint {
			unchanged++
			continue
		}
		changed++
		fmt.Printf("  %s\n    %q\n →  %q\n", r.Name, r.Endpoint, normalized)
		if !opts.dryRun {
			if err := db.UpdateCameraOnvifEndpointRaw(ctx, r.ID, normalized); err != nil {
				fmt.Fprintf(os.Stderr, "Error updating %s: %v\n", r.ID, err)
				return 1
			}
		}
	}

	fmt.Println()
	if changed == 0 {
		fmt.Printf("All %d camera endpoint(s) already canonical. Nothing to do.\n", unchanged)
	} else {
		action := "would be updated"
		if !opts.dryRun {
			action = "updated"
		}
		fmt.Printf("%d endpoint(s) %s, %d already canonical.\n", changed, action, unchanged)
	}
	return 0
}

func printRepairNormalizeEndpointsUsage() {
	fmt.Print(`Usage: mibee-nvr repair normalize-endpoints [--execute] [--config <path>]

Canonicalize every camera's onvif_endpoint column so that dedup queries match
across discovery paths (elides default :80/:443, lowercases scheme/host, strips
trailing slash). Fixes legacy rows written before issue #175, where a device
stored as "http://1.2.3.4:80/..." failed to match a later discovery of
"http://1.2.3.4/..." and got re-enrolled as a duplicate.

Safe to run while the server is stopped (preferred) or running (WAL mode).

Options:
  --dry-run       Report what would change without modifying (default)
  --execute       Actually apply the normalization
  --config <path> Config file path (default: mibee-nvr.yaml)
  --help, -h      Show this help
`)
}
