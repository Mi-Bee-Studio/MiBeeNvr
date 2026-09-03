package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeArtifact wires a test HTTP server serving the binary, checksums.txt and
// checksums.txt.sig, tracking what got requested.
type fakeArtifact struct {
	srv        *httptest.Server
	binary     []byte
	checksums  string
	sig        []byte
	binaryHits int
}

func newFakeArtifact(t *testing.T, binary []byte, withSig bool) *fakeArtifact {
	t.Helper()
	fa := &fakeArtifact{binary: binary}
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/download/v9.9.9/mibee-nvr-"+testArch()+"/", func(w http.ResponseWriter, r *http.Request) {
		fa.binaryHits++
		w.Header().Set("Content-Length", strconv.Itoa(len(fa.binary)))
		w.Write(fa.binary)
	})
	mux.HandleFunc("/releases/download/v9.9.9/mibee-nvr-"+testArch(), func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.Itoa(len(fa.binary)))
			return
		}
		fa.binaryHits++
		w.Write(fa.binary)
	})
	fa.checksums = sha256Line("mibee-nvr-"+testArch(), binary)
	mux.HandleFunc("/releases/download/v9.9.9/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fa.checksums))
	})
	if withSig {
		fa.sig = []byte("fake-signature-material")
		mux.HandleFunc("/releases/download/v9.9.9/checksums.txt.sig", func(w http.ResponseWriter, r *http.Request) {
			w.Write(fa.sig)
		})
	}
	fa.srv = httptest.NewServer(mux)
	t.Cleanup(fa.srv.Close)
	return fa
}

func testArch() string {
	if runtime.GOARCH == "arm" {
		return "armv7"
	}
	return runtime.GOARCH
}

// newTestApplier builds an Applier with every external seam faked: downloads
// hit the test server, signature/digest checks are stubs, restart and health
// are recorded callbacks. Verifications use the REAL digest logic (the same
// VerifyBinaryChecksum production uses) over the fake server's bytes.
func newTestApplier(t *testing.T, fa *fakeArtifact) (*Applier, *applyRecorder) {
	t.Helper()
	rec := &applyRecorder{}
	a := &Applier{
		SignVerify: func(checksums, sig []byte) error {
			rec.sigVerified = true
			if !withSig(fa) {
				return errors.New("no sig")
			}
			return nil
		},
		DigestVerify: VerifyBinaryChecksum,
		FreeSpace: func(string) (int64, int64, error) {
			return 1 << 40, 1 << 35, nil // 1TB total, 32GB free
		},
		Restart: func(context.Context) error {
			rec.restarts++
			return nil
		},
		HealthWait: func(context.Context, string, time.Duration) error {
			rec.healthProbes++
			return nil
		},
		Now:           func() time.Time { return time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC) },
		ReleaseBase:   fa.srv.URL + "/releases/download/v9.9.9",
		HealthTimeout: 30 * time.Second,
	}
	return a, rec
}

func withSig(fa *fakeArtifact) bool { return fa.sig != nil }

type applyRecorder struct {
	restarts     int
	healthProbes int
	sigVerified  bool
}

func testRequest(t *testing.T, fa *fakeArtifact) Request {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "mibee-nvr")
	oldBinary := []byte("#!/bin/sh\nold version")
	if err := os.WriteFile(bin, oldBinary, 0o755); err != nil {
		t.Fatal(err)
	}
	return Request{
		Current:    "v1.0.0",
		TargetTag:  "v9.9.9",
		Repo:       "Mi-Bee-Studio/MiBeeNvr",
		BinaryPath: bin,
		DataDir:    dir,
		HealthURL:  "http://127.0.0.1:19090/api/health",
	}
}

