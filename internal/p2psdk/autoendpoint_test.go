package peer

import (
	"errors"
	"testing"
	"time"
)

// TestResolveAdvertiseStaticPassthrough 钉死显式地址语义：合法直通、
// 非法值静默禁用（会话不因此失败）、空=禁用。
func TestResolveAdvertiseStaticPassthrough(t *testing.T) {
	if got := resolveAdvertise("", nil); got != "" {
		t.Fatalf("empty must stay disabled, got %q", got)
	}
	if got := resolveAdvertise("203.0.113.5:41913", nil); got != "203.0.113.5:41913" {
		t.Fatalf("valid static must pass through, got %q", got)
	}
	// 非法静态值透传——由 applyAdvertiseAddr 响亮拒绝（advertise_test 契约）。
	for _, passthrough := range []string{"1.2.3.4:0", "not-an-ip:80"} {
		if got := resolveAdvertise(passthrough, nil); got != passthrough {
			t.Fatalf("static %q must pass through, got %q", passthrough, got)
		}
	}
}

// withAutoHooks swaps the package-level hook table for one test and restores
// it after (resolveAdvertise reads the package var; no injection parameter).
func withAutoHooks(t *testing.T, h *autoEndpointHooks) {
	t.Helper()
	saved := autoEndpointHooksPtr
	autoEndpointHooksPtr = h
	t.Cleanup(func() { autoEndpointHooksPtr = saved })
}

func fakeAutoHooks() (*autoEndpointHooks, *[]string) {
	calls := &[]string{}
	h := &autoEndpointHooks{
		discoverUPnP: func(time.Duration) (*UPnPGateway, error) {
			*calls = append(*calls, "ssdp")
			return nil, errors.New("no IGD")
		},
		externalIP: func(*UPnPGateway) (string, error) { return "", errors.New("no gw") },
		addMapping: func(*UPnPGateway, int, int, int) error { return errors.New("no gw") },
		stunQuery: func(addr string, _ time.Duration) (string, error) {
			*calls = append(*calls, "stun:"+addr)
			return "203.0.113.41:51820", nil
		},
	}
	return h, calls
}

// TestAutoEndpointLadderUPnPWins：UPnP 成功时不打 STUN，通告映射地址并缓存。
func TestAutoEndpointLadderUPnPWins(t *testing.T) {
	resetAutoEndpoint(t)
	h, calls := fakeAutoHooks()
	h.discoverUPnP = func(time.Duration) (*UPnPGateway, error) {
		*calls = append(*calls, "ssdp")
		return &UPnPGateway{}, nil
	}
	h.externalIP = func(*UPnPGateway) (string, error) { return "203.0.113.9", nil }
	h.addMapping = func(_ *UPnPGateway, ext, inner, _ int) error {
		*calls = append(*calls, "map")
		if ext != 40000 || inner != 40000 {
			t.Fatalf("mapping ports = %d/%d, want 40000/40000", ext, inner)
		}
		return nil
	}
	withAutoHooks(t, h)
	got := resolveAdvertise("auto", []string{"stun:198.51.100.8:13477"})
	if got != "203.0.113.9:40000" {
		t.Fatalf("upnp ladder must advertise mapped addr, got %q", got)
	}
	for _, c := range *calls {
		if c == "stun:198.51.100.8:13477" {
			t.Fatal("stun must not be queried when UPnP succeeds")
		}
	}
	// 命中缓存：再解析零新调用。
	n := len(*calls)
	if again := resolveAdvertise("auto", nil); again != "203.0.113.9:40000" {
		t.Fatalf("cached resolve changed: %q", again)
	}
	if len(*calls) != n {
		t.Fatalf("cache miss: %d extra calls", len(*calls)-n)
	}
}

