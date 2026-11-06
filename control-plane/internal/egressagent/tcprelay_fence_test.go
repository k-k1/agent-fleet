package egressagent

import (
	"net/netip"
	"testing"
)

// The relay and the entry check share one predicate; this pins the relay side, including the
// IPv4-mapped form a dual-stack socket reports.
func TestFencedAddrSharedPredicate(t *testing.T) {
	for s, want := range map[string]bool{
		"10.0.0.5": true, "0.1.2.3": true, "100.64.0.1": true, "198.18.0.7": true, "240.0.0.1": true,
		"::ffff:10.0.0.5": true, "::ffff:8.8.8.8": false, "203.0.113.7": false, "100.63.255.255": false,
	} {
		if got := fencedAddr(netip.MustParseAddr(s)); got != want {
			t.Errorf("fencedAddr(%s) = %v, want %v", s, got, want)
		}
	}
}
