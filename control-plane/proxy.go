package main

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// auditActionTarget classifies a proxied request as an auditable CHANGE operation
// (M1 audit, docs/20 §A / §E.1: file, git, session mutations). Read-only and other
// non-mutating calls return ok=false. Target is taken from the URL only (path value
// or query), never the request body — so no secrets are read (docs §A.6).
func auditActionTarget(r *http.Request) (action, target string, ok bool) {
	p := r.URL.Path
	q := r.URL.Query()
	name := r.PathValue("name") // repo / session name for {name} routes ("" otherwise)
	switch r.Method {
	case http.MethodPost:
		switch {
		case p == "/api/fs/upload":
			return "fs.upload", q.Get("path"), true
		case p == "/api/fs/mkdir":
			return "fs.mkdir", q.Get("path"), true
		case p == "/api/fs/newfile":
			return "fs.newfile", q.Get("path"), true
		case p == "/api/fs/rename":
			return "fs.rename", q.Get("from") + " → " + q.Get("to"), true
		case p == "/api/repos":
			return "repo.clone", "", true
		case name != "" && strings.HasSuffix(p, "/commit"):
			return "git.commit", name, true
		case name != "" && strings.HasSuffix(p, "/discard"):
			return "git.discard", name, true
		case name != "" && strings.HasSuffix(p, "/checkout"):
			return "git.checkout", name, true
		case name != "" && strings.HasSuffix(p, "/fetch"):
			return "git.fetch", name, true
		case name != "" && strings.HasSuffix(p, "/ff"):
			return "git.ff", name, true
		case p == "/api/sessions":
			return "session.create", "", true
		case name != "" && strings.HasSuffix(p, "/fork"):
			return "session.fork", name, true
		case name != "" && strings.HasSuffix(p, "/stop"):
			return "session.stop", name, true
		}
	case http.MethodDelete:
		switch {
		case p == "/api/fs/delete":
			return "fs.delete", q.Get("path"), true
		case name != "" && p == "/api/repos/"+name:
			return "repo.delete", name, true
		}
	}
	return "", "", false
}

// proxyAgentREST forwards /api/sessions* to the Workspace Agent's /sessions*.
// The Control Plane never talks to tmux directly; it delegates to the Agent.
func (c config) proxyAgentREST(w http.ResponseWriter, r *http.Request) {
	res, ok := c.resolvedFor(w, r)
	if !ok {
		return
	}
	rt := res.rt
	// P3-9: only mutating calls (session input/create, repo clone, connections…)
	// count as activity. Background GET polling (session list, workspace state)
	// must NOT keep a workspace warm, or a left-open tab would defeat idle-stop —
	// real presence is instead signalled by an attached terminal or a busy session.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		c.mgr.conns.touch(res.ws.ID)
	}
	target := rt.Endpoint() + strings.TrimPrefix(r.URL.Path, "/api")
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		http.Error(w, "bad proxy request", http.StatusBadGateway)
		return
	}
	req.Header = r.Header.Clone()
	if rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+rt.Token()) // CP↔Agent auth
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, "workspace agent unreachable (is the workspace running?)", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// M1 audit (docs/20): record a successful change operation. actor/tenant come from
	// the resolved request; best-effort with a detached context so a client disconnect
	// during the body copy below can't cancel the write.
	if action, target, ok := auditActionTarget(r); ok && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_ = c.mgr.store.InsertAudit(context.Background(), AuditLog{
			ID: newID(), TenantID: res.ws.TenantID, ActorKind: "user", ActorID: res.ident.ID,
			Action: action, Target: target, At: nowTS(),
		})
	}

	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// proxyAgentStream is proxyAgentREST for a streaming (SSE) endpoint: it forwards to
// the Agent and copies the response back FLUSHING after each chunk, so token deltas
// reach the browser as they arrive instead of buffering in net/http's ~4KB writer.
func (c config) proxyAgentStream(w http.ResponseWriter, r *http.Request) {
	res, ok := c.resolvedFor(w, r)
	if !ok {
		return
	}
	rt := res.rt
	c.mgr.conns.touch(res.ws.ID) // a chat turn is real activity (POST)
	target := rt.Endpoint() + strings.TrimPrefix(r.URL.Path, "/api")
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		http.Error(w, "bad proxy request", http.StatusBadGateway)
		return
	}
	req.Header = r.Header.Clone()
	if rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+rt.Token())
	}
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
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return // client gone
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			return
		}
	}
}

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

// proxyTerminal bridges the browser terminal WS to the Agent's /ws/pty,
// relaying frames in both directions while preserving message types
// (binary = PTY output, text = input/resize control).
func (c config) proxyTerminal(w http.ResponseWriter, r *http.Request) {
	res, ok := c.resolvedFor(w, r)
	if !ok {
		return
	}
	rt := res.rt
	// P3-9: an attached terminal keeps the workspace warm and pins its session
	// (tier 1 won't halt a session someone is watching).
	session := r.URL.Query().Get("session")
	c.mgr.conns.addConn(res.ws.ID, session)
	defer c.mgr.conns.doneConn(res.ws.ID, session)
	// No auto-start: opening a terminal must NOT boot a stopped workspace. Otherwise a
	// mere session click (which opens /ws/pty) would silently revive the whole WS. A
	// stopped workspace is brought up only by the explicit WORKSPACE Start control;
	// here we fail fast so the terminal stays down and the user resumes on purpose.
	if rt.State(r.Context()) != "running" {
		writeAPIErr(w, &apiError{http.StatusConflict, "workspace_stopped",
			"workspace is stopped — start it first"})
		return
	}
	// Derive the Agent's ws:// URL from its (http) Endpoint so the reachability
	// detail (host:port locally, Service Connect on ECS) stays behind the port.
	base, err := url.Parse(rt.Endpoint())
	if err != nil {
		http.Error(w, "bad agent endpoint", http.StatusBadGateway)
		return
	}
	agentURL := url.URL{Scheme: "ws", Host: base.Host, Path: "/ws/pty", RawQuery: r.URL.RawQuery}

	var hdr http.Header
	if rt.Token() != "" {
		hdr = http.Header{"Authorization": []string{"Bearer " + rt.Token()}} // CP↔Agent auth
	}
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	up, _, err := dialer.Dial(agentURL.String(), hdr)
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
	go relay(up, down, errc) // agent -> browser
	go relay(down, up, errc) // browser -> agent
	<-errc                   // first side to close ends the bridge
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
