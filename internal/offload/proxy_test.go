package offload

// proxy_test.go — the playback proxy's range coalescing (issue #874 batch 2).
// A browser scrubbing a remote MP4 fires many small ranged GETs; forwarding
// them 1:1 to the object store multiplies request count and latency. The
// proxy expands each request to aligned blocks, fetches missing contiguous
// spans in ONE GetRange each, and caches blocks LRU-capped so sequential
// playback and nearby seeks stop hitting the wire.

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/objectstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingStore wraps fakeStore counting GetRange calls (the coalescing
// observable) and recording the fetched spans.
type countingStore struct {
	*fakeStore
	mu     sync.Mutex
	ranges []string // "start-end" per GetRange call
}

func (c *countingStore) GetRange(ctx context.Context, key string, start, end int64) (io.ReadCloser, objectstore.ObjectInfo, error) {
	c.mu.Lock()
	c.ranges = append(c.ranges, fmt.Sprintf("%d-%d", start, end))
	c.mu.Unlock()
	return c.fakeStore.GetRange(ctx, key, start, end)
}

func (c *countingStore) fetches() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.ranges...)
}

// newProxyEnv builds a proxy over a 1 MiB deterministic object
// (content[i] = byte(i%251)) with 256 KiB blocks.
func newProxyEnv(t *testing.T, opts func(*ProxyOptions)) (*Proxy, *countingStore, string, []byte) {
	t.Helper()
	content := make([]byte, 1<<20)
	for i := range content {
		content[i] = byte(i % 251)
	}
	store := &countingStore{fakeStore: newFakeStore()}
	store.objects["remote.mp4"] = content
	o := ProxyOptions{BlockSize: 256 << 10, MaxCachedBytes: 4 << 20}
	if opts != nil {
		opts(&o)
	}
	p := NewProxy(store, o)
	return p, store, "remote.mp4", content
}

func readAll(t *testing.T, rc io.ReadCloser) []byte {
	t.Helper()
	defer rc.Close()
	b, err := io.ReadAll(rc)
	require.NoError(t, err)
	return b
}

func TestProxyServesRangeAndCoalesces(t *testing.T) {
	p, store, key, content := newProxyEnv(t, nil)
	ctx := context.Background()

	// First request inside block 0: one fetch (whole block span).
	got := readAll(t, mustServe(t, p, ctx, key, 100_000, 150_000, int64(len(content))))
	assert.Equal(t, content[100_000:150_001], got)
	require.Len(t, store.fetches(), 1, "first request fetches the block span")

	// Adjacent follow-up request inside the SAME cached block: no new fetch.
	got = readAll(t, mustServe(t, p, ctx, key, 150_001, 200_000, int64(len(content))))
	assert.Equal(t, content[150_001:200_001], got)
	assert.Len(t, store.fetches(), 1, "adjacent request served from cache")

	// A request in the next block: exactly one more fetch.
	got = readAll(t, mustServe(t, p, ctx, key, 300_000, 300_100, int64(len(content))))
	assert.Equal(t, content[300_000:300_101], got)
	assert.Len(t, store.fetches(), 2)
}

func mustServe(t *testing.T, p *Proxy, ctx context.Context, key string, start, end, total int64) io.ReadCloser {
	t.Helper()
	rc, err := p.ServeRange(ctx, "", key, start, end, total)
	require.NoError(t, err)
	return rc
}

func TestProxyRequestSpanningBlocks(t *testing.T) {
	p, store, key, content := newProxyEnv(t, nil)
	ctx := context.Background()

	// Spans the block0/block1 boundary at 256 KiB (262144).
	start, end := int64(250_000), int64(270_000)
	got := readAll(t, mustServe(t, p, ctx, key, start, end, int64(len(content))))
	assert.Equal(t, content[start:end+1], got)
	// Two blocks missing → fetched as ONE contiguous span (range merging).
	fetches := store.fetches()
	require.Len(t, fetches, 1)
	assert.Equal(t, "0-524287", fetches[0], "contiguous missing blocks merge into one GetRange")
}

func TestProxyClampsBeyondEOF(t *testing.T) {
	p, _, key, content := newProxyEnv(t, nil)
	ctx := context.Background()
	total := int64(len(content))

	got := readAll(t, mustServe(t, p, ctx, key, total-100, total+50_000, total))
	assert.Len(t, got, 100)
	assert.Equal(t, content[total-100:], got)
}

func TestProxyLargeRangeBypassesCache(t *testing.T) {
	p, store, key, content := newProxyEnv(t, func(o *ProxyOptions) { o.MaxCachedBytes = 256 << 10 })
	ctx := context.Background()

	// Span (600 KiB) above the cache cap: direct stream, nothing cached.
	got := readAll(t, mustServe(t, p, ctx, key, 100_000, 700_000, int64(len(content))))
	assert.Equal(t, content[100_000:700_001], got)
	require.Len(t, store.fetches(), 1)
	assert.Equal(t, 0, p.CachedBytes(), "oversized span must not populate the cache")
}

func TestProxyLRUEviction(t *testing.T) {
	p, store, key, content := newProxyEnv(t, func(o *ProxyOptions) {
		o.MaxCachedBytes = 256 << 10 // exactly one block
	})
	ctx := context.Background()
	total := int64(len(content))

	_ = readAll(t, mustServe(t, p, ctx, key, 0, 1000, total))
	_ = readAll(t, mustServe(t, p, ctx, key, 300_000, 301_000, total)) // block 1 evicts block 0
	assert.Equal(t, 256<<10, p.CachedBytes(), "cache stays at cap")

	_ = readAll(t, mustServe(t, p, ctx, key, 1000, 2000, total)) // block 0 again → refetch
	assert.Len(t, store.fetches(), 3, "evicted block must refetch")
}

func TestProxyFetchErrorPropagates(t *testing.T) {
	p, _, _, _ := newProxyEnv(t, nil)
	ctx := context.Background()

	_, err := p.ServeRange(ctx, "", "missing.mp4", 0, 100, 1<<20)
	assert.ErrorIs(t, err, objectstore.ErrObjectNotFound)
}

func TestProxyOpenEndedServesRest(t *testing.T) {
	p, store, key, content := newProxyEnv(t, nil)
	ctx := context.Background()
	total := int64(len(content))

	got := readAll(t, mustServe(t, p, ctx, key, total-1000, -1, total))
	assert.Equal(t, content[total-1000:], got)
	// Small tail span → cached path (≤ cap), not a full-object stream.
	require.Len(t, store.fetches(), 1)
}