// The happy path: verified download → atomic replace (with .prev backup) →
// restart → health gate → history row.
func TestApplier_Apply_HappyPath(t *testing.T) {
	fa := newFakeArtifact(t, []byte("new binary bytes"), true)
	a, rec := newTestApplier(t, fa)
	req := testRequest(t, fa)

	if err := a.Apply(context.Background(), req); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got, err := os.ReadFile(req.BinaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new binary bytes" {
		t.Fatalf("binary not replaced: %q", got)
	}
	prev, err := os.ReadFile(req.BinaryPath + ".prev")
	if err != nil {
		t.Fatalf(".prev backup missing: %v", err)
	}
	if string(prev) != "#!/bin/sh\nold version" {
		t.Fatalf(".prev content wrong: %q", prev)
	}
	if rec.restarts != 1 || rec.healthProbes != 1 || !rec.sigVerified {
		t.Fatalf("pipeline steps wrong: restarts=%d health=%d sigVerified=%v", rec.restarts, rec.healthProbes, rec.sigVerified)
	}
	if fi, err := os.Stat(req.BinaryPath); err != nil || fi.Mode()&0o111 == 0 {
		t.Fatalf("replaced binary must be executable: %v %v", fi, err)
	}

	// History row records from/to/result.
	hist, err := os.ReadFile(filepath.Join(req.DataDir, "update-history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(lastLine(string(hist))), &row); err != nil {
		t.Fatalf("history row not JSON: %v\n%s", err, hist)
	}
	if row["from"] != "v1.0.0" || row["to"] != "v9.9.9" || row["result"] != "ok" {
		t.Fatalf("history row wrong: %v", row)
	}
}

// The #646 acceptance case in the pipeline context: a tampered binary must be
// rejected BEFORE any system change — no replace, no restart, no history.
func TestApplier_Apply_TamperedBinaryRejectedBeforeApply(t *testing.T) {
	fa := newFakeArtifact(t, []byte("forged binary bytes — digest will not match"), true)
	a, rec := newTestApplier(t, fa)
	// Real digest verification over checksums for a DIFFERENT binary.
	fa.checksums = sha256Line("mibee-nvr-"+testArch(), []byte("the genuine artifact"))
	req := testRequest(t, fa)

	err := a.Apply(context.Background(), req)
	if err == nil {
		t.Fatal("digest mismatch must fail Apply")
	}

	got, readErr := os.ReadFile(req.BinaryPath)
	if readErr != nil || string(got) != "#!/bin/sh\nold version" {
		t.Fatalf("binary must be untouched on verification failure: %q %v", got, readErr)
	}
	if rec.restarts != 0 {
		t.Fatalf("no restart may happen on verification failure, got %d", rec.restarts)
	}
	if _, err := os.Stat(req.BinaryPath + ".new"); !os.IsNotExist(err) {
		t.Fatalf("temp file must be cleaned up: %v", err)
	}
	if _, err := os.Stat(req.BinaryPath + ".prev"); !os.IsNotExist(err) {
		t.Fatal("no backup may exist when nothing was applied")
	}
}

func TestApplier_Apply_MissingSignatureRejected(t *testing.T) {
	fa := newFakeArtifact(t, []byte("new binary"), false) // release without .sig
	a, _ := newTestApplier(t, fa)
	req := testRequest(t, fa)

	if err := a.Apply(context.Background(), req); err == nil {
		t.Fatal("unsigned release must be rejected by the pipeline")
	}
	if got, _ := os.ReadFile(req.BinaryPath); string(got) != "#!/bin/sh\nold version" {
		t.Fatal("binary must be untouched")
	}
}

// Health gate failure after a successful replace → automatic rollback to the
// .prev binary + second restart.
func TestApplier_Apply_HealthGateFailureRollsBack(t *testing.T) {
	fa := newFakeArtifact(t, []byte("new but broken binary"), true)
	a, rec := newTestApplier(t, fa)
	a.HealthWait = func(context.Context, string, time.Duration) error {
		return errors.New("health gate: /api/health not ready in time")
	}
	req := testRequest(t, fa)

	err := a.Apply(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "rollback") {
		t.Fatalf("expected rollback error, got: %v", err)
	}

	got, readErr := os.ReadFile(req.BinaryPath)
	if readErr != nil || string(got) != "#!/bin/sh\nold version" {
		t.Fatalf("rollback must restore the previous binary: %q %v", got, readErr)
	}
	if rec.restarts != 2 {
		t.Fatalf("rollback must restart again, restarts=%d", rec.restarts)
	}
	hist, _ := os.ReadFile(filepath.Join(req.DataDir, "update-history.jsonl"))
	if !strings.Contains(string(hist), `"result":"failed"`) {
		t.Fatalf("history must record the failure: %s", hist)
	}
}

func TestApplier_Apply_Guards(t *testing.T) {
	fa := newFakeArtifact(t, []byte("new"), true)

	cases := []struct {
		name string
		mut  func(*Request, *Applier)
		want string
	}{
		{
			name: "docker deployment refused",
			mut: func(r *Request, a *Applier) {
				a.DeploymentOverride = "docker"
			},
			want: "docker",
		},
		{
			name: "dev build refused",
			mut:  func(r *Request, a *Applier) { r.Current = "dev" },
			want: "dev",
		},
		{
			name: "downgrade refused",
			mut:  func(r *Request, a *Applier) { r.TargetTag = "v0.0.1" },
			want: "downgrade",
		},
		{
			name: "insufficient disk space refused",
			mut: func(r *Request, a *Applier) {
				// Artifact is 3 bytes ("new"); 1 byte free < required 9.
				a.FreeSpace = func(string) (int64, int64, error) { return 1 << 30, 1, nil }
			},
			want: "disk",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, rec := newTestApplier(t, fa)
			req := testRequest(t, fa)
			tc.mut(&req, a)
			err := a.Apply(context.Background(), req)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got: %v", tc.want, err)
			}
			if rec.restarts != 0 {
				t.Fatalf("guards must run before any system change, restarts=%d", rec.restarts)
			}
			if got, _ := os.ReadFile(req.BinaryPath); string(got) != "#!/bin/sh\nold version" {
				t.Fatal("binary must be untouched")
			}
		})
	}
}

