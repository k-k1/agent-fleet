package main

import (
	"bytes"
	"context"
	"encoding/json"
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
	listen := envOr("AF_EGRESS_LISTEN", ":3128")
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
	allowed := pol.policy.allows(host)
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

// egressBatcher aggregates observations by (host, allowed) and flushes them to the CP
// periodically so a burst of connections becomes one small POST.
type egressBatcher struct {
	url, token string
	mu         sync.Mutex
	counts     map[string]int // key = host + "\x00" + ("1"|"0")
}

func newEgressBatcher(url, token string) *egressBatcher {
	return &egressBatcher{url: url, token: token, counts: map[string]int{}}
}

func (b *egressBatcher) add(host string, allowed bool) {
	k := host + "\x00" + map[bool]string{true: "1", false: "0"}[allowed]
	b.mu.Lock()
	b.counts[k]++
	b.mu.Unlock()
}

func (b *egressBatcher) drain() []egressEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.counts) == 0 {
		return nil
	}
	out := make([]egressEvent, 0, len(b.counts))
	for k, n := range b.counts {
		host, a, _ := strings.Cut(k, "\x00")
		out = append(out, egressEvent{Host: host, Allowed: a == "1", Count: n})
	}
	b.counts = map[string]int{}
	return out
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

func (b *egressBatcher) flush() {
	evs := b.drain()
	if len(evs) == 0 {
		return
	}
	body, _ := json.Marshal(egressIngest{Events: evs})
	req, err := http.NewRequest(http.MethodPost, b.url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if b.token != "" {
		req.Header.Set("Authorization", "Bearer "+b.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("egress-proxy: report failed: %v", err)
		return
	}
	resp.Body.Close()
}
