package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// scaleBrowserKeepalive shrinks the idle window so a test covers several ping periods.
func scaleBrowserKeepalive(t *testing.T, every, wait time.Duration) {
	t.Helper()
	oe, ow := browserPingEvery, browserPongWait
	browserPingEvery, browserPongWait = every, wait
	t.Cleanup(func() { browserPingEvery, browserPongWait = oe, ow })
}

// startKeepaliveAgent runs a fake Agent that sends nothing (a static page), counts the
// pings it receives and echoes any text frame back.
func startKeepaliveAgent(t *testing.T, pings *atomic.Int32, closed chan struct{}) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		defer close(closed)
		def := conn.PingHandler()
		conn.SetPingHandler(func(s string) error { pings.Add(1); return def(s) })
		for {
			typ, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(typ, data); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func dialBrowserCP(t *testing.T, env browserTestEnv) (*websocket.Conn, func()) {
	t.Helper()
	cp := httptest.NewServer(env.mux)
	wsURL := "ws" + cp.URL[len("http"):] + "/ws/browser?id=page-1&tenant=default"
	c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		cp.Close()
		t.Fatalf("CP dial: %v", err)
	}
	return c, func() { c.Close(); cp.Close() }
}

// An idle pane socket receives pings on both legs, stays open well past the scaled idle
// window, and ordinary frames still pass in between.
func TestBrowserWebSocketPingsKeepIdleSocketOpen(t *testing.T) {
	scaleBrowserKeepalive(t, 40*time.Millisecond, 400*time.Millisecond)
	var agentPings, clientPings atomic.Int32
	closed := make(chan struct{})
	agent := startKeepaliveAgent(t, &agentPings, closed)
	env := newBrowserTestEnv(t, browserTestRuntime{endpoint: agent.URL, state: "running"})
	client, stop := dialBrowserCP(t, env)
	defer stop()
	def := client.PingHandler()
	client.SetPingHandler(func(s string) error { clientPings.Add(1); return def(s) })

	msgs := make(chan string, 8)
	go func() {
		for {
			_, d, err := client.ReadMessage()
			if err != nil {
				close(msgs)
				return
			}
			msgs <- string(d)
		}
	}()

	time.Sleep(1200 * time.Millisecond) // three idle windows
	select {
	case <-closed:
		t.Fatal("agent leg closed while idle")
	default:
	}
	if clientPings.Load() < 5 || agentPings.Load() < 5 {
		t.Fatalf("pings client=%d agent=%d, want >=5 each", clientPings.Load(), agentPings.Load())
	}
	if err := client.WriteMessage(websocket.TextMessage, []byte(`{"type":"mouse"}`)); err != nil {
		t.Fatalf("write after idle: %v", err)
	}
	select {
	case m, ok := <-msgs:
		if !ok || m != `{"type":"mouse"}` {
			t.Fatalf("echo = %q ok=%v", m, ok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no echo after idle")
	}
}

// A client that never reads (so never answers a ping) is dropped after the pong wait,
// and the Agent leg is closed with it.
func TestBrowserWebSocketDropsClientThatNeverPongs(t *testing.T) {
	scaleBrowserKeepalive(t, 50*time.Millisecond, 300*time.Millisecond)
	var agentPings atomic.Int32
	closed := make(chan struct{})
	agent := startKeepaliveAgent(t, &agentPings, closed)
	env := newBrowserTestEnv(t, browserTestRuntime{endpoint: agent.URL, state: "running"})
	_, stop := dialBrowserCP(t, env) // never reads: no pong
	defer stop()
	waitBrowserConns(t, env.mgr.conns, env.workspace.ID, 1) // the bridge was really established
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("silent client was not dropped")
	}
	if agentPings.Load() == 0 {
		t.Fatal("agent leg never received a ping before the drop")
	}
	waitBrowserConns(t, env.mgr.conns, env.workspace.ID, 0)
}

// A peer that keeps the socket alive with its own pings (and never answers ours) is not
// dropped: an inbound ping counts as liveness.
func TestBrowserWebSocketInboundPingsExtendDeadline(t *testing.T) {
	scaleBrowserKeepalive(t, 50*time.Millisecond, 300*time.Millisecond)
	var agentPings atomic.Int32
	closed := make(chan struct{})
	agent := startKeepaliveAgent(t, &agentPings, closed)
	env := newBrowserTestEnv(t, browserTestRuntime{endpoint: agent.URL, state: "running"})
	client, stop := dialBrowserCP(t, env)
	defer stop()
	waitBrowserConns(t, env.mgr.conns, env.workspace.ID, 1)
	for end := time.Now().Add(900 * time.Millisecond); time.Now().Before(end); time.Sleep(60 * time.Millisecond) {
		if err := client.WriteControl(websocket.PingMessage, nil, time.Now().Add(time.Second)); err != nil {
			t.Fatalf("ping: %v", err)
		}
	}
	select {
	case <-closed:
		t.Fatal("socket dropped although the client kept pinging")
	default:
	}
}
