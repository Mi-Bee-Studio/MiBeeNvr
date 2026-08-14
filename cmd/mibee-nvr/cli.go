package main

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	authmw "github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	_ "modernc.org/sqlite"
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
	case "cleanup":
		cmdCleanup()
	}
}

// cmdCleanup 录像清理工具，支持多种模式：
//
// 1. 按日期清理（删除 DB 行 + 文件 + 孤儿AI事件）:
//
//	mibee-nvr cleanup --before 2026-08-07
//
// 2. 清理孤儿文件（磁盘上有但 DB 里没记录的文件）:
//
//	mibee-nvr cleanup --orphans
//
// 3. 组合使用:
//
//	mibee-nvr cleanup --before 2026-08-07 --orphans
//
// 通用选项:
//
//	--config   配置文件路径（默认 mibee-nvr.yaml）
//	--dry-run  只统计不删除
func cmdCleanup() {
	var cfgPath, beforeDate string
	var orphans, dryRun bool
	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--config":
			i++
			if i < len(os.Args) {
				cfgPath = os.Args[i]
			}
		case "--before":
			i++
			if i < len(os.Args) {
				beforeDate = os.Args[i]
			}
		case "--orphans":
			orphans = true
		case "--dry-run":
			dryRun = true
		case "--help", "-h":
			fmt.Println(`cleanup — 录像清理工具

用法:
  mibee-nvr cleanup [选项]

选项:
  --before YYYY-MM-DD  删除此日期之前的录像（DB行 + 文件 + AI事件）
  --orphans            清理孤儿文件（磁盘有但DB无记录的视频文件）
  --config PATH        配置文件路径（默认 mibee-nvr.yaml）
  --dry-run            只统计不删除

示例:
  mibee-nvr cleanup --before 2026-08-07 --dry-run   # 预览清理7月数据
  mibee-nvr cleanup --before 2026-08-07              # 执行清理
  mibee-nvr cleanup --orphans --dry-run              # 预览孤儿文件
  mibee-nvr cleanup --orphans                         # 清理孤儿文件`)
			os.Exit(0)
		}
	}

	if cfgPath == "" {
		cfgPath = "mibee-nvr.yaml"
	}
	if beforeDate == "" && !orphans {
		fmt.Fprintln(os.Stderr, "Error: 需要指定 --before 或 --orphans")
		fmt.Fprintln(os.Stderr, "运行 mibee-nvr cleanup --help 查看用法")
		os.Exit(1)
	}

	// 加载配置获取存储根目录。
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config %s: %v\n", cfgPath, err)
		os.Exit(1)
	}
	storageRoot := cfg.Storage.RootDir
	dbPath := storageRoot + "/mibee-nvr.db"

	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening DB: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx := context.Background()
	dryRunSuffix := ""
	if dryRun {
		dryRunSuffix = " (--dry-run)"
	}

	// ── 模式 1: 按日期清理 ──
	if beforeDate != "" {
		fmt.Printf("=== 按日期清理 (before %s)%s ===\n", beforeDate, dryRunSuffix)
		cleanupByDate(ctx, db, storageRoot, beforeDate, dryRun)
	}

	// ── 模式 2: 清理孤儿文件 ──
	if orphans {
		fmt.Printf("\n=== 孤儿文件清理%s ===\n", dryRunSuffix)
		cleanupOrphanFiles(ctx, db, storageRoot, dryRun)
	}

	fmt.Println("\nCleanup complete.")
	os.Exit(0)
}

