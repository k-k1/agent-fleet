package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

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

	body, mark, aerr := a.mgr.relayBody(w, r, res)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	if u, ok := body.(spooledUpload); ok {
		defer u.Close() // the transport closes it too; this covers a request that never goes out
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, body)
	if err != nil {
		http.Error(w, "bad proxy request", http.StatusBadGateway)
		return
	}
	req.Header = r.Header.Clone()
	stripAgentRelayCredentials(req, r, a.mgr.emailHeader)
	if rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+rt.Token()) // CP↔Agent auth
	}
	// A hint for the Agent's own log (ADR 0102 decision 4): an agent calling the Agent
	// directly can set it too, so the Agent never decides anything on it.
	req.Header.Set("X-AF-Relay", "cp")
	if mark != "" {
		req.Header.Set(memberOriginHeader, mark)
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
		log.Printf("agent proxy: %s %s: %v (ctx err=%v)", r.Method, relayLogPath(r.URL.Path), relayLogErr(r.URL.Path, err), r.Context().Err())
		http.Error(w, "workspace agent unreachable (is the workspace running?)", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	var bufferedResponse []byte
	var fsPutOutcome string
	// Two error envelopes decide the audit: a file write that may have landed, and an approved
	// review proposal that was published although clearing it from the queue failed (the commit
	// exists, so the approval must be on record even if the member never retries).
	reviewApprove := r.Method == http.MethodPost && r.URL.Path == "/api/agents/memory-review/proposals/approve" &&
		resp.StatusCode == http.StatusInternalServerError
	storedGate := auditGated(r)
	if (r.Method == http.MethodPut && r.URL.Path == "/api/fs/file") || reviewApprove || storedGate != nil {
		// These errors are deliberately small JSON envelopes. Read only a bounded
		// prefix before the audit decision so the outcome cannot be lost,
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
	reviewPublishedUncleared := reviewApprove && fsPutOutcome == reviewDecisionIncompleteCode

	// M1 audit (docs/log/20): record a successful change operation. actor/tenant come from
	// the resolved request; best-effort with a detached context so a client disconnect
	// during the body copy below can't cancel the write.
	// A sign-in step audits only the answer that says it stored the credential, not a pending
	// poll; a failed call (non-2xx) is not audited, as for every other row.
	if action, target, ok := auditActionTarget(r); ok &&
		(storedGate == nil || (resp.StatusCode >= 200 && resp.StatusCode < 300 && len(bufferedResponse) <= 64<<10 && storedGate(bufferedResponse))) &&
		((resp.StatusCode >= 200 && resp.StatusCode < 300) ||
			(resp.StatusCode == http.StatusInternalServerError && fsPutOutcome == errCodeFSWriteStateUnknown) ||
			reviewPublishedUncleared) {
		detail := ""
		if reviewPublishedUncleared {
			detail = "published_queue_not_cleared"
		} else if fsPutOutcome == errCodeFSWriteStateUnknown {
			detail = "write_state_unknown"
		} else if strings.HasPrefix(action, "gcp.login.") {
			// ADR 0107 decision 3: the audit says the call came through the relay; the
			// Agent's log has the same hint (X-AF-Relay), which an agent could also set.
			detail = "via relay"
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
	stripAgentRelayCredentials(req, r, a.mgr.emailHeader) // as rest; no stream route is signed
	if rt.Token() != "" {
		req.Header.Set("Authorization", "Bearer "+rt.Token())
	}
	req.Header.Set("X-AF-Relay", "cp") // the same log hint rest sets
	resp, err := agentRelayClient.Do(req)
	if err != nil {
		log.Printf("agent stream proxy: %s %s: %v (ctx err=%v)", r.Method, r.URL.Path, err, r.Context().Err())
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
			// The status is already sent, so a stream the Agent cut short is otherwise a 200 in
			// every log while the browser sees a missing final frame.
			if !errors.Is(rerr, io.EOF) {
				log.Printf("agent stream proxy: %s %s: body read: %v (ctx err=%v)", r.Method, r.URL.Path, rerr, r.Context().Err())
			}
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
	// A hijacked socket does not watch the request context, and removing the member
	// cancels it (memberConnRegistry): close the socket so the relay below ends.
	defer context.AfterFunc(r.Context(), func() { down.Close() })()

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

// agentMemProjectIDRe is the form of an agent-memory project id: a readable prefix, then 12
// hex digits of the repository key's hash.
var agentMemProjectIDRe = regexp.MustCompile(`^[a-z0-9._-]{1,40}-[0-9a-f]{12}$`)

// auditProjectHint keeps a project-id-shaped audit hint and drops anything else.
func auditProjectHint(s string) string {
	if agentMemProjectIDRe.MatchString(s) {
		return s
	}
	return ""
}

// reviewDecisionIncompleteCode is the Agent's answer to an approval that published the memory but
// could not clear the proposal from its queue (workspace/agent/internal/memreview). It is the
// Agent's code, not one the CP emits, so it is not an errcodes.go entry.
const reviewDecisionIncompleteCode = "decision_incomplete"

var auditSessionNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

// auditSessionNameHint keeps a session-name-shaped audit hint and drops anything else.
func auditSessionNameHint(s string) string {
	if auditSessionNameRe.MatchString(s) {
		return s
	}
	return ""
}

// auditRepoNameRe is the folder-name rule the Agent applies to a new working copy
// (workspace/agent/internal/gitx repoNameRe). It admits no control or format characters, no "/"
// and no leading ".", so a name it accepts cannot traverse or spoof the audit view.
var auditRepoNameRe = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N}._@-]{0,95}$`)

// auditRepoNameHint keeps a name the Agent would accept as a working-copy folder and drops
// anything else. It is the client's claim, never checked against the body the Agent acts on.
func auditRepoNameHint(s string) string {
	if auditRepoNameRe.MatchString(s) {
		return s
	}
	return ""
}

var auditReviewIDRe = regexp.MustCompile(`^[0-9a-f]{20}$`)

// auditReviewIDHint keeps a review proposal or run id (20 hex characters) and drops anything else.
func auditReviewIDHint(s string) string {
	if auditReviewIDRe.MatchString(s) {
		return s
	}
	return ""
}

var auditImportIDRe = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z(-[0-9]+)?$`)

// auditImportHint keeps an import-id-shaped audit hint and drops anything else.
func auditImportHint(s string) string {
	if auditImportIDRe.MatchString(s) {
		return s
	}
	return ""
}

// auditAutoSyncHint keeps the member's action on the Agent memory switch's run, and for a wake the
// direction ("wake:on"); anything else is recorded as an empty target.
func auditAutoSyncHint(action, to string) string {
	switch action {
	case "retry", "abandon":
		return action
	case "wake":
		if to == "on" || to == "off" {
			return action + ":" + to
		}
		return action
	}
	return ""
}

// auditCommitHint keeps a commit-id-shaped audit hint and drops anything else.
func auditCommitHint(s string) string {
	if len(s) < 7 || len(s) > 64 {
		return ""
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return ""
		}
	}
	return s
}
