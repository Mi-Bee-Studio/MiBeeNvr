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
//
// Each subcommand is a thin shell over a (parse*, run*) pair: parsing is a
// pure function of args, run* does the work with injected writers and
// returns an error instead of exiting — the shape the direct tests target.
// The shells own os.Args, the terminal check, and process exit codes.
// ---------------------------------------------------------------------------

// errInitConfigExists marks the init "config exists without --force"
// failure, which historically exits with code 2.
var errInitConfigExists = errors.New("config file already exists")

// errCleanupHelp marks the cleanup --help path, which prints usage and
// exits 0.
var errCleanupHelp = errors.New("cleanup help printed")

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
	if err := runHealth(os.Args, os.Stdout, os.Stderr); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// runHealth probes /api/health at the resolved address. User-facing failure
// lines are written to stderr exactly as the CLI always printed them.
func runHealth(args []string, _, stderr io.Writer) error {
	addr, err := resolveHealthAddr(args)
	if err != nil {
		fmt.Fprintf(stderr, "Error loading config: %v\n", err)

		return err
	}

	resp, err := http.Get("http://localhost" + addr + "/api/health")
	if err != nil {
		fmt.Fprintf(stderr, "Health check failed: %v\n", err)

		return err
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		fmt.Fprintf(stderr, "Health check failed: HTTP %d\n", resp.StatusCode)

		return fmt.Errorf("health check failed: HTTP %d", resp.StatusCode)
	}
	resp.Body.Close()

	return nil
}

// initOptions carries the parsed init flags (defaults applied).
type initOptions struct {
	password   string
	dataDir    string
	listenAddr string
	cfgPath    string
	username   string
	force      bool
}

// parseInitArgs extracts init flags from args (the full os.Args shape,
// flags starting at index 2) and applies the defaults.
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

func cmdInit() {
	opts := parseInitArgs(os.Args)
	err := runInit(opts, os.Stdin, stdinIsTerminal(), os.Stdout, os.Stderr)
	switch {
	case err == nil:
		os.Exit(0)
	case errors.Is(err, errInitConfigExists):
		os.Exit(2)
	default:
		os.Exit(1)
	}
}

// stdinIsTerminal reports whether stdin is an interactive terminal (the
// condition the password prompt requires).
func stdinIsTerminal() bool {
	stat, err := os.Stdin.Stat()

	return err == nil && (stat.Mode()&os.ModeCharDevice) != 0
}

// runInit writes the initial configuration. The password prompt reads
// stdin only when interactive (a terminal); piped stdin never prompts.
func runInit(opts initOptions, stdin io.Reader, interactive bool, stdout, stderr io.Writer) error {
	password := opts.password
	if password == "" && interactive {
		fmt.Fprint(stdout, "Enter password: ")
		scanner := bufio.NewScanner(stdin)
		if scanner.Scan() {
			password = scanner.Text()
		}
	}
	if password == "" {
		fmt.Fprintln(stderr, "Error: password is required (use --password or provide via terminal)")

		return errors.New("password is required")
	}
	if len(password) < 8 {
		fmt.Fprintln(stderr, "Error: password must be at least 8 characters")

		return errors.New("password too short")
	}
	if _, err := os.Stat(opts.cfgPath); err == nil && !opts.force {
		fmt.Fprintf(stderr, "Error: config file %s already exists (use --force to overwrite)\n", opts.cfgPath)

		return fmt.Errorf("%w: %s", errInitConfigExists, opts.cfgPath)
	}
	if err := os.MkdirAll(opts.dataDir, 0o755); err != nil {
		fmt.Fprintf(stderr, "Error creating data directory: %v\n", err)

		return fmt.Errorf("create data directory: %w", err)
	}
	hash, err := authmw.HashPassword(password)
	if err != nil {
		fmt.Fprintf(stderr, "Error hashing password: %v\n", err)

		return fmt.Errorf("hash password: %w", err)
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
		fmt.Fprintf(stderr, "Error saving config: %v\n", err)

		return fmt.Errorf("save config: %w", err)
	}
	fmt.Fprintf(stdout, "Configuration saved to %s\n", opts.cfgPath)
	fmt.Fprintf(stdout, "Data directory: %s\n", opts.dataDir)
	fmt.Fprintln(stdout, "\nNext steps:")
	fmt.Fprintf(stdout, "  1. Edit %s to add your cameras\n", opts.cfgPath)
	fmt.Fprintf(stdout, "  2. Run: ./mibee-nvr -config %s\n", opts.cfgPath)
	fmt.Fprintf(stdout, "  3. Open http://localhost%s in your browser\n", opts.listenAddr)

	return nil
}

