package egressagent

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"net/textproto"
	"strings"
	"time"
)

// RelayListen is where nftables redirects the workspaces' port 443, and RelayUpstream is
// Squid's `http_port ... require-proxy-header ssl-bump` the relay hands each connection to.
// deploy/local/cfn-egress-gateway-test.py holds the gateway's user data to both.
const (
	RelayListen   = ":3130"
	RelayUpstream = "127.0.0.1:3131"
)

// Run as `control-plane egress-relay`, Relay turns an intercepted TLS connection into an
// explicit CONNECT to its original destination address, so Squid treats it like a client
// that uses the proxy: the allow decision is ssl_bump on the SNI, and an allowed connection
// goes to the address the workspace dialled.
//
// Squid's own intercepting https_port cannot do this. Splicing there builds a CONNECT to
// the SNI, checks the destination against Squid's own lookup of that name, and answers a
// mismatch with 409 whatever host_verify_strict says (client_side_request.cc,
// hostHeaderVerifyFailed). Names whose answers rotate (S3, ECR, STS) mismatch most of the
// time, because the workspace and Squid resolve separately and Squid keeps an answer for at
// least negative_dns_ttl.
//
// The PROXY header gives Squid the workspace as the client (src ACLs, %>a in the log), so
// the upstream port must trust it from loopback only (proxy_protocol_access).
type Relay struct {
	Listen   string
	Upstream string
	// OrigDst returns the destination the client dialled before nftables redirected it.
	OrigDst func(*net.TCPConn) (netip.AddrPort, error)
	// Logf reports a connection the relay dropped; nil is log.Printf.
	Logf func(format string, args ...any)
	// MaxConns caps open connections; one past it is closed at once. 0 is defaultMaxConns.
	MaxConns int
	// CloseGrace is how long the client may keep its side open after Squid's side ended;
	// 0 is defaultCloseGrace.
	CloseGrace time.Duration
	// WriteStall is how long one write to either side may block; 0 is defaultWriteStall.
	WriteStall time.Duration
}

// defaultMaxConns stays under the file limit systemd gives the relay (two descriptors per
// connection), so overload refuses new connections instead of failing accept for all.
const defaultMaxConns = 4096

// defaultCloseGrace: Squid closes a tunnel as a whole, so once its side ended the client
// has nowhere left to send. Without a bound, a client that never closes holds the
// connection and its goroutines forever.
const defaultCloseGrace = 10 * time.Second

// defaultWriteStall bounds a write the receiver does not drain: a workspace that stops
// reading would otherwise hold the connection even after Squid closed, since the relay
// never reaches Squid's EOF while it cannot hand on the bytes before it.
const defaultWriteStall = 2 * time.Minute

// upstreamTimeout bounds the dial to Squid and Squid's answer to the CONNECT. The TLS
// handshake after it is Squid's to time out.
const upstreamTimeout = 10 * time.Second

// Run accepts until ctx ends.
func (r Relay) Run(ctx context.Context) error {
	if r.OrigDst == nil {
		r.OrigDst = origDst
	}
	if r.Logf == nil {
		r.Logf = log.Printf
	}
	if r.MaxConns <= 0 {
		r.MaxConns = defaultMaxConns
	}
	sem := make(chan struct{}, r.MaxConns)
	ln, err := net.Listen("tcp4", r.Listen)
	if err != nil {
		return err
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
			r.Logf("egress-relay: accept: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		select {
		case sem <- struct{}{}:
			go func() {
				defer func() { <-sem }()
				r.serve(c.(*net.TCPConn))
			}()
		default:
			r.Logf("egress-relay: %s: %d connections open, refused", c.RemoteAddr(), r.MaxConns)
			c.Close()
		}
	}
}

func (r Relay) serve(c *net.TCPConn) {
	defer c.Close()
	src := c.RemoteAddr().(*net.TCPAddr).AddrPort()
	dst, err := r.OrigDst(c)
	if err != nil {
		r.Logf("egress-relay: %s: no original destination: %v", src, err)
		return
	}
	// A connection made to the relay itself has no other destination to hand on.
	if local := c.LocalAddr().(*net.TCPAddr).AddrPort(); dst == local {
		r.Logf("egress-relay: %s: not a redirected connection", src)
		return
	}
	if !src.Addr().Unmap().Is4() || !dst.Addr().Unmap().Is4() {
		r.Logf("egress-relay: %s -> %s: IPv4 only", src, dst)
		return
	}
	u, err := net.DialTimeout("tcp", r.Upstream, upstreamTimeout)
	if err != nil {
		r.Logf("egress-relay: %s -> %s: %v", src, dst, err)
		return
	}
	defer u.Close()
	br, err := connect(u, src, dst)
	if err != nil {
		r.Logf("egress-relay: %s -> %s: %v", src, dst, err)
		return
	}
	grace := r.CloseGrace
	if grace <= 0 {
		grace = defaultCloseGrace
	}
	stall := r.WriteStall
	if stall <= 0 {
		stall = defaultWriteStall
	}
	pipe(c, u.(*net.TCPConn), br, grace, stall)
}

// connect sends the PROXY header and the CONNECT, and returns the reader positioned after
// Squid's 200. Nothing from the client is sent before that: a refused CONNECT must not
// carry the client's bytes anywhere.
func connect(u net.Conn, src, dst netip.AddrPort) (*bufio.Reader, error) {
	s, d := netip.AddrPortFrom(src.Addr().Unmap(), src.Port()), netip.AddrPortFrom(dst.Addr().Unmap(), dst.Port())
	if err := u.SetDeadline(time.Now().Add(upstreamTimeout)); err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintf(u, "PROXY TCP4 %s %s %d %d\r\nCONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n",
		s.Addr(), d.Addr(), s.Port(), d.Port(), d, d); err != nil {
		return nil, err
	}
	br := bufio.NewReader(u)
	tp := textproto.NewReader(br)
	status, err := tp.ReadLine()
	if err != nil {
		return nil, fmt.Errorf("CONNECT: %w", err)
	}
	if f := strings.Fields(status); len(f) < 2 || !strings.HasPrefix(f[0], "HTTP/1.") || f[1] != "200" {
		return nil, fmt.Errorf("CONNECT refused: %q", status)
	}
	if _, err := tp.ReadMIMEHeader(); err != nil {
		return nil, fmt.Errorf("CONNECT: %w", err)
	}
	return br, u.SetDeadline(time.Time{})
}

// pipe copies both ways. The client's half-close is passed on and Squid's answer still
// flows back for as long as Squid keeps its side open (Squid's own timeouts bound that).
// Once Squid's side ended, the client gets grace to finish; a write stalled past stall, or
// any other error either way, closes both.
func pipe(c, u *net.TCPConn, fromU io.Reader, grace, stall time.Duration) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := copyStall(u, c, stall); err != nil {
			c.Close()
			u.Close()
			return
		}
		u.CloseWrite()
	}()
	if err := copyStall(c, fromU, stall); err != nil {
		c.Close()
		u.Close()
	} else {
		c.CloseWrite()
		c.SetReadDeadline(time.Now().Add(grace))
	}
	<-done
}

// copyStall copies until src's EOF, giving each write at most stall.
func copyStall(dst *net.TCPConn, src io.Reader, stall time.Duration) error {
	buf := make([]byte, 32<<10)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if err := dst.SetWriteDeadline(time.Now().Add(stall)); err != nil {
				return err
			}
			if _, err := dst.Write(buf[:n]); err != nil {
				return err
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}
