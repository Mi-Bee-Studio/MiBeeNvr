package main

import (
	"bufio"
	"fmt"
	"net/http"
	"os"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	authmw "github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
)

// Function variables for testability. Tests override these to avoid os.Exit().
var (
	cmdEncryptConfigFn = cmdEncryptConfig
	cmdDownloadModelFn = cmdDownloadModel
)

// ---------------------------------------------------------------------------
// CLI subcommands
// ---------------------------------------------------------------------------

// resolveHealthAddr determines the address the health probe should target.
//
// Precedence: explicit --addr > --config <path> > Docker auto-detected config >
// ":9090" default.
//
// The Docker auto-detection reads the config at the data dir (default /data,
// overridable via NVR_DATA_DIR) so the HEALTHCHECK command can stay bare
// (`mibee-nvr health`) and still probe the real server.listen — required for
// host-network mode where operators change server.listen (e.g. :9191) to avoid
// a host-port conflict. A missing/unreadable auto-detected config is non-fatal
// and falls through to the default addr.
func resolveHealthAddr(args []string) (string, error) {
	addr := ":9090"
	addrExplicit := false   // --addr given: skip all config-based resolution
	configExplicit := false // --config given: skip Docker auto-detection
	for i := 2; i < len(args); i++ {
		switch args[i] {
		case "--addr":
			addrExplicit = true
			i++
			if i < len(args) {
				addr = args[i]
			}
		case "--config":
			configExplicit = true
			i++
			if i < len(args) {
				cfg, err := config.Load(args[i])
				if err != nil {
					return "", err
				}
				if cfg.Server.Listen != "" {
					addr = cfg.Server.Listen
				}
			}
		}
	}
	// Auto-detect config in Docker environments only when neither --addr nor
	// --config was given.
	if !addrExplicit && !configExplicit {
		if dir := dockerStorageDir(); dir != "" {
			if cfg, err := config.Load(dir + "/mibee-nvr.yaml"); err == nil {
				if cfg.Server.Listen != "" {
					addr = cfg.Server.Listen
				}
			}
		}
	}
	return addr, nil
}

func cmdHealth() {
	addr, err := resolveHealthAddr(os.Args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}
	resp, err := http.Get("http://localhost" + addr + "/api/health")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Health check failed: %v\n", err)
		os.Exit(1)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		fmt.Fprintf(os.Stderr, "Health check failed: HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}
	resp.Body.Close()
	os.Exit(0)
}

func cmdInit() {
	var password, dataDir, listenAddr, cfgPath, username string
	var force bool
	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--password":
			i++
			if i < len(os.Args) {
				password = os.Args[i]
			}
		case "--data-dir":
			i++
			if i < len(os.Args) {
				dataDir = os.Args[i]
			}
		case "--listen":
			i++
			if i < len(os.Args) {
				listenAddr = os.Args[i]
			}
		case "--config":
			i++
			if i < len(os.Args) {
				cfgPath = os.Args[i]
			}
		case "--username":
			i++
			if i < len(os.Args) {
				username = os.Args[i]
			}
		case "--force":
			force = true
		}
	}
	if dataDir == "" {
		dataDir = "/var/lib/mibee-nvr"
	}
	if listenAddr == "" {
		listenAddr = ":9090"
	}
	if cfgPath == "" {
		cfgPath = "mibee-nvr.yaml"
	}
	if username == "" {
		username = "admin"
	}
	if password == "" {
		stat, _ := os.Stdin.Stat()
		if (stat.Mode() & os.ModeCharDevice) != 0 {
			fmt.Print("Enter password: ")
			scanner := bufio.NewScanner(os.Stdin)
			if scanner.Scan() {
				password = scanner.Text()
			}
		}
		if password == "" {
			fmt.Fprintln(os.Stderr, "Error: password is required (use --password or provide via terminal)")
			os.Exit(1)
		}
	}
	if len(password) < 8 {
		fmt.Fprintln(os.Stderr, "Error: password must be at least 8 characters")
		os.Exit(1)
	}
	if _, err := os.Stat(cfgPath); err == nil && !force {
		fmt.Fprintf(os.Stderr, "Error: config file %s already exists (use --force to overwrite)\n", cfgPath)
		os.Exit(2)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating data directory: %v\n", err)
		os.Exit(1)
	}
	hash, err := authmw.HashPassword(password)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error hashing password: %v\n", err)
		os.Exit(1)
	}
	cfg := config.Config{
		Server:        config.ServerConfig{Listen: listenAddr},
		Storage:       config.StorageConfig{RootDir: dataDir, SegmentDuration: "30s"},
		Auth:          config.AuthConfig{Username: username, PasswordHash: hash},
		Cameras:       []config.CameraConfig{},
		Cleanup:       config.CleanupConfig{RetentionDays: 30, CheckInterval: "1h", DiskThresholdPercent: 95},
		FTP:           config.FTPConfig{Port: 2121, PassivePortRange: "2122-2140"},
		WebDAV:        config.WebDAVConfig{PathPrefix: "/dav"},
		Observability: config.ObservabilityConfig{LogLevel: "info", LogFormat: "text"},
		Version:       "1.0",
		AI: config.AIConfig{
			Enabled:             false,
			ConfidenceThreshold: 0.5,
			FrameSkipRate:       10,
			EnabledCameras:      []string{},
		},
	}
	if err := config.Save(cfgPath, &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving config: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Configuration saved to %s\n", cfgPath)
	fmt.Printf("Data directory: %s\n", dataDir)
	fmt.Println("\nNext steps:")
	fmt.Printf("  1. Edit %s to add your cameras\n", cfgPath)
	fmt.Printf("  2. Run: ./mibee-nvr -config %s\n", cfgPath)
	fmt.Printf("  3. Open http://localhost%s in your browser\n", listenAddr)
	os.Exit(0)
}

func cmdHashPassword() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: mibee-nvr hash-password <password>")
		os.Exit(1)
	}
	hash, err := authmw.HashPassword(os.Args[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(hash)
	os.Exit(0)
}

func cmdEncryptConfig() {
	cfgPath := "mibee-nvr.yaml"
	for i := 2; i < len(os.Args); i++ {
		if os.Args[i] == "--config" {
			i++
			if i < len(os.Args) {
				cfgPath = os.Args[i]
			}
		}
	}
	fields, err := config.EncryptConfigFile(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if len(fields) == 0 {
		fmt.Println("No plaintext sensitive fields found. All fields are already encrypted or empty.")
	} else {
		fmt.Printf("Encrypted %d sensitive field(s) in %s:\n", len(fields), cfgPath)
		for _, f := range fields {
			fmt.Printf("  - %s\n", f)
		}
	}
	os.Exit(0)
}

// dispatchSubcommand handles CLI subcommand dispatch.
// It routes os.Args to the appropriate handler function.
func dispatchSubcommand(args []string) {
	if len(args) <= 1 {
		return
	}
	switch args[1] {
	case "health":
		cmdHealth()
	case "init":
		cmdInit()
	case "repair":
		cmdRepair()
	case "hash-password":
		cmdHashPassword()
	case "encrypt-config":
		cmdEncryptConfigFn()
	case "download-model":
		cmdDownloadModelFn()
	case "merge-cameras":
		cmdMergeCameras()
	}
}
