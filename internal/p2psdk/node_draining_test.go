package peer

// node_draining_test.go — #163 节点生命周期的客户端契约：
//   - node_draining 不是鉴权类关闭（凭证根本没被查看）
//   - 收到它时丢弃发现缓存：下一级重连重新选点，而不是继续撞刚说
//     「去别处」的节点（缓存 TTL 最长 5 分钟）

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

// oneNodeFixture serves a one-node /api/nodes list plus a 200 /readyz
// (the readiness probe discovery measures). listHits counts list fetches —
// a cached answer is a fetch that does not happen.
func oneNodeFixture(t *testing.T) (nodesURL string, listHits *atomic.Int64) {
	t.Helper()
	hits := &atomic.Int64{}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		hits.Add(1)
		_, _ = w.Write([]byte(`{"nodes":[{"node_id":"nd-a","name":"a","online":true,` +
			`"signal_url":"ws://` + strings.TrimPrefix(srv.URL, "http://") + `/signal"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/api/nodes", hits
}

func TestIsNodeDrainingClose(t *testing.T) {
	drain := &websocket.CloseError{Code: websocket.CloseTryAgainLater, Text: closeReasonNodeDraining}
	require.True(t, IsNodeDrainingClose(drain))
	require.True(t, IsNodeDrainingClose(fmt.Errorf("read welcome: %w", drain)), "must see through wrapping")

	require.False(t, IsNodeDrainingClose(
		&websocket.CloseError{Code: websocket.CloseTryAgainLater, Text: "busy"}), "same code, other reason")
	require.False(t, IsNodeDrainingClose(errors.New("boom")))
	require.False(t, IsNodeDrainingClose(nil))

	// Never a credential verdict: the ladder must not abandon, and no
	// re-mint is owed.
	require.False(t, IsAuthClassClose(closeReasonNodeDraining))
	require.False(t, IsMintableAuthClose(closeReasonNodeDraining))
	require.Equal(t, authCloseNone, classifyCloseError(drain))
}

func TestNodeDrainingCloseDropsDiscoveryCache(t *testing.T) {
	nodesURL, hits := oneNodeFixture(t)
	ctx := context.Background()
	sc := &signalingClient{
		cfg:    SignalingConfig{NodesURL: nodesURL},
		logger: slog.New(slog.DiscardHandler),
	}

	best := DiscoverBestEndpoint(ctx, nodesURL, nil)
	require.True(t, strings.HasSuffix(best.URL, "/signal"))
	require.EqualValues(t, 1, hits.Load())
	_ = DiscoverBestEndpoint(ctx, nodesURL, nil)
	require.EqualValues(t, 1, hits.Load(), "inside the TTL the cached answer is reused")

	// Any other handshake close leaves the cache alone.
	sc.noteNodeDrainingClose(&websocket.CloseError{Code: websocket.ClosePolicyViolation, Text: "license_expired"})
	_ = DiscoverBestEndpoint(ctx, nodesURL, nil)
	require.EqualValues(t, 1, hits.Load())

	// node_draining drops it: the next lookup re-discovers.
	sc.noteNodeDrainingClose(fmt.Errorf("read welcome: %w",
		&websocket.CloseError{Code: websocket.CloseTryAgainLater, Text: closeReasonNodeDraining}))
	_ = DiscoverBestEndpoint(ctx, nodesURL, nil)
	require.EqualValues(t, 2, hits.Load(), "the next rung must re-run discovery")

	// A client without discovery (fixed ServerURL) has nothing to drop.
	require.NotPanics(t, func() { invalidateEndpointCache("") })
}
