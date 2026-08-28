package main

// Direct tests for the CLI subcommand cores (issue #589): the parse*/run*
// split keeps the logic testable without exec-ing the binary — parsing is
// pure, run* takes injected writers and returns errors.

import (
	"bytes"
	"database/sql"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	authmw "github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
)

// --- parseInitArgs ---

func TestParseInitArgsDefaults(t *testing.T) {
	opts := parseInitArgs([]string{"mibee-nvr", "init"})

	if opts.dataDir != "/var/lib/mibee-nvr" {
		t.Errorf("dataDir default = %q", opts.dataDir)
	}
	if opts.listenAddr != ":9090" {
		t.Errorf("listenAddr default = %q", opts.listenAddr)
	}
	if opts.cfgPath != "mibee-nvr.yaml" {
		t.Errorf("cfgPath default = %q", opts.cfgPath)
	}
	if opts.username != "admin" {
		t.Errorf("username default = %q", opts.username)
	}
	if opts.force {
		t.Error("force default must be false")
	}
}

func TestParseInitArgsAllFlags(t *testing.T) {
	opts := parseInitArgs([]string{
		"mibee-nvr", "init",
		"--password", "hunter2hunter2",
		"--data-dir", "/srv/nvr",
		"--listen", ":9191",
		"--config", "/etc/nvr.yaml",
		"--username", "root",
		"--force",
	})

	if opts.password != "hunter2hunter2" || opts.dataDir != "/srv/nvr" ||
		opts.listenAddr != ":9191" || opts.cfgPath != "/etc/nvr.yaml" ||
		opts.username != "root" || !opts.force {
		t.Errorf("flags not parsed: %+v", opts)
	}
}

// --- runInit ---

func runInitForTest(t *testing.T, args []string, stdin string, interactive bool) (initOptions, *bytes.Buffer, *bytes.Buffer, error) {
	t.Helper()

	var stdout, stderr bytes.Buffer
	opts := parseInitArgs(args)

	dir := t.TempDir()
	if opts.dataDir == "/var/lib/mibee-nvr" {
		opts.dataDir = filepath.Join(dir, "data")
	}
	if opts.cfgPath == "mibee-nvr.yaml" {
		opts.cfgPath = filepath.Join(dir, "mibee-nvr.yaml")
	}

	err := runInit(opts, strings.NewReader(stdin), interactive, &stdout, &stderr)

	return opts, &stdout, &stderr, err
}

func TestRunInitWritesConfigAndHash(t *testing.T) {
	opts, stdout, _, err := runInitForTest(t,
		[]string{"mibee-nvr", "init", "--password", "correct-horse"}, "", false)
	if err != nil {
		t.Fatalf("runInit: %v", err)
	}

	if info, statErr := os.Stat(opts.dataDir); statErr != nil || !info.IsDir() {
		t.Errorf("data dir not created: %v", statErr)
	}

	cfg, err := config.Load(opts.cfgPath)
	if err != nil {
		t.Fatalf("reload written config: %v", err)
	}

	if cfg.Auth.Username != "admin" || cfg.Auth.PasswordHash == "" {
		t.Errorf("auth block wrong: %+v", cfg.Auth)
	}

	if !authmw.CheckPassword("correct-horse", cfg.Auth.PasswordHash) {
		t.Error("stored hash does not verify")
	}

	if !strings.Contains(stdout.String(), "Configuration saved to ") {
		t.Errorf("success output missing: %q", stdout.String())
	}
}