func cmdHashPassword() {
	if err := runHashPassword(os.Args, os.Stdout, os.Stderr); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// runHashPassword prints the bcrypt hash for args[2].
func runHashPassword(args []string, stdout, stderr io.Writer) error {
	if len(args) < 3 {
		fmt.Fprintln(stderr, "Usage: mibee-nvr hash-password <password>")

		return errors.New("missing password argument")
	}
	hash, err := authmw.HashPassword(args[2])
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)

		return fmt.Errorf("hash password: %w", err)
	}
	fmt.Fprintln(stdout, hash)

	return nil
}

func cmdEncryptConfig() {
	if err := runEncryptConfig(os.Args, os.Stdout, os.Stderr); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// runEncryptConfig encrypts the plaintext sensitive fields of a config
// file in place.
func runEncryptConfig(args []string, stdout, stderr io.Writer) error {
	cfgPath := "mibee-nvr.yaml"
	for i := 2; i < len(args); i++ {
		if args[i] == "--config" {
			i++
			if i < len(args) {
				cfgPath = args[i]
			}
		}
	}
	fields, err := config.EncryptConfigFile(cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)

		return err
	}
	if len(fields) == 0 {
		fmt.Fprintln(stdout, "No plaintext sensitive fields found. All fields are already encrypted or empty.")
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

// cleanupOptions carries the parsed cleanup flags (config default applied).
type cleanupOptions struct {
	cfgPath    string
	beforeDate string
	orphans    bool
	dryRun     bool
}

// parseCleanupArgs extracts cleanup flags from args (full os.Args shape).
// --help prints usage to stdout and returns errCleanupHelp.
func parseCleanupArgs(args []string, stdout io.Writer) (cleanupOptions, error) {
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
			fmt.Fprintln(stdout, `cleanup — 录像清理工具

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

			return opts, errCleanupHelp
		}
	}
	if opts.cfgPath == "" {
		opts.cfgPath = "mibee-nvr.yaml"
	}

	return opts, nil
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
	opts, err := parseCleanupArgs(os.Args, os.Stdout)
	if err != nil {
		if errors.Is(err, errCleanupHelp) {
			os.Exit(0)
		}
		os.Exit(1)
	}
	if err := runCleanup(opts, os.Stdout, os.Stderr); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// runCleanup loads the config, opens the recordings DB, and executes the
// selected cleanup modes.
func runCleanup(opts cleanupOptions, stdout, stderr io.Writer) error {
	if opts.beforeDate == "" && !opts.orphans {
		fmt.Fprintln(stderr, "Error: 需要指定 --before 或 --orphans")
		fmt.Fprintln(stderr, "运行 mibee-nvr cleanup --help 查看用法")

		return errors.New("no cleanup mode specified")
	}

	// 加载配置获取存储根目录。
	cfg, err := config.Load(opts.cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "Error loading config %s: %v\n", opts.cfgPath, err)

		return fmt.Errorf("load config: %w", err)
	}
	storageRoot := cfg.Storage.RootDir
	dbPath := storageRoot + "/mibee-nvr.db"

	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		fmt.Fprintf(stderr, "Error opening DB: %v\n", err)

		return fmt.Errorf("open DB: %w", err)
	}

	ctx := context.Background()
	dryRunSuffix := ""
	if opts.dryRun {
		dryRunSuffix = " (--dry-run)"
	}

	// ── 模式 1: 按日期清理 ──
	if opts.beforeDate != "" {
		fmt.Fprintf(stdout, "=== 按日期清理 (before %s)%s ===\n", opts.beforeDate, dryRunSuffix)
		cleanupByDate(ctx, db, storageRoot, opts.beforeDate, opts.dryRun, stdout, stderr)
	}

	// ── 模式 2: 清理孤儿文件 ──
	if opts.orphans {
		fmt.Fprintf(stdout, "\n=== 孤儿文件清理%s ===\n", dryRunSuffix)
		cleanupOrphanFiles(ctx, db, storageRoot, opts.dryRun, stdout, stderr)
	}

	fmt.Fprintln(stdout, "\nCleanup complete.")
	db.Close()

	return nil
}

// cleanupByDate 删除指定日期之前的录像（文件 + DB 行 + AI 事件）。
func cleanupByDate(ctx context.Context, db *sql.DB, storageRoot, beforeDate string, dryRun bool, stdout, stderr io.Writer) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, file_path, file_size FROM recordings WHERE file_path != '' AND started_at < ?`,
		beforeDate+" 00:00:00")
	if err != nil {
		fmt.Fprintf(stderr, "  Error querying: %v\n", err)
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
		fmt.Fprintf(stderr, "  Warning: iterate recordings: %v\n", err)
	}

	fmt.Fprintf(stdout, "  Found %d recordings (%.1f GB)\n", len(recs), float64(totalSize)/1e9)
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
	fmt.Fprintf(stdout, "  Files: %d deleted, %.1f GB freed\n", deletedFiles, float64(freedBytes)/1e9)

	// 删除 DB 行。
	if result, err := db.ExecContext(ctx,
		`DELETE FROM recordings WHERE file_path != '' AND started_at < ?`,
		beforeDate+" 00:00:00"); err == nil {
		if n, _ := result.RowsAffected(); n > 0 {
			fmt.Fprintf(stdout, "  DB: %d rows deleted\n", n)
		}
	}

	// 删除孤儿 AI 事件。
	if result, err := db.ExecContext(ctx,
		`DELETE FROM ai_events WHERE recording_id != '' AND recording_id NOT IN (SELECT id FROM recordings)`); err == nil {
		if n, _ := result.RowsAffected(); n > 0 {
			fmt.Fprintf(stdout, "  AI events: %d orphaned rows deleted\n", n)
		}
	}
}

// cleanupOrphanFiles 扫描磁盘上的视频文件，删除 DB 中无记录的孤儿文件。
// 包括：录像目录 + periodic-merge 目录 + 临时文件。
func cleanupOrphanFiles(ctx context.Context, db *sql.DB, storageRoot string, dryRun bool, stdout, stderr io.Writer) {
	// 加载所有 DB 中的 file_path 到 set。
	dbPaths := make(map[string]bool)
	rows, err := db.QueryContext(ctx, `SELECT file_path FROM recordings WHERE file_path != ''`)
	if err != nil {
		fmt.Fprintf(stderr, "  Error querying DB paths: %v\n", err)
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
		fmt.Fprintf(stderr, "  Warning: iterate DB paths: %v\n", err)
	}
	fmt.Fprintf(stdout, "  DB has %d recording file paths\n", len(dbPaths))

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
		fmt.Fprintf(stderr, "  Error walking directory: %v\n", err)
		return
	}

	fmt.Fprintf(stdout, "  Found %d orphan files (%.1f GB)\n", len(orphanFiles), float64(orphanSize)/1e9)

	if dryRun || len(orphanFiles) == 0 {
		// 显示前 10 个孤儿文件路径（帮助用户确认）。
		for i, p := range orphanFiles {
			if i >= 10 {
				fmt.Fprintf(stdout, "  ... and %d more\n", len(orphanFiles)-10)
				break
			}
			fmt.Fprintf(stdout, "    %s\n", p)
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
	fmt.Fprintf(stdout, "  Deleted %d orphan files, %.1f GB freed\n", deleted, float64(freed)/1e9)
}
