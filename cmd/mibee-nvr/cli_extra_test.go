package main

// Coverage for the CLI subcommand cores after the testability refactor
// (#589): parse*Args/run* are pure (no os.Args reads, no os.Exit), so they
// are exercised in-process. cleanupByDate/cleanupOrphanFiles run against a
// real SQLite DB opened via the same driver the command uses.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/stretchr/testify/require"
)

func TestParseInitArgs(t *testing.T) {
	t.Parallel()

	opts := parseInitArgs([]string{"mibee-nvr", "init"})
	require.Equal(t, "/var/lib/mibee-nvr", opts.dataDir)
	require.Equal(t, ":9090", opts.listenAddr)
	require.Equal(t, "mibee-nvr.yaml", opts.cfgPath)
	require.Equal(t, "admin", opts.username)
	require.False(t, opts.force)

	opts = parseInitArgs([]string{
		"mibee-nvr", "init",
		"--password", "hunter2hunter2",
		"--data-dir", "/srv/nvr",
		"--listen", ":8080",
		"--config", "/etc/nvr.yaml",
		"--username", "root",
		"--force",
	})
	require.Equal(t, "hunter2hunter2", opts.password)
	require.Equal(t, "/srv/nvr", opts.dataDir)
	require.Equal(t, ":8080", opts.listenAddr)
	require.Equal(t, "/etc/nvr.yaml", opts.cfgPath)
	require.Equal(t, "root", opts.username)
	require.True(t, opts.force)
}

func TestRunInit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mibee-nvr.yaml")
	dataDir := filepath.Join(dir, "data")
	var out strings.Builder

	opts := initOptions{
		password: "long-enough-password", dataDir: dataDir,
		listenAddr: ":9090", cfgPath: cfgPath, username: "admin",
	}
	require.NoError(t, runInit(opts, &out))
	require.Contains(t, out.String(), "Configuration saved")

	// The config loads and carries the bcrypt hash.
	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)
	require.Equal(t, "admin", cfg.Auth.Username)
	require.True(t, middleware.CheckPassword("long-enough-password", cfg.Auth.PasswordHash))
	require.DirExists(t, dataDir)

	// Second run without --force refuses; with --force overwrites.
	require.ErrorContains(t, runInit(opts, &out), "already exists")
	opts.force = true
	require.NoError(t, runInit(opts, &out))

	// Short password rejected before any file is written.
	short := opts
	short.password = "short"
	short.cfgPath = filepath.Join(dir, "never-created.yaml")
	require.ErrorContains(t, runInit(short, &out), "at least 8 characters")
	_, err = os.Stat(short.cfgPath)
	require.True(t, os.IsNotExist(err))
}

func TestRunHashPassword(t *testing.T) {
	t.Parallel()
	hash, err := runHashPassword("correct horse battery staple")
	require.NoError(t, err)
	require.NotEqual(t, "correct horse battery staple", hash)
	require.True(t, middleware.CheckPassword("correct horse battery staple", hash))
}

func TestRunHealth(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	// runHealth probes http://localhost<addr>; pass a port-only addr bound
	// by the test server.
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, runHealth(fmt.Sprintf(":%d", port)))

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv2.Close()
	port2 := srv2.Listener.Addr().(*net.TCPAddr).Port
	require.ErrorContains(t, runHealth(fmt.Sprintf(":%d", port2)), "HTTP 503")

	require.Error(t, runHealth(":1")) // connection refused on loopback port 1
}

func TestRunEncryptConfig(t *testing.T) {
	t.Setenv("NVR_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "nvr.yaml")
	cfg := &config.Config{MQTT: config.MQTTConfig{Enabled: true, Password: "plain-secret"}}
	require.NoError(t, config.Save(cfgPath, cfg))

	var out strings.Builder
	require.NoError(t, runEncryptConfig(cfgPath, &out))
	require.Contains(t, out.String(), "Encrypted 1 sensitive field(s)")
	require.Contains(t, out.String(), "mqtt.password")

	// A second pass runs cleanly (idempotence at the CLI level: no error).
	out.Reset()
	require.NoError(t, runEncryptConfig(cfgPath, &out))

	// Missing file errors.
	require.Error(t, runEncryptConfig(filepath.Join(dir, "missing.yaml"), &out))
}

func TestParseCleanupArgs(t *testing.T) {
	t.Parallel()

	opts, help := parseCleanupArgs([]string{"mibee-nvr", "cleanup"})
	require.False(t, help)
	require.Equal(t, "mibee-nvr.yaml", opts.cfgPath)
	require.False(t, opts.orphans)

	_, help = parseCleanupArgs([]string{"mibee-nvr", "cleanup", "--help"})
	require.True(t, help)

	opts, _ = parseCleanupArgs([]string{
		"mibee-nvr", "cleanup",
		"--before", "2026-08-01", "--orphans", "--dry-run", "--config", "/x.yaml",
	})
	require.Equal(t, "2026-08-01", opts.beforeDate)
	require.True(t, opts.orphans)
	require.True(t, opts.dryRun)
	require.Equal(t, "/x.yaml", opts.cfgPath)
}

func TestRunCleanup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	storageRoot := filepath.Join(dir, "data")
	cfgPath := filepath.Join(dir, "nvr.yaml")
	require.NoError(t, config.Save(cfgPath, &config.Config{
		Storage: config.StorageConfig{RootDir: storageRoot},
	}))

	// No mode selected → usage error.
	require.ErrorContains(t, runCleanup(cleanupOptions{cfgPath: cfgPath}), "--before")

	// Nonexistent config → load error.
	require.ErrorContains(t, runCleanup(cleanupOptions{cfgPath: "missing.yaml", beforeDate: "2026-01-01"}), "loading config")

	// Dry-run against an empty DB completes (the storage root must exist —
	// production always has it; sqlite does not create parent dirs).
	require.NoError(t, os.MkdirAll(storageRoot, 0o755))
	require.NoError(t, runCleanup(cleanupOptions{cfgPath: cfgPath, beforeDate: "2026-01-01", dryRun: true}))
	require.FileExists(t, filepath.Join(storageRoot, "mibee-nvr.db"))
}

