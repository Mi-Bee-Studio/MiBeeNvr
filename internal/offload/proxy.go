package offload

// proxy.go — remote-object playback proxy with range coalescing (issue #874
// batch 2). Browsers scrubbing an MP4 fire many small ranged GETs; the issue
// is explicit that forwarding them 1:1 to the object store would explode
// request count and latency. Strategy:
//
//   - requests are expanded to blockSize-aligned spans;
//   - each contiguous run of MISSING blocks is fetched in ONE GetRange
//     (adjacent missing blocks never fan out into per-block requests);
//   - fetched blocks live in an LRU cache capped at MaxCachedBytes, so
//     sequential playback and nearby seeks stop hitting the wire entirely;
//   - spans larger than the cache cap bypass the cache and stream straight
//     through (bounded memory on RPi: never more than MaxCachedBytes +
//     one response buffer).
//
// The proxy is read-only and safe for concurrent use.

import (
	"bytes"
	"container/list"
	"context"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/objectstore"
)

// ProxyOptions tunes the playback proxy.
type ProxyOptions struct {
	// BlockSize is the fetch/cache granularity. Default 2 MiB.
	BlockSize int64
	// MaxCachedBytes caps the block cache (LRU eviction). Default 16 MiB —
	// small against the 512 MiB process budget, large enough to hold the
	// moov atom plus several playback chunks per object.
	MaxCachedBytes int64

	// NewBucketStore resolves Stores for per-camera override buckets
	// (batch 3); '' (default bucket) never passes through here — the proxy's
	// base Store serves it. nil = override buckets cannot be played back.
	NewBucketStore func(bucket string) (objectstore.Store, error)

	// PresignFor produces presigned GET URLs per bucket (batch 3 direct
	// playback). nil = presigning unavailable; PresignGet errors and callers
	// fall back to proxying.
	PresignFor func(bucket string) (objectstore.Presigner, error)
}

func (o *ProxyOptions) normalize() {
	if o.BlockSize <= 0 {
		o.BlockSize = 2 << 20
	}
	if o.MaxCachedBytes <= 0 {
		o.MaxCachedBytes = 16 << 20
	}
}

type cacheEntry struct {
	id   string // object key + block index
	data []byte
	el   *list.Element
}

// Proxy serves byte ranges of remote objects through a coalescing block
// cache. Construct with NewProxy; zero additional lifecycle.
type Proxy struct {
	store objectstore.Store
	opt   ProxyOptions

	// bucketStores caches override-bucket Stores (batch 3).
	bucketStores sync.Map

	mu          sync.Mutex
	lru         *list.List               // front = most recent
	entries     map[string]*list.Element // block id → element
	cachedBytes int64
}

// NewProxy builds the proxy.
func NewProxy(store objectstore.Store, opt ProxyOptions) *Proxy {
	opt.normalize()
	return &Proxy{
		store:   store,
		opt:     opt,
		lru:     list.New(),
		entries: map[string]*list.Element{},
	}
}

// CachedBytes reports the current cache occupancy (observability).
func (p *Proxy) CachedBytes() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return int(p.cachedBytes)
}

// storeFor resolves the proxy's Store for a bucket (” = default base store).
func (p *Proxy) storeFor(bucket string) (objectstore.Store, error) {
	if bucket == "" {
		return p.store, nil
	}
	if v, ok := p.bucketStores.Load(bucket); ok {
		return v.(objectstore.Store), nil
	}
	if p.opt.NewBucketStore == nil {
		return nil, fmt.Errorf("proxy: bucket %q has no store (per-camera routing playback unavailable)", bucket)
	}
	st, err := p.opt.NewBucketStore(bucket)
	if err != nil {
		return nil, fmt.Errorf("proxy: build store for bucket %q: %w", bucket, err)
	}
	p.bucketStores.Store(bucket, st)
	return st, nil
}

// PresignGet produces a presigned direct-GET URL (batch 3). Errors when no
// PresignFor resolver is wired — callers fall back to proxying the bytes.
func (p *Proxy) PresignGet(ctx context.Context, bucket, key string, ttl time.Duration) (string, error) {
	if p.opt.PresignFor == nil {
		return "", fmt.Errorf("proxy: presigned playback not configured")
	}
	presigner, err := p.opt.PresignFor(bucket)
	if err != nil {
		return "", fmt.Errorf("proxy: presigner for bucket %q: %w", bucket, err)
	}
	return presigner.PresignGet(ctx, key, ttl)
}

func blockID(bucket, key string, index int64) string {
	// Bucket-qualified: per-camera routing can place objects with the same
	// key shape in different buckets.
	return bucket + "/" + key + "#" + strconv.FormatInt(index, 10)
}

