package egressagent

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net"
	"net/netip"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/egressallow"
)

// TCP passthrough (ADR 0122, addendum 2026-11-05). The agent resolves the hosts of the
// policy's tcp: entries and writes the result to one file; the TCP relay (tcprelay.go)
// reads that file and decides each connection by address. The two are separate processes
// that share only this file, so a restart of either keeps the other working.

const (
	// tcpRefresh is how often a host is resolved again. The Go resolver does not expose a
	// record's TTL, so this is a fixed period instead of the TTL.
	tcpRefresh = 30 * time.Second
	// tcpRetain is how long an address stays admitted after the last answer that held it.
	// The workspace and the gateway resolve a name separately, and a name with rotating
	// answers would otherwise send the workspace to an address the gateway no longer lists.
	tcpRetain = 15 * time.Minute
)

// tcpTable is the file the relay reads. Enforce is the deployment mode, so a relay that
// cannot reach the CP still decides as the last policy said.
type tcpTable struct {
	Enforce bool            `json:"enforce"`
	Entries []tcpTableEntry `json:"entries"`
}

type tcpTableEntry struct {
	Host  string   `json:"host"`
	From  int      `json:"from"`
	To    int      `json:"to"`
	Addrs []string `json:"addrs"`
	// Seen is when each address was last answered (unix ms). The relay ignores it; a
	// restarted agent restores its retention clock from it, so a restart neither extends
	// an address's life nor stops it from expiring.
	Seen map[string]int64 `json:"seen,omitempty"`
}

// tcpResolver keeps the addresses each passthrough host has resolved to.
type tcpResolver struct {
	path   string
	lookup func(ctx context.Context, host string) ([]netip.Addr, error)
	now    func() time.Time

	mu      sync.Mutex
	set     bool // a policy has been applied; until then the file is left as it is
	enforce bool
	entries []egressallow.TCPEntry
	seen    map[string]map[netip.Addr]time.Time // host -> address -> last answer
	last    map[string]time.Time                // host -> last lookup attempt
	written []byte
}

func newTCPResolver(path string) *tcpResolver {
	r := &tcpResolver{
		path: path,
		now:  time.Now,
		seen: map[string]map[netip.Addr]time.Time{},
		last: map[string]time.Time{},
	}
	r.lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
	}
	r.seedFromDisk()
	return r
}

// seedFromDisk restores what a previous run wrote: the policy (entries and mode), so that
// DNS refresh and expiry go on while the CP is unreachable, and each address's last-seen
// time. An address with no recorded time (a table from before Seen) counts as seen now.
func (r *tcpResolver) seedFromDisk() {
	if r.path == "" {
		return
	}
	b, err := os.ReadFile(r.path)
	if err != nil {
		return
	}
	var t tcpTable
	if json.Unmarshal(b, &t) != nil {
		return
	}
	now := r.now()
	var entries []egressallow.TCPEntry
	for _, e := range t.Entries {
		te, err := egressallow.ParseTCPEntry(egressallow.TCPEntry{Host: e.Host, From: e.From, To: e.To}.String())
		if err != nil {
			continue
		}
		entries = append(entries, te)
		for _, s := range e.Addrs {
			if a, err := netip.ParseAddr(s); err == nil {
				at := now
				if ms, ok := e.Seen[s]; ok {
					at = time.UnixMilli(ms)
				}
				r.note(e.Host, a, at)
			}
		}
	}
	r.set, r.enforce, r.entries = true, t.Enforce, entries
}

func (r *tcpResolver) note(host string, a netip.Addr, at time.Time) {
	if r.seen[host] == nil {
		r.seen[host] = map[netip.Addr]time.Time{}
	}
	r.seen[host][a] = at
}

// setPolicy installs the entries and mode of the latest policy.
func (r *tcpResolver) setPolicy(entries []egressallow.TCPEntry, enforce bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.set, r.enforce, r.entries = true, enforce, entries
}

// resolvable reports whether an address may be admitted at all: global unicast IPv4. The
// relay checks the same again per connection; filtering here keeps a hostile record out of
// the file.
func resolvable(a netip.Addr) bool {
	a = a.Unmap()
	return a.Is4() && !fencedAddr(a)
}

// refresh resolves every host whose answer is older than tcpRefresh and rewrites the file
// when its content changed. A failed lookup keeps the addresses already held. It does
// nothing before the first policy: the file on disk is the last policy that worked.
func (r *tcpResolver) refresh(ctx context.Context) {
	r.mu.Lock()
	if !r.set {
		r.mu.Unlock()
		return
	}
	entries, enforce := r.entries, r.enforce
	r.mu.Unlock()

	now := r.now()
	hosts := map[string]bool{}
	for _, e := range entries {
		hosts[e.Host] = true
	}
	for h := range hosts {
		if lit, err := netip.ParseAddr(h); err == nil {
			r.mu.Lock()
			r.note(h, lit, now)
			r.mu.Unlock()
			continue
		}
		r.mu.Lock()
		due := now.Sub(r.last[h]) >= tcpRefresh
		r.mu.Unlock()
		if !due {
			continue
		}
		addrs, err := r.lookup(ctx, h)
		r.mu.Lock()
		r.last[h] = now
		if err != nil {
			log.Printf("egress-agent: tcp passthrough: resolving %s failed, keeping its last addresses: %v", h, err)
		}
		for _, a := range addrs {
			if resolvable(a) {
				r.note(h, a.Unmap(), now)
			} else {
				log.Printf("egress-agent: tcp passthrough: %s resolved to %s, which is never admitted", h, a)
			}
		}
		r.mu.Unlock()
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for h, m := range r.seen {
		if !hosts[h] {
			delete(r.seen, h)
			delete(r.last, h)
			continue
		}
		for a, at := range m {
			if now.Sub(at) > tcpRetain {
				delete(m, a)
			}
		}
	}
	t := tcpTable{Enforce: enforce, Entries: []tcpTableEntry{}}
	for _, e := range entries {
		te := tcpTableEntry{Host: e.Host, From: e.From, To: e.To, Addrs: []string{}, Seen: map[string]int64{}}
		for a, at := range r.seen[e.Host] {
			te.Addrs = append(te.Addrs, a.String())
			te.Seen[a.String()] = at.UnixMilli()
		}
		sort.Strings(te.Addrs)
		t.Entries = append(t.Entries, te)
	}
	b, err := json.Marshal(t)
	if err != nil || bytes.Equal(b, r.written) {
		return
	}
	if r.path == "" {
		return
	}
	if _, err := writeIfChanged(r.path, b); err != nil {
		log.Printf("egress-agent: tcp passthrough: writing %s: %v", r.path, err)
		return
	}
	r.written = b
}
