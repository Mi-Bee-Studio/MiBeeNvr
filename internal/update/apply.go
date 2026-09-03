// Bare-metal upgrade execution layer (#647).
//
// The sandbox in deploy/mibee-nvr.service (User=nvr, ProtectSystem=strict)
// prevents the running app from writing /usr/local/bin/mibee-nvr, so
// in-process self-replacement is impossible by design. Instead the pipeline
// below runs as ROOT inside the one-shot mibee-nvr-update.service helper
// (allowed for the nvr user via a narrowly-scoped polkit rule), or manually
// via `sudo mibee-nvr update`. The app side only writes a request file and
// triggers the helper — it never touches the binary itself.

package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// Request describes one upgrade run.
type Request struct {
	Current    string // running version (appVersion, e.g. "v0.11.0")
	TargetTag  string // release tag to install (e.g. "v0.12.0")
	Repo       string // "owner/name"
	BinaryPath string // absolute path of the binary to replace
	DataDir    string // writable data dir (request file, history)
	HealthURL  string // e.g. http://127.0.0.1:9090/api/health
}

// Applier executes the upgrade pipeline with every external effect behind an
// injectable seam (tests fake all of them; production defaults are the real
// system operations). Not goroutine-safe — one run at a time.
type Applier struct {
	// Mirror is a download-mirror base that replaces "https://github.com"
	// (#649, config update.download_mirror) — the {repo}/releases/download
	// path is preserved underneath it, so a ghproxy-style prefix mirror or a
	// self-hosted path-preserving mirror both fit. Empty = GitHub official.
	// ALL artifacts (binary, checksums.txt, signature) come from the SAME
	// origin: mixing a mirror binary with officially-signed checksums would
	// keep the signature valid while swapping the bytes it vouches for. A
	// mirror failure therefore fails CLOSED — never a silent fallback to
	// GitHub.
	Mirror string

	// ReleaseBase overrides the full download base URL (mirror/tag already
	// resolved) — tests point it at an httptest server.
	ReleaseBase string

	// DeploymentOverride replaces Deployment() (tests).
	DeploymentOverride string

	// SignVerify checks the ed25519 signature over checksums.txt.
	// Default: VerifyChecksumsSignature (embedded release key).
	SignVerify func(checksums, sig []byte) error

	// DigestVerify matches the downloaded artifact against its checksums.txt
	// line. Default: VerifyBinaryChecksum.
	DigestVerify func(checksums []byte, filename string, data []byte) error

	// FreeSpace reports (total, free) bytes on the path's filesystem.
	// Default: storage.FreeSpace (the shared statfs wrapper, #664).
	FreeSpace func(path string) (total, free int64, err error)

	// Restart restarts the mibee-nvr service. Default: systemctl restart.
	Restart func(ctx context.Context) error

	// HealthWait blocks until the upgraded service answers /api/health or the
	// timeout elapses. Default: pollHealth.
	HealthWait func(ctx context.Context, url string, timeout time.Duration) error

	Now func() time.Time // default time.Now

	HealthTimeout  time.Duration // default 120s
	MinSpaceFactor int64         // free disk must exceed factor×artifact size (default 3)

	log *slog.Logger
}

// historyRow is one update-history.jsonl entry (UI reads it via the API).
type historyRow struct {
	Time   string `json:"time"`
	From   string `json:"from"`
	To     string `json:"to"`
	Result string `json:"result"` // "ok" | "failed"
	Error  string `json:"error,omitempty"`
}

// AutoRequest is the polkit handoff file: the app (nvr user) writes it, the
// root helper (mibee-nvr-update.service) reads it and removes it after use.
type AutoRequest struct {
	TargetTag   string `json:"target_tag"`
	RequestedAt string `json:"requested_at"`
}

const (
	requestFile  = "update-request.json"
	historyFile  = "update-history.jsonl"
	maxMetaBytes = 1 << 16 // checksums.txt + .sig are tiny; 64KB is generous
)

