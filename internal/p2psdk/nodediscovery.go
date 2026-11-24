package peer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"net/url"
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
	NodeID string `json:"node_id"`
	Name   string `json:"name"`
	Region string `json:"region,omitempty"`
	Group  string `json:"group,omitempty"`
	// SignalURL is the advertised endpoint; its host is the TLS SNI to
	// present. One region domain covers every node in the region, so adding a
	// node needs no DNS record (plan A).
	SignalURL string `json:"signal_url"`
	// SignalIP is the address to dial instead of resolving SignalURL's host.
	// Dial it directly, keep SignalURL's host as SNI. Empty = resolve normally.
	SignalIP string `json:"signal_ip,omitempty"`
	// SignalDomain is the region domain used as SNI (host part of SignalURL).
	// Provided so clients need not parse the URL; also the value browser
	// clients use verbatim (plan C).
	SignalDomain string `json:"signal_domain,omitempty"`
	Online       bool   `json:"online"`
	Priority     int    `json:"priority,omitempty"`
}

// TLSServerName returns the SNI a client must present for this node: the
// region domain when the node published one, else "" (caller falls back to
// the dialed host).
func (n PublicNode) TLSServerName() string {
	if n.SignalDomain != "" {
		return n.SignalDomain
	}
	if u, err := url.Parse(n.SignalURL); err == nil {
		return u.Host
	}
	return ""
}

// DialAddr rewrites the URL's host to SignalIP when one is published, so a
// caller can dial the bare address while keeping the original host for SNI.
// The returned changed=false means "use the URL unchanged".
func (n PublicNode) DialAddr(rawURL string) (dialed string, serverName string, changed bool) {
	serverName = n.TLSServerName()
	if n.SignalIP == "" {
		return rawURL, serverName, false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL, serverName, false
	}
	// Preserve the port from the original URL when SignalIP carries none.
	host, port, splitErr := net.SplitHostPort(u.Host)
	if splitErr != nil {
		host = u.Host
		port = ""
	}
	_ = host
	if _, _, ipErr := net.SplitHostPort(n.SignalIP); ipErr != nil && port != "" {
		u.Host = net.JoinHostPort(n.SignalIP, port)
	} else {
		u.Host = n.SignalIP
	}
	return u.String(), serverName, true
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
	fetched time.Time
	best    string
	// rttMs is the winning candidate's probed round trip; observational
	// only (status pages), never an input to selection.
	rttMs int64
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
	apiBaseCache   map[string]discoveryResult
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
		logger.WarnContext(ctx, "node discovery fetch failed — using configured URL", errKey, err)
		return ""
	}
	candidates := onlineSignalURLs(nodes)
	if len(candidates) == 0 {
		return ""
	}

	cands := probeCandidates(ctx, candidates)
	if len(cands) == 0 {
		logger.WarnContext(ctx, "node discovery: every candidate probe failed — using configured URL")
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
		//nolint:gosec // bucket shuffle anti-herd, not crypto
		rand.Shuffle(len(g), func(i, j int) { g[i], g[j] = g[j], g[i] })
		ordered = append(ordered, g...)
	}
	best := ordered[0].url
	logger.InfoContext(ctx, "node discovery selected endpoint",
		urlKey, best, rttMsKey, ordered[0].rtt.Milliseconds(), candidatesKey, len(cands))

	discoveryMu.Lock()
	discoveryCache[nodesURL] = discoveryResult{
		best:    best,
		rttMs:   ordered[0].rtt.Milliseconds(),
		fetched: time.Now(),
	}
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

// nodeListEnvelope is the server's GET /api/nodes answer. APIBaseURL is
// the authoritative identity/commerce base (#156): device enrollment,
// /api/my/*, orders and OTP live ONLY on the master — clients must point
// these calls there even when signaling connects to a nearer worker.
type nodeListEnvelope struct {
	APIBaseURL string       `json:"api_base_url,omitempty"`
	Nodes      []PublicNode `json:"nodes"`
}

