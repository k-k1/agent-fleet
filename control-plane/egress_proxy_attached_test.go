package main

import (
	"errors"
	"net/netip"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/egressallow"
)

// AF_EGRESS_REFUSE_ATTACHED must cover both ways out of the proxy: the CONNECT dialer and
// the plain-HTTP transport.
func TestAttachedEgressDialCoversBothPaths(t *testing.T) {
	attached := func() []netip.Prefix { return []netip.Prefix{netip.MustParsePrefix("172.20.0.0/16")} }
	d, rt := newAttachedEgressDial(attached)
	if err := d.Control("tcp", "172.20.0.3:7700", nil); !errors.Is(err, egressallow.ErrRefusedAddr) {
		t.Errorf("CONNECT dialer: %v", err)
	}
	if err := d.Control("tcp", "172.17.0.1:8098", nil); err != nil {
		t.Errorf("CONNECT dialer refused an address outside the attached networks: %v", err)
	}
	for name, dial := range map[string]func() error{
		"CONNECT": func() error { _, err := d.DialContext(t.Context(), "tcp", "172.20.0.3:7700"); return err },
		"HTTP":    func() error { _, err := rt.DialContext(t.Context(), "tcp", "172.20.0.3:7700"); return err },
	} {
		if err := dial(); !errors.Is(err, egressallow.ErrRefusedAddr) {
			t.Errorf("%s path dialled an attached network: %v", name, err)
		}
	}
	if err := newEgressDialer().Control("tcp", "172.20.0.3:7700", nil); err != nil {
		t.Fatalf("the default control refuses it too (%v): this test would pass on nothing", err)
	}
}
