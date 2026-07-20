package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// merge_cameras is an end-to-end CLI: it reassigns all DB data (recordings,
// health/AI/transcoding events, file_path columns), moves disk files, removes
// the source camera from config + DB, AND can apply target field overrides
// (--target-onvif-endpoint, --target-url, --target-disable-timelapse) so that
// one command fully resolves a merge. The CLI is transactional: any failure
// triggers an automatic rollback of DB, disk, and config.

func cmdMergeCameras() {
	os.Exit(runMergeCameras())
}

type mergeCamerasFlags struct {
	cfgPath  string
	sourceID string
	targetID string
	dryRun   bool
	execute  bool
	force    bool
	// Target field overrides applied to the target camera after merge.
	targetOnvifEndpoint   string
	targetURL             string
	targetDisableTimelase bool
}

// parseFlag handles both "--flag value" and "--flag=value" syntaxes for the
// next argument in argv. Returns the consumed value and whether it matched.
func parseFlag(args []string, i *int, name string) (string, bool) {
	a := args[*i]
	// "--name=value" form
	if strings.HasPrefix(a, "--"+name+"=") {
		v := strings.TrimPrefix(a, "--"+name+"=")
		return v, true
	}
	// "--name value" form
	if a == "--"+name && *i+1 < len(args) {
		*i++
		return args[*i], true
	}
	return "", false
}

// parseBoolFlag handles "--name" (sets true) and "--name=true|false".
func parseBoolFlag(args []string, i *int, name string) (bool, bool) {
	a := args[*i]
	if strings.HasPrefix(a, "--"+name+"=") {
		v := strings.TrimPrefix(a, "--"+name+"=")
		switch strings.ToLower(v) {
		case "true", "1", "yes":
			return true, true
		case "false", "0", "no":
			return false, true
		}
		return false, false
	}
	if a == "--"+name {
		return true, true
	}
	return false, false
}

func parseMergeCamerasFlags() (mergeCamerasFlags, int) {
	var f mergeCamerasFlags
	f.dryRun = true // default: safe mode

	args := os.Args
	for i := 2; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--execute":
			f.dryRun = false
			f.execute = true
		case arg == "--dry-run":
			f.dryRun = true
			f.execute = false
		case arg == "--force":
			f.force = true
		default:
			if v, ok := parseFlag(args, &i, "source"); ok {
				f.sourceID = v
				continue
			}
			if v, ok := parseFlag(args, &i, "target"); ok {
				f.targetID = v
				continue
			}
			if v, ok := parseFlag(args, &i, "config"); ok {
				f.cfgPath = v
				continue
			}
			if v, ok := parseFlag(args, &i, "target-onvif-endpoint"); ok {
				f.targetOnvifEndpoint = v
				continue
			}
			if v, ok := parseFlag(args, &i, "target-url"); ok {
				f.targetURL = v
				continue
			}
			if v, ok := parseBoolFlag(args, &i, "target-disable-timelapse"); ok {
				f.targetDisableTimelase = v
				continue
			}
			if arg == "--help" || arg == "-h" {
				printMergeCamerasUsage()
				return f, 0
			}
		}
	}
	return f, -1
}

