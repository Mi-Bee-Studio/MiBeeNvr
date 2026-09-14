//go:build gb35114

package main

// gen-gb35114-certs (#452): issues the GB 35114-2017 A-level pilot kit —
// a self-signed SM2 platform identity plus device identities signed by the
// platform — laid out exactly as gb28181.security35114 consumes. Only
// exists in -tags gb35114 builds (the default build prints guidance).

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/gb28181"
)

func cmdGenGB35114Certs() {
	fs := flag.NewFlagSet("gen-gb35114-certs", flag.ExitOnError)
	platformID := fs.String("platform-id", "", "20-digit GB platform ID (gb28181.server_id)")
	deviceIDs := fs.String("device-id", "", "20-digit device ID; comma-separate for several (repeat not supported)")
	outDir := fs.String("out-dir", "gb35114-certs", "output directory (created if missing)")
	days := fs.Int("days", 0, "certificate validity in days (default 3650)")
	_ = fs.Parse(os.Args[2:])

	var ids []string
	for _, id := range strings.Split(*deviceIDs, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}

	res, err := gb28181.GenerateGB35114Material(gb28181.GB35114CertRequest{
		PlatformID: *platformID,
		DeviceIDs:  ids,
		OutDir:     *outDir,
		Validity:   time.Duration(*days) * 24 * time.Hour,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("GB 35114-2017 A-level pilot material issued:")
	fmt.Printf("  platform cert:  %s\n", res.PlatformCert)
	fmt.Printf("  platform key:   %s (0600)\n", res.PlatformKey)
	if len(ids) > 0 {
		fmt.Printf("  device certs:   %s/<deviceID>.pem + .key\n", res.DeviceCertsDir)
	} else {
		fmt.Printf("  device certs:   %s (none issued — rerun with --device-id)\n", res.DeviceCertsDir)
	}
	fmt.Println(`
Wire into mibee-nvr.yaml (rebuild with -tags gb35114 to enable):
  gb28181:
    security35114:
      enabled: true
      platform_cert: ` + res.PlatformCert + `
      platform_key:  ` + res.PlatformKey + `
      device_certs_dir: ` + res.DeviceCertsDir + `

Distribute <deviceID>.pem + .key to each device (device-side signing
identity). For production, provision from a real CA instead — this kit is
for pilots and labs.`)
}
