package routegw

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// The gateway listens on a Unix socket of its own, not on the Agent's mux: the mux is gated by
// AGENT_TOKEN, which is in every session's shell, while a Unix socket names its peer (peer.go).
// A harness that cannot use a socket gets a loopback port of the gateway's own, whose peer is
// read from /proc (peer_tcp.go), and its token is served there only. A listener of its own also
// keeps a model call's streaming and its body buffering off the Agent's control routes.

var def struct {
	mu   sync.RWMutex
	g    *Gateway
	sock string
	loop string
}

// Default is the running gateway, nil before Start and after it is stopped.
func Default() *Gateway {
	def.mu.RLock()
	defer def.mu.RUnlock()
	return def.g
}

func socketPath() string {
	def.mu.RLock()
	defer def.mu.RUnlock()
	return def.sock
}

func loopbackURL() string {
	def.mu.RLock()
	defer def.mu.RUnlock()
	return def.loop
}

// SocketPath is where the gateway listens: a fixed path, so a running pane's socket survives
// an Agent restart (its token does not: tokens are memory only).
func SocketPath() string { return filepath.Join(paths.AgentStateDir(), "route-gateway.sock") }

// maxSocketPath is the room sockaddr_un leaves for a path on Linux (108 bytes with the NUL).
const maxSocketPath = 107

// listenSocket removes what a previous Agent left at path (only a socket: anything else there
// is not ours to delete) and listens there, readable and writable by this user only.
func listenSocket(path string) (net.Listener, error) {
	if len(path) > maxSocketPath {
		return nil, fmt.Errorf("socket path is %d bytes, longer than a Unix socket allows: %s", len(path), path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&fs.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return ln, nil
}

// newServer is the HTTP server over the gateway: every connection carries its peer process.
func newServer(g *Gateway) *http.Server {
	return &http.Server{Handler: g, ReadHeaderTimeout: 15 * time.Second, ConnContext: connContext}
}

// listenLoopback listens on an ephemeral loopback port, for a harness that can only speak TCP
// (peer_tcp.go). The port changes with every Agent start, which costs nothing: tokens do not
// survive a restart either.
func listenLoopback() (net.Listener, error) { return net.Listen("tcp4", "127.0.0.1:0") }

// Start listens on SocketPath and on a loopback port, and serves both in the background. The
// returned function stops them. A loopback port that cannot be had costs only the TCP harnesses
// (IssueLoopback refuses), not the socket. The sweep that renews running sessions' tokens and
// frees ended ones runs with it.
func Start(d Deps) (stop func(), err error) {
	path := SocketPath()
	ln, err := listenSocket(path)
	if err != nil {
		return nil, err
	}
	tcp, terr := listenLoopback()
	loop := ""
	if terr != nil {
		log.Printf("route gateway: no loopback listener: %v", terr)
	} else {
		loop = "http://" + tcp.Addr().String()
	}
	d.Socket = socketPath
	d.Loopback = loopbackURL
	g := New(d)
	srv := newServer(g)
	def.mu.Lock()
	def.g, def.sock, def.loop = g, path, loop
	def.mu.Unlock()
	agents.SetRouteGateway(g)
	done := make(chan struct{})
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("route gateway: %v", err)
		}
	}()
	if tcp != nil {
		go func() {
			if err := srv.Serve(tcp); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("route gateway (loopback): %v", err)
			}
		}()
	}
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				g.Sweep()
			case <-done:
				return
			}
		}
	}()
	return func() {
		close(done)
		agents.SetRouteGateway(nil)
		def.mu.Lock()
		def.g, def.sock, def.loop = nil, "", ""
		def.mu.Unlock()
		_ = srv.Close()
		_ = ln.Close() // unlinks the socket
		if tcp != nil {
			_ = tcp.Close()
		}
	}, nil
}

// EndSession ends every route token of session name at once, on the running gateway if there is
// one. The session's lifecycle exits call it (stop, delete, archive, recreate, a driver or
// route switch), so revocation does not wait for the next call or the sweep.
func EndSession(name string) int {
	if g := Default(); g != nil {
		return g.Registry().RevokeSession(name)
	}
	return 0
}
