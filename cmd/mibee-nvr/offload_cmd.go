package main

// offload_cmd.go — `mibee-nvr offload` CLI (issue #874 batch 1): operator
// surface for the S3-compatible object-storage offload pipeline.
//
//	mibee-nvr offload status                       — outbox counters
//	mibee-nvr offload evict [--all-uploaded]       — dry-run report (default)
//	                [--camera ID] [--execute]         --execute applies verified eviction
//
// Eviction is HeadObject-verified per item (internal/offload.RunEvict); the
// remote object is never deleted. Safe to run against a live server (WAL
// readers); prefer a quiet server for large batches.

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/objectstore"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/offload"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// newOffloadStoreFn is the S3 store factory seam (tests inject a fake).
var newOffloadStoreFn = func(rc config.RemoteStorageConfig) (objectstore.Store, error) {
	return objectstore.NewS3(objectstore.Config{
		EndpointURL:     rc.EndpointURL,
		Region:          rc.Region,
		Bucket:          rc.Bucket,
		PathStyle:       rc.PathStyle == nil || *rc.PathStyle,
		AccessKeyID:     rc.AccessKeyID,
		SecretAccessKey: rc.SecretAccessKey,
	})
}

// cmdOffload is the dispatch entry from cli.go. The work lives in
// runOffloadCommand (returns errors) so the DB lifecycle is defer-managed and
// os.Exit only happens here, after it.
func cmdOffload() {
	if err := runOffloadCommand(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	// Exit explicitly on success: returning would fall through into the full
	// server startup path in main (the cmdRepair os.Exit(runRepair()) pattern
	// — every subcommand owns its exit).
	os.Exit(0)
}

func runOffloadCommand() error {
	if len(os.Args) < 3 {
		return fmt.Errorf("usage: mibee-nvr offload <status|evict> [flags]")
	}
	configPath := "mibee-nvr.yaml"
	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		if args[i] == "--config" && i+1 < len(args) {
			i++
			configPath = args[i]
		}
	}

	switch os.Args[2] {
	case "status":
		db, _, err := openOffloadDB(configPath)
		if err != nil {
			return err
		}
		defer db.Close()
		return runOffloadStatus(db, os.Stdout)
	case "evict":
		return runOffloadEvictCommand(configPath)
	default:
		return fmt.Errorf("unknown offload subcommand %q (status | evict)", os.Args[2])
	}
}

// offloadEvictFlags is the parsed evict flag set.
type offloadEvictFlags struct {
	ConfigPath  string
	AllUploaded bool
	CameraID    string
	Execute     bool
	ShowHelp    bool
}

func runOffloadEvictCommand(configPath string) error {
	flags := offloadEvictFlags{ConfigPath: configPath}
	args := os.Args[3:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--all-uploaded":
			flags.AllUploaded = true
		case "--camera":
			if i+1 < len(args) {
				i++
				flags.CameraID = args[i]
			}
		case "--execute":
			flags.Execute = true
		case "--config":
			if i+1 < len(args) {
				i++
				flags.ConfigPath = args[i]
			}
		case "--help", "-h":
			flags.ShowHelp = true
		}
	}
	if flags.ShowHelp {
		fmt.Println(`usage: mibee-nvr offload evict [--config PATH] [--all-uploaded] [--camera ID] [--execute]

Evict (delete) local copies of recordings whose upload to the remote object
store is CONFIRMED. Every item is re-verified with a fresh HeadObject before
its local file is deleted; the remote object is never deleted.

  --all-uploaded   consider every confirmed upload regardless of evict.after_days
  --camera ID      restrict to one camera
  --execute        apply (default: dry-run report)`)
		return nil
	}

	db, cfg, err := openOffloadDB(flags.ConfigPath)
	if err != nil {
		return err
	}
	defer db.Close()

	store, err := newOffloadStoreFn(cfg.Storage.Remote)
	if err != nil {
		return fmt.Errorf("remote store: %w", err)
	}
	return runOffloadEvict(db, store, cfg.Storage.Remote, flags, os.Stdout)
}