// The polkit handoff file: the app (nvr user) writes a request, the root
// helper reads it and deletes it after a successful run.
func TestRequestFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteRequest(dir, "v9.9.9", time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != dir {
		t.Fatalf("request file must live in the data dir, got %s", path)
	}
	got, err := ReadRequest(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.TargetTag != "v9.9.9" {
		t.Fatalf("round trip lost the tag: %+v", got)
	}
	if got.RequestedAt == "" {
		t.Fatal("requested_at must be recorded")
	}
	if err := RemoveRequest(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("request must be deletable")
	}
}

// Auto-apply eligibility: only stable-channel, semver-current, non-docker
// deployments with a strictly newer candidate may trigger the helper.
func TestShouldAutoApply(t *testing.T) {
	cases := []struct {
		name        string
		current     string
		channel     string
		deployment  string
		updateAvail bool
		latest      string
		want        bool
	}{
		{"eligible", "v1.0.0", "stable", "binary", true, "v9.9.9", true},
		{"docker never", "v1.0.0", "stable", "docker", true, "v9.9.9", false},
		{"dev build never", "dev", "stable", "binary", true, "v9.9.9", false},
		{"beta channel never", "v1.0.0", "beta", "binary", true, "v9.9.9", false},
		{"no update available", "v1.0.0", "stable", "binary", false, "v1.0.0", false},
		{"no latest known", "v1.0.0", "stable", "binary", false, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := Status{UpdateAvailable: tc.updateAvail, Latest: tc.latest, Channel: tc.channel}
			if got := ShouldAutoApply(tc.current, st, tc.deployment); got != tc.want {
				t.Fatalf("ShouldAutoApply(%q, %+v, %q) = %v, want %v", tc.current, st, tc.deployment, got, tc.want)
			}
		})
	}
}

// sha256Line renders one sha256sum-format line for the digest verifier.
func sha256Line(name string, data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) + "  " + name + "\n"
}

// lastLine returns the final non-empty line of s.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

