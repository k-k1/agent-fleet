package main

import (
	"bytes"
	"context"
	"encoding/json"
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
	case http.MethodPut:
		if p == "/api/fs/file" {
			target, ok := r.Context().Value(fsPutAuditTargetContextKey{}).(string)
			return "fs.file.put", target, ok && target != ""
		}
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
		case p == "/api/repos/svn":
			return "repo.svn.checkout", "", true
		case name != "" && strings.HasSuffix(p, "/svn-update"):
			return "repo.svn.update", name, true
		case name != "" && strings.HasSuffix(p, "/svn-cleanup"):
			return "repo.svn.cleanup", name, true
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
		case p == "/api/agents/memory/snapshots":
			// エージェントメモリの手動 snapshot（docs/39）。
			return "memory.snapshot", "", true
		case p == "/api/agents/memory/import":
			// 受領（refs/imports への取り込み。live にはまだ触れない）。
			return "memory.import", "", true
		case p == "/api/agents/memory/import/apply":
			// 取り込んだ内容の live への適用。どの系譜かは URL のヒントから採る。
			return "memory.import.apply", q.Get("importId"), true
		case p == "/api/agents/memory/restore":
			// 巻き戻し（docs/39 ④）。戻し元 rev は Console が **監査用のヒントとして**
			// クエリにも載せる（本文は読まない = §A.6）。実処理は本文の rev/at/scope が正で、
			// 何が起きたかは repo の restore commit（AF-Restore-Rev / -Scope）に残る。
			return "memory.restore", q.Get("rev"), true
		case p == "/api/sessions":
			return "session.create", "", true
		case name != "" && strings.HasSuffix(p, "/fork"):
			return "session.fork", name, true
		case name != "" && strings.HasSuffix(p, "/stop"):
			return "session.stop", name, true
		}
	case http.MethodGet:
		// 読み取りは原則として監査しないが、メモリの export だけは例外にする（docs/39 ★4）:
		// 個人のメモリを環境の外へ持ち出す唯一の経路であり、「誰がいつ何形式で出したか」が
		// 残らないと後追いができない。target は形式のみ（本文も内容も読まない）。
		if p == "/api/agents/memory/export" {
			return "memory.export", q.Get("format"), true
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

// agentProxyAPI は Workspace Agent への素通しプロキシ集（docs/23 残③）。解決は
// 埋め込みの memberAuth（登録側で withResolved に包む — terminal/WS の tenant 選択
// が query param なのも tenantSel が吸収し従来の resolvedFor と同一）。依存は
// a.mgr（conns の activity フック・監査 store）のみ。
type agentProxyAPI struct{ memberAuth }

func newAgentProxyAPI(m *manager) agentProxyAPI { return agentProxyAPI{memberAuth{m}} }

// rest forwards /api/sessions* to the Workspace Agent's /sessions*.
// The Control Plane never talks to tmux directly; it delegates to the Agent.
func (a agentProxyAPI) rest(w http.ResponseWriter, r *http.Request, res *resolved) {
	rt := res.rt
	if err := scheduleDeleteGuard(r.Context(), a.mgr.store, res.mv.MembershipID, r); err != nil {
		writeAPIErr(w, &apiError{http.StatusConflict, "schedule_in_use", err.Error()})
		return
	}
	// P3-9: only mutating calls (session input/create, repo clone, connections…)
	// count as activity. Background GET polling (session list, workspace state)
	// must NOT keep a workspace warm, or a left-open tab would defeat idle-stop —
	// real presence is instead signalled by an attached terminal or a busy session.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		a.mgr.conns.touch(res.ws.ID)
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

	var bufferedResponse []byte
	var fsPutOutcome string
	if r.Method == http.MethodPut && r.URL.Path == "/api/fs/file" {
		// PUT errors are deliberately small JSON envelopes. Read only a bounded
		// prefix before the audit decision so write_state_unknown cannot be lost,
		// while still forwarding an unexpectedly larger upstream response.
		bufferedResponse, _ = io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
		if len(bufferedResponse) <= 64<<10 {
			var envelope struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if json.Unmarshal(bufferedResponse, &envelope) == nil {
				fsPutOutcome = envelope.Error.Code
			}
		}
	}

	// M1 audit (docs/20): record a successful change operation. actor/tenant come from
	// the resolved request; best-effort with a detached context so a client disconnect
	// during the body copy below can't cancel the write.
	if action, target, ok := auditActionTarget(r); ok &&
		((resp.StatusCode >= 200 && resp.StatusCode < 300) ||
			(resp.StatusCode == http.StatusInternalServerError && fsPutOutcome == errCodeFSWriteStateUnknown)) {
		detail := ""
		if fsPutOutcome == errCodeFSWriteStateUnknown {
			detail = "write_state_unknown"
		}
		_ = a.mgr.store.InsertAudit(context.Background(), AuditLog{
			ID: newID(), TenantID: res.ws.TenantID, ActorKind: "user", ActorID: res.ident.ID,
			Action: action, Target: target, Detail: detail, HTTPStatus: resp.StatusCode, At: nowTS(),
		})
	}

	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if len(bufferedResponse) > 0 {
		_, _ = io.Copy(w, bytes.NewReader(bufferedResponse))
	}
	_, _ = io.Copy(w, resp.Body)
}

// stream is rest for a streaming (SSE) endpoint: it forwards to
// the Agent and copies the response back FLUSHING after each chunk, so token deltas
// reach the browser as they arrive instead of buffering in net/http's ~4KB writer.
func (a agentProxyAPI) stream(w http.ResponseWriter, r *http.Request, res *resolved) {
	rt := res.rt
	a.mgr.conns.touch(res.ws.ID) // a chat turn is real activity (POST)
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

// EnableCompression: permessage-deflate に対応するブラウザとだけネゴする（モバイル
// 回線での PTY 出力・スクリーンキャストの帯域削減）。非対応クライアントは従来通り。
var upgrader = websocket.Upgrader{
	CheckOrigin:       func(r *http.Request) bool { return true },
	EnableCompression: true,
}

// terminal bridges the browser terminal WS to the Agent's /ws/pty,
// relaying frames in both directions while preserving message types
// (binary = PTY output, text = input/resize control).
func (a agentProxyAPI) terminal(w http.ResponseWriter, r *http.Request, res *resolved) {
	rt := res.rt
	// P3-9: an attached terminal keeps the workspace warm and pins its session
	// (tier 1 won't halt a session someone is watching).
	session := r.URL.Query().Get("session")
	a.mgr.conns.addConn(res.ws.ID, session)
	defer a.mgr.conns.doneConn(res.ws.ID, session)
	// No auto-start: opening a terminal must NOT boot a stopped workspace. Otherwise a
	// mere session click (which opens /ws/pty) would silently revive the whole WS. A
	// stopped workspace is brought up only by the explicit WORKSPACE Start control;
	// here we fail fast so the terminal stays down and the user resumes on purpose.
	switch rt.State(r.Context()) {
	case "running":
	case "starting":
		// Distinct code so the Console can say "wait" instead of "start it first" —
		// a starting workspace must not be re-started.
		writeAPIErr(w, &apiError{http.StatusConflict, "workspace_starting",
			"workspace is starting — wait for it to come up"})
		return
	default:
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
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second, EnableCompression: true}
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
