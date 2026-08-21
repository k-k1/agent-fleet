package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
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
		case name != "" && strings.HasSuffix(p, "/parent-ff"):
			return "git.parent_ff", name, true
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

// agentRelayClient relays browser↔Agent traffic WITHOUT following redirects: a 3xx
// from the Agent (e.g. its mux's clean-path redirect) must be returned to the browser,
// not re-followed by the CP with the bearer attached — following would let an encoded
// `..` reach Agent endpoints outside the CP's explicit route allowlist (and skip the
// route-level audit classification).
var agentRelayClient = &http.Client{
	Transport:     newAgentTransport(),
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// unsafeRelayPath reports whether a decoded request path (or path suffix) contains a
// dot-dot / dot / interior-empty segment. The Agent target URL is built by string
// concatenation, and the mux does not normalize %2e%2e%2f, so such a segment would be
// re-interpreted on the Agent side and escape the CP's route allowlist.
func unsafeRelayPath(path string) bool {
	segs := strings.Split(path, "/")
	for i, seg := range segs {
		if seg == ".." || seg == "." {
			return true
		}
		if seg == "" && i > 0 && i < len(segs)-1 {
			return true // interior empty segment ("//")
		}
	}
	return false
}

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
		if err := a.mgr.touchWorkspace(r.Context(), res.ws.ID); err != nil {
			writeAPIErr(w, workspaceActivityAPIError(err))
			return
		}
	}
	if unsafeRelayPath(r.URL.Path) {
		http.Error(w, "bad proxy path", http.StatusBadRequest)
		return
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

	resp, err := agentRelayClient.Do(req)
	if err != nil {
		// TEMP DIAGNOSTIC (2026-08-19): a long-blocking agent-CLI login complete
		// (Claude ~40s, agy ~60s) has been observed failing here with the caller
		// having already gotten a full response server-side (Agent's own access
		// log shows the same request completing normally) — logging the
		// underlying error distinguishes r.Context() cancellation (browser/ALB
		// gave up) from a genuine transport failure (connection reset, i/o
		// timeout) instead of collapsing both into one opaque message.
		log.Printf("agent proxy: %s %s: %v (ctx err=%v)", r.Method, r.URL.Path, err, r.Context().Err())
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

// restLoginFlow wraps rest for the agent-CLI login endpoints (Claude/agy/cursor/
// kiro/opencode/codex/github start-poll-complete) whose state — an OAuth flow_id,
// a device code, a PTY login session — lives only in the Workspace Agent
// process's memory, not in the workspace's shared home volume. If the workspace
// is still converging (rt.State() != "running": e.g. right after a wake, which
// re-registers a task definition and force-deploys on every Start — see
// serviceRolledOut in runtime_ecs.go), the request can be served by a task a
// rolling deployment retires moments later, silently losing that state — the
// user sees "unknown or expired flow_id" or a bare timeout with no clear cause
// (confirmed 2026-08-19 on af.lazmix.jp). Refuse up front instead so the client
// can show "still starting, try again" rather than a confusing failure mid-flow.
func (a agentProxyAPI) restLoginFlow(w http.ResponseWriter, r *http.Request, res *resolved) {
	if s := res.rt.State(r.Context()); s != "running" {
		writeAPIErr(w, &apiError{http.StatusConflict, "workspace_starting",
			"workspace is still starting up — wait a moment and try connecting again"})
		return
	}
	a.rest(w, r, res)
}

// stream is rest for a streaming (SSE) endpoint: it forwards to
// the Agent and copies the response back FLUSHING after each chunk, so token deltas
// reach the browser as they arrive instead of buffering in net/http's ~4KB writer.
func (a agentProxyAPI) stream(w http.ResponseWriter, r *http.Request, res *resolved) {
	rt := res.rt
	if err := a.mgr.touchWorkspace(r.Context(), res.ws.ID); err != nil {
		writeAPIErr(w, workspaceActivityAPIError(err))
		return
	}
	if unsafeRelayPath(r.URL.Path) {
		http.Error(w, "bad proxy path", http.StatusBadRequest)
		return
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
		req.Header.Set("Authorization", "Bearer "+rt.Token())
	}
	resp, err := agentRelayClient.Do(req)
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
	CheckOrigin:       checkWSOrigin,
	EnableCompression: true,
}

// wsAllowedOriginHost is the PUBLIC_BASE_URL host (set at startup in main), an
// extra allowed browser origin besides the request's own Host.
var wsAllowedOriginHost string

// checkWSOrigin allows same-host browser origins, the deployment's public host,
// and clients that send no Origin (non-browser tools). /ws/terminal and
// /ws/browser authenticate by cookie, so an unconditional true would let any
// cross-site page hijack a PTY over the visitor's cookies (CSWSH).
func checkWSOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host) ||
		(wsAllowedOriginHost != "" && strings.EqualFold(u.Host, wsAllowedOriginHost))
}

// terminal bridges the browser terminal WS to the Agent's /ws/pty,
// relaying frames in both directions while preserving message types
// (binary = PTY output, text = input/resize control).
func (a agentProxyAPI) terminal(w http.ResponseWriter, r *http.Request, res *resolved) {
	rt := res.rt
	// P3-9: an attached terminal keeps the workspace warm and pins its session
	// (tier 1 won't halt a session someone is watching).
	session := r.URL.Query().Get("session")
	releasePresence, err := a.mgr.trackWorkspaceConnection(r.Context(), res.ws.ID, session)
	if err != nil {
		writeAPIErr(w, workspaceActivityAPIError(err))
		return
	}
	defer releasePresence()
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
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second, EnableCompression: true, NetDialContext: dialAgent}
	// Keep the Agent's response: it refuses with real statuses the user needs to see —
	// notably 409 "session not running and no terminal history" for a stopped session
	// whose replay has expired (terminal history is a /tmp ring buffer, so a container
	// restart empties it). Discarding it reported "cannot reach workspace agent
	// terminal" (502) for an Agent that answered immediately and correctly, which sent
	// this investigation looking at connectivity instead of at history.
	up, agentResp, err := dialer.Dial(agentURL.String(), hdr)
	if err != nil {
		writeAgentHandshakeError(w, agentResp, "cannot reach workspace agent terminal")
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
