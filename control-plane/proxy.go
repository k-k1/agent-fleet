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
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// auditActionTarget classifies a proxied request as an auditable CHANGE operation
// (M1 audit, docs/log/20 §A / §E.1: file, git, session mutations). Read-only and other
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
			// Manual snapshot of the agent memory (docs/log/39).
			return "memory.snapshot", "", true
		case p == "/api/agents/memory/import":
			// Receipt into refs/imports; live is not touched yet.
			return "memory.import", "", true
		case p == "/api/agents/memory/import/apply":
			// Applying an imported tree to live; which lineage comes from the URL hint.
			return "memory.import.apply", q.Get("importId"), true
		case p == "/api/agents/memory/restore":
			// Rollback (docs/log/39 stage 4). The Console repeats the source rev in the
			// query as an audit hint, because the body is never read (§A.6). The body's
			// rev/at/scope is what actually governs, and what happened is recorded in the
			// repo's restore commit (AF-Restore-Rev / -Scope).
			return "memory.restore", q.Get("rev"), true
		case p == "/api/sessions":
			return "session.create", "", true
		case name != "" && strings.HasSuffix(p, "/fork"):
			return "session.fork", name, true
		case name != "" && strings.HasSuffix(p, "/stop"):
			return "session.stop", name, true
		}
	case http.MethodGet:
		// Reads are not audited, with one exception: memory export is the only path that
		// carries someone's personal memory out of the environment, and without a record
		// of who exported what format when there is nothing to trace afterwards
		// (docs/log/39 stage 4). The target is the format only — no body, no content.
		if p == "/api/agents/memory/export" {
			return "memory.export", q.Get("format"), true
		}
	case http.MethodDelete:
		switch {
		case p == "/api/fs/delete":
			return "fs.delete", q.Get("path"), true
		case strings.HasPrefix(p, "/api/repo-jobs/"):
			// Cancel / dismiss an import job (docs/log/78). The target is the job id,
			// taken from the URL alone.
			return "repo.job.cancel", strings.TrimPrefix(p, "/api/repo-jobs/"), true
		case name != "" && p == "/api/repos/"+name:
			return "repo.delete", name, true
		}
	}
	return "", "", false
}

// agentProxyAPI is the set of pass-through proxies to the Workspace Agent. Resolution
// comes from the embedded memberAuth (wrapped in withResolved at registration; tenantSel
// absorbs the terminal/WS case where the tenant is chosen by query param). Its only
// dependency is a.mgr, for the connection activity hooks and the audit store.
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

	// M1 audit (docs/log/20): record a successful change operation. actor/tenant come from
	// the resolved request; best-effort with a detached context so a client disconnect
	// during the body copy below can't cancel the write.
	if action, target, ok := auditActionTarget(r); ok &&
		((resp.StatusCode >= 200 && resp.StatusCode < 300) ||
			(resp.StatusCode == http.StatusInternalServerError && fsPutOutcome == errCodeFSWriteStateUnknown)) {
		detail := ""
		if fsPutOutcome == errCodeFSWriteStateUnknown {
			detail = "write_state_unknown"
		}
		_ = a.mgr.store.InsertAudit(context.Background(), store.AuditLog{
			ID: store.NewID(), TenantID: res.ws.TenantID, ActorKind: "user", ActorID: res.ident.ID,
			Action: action, Target: target, Detail: detail, HTTPStatus: resp.StatusCode, At: store.NowTS(),
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
// (confirmed 2026-08-19 on the dev deployment). Refuse up front instead so the client
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

// upgrader negotiates permessage-deflate only with browsers that support it, to cut PTY
// output and screencast bandwidth on mobile links. Other clients are unaffected.
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
	// Presence counts "a human is touching it", not "a socket exists" (docs/log/75 P3).
	// noteInput is called only when the browser→agent relay below sees a keystroke frame.
	releasePresence, noteInput, err := a.mgr.trackWorkspaceTerminal(r.Context(), res.ws.ID, session)
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
	go relay(up, down, errc, nil)       // agent -> browser
	go relay(down, up, errc, noteInput) // browser -> agent; only keystrokes count as presence
	<-errc                              // first side to close ends the bridge
}

// terminalFrame is the browser→agent control envelope (console/src/terminal/term.ts):
// {"type":"input"|"resize"|"ping", …}. Only "input" means a human is at the keyboard.
//
// Never count ping or resize. The Console pings an open socket periodically, so treating
// "a frame arrived" as presence brings back exactly what docs/log/75 P3 removes: a
// forgotten tab keeping the workspace warm forever.
type terminalFrame struct {
	Type string `json:"type"`
}

func isTerminalInput(mt int, data []byte) bool {
	if mt != websocket.TextMessage {
		return false
	}
	var f terminalFrame
	if json.Unmarshal(data, &f) != nil {
		return false
	}
	return f.Type == "input"
}

func relay(src, dst *websocket.Conn, errc chan<- error, onInput func()) {
	for {
		mt, data, err := src.ReadMessage()
		if err != nil {
			// Pass the peer's CLOSE frame through. gorilla answers the close handshake
			// itself and surfaces it here as a CloseError, so the frame never reaches
			// the other side on its own — the bridge just tore the socket down and the
			// browser saw an abnormal 1006. That erased the only signal distinguishing
			// "the session ended" (1000 + reason) from "the connection broke", which is
			// why a stopped session's finite history replay rendered as [disconnected].
			// 1005/1006 are status codes a close frame may never CARRY, so they are the
			// one case we still drop.
			if ce, ok := err.(*websocket.CloseError); ok &&
				ce.Code != websocket.CloseNoStatusReceived && ce.Code != websocket.CloseAbnormalClosure {
				// Best-effort and synchronous: WriteMessage flushes to the socket before
				// the deferred Close() in ptyProxy tears the connection down.
				_ = dst.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(ce.Code, ce.Text))
			}
			errc <- err
			return
		}
		if onInput != nil && isTerminalInput(mt, data) {
			onInput()
		}
		if err := dst.WriteMessage(mt, data); err != nil {
			errc <- err
			return
		}
	}
}
