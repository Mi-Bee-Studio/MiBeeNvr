package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

func cmdMigrateMJPEG() {
	var cfgPath string
	opts := storage.MigrateOptions{
		Concurrency: 1,
		Cutoff:      72 * time.Hour,
	}

	for i := 2; i < len(os.Args); i++ {
		arg := os.Args[i]
		switch {
		case arg == "--execute":
			opts.DryRun = false
		case arg == "--dry-run":
			opts.DryRun = true
		case arg == "--purge-old":
			opts.PurgeOld = true
		case arg == "--resume":
			opts.Resume = true
		case arg == "--config" && i+1 < len(os.Args):
			i++
			cfgPath = os.Args[i]
		case arg == "--camera" && i+1 < len(os.Args):
			i++
			opts.CameraID = os.Args[i]
		case arg == "--limit" && i+1 < len(os.Args):
			i++
			n, err := strconv.Atoi(os.Args[i])
			if err == nil && n > 0 {
				opts.Limit = n
			}
		case arg == "--concurrency" && i+1 < len(os.Args):
			i++
			n, err := strconv.Atoi(os.Args[i])
			if err == nil && n > 0 {
				if n > 4 {
					n = 4
				}
				opts.Concurrency = n
			}
		case arg == "--cutoff" && i+1 < len(os.Args):
			i++
			d, err := time.ParseDuration(os.Args[i])
			if err == nil && d > 0 {
				opts.Cutoff = d
			}
		case arg == "--help" || arg == "-h":
			printMigrateMJPEGUsage()
			os.Exit(0)
		}
	}

	if cfgPath == "" {
		cfgPath = "mibee-nvr.yaml"
	}

	// Load config to get storage root dir.
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config %q: %v\n", cfgPath, err)
		os.Exit(1)
	}

	if err := config.Validate(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Config validation error: %v\n", err)
		os.Exit(1)
	}

	// Initialise DB.
	dbPath := cfg.Storage.RootDir + "/mibee-nvr.db"
	db, err := storage.New(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening database %q: %v\n", dbPath, err)
		os.Exit(1)
	}
	defer db.Close()

	ctx := context.Background()
	if err := db.Init(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error initialising database: %v\n", err)
		os.Exit(1)
	}

	// Initialise storage manager.
	store, err := storage.NewManager(cfg.Storage.RootDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating storage manager: %v\n", err)
		os.Exit(1)
	}

	// Set up SIGINT handling.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		slog.Warn("received signal, finishing current recording then exiting", "signal", sig.String())
		cancel()
	}()

	if opts.DryRun {
		fmt.Println("DRY RUN MODE — no changes will be made")
		fmt.Println()
	} else {
		fmt.Println("EXECUTE MODE — changes WILL be made")
		fmt.Println()
	}

	if err := storage.MigrateMJPEGToAVI(ctx, db, store, opts); err != nil {
		if err == context.Canceled {
			fmt.Fprintln(os.Stderr, "\nMigration interrupted by signal.")
			os.Exit(130)
		}
		fmt.Fprintf(os.Stderr, "Migration failed: %v\n", err)
		os.Exit(1)
	}

	os.Exit(0)
}

func printMigrateMJPEGUsage() {
	fmt.Print(`Usage: mibee-nvr migrate-mjpeg [options]

Migrate legacy MJPEG (jpg-directory) recordings to AVI format.

Options:
  --execute         Actually perform the migration (default: dry-run, no changes)
  --dry-run         Explicit dry-run mode (default)
  --camera <id>     Only process recordings for this camera
  --limit <n>       Max recordings to process (0 = no limit)
  --concurrency <n> Worker count (default 1, max 4)
  --cutoff <dur>    Age cutoff for "old" recordings (default 72h, e.g. 24h, 7d)
  --purge-old       Delete recordings older than cutoff instead of skipping
  --resume          Clean orphan .avi files before starting
  --config <path>   Config file path (default: mibee-nvr.yaml)
  --help, -h        Show this help

Safe by default: run without --execute to see what would be done.
`)
}