// Apply runs the full pipeline: guards → download → verify → atomic replace
// (with .prev backup) → restart → health gate (rollback on failure) → history.
// Any pre-replace failure leaves the system untouched.
func (a *Applier) Apply(ctx context.Context, req Request) error {
	a.log = slog.Default().With("component", "update-apply", "to", req.TargetTag)
	if err := a.guards(req); err != nil {
		return err
	}

	base := a.releaseBase(req)
	binaryURL, checksumsURL, sigURL := base+"/"+assetName(), base+"/checksums.txt", base+"/checksums.txt.sig"

	size, err := a.artifactSize(ctx, binaryURL)
	if err != nil {
		return fmt.Errorf("update: resolve artifact size: %w", err)
	}
	if err := a.checkDisk(req.BinaryPath, size); err != nil {
		return err
	}

	checksums, err := a.fetchMeta(ctx, checksumsURL)
	if err != nil {
		return fmt.Errorf("update: download checksums.txt: %w", err)
	}
	sig, err := a.fetchMeta(ctx, sigURL)
	if err != nil {
		return fmt.Errorf("update: download checksums.txt.sig: %w", err)
	}
	if err := a.signVerify()(checksums, sig); err != nil {
		return fmt.Errorf("update: signature verification failed (artifact may be corrupted or tampered with): %w", err)
	}

	tmpPath := req.BinaryPath + ".new"
	if err := a.downloadBinary(ctx, binaryURL, tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("update: download binary: %w", err)
	}
	data, err := os.ReadFile(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("update: read downloaded binary: %w", err)
	}
	if err := a.digestVerify()(checksums, assetName(), data); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("update: artifact verification failed, system left untouched: %w", err)
	}

	// Atomic replace with rollback anchor. The .new file already sits on the
	// same filesystem, so rename cannot cross devices.
	prevPath := req.BinaryPath + ".prev"
	_ = os.Remove(prevPath)
	if err := os.Rename(req.BinaryPath, prevPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("update: back up current binary: %w", err)
	}
	if err := os.Rename(tmpPath, req.BinaryPath); err != nil {
		// Put the old binary back — the service is still running the old code
		// from memory, but a machine reboot in this window must find a bootable file.
		_ = os.Rename(prevPath, req.BinaryPath)
		_ = os.Remove(tmpPath)
		return fmt.Errorf("update: replace binary: %w", err)
	}

	if err := a.restart()(ctx); err != nil {
		a.rollback(req, prevPath)
		return fmt.Errorf("update: restart service failed (rollback performed): %w", err)
	}
	if err := a.healthWait()(ctx, req.HealthURL, a.healthTimeout()); err != nil {
		a.rollback(req, prevPath)
		a.appendHistory(req, "failed", "health gate: "+err.Error())
		return fmt.Errorf("update: health gate after upgrade failed — rollback to %s performed: %w", req.Current, err)
	}

	a.appendHistory(req, "ok", "")
	a.log.Info("update applied", "from", req.Current, "to", req.TargetTag)
	return nil
}

// guards enforces the恒禁用 conditions from #647: docker deployments, dev
// builds, and non-strictly-newer targets never run.
func (a *Applier) guards(req Request) error {
	deployment := a.DeploymentOverride
	if deployment == "" {
		deployment = Deployment()
	}
	if deployment == "docker" {
		return fmt.Errorf("update: auto-apply is permanently disabled for docker deployments (the container is immutable; use Watchtower or compose pull)")
	}
	if !isNewer(req.Current, req.TargetTag) {
		if ensureV(strings.TrimSpace(req.Current)) == ensureV(strings.TrimSpace(req.TargetTag)) {
			return fmt.Errorf("update: target %s is the running version — nothing to do", req.TargetTag)
		}
		if isSemver(req.Current) && isSemver(req.TargetTag) {
			return fmt.Errorf("update: refusing downgrade %s → %s", req.Current, req.TargetTag)
		}
		return fmt.Errorf("update: running version %q is not a semver release build — self-update is disabled for dev builds", req.Current)
	}
	return nil
}