// openOffloadDB opens the DB + config, erroring unless storage.remote.enabled.
func openOffloadDB(cfgPath string) (*storage.DB, *config.Config, error) {
	db, cfg, err := openDBFromConfig(cfgPath)
	if err != nil {
		return nil, nil, err
	}
	if !cfg.Storage.Remote.Enabled {
		db.Close()
		return nil, nil, fmt.Errorf("storage.remote is not enabled in the config")
	}
	return db, cfg, nil
}

// evictStoreFor builds the bucket→store resolver: the default store for ”,
// lazily-built stores for per-camera override buckets (config-derived, so a
// CLI evict reaches routed buckets exactly like the running manager).
func evictStoreFor(rc config.RemoteStorageConfig, def objectstore.Store) func(string) (objectstore.Store, error) {
	return func(bucket string) (objectstore.Store, error) {
		if bucket == "" {
			return def, nil
		}
		return objectstore.NewS3(objectstore.Config{
			EndpointURL:     rc.EndpointURL,
			Region:          rc.Region,
			Bucket:          bucket,
			PathStyle:       rc.PathStyle == nil || *rc.PathStyle,
			AccessKeyID:     rc.AccessKeyID,
			SecretAccessKey: rc.SecretAccessKey,
		})
	}
}

// runOffloadStatus prints the outbox counters.
func runOffloadStatus(db *storage.DB, w io.Writer) error {
	counts, err := db.CountOffloadByStatus(context.Background())
	if err != nil {
		return fmt.Errorf("count offload: %w", err)
	}
	backlog, err := db.CountOffloadBacklog(context.Background())
	if err != nil {
		return fmt.Errorf("count backlog: %w", err)
	}
	order := []string{
		storage.OffloadStatusPending, storage.OffloadStatusUploading,
		storage.OffloadStatusUploaded, storage.OffloadStatusEvicted, storage.OffloadStatusSkipped,
	}
	for _, status := range order {
		fmt.Fprintf(w, "%-10s %d\n", status+":", counts[status])
	}
	fmt.Fprintf(w, "backlog     %d (pending+uploading)\n", backlog)
	return nil
}

// runOffloadEvict computes the eligibility window from the config and runs
// the verified eviction. Dry-run unless flags.Execute.
func runOffloadEvict(db *storage.DB, store objectstore.Store, rc config.RemoteStorageConfig, flags offloadEvictFlags, w io.Writer) error {
	if rc.Evict.AfterDays <= 0 && !flags.AllUploaded {
		return fmt.Errorf("evict.after_days is 0 (upload-only) — pass --all-uploaded to evict confirmed uploads anyway")
	}
	var cutoff time.Time
	if flags.AllUploaded {
		cutoff = time.Now().UTC()
	} else {
		cutoff = time.Now().UTC().AddDate(0, 0, -rc.Evict.AfterDays)
	}
	if !flags.Execute {
		_, _ = fmt.Fprintln(w, "DRY-RUN (pass --execute to apply)")
	}

	summary, err := offload.RunEvict(context.Background(), db, offload.EvictOptions{
		StoreFor:        evictStoreFor(rc, store),
		ConfirmedBefore: cutoff,
		CameraID:        flags.CameraID,
		Execute:         flags.Execute,
	})
	if err != nil {
		return err
	}

	mode := "eligible"
	if flags.Execute {
		mode = "evicted"
	}
	fmt.Fprintf(w, "%s %d item(s), %s reclaimed\n", mode, summary.Eligible, humanBytes(summary.ReclaimedBytes))
	if flags.Execute && summary.Eligible != summary.Evicted {
		fmt.Fprintf(w, "note: %d verified item(s) were not evicted (local delete failed)\n", summary.Eligible-summary.Evicted)
	}
	for _, r := range summary.Refused {
		fmt.Fprintf(w, "REFUSED %s (%s): %s\n", r.RecordingID, r.ObjectKey, r.Reason)
	}
	if len(summary.Refused) > 0 {
		fmt.Fprintf(w, "%d item(s) refused — remote verification failed; nothing was deleted for them\n", len(summary.Refused))
	}
	return nil
}