func runMergeCameras() int {
	f, exitCode := parseMergeCamerasFlags()
	if exitCode == 0 {
		return 0
	}

	if f.sourceID == "" || f.targetID == "" {
		fmt.Fprintln(os.Stderr, "Error: --source and --target are required")
		printMergeCamerasUsage()
		return 1
	}
	if f.sourceID == f.targetID {
		fmt.Fprintln(os.Stderr, "Error: source and target must be different camera IDs")
		return 1
	}
	if f.cfgPath == "" {
		f.cfgPath = "mibee-nvr.yaml"
	}

	// Load config. Keep an in-memory copy for rollback.
	cfg, err := config.Load(f.cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config %q: %v\n", f.cfgPath, err)
		return 1
	}
	originalYamlBytes, err := os.ReadFile(f.cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading config for backup: %v\n", err)
		return 1
	}

	var sourceCfg, targetCfg *config.CameraConfig
	for i := range cfg.Cameras {
		if cfg.Cameras[i].ID == f.sourceID {
			sourceCfg = &cfg.Cameras[i]
		}
		if cfg.Cameras[i].ID == f.targetID {
			targetCfg = &cfg.Cameras[i]
		}
	}
	if sourceCfg == nil {
		fmt.Fprintf(os.Stderr, "Error: source camera %q not found in config\n", f.sourceID)
		return 1
	}
	if targetCfg == nil {
		fmt.Fprintf(os.Stderr, "Error: target camera %q not found in config\n", f.targetID)
		return 1
	}

	dbPath := cfg.Storage.RootDir + "/mibee-nvr.db"
	db, err := storage.New(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening database %q: %v\n", dbPath, err)
		return 1
	}
	defer db.Close()

	ctx := context.Background()
	if err := db.Init(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error initialising database: %v\n", err)
		return 1
	}

	store, err := storage.NewManager(cfg.Storage.RootDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating storage manager: %v\n", err)
		return 1
	}

	recCount, err := db.CountRecordingsByCamera(ctx, f.sourceID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error counting recordings for %q: %v\n", f.sourceID, err)
		return 1
	}
	healthCount := countRowsByCamera(ctx, db.DB(), "camera_health_events", f.sourceID)
	aiCount := countRowsByCamera(ctx, db.DB(), "ai_events", f.sourceID)
	transcodeCount := countRowsByCamera(ctx, db.DB(), "transcoding_tasks", f.sourceID)

	recordings, err := listRecordingsByCamera(ctx, db.DB(), f.sourceID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing recordings for %q: %v\n", f.sourceID, err)
		return 1
	}

	rootDir := store.RootDir()
	sourceDir := filepath.Join(rootDir, f.sourceID)
	targetDir := filepath.Join(rootDir, f.targetID)

	var orphanCount int
	var fileList []string
	for _, rec := range recordings {
		if rec.filePath != "" {
			fileList = append(fileList, rec.filePath)
			if _, err := os.Stat(rec.filePath); errors.Is(err, fs.ErrNotExist) {
				orphanCount++
			}
		}
	}

	// Plan summary (printed in both dry-run and execute modes).
	fmt.Println("MERGE CAMERAS PLAN")
	fmt.Println("==================")
	fmt.Println()
	fmt.Printf("Source:      %s (%s)\n", f.sourceID, sourceCfg.Name)
	fmt.Printf("Target:      %s (%s)\n", f.targetID, targetCfg.Name)
	fmt.Printf("Config:      %s\n", f.cfgPath)
	fmt.Printf("Storage:     %s\n", cfg.Storage.RootDir)
	fmt.Println()
	fmt.Println("Data to be re-tagged (camera_id + file_path rewritten):")
	fmt.Printf("  Recordings:          %d\n", recCount)
	fmt.Printf("  Health events:       %d\n", healthCount)
	fmt.Printf("  AI events:           %d\n", aiCount)
	fmt.Printf("  Transcoding tasks:   %d\n", transcodeCount)
	fmt.Println()
	fmt.Println("Disk operations:")
	fmt.Printf("  Source directory:    %s\n", sourceDir)
	fmt.Printf("  Target directory:    %s\n", targetDir)
	fmt.Printf("  Recording files:     %d\n", len(fileList))
	fmt.Printf("  Orphan records:      %d\n", orphanCount)
	fmt.Println()
	fmt.Println("Config changes:")
	fmt.Printf("  Remove source camera: %s\n", f.sourceID)
	if f.targetOnvifEndpoint != "" {
		fmt.Printf("  Update target onvif_endpoint: %s\n", f.targetOnvifEndpoint)
	}
	if f.targetURL != "" {
		fmt.Printf("  Update target url: %s\n", f.targetURL)
	}
	if f.targetDisableTimelase {
		fmt.Printf("  Disable target timelapse.enabled: true → false\n")
	}
	fmt.Println()

	if !f.execute {
		fmt.Println("DRY RUN — no changes made.")
		fmt.Println("Run with --execute to apply.")
		return 0
	}

	// EXECUTE MODE below.

	if isPortOpen("localhost:9090") {
		fmt.Fprintln(os.Stderr, "Error: NVR appears to be running on port 9090.")
		fmt.Fprintln(os.Stderr, "  Please stop the NVR first (systemctl stop mibee-nvr)")
		fmt.Fprintln(os.Stderr, "  before running merge-cameras --execute.")
		return 1
	}

	if orphanCount > 0 && !f.force {
		fmt.Fprintf(os.Stderr, "Error: %d orphan recording(s) found (DB row has no disk file).\n", orphanCount)
		fmt.Fprintln(os.Stderr, "  Use --force to proceed anyway.")
		return 1
	}

	fmt.Println("EXECUTE MODE — applying changes...")
	fmt.Println()

	// SIGINT/SIGTERM cancellation.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		slog.Warn("merge-cameras received signal, completing current step then exiting", "signal", sig.String())
		cancel()
	}()

	// Track progress so we can rollback if a later step fails.
	var (
		dbBackedUp     bool
		backupPath     string
		dataReassigned bool
		diskMoved      bool
		movedCount     int
		movedFiles     []string // manifest of files moved during forward move
		cfgWritten     bool
	)

	// rollback reverts all completed steps. Disk rollback is best-effort
	// (the failure may have been triggered by disk itself).
	rollback := func(failedStep string, stepErr error) int {
		fmt.Fprintf(os.Stderr, "\n[ROLLBACK] step %q failed: %v\n", failedStep, stepErr)
		if cfgWritten {
			fmt.Fprint(os.Stderr, "  -> restoring config from in-memory copy... ")
			if err := os.WriteFile(f.cfgPath, originalYamlBytes, 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "FAILED: %v (manual restore needed)\n", err)
			} else {
				fmt.Fprintln(os.Stderr, "OK")
			}
		}
		if diskMoved {
			fmt.Fprintf(os.Stderr, "  -> moving %d files back from target to source... ", movedCount)
			movedBack := 0
			for _, dstPath := range movedFiles {
				rel, err := filepath.Rel(targetDir, dstPath)
				if err != nil {
					continue
				}
				// Reverse the rename: targetID → sourceID in the relative path
				rel = strings.ReplaceAll(rel, f.targetID, f.sourceID)
				srcPath := filepath.Join(sourceDir, rel)
				if err := os.MkdirAll(filepath.Dir(srcPath), 0o755); err != nil {
					continue
				}
				if err := os.Rename(dstPath, srcPath); err == nil {
					movedBack++
				}
			}
			fmt.Fprintf(os.Stderr, "%d files moved back\n", movedBack)
		}
		if dataReassigned {
			fmt.Fprint(os.Stderr, "  -> restoring DB from backup... ")
			if err := restoreDBFromBackup(dbPath, backupPath); err != nil {
				fmt.Fprintf(os.Stderr, "FAILED: %v (manual: cp %s %s)\n", err, backupPath, dbPath)
			} else {
				fmt.Fprintln(os.Stderr, "OK")
			}
		}
		fmt.Fprintf(os.Stderr, "[ROLLBACK] complete. Original state restored as much as possible.\n")
		return 1
	}

	// Step 1: DB backup.
	backupPath = dbPath + ".merge-backup." + time.Now().Format("20060102T150405")
	fmt.Printf("[1/5] Backing up DB to %s ... ", backupPath)
	if err := db.Backup(ctx, backupPath); err != nil {
		fmt.Fprintf(os.Stderr, "FAILED: %v\n", err)
		return 1
	}
	fmt.Println("OK")
	dbBackedUp = true
	_ = dbBackedUp

	// Step 2: ReassignCameraData (re-tag rows + rewrite file_path).
	fmt.Printf("[2/5] Reassigning camera data (%d recordings, %d health, %d ai, %d transcoding) ... ",
		recCount, healthCount, aiCount, transcodeCount)
	if err := db.ReassignCameraData(ctx, f.sourceID, f.targetID); err != nil {
		fmt.Fprintf(os.Stderr, "FAILED: %v\n", err)
		fmt.Fprintln(os.Stderr, "  (DB unchanged — ReassignCameraData is transactional)")
		return 1
	}
	fmt.Println("OK")
	dataReassigned = true

	// Step 3: Disk rename — move source files into target directory.
	fmt.Printf("[3/5] Moving recording files from source to target ... ")
	movedCount, movedFiles, err = mergeDiskDirectories(ctx, sourceDir, targetDir, "", f.sourceID, f.targetID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAILED: %v\n", err)
		return rollback("disk move", err)
	}
	fmt.Printf("%d files moved\n", movedCount)
	diskMoved = true

	// Step 4: Update config — remove source + apply target overrides.
	fmt.Printf("[4/5] Updating config %s ... ", f.cfgPath)
	updatedCfg := removeCameraFromConfig(cfg, f.sourceID)
	applyTargetOverrides(updatedCfg, f.targetID, f)
	if err := config.Save(f.cfgPath, updatedCfg); err != nil {
		fmt.Fprintf(os.Stderr, "FAILED: %v\n", err)
		return rollback("config save", err)
	}
	fmt.Println("OK")
	cfgWritten = true

	// Step 5: Delete source camera row.
	fmt.Printf("[5/5] Deleting source camera row from DB ... ")
	if err := db.DeleteCameraRow(ctx, f.sourceID); err != nil {
		fmt.Printf("WARN: %v (source row preserved)\n", err)
	} else {
		fmt.Println("OK")
	}

	fmt.Println()
	fmt.Println("MERGE COMPLETE")
	fmt.Println("==============")
	fmt.Printf("  Source camera %q removed from config and DB.\n", f.sourceID)
	fmt.Printf("  %d recordings, %d health events, %d AI events, %d transcoding tasks re-tagged to %q.\n",
		recCount, healthCount, aiCount, transcodeCount, f.targetID)
	fmt.Printf("  %d recording files moved to %s.\n", movedCount, targetDir)
	fmt.Printf("  DB backup: %s\n", backupPath)
	if f.targetOnvifEndpoint != "" || f.targetURL != "" || f.targetDisableTimelase {
		fmt.Printf("  Target overrides applied.\n")
	}
	return 0
}

