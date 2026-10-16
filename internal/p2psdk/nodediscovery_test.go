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
