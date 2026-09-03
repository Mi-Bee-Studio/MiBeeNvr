package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/update"
)

// cmdUpdate implements `mibee-nvr update` — the bare-metal upgrade execution
// layer (#647). Two entry paths share one pipeline:
//
//	manual:      sudo mibee-nvr update [--version vX.Y.Z]
//	root helper: mibee-nvr update --apply-request /var/lib/mibee-nvr/update-request.json
//	             (ExecStart of mibee-nvr-update.service; the file is written by
//	             the app and consumed exactly once)
//
//	--check prints the sensing-layer status without touching anything.
func cmdUpdate() {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	cfgPath := fs.String("config", "mibee-nvr.yaml", "path to configuration file")
	version := fs.String("version", "", "target release tag (default: latest stable)")
	checkOnly := fs.Bool("check", false, "only show the update status, change nothing")
	applyRequest := fs.String("apply-request", "", "run the request file written by the app (helper entry)")
	if err := fs.Parse(os.Args[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if err := runUpdate(runUpdateArgs{
		cfgPath:      *cfgPath,
		version:      *version,
		checkOnly:    *checkOnly,
		applyRequest: *applyRequest,
	}, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// runUpdateArgs is the parsed `update` invocation (kept as a struct so tests
// can call runUpdate without argv games).
type runUpdateArgs struct {
	cfgPath      string
	version      string
	checkOnly    bool
	applyRequest string
}

// applyFn is overridable in tests (the real one runs the full root pipeline).
// The download mirror from config (#649) applies to ALL artifacts; empty
// mirror keeps GitHub official URLs.
var applyFn = func(req update.Request, mirror string) error {
	return (&update.Applier{Mirror: mirror}).Apply(context.Background(), req)
}

// healthFn resolves the health-probe URL for the running config (tests).
var healthFn = healthURLForConfig

func runUpdate(args runUpdateArgs, out io.Writer) error {
	cfg, err := config.Load(args.cfgPath)
	if err != nil {
		return fmt.Errorf("update: load config %s: %w", args.cfgPath, err)
	}
	repo := cfg.Update.Repo
	if repo == "" {
		repo = "Mi-Bee-Studio/MiBeeNvr"
	}

	// Sensing only: print what the checker knows right now.
	if args.checkOnly {
		st, err := updateCheckFn(context.Background(), repo)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "current:    %s\nlatest:     %s\navailable:  %v\ndeployment: %s\n",
			appVersion, st.Latest, st.UpdateAvailable, update.Deployment())
		return nil
	}

	// Root-helper entry: consume the request file exactly once.
	target := args.version
	applyID := ""
	if args.applyRequest != "" {
		reqFile, err := update.ReadRequest(args.applyRequest)
		if err != nil {
			return fmt.Errorf("update: read request file: %w", err)
		}
		target = reqFile.TargetTag
		applyID = reqFile.ID
		defer func() {
			// Remove on ANY outcome (success or failure): a failed attempt is
			// logged in the history file; a stale request must never re-run
			// on the next manual `systemctl start`.
			if err := update.RemoveRequest(args.applyRequest); err != nil {
				slog.Warn("update: remove request file", "path", args.applyRequest, "error", err)
			}
		}()
	}

	if os.Geteuid() != 0 {
		return fmt.Errorf("update: must run as root — use `sudo mibee-nvr update` or the mibee-nvr-update.service helper")
	}

	if target == "" {
		st, err := updateCheckFn(context.Background(), repo)
		if err != nil {
			return fmt.Errorf("update: resolve latest release: %w", err)
		}
		if !st.UpdateAvailable {
			fmt.Fprintf(out, "already up to date (%s)\n", appVersion)
			return nil
		}
		target = st.Latest
	}

	binPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("update: resolve running binary path: %w", err)
	}
	binPath, err = resolveBinaryPath(binPath)
	if err != nil {
		return err
	}
	dataDir := cfg.Storage.RootDir
	if dataDir == "" {
		dataDir = "/var/lib/mibee-nvr"
	}

	fmt.Fprintf(out, "upgrading %s → %s (binary %s)\n", appVersion, target, binPath)
	req := update.Request{
		ID:         applyID,
		Current:    appVersion,
		TargetTag:  target,
		Repo:       repo,
		BinaryPath: binPath,
		DataDir:    dataDir,
		HealthURL:  healthFn(cfg),
	}
	if err := applyFn(req, cfg.Update.DownloadMirror); err != nil {
		return err
	}
	fmt.Fprintf(out, "upgraded to %s — health gate passed\n", target)
	return nil
}

// resolveBinaryPath follows a symlinked binary to its real location so the
// .prev backup and replace target the file systemd actually executes.
func resolveBinaryPath(p string) (string, error) {
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("update: resolve binary symlinks: %w", err)
	}
	if !strings.HasPrefix(resolved, "/") {
		abs, err := os.Getwd()
		if err == nil {
			resolved = abs + "/" + resolved
		}
	}
	return resolved, nil
}

// healthURLForConfig maps the config's server.listen to a loopback health URL
// (":9090" → "http://127.0.0.1:9090/api/health"; an explicit host is kept,
// IPv6 literals bracketed).
func healthURLForConfig(cfg *config.Config) string {
	listen := cfg.Server.Listen
	if listen == "" {
		listen = ":9090"
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		host, port = listen, ""
	}
	if host == "" {
		host = "127.0.0.1"
	}
	if strings.Contains(host, ":") { // IPv6 literal
		host = "[" + host + "]"
	}
	return "http://" + host + ":" + port + "/api/health"
}

// updateCheckFn resolves the CURRENT release status (tests stub the network).
var updateCheckFn = func(ctx context.Context, repo string) (update.Status, error) {
	c := update.New(appVersion, repo, "stable", time.Hour)
	return c.Refresh(ctx)
}