func (a *Applier) releaseBase(req Request) string {
	if a.ReleaseBase != "" {
		return strings.TrimRight(a.ReleaseBase, "/")
	}
	if m := strings.TrimRight(a.Mirror, "/"); m != "" {
		return fmt.Sprintf("%s/%s/releases/download/%s", m, req.Repo, req.TargetTag)
	}
	return fmt.Sprintf("https://github.com/%s/releases/download/%s", req.Repo, req.TargetTag)
}

func assetName() string {
	arch := runtime.GOARCH
	if arch == "arm" {
		arch = "armv7"
	}
	return "mibee-nvr-" + arch
}

func (a *Applier) artifactSize(ctx context.Context, url string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HEAD %s: %s", url, resp.Status)
	}
	if resp.ContentLength <= 0 {
		return 0, fmt.Errorf("HEAD %s: no content length", url)
	}
	return resp.ContentLength, nil
}

func (a *Applier) checkDisk(path string, artifactSize int64) error {
	_, free, err := a.freeSpace()(filepath.Dir(path))
	if err != nil {
		// A failed space check fails CLOSED: without knowing the free space we
		// cannot guarantee the replace won't fill the disk.
		return fmt.Errorf("update: disk space precheck failed: %w", err)
	}
	factor := a.MinSpaceFactor
	if factor <= 0 {
		factor = 3
	}
	if free < artifactSize*factor {
		return fmt.Errorf("update: insufficient disk space: %d bytes free, need %d (artifact %d × factor %d)",
			free, artifactSize*factor, artifactSize, factor)
	}
	return nil
}

func (a *Applier) fetchMeta(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxMetaBytes))
}

