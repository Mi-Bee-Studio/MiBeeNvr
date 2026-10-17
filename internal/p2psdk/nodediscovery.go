package peer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"
)

// #48 SDK client nearest-node selection, with the syncthing-style delay
// bucketing from #36①: fetch the platform's public node list
// (GET /api/nodes), probe every online candidate concurrently, group the
// survivors into 50 ms latency buckets (floor), shuffle inside each bucket
// (anti-thundering-herd), and offer the buckets' head as the best endpoint.
// Probe failures sort last. A discovered URL never REPLACES the configured
// ServerURL outright — callers fall back to it on any discovery failure,
// so an empty NodesURL keeps behavior byte-identical to before (the hard
// backward-compatibility constraint of #48).

// PublicNode mirrors one entry of GET /api/nodes.
type PublicNode struct {
	NodeID    string `json:"node_id"`
	Name      string `json:"name"`
	Region    string `json:"region,omitempty"`
	Group     string `json:"group,omitempty"`
	SignalURL string `json:"signal_url"`
	Online    bool   `json:"online"`
	Priority  int    `json:"priority,omitempty"`
}

// nodeDiscoveryCache TTL: probing on every reconnect rung would hammer the
// fleet after a network flap; one fresh answer per window is plenty.
var nodeDiscoveryTTL = 5 * time.Minute

// nodeProbeTimeout bounds ONE candidate probe.
var nodeProbeTimeout = 3 * time.Second

// bucketWidthMs is the syncthing relay-client bucket granularity (#36①):
// candidates within one bucket are treated as equal and shuffled — all
// clients do not stampede the nominal best node, while the choice stays
// latency-ordered.
const bucketWidthMs = 50

type discoveryResult struct {
	best    string
	rttMs   int64
	fetched time.Time
}

// NodeSelection is the outcome of the last successful nearest-node
// discovery for a configured NodesURL — the observability surface for
// embedders (status pages) to show which node the device is using.
type NodeSelection struct {
	URL   string
	RTTMs int64
	At    time.Time
}

// LastNodeSelection returns the cached selection for nodesURL. ok=false
// means no successful discovery has completed yet (or the TTL cache
// expired) — callers report the configured ServerURL as the endpoint.
func LastNodeSelection(nodesURL string) (NodeSelection, bool) {
	discoveryMu.Lock()
	defer discoveryMu.Unlock()
	r, ok := discoveryCache[nodesURL]
	if !ok || r.best == "" {
		return NodeSelection{}, false
	}
	return NodeSelection{URL: r.best, RTTMs: r.rttMs, At: r.fetched}, true
}

var (
	discoveryMu    sync.Mutex
	discoveryCache map[string]discoveryResult
)

// DiscoverBestNode fetches the node list and returns the best signal URL
// by probed latency (bucketed, shuffled). Empty string means "no candidate
// beat the fallback" — callers then use their configured ServerURL.
func DiscoverBestNode(ctx context.Context, nodesURL string, logger *slog.Logger) string {
	if logger == nil {
		logger = slog.Default()
	}
	discoveryMu.Lock()
	if discoveryCache == nil {
		discoveryCache = make(map[string]discoveryResult)
	}
	if r, ok := discoveryCache[nodesURL]; ok && time.Since(r.fetched) < nodeDiscoveryTTL && r.best != "" {
		discoveryMu.Unlock()
		return r.best
	}
	discoveryMu.Unlock()

	nodes, err := fetchNodeList(ctx, nodesURL)
	if err != nil {
		logger.Warn("node discovery fetch failed — using configured URL", "error", err)
		return ""
	}
	candidates := onlineSignalURLs(nodes)
	if len(candidates) == 0 {
		return ""
	}

	cands := probeCandidates(ctx, candidates)
	if len(cands) == 0 {
		logger.Warn("node discovery: every candidate probe failed — using configured URL")
		return ""
	}

	// Bucket by floor(rtt/50ms), shuffle inside the bucket, order by bucket.
	buckets := make(map[int64][]nodeCand)
	for _, c := range cands {
		b := int64(c.rtt/time.Millisecond) / bucketWidthMs
		buckets[b] = append(buckets[b], c)
	}
	var ordered []nodeCand
	for b := int64(0); b <= maxBucket(buckets); b++ {
		g := buckets[b]
		rand.Shuffle(len(g), func(i, j int) { g[i], g[j] = g[j], g[i] })
		ordered = append(ordered, g...)
	}
	best := ordered[0].url
	logger.Info("node discovery selected endpoint",
		"url", best, "rtt_ms", ordered[0].rtt.Milliseconds(), "candidates", len(cands))

	discoveryMu.Lock()
	discoveryCache[nodesURL] = discoveryResult{best: best, rttMs: ordered[0].rtt.Milliseconds(), fetched: time.Now()}
	discoveryMu.Unlock()
	return best
}

