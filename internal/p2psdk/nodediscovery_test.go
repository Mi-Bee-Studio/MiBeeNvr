package peer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nodeFixture serves /api/nodes plus per-node /readyz endpoints with
// controllable latencies.
type nodeFixture struct {
	listSrv  *httptest.Server
	slowSrv  *httptest.Server
	fastSrv  *httptest.Server
	fastHits atomic.Int64
}

func newNodeFixture(t *testing.T) *nodeFixture {
	f := &nodeFixture{}

	mk := func(delay time.Duration, hitCounter *atomic.Int64) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if hitCounter != nil {
				hitCounter.Add(1)
			}
			time.Sleep(delay)
			w.WriteHeader(http.StatusOK)
		}))
	}
	f.slowSrv = mk(160*time.Millisecond, nil) // bucket 3
	f.fastSrv = mk(0, &f.fastHits)            // bucket 0

	f.listSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"node_id":"fast","signal_url":"ws://` + f.fastSrv.Listener.Addr().String() + `/signal","online":true},
			{"node_id":"slow","signal_url":"ws://` + f.slowSrv.Listener.Addr().String() + `/signal","online":true},
			{"node_id":"dead","signal_url":"ws://127.0.0.1:1/signal","online":true}
		]`))
	}))
	t.Cleanup(f.listSrv.Close)
	t.Cleanup(f.slowSrv.Close)
	t.Cleanup(f.fastSrv.Close)
	return f
}

// TestDiscoverBestNodePrefersLowLatencyBucket (#48/#36①): the fast node
// wins, the dead candidate is excluded, and repeat calls are served from
// the TTL cache (fast node probed once).
func TestDiscoverBestNodePrefersLowLatencyBucket(t *testing.T) {
	f := newNodeFixture(t)

	// Isolate the package-level cache for this test.
	discoveryMu.Lock()
	saved := discoveryCache
	discoveryCache = make(map[string]discoveryResult)
	discoveryMu.Unlock()
	t.Cleanup(func() {
		discoveryMu.Lock()
		discoveryCache = saved
		discoveryMu.Unlock()
	})

	best := DiscoverBestNode(context.Background(), f.listSrv.URL, nil)
	require.NotEmpty(t, best)
	assert.Contains(t, best, f.fastSrv.Listener.Addr().String(),
		"the 0ms node must beat the 160ms node")

	// Cached: second call must not re-probe (hit count stays at 1 probe).
	before := f.fastHits.Load()
	best2 := DiscoverBestNode(context.Background(), f.listSrv.URL, nil)
	assert.Equal(t, best, best2)
	assert.Equal(t, before, f.fastHits.Load(), "TTL cache must absorb repeat discovery")
}

// TestReadyzURLDerivation: signal URLs map to the readiness endpoint.
func TestReadyzURLDerivation(t *testing.T) {
	assert.Equal(t, "http://h:1/readyz", readyzURL("ws://h:1/signal"))
	assert.Equal(t, "https://h/readyz", readyzURL("wss://h/signal"))
	assert.Equal(t, "http://127.0.0.1:9/readyz", readyzURL("ws://127.0.0.1:9/signal"))
}

// TestNodesURLFallbackOnDiscoveryFailure (#48 hard constraint): when the
// node list endpoint is unreachable, connect() must still dial the
// configured ServerURL — behavior identical to NodesURL being empty.
func TestNodesURLFallbackOnDiscoveryFailure(t *testing.T) {
	mock := newMockServer(t)
	deadList := "http://127.0.0.1:1/api/nodes"

	sess, err := Dial(context.Background(), SignalingConfig{
		ServerURL: mock.url,
		NodesURL:  deadList,
		JWT:       "test-device-jwt",
		ProductID: "test-product",
		PeerID:    "device-1",
		Role:      "device",
	})
	require.NoError(t, err, "discovery failure must fall back to ServerURL")
	t.Cleanup(func() { _ = sess.Close() })
	require.Eventually(t, func() bool {
		return sess.ActiveSessions() >= 0 // connected; presence established
	}, 5*time.Second, 50*time.Millisecond)
}

// TestFetchNodeListDualShape: the production server answers the
// {"nodes":[...]} envelope; the bare array (spec §7 era) stays accepted.
// Before the fix the envelope decode failed and every DiscoverBestNode
// call silently fell back to the configured URL.
func TestFetchNodeListDualShape(t *testing.T) {
	envelope := `{"nodes":[{"node_id":"n1","name":"bj","signal_url":"wss://bj/signal","online":true,"priority":0}]}`
	bare := `[{"node_id":"n1","name":"bj","signal_url":"wss://bj/signal","online":true}]`

	for name, body := range map[string]string{"envelope": envelope, "bare": bare} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			t.Cleanup(srv.Close)

			nodes, err := fetchNodeList(context.Background(), srv.URL)
			require.NoError(t, err)
			require.Len(t, nodes, 1)
			require.Equal(t, "n1", nodes[0].NodeID)
			require.True(t, nodes[0].Online)
		})
	}

	t.Run("garbage still errors", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`not json`))
		}))
		t.Cleanup(srv.Close)
		_, err := fetchNodeList(context.Background(), srv.URL)
		require.Error(t, err)
	})
}

// TestDiscoverAPIBase (#156): the envelope's api_base_url is the
// authoritative identity/commerce base; bare-array answers (old servers)
// and fetch failures yield "" (caller falls back to its configured base).
func TestDiscoverAPIBase(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"nodes":[{"node_id":"m","signal_url":"wss://m/signal","online":true}],"api_base_url":"https://master.example"}`))
	}))
	t.Cleanup(srv.Close)

	discoveryMu.Lock()
	apiBaseCache = nil
	discoveryMu.Unlock()

	base := DiscoverAPIBase(context.Background(), srv.URL, nil)
	require.Equal(t, "https://master.example", base)

	// Cached second call must not re-fetch: kill the server and re-ask.

	srv.Close()
	require.Equal(t, "https://master.example", DiscoverAPIBase(context.Background(), srv.URL, nil))

	// Bare-array (no api_base_url advertised) → empty.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"node_id":"m","signal_url":"wss://m/signal","online":true}]`))
	}))
	t.Cleanup(srv2.Close)
	discoveryMu.Lock()
	apiBaseCache = nil
	discoveryMu.Unlock()
	require.Empty(t, DiscoverAPIBase(context.Background(), srv2.URL, nil))
}
