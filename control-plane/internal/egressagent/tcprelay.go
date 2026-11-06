package egressagent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/egressallow"
)

// TCPRelayListen is where nftables redirects the workspaces' TCP that is neither 80, 443
// nor the explicit proxy's 3128. deploy/local/cfn-egress-gateway-test.py holds the
// gateway's user data to it.
const TCPRelayListen = ":3132"

// fencedNets are destinations a passthrough never reaches, whatever DNS says. A name the
// organisation lists can be made to resolve to anything by whoever runs its zone, and
// without this fence that is a way to the instance metadata service or the VPC.
var fencedNets = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{"100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15", "240.0.0.0/4"} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// fencedAddr reports an address no passthrough may reach: anything that is not a global
// unicast IPv4 address, and the private and shared ranges.
func fencedAddr(a netip.Addr) bool {
	a = a.Unmap()
	if egressallow.TCPAddrRefused(a) {
		return true
	}
	for _, p := range fencedNets {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// TCPRelay is `control-plane egress-relay --mode tcp`: it passes a workspace's TCP
// connection to a destination the policy names (tcp: entries, resolved by the agent into
// Table), and decides every other one by the deployment's mode. Each connection is
// recorded when it is decided, in the af2 log the agent ships.
//
// Unlike Relay it does not hand the connection to Squid: there is no HTTP to read. The
// decision is by address, as ADR 0122's Open point 4 says of SSH, and a refused
// connection is closed before anything is dialled.
type TCPRelay struct {
	Listen string
	// Listener, when set, is used instead of listening on Listen (tests pass one).
	Listener net.Listener
	// Table is the file the agent writes (tcptable.go). Missing or unreadable means no
	// policy yet: everything is refused, as the gateway's bootstrap files do for HTTP.
	Table string
	// Log is the af2 log; every decision is appended to it. A connection that cannot be
	// recorded is not passed.
	Log *TCPLog
	// Fence lists extra destination networks to refuse (the VPC's own CIDR).
	Fence []netip.Prefix
	// OrigDst returns the destination the client dialled before nftables redirected it.
	OrigDst func(*net.TCPConn) (netip.AddrPort, error)
	// Dial opens the connection to the destination; nil dials TCP/IPv4 with a timeout.
	Dial func(ctx context.Context, dst netip.AddrPort) (net.Conn, error)
	// Local reports whether an address is one of this host's; nil reads the interfaces.
	Local func(netip.Addr) bool
	Logf  func(format string, args ...any)
	// MaxConns caps open connections; 0 is defaultMaxConns. A connection past it is
	// refused and recorded with the verdict deny-cap.
	MaxConns int
	// HalfClose is how long one direction may stay open after the other ended; 0 is
	// defaultHalfClose. A peer that does not close after the client has gone would
	// otherwise hold its connection slot for ever.
	HalfClose  time.Duration
	WriteStall time.Duration
	Now        func() time.Time

	mu       sync.Mutex
	table    tcpTable
	tableMod time.Time
	tableSz  int64
	tableAt  time.Time
	loaded   bool
}

// tcpTableCheck is how often the table file is looked at; a change is picked up within it.
const tcpTableCheck = time.Second

// current returns the table, re-read when the file changed. An unreadable file after a good
// read keeps the last good table: a half-written file cannot happen (the agent renames into
// place), and losing the table to a transient error would drop every connection.
func (r *TCPRelay) current() (tcpTable, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if r.loaded && now.Sub(r.tableAt) < tcpTableCheck {
		return r.table, true
	}
	r.tableAt = now
	fi, err := os.Stat(r.Table)
	if err != nil {
		return r.table, r.loaded
	}
	if r.loaded && fi.ModTime().Equal(r.tableMod) && fi.Size() == r.tableSz {
		return r.table, true
	}
	b, err := os.ReadFile(r.Table)
	if err != nil {
		return r.table, r.loaded
	}
	var t tcpTable
	if err := json.Unmarshal(b, &t); err != nil {
		r.logf("egress-relay: %s does not decode, keeping the previous table: %v", r.Table, err)
		return r.table, r.loaded
	}
	r.table, r.tableMod, r.tableSz, r.loaded = t, fi.ModTime(), fi.Size(), true
	return t, true
}

func (r *TCPRelay) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *TCPRelay) logf(f string, a ...any) {
	if r.Logf != nil {
		r.Logf(f, a...)
		return
	}
	log.Printf(f, a...)
}

// Decision is what the relay does with one connection.
type Decision struct {
	Verdict string // one of the Verdict* constants
	Entry   string // the host of the entry that admitted it, or ""
	Pass    bool   // whether the bytes are passed
}

// Decide is the whole policy for one destination.
func (r *TCPRelay) Decide(dst netip.AddrPort) Decision {
	a := dst.Addr().Unmap()
	if fencedAddr(a) || egressallow.TCPRefusedPort(int(dst.Port())) || r.fenced(a) || r.isLocal(a) {
		return Decision{Verdict: VerdictDeny}
	}
	t, ok := r.current()
	if ok {
		var names []string
		for _, e := range t.Entries {
			if int(dst.Port()) < e.From || int(dst.Port()) > e.To {
				continue
			}
			for _, s := range e.Addrs {
				if x, err := netip.ParseAddr(s); err == nil && x.Unmap() == a {
					names = append(names, e.Host)
					break
				}
			}
		}
		if len(names) > 0 {
			sort.Strings(names)
			return Decision{Verdict: VerdictAdmit, Entry: names[0], Pass: true}
		}
		if !t.Enforce {
			return Decision{Verdict: VerdictWouldDeny, Pass: true}
		}
	}
	return Decision{Verdict: VerdictDeny}
}

func (r *TCPRelay) fenced(a netip.Addr) bool {
	for _, p := range r.Fence {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func (r *TCPRelay) isLocal(a netip.Addr) bool {
	if r.Local != nil {
		return r.Local(a)
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return true // when in doubt, refuse
	}
	for _, x := range addrs {
		if p, err := netip.ParsePrefix(x.String()); err == nil && p.Addr().Unmap() == a {
			return true
		}
	}
	return false
}

// Run accepts until ctx ends.
func (r *TCPRelay) Run(ctx context.Context) error {
	if r.OrigDst == nil {
		r.OrigDst = origDst
	}
	if r.MaxConns <= 0 {
		r.MaxConns = defaultMaxConns
	}
	sem := make(chan struct{}, r.MaxConns)
	ln := r.Listener
	if ln == nil {
		var err error
		if ln, err = net.Listen("tcp4", r.Listen); err != nil {
			return err
		}
	}
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			r.logf("egress-relay: tcp accept: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		select {
		case sem <- struct{}{}:
			go func() {
				defer func() { <-sem }()
				r.serve(ctx, c.(*net.TCPConn))
			}()
		default:
			// Recorded like any other refusal: the connection happened.
			r.logf("egress-relay: tcp: %s: %d connections open, refused", c.RemoteAddr(), r.MaxConns)
			r.refuseCap(c.(*net.TCPConn))
		}
	}
}

func (r *TCPRelay) refuseCap(c *net.TCPConn) {
	defer c.Close()
	src := c.RemoteAddr().(*net.TCPAddr).AddrPort()
	if dst, err := r.OrigDst(c); err == nil {
		if err := r.Log.Record(r.now(), src.Addr(), dst, VerdictDenyCap, ""); err != nil {
			r.logf("egress-relay: tcp: recording %s -> %s: %v", src, dst, err)
		}
	}
}

func (r *TCPRelay) serve(ctx context.Context, c *net.TCPConn) {
	defer c.Close()
	src := c.RemoteAddr().(*net.TCPAddr).AddrPort()
	dst, err := r.OrigDst(c)
	if err != nil {
		r.logf("egress-relay: tcp: %s: no original destination: %v", src, err)
		return
	}
	if local := c.LocalAddr().(*net.TCPAddr).AddrPort(); dst == local {
		r.logf("egress-relay: tcp: %s: not a redirected connection", src)
		return
	}
	if !src.Addr().Unmap().Is4() || !dst.Addr().Unmap().Is4() {
		r.logf("egress-relay: tcp: %s -> %s: IPv4 only", src, dst)
		return
	}
	d := r.Decide(dst)
	if err := r.Log.Record(r.now(), src.Addr(), dst, d.Verdict, d.Entry); err != nil {
		// A connection that cannot be recorded is not passed: the record is the point.
		r.logf("egress-relay: tcp: %s -> %s: not recorded, refused: %v", src, dst, err)
		return
	}
	if !d.Pass {
		return
	}
	dial := r.Dial
	if dial == nil {
		dial = func(ctx context.Context, dst netip.AddrPort) (net.Conn, error) {
			return (&net.Dialer{Timeout: upstreamTimeout}).DialContext(ctx, "tcp4", dst.String())
		}
	}
	u, err := dial(ctx, dst)
	if err != nil {
		r.logf("egress-relay: tcp: %s -> %s: %v", src, dst, err)
		return
	}
	defer u.Close()
	ut, ok := u.(*net.TCPConn)
	if !ok {
		return
	}
	half, stall := r.HalfClose, r.WriteStall
	if half <= 0 {
		half = defaultHalfClose
	}
	if stall <= 0 {
		stall = defaultWriteStall
	}
	pipeTCP(c, ut, half, stall)
}

// defaultHalfClose is long enough for a command's answer after the client sent its last
// byte (SSH, a database query) and short enough that a peer which never closes cannot
// keep a slot for ever.
const defaultHalfClose = 5 * time.Minute

// pipeTCP copies both ways. A direction that ended cleanly is passed on as a half-close
// and the other keeps flowing, for at most halfClose; any error, or the deadline, closes
// both. Unlike pipe (for the Squid side, whose own timeouts end a tunnel) a raw TCP peer
// has no one else to end the connection.
func pipeTCP(c, u *net.TCPConn, halfClose, stall time.Duration) {
	done := make(chan error, 2)
	go func() {
		err := copyStall(u, c, stall)
		if err == nil {
			u.CloseWrite()
		}
		done <- err
	}()
	go func() {
		err := copyStall(c, u, stall)
		if err == nil {
			c.CloseWrite()
		}
		done <- err
	}()
	if err := <-done; err != nil {
		c.Close()
		u.Close()
		<-done
		return
	}
	t := time.NewTimer(halfClose)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		c.Close()
		u.Close()
		<-done
	}
}

// TCPLog appends af2 lines to a file and rotates it by size, renaming to .0, .1, ... the
// way Squid does, which is what the agent follows.
type TCPLog struct {
	Path string
	// MaxBytes triggers a rotation; 0 is 64 MiB. Keep is the number of rotated files; 0 is 10.
	MaxBytes int64
	Keep     int

	mu   sync.Mutex
	f    *os.File
	size int64
}

// Record writes one line. It returns an error when the line did not reach the file.
func (l *TCPLog) Record(at time.Time, src netip.Addr, dst netip.AddrPort, verdict, entry string) error {
	if entry == "" {
		entry = "-"
	}
	ms := at.UnixMilli()
	line := fmt.Sprintf("%s %d.%03d %s %s %d %s %s\n", tcpFormatVersion, ms/1000, ms%1000,
		src.Unmap(), dst.Addr().Unmap(), dst.Port(), verdict, entry)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		f, err := os.OpenFile(l.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
		if err != nil {
			return err
		}
		fi, err := f.Stat()
		if err != nil {
			f.Close()
			return err
		}
		l.f, l.size = f, fi.Size()
	}
	max := l.MaxBytes
	if max <= 0 {
		max = 64 << 20
	}
	if l.size >= max {
		if err := l.rotate(); err != nil {
			return err
		}
	}
	n, err := l.f.WriteString(line)
	l.size += int64(n)
	return err
}

func (l *TCPLog) rotate() error {
	keep := l.Keep
	if keep <= 0 {
		keep = 10
	}
	l.f.Close()
	l.f = nil
	_ = os.Remove(fmt.Sprintf("%s.%d", l.Path, keep-1))
	for i := keep - 2; i >= 0; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", l.Path, i), fmt.Sprintf("%s.%d", l.Path, i+1))
	}
	if err := os.Rename(l.Path, l.Path+".0"); err != nil {
		return err
	}
	f, err := os.OpenFile(l.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	l.f, l.size = f, 0
	return nil
}

// Close closes the file.
func (l *TCPLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
