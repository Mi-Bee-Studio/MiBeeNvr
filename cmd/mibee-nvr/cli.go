package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
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
		if dir := config.DockerDataDir(); dir != "" {
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
	if err := runHealth(addr); err != nil {
		fmt.Fprintf(os.Stderr, "Health check failed: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// runHealth probes the NVR health endpoint once and returns the failure.
func runHealth(addr string) error {
	resp, err := http.Get("http://localhost" + addr + "/api/health")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func cmdInit() {
	opts := parseInitArgs(os.Args)

	if opts.password == "" {
		stat, _ := os.Stdin.Stat()
		if (stat.Mode() & os.ModeCharDevice) != 0 {
			fmt.Print("Enter password: ")
			scanner := bufio.NewScanner(os.Stdin)
			if scanner.Scan() {
				opts.password = scanner.Text()
			}
		}
		if opts.password == "" {
			fmt.Fprintln(os.Stderr, "Error: password is required (use --password or provide via terminal)")
			os.Exit(1)
		}
	}
	if err := runInit(opts, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		if errors.Is(err, errInitConfigExists) {
			os.Exit(2)
		}
		os.Exit(1)
	}
	os.Exit(0)
}

// initOptions carries the parsed `init` subcommand flags.
type initOptions struct {
	password   string
	dataDir    string
	listenAddr string
	cfgPath    string
	username   string
	force      bool
}

// parseInitArgs reads the init flags from a full argv (flags start at [2]).
func parseInitArgs(args []string) initOptions {
	var opts initOptions
	for i := 2; i < len(args); i++ {
		switch args[i] {
		case "--password":
			i++
			if i < len(args) {
				opts.password = args[i]
			}
		case "--data-dir":
			i++
			if i < len(args) {
				opts.dataDir = args[i]
			}
		case "--listen":
			i++
			if i < len(args) {
				opts.listenAddr = args[i]
			}
		case "--config":
			i++
			if i < len(args) {
				opts.cfgPath = args[i]
			}
		case "--username":
			i++
			if i < len(args) {
				opts.username = args[i]
			}
		case "--force":
			opts.force = true
		}
	}
	if opts.dataDir == "" {
		opts.dataDir = "/var/lib/mibee-nvr"
	}
	if opts.listenAddr == "" {
		opts.listenAddr = ":9090"
	}
	if opts.cfgPath == "" {
		opts.cfgPath = "mibee-nvr.yaml"
	}
	if opts.username == "" {
		opts.username = "admin"
	}
	return opts
}

// errInitConfigExists distinguishes the existing-config refusal — the CLI
// contract (tests/cli_test.go) maps it to exit code 2.
var errInitConfigExists = errors.New("config file already exists")

// runInit validates the password and writes the initial config file.
func runInit(opts initOptions, stdout io.Writer) error {
	if len(opts.password) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}
	if _, err := os.Stat(opts.cfgPath); err == nil && !opts.force {
		return fmt.Errorf("%w: %s (use --force to overwrite)", errInitConfigExists, opts.cfgPath)
	}
	if err := os.MkdirAll(opts.dataDir, 0o755); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}
	hash, err := authmw.HashPassword(opts.password)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}
	cfg := config.Config{
		Server:        config.ServerConfig{Listen: opts.listenAddr},
		Storage:       config.StorageConfig{RootDir: opts.dataDir, SegmentDuration: "30s"},
		Auth:          config.AuthConfig{Username: opts.username, PasswordHash: hash},
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
	if err := config.Save(opts.cfgPath, &cfg); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}
	fmt.Fprintf(stdout, "Configuration saved to %s\n", opts.cfgPath)
	fmt.Fprintf(stdout, "Data directory: %s\n", opts.dataDir)
	_, _ = fmt.Fprintln(stdout, "\nNext steps:")
	fmt.Fprintf(stdout, "  1. Edit %s to add your cameras\n", opts.cfgPath)
	fmt.Fprintf(stdout, "  2. Run: ./mibee-nvr -config %s\n", opts.cfgPath)
	fmt.Fprintf(stdout, "  3. Open http://localhost%s in your browser\n", opts.listenAddr)
	return nil
}

func cmdHashPassword() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: mibee-nvr hash-password <password>")
		os.Exit(1)
	}
	hash, err := runHashPassword(os.Args[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(hash)
	os.Exit(0)
}

// runHashPassword hashes one password (bcrypt), verifying it round-trips.
func runHashPassword(password string) (string, error) {
	return authmw.HashPassword(password)
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
	if err := runEncryptConfig(cfgPath, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// runEncryptConfig encrypts the sensitive fields of a config file in place and
// reports what was encrypted to stdout.
func runEncryptConfig(cfgPath string, stdout io.Writer) error {
	fields, err := config.EncryptConfigFile(cfgPath)
	if err != nil {
		return err
	}
	if len(fields) == 0 {
		_, _ = fmt.Fprintln(stdout, "No plaintext sensitive fields found. All fields are already encrypted or empty.")
	} else {
		fmt.Fprintf(stdout, "Encrypted %d sensitive field(s) in %s:\n", len(fields), cfgPath)
		for _, f := range fields {
			fmt.Fprintf(stdout, "  - %s\n", f)
		}
	}
	return nil
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
	opts, help := parseCleanupArgs(os.Args)
	if help {
		os.Exit(0)
	}
	if err := runCleanup(opts); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// cleanupOptions carries the parsed `cleanup` subcommand flags.
type cleanupOptions struct {
	cfgPath    string
	beforeDate string
	orphans    bool
	dryRun     bool
}

const cleanupUsage = `cleanup — 录像清理工具

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
  mibee-nvr cleanup --orphans                         # 清理孤儿文件`

// parseCleanupArgs reads the cleanup flags from a full argv. The boolean
// result reports whether help was requested (caller prints and exits 0).
func parseCleanupArgs(args []string) (cleanupOptions, bool) {
	var opts cleanupOptions
	for i := 2; i < len(args); i++ {
		switch args[i] {
		case "--config":
			i++
			if i < len(args) {
				opts.cfgPath = args[i]
			}
		case "--before":
			i++
			if i < len(args) {
				opts.beforeDate = args[i]
			}
		case "--orphans":
			opts.orphans = true
		case "--dry-run":
			opts.dryRun = true
		case "--help", "-h":
			fmt.Println(cleanupUsage)
			return opts, true
		}
	}
	if opts.cfgPath == "" {
		opts.cfgPath = "mibee-nvr.yaml"
	}
	return opts, false
}

// runCleanup executes the cleanup modes against the configured storage root.
func runCleanup(opts cleanupOptions) error {
	if opts.beforeDate == "" && !opts.orphans {
		return fmt.Errorf("需要指定 --before 或 --orphans（运行 mibee-nvr cleanup --help 查看用法）")
	}

	// 加载配置获取存储根目录。
	cfg, err := config.Load(opts.cfgPath)
	if err != nil {
		return fmt.Errorf("loading config %s: %w", opts.cfgPath, err)
	}
	storageRoot := cfg.Storage.RootDir
	dbPath := storageRoot + "/mibee-nvr.db"

	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("opening DB: %w", err)
	}
	defer db.Close()

	ctx := context.Background()
	dryRunSuffix := ""
	if opts.dryRun {
		dryRunSuffix = " (--dry-run)"
	}

	// ── 模式 1: 按日期清理 ──
	if opts.beforeDate != "" {
		fmt.Printf("=== 按日期清理 (before %s)%s ===\n", opts.beforeDate, dryRunSuffix)
		cleanupByDate(ctx, db, storageRoot, opts.beforeDate, opts.dryRun)
	}

	// ── 模式 2: 清理孤儿文件 ──
	if opts.orphans {
		fmt.Printf("\n=== 孤儿文件清理%s ===\n", dryRunSuffix)
		cleanupOrphanFiles(ctx, db, storageRoot, opts.dryRun)
	}

	fmt.Println("\nCleanup complete.")
	return nil
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
	defer rows.Close()
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
	if err := rows.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "  Warning: iterate recordings: %v\n", err)
	}

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
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			continue
		}
		// 标准化：转绝对路径。
		if !filepath.IsAbs(p) {
			p = filepath.Join(storageRoot, p)
		}
		dbPaths[filepath.Clean(p)] = true
	}
	if err := rows.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "  Warning: iterate DB paths: %v\n", err)
	}
	fmt.Printf("  DB has %d recording file paths\n", len(dbPaths))

	// 扫描磁盘上的视频文件。
	videoExts := map[string]bool{".mp4": true, ".mkv": true, ".avi": true, ".dav": true, ".flv": true}
	var orphanFiles []string
	var orphanSize int64

	err = filepath.Walk(storageRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// 跳过不可访问的目录(权限/断链等),继续扫描其余部分。
			return nil //nolint:nilerr // 有意忽略子树错误,Walk 继续遍历
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