// TestAutoEndpointLadderPMPMiddle：无 IGD → PMP 接住（STUN 不该被打），
// PMP 也失败 → 落 STUN（#43-D 阶梯顺序）。
func TestAutoEndpointLadderPMPMiddle(t *testing.T) {
	resetAutoEndpoint(t)

	// PMP wins the middle rung.
	h, calls := fakeAutoHooks()
	h.pmpMapping = func(port int) (string, error) {
		*calls = append(*calls, "pmp")
		if port != 40000 {
			t.Fatalf("pmp port = %d, want 40000", port)
		}
		return "198.51.100.7:40000", nil
	}
	withAutoHooks(t, h)
	if got := resolveAdvertise("auto", []string{"stun:198.51.100.8:13477"}); got != "198.51.100.7:40000" {
		t.Fatalf("pmp rung must advertise mapped addr, got %q", got)
	}
	for _, c := range *calls {
		if c == "stun:198.51.100.8:13477" {
			t.Fatal("stun must not be queried when PMP succeeds")
		}
	}

	// PMP failing falls through to STUN.
	h2, _ := fakeAutoHooks()
	h2.pmpMapping = func(int) (string, error) { return "", errors.New("no pmp") }
	st := &autoEndpointState{}
	res := st.resolve(40000, []string{"stun:198.51.100.8:13477"}, h2)
	if res.addr != "203.0.113.41:40000" || res.mode != "stun" {
		t.Fatalf("pmp-fail → stun: %+v", res)
	}
	if st.mode != "stun" {
		t.Fatalf("mode=%q want stun", st.mode)
	}
}

// TestAutoEndpointLadderSTUNFallback：无 IGD → STUN 兜底（提取 host，端口用固定口）。
func TestAutoEndpointLadderSTUNFallback(t *testing.T) {
	resetAutoEndpoint(t)
	h, _ := fakeAutoHooks()
	// swap in via hooks pointer through resolve directly
	autoEndpoint.mu.Lock()
	autoEndpoint.addr, autoEndpoint.mode, autoEndpoint.expires = "", "", time.Time{}
	autoEndpoint.mu.Unlock()

	st := &autoEndpointState{}
	res := st.resolve(40000, []string{"stun:198.51.100.8:13477", "turn:x:1"}, h)
	if res.addr != "203.0.113.41:40000" || res.mode != "stun" {
		t.Fatalf("stun fallback: %+v", res)
	}
	if st.mode != "stun" {
		t.Fatalf("mode=%q want stun", st.mode)
	}
}

// TestAutoEndpointNegativeCache：双梯全败 → 空地址负缓存（期间不重试），
// 过期后自适应重试（UPnP 后开场景）。
func TestAutoEndpointNegativeCache(t *testing.T) {
	resetAutoEndpoint(t)
	st := &autoEndpointState{}
	h, calls := fakeAutoHooks()
	h.stunQuery = func(string, time.Duration) (string, error) {
		return "", errors.New("stun down")
	}
	if res := st.resolve(40000, []string{"stun:1.2.3.4:3478"}, h); res.addr != "" || !res.fresh || res.upnpErr == nil {
		t.Fatalf("total failure must yield empty fresh addr with causes, got %+v", res)
	}
	n := len(*calls)
	if res := st.resolve(40000, []string{"stun:1.2.3.4:3478"}, h); res.addr != "" || res.fresh {
		t.Fatal("negative cache must hold")
	}
	if len(*calls) != n {
		t.Fatal("negative cache must suppress retries within TTL")
	}
	// 过期 + UPnP 后开 → 自适应升级成功。
	h.discoverUPnP = func(time.Duration) (*UPnPGateway, error) { return &UPnPGateway{}, nil }
	h.externalIP = func(*UPnPGateway) (string, error) { return "203.0.113.7", nil }
	h.addMapping = func(*UPnPGateway, int, int, int) error { return nil }
	st.expires = time.Now().Add(-time.Second)
	if res := st.resolve(40000, nil, h); res.addr != "203.0.113.7:40000" || res.mode != "upnp" {
		t.Fatalf("adaptive upgrade failed: %+v", res)
	}
}

// TestStunURLToAddr：Welcome URL 形态解析（默认 3478、跳过 turn:）。
func TestStunURLToAddr(t *testing.T) {
	cases := map[string]string{
		"stun:198.51.100.8:13477":  "198.51.100.8:13477",
		"stun:stun.example.com":     "stun.example.com:3478",
		"turn:host:1?transport=udp": "",
		"":                          "",
	}
	for in, want := range cases {
		if got := stunURLToAddr(in); got != want {
			t.Fatalf("stunURLToAddr(%q)=%q want %q", in, got, want)
		}
	}
}

func resetAutoEndpoint(t *testing.T) {
	t.Helper()
	autoEndpoint.mu.Lock()
	autoEndpoint.addr, autoEndpoint.mode, autoEndpoint.port, autoEndpoint.gw, autoEndpoint.resolved = "", "", 0, nil, false
	autoEndpoint.expires = time.Time{}
	autoEndpoint.upnpErr, autoEndpoint.pmpErr, autoEndpoint.stunErr = nil, nil, nil
	autoEndpoint.mu.Unlock()
}