// onlineSignalURLs returns the signal URLs of online nodes.
func onlineSignalURLs(nodes []PublicNode) []string {
	var candidates []string
	for _, n := range nodes {
		if n.Online && strings.Contains(n.SignalURL, "://") {
			candidates = append(candidates, n.SignalURL)
		}
	}
	return candidates
}

// probeCandidates latency-probes the candidate URLs and keeps the ones
// that answered.
func probeCandidates(ctx context.Context, candidates []string) []nodeCand {
	rtts := probeAll(ctx, candidates)
	var cands []nodeCand
	for _, u := range candidates {
		if d, ok := rtts[u]; ok {
			cands = append(cands, nodeCand{url: u, rtt: d})
		}
	}
	return cands
}

// nodeCand is one probed discovery candidate.
type nodeCand struct {
	url string
	rtt time.Duration
}

func maxBucket(m map[int64][]nodeCand) int64 {
	var top int64
	for b := range m {
		if b > top {
			top = b
		}
	}
	return top
}

// fetchNodeList retrieves the public node list.
func fetchNodeList(ctx context.Context, nodesURL string) ([]PublicNode, error) {
	ctx, cancel := context.WithTimeout(ctx, nodeProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nodesURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("node list status %d", resp.StatusCode)
	}
	var nodes []PublicNode
	if err := json.NewDecoder(resp.Body).Decode(&nodes); err != nil {
		return nil, err
	}
	return nodes, nil
}

// probeAll times an HTTP readiness check against each candidate's signal
// host concurrently. Missing answers are simply absent from the map.
func probeAll(ctx context.Context, urls []string) map[string]time.Duration {
	type result struct {
		url string
		rtt time.Duration
	}
	out := make(map[string]time.Duration, len(urls))
	ch := make(chan result, len(urls))
	for _, u := range urls {
		go func(u string) {
			start := time.Now()
			pctx, cancel := context.WithTimeout(ctx, nodeProbeTimeout)
			defer cancel()
			req, err := http.NewRequestWithContext(pctx, http.MethodGet, readyzURL(u), nil)
			if err != nil {
				return
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return
			}
			ch <- result{url: u, rtt: time.Since(start)}
		}(u)
	}
	deadline := time.After(nodeProbeTimeout + 500*time.Millisecond)
	for len(out) < len(urls) {
		select {
		case r := <-ch:
			out[r.url] = r.rtt
		case <-deadline:
			return out
		}
	}
	return out
}

// readyzURL derives the readiness endpoint from a signal URL: the signal
// server and the HTTP API share one process, so ws(s)://host:port/anything
// → http(s)://host:port/readyz.
func readyzURL(signalURL string) string {
	u := signalURL
	switch {
	case strings.HasPrefix(u, "wss://"):
		u = "https://" + strings.TrimPrefix(u, "wss://")
	case strings.HasPrefix(u, "ws://"):
		u = "http://" + strings.TrimPrefix(u, "ws://")
	}
	if i := strings.Index(u, "/"); i >= 0 && strings.Contains(u[5:], "/") {
		// strip any path on the host part: find the first '/' after scheme
		if si := strings.Index(u, "://"); si >= 0 {
			if pi := strings.Index(u[si+3:], "/"); pi >= 0 {
				u = u[:si+3+pi]
			}
		}
	}
	return strings.TrimRight(u, "/") + "/readyz"
}
