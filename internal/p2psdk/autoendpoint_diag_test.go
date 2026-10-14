package peer

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestAdvertiseResolutionDiagnosable 钉住阶梯的可诊断性（#43-A 接线前置）：
// 全败时带每档原因、负缓存命中不再 fresh、静态/未配置不发梯子。
func TestAdvertiseResolutionDiagnosable(t *testing.T) {
	resetAutoEndpoint(t)
	h, _ := fakeAutoHooks()
	h.pmpMapping = func(int) (string, error) { return "", errors.New("pmp timeout") }
	h.stunQuery = func(string, time.Duration) (string, error) {
		return "", errors.New("stun unreachable")
	}
	withAutoHooks(t, h)

	res := resolveAdvertiseDetailed("auto", []string{"stun:8.134.136.138:13477"})
	if res.addr != "" || res.mode != "" || !res.fresh || !res.ladder {
		t.Fatalf("total failure resolution = %+v; want fresh negative ladder", res)
	}
	if res.upnpErr == nil || res.pmpErr == nil || res.stunErr == nil {
		t.Fatalf("negative resolution must carry per-rung causes, got %+v", res)
	}

	// Negative result is cached: second call within TTL is not fresh, so the
	// warn log fires once per ladder run, not once per session build.
	cached := resolveAdvertiseDetailed("auto", nil)
	if cached.fresh || cached.addr != "" {
		t.Fatalf("cached negative = %+v; want fresh=false addr=''", cached)
	}
}

func TestAdvertiseResolutionNonLadderSpecs(t *testing.T) {
	resetAutoEndpoint(t)

	if res := resolveAdvertiseDetailed("", nil); res.ladder || res.addr != "" {
		t.Fatalf("empty spec = %+v; want non-ladder empty", res)
	}
	static := "203.0.113.5:41913"
	if res := resolveAdvertiseDetailed(static, nil); res.ladder || res.addr != static {
		t.Fatalf("static spec = %+v; want non-ladder passthrough", res)
	}
	if res := resolveAdvertiseDetailed("auto:xyz", nil); res.ladder || res.addr != "auto:xyz" {
		t.Fatalf("typo spec = %+v; want passthrough (loud downstream)", res)
	}
}

// TestLogAdvertiseResolution 钉住日志策略：fresh 成功=Info、fresh 全败=Warn
// 带三档原因、缓存命中与非 auto spec=静默。
func TestLogAdvertiseResolution(t *testing.T) {
	makeLogger := func() (*bytes.Buffer, *slog.Logger) {
		var buf bytes.Buffer
		return &buf, slog.New(slog.NewTextHandler(&buf, nil))
	}

	buf, lg := makeLogger()
	logAdvertiseResolution(lg, advertiseResolution{addr: "203.0.113.9:40000", mode: "upnp", fresh: true, ladder: true})
	if !strings.Contains(buf.String(), "auto advertise resolved") || !strings.Contains(buf.String(), "mode=upnp") {
		t.Fatalf("fresh success log = %q", buf.String())
	}

	buf, lg = makeLogger()
	logAdvertiseResolution(lg, advertiseResolution{
		fresh: true, ladder: true,
		upnpErr: errors.New("no IGD"), pmpErr: errors.New("timeout"), stunErr: errors.New("unreachable"),
	})
	out := buf.String()
	if !strings.Contains(out, "ladder failed") || !strings.Contains(out, "no IGD") || !strings.Contains(out, "timeout") {
		t.Fatalf("fresh failure log = %q", out)
	}
	if !strings.Contains(out, "level=WARN") {
		t.Fatalf("failure must log at warn level, got %q", out)
	}

	buf, _ = makeLogger()
	logAdvertiseResolution(lg, advertiseResolution{addr: "x", mode: "upnp", fresh: false, ladder: true})
	logAdvertiseResolution(lg, advertiseResolution{addr: "203.0.113.5:41913"})
	logAdvertiseResolution(nil, advertiseResolution{fresh: true, ladder: true})
	if buf.Len() != 0 {
		t.Fatalf("cache hits / non-ladder specs / nil logger must stay silent, got %q", buf.String())
	}
}
