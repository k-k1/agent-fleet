package egressagent

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/egressallow"
)

func tcpEntries(t *testing.T, in ...string) []egressallow.TCPEntry {
	t.Helper()
	var out []egressallow.TCPEntry
	for _, s := range in {
		e, err := egressallow.ParseTCPEntry(s)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

type fakeDNS struct {
	answers map[string][]string
	err     error
	calls   int
}

func (f *fakeDNS) lookup(_ context.Context, host string) ([]netip.Addr, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	var out []netip.Addr
	for _, s := range f.answers[host] {
		out = append(out, netip.MustParseAddr(s))
	}
	return out, nil
}

func readTable(t *testing.T, path string) tcpTable {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var tb tcpTable
	if err := json.Unmarshal(b, &tb); err != nil {
		t.Fatal(err)
	}
	return tb
}

func newTestResolver(t *testing.T, dns *fakeDNS) (*tcpResolver, *time.Time, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "af-tcp.json")
	r := newTCPResolver(path)
	clock := time.Unix(1_800_000_000, 0)
	r.now = func() time.Time { return clock }
	r.lookup = dns.lookup
	return r, &clock, path
}

// Nothing is written before the first policy: the file on disk is the last policy that
// worked, and an agent that restarts while the CP is down must not blank it.
func TestTCPResolverLeavesTheFileUntilAPolicy(t *testing.T) {
	dns := &fakeDNS{answers: map[string][]string{}}
	r, _, path := newTestResolver(t, dns)
	r.refresh(context.Background())
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file written without a policy: %v", err)
	}
}

func TestTCPResolverWritesTheTableForRangesAndLiterals(t *testing.T) {
	dns := &fakeDNS{answers: map[string][]string{
		"git.example.com": {"203.0.113.9", "203.0.113.8"},
		"db.example.com":  {"198.51.100.4"},
	}}
	r, _, path := newTestResolver(t, dns)
	r.setPolicy(tcpEntries(t, "tcp:git.example.com:22", "tcp:db.example.com:11000-11999", "tcp:192.0.2.77:2222"), true)
	r.refresh(context.Background())
	tb := readTable(t, path)
	if !tb.Enforce || len(tb.Entries) != 3 {
		t.Fatalf("table: %+v", tb)
	}
	byHost := map[string]tcpTableEntry{}
	for _, e := range tb.Entries {
		byHost[e.Host] = e
	}
	if g := byHost["git.example.com"]; g.From != 22 || g.To != 22 || !slices.Equal(g.Addrs, []string{"203.0.113.8", "203.0.113.9"}) {
		t.Errorf("git: %+v", g)
	}
	if d := byHost["db.example.com"]; d.From != 11000 || d.To != 11999 || !slices.Equal(d.Addrs, []string{"198.51.100.4"}) {
		t.Errorf("db: %+v", d)
	}
	if l := byHost["192.0.2.77"]; !slices.Equal(l.Addrs, []string{"192.0.2.77"}) {
		t.Errorf("literal: %+v", l)
	}
	if dns.calls != 2 {
		t.Errorf("a literal must not be looked up: %d lookups", dns.calls)
	}
}

// Rotating answers: an address stays admitted for tcpRetain after it was last answered,
// a failed lookup keeps the set, and nothing is looked up more often than tcpRefresh.
func TestTCPResolverRetainsRotatedAddressesAndSurvivesFailure(t *testing.T) {
	dns := &fakeDNS{answers: map[string][]string{"db.example.com": {"198.51.100.4"}}}
	r, clock, path := newTestResolver(t, dns)
	r.setPolicy(tcpEntries(t, "tcp:db.example.com:1433"), true)
	ctx := context.Background()
	r.refresh(ctx)

	*clock = clock.Add(10 * time.Second)
	r.refresh(ctx)
	if dns.calls != 1 {
		t.Fatalf("looked up again after 10s: %d", dns.calls)
	}

	*clock = clock.Add(tcpRefresh)
	dns.answers["db.example.com"] = []string{"198.51.100.5"}
	r.refresh(ctx)
	if got := readTable(t, path).Entries[0].Addrs; !slices.Equal(got, []string{"198.51.100.4", "198.51.100.5"}) {
		t.Fatalf("rotation must keep the old address for a while: %v", got)
	}

	dns.err = errors.New("servfail")
	*clock = clock.Add(tcpRefresh)
	r.refresh(ctx)
	if got := readTable(t, path).Entries[0].Addrs; len(got) != 2 {
		t.Fatalf("a failed lookup must keep the set: %v", got)
	}

	dns.err = nil
	*clock = clock.Add(tcpRetain)
	r.refresh(ctx)
	if got := readTable(t, path).Entries[0].Addrs; !slices.Equal(got, []string{"198.51.100.5"}) {
		t.Fatalf("an address unseen for %s must leave: %v", tcpRetain, got)
	}
}

// A record that points into the VPC, at the metadata service or at loopback never reaches
// the file, and an entry removed from the policy leaves it.
func TestTCPResolverRefusesHostileAnswersAndDropsRemovedEntries(t *testing.T) {
	dns := &fakeDNS{answers: map[string][]string{
		"db.example.com": {"169.254.169.254", "10.0.1.5", "127.0.0.1", "100.64.0.9", "198.51.100.4"},
	}}
	r, _, path := newTestResolver(t, dns)
	r.setPolicy(tcpEntries(t, "tcp:db.example.com:1433"), false)
	r.refresh(context.Background())
	tb := readTable(t, path)
	if tb.Enforce || !slices.Equal(tb.Entries[0].Addrs, []string{"198.51.100.4"}) {
		t.Fatalf("table: %+v", tb)
	}
	r.setPolicy(nil, true)
	r.refresh(context.Background())
	if tb := readTable(t, path); len(tb.Entries) != 0 || !tb.Enforce {
		t.Fatalf("removed entry stays: %+v", tb)
	}
}

// A restart seeds the set from the file, so it does not shrink to the first answer.
func TestTCPResolverSeedsFromDisk(t *testing.T) {
	dns := &fakeDNS{answers: map[string][]string{"db.example.com": {"198.51.100.4", "198.51.100.5"}}}
	r, _, path := newTestResolver(t, dns)
	r.setPolicy(tcpEntries(t, "tcp:db.example.com:1433"), true)
	r.refresh(context.Background())

	dns2 := &fakeDNS{answers: map[string][]string{"db.example.com": {"198.51.100.9"}}}
	r2 := newTCPResolver(path)
	r2.lookup = dns2.lookup
	r2.setPolicy(tcpEntries(t, "tcp:db.example.com:1433"), true)
	r2.refresh(context.Background())
	if got := readTable(t, path).Entries[0].Addrs; len(got) != 3 {
		t.Fatalf("restart lost the earlier addresses: %v", got)
	}
}
