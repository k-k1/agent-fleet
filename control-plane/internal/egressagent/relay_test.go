package egressagent

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSquid accepts one connection, records what arrived before the blank line that ends
// the CONNECT, answers with reply, and then (on a 200) echoes "srv:"+what the client sent.
type fakeSquid struct {
	ln    net.Listener
	reply string
	head  chan string
	rest  chan string
}

func newFakeSquid(t *testing.T, reply string) *fakeSquid {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &fakeSquid{ln: ln, reply: reply, head: make(chan string, 1), rest: make(chan string, 1)}
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		br := bufio.NewReader(c)
		var head strings.Builder
		for {
			line, err := br.ReadString('\n')
			head.WriteString(line)
			if err != nil || line == "\r\n" {
				break
			}
		}
		f.head <- head.String()
		io.WriteString(c, reply)
		if !strings.Contains(reply, " 200 ") {
			b, _ := io.ReadAll(br)
			f.rest <- string(b)
			return
		}
		b, _ := io.ReadAll(br) // until the relay passes the client's half-close on
		f.rest <- string(b)
		io.WriteString(c, "srv:"+string(b))
	}()
	return f
}

// dialRelay connects a client to Relay.serve with OrigDst returning dst (or err), and
// returns the client side, the client's own address, and a channel closed when serve returned.
func dialRelay(t *testing.T, upstream string, dst netip.AddrPort, dstErr error) (net.Conn, string, chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c, err := net.Dial("tcp4", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	s, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	r := Relay{
		Upstream: upstream,
		OrigDst:  func(*net.TCPConn) (netip.AddrPort, error) { return dst, dstErr },
		Logf:     t.Logf,
		// Short, so a test that relies on it fails fast; longer than any wait below.
		CloseGrace: 300 * time.Millisecond,
	}
	done := make(chan struct{})
	go func() { r.serve(s.(*net.TCPConn)); close(done) }()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	return c, c.LocalAddr().String(), done
}

func TestRelay(t *testing.T) {
	dst := netip.MustParseAddrPort("203.0.113.7:443")
	t.Run("allowed", func(t *testing.T) {
		sq := newFakeSquid(t, "HTTP/1.1 200 Connection established\r\n\r\n")
		c, local, _ := dialRelay(t, sq.ln.Addr().String(), dst, nil)
		io.WriteString(c, "client-hello")
		c.(*net.TCPConn).CloseWrite()
		got, _ := io.ReadAll(c)
		want := "PROXY TCP4 127.0.0.1 203.0.113.7 " + portOf(local) + " 443\r\n" +
			"CONNECT 203.0.113.7:443 HTTP/1.1\r\nHost: 203.0.113.7:443\r\n\r\n"
		if h := <-sq.head; h != want {
			t.Errorf("Squid got\n%q\nwant\n%q", h, want)
		}
		if r := <-sq.rest; r != "client-hello" {
			t.Errorf("Squid got tunnel bytes %q", r)
		}
		if string(got) != "srv:client-hello" {
			t.Errorf("client got %q", got)
		}
	})
	t.Run("refused", func(t *testing.T) {
		sq := newFakeSquid(t, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
		c, _, _ := dialRelay(t, sq.ln.Addr().String(), dst, nil)
		io.WriteString(c, "client-hello")
		got, _ := io.ReadAll(c)
		<-sq.head
		if r := <-sq.rest; r != "" {
			t.Errorf("a refused CONNECT carried the client's bytes: %q", r)
		}
		if len(got) != 0 {
			t.Errorf("client got %q from a refused CONNECT", got)
		}
	})
	t.Run("relay's own address", func(t *testing.T) {
		sq := newFakeSquid(t, "HTTP/1.1 200 Connection established\r\n\r\n")
		ln, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		self := netip.MustParseAddrPort(ln.Addr().String())
		c, err := net.Dial("tcp4", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		s, _ := ln.Accept()
		r := Relay{Upstream: sq.ln.Addr().String(), Logf: t.Logf,
			OrigDst: func(*net.TCPConn) (netip.AddrPort, error) { return self, nil }}
		go r.serve(s.(*net.TCPConn))
		c.SetDeadline(time.Now().Add(5 * time.Second))
		if got, _ := io.ReadAll(c); len(got) != 0 {
			t.Errorf("client got %q", got)
		}
		select {
		case h := <-sq.head:
			t.Errorf("Squid was dialled: %q", h)
		case <-time.After(100 * time.Millisecond):
		}
	})
	for name, tc := range map[string]struct {
		dst netip.AddrPort
		err error
	}{
		"no original destination": {err: errors.New("ENOENT")},
		"IPv6 destination":        {dst: netip.MustParseAddrPort("[2001:db8::1]:443")},
	} {
		t.Run(name, func(t *testing.T) {
			sq := newFakeSquid(t, "HTTP/1.1 200 Connection established\r\n\r\n")
			c, _, _ := dialRelay(t, sq.ln.Addr().String(), tc.dst, tc.err)
			got, _ := io.ReadAll(c)
			if len(got) != 0 {
				t.Errorf("client got %q", got)
			}
			select {
			case h := <-sq.head:
				t.Errorf("Squid was dialled: %q", h)
			case <-time.After(100 * time.Millisecond):
			}
		})
	}
}

func portOf(addr string) string {
	_, p, _ := net.SplitHostPort(addr)
	return p
}

// rawSquid answers the CONNECT with 200 and then runs fn on the tunnel.
func rawSquid(t *testing.T, fn func(c net.Conn, br *bufio.Reader)) string {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		br := bufio.NewReader(c)
		for {
			line, err := br.ReadString('\n')
			if err != nil || line == "\r\n" {
				break
			}
		}
		io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
		fn(c, br)
	}()
	return ln.Addr().String()
}

// After the client's half-close, Squid's answer still comes back, however long after the
// close grace it takes: the grace starts only when Squid's side ended.
func TestRelayAnswerAfterClientHalfClose(t *testing.T) {
	up := rawSquid(t, func(c net.Conn, br *bufio.Reader) {
		b, _ := io.ReadAll(br)
		time.Sleep(time.Second)
		io.WriteString(c, "late:"+string(b))
	})
	c, _, done := dialRelay(t, up, netip.MustParseAddrPort("203.0.113.7:443"), nil)
	io.WriteString(c, "req")
	c.(*net.TCPConn).CloseWrite()
	if got, _ := io.ReadAll(c); string(got) != "late:req" {
		t.Errorf("client got %q", got)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("serve did not return after both sides ended")
	}
}

// Squid closed the tunnel and the client neither sends nor closes: the relay must still
// let go of the connection, within the close grace.
func TestRelayReleasesSilentClientAfterSquidClosed(t *testing.T) {
	up := rawSquid(t, func(net.Conn, *bufio.Reader) {})
	c, _, done := dialRelay(t, up, netip.MustParseAddrPort("203.0.113.7:443"), nil)
	if got, err := io.ReadAll(c); err != nil || len(got) != 0 {
		t.Fatalf("client got %q, %v; want a clean EOF", got, err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serve still holds a client that never closes after Squid closed")
	}
}

// One connection past MaxConns is closed at once, without reaching Squid.
func TestRelayMaxConns(t *testing.T) {
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	up := rawSquid(t, func(net.Conn, *bufio.Reader) { <-hold })
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	var logged []string
	var mu sync.Mutex
	r := Relay{Listen: addr, Upstream: up, MaxConns: 1,
		OrigDst: func(*net.TCPConn) (netip.AddrPort, error) { return netip.MustParseAddrPort("203.0.113.7:443"), nil },
		Logf:    func(f string, a ...any) { mu.Lock(); logged = append(logged, f); mu.Unlock() }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)
	var first net.Conn
	for i := 0; i < 100; i++ {
		if first, err = net.Dial("tcp4", addr); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if first == nil {
		t.Fatal("relay did not start")
	}
	defer first.Close()
	time.Sleep(100 * time.Millisecond) // the first one now holds the only slot
	second, err := net.Dial("tcp4", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	second.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := second.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("connection past MaxConns was not closed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logged) != 1 || !strings.Contains(logged[0], "refused") {
		t.Errorf("logged %q", logged)
	}
}
