package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/egressallow"
)

// fakeDNS answers every A query with ip, on a loopback UDP socket, and returns a resolver
// that sends its lookups there. It stands in for a name whose answer is not what a
// separate pre-check lookup saw.
func fakeDNS(t *testing.T, ip [4]byte) *net.Resolver {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < 12 {
				continue
			}
			q := buf[12:n]
			end := 0
			for end < len(q) && q[end] != 0 {
				end += int(q[end]) + 1
			}
			end += 5 // root label + QTYPE + QCLASS
			if end > len(q) {
				continue
			}
			qtype := uint16(q[end-4])<<8 | uint16(q[end-3])
			resp := append([]byte{buf[0], buf[1], 0x81, 0x80, 0, 1, 0, 0, 0, 0, 0, 0}, q[:end]...)
			if qtype == 1 {
				resp[7] = 1
				resp = append(resp, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 0, 0, 4, ip[0], ip[1], ip[2], ip[3])
			}
			_, _ = pc.WriteTo(resp, addr)
		}
	}()
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "udp", pc.LocalAddr().String())
	}}
}

func TestEgressDialControlRefusedRanges(t *testing.T) {
	refused := []string{
		"127.0.0.1:80", "[::1]:80", "0.0.0.0:80", "169.254.169.254:80", "[fe80::1%eth0]:80",
		"[::ffff:127.0.0.1]:80", "[::ffff:169.254.169.254]:80", "[ff02::1]:80", "not-an-address",
	}
	for _, a := range refused {
		if err := egressallow.ProxyDialControl("tcp", a, nil); !errors.Is(err, egressallow.ErrRefusedAddr) {
			t.Errorf("%s: want refused, got %v", a, err)
		}
	}
	// Private ranges stay reachable through the proxy (internal Git, MCP, cluster services).
	for _, a := range []string{"93.184.216.34:443", "[2606:4700::1111]:443", "203.0.113.7:80",
		"10.1.2.3:80", "172.16.0.1:80", "192.168.1.1:80", "[fd00::1]:80", "100.64.0.1:80"} {
		if err := egressallow.ProxyDialControl("tcp", a, nil); err != nil {
			t.Errorf("%s: want allowed, got %v", a, err)
		}
	}
}

// A name resolved by the dialer to a loopback address is refused at connect time, in both
// the CONNECT and the plain-HTTP path, and the listener behind it never sees a connection.
func TestEgressProxyRefusesAtDialTime(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, "reached")
	}))
	defer up.Close()
	port := up.Listener.Addr().(*net.TCPAddr).Port

	d := newEgressDialer()
	d.Resolver = fakeDNS(t, [4]byte{127, 0, 0, 1})
	rt := newEgressTransport()
	rt.DialContext = d.DialContext
	p := &egressProxy{batch: newEgressBatcher("", ""), dial: d, rt: rt}
	p.cur.Store(&proxyPolicy{policy: newEgressPolicy([]string{"*"}), enforce: false})
	ps := httptest.NewServer(http.HandlerFunc(p.handle))
	defer ps.Close()

	host := "rebind.example.test:" + strconv.Itoa(port)
	conn, err := net.Dial("tcp", ps.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "CONNECT "+host+" HTTP/1.1\r\nHost: "+host+"\r\n\r\n")
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	b, _ := io.ReadAll(conn)
	if !strings.Contains(string(b), "403") {
		t.Fatalf("CONNECT: want 403, got %q", b)
	}

	req, _ := http.NewRequest("GET", "http://"+host+"/", nil)
	pu, _ := url.Parse(ps.URL)
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}, Timeout: 5 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("HTTP: want 403, got %d", resp.StatusCode)
	}
	if hits.Load() != 0 {
		t.Fatalf("the refused destination was reached %d time(s)", hits.Load())
	}
}

// The relay plumbing itself still works for a destination the control hook lets through
// (the hook is lifted here only so a loopback listener can stand in for a public host).
func TestEgressProxyRelaysAllowedDestination(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "reached")
	}))
	defer up.Close()
	d := newEgressDialer()
	d.Control = nil
	rt := newEgressTransport()
	rt.DialContext = d.DialContext
	p := &egressProxy{batch: newEgressBatcher("", ""), dial: d, rt: rt}
	p.cur.Store(&proxyPolicy{policy: newEgressPolicy([]string{"*"}), enforce: false})
	ps := httptest.NewServer(http.HandlerFunc(p.handle))
	defer ps.Close()
	pu, _ := url.Parse(ps.URL)

	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}, Timeout: 5 * time.Second}
	resp, err := c.Get(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "reached" {
		t.Fatalf("HTTP: got %q", b)
	}

	conn, err := net.Dial("tcp", ps.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	h := up.Listener.Addr().String()
	_, _ = io.WriteString(conn, "CONNECT "+h+" HTTP/1.1\r\nHost: "+h+"\r\n\r\n")
	br := bufio.NewReader(conn)
	line, _ := br.ReadString('\n')
	if !strings.Contains(line, "200") {
		t.Fatalf("CONNECT: got %q", line)
	}
}

// The look-ahead used by engine ingest keeps the proxy's scope: internal HTTPS mirrors on
// private addresses stay acceptable, loopback and metadata do not.
func TestInternalEgressDestKeepsProxyScope(t *testing.T) {
	for host, want := range map[string]bool{"10.0.0.5": false, "192.168.1.9": false, "93.184.216.34": false,
		"127.0.0.1": true, "169.254.169.254": true, "0.0.0.0": true} {
		if got := internalEgressDest(host); got != want {
			t.Errorf("internalEgressDest(%s) = %v, want %v", host, got, want)
		}
	}
}