// SetOnAvailable fires once per distinct newer tag — the auto-apply hook
// (#647). Repeated polls of the same tag must not re-trigger.
func TestChecker_SetOnAvailable_FiresOncePerTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v9.9.9","html_url":"x"}`)
	}))
	defer srv.Close()

	c := New("v1.0.0", "Mi-Bee-Studio/MiBeeNvr", "stable", time.Hour)
	c.endpoint = srv.URL
	var mu sync.Mutex
	var tags []string
	c.SetOnAvailable(func(st Status) {
		mu.Lock()
		tags = append(tags, st.Latest)
		mu.Unlock()
	})

	for range 3 {
		if _, err := c.fetchAndCache(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(tags) != 1 || tags[0] != "v9.9.9" {
		t.Fatalf("callback must fire exactly once for the tag, got %v", tags)
	}
}

// The auto-apply trigger: eligible status → request file written + helper
// triggered; ineligible → nothing.
func TestTriggerAutoApply(t *testing.T) {
	dir := t.TempDir()
	var started []string
	trig := func(unit string) error {
		started = append(started, unit)
		return nil
	}

	st := Status{UpdateAvailable: true, Latest: "v9.9.9", Channel: "stable"}
	if err := TriggerAutoApply("v1.0.0", st, "binary", dir, trig); err != nil {
		t.Fatalf("eligible trigger: %v", err)
	}
	reqPath := RequestFilePath(dir)
	got, err := ReadRequest(reqPath)
	if err != nil || got.TargetTag != "v9.9.9" {
		t.Fatalf("request file must carry the target tag: %+v %v", got, err)
	}
	if len(started) != 1 || started[0] != "mibee-nvr-update.service" {
		t.Fatalf("helper must be started exactly once, got %v", started)
	}

	if err := TriggerAutoApply("v1.0.0", st, "docker", dir, trig); err == nil {
		t.Fatal("docker deployment must refuse to trigger")
	}
	if len(started) != 1 {
		t.Fatalf("ineligible status must not trigger the helper, got %v", started)
	}
}

// #649: a configured download mirror must serve ALL artifacts (binary,
// checksums, signature) — the trust chain may never mix origins — and a
// mirror failure must NOT silently fall back to GitHub.
func TestApplier_MirrorSameOriginAndNoFallback(t *testing.T) {
	// Official GitHub side: would serve a GOOD artifact, but must never be hit.
	official := newFakeArtifact(t, []byte("genuine official artifact"), true)

	// Mirror side: serves checksums+sig but the binary endpoint 404s — the
	// classic broken-mirror case that must fail loudly, not fall back.
	var mirrorBinaryHits, mirrorMetaHits int
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/mibee-nvr-") {
			mirrorBinaryHits++
			if r.Method == http.MethodHead || r.Method == http.MethodGet {
				http.NotFound(w, r)
				return
			}
		}
		mirrorMetaHits++
		if strings.HasSuffix(r.URL.Path, "checksums.txt") {
			fmt.Fprint(w, sha256Line("mibee-nvr-"+testArch(), []byte("genuine official artifact")))
			return
		}
		fmt.Fprint(w, "mirror-signature")
	}))
	defer mirror.Close()

	a, rec := newTestApplier(t, official)
	// Mirror replaces https://github.com — the {repo}/releases/... path is
	// preserved underneath it. Clear the helper's ReleaseBase so the mirror
	// derivation runs.
	a.ReleaseBase = ""
	a.Mirror = mirror.URL
	req := testRequest(t, official)

	err := a.Apply(context.Background(), req)
	if err == nil {
		t.Fatal("broken mirror must fail the apply")
	}
	if mirrorBinaryHits == 0 && mirrorMetaHits == 0 {
		t.Fatal("mirror must have been contacted")
	}
	if official.binaryHits != 0 {
		t.Fatal("must NOT fall back to the official origin when a mirror is configured")
	}
	if rec.restarts != 0 {
		t.Fatal("no system change on mirror failure")
	}
}

// Empty mirror (the default) keeps today's behavior: GitHub official URLs.
func TestApplier_NoMirrorUsesOfficial(t *testing.T) {
	fa := newFakeArtifact(t, []byte("artifact"), true)
	a, _ := newTestApplier(t, fa)
	a.ReleaseBase = "" // production default path
	req := testRequest(t, fa)

	base := a.releaseBase(req)
	if !strings.HasPrefix(base, "https://github.com/Mi-Bee-Studio/MiBeeNvr/releases/download/v9.9.9") {
		t.Fatalf("default base must be GitHub official, got %s", base)
	}
}
