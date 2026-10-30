package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/k-k1/agent-fleet/control-plane/internal/envx"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// egress-proxy subcommand (docs/log/20 M2): a small HTTP forward proxy the workspace
// containers route through (http_proxy/https_proxy env, injected by the CP when
// AF_EGRESS_PROXY_ADDR is set). It classifies each destination against the allowlist
// and batches observations to the Control Plane. LOG-ONLY by default: it NEVER blocks
// — it records what WOULD be blocked so an operator can curate the allowlist before
// switching to enforce (AF_EGRESS_ENFORCE=1). Run as: `control-plane egress-proxy`.
//
// Env: AF_EGRESS_LISTEN (":3128"), AF_EGRESS_INGEST_URL (CP's POST /internal/egress),
// AF_EGRESS_TOKEN (bearer for ingest), AF_EGRESS_ALLOWLIST (file, one host per line,
// "#" comments; appended to the built-in default), AF_EGRESS_ENFORCE ("1" to block).

// proxyPolicy is the atomically-swappable decision state: the compiled allowlist and
// whether to actually block. Held in an atomic.Pointer so a live policy refresh
// (docs/log/20 M3) never races the request path.
type proxyPolicy struct {
	policy  *egressPolicy
	enforce bool
}

type egressProxy struct {
	cur   atomic.Pointer[proxyPolicy]
	batch *egressBatcher
}

func runEgressProxy() {
	listen := envx.Or("AF_EGRESS_LISTEN", ":3128")
	// Static seed: built-in defaults + optional file + AF_EGRESS_ENFORCE. When a CP
	// policy URL is configured it overrides this on first poll (docs/log/20 M3).
	entries := append([]string(nil), defaultEgressAllowlist...)
	if f := os.Getenv("AF_EGRESS_ALLOWLIST"); f != "" {
		if b, err := os.ReadFile(f); err == nil {
			entries = append(entries, strings.Split(string(b), "\n")...)
		} else {
			log.Printf("egress-proxy: allowlist %s: %v (using default only)", f, err)
		}
	}
	p := &egressProxy{batch: newEgressBatcher(os.Getenv("AF_EGRESS_INGEST_URL"), os.Getenv("AF_EGRESS_TOKEN"))}
	p.cur.Store(&proxyPolicy{policy: newEgressPolicy(entries), enforce: os.Getenv("AF_EGRESS_ENFORCE") == "1"})
	go p.batch.run(context.Background())
	if u := os.Getenv("AF_EGRESS_POLICY_URL"); u != "" {
		go p.pollPolicy(context.Background(), u, os.Getenv("AF_EGRESS_TOKEN"))
	}
	log.Printf("egress-proxy on %s (%s)", listen, p.modeLabel())
	log.Fatal((&http.Server{Addr: listen, Handler: http.HandlerFunc(p.handle)}).ListenAndServe())
}

func (p *egressProxy) modeLabel() string {
	if p.cur.Load().enforce {
		return "ENFORCE"
	}
	return "log-only"
}

// pollPolicy refreshes the allowlist + mode from the CP so admin edits (docs/log/20 M3)
// take effect without restarting the proxy. On any error the last-good policy stays.
func (p *egressProxy) pollPolicy(ctx context.Context, url, token string) {
	fetch := func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Printf("egress-proxy: policy fetch failed: %v", err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return
		}
		var b struct {
			Allowlist []string `json:"allowlist"`
			Enforce   bool     `json:"enforce"`
		}
		if json.NewDecoder(resp.Body).Decode(&b) != nil {
			return
		}
		p.cur.Store(&proxyPolicy{policy: newEgressPolicy(b.Allowlist), enforce: b.Enforce})
	}
	fetch()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fetch()
		}
	}
}

func (p *egressProxy) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	p.handleHTTP(w, r)
}

// hostOnly strips the :port from a host:port ("api.x.com:443" -> "api.x.com").
func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

// decide records the observation and reports whether to proceed. Log-only always
// proceeds (even for a would-block); enforce blocks a host that's not allowed.
func (p *egressProxy) decide(host string) bool {
	pol := p.cur.Load()
	allowed := pol.policy.Allows(host)
	p.batch.add(host, allowed)
	return allowed || !pol.enforce
}

