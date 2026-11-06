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

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// The gateway listens on a Unix socket of its own, not on the Agent's mux and not on a TCP
// port: the mux is gated by AGENT_TOKEN, which is in every session's shell, and a TCP peer
// cannot be told apart from another session's process without guessing, while a Unix socket
// names its peer (peer.go). A listener of its own also keeps a model call's streaming and its
// body buffering off the Agent's control routes.

var def struct {
	mu   sync.RWMutex
	g    *Gateway
	sock string
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

// Start listens on SocketPath and serves in the background. The returned function stops it.
// The sweep that renews running sessions' tokens and frees ended ones runs with it.
func Start(d Deps) (stop func(), err error) {
	path := SocketPath()
	ln, err := listenSocket(path)
	if err != nil {
		return nil, err
	}
	d.Socket = socketPath
	g := New(d)
	srv := newServer(g)
	def.mu.Lock()
	def.g, def.sock = g, path
	def.mu.Unlock()
	done := make(chan struct{})
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("route gateway: %v", err)
		}
	}()
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
		def.mu.Lock()
		def.g, def.sock = nil, ""
		def.mu.Unlock()
		_ = srv.Close()
		_ = ln.Close() // unlinks the socket
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