// ServeRange returns the object's bytes [start, end] (end INCLUSIVE; -1 =
// through EOF). Small spans come back fully buffered from the block cache;
// spans larger than the cache budget stream straight through GetRange
// without touching it. total is the object's exact size (the caller knows
// it from the outbox row — no Head needed per request).
func (p *Proxy) ServeRange(ctx context.Context, bucket, key string, start, end, total int64) (io.ReadCloser, error) {
	if total <= 0 || start < 0 || start >= total {
		return nil, fmt.Errorf("proxy: invalid range start=%d total=%d", start, total)
	}
	if end < 0 || end > total-1 {
		end = total - 1
	}
	store, err := p.storeFor(bucket)
	if err != nil {
		return nil, err
	}

	// Oversized span: stream through, keep the cache untouched. The store
	// streams the body; memory stays at one response buffer.
	if end-start+1 > p.opt.MaxCachedBytes {
		rc, _, err := store.GetRange(ctx, key, start, end)
		if err != nil {
			return nil, fmt.Errorf("proxy: stream %s: %w", key, err)
		}
		return rc, nil
	}

	data, err := p.fetchBlocks(ctx, bucket, key, start, end, total)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// fetchBlocks ensures every block overlapping [start,end] is cached (missing
// contiguous runs fetched in one GetRange each) and returns the fully
// assembled span bytes.
func (p *Proxy) fetchBlocks(ctx context.Context, bucket, key string, start, end, total int64) ([]byte, error) {
	firstBlock := start / p.opt.BlockSize
	lastBlock := end / p.opt.BlockSize

	// Snapshot what is cached (and count the spans to fetch) under the lock,
	// then do the fetches WITHOUT the lock (network I/O must not serialize
	// concurrent requests), then merge back.
	store, err := p.storeFor(bucket)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	var missing []int64
	for i := firstBlock; i <= lastBlock; i++ {
		if _, ok := p.entries[blockID(bucket, key, i)]; !ok {
			missing = append(missing, i)
		}
	}
	p.mu.Unlock()

	// Fetch each contiguous missing run in one GetRange, then insert into
	// the cache. Two concurrent requests for the same blocks may both fetch
	// (last writer wins) — correctness is preserved (immutable object bytes),
	// only a rare duplicate request is wasted.
	for i := 0; i < len(missing); {
		j := i
		for j+1 < len(missing) && missing[j+1] == missing[j]+1 {
			j++
		}
		fetchStart := missing[i] * p.opt.BlockSize
		fetchEnd := missing[j]*p.opt.BlockSize + p.opt.BlockSize - 1
		if fetchEnd > total-1 {
			fetchEnd = total - 1
		}
		rc, _, err := store.GetRange(ctx, key, fetchStart, fetchEnd)
		if err != nil {
			return nil, fmt.Errorf("proxy: fetch %s[%d,%d]: %w", key, fetchStart, fetchEnd, err)
		}
		buf, err := io.ReadAll(rc)
		closeErr := rc.Close()
		if err != nil {
			return nil, fmt.Errorf("proxy: read %s[%d,%d]: %w", key, fetchStart, fetchEnd, err)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("proxy: close %s[%d,%d]: %w", key, fetchStart, fetchEnd, closeErr)
		}
		p.storeBlocks(bucket, key, missing[i], missing[j], buf)
		i = j + 1
	}

	// Assemble from cache.
	var out bytes.Buffer
	out.Grow(int(end - start + 1))
	for i := firstBlock; i <= lastBlock; i++ {
		blk := p.getBlock(bucket, key, i)
		if blk == nil {
			return nil, fmt.Errorf("proxy: block %s#%d vanished from cache mid-assembly", key, i)
		}
		lo := int64(0)
		if i == firstBlock {
			lo = start - i*p.opt.BlockSize
		}
		hi := int64(len(blk))
		if i == lastBlock {
			hi = end - i*p.opt.BlockSize + 1
		}
		out.Write(blk[lo:hi])
	}
	return out.Bytes(), nil
}

// storeBlocks splits a fetched contiguous span into blocks and inserts them
// into the LRU (evicting oldest entries to stay under the cap).
func (p *Proxy) storeBlocks(bucket, key string, first, last int64, buf []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := first; i <= last; i++ {
		off := (i - first) * p.opt.BlockSize
		if off >= int64(len(buf)) {
			break
		}
		end := off + p.opt.BlockSize
		if end > int64(len(buf)) {
			end = int64(len(buf))
		}
		blk := append([]byte(nil), buf[off:end]...)
		id := blockID(bucket, key, i)
		if existing, ok := p.entries[id]; ok {
			p.lru.MoveToFront(existing)
			existing.Value.(*cacheEntry).data = blk
			continue
		}
		entry := &cacheEntry{id: id, data: blk}
		entry.el = p.lru.PushFront(entry)
		p.entries[id] = entry.el
		p.cachedBytes += int64(len(blk))
	}
	for p.cachedBytes > p.opt.MaxCachedBytes && p.lru.Len() > 0 {
		oldest := p.lru.Back()
		if oldest == nil {
			break
		}
		e := oldest.Value.(*cacheEntry)
		p.lru.Remove(oldest)
		delete(p.entries, e.id)
		p.cachedBytes -= int64(len(e.data))
		// Never evict the block we just inserted — the response below needs
		// it; that would only happen with a cap smaller than one block.
		if e.el == p.lru.Front() {
			break
		}
	}
}

// getBlock returns the cached block, bumping its LRU position.
func (p *Proxy) getBlock(bucket, key string, index int64) []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	el, ok := p.entries[blockID(bucket, key, index)]
	if !ok {
		return nil
	}
	p.lru.MoveToFront(el)
	return el.Value.(*cacheEntry).data
}
