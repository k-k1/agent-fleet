package egressagent

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
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
// returns the client side and the client's own address.
func dialRelay(t *testing.T, upstream string, dst netip.AddrPort, dstErr error) (net.Conn, string) {
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
	}
	go r.serve(s.(*net.TCPConn))
	c.SetDeadline(time.Now().Add(5 * time.Second))
	return c, c.LocalAddr().String()
}

func TestRelay(t *testing.T) {
	dst := netip.MustParseAddrPort("203.0.113.7:443")
	t.Run("allowed", func(t *testing.T) {
		sq := newFakeSquid(t, "HTTP/1.1 200 Connection established\r\n\r\n")
		c, local := dialRelay(t, sq.ln.Addr().String(), dst, nil)
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
		c, _ := dialRelay(t, sq.ln.Addr().String(), dst, nil)
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
			c, _ := dialRelay(t, sq.ln.Addr().String(), tc.dst, tc.err)
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

func TestSockaddrIn4(t *testing.T) {
	// AF_INET (2, host order), port 443 (network order), 203.0.113.7, zero padding.
	b := [16]byte{2, 0, 0x01, 0xbb, 203, 0, 113, 7}
	if got := sockaddrIn4(b); got != netip.MustParseAddrPort("203.0.113.7:443") {
		t.Errorf("sockaddrIn4 = %s", got)
	}
}