func TestRunInitShortPasswordRejected(t *testing.T) {
	_, _, stderr, err := runInitForTest(t,
		[]string{"mibee-nvr", "init", "--password", "short"}, "", false)
	if err == nil {
		t.Fatal("short password accepted")
	}

	if !strings.Contains(stderr.String(), "at least 8 characters") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunInitRequiresPasswordNonInteractive(t *testing.T) {
	_, _, stderr, err := runInitForTest(t,
		[]string{"mibee-nvr", "init"}, "", false)
	if err == nil {
		t.Fatal("missing password accepted")
	}

	if !strings.Contains(stderr.String(), "password is required") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunInitPromptPathReadsStdin(t *testing.T) {
	_, stdout, _, err := runInitForTest(t,
		[]string{"mibee-nvr", "init"}, "prompted-password", true)
	if err != nil {
		t.Fatalf("runInit via prompt: %v", err)
	}

	if !strings.Contains(stdout.String(), "Enter password: ") {
		t.Errorf("prompt not shown: %q", stdout.String())
	}
}

func TestRunInitExistingConfigNeedsForce(t *testing.T) {
	opts, _, _, err := runInitForTest(t,
		[]string{"mibee-nvr", "init", "--password", "correct-horse"}, "", false)
	if err != nil {
		t.Fatalf("first init: %v", err)
	}

	var stdout, stderr bytes.Buffer
	second := initOptions{
		password: "correct-horse",
		dataDir:  opts.dataDir,
		cfgPath:  opts.cfgPath,
		username: "admin",
	}

	err = runInit(second, strings.NewReader(""), false, &stdout, &stderr)
	if err == nil {
		t.Fatal("second init without --force accepted")
	}

	if !strings.Contains(err.Error(), errInitConfigExists.Error()) {
		t.Errorf("err = %v, want errInitConfigExists", err)
	}

	second.force = true
	if err := runInit(second, strings.NewReader(""), false, &stdout, &stderr); err != nil {
		t.Fatalf("second init with --force: %v", err)
	}
}

// --- runHealth ---

func TestRunHealthOK(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer

	err := runHealth([]string{"mibee-nvr", "health", "--addr", portOfURL(t, ts.URL)}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runHealth: %v (stderr %q)", err, stderr.String())
	}
}

func TestRunHealthNon200(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer

	err := runHealth([]string{"mibee-nvr", "health", "--addr", portOfURL(t, ts.URL)}, &stdout, &stderr)
	if err == nil {
		t.Fatal("503 accepted as healthy")
	}

	if !strings.Contains(stderr.String(), "HTTP 503") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunHealthUnreachable(t *testing.T) {
	var stdout, stderr bytes.Buffer

	// Port 1 is not a listening service. (A sandbox loopback proxy may
	// intercept the dial; in that case the response is still not 200 and
	// runHealth errors either way — only the error itself is asserted.)
	if err := runHealth([]string{"mibee-nvr", "health", "--addr", ":1"}, &stdout, &stderr); err == nil {
		t.Fatal("unreachable address accepted as healthy")
	}
}

func portOfURL(t *testing.T, rawURL string) string {
	t.Helper()

	_, port, err := net.SplitHostPort(strings.TrimPrefix(rawURL, "http://"))
	if err != nil {
		t.Fatalf("parse port of %q: %v", rawURL, err)
	}

	return ":" + port
}

// --- runHashPassword ---

func TestRunHashPasswordRoundtrip(t *testing.T) {
	var stdout, stderr bytes.Buffer

	err := runHashPassword([]string{"mibee-nvr", "hash-password", "s3cret-pass"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runHashPassword: %v", err)
	}

	hash := strings.TrimSpace(stdout.String())
	if !strings.HasPrefix(hash, "$2") {
		t.Fatalf("output not a bcrypt hash: %q", hash)
	}

	if !authmw.CheckPassword("s3cret-pass", hash) {
		t.Error("hash does not verify")
	}
}

func TestRunHashPasswordUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer

	err := runHashPassword([]string{"mibee-nvr", "hash-password"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("missing argument accepted")
	}

	if !strings.Contains(stderr.String(), "Usage: mibee-nvr hash-password") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

// --- runEncryptConfig ---

func TestRunEncryptConfig(t *testing.T) {
	t.Setenv("NVR_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mibee-nvr.yaml")

	writeConfigForEncrypt(t, cfgPath, "plain-pass")

	var stdout, stderr bytes.Buffer

	err := runEncryptConfig([]string{"mibee-nvr", "encrypt-config", "--config", cfgPath}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runEncryptConfig: %v (stderr %q)", err, stderr.String())
	}

	if !strings.Contains(stdout.String(), "auth.password") {
		t.Errorf("encrypted-field list missing auth.password: %q", stdout.String())
	}

	// config.Load decrypts transparently, so plaintext removal is
	// asserted against the raw file bytes.
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read encrypted config: %v", err)
	}

	if bytes.Contains(raw, []byte("plain-pass")) {
		t.Fatal("plaintext password survived encryption")
	}

	// Second run: config.Load decrypts transparently when the key is
	// set, so the walker sees decrypted plaintext and re-reports the
	// field every run (values stay encrypted at rest — harmless repeat,
	// pinned here as the byte-for-byte pre-refactor behavior).
	stdout.Reset()
	if err := runEncryptConfig([]string{"mibee-nvr", "encrypt-config", "--config", cfgPath}, &stdout, &stderr); err != nil {
		t.Fatalf("second run: %v", err)
	}

	if !strings.Contains(stdout.String(), "auth.password") {
		t.Errorf("second run output changed: %q", stdout.String())
	}

	raw2, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("re-read config: %v", err)
	}

	if bytes.Contains(raw2, []byte("plain-pass")) {
		t.Error("plaintext leaked back into the file on the second run")
	}
}

func writeConfigForEncrypt(t *testing.T, path, password string) {
	t.Helper()

	content := "auth:\n  username: admin\n  password: " + password + "\nserver:\n  listen: :9090\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// --- parseCleanupArgs ---

func TestParseCleanupArgs(t *testing.T) {
	var stdout bytes.Buffer

	opts, err := parseCleanupArgs([]string{"mibee-nvr", "cleanup"}, &stdout)
	if err != nil {
		t.Fatalf("bare parse: %v", err)
	}

	if opts.cfgPath != "mibee-nvr.yaml" || opts.beforeDate != "" || opts.orphans || opts.dryRun {
		t.Errorf("defaults wrong: %+v", opts)
	}

	opts, err = parseCleanupArgs([]string{
		"mibee-nvr", "cleanup",
		"--before", "2026-08-07",
		"--config", "/tmp/x.yaml",
		"--orphans", "--dry-run",
	}, &stdout)
	if err != nil {
		t.Fatalf("flag parse: %v", err)
	}

	if opts.beforeDate != "2026-08-07" || opts.cfgPath != "/tmp/x.yaml" || !opts.orphans || !opts.dryRun {
		t.Errorf("flags wrong: %+v", opts)
	}
}

func TestParseCleanupArgsHelp(t *testing.T) {
	var stdout bytes.Buffer

	_, err := parseCleanupArgs([]string{"mibee-nvr", "cleanup", "--help"}, &stdout)
	if err == nil || !strings.Contains(err.Error(), errCleanupHelp.Error()) {
		t.Fatalf("err = %v, want errCleanupHelp", err)
	}

	if !strings.Contains(stdout.String(), "--before YYYY-MM-DD") {
		t.Errorf("help text missing: %q", stdout.String())
	}
}

// --- runCleanup ---

func TestRunCleanupNoMode(t *testing.T) {
	var stdout, stderr bytes.Buffer

	err := runCleanup(cleanupOptions{cfgPath: "unused.yaml"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("mode-less cleanup accepted")
	}

	if !strings.Contains(stderr.String(), "--before 或 --orphans") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunCleanupBadConfig(t *testing.T) {
	var stdout, stderr bytes.Buffer

	err := runCleanup(cleanupOptions{beforeDate: "2026-08-07", cfgPath: filepath.Join(t.TempDir(), "missing.yaml")}, &stdout, &stderr)
	if err == nil {
		t.Fatal("missing config accepted")
	}
}

func TestRunCleanupDryRunEmptyDB(t *testing.T) {
	dir := t.TempDir()
	openCleanupDB(t, dir) // schema only — an empty recordings DB

	var stdout, stderr bytes.Buffer

	cfgPath := filepath.Join(dir, "mibee-nvr.yaml")
	writeConfigForCleanup(t, cfgPath, dir)

	err := runCleanup(cleanupOptions{beforeDate: "2026-08-07", cfgPath: cfgPath, dryRun: true}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runCleanup: %v (stderr %q)", err, stderr.String())
	}

	if !strings.Contains(stdout.String(), "Found 0 recordings") {
		t.Errorf("dry-run output missing: %q", stdout.String())
	}
}

func writeConfigForCleanup(t *testing.T, path, rootDir string) {
	t.Helper()

	content := "storage:\n  root_dir: " + rootDir + "\nserver:\n  listen: :9090\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// openCleanupDB opens a sqlite DB under rootDir with the minimal
// recordings/ai_events schema the cleanup SQL touches (no rows).
func openCleanupDB(t *testing.T, rootDir string) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", filepath.Join(rootDir, "mibee-nvr.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, stmt := range []string{
		`CREATE TABLE recordings (id TEXT PRIMARY KEY, file_path TEXT NOT NULL DEFAULT '', file_size INTEGER NOT NULL DEFAULT 0, started_at TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE ai_events (id INTEGER PRIMARY KEY AUTOINCREMENT, recording_id TEXT NOT NULL DEFAULT '')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("create schema: %v", err)
		}
	}

	return db
}

// writeCleanupDB seeds the cleanup schema with an old and a new
// recording (real files on disk) plus an orphan video file.
func writeCleanupDB(t *testing.T, rootDir string) *sql.DB {
	t.Helper()

	db := openCleanupDB(t, rootDir)

	oldFile := filepath.Join(rootDir, "old.mp4")
	newFile := filepath.Join(rootDir, "new.mp4")
	orphanFile := filepath.Join(rootDir, "orphan.mp4")
	for _, f := range []string{oldFile, newFile, orphanFile} {
		if err := os.WriteFile(f, bytes.Repeat([]byte("x"), 1024), 0o600); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	exec(`INSERT INTO recordings (id, file_path, file_size, started_at) VALUES ('rec-old', ?, 1024, '2026-08-01 10:00:00')`, oldFile)
	exec(`INSERT INTO recordings (id, file_path, file_size, started_at) VALUES ('rec-new', ?, 1024, '2026-08-20 10:00:00')`, newFile)
	exec(`INSERT INTO ai_events (recording_id) VALUES ('rec-old')`)

	return db
}

// --- cleanupByDate / cleanupOrphanFiles (real SQLite) ---

func TestCleanupByDateDryRunDeletesNothing(t *testing.T) {
	dir := t.TempDir()
	db := writeCleanupDB(t, dir)

	var stdout, stderr bytes.Buffer

	cleanupByDate(t.Context(), db, dir, "2026-08-07", true, &stdout, &stderr)

	if _, err := os.Stat(filepath.Join(dir, "old.mp4")); err != nil {
		t.Fatalf("dry-run deleted the old file: %v", err)
	}

	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recordings`).Scan(&rows); err != nil || rows != 2 {
		t.Errorf("dry-run must keep DB rows, got %d (err %v)", rows, err)
	}
}

func TestCleanupByDateDeletesOldKeepsNew(t *testing.T) {
	dir := t.TempDir()
	db := writeCleanupDB(t, dir)

	var stdout, stderr bytes.Buffer

	cleanupByDate(t.Context(), db, dir, "2026-08-07", false, &stdout, &stderr)

	if _, err := os.Stat(filepath.Join(dir, "old.mp4")); !os.IsNotExist(err) {
		t.Errorf("old file still on disk: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "new.mp4")); err != nil {
		t.Fatalf("new file removed: %v", err)
	}

	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recordings WHERE id='rec-old'`).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("old DB row survived: %d (err %v)", rows, err)
	}

	if err := db.QueryRow(`SELECT COUNT(*) FROM recordings WHERE id='rec-new'`).Scan(&rows); err != nil || rows != 1 {
		t.Errorf("new DB row lost: %d (err %v)", rows, err)
	}

	var events int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_events`).Scan(&events); err != nil || events != 0 {
		t.Errorf("orphaned AI event survived: %d (err %v)", events, err)
	}
}

func TestCleanupOrphanFiles(t *testing.T) {
	dir := t.TempDir()
	db := writeCleanupDB(t, dir)

	var stdout, stderr bytes.Buffer

	cleanupOrphanFiles(t.Context(), db, dir, false, &stdout, &stderr)

	if _, err := os.Stat(filepath.Join(dir, "orphan.mp4")); !os.IsNotExist(err) {
		t.Errorf("orphan file survived: %v", err)
	}

	for _, keep := range []string{"old.mp4", "new.mp4"} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Errorf("recorded file %s removed: %v", keep, err)
		}
	}

	if !strings.Contains(stdout.String(), "Deleted 1 orphan files") {
		t.Errorf("orphan deletion output missing: %q", stdout.String())
	}
}

func TestCleanupOrphanFilesDryRun(t *testing.T) {
	dir := t.TempDir()
	db := writeCleanupDB(t, dir)

	var stdout, stderr bytes.Buffer

	cleanupOrphanFiles(t.Context(), db, dir, true, &stdout, &stderr)

	if _, err := os.Stat(filepath.Join(dir, "orphan.mp4")); err != nil {
		t.Fatalf("dry-run deleted the orphan: %v", err)
	}

	if !strings.Contains(stdout.String(), "orphan.mp4") {
		t.Errorf("dry-run should list the orphan path: %q", stdout.String())
	}
}

// --- merge-cameras pure helpers ---

func TestListenHostOfMatrix(t *testing.T) {
	cases := map[string]string{
		":9090":              "localhost",
		"0.0.0.0:9090":       "localhost",
		"[::]:9090":          "localhost",
		"9090":               "localhost",
		"":                   "localhost",
		"192.168.1.5:8080":   "192.168.1.5",
		"[2001:db8::1]:8080": "2001:db8::1",
		"example.com:80":     "example.com",
	}

	for listen, want := range cases {
		if got := listenHostOf(listen); got != want {
			t.Errorf("listenHostOf(%q) = %q, want %q", listen, got, want)
		}
	}
}

func TestListenPortOfMatrix(t *testing.T) {
	cases := map[string]string{
		":9090":              "9090",
		"0.0.0.0:9090":       "9090",
		"192.168.1.5:8080":   "8080",
		"[2001:db8::1]:8443": "8443",
		"9191":               "9191",
		"":                   "9090",
	}

	for listen, want := range cases {
		if got := listenPortOf(listen); got != want {
			t.Errorf("listenPortOf(%q) = %q, want %q", listen, got, want)
		}
	}
}

func TestIsPortOpenPositive(t *testing.T) {
	// Only the positive case is asserted: this sandbox's loopback
	// transparent proxy makes "closed port" dials succeed, so negative
	// assertions are environment-dependent.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()

	addr := listener.Addr().String()
	if !isPortOpen(addr) {
		t.Errorf("isPortOpen(%q) = false, want true for a live listener", addr)
	}
}

func TestParseFlagForms(t *testing.T) {
	args := []string{"cmd", "--source", "/a", "--target=/b", "--dry"}

	i := 1
	if v, ok := parseFlag(args, &i, "source"); !ok || v != "/a" {
		t.Errorf("space form: %q %v", v, ok)
	}

	// The space form leaves i on the consumed value; the next flag is i+1.
	i = 3
	if v, ok := parseFlag(args, &i, "target"); !ok || v != "/b" {
		t.Errorf("equals form: %q %v", v, ok)
	}

	i = 3
	if _, ok := parseFlag(args, &i, "missing"); ok {
		t.Error("missing flag reported present")
	}
}

func TestParseBoolFlagForms(t *testing.T) {
	args := []string{"cmd", "--flag", "--off=false", "--on=true"}

	i := 1
	if v, ok := parseBoolFlag(args, &i, "flag"); !ok || !v {
		t.Errorf("bare form: %v %v, want true/true", v, ok)
	}

	i = 2
	if v, ok := parseBoolFlag(args, &i, "off"); !ok || v {
		t.Errorf("=false form: %v %v", v, ok)
	}

	i = 3
	if v, ok := parseBoolFlag(args, &i, "on"); !ok || !v {
		t.Errorf("=true form: %v %v", v, ok)
	}

	i = 0
	if _, ok := parseBoolFlag(args, &i, "missing"); ok {
		t.Error("missing bool flag reported present")
	}
}