// TestCleanupByDateRealDB drives cleanupByDate/cleanupOrphanFiles against a
// real SQLite DB seeded through the same driver string the command uses.
func TestCleanupByDateRealDB(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	storageRoot := dir
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "mibee-nvr.db")+"?_pragma=busy_timeout(5000)")
	require.NoError(t, err)
	defer db.Close()
	ctx := context.Background()
	_, err = db.ExecContext(ctx, `CREATE TABLE recordings (
		id TEXT PRIMARY KEY, camera_id TEXT, file_path TEXT, started_at TEXT, ended_at TEXT,
		duration REAL, file_size INTEGER, frame_count INTEGER, merge_status TEXT, merge_tier TEXT)`)
	require.NoError(t, err)

	// One old recording with a real file on disk; one new recording kept.
	oldFile := filepath.Join(dir, "old.mp4")
	require.NoError(t, os.WriteFile(oldFile, []byte("x"), 0o644))
	keepFile := filepath.Join(dir, "keep.mp4")
	require.NoError(t, os.WriteFile(keepFile, []byte("y"), 0o644))
	for _, row := range []struct{ id, path, started string }{
		{"old-1", oldFile, "2026-01-01 00:00:00"},
		{"keep-1", keepFile, "2026-08-01 00:00:00"},
	} {
		_, err = db.ExecContext(ctx,
			"INSERT INTO recordings(id, camera_id, file_path, started_at, ended_at, duration, file_size, frame_count, merge_status, merge_tier) VALUES(?,?,?,?,?,?,?,?,?,?)",
			row.id, "cam-1", row.path, row.started, row.started, 60, 10, 1, "pending", "")
		require.NoError(t, err)
	}

	// Dry run reports but deletes nothing.
	cleanupByDate(ctx, db, storageRoot, "2026-07-01", true)
	require.FileExists(t, oldFile)

	// Real run removes the old file and its row; the new one survives.
	cleanupByDate(ctx, db, storageRoot, "2026-07-01", false)
	_, err = os.Stat(oldFile)
	require.True(t, os.IsNotExist(err))
	var n int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recordings WHERE id='old-1'").Scan(&n))
	require.Equal(t, 0, n)
	require.NoError(t, db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recordings WHERE id='keep-1'").Scan(&n))
	require.Equal(t, 1, n)
	require.FileExists(t, keepFile)

	// Orphan cleanup removes disk files the DB does not know about.
	orphan := filepath.Join(dir, "orphan.mp4")
	require.NoError(t, os.WriteFile(orphan, []byte("z"), 0o644))
	cleanupOrphanFiles(ctx, db, storageRoot, false)
	_, err = os.Stat(orphan)
	require.True(t, os.IsNotExist(err))
	require.FileExists(t, keepFile, "known recordings stay on disk")
}

func TestMergeCamerasPureHelpers(t *testing.T) {
	t.Parallel()

	// listenHostOf normalizes wildcard/bare forms.
	for in, want := range map[string]string{
		":9090":          "localhost",
		"0.0.0.0:9090":   "localhost",
		"[::]:9090":      "localhost",
		"9090":           "localhost",
		"192.168.1.5:80": "192.168.1.5",
	} {
		require.Equal(t, want, listenHostOf(in), in)
	}

	// listenPortOf extracts the port with the default fallback.
	for in, want := range map[string]string{
		":9090":          "9090",
		"0.0.0.0:8080":   "8080",
		"192.168.1.5:80": "80",
		"9090":           "9090",
		"garbage":        "garbage", // bare token treated as the port itself
	} {
		require.Equal(t, want, listenPortOf(in), in)
	}

	// isPortOpen: a live loopback listener is open; a dead port is not.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	require.True(t, isPortOpen(ln.Addr().String()))
	// No negative case: sandboxes with loopback transparent proxies (and
	// some CI networks) accept dials to ANY loopback port, so "closed port"
	// assertions are environment-dependent. The open-listener path is the
	// behavior the merge-cameras probe depends on.

	// parseFlag / parseBoolFlag advance the index and report presence.
	args := []string{"cmd", "--target", "/x", "--execute"}
	i := 1
	v, ok := parseFlag(args, &i, "target")
	require.True(t, ok)
	require.Equal(t, "/x", v)
	require.Equal(t, 2, i, "index advanced to the value position")

	_, ok = parseFlag(args, &i, "missing")
	require.False(t, ok)

	j := 3 // --execute sits at index 3
	b, ok := parseBoolFlag(args, &j, "execute")
	require.True(t, ok)
	require.True(t, b)
}

func TestRunInitExistingConfigSentinel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "nvr.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("x"), 0o644))
	err := runInit(initOptions{password: "long-enough", cfgPath: cfgPath}, os.Stdout)
	require.ErrorIs(t, err, errInitConfigExists, "cmdInit maps this to exit code 2")
}