// downloadBinary streams the artifact to path — never buffered whole in memory
// (512MB-class binaries must not blow the memory budget of a 1GB ARM box).
func (a *Applier) downloadBinary(ctx context.Context, url, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", resp.Status)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, resp.Body)
	syncErr := f.Sync()
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

// rollback restores the .prev binary and restarts again. Best-effort: errors
// are logged but do not mask the original failure.
func (a *Applier) rollback(req Request, prevPath string) {
	if _, err := os.Stat(prevPath); err != nil {
		a.log.Error("rollback: no .prev binary to restore", "path", prevPath)
		return
	}
	if err := os.Rename(prevPath, req.BinaryPath); err != nil {
		a.log.Error("rollback: restore previous binary failed", "error", err)
		return
	}
	if err := a.restart()(context.Background()); err != nil {
		a.log.Error("rollback: restart after restore failed", "error", err)
		return
	}
	a.log.Warn("rollback complete — previous version restored", "version", req.Current)
}

func (a *Applier) appendHistory(req Request, result, errMsg string) {
	row := historyRow{
		Time:   a.now().UTC().Format(time.RFC3339),
		From:   req.Current,
		To:     req.TargetTag,
		Result: result,
		Error:  errMsg,
	}
	b, err := json.Marshal(row)
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(req.DataDir, historyFile),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		a.log.Warn("history: open failed", "error", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		a.log.Warn("history: write failed", "error", err)
	}
}

func (a *Applier) healthTimeout() time.Duration {
	if a.HealthTimeout > 0 {
		return a.HealthTimeout
	}
	return 120 * time.Second
}

func (a *Applier) signVerify() func([]byte, []byte) error {
	if a.SignVerify != nil {
		return a.SignVerify
	}
	return VerifyChecksumsSignature
}

func (a *Applier) digestVerify() func([]byte, string, []byte) error {
	if a.DigestVerify != nil {
		return a.DigestVerify
	}
	return VerifyBinaryChecksum
}

func (a *Applier) freeSpace() func(string) (int64, int64, error) {
	if a.FreeSpace != nil {
		return a.FreeSpace
	}
	return storage.FreeSpace
}

func (a *Applier) restart() func(context.Context) error {
	if a.Restart != nil {
		return a.Restart
	}
	return func(ctx context.Context) error {
		return exec.CommandContext(ctx, "systemctl", "restart", "mibee-nvr").Run()
	}
}

func (a *Applier) healthWait() func(context.Context, string, time.Duration) error {
	if a.HealthWait != nil {
		return a.HealthWait
	}
	return pollHealth
}

func (a *Applier) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// pollHealth GETs url every 2s until it answers 200 or the timeout elapses.
func pollHealth(ctx context.Context, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("/api/health not ready within %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// --- Request file (polkit handoff) ---

// WriteRequest atomically writes the auto-apply request into dataDir and
// returns its path. The nvr user owns the data dir (ReadWritePaths), so this
// is the ONE file the sandboxed app may use to ask root for an upgrade.
func WriteRequest(dataDir, targetTag string, now time.Time) (string, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", err
	}
	req := AutoRequest{TargetTag: targetTag, RequestedAt: now.UTC().Format(time.RFC3339)}
	b, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dataDir, requestFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return "", err
	}
	return path, os.Rename(tmp, path)
}

// ReadRequest parses a request file.
func ReadRequest(path string) (AutoRequest, error) {
	var req AutoRequest
	b, err := os.ReadFile(path)
	if err != nil {
		return req, err
	}
	return req, json.Unmarshal(b, &req)
}

// RemoveRequest deletes the request file (called by the helper after use so a
// later manual `systemctl start` doesn't re-run a stale upgrade).
func RemoveRequest(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// RequestFilePath returns the conventional request-file path inside dataDir.
func RequestFilePath(dataDir string) string { return filepath.Join(dataDir, requestFile) }

// --- Auto-apply eligibility ---

// ShouldAutoApply reports whether the sensing layer may trigger the root
// helper for this status: stable channel, semver current build, binary
// deployment, strictly newer candidate. Docker/dev/beta never auto-apply.
func ShouldAutoApply(currentVersion string, st Status, deployment string) bool {
	return st.UpdateAvailable &&
		st.Latest != "" &&
		deployment != "docker" &&
		st.Channel == "stable" &&
		isSemver(currentVersion) &&
		isNewer(currentVersion, st.Latest)
}

// isSemver reports whether v is a canonical semver string (optionally
// "v"-prefixed) — "dev" or "" are not.
func isSemver(v string) bool {
	return semver.IsValid(ensureV(strings.TrimSpace(v)))
}

// TriggerAutoApply is the app-side half of the #647 handoff: for an eligible
// status it writes the request file (the one path the sandboxed nvr user may
// write) and starts the root helper unit. startUnit is injectable — production
// uses systemctl via polkit; trigger failures are returned so the caller can
// log them (auto-apply is best-effort, never fatal).
func TriggerAutoApply(currentVersion string, st Status, deployment, dataDir string, startUnit func(unit string) error) error {
	if !ShouldAutoApply(currentVersion, st, deployment) {
		return fmt.Errorf("update: auto-apply not eligible (deployment=%s channel=%s latest=%q current=%q)",
			deployment, st.Channel, st.Latest, currentVersion)
	}
	if _, err := WriteRequest(dataDir, st.Latest, time.Now()); err != nil {
		return fmt.Errorf("update: write auto-apply request: %w", err)
	}
	const helperUnit = "mibee-nvr-update.service"
	if err := startUnit(helperUnit); err != nil {
		return fmt.Errorf("update: start %s (polkit rule installed?): %w", helperUnit, err)
	}
	slog.Info("update: auto-apply triggered", "version", st.Latest, "helper", helperUnit)
	return nil
}

// StartHelperUnit is the production startUnit: systemctl start via D-Bus
// (polkit authorizes the nvr user for exactly this unit). Use `systemctl
// start` (not `isolate`) so the request is one policy check.
func StartHelperUnit(unit string) error {
	return exec.Command("systemctl", "start", unit).Run()
}