// cleanupByDate 删除指定日期之前的录像（文件 + DB 行 + AI 事件）。
func cleanupByDate(ctx context.Context, db *sql.DB, storageRoot, beforeDate string, dryRun bool) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, file_path, file_size FROM recordings WHERE file_path != '' AND started_at < ?`,
		beforeDate+" 00:00:00")
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Error querying: %v\n", err)
		return
	}
	type rec struct {
		id, path string
		size     int64
	}
	var recs []rec
	var totalSize int64
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.id, &r.path, &r.size); err != nil {
			continue
		}
		recs = append(recs, r)
		totalSize += r.size
	}
	rows.Close()

	fmt.Printf("  Found %d recordings (%.1f GB)\n", len(recs), float64(totalSize)/1e9)
	if dryRun || len(recs) == 0 {
		return
	}

	// 删除文件。
	var deletedFiles int
	var freedBytes int64
	for _, r := range recs {
		absPath := r.path
		if !filepath.IsAbs(absPath) {
			absPath = filepath.Join(storageRoot, r.path)
		}
		if err := os.Remove(absPath); err == nil {
			deletedFiles++
			freedBytes += r.size
		}
	}
	fmt.Printf("  Files: %d deleted, %.1f GB freed\n", deletedFiles, float64(freedBytes)/1e9)

	// 删除 DB 行。
	if result, err := db.ExecContext(ctx,
		`DELETE FROM recordings WHERE file_path != '' AND started_at < ?`,
		beforeDate+" 00:00:00"); err == nil {
		if n, _ := result.RowsAffected(); n > 0 {
			fmt.Printf("  DB: %d rows deleted\n", n)
		}
	}

	// 删除孤儿 AI 事件。
	if result, err := db.ExecContext(ctx,
		`DELETE FROM ai_events WHERE recording_id != '' AND recording_id NOT IN (SELECT id FROM recordings)`); err == nil {
		if n, _ := result.RowsAffected(); n > 0 {
			fmt.Printf("  AI events: %d orphaned rows deleted\n", n)
		}
	}
}

// cleanupOrphanFiles 扫描磁盘上的视频文件，删除 DB 中无记录的孤儿文件。
// 包括：录像目录 + periodic-merge 目录 + 临时文件。
func cleanupOrphanFiles(ctx context.Context, db *sql.DB, storageRoot string, dryRun bool) {
	// 加载所有 DB 中的 file_path 到 set。
	dbPaths := make(map[string]bool)
	rows, err := db.QueryContext(ctx, `SELECT file_path FROM recordings WHERE file_path != ''`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Error querying DB paths: %v\n", err)
		return
	}
	for rows.Next() {
		var p string
		rows.Scan(&p)
		// 标准化：转绝对路径。
		if !filepath.IsAbs(p) {
			p = filepath.Join(storageRoot, p)
		}
		dbPaths[filepath.Clean(p)] = true
	}
	rows.Close()
	fmt.Printf("  DB has %d recording file paths\n", len(dbPaths))

	// 扫描磁盘上的视频文件。
	videoExts := map[string]bool{".mp4": true, ".mkv": true, ".avi": true, ".dav": true, ".flv": true}
	var orphanFiles []string
	var orphanSize int64

	err = filepath.Walk(storageRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 跳过不可访问的目录
		}
		// 跳过 DB 文件和配置文件。
		if !info.IsDir() {
			ext := filepath.Ext(path)
			if !videoExts[ext] {
				return nil
			}
			cleanPath := filepath.Clean(path)
			if !dbPaths[cleanPath] {
				orphanFiles = append(orphanFiles, cleanPath)
				orphanSize += info.Size()
			}
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Error walking directory: %v\n", err)
		return
	}

	fmt.Printf("  Found %d orphan files (%.1f GB)\n", len(orphanFiles), float64(orphanSize)/1e9)

	if dryRun || len(orphanFiles) == 0 {
		// 显示前 10 个孤儿文件路径（帮助用户确认）。
		for i, p := range orphanFiles {
			if i >= 10 {
				fmt.Printf("  ... and %d more\n", len(orphanFiles)-10)
				break
			}
			fmt.Printf("    %s\n", p)
		}
		return
	}

	// 删除孤儿文件。
	var deleted int
	var freed int64
	for _, p := range orphanFiles {
		if info, err := os.Stat(p); err == nil {
			size := info.Size()
			if err := os.Remove(p); err == nil {
				deleted++
				freed += size
			}
		}
	}
	fmt.Printf("  Deleted %d orphan files, %.1f GB freed\n", deleted, float64(freed)/1e9)
}
