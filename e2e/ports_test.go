//go:build e2e

package e2e

import (
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// portAllocator hands out 127.0.0.1 ports and remembers every one it has handed out, so
// two tests running in parallel never get overlapping ranges between the probe and the bind.
type portAllocator struct {
	mu     sync.Mutex
	handed map[int]bool
}

// harnessPorts is the allocator every scenario shares for the life of the test binary.
var harnessPorts portAllocator

// freePortRange returns the first of n consecutive 127.0.0.1 ports that are free right now.
func freePortRange(t *testing.T, n int) int {
	t.Helper()
	return harnessPorts.freeRange(t, n)
}

// freeRange picks n consecutive free ports from outside the kernel's ephemeral range
// (ip_local_port_range). Inside it, any outgoing connection can take one as its source
// port after the probe released it: a keep-alive client connection to the CP then holds
// the agent port for as long as the test keeps polling, and every
// `docker run -p 127.0.0.1:<port>:7700` fails with "failed to bind host port". The kernel
// never auto-assigns a port outside that range, so only an explicit listener can take one
// there.
func (a *portAllocator) freeRange(t *testing.T, n int) int {
	t.Helper()
	lo, hi := nonEphemeralPorts(t)
	if hi-lo+1 < n {
		t.Fatalf("no room for %d ports outside the ephemeral range", n)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.handed == nil {
		a.handed = map[int]bool{}
	}
	for try := 0; try < 200; try++ {
		base := lo + rand.IntN(hi-lo+2-n)
		if !a.free(base, n) {
			continue
		}
		for p := base; p < base+n; p++ {
			a.handed[p] = true
		}
		return base
	}
	t.Fatalf("no %d consecutive free ports found in %d-%d", n, lo, hi)
	return 0
}

func (a *portAllocator) free(base, n int) bool {
	for p := base; p < base+n; p++ {
		if a.handed[p] {
			return false
		}
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			return false
		}
		l.Close()
	}
	return true
}

// lowestPickedPort keeps the picks clear of the well-known and registered service ports
// a developer host is likely to run.
const lowestPickedPort = 10000

// nonEphemeralPorts returns the window [lo, hi] the harness picks ports from: below the
// ephemeral range, or above it when the range starts too low to leave room.
func nonEphemeralPorts(t *testing.T) (lo, hi int) {
	t.Helper()
	elo, ehi := ephemeralPortRange()
	if elo-1-lowestPickedPort >= 1000 {
		return lowestPickedPort, elo - 1
	}
	if 65535-ehi >= 1000 {
		return ehi + 1, 65535
	}
	t.Fatalf("the ephemeral port range %d-%d leaves no room for the harness's ports", elo, ehi)
	return 0, 0
}

// ephemeralPortRange reads Linux's ip_local_port_range. Elsewhere (macOS has no /proc)
// it answers 32768-65535, which covers both the Linux and the IANA/BSD defaults.
func ephemeralPortRange() (lo, hi int) {
	b, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err == nil {
		if f := strings.Fields(string(b)); len(f) == 2 {
			l, err1 := strconv.Atoi(f[0])
			h, err2 := strconv.Atoi(f[1])
			if err1 == nil && err2 == nil && l <= h {
				return l, h
			}
		}
	}
	return 32768, 65535
}

// TestFreePortRangeAvoidsEphemeralPorts: every pick lies outside ip_local_port_range, and
// no two picks overlap. The tests use their own allocator, so repeated runs (-count) never
// use up the window harnessPorts serves the scenarios from.
func TestFreePortRangeAvoidsEphemeralPorts(t *testing.T) {
	var ports portAllocator
	elo, ehi := ephemeralPortRange()
	seen := map[int]bool{}
	for i := 0; i < 50; i++ {
		base := ports.freeRange(t, 4)
		for p := base; p < base+4; p++ {
			if p >= elo && p <= ehi {
				t.Fatalf("pick %d: port %d is inside the ephemeral range %d-%d", i, p, elo, ehi)
			}
			if seen[p] {
				t.Fatalf("pick %d: port %d was already handed out", i, p)
			}
			seen[p] = true
		}
	}
}

// TestClientConnectionsLeaveAgentPortsFree is the docker bind failure itself: connections
// opened after the pick (the test client's keep-alive requests to the CP) must never take
// one of the picked ports as their source port, or `docker run -p` cannot bind it.
func TestClientConnectionsLeaveAgentPortsFree(t *testing.T) {
	srv, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	go func() {
		for {
			c, err := srv.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = c.Read(make([]byte, 1)); c.Close() }()
		}
	}()
	var ports portAllocator
	for i := 0; i < 50; i++ {
		base := ports.freeRange(t, 4)
		var open []net.Conn
		for j := 0; j < 200; j++ {
			c, err := net.Dial("tcp", srv.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			open = append(open, c)
			if lp := c.LocalAddr().(*net.TCPAddr).Port; lp >= base && lp < base+4 {
				t.Errorf("pick %d: a client connection took port %d of the picked range %d-%d", i, lp, base, base+3)
			}
		}
		for _, c := range open {
			c.Close()
		}
	}
}