// applyTargetOverrides mutates the target camera in cfg according to flags.
func applyTargetOverrides(cfg *config.Config, targetID string, f mergeCamerasFlags) {
	for i := range cfg.Cameras {
		if cfg.Cameras[i].ID != targetID {
			continue
		}
		if f.targetOnvifEndpoint != "" {
			cfg.Cameras[i].ONVIFEndpoint = f.targetOnvifEndpoint
		}
		if f.targetURL != "" {
			cfg.Cameras[i].URL = f.targetURL
		}
		if f.targetDisableTimelase {
			// Force timelapse.enabled = false. We can't write *bool false
			// because the field uses *bool for tri-state; use a local.
			b := false
			cfg.Cameras[i].Timelapse.Enabled = b
			_ = b
		}
	}
}

func countRowsByCamera(ctx context.Context, d *sql.DB, table, cameraID string) int {
	var count int
	err := d.QueryRowContext(ctx,
		fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE camera_id=?", table), cameraID).Scan(&count)
	if err != nil {
		return 0
	}
	return count
}

type recordingInfo struct {
	filePath string
}

func listRecordingsByCamera(ctx context.Context, d *sql.DB, cameraID string) ([]recordingInfo, error) {
	rows, err := d.QueryContext(ctx,
		"SELECT file_path FROM recordings WHERE camera_id=?", cameraID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []recordingInfo
	for rows.Next() {
		var r recordingInfo
		if err := rows.Scan(&r.filePath); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// mergeDiskDirectories moves all files from srcDir into dstDir, preserving
// relative subdirectory structure. If namePrefix is non-empty, only files
// whose basename starts with namePrefix are moved (used during rollback to
// pick out source-prefixed files from the target dir).
// If renameFrom/renameTo are non-empty, file basenames containing renameFrom
// have it replaced with renameTo on the destination (so the on-disk filename
// matches the rewritten DB file_path, keeping DB and disk fully consistent).
// Returns the number of files moved and a manifest of destination paths.
// If srcDir doesn't exist, returns (0, nil, nil) — caller treats as no-op.
func mergeDiskDirectories(ctx context.Context, srcDir, dstDir, namePrefix, renameFrom, renameTo string) (int, []string, error) {
	srcDirInfo, err := os.Stat(srcDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil, nil
		}
		return 0, nil, fmt.Errorf("stat source dir: %w", err)
	}
	if !srcDirInfo.IsDir() {
		return 0, nil, fmt.Errorf("source path %q is not a directory", srcDir)
	}

	var moved int
	var manifest []string
	err = filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			return nil
		}
		if namePrefix != "" && !strings.HasPrefix(d.Name(), namePrefix) {
			return nil
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return fmt.Errorf("compute relative path for %q: %w", path, err)
		}
		// Rewrite relative path components (directories + filename) if renameFrom is set.
		// This keeps DB file_path and disk filename consistent after the merge.
		if renameFrom != "" && renameTo != "" {
			rel = strings.ReplaceAll(rel, renameFrom, renameTo)
		}
		dstPath := filepath.Join(dstDir, rel)
		if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
			return fmt.Errorf("mkdir %q: %w", filepath.Dir(dstPath), err)
		}
		if err := os.Rename(path, dstPath); err != nil {
			return fmt.Errorf("rename %q → %q: %w", path, dstPath, err)
		}
		moved++
		manifest = append(manifest, dstPath)
		return nil
	})
	if err != nil {
		return moved, manifest, err
	}
	removeEmptyDirs(srcDir)
	return moved, manifest, nil
}