// internalEgressDest reports whether the destination resolves to an address no
// workspace has a legitimate reason to reach THROUGH the proxy: loopback (the
// proxy host itself), link-local (incl. the 169.254.169.254 cloud metadata
// service), or unspecified. Denied even in log-only mode — log-only exists to
// curate the public allowlist, not to expose the proxy host's own services.
func internalEgressDest(host string) bool {
	ips, err := net.LookupIP(host)
	if err != nil {
		return false // unresolvable: let the dial fail on its own
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return true
		}
	}
	return false
}

// handleConnect tunnels an HTTPS CONNECT after the allow/log decision.
func (p *egressProxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	if !p.decide(hostOnly(r.Host)) {
		http.Error(w, "egress blocked by policy", http.StatusForbidden)
		return
	}
	if internalEgressDest(hostOnly(r.Host)) {
		http.Error(w, "egress to an internal address is not allowed", http.StatusForbidden)
		return
	}
	dst, err := net.DialTimeout("tcp", r.Host, 15*time.Second)
	if err != nil {
		http.Error(w, "upstream dial failed", http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		dst.Close()
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	src, _, err := hj.Hijack()
	if err != nil {
		dst.Close()
		return
	}
	_, _ = io.WriteString(src, "HTTP/1.1 200 Connection Established\r\n\r\n")
	go func() { _, _ = io.Copy(dst, src); dst.Close() }()
	_, _ = io.Copy(src, dst)
	src.Close()
}

// handleHTTP proxies a plain (non-TLS) HTTP forward request.
func (p *egressProxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	if !p.decide(hostOnly(r.Host)) {
		http.Error(w, "egress blocked by policy", http.StatusForbidden)
		return
	}
	if internalEgressDest(hostOnly(r.Host)) {
		http.Error(w, "egress to an internal address is not allowed", http.StatusForbidden)
		return
	}
	r.RequestURI = ""
	if r.URL.Scheme == "" {
		r.URL.Scheme = "http"
	}
	if r.URL.Host == "" {
		r.URL.Host = r.Host
	}
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// maxEgressKeys bounds the distinct (host, verdict) buckets held while the CP is
// unreachable. A workspace can touch unbounded hostnames, so without a cap a long CP
// outage grows the proxy's memory until it is OOM-killed and takes egress down with it.
const maxEgressKeys = 5000

// egressReportTimeout bounds one report POST; http.DefaultClient has none, and a hung CP
// would otherwise wedge the flush loop and with it every later report.
const egressReportTimeout = 10 * time.Second

// egressBatcher aggregates observations by (host, allowed) and flushes them to the CP
// periodically so a burst of connections becomes one small POST. A batch stays pending
// until the CP answers 2xx: log-only mode curates the allowlist from what was seen, so a
// lost batch leaves a host off the list and enforce later breaks a tool nobody saw.
type egressBatcher struct {
	url, token string
	client     *http.Client
	flushMu    sync.Mutex     // serialises flush; add() never takes it
	mu         sync.Mutex     // guards counts and dropped
	counts     map[string]int // key = host + "\x00" + ("1"|"0")
	// dropped counts observations that did not fit under maxEgressKeys. It is reported
	// to the CP (egressIngest.Dropped) so the loss is visible rather than silent.
	dropped int
}

func newEgressBatcher(url, token string) *egressBatcher {
	return &egressBatcher{url: url, token: token, client: &http.Client{Timeout: egressReportTimeout}, counts: map[string]int{}}
}

func (b *egressBatcher) add(host string, allowed bool) {
	k := host + "\x00" + map[bool]string{true: "1", false: "0"}[allowed]
	b.mu.Lock()
	if len(host) > maxEgressHost {
		b.dropped++
		b.mu.Unlock()
		return
	}
	b.addLocked(k, 1)
	b.mu.Unlock()
}

// addLocked adds n to a bucket, or to dropped when the bucket is new and the cap is hit.
// An existing bucket always grows, so the cap bounds keys, not counts.
func (b *egressBatcher) addLocked(k string, n int) {
	if _, ok := b.counts[k]; !ok && len(b.counts) >= maxEgressKeys {
		b.dropped += n
		return
	}
	b.counts[k] += n
}

// drain hands over everything pending and starts a fresh window. The caller owns the
// result and must restore() it when the report fails.
func (b *egressBatcher) drain() ([]egressEvent, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	dropped := b.dropped
	b.dropped = 0
	if len(b.counts) == 0 {
		return nil, dropped
	}
	out := make([]egressEvent, 0, len(b.counts))
	for k, n := range b.counts {
		host, a, _ := strings.Cut(k, "\x00")
		out = append(out, egressEvent{Host: host, Allowed: a == "1", Count: n})
	}
	b.counts = map[string]int{}
	return out, dropped
}

// restore merges an unreported batch back into the pending counts, next to whatever
// add() collected while the POST was in flight. What no longer fits under the cap is
// folded into dropped.
func (b *egressBatcher) restore(evs []egressEvent, dropped int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dropped += dropped
	for _, e := range evs {
		k := e.Host + "\x00" + map[bool]string{true: "1", false: "0"}[e.Allowed]
		b.addLocked(k, e.Count)
	}
}

func (b *egressBatcher) run(ctx context.Context) {
	if b.url == "" {
		return // no CP configured: the proxy still works, it just doesn't report
	}
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.flush()
		}
	}
}

// maxEgressReportBytes keeps one report well under the CP's 1 MiB ingest limit
// (egressAPI.ingest). Measured: 5000 buckets of 248-character hosts marshal to ~1.4 MB,
// so an uncapped batch accumulated during an outage would be answered 400 forever.
const maxEgressReportBytes = 256 << 10

// maxEgressHost is the longest DNS name (253) that is worth recording; anything longer
// is not a destination and would let one row eat the report budget.
const maxEgressHost = 253

// flush reports the pending batch in chunks that fit the CP's body limit and keeps
// whatever the CP did not accept for the next tick. Delivery is at-least-once: a timeout
// after the CP recorded a chunk re-sends it, and the CP counts it twice.
func (b *egressBatcher) flush() {
	b.flushMu.Lock()
	defer b.flushMu.Unlock()
	evs, dropped := b.drain()
	if len(evs) == 0 && dropped == 0 {
		return
	}
	chunks := splitEgressEvents(evs)
	if len(chunks) == 0 {
		chunks = [][]egressEvent{nil}
	}
	for i, c := range chunks {
		d := 0
		if i == 0 {
			d = dropped // rides on the first chunk; later chunks carry none
		}
		if err := b.post(c, d); err != nil {
			log.Printf("egress-proxy: report failed (will retry): %v", err)
			// Chunks before i were accepted and are not restored; this one and the
			// untried rest are.
			rest := append([]egressEvent(nil), c...)
			for _, r := range chunks[i+1:] {
				rest = append(rest, r...)
			}
			b.restore(rest, d)
			return
		}
	}
}

// splitEgressEvents groups events so each chunk's JSON stays under maxEgressReportBytes;
// the budget leaves ample room for the envelope and the dropped field.
func splitEgressEvents(evs []egressEvent) [][]egressEvent {
	var out [][]egressEvent
	var cur []egressEvent
	size := 0
	for _, e := range evs {
		// The marshalled length, not len(Host): json.Marshal escapes '&', '<', '>' to
		// \u00XX, so a host of valid-looking characters can be 6x its raw size.
		eb, _ := json.Marshal(e)
		n := len(eb) + 1 // the separating comma
		if len(cur) > 0 && size+n > maxEgressReportBytes {
			out = append(out, cur)
			cur, size = nil, 0
		}
		cur = append(cur, e)
		size += n
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

func (b *egressBatcher) post(evs []egressEvent, dropped int) error {
	body, err := json.Marshal(egressIngest{Events: evs, Dropped: dropped})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, b.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if b.token != "" {
		req.Header.Set("Authorization", "Bearer "+b.token)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("CP answered %s", resp.Status)
	}
	return nil
}
