package routegw

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
)

// The gateway listens on its own loopback port, not on the Agent's mux: the mux is gated by
// AGENT_TOKEN, which is in every session's shell and is the Control Plane's credential, while
// this port is gated by a route token that is bound to one session. A listener of its own also
// keeps a model call's streaming and its body buffering off the Agent's control routes.

var def struct {
	mu   sync.RWMutex
	g    *Gateway
	port int
}

// Default is the running gateway, nil before Start and after it is stopped.
func Default() *Gateway {
	def.mu.RLock()
	defer def.mu.RUnlock()
	return def.g
}

func baseURL() string {
	def.mu.RLock()
	defer def.mu.RUnlock()
	if def.port == 0 {
		return ""
	}
	return "http://127.0.0.1:" + strconv.Itoa(def.port)
}

type serverState struct {
	Port int `json:"port"`
}

func statePath() string { return filepath.Join(paths.AgentStateDir(), "route-gateway.json") }

// Start opens the loopback listener, preferring the port used last time so a running pane's
// base URL survives an Agent restart (its token does not: tokens are memory only), and serves in
// the background. The returned function stops it. The sweep that frees ended sessions' tokens
// runs with it.
func Start(d Deps) (stop func(), err error) {
	d.BaseURL = baseURL
	g := New(d)
	var ln net.Listener
	var st serverState
	if b, rerr := os.ReadFile(statePath()); rerr == nil && json.Unmarshal(b, &st) == nil && st.Port > 0 {
		ln, _ = net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(st.Port))
	}
	if ln == nil {
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return nil, err
		}
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if b, merr := json.Marshal(serverState{Port: port}); merr == nil {
		_ = os.MkdirAll(filepath.Dir(statePath()), 0o700)
		_ = os.WriteFile(statePath(), b, 0o600)
	}
	srv := &http.Server{Handler: g, ReadHeaderTimeout: 15 * time.Second}
	def.mu.Lock()
	def.g, def.port = g, port
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
		def.g, def.port = nil, 0
		def.mu.Unlock()
		_ = srv.Close()
		_ = ln.Close()
	}, nil
}