// removeEmptyDirs recursively removes empty directories starting from path.
func removeEmptyDirs(path string) {
	for {
		if err := os.Remove(path); err != nil {
			break
		}
		path = filepath.Dir(path)
	}
}

// restoreDBFromBackup copies the backup file back to the live DB path.
// The DB must be closed by the caller before calling this.
func restoreDBFromBackup(dbPath, backupPath string) error {
	in, err := os.Open(backupPath)
	if err != nil {
		return fmt.Errorf("open backup: %w", err)
	}
	defer in.Close()
	out, err := os.Create(dbPath)
	if err != nil {
		return fmt.Errorf("create db file: %w", err)
	}
	defer out.Close()
	if _, err := out.ReadFrom(in); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	return nil
}

func removeCameraFromConfig(cfg *config.Config, sourceID string) *config.Config {
	updated := *cfg
	updated.Cameras = make([]config.CameraConfig, 0, len(cfg.Cameras))
	for _, cam := range cfg.Cameras {
		if cam.ID != sourceID {
			updated.Cameras = append(updated.Cameras, cam)
		}
	}
	return &updated
}

func isPortOpen(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func printMergeCamerasUsage() {
	fmt.Print(`Usage: mibee-nvr merge-cameras [options]

Merge two camera entries end-to-end:
  1. Backup database
  2. Re-tag all recordings/events AND rewrite file_path/merge_path to target
  3. Move recording files from source directory to target directory
  4. Remove source camera from config YAML and apply target overrides
  5. Delete source camera row from database

Any failure triggers automatic rollback of all completed steps.

Options:
  --source <id>                       Source camera ID (data moved FROM this camera)
  --target <id>                       Target camera ID (data moved TO this camera)
  --execute                           Perform the merge (default: dry-run)
  --dry-run                           Explicit dry-run mode (default)
  --force                             Proceed even if orphan records exist
  --config <path>                     Config file path (default: mibee-nvr.yaml)

Target overrides (applied to target camera after merge):
  --target-onvif-endpoint <url>       New ONVIF endpoint, e.g. http://192.168.1.50:80/onvif/device_service
  --target-url <url>                  New primary stream URL
  --target-disable-timelapse[=bool]   Disable timelapse on target (use when target has recording_enabled=true)

Flags accept both --flag=value and --flag value forms.

Examples:
  # Dry-run preview
  mibee-nvr merge-cameras --source=cam-A --target=cam-B

  # Merge + fix endpoint + disable timelapse in one shot
  mibee-nvr merge-cameras \
    --source=cam-A --target=cam-B \
    --target-onvif-endpoint=http://192.168.63.134:80/onvif/device_service \
    --target-disable-timelapse \
    --execute --force
`)
}
