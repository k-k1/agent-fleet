package main

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// proxyAgentREST forwards /api/sessions* to the Workspace Agent's /sessions*.
// The Control Plane never talks to tmux directly; it delegates to the Agent.
func (c config) proxyAgentREST(w http.ResponseWriter, r *http.Request) {
	target := c.rt.agentBase() + strings.TrimPrefix(r.URL.Path, "/api")
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		http.Error(w, "bad proxy request", http.StatusBadGateway)
		return
	}
	req.Header = r.Header.Clone()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, "workspace agent unreachable (is the workspace running?)", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

// proxyTerminal bridges the browser terminal WS to the Agent's /ws/pty,
// relaying frames in both directions while preserving message types
// (binary = PTY output, text = input/resize control).
func (c config) proxyTerminal(w http.ResponseWriter, r *http.Request) {
	agentURL := url.URL{Scheme: "ws", Host: c.rt.agentHost + ":" + c.rt.agentPort, Path: "/ws/pty", RawQuery: r.URL.RawQuery}

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	up, _, err := dialer.Dial(agentURL.String(), nil)
	if err != nil {
		http.Error(w, "cannot reach workspace agent terminal", http.StatusBadGateway)
		return
	}
	defer up.Close()

	down, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer down.Close()

	errc := make(chan error, 2)
	go relay(up, down, errc)   // agent -> browser
	go relay(down, up, errc)   // browser -> agent
	<-errc                     // first side to close ends the bridge
}

func relay(src, dst *websocket.Conn, errc chan<- error) {
	for {
		mt, data, err := src.ReadMessage()
		if err != nil {
			errc <- err
			return
		}
		if err := dst.WriteMessage(mt, data); err != nil {
			errc <- err
			return
		}
	}
}