// fetchNodeListEnvelope retrieves the public node list and its advertised
// API base. The bare-array shape (spec §7 era) stays accepted for old
// deployments — same dual-shape compatibility the linktest probe adopted.
func fetchNodeListEnvelope(ctx context.Context, nodesURL string) (nodeListEnvelope, error) {
	ctx, cancel := context.WithTimeout(ctx, nodeProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nodesURL, nil)
	if err != nil {
		return nodeListEnvelope{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nodeListEnvelope{}, err
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return nodeListEnvelope{}, fmt.Errorf("node list status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nodeListEnvelope{}, err
	}
	var envelope nodeListEnvelope
	if envErr := json.Unmarshal(body, &envelope); envErr == nil && envelope.Nodes != nil {
		return envelope, nil
	}
	var nodes []PublicNode
	if decodeErr := json.Unmarshal(body, &nodes); decodeErr != nil {
		return nodeListEnvelope{}, decodeErr
	}
	return nodeListEnvelope{Nodes: nodes}, nil
}

// fetchNodeList retrieves just the node list (signal-plane discovery).
func fetchNodeList(ctx context.Context, nodesURL string) ([]PublicNode, error) {
	env, err := fetchNodeListEnvelope(ctx, nodesURL)
	return env.Nodes, err
}

// DiscoverAPIBase returns the identity/commerce API base advertised by the
// platform (#156): the URL under which /device/*, /api/my/*, /api/orders*
// and /api/auth/phone/* must be called — NEVER a worker's base even when
// signaling selected that worker. Empty string means "not advertised" —
// callers fall back to their own configured master base.
// Cached per nodesURL for nodeDiscoveryTTL like DiscoverBestNode.
func DiscoverAPIBase(ctx context.Context, nodesURL string, logger *slog.Logger) string {
	if logger == nil {
		logger = slog.Default()
	}
	discoveryMu.Lock()
	if r, ok := apiBaseCache[nodesURL]; ok && time.Since(r.fetched) < nodeDiscoveryTTL {
		discoveryMu.Unlock()
		return r.best
	}
	discoveryMu.Unlock()

	env, err := fetchNodeListEnvelope(ctx, nodesURL)
	if err != nil {
		logger.WarnContext(ctx, "node discovery fetch failed — api base unavailable", errKey, err)
		return ""
	}
	discoveryMu.Lock()
	if apiBaseCache == nil {
		apiBaseCache = make(map[string]discoveryResult)
	}
	apiBaseCache[nodesURL] = discoveryResult{best: env.APIBaseURL, fetched: time.Now()}
	discoveryMu.Unlock()
	return env.APIBaseURL
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

// Endpoint is one discovery result with everything a client needs to open a
// TLS connection under DNS plan A: the address to dial (possibly a bare IP)
// and the server name to present as SNI (the region domain).
type Endpoint struct {
	// URL is the advertised endpoint (also the HTTP-probe target).
	URL string
	// DialURL is URL with its host rewritten to the node's SignalIP when the
	// platform published one. Dialing this keeps the certificate valid (SNI
	// comes from ServerName) while skipping per-node DNS.
	DialURL string
	// ServerName is the SNI/TLS server name to present. Empty = the transport
	// derives it from the dialed host.
	ServerName string
	// RTT is the measured latency used for ranking.
	RTT time.Duration
}

var endpointCache sync.Map // nodesURL → endpointResult

type endpointResult struct {
	fetched time.Time
	ep      Endpoint
}

// cachedEndpoint returns the still-fresh cached answer for nodesURL, if any.
func cachedEndpoint(nodesURL string) (Endpoint, bool) {
	if cached, ok := endpointCache.Load(nodesURL); ok {
		if r, isOK := cached.(endpointResult); isOK &&
			time.Since(r.fetched) < nodeDiscoveryTTL && r.ep.URL != "" {
			return r.ep, true
		}
	}
	return Endpoint{}, false
}

// invalidateEndpointCache drops the cached discovery answer for nodesURL
// (#163 node lifecycle): called when a node answered a handshake with
// node_draining — the platform pulled it after this answer was cached, and
// the cache would keep steering every rung at it for up to nodeDiscoveryTTL.
func invalidateEndpointCache(nodesURL string) {
	if nodesURL == "" {
		return
	}
	endpointCache.Delete(nodesURL)
}

// endpointCand is one node-list entry prepared for probing: the node plus the
// URL the probe measures. Field order is the fieldalignment optimum (the
// string leads).
type endpointCand struct {
	url  string
	node PublicNode
}

// endpointCandidates filters the node list down to online nodes with a signal
// URL and derives each one's probe target. Under plan A that is the direct-IP
// dial target, so the probe measures the path the client will actually use.
func endpointCandidates(nodes []PublicNode) ([]endpointCand, []string) {
	cands := make([]endpointCand, 0, len(nodes))
	probeURLs := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if !n.Online || !strings.Contains(n.SignalURL, "://") {
			continue
		}
		dialURL, _, changed := n.DialAddr(n.SignalURL)
		probeTarget := n.SignalURL
		if changed {
			probeTarget = dialURL
		}
		cands = append(cands, endpointCand{url: probeTarget, node: n})
		probeURLs = append(probeURLs, probeTarget)
	}
	return cands, probeURLs
}

// aliveEndpoints keeps the candidates whose probe answered, as dials.
func aliveEndpoints(cands []endpointCand, rtts map[string]time.Duration) []Endpoint {
	alive := make([]Endpoint, 0, len(cands))
	for _, c := range cands {
		rtt, ok := rtts[c.url]
		if !ok {
			continue
		}
		dialURL, serverName, _ := c.node.DialAddr(c.node.SignalURL)
		alive = append(alive, Endpoint{
			URL:        c.node.SignalURL,
			DialURL:    dialURL,
			ServerName: serverName,
			RTT:        rtt,
		})
	}
	return alive
}

// rankEndpoints orders alive endpoints best-first: 50 ms latency buckets
// (floor), shuffled inside each bucket (anti-thundering-herd) — the same
// policy as DiscoverBestNode.
func rankEndpoints(alive []Endpoint) []Endpoint {
	buckets := make(map[int64][]Endpoint)
	for _, e := range alive {
		b := int64(e.RTT/time.Millisecond) / bucketWidthMs
		buckets[b] = append(buckets[b], e)
	}
	ordered := make([]Endpoint, 0, len(alive))
	for b := int64(0); b <= maxBucketEndpoints(buckets); b++ {
		group := buckets[b]
		//nolint:gosec // bucket shuffle anti-herd, not crypto
		rand.Shuffle(len(group), func(i, j int) { group[i], group[j] = group[j], group[i] })
		ordered = append(ordered, group...)
	}
	return ordered
}

// DiscoverBestEndpoint is DiscoverBestNode plus the DNS plan-A connection
// details. Callers that need a TLS SNI distinct from the dialed IP (Go/Rust
// with a published signal_ip) use this; DiscoverBestNode stays for callers
// that only need a URL.
//
// Zero Endpoint (URL == "") means "no candidate beat the fallback".
func DiscoverBestEndpoint(ctx context.Context, nodesURL string, logger *slog.Logger) Endpoint {
	if logger == nil {
		logger = slog.Default()
	}
	if cached, ok := cachedEndpoint(nodesURL); ok {
		return cached
	}

	nodes, err := fetchNodeList(ctx, nodesURL)
	if err != nil {
		logger.WarnContext(ctx, "node discovery fetch failed — using configured URL", errKey, err)
		return Endpoint{}
	}

	cands, probeURLs := endpointCandidates(nodes)
	if len(cands) == 0 {
		return Endpoint{}
	}

	alive := aliveEndpoints(cands, probeAll(ctx, probeURLs))
	if len(alive) == 0 {
		logger.WarnContext(ctx, "node discovery: every candidate probe failed — using configured URL")
		return Endpoint{}
	}

	best := rankEndpoints(alive)[0]
	logger.InfoContext(ctx, "node discovery selected endpoint",
		urlKey, best.URL, rttMsKey, best.RTT.Milliseconds(),
		serverNameKey, best.ServerName, candidatesKey, len(alive))

	endpointCache.Store(nodesURL, endpointResult{fetched: time.Now(), ep: best})
	return best
}

func maxBucketEndpoints(m map[int64][]Endpoint) int64 {
	var top int64
	for b := range m {
		if b > top {
			top = b
		}
	}
	return top
}
