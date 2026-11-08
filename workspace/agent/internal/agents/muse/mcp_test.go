package muse

import (
	"encoding/json"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp/msptest"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// 🔴 The union the wire declares is CLOSED: an undeclared `transport` does not degrade to
// "that server did not start", it fails `session/start` decode and takes the whole session
// with it. And the two routes spell the same transport differently — decision 11 documents
// the settings file's block as `streamable_http`, and the wire wants `streamableHttp`. A
// member with one HTTP integration would have had every muse session refuse to start.
func TestHTTPServerUsesTheWireSpellingNotTheFiles(t *testing.T) {
	cfg := mcpServerConfig(mcpreg.ServerDef{
		Name: "tickets", Transport: mcpreg.TransportHTTP, URL: "https://mcp.example.com/mcp",
		Headers: map[string]string{"Authorization": "Bearer x"},
	}, "")
	if cfg.Transport != "streamableHttp" {
		t.Errorf("transport = %q, want streamableHttp", cfg.Transport)
	}
	if cfg.URL == nil || *cfg.URL != "https://mcp.example.com/mcp" {
		t.Errorf("url = %v", cfg.URL)
	}
	if cfg.Headers["Authorization"] != "Bearer x" {
		t.Errorf("headers = %v", cfg.Headers)
	}
	// The stdio arm's own spelling, so a change to either is caught here rather than by a
	// member whose sessions stop starting.
	stdio := mcpServerConfig(mcpreg.ServerDef{Name: "wiki", Transport: mcpreg.TransportStdio, Command: "/bin/true"}, "")
	if stdio.Transport != "stdio" {
		t.Errorf("stdio transport = %q", stdio.Transport)
	}
}

// Every server goes out optional. The wire default is `required`, and a required server that
// fails to start aborts the whole run — so a tenant integration whose endpoint is down would
// stop the member's agent from starting at all.
func TestEveryServerIsOptional(t *testing.T) {
	for _, d := range []mcpreg.ServerDef{
		{Name: "wiki", Transport: mcpreg.TransportStdio, Command: "/bin/true"},
		{Name: "tickets", Transport: mcpreg.TransportHTTP, URL: "https://example.com/mcp"},
	} {
		cfg := mcpServerConfig(d, "")
		if cfg.Mode == nil {
			t.Fatalf("%s: no mode sent, so the host applies its own default (required)", d.Name)
		}
		if *cfg.Mode != msp.SessionMCPServerModeOptional {
			t.Errorf("%s: mode = %q", d.Name, *cfg.Mode)
		}
	}
}

// The end of the route decision 11 chose: the servers have to be ON `session/start`, because
// nothing else sends them. This is the assertion that would have failed for the whole of
// Phase 2 until now — the settings writer correctly wrote no `mcp_servers`, and nobody sent
// anything either.
func TestSessionStartCarriesTheRegistrysServers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	if _, err := mcpreg.Create(mcpreg.ServerDef{
		Name: "wiki", Transport: mcpreg.TransportStdio, Command: "/bin/true",
		Args: []string{"--serve"}, Env: map[string]string{"TOKEN": "s3cret"},
		Enabled: true, Targets: mcpreg.Targets{Session: true}, Kinds: []string{session.KindMuse},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	h := &threadHandle{slotSid: "00000000-0000-5000-8000-0000000000c1", sessionMCP: true}
	host := newTestHandle(t, h)
	sent := startCapture(t, host)

	if err := h.openSession(h.cl, agents.ThreadSettings{Model: "muse-spark-1.3"}); err != nil {
		t.Fatalf("openSession: %v", err)
	}
	p := <-sent
	if p.Config == nil || len(p.Config.MCPServers) == 0 {
		t.Fatalf("session/start carried no MCP servers: %+v", p.Config)
	}
	wiki, ok := p.Config.MCPServers["wiki"]
	if !ok {
		t.Fatalf("servers = %+v", p.Config.MCPServers)
	}
	if wiki.Command == nil || *wiki.Command != "/bin/true" || len(wiki.Args) != 1 {
		t.Errorf("wiki = %+v", wiki)
	}
	if wiki.Env["TOKEN"] != "s3cret" {
		t.Errorf("the definition's env did not reach the wire: %+v", wiki.Env)
	}
}

// A host that did not grant `sessionMcp` cannot take them, and sending anyway is how a
// capability negotiation turns into a decode failure the member reads as "my integration is
// broken".
func TestNoServersWithoutTheGrant(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	if _, err := mcpreg.Create(mcpreg.ServerDef{
		Name: "wiki", Transport: mcpreg.TransportStdio, Command: "/bin/true",
		Enabled: true, Targets: mcpreg.Targets{Session: true}, Kinds: []string{session.KindMuse},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	h := &threadHandle{slotSid: "00000000-0000-5000-8000-0000000000c2"} // sessionMCP false
	host := newTestHandle(t, h)
	sent := startCapture(t, host)

	if err := h.openSession(h.cl, agents.ThreadSettings{Model: "muse-spark-1.3"}); err != nil {
		t.Fatalf("openSession: %v", err)
	}
	if p := <-sent; p.Config != nil {
		t.Errorf("servers were sent to a host that did not grant sessionMcp: %+v", p.Config)
	}
}

// A definition scoped to other kinds is not muse's. Without this the "carries the servers"
// test above passes for a build that sends every server to every kind.
func TestServersScopedToAnotherKindAreNotSent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	if _, err := mcpreg.Create(mcpreg.ServerDef{
		Name: "codexonly", Transport: mcpreg.TransportStdio, Command: "/bin/true",
		Enabled: true, Targets: mcpreg.Targets{Session: true}, Kinds: []string{session.KindCodex},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	servers, err := sessionMCPServers("")
	if err != nil {
		t.Fatalf("sessionMCPServers: %v", err)
	}
	if _, ok := servers["codexonly"]; ok {
		t.Errorf("a codex-scoped server reached muse: %+v", servers)
	}
}

// startCapture makes the host answer session/start and hands back what it was sent.
func startCapture(t *testing.T, host *msptest.Host) <-chan msp.SessionStartParams {
	t.Helper()
	ch := make(chan msp.SessionStartParams, 1)
	host.Handle(msp.MethodSessionStart, func(m msptest.Message) (any, *msp.Error) {
		var p msp.SessionStartParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			t.Errorf("session/start params: %v", err)
		}
		ch <- p
		return map[string]any{
			"session": map[string]any{
				"sessionId": "01a0c1d6-0000-7000-8000-0000000000c9", "path": "/tmp/s.jsonl",
				"status": "idle", "createdAt": "", "updatedAt": "", "turnCount": 0,
			},
			"viewCursor": "c1",
		}, nil
	})
	return ch
}

// The builtin `af` server is what carries af_report and the rest of the session tools, and
// muse gets it by the same route as everything else — the wire. This is the claim behind
// mcpreg.ServedKinds: without it a muse session is told to call af_report and has no such
// tool, which fails by simply never reporting.
func TestTheBuiltinAFServerReachesMuse(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	servers, err := sessionMCPServers("")
	if err != nil {
		t.Fatalf("sessionMCPServers: %v", err)
	}
	found := false
	for name := range servers {
		if mcpreg.StaleAFServerName(name, map[string]bool{}) || name == mcpreg.AFServerName() {
			found = true
		}
	}
	if !found {
		t.Fatalf("no af server among %v", keysOf(servers))
	}
}

func keysOf(m map[string]msp.SessionMCPServerConfig) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// 🔴 The 401 nobody would see until a report never arrives. Measured live (ADR 0095 P2-14): a
// muse session's `af` server starts, its tools reach the model, and every call that writes
// back to the Agent fails with `missing or invalid agent token` — because muse scrubs an MCP
// child's environment the way codex does and the wire config carried no `env`.
//
// The wire's `env` takes VALUES, not names, so this is the one place a forwarded variable's
// value is read out of the Agent's own environment.
func TestBuiltinServersCarryTheEnvTheAgentAPINeeds(t *testing.T) {
	t.Setenv("AGENT_TOKEN", "probe-token")
	t.Setenv("AF_MEMO_TOKEN", "probe-memo")
	t.Setenv("AF_SECRET_KEY", "probe-key")

	af := mcpServerConfig(mcpreg.ServerDef{
		Name: "af", Origin: mcpreg.OriginBuiltin, ID: mcpreg.BuiltinAF,
		Transport: mcpreg.TransportStdio, Command: "workspace-agent",
		Args: []string{"mcp-stdio", "--self-report"},
	}, "")
	if af.Env["AGENT_TOKEN"] != "probe-token" {
		t.Errorf("the af server gets no agent token: env = %v", af.Env)
	}
	if af.Env["AF_MEMO_TOKEN"] != "probe-memo" {
		t.Errorf("the memo tools hairpin to the CP and need their own token: env = %v", af.Env)
	}

	// Another builtin needs the store key its mcp-run wrapper opens the secret store with,
	// and NOT the agent token — the list is per definition, not one bag for all of them.
	pd := mcpServerConfig(mcpreg.ServerDef{
		Name: "pagerduty", Origin: mcpreg.OriginBuiltin, ID: mcpreg.BuiltinPagerDuty,
		Transport: mcpreg.TransportStdio, Command: "workspace-agent",
	}, "")
	if pd.Env["AF_SECRET_KEY"] != "probe-key" {
		t.Errorf("a builtin's mcp-run wrapper cannot open the store: env = %v", pd.Env)
	}
	if _, leaked := pd.Env["AGENT_TOKEN"]; leaked {
		t.Errorf("pagerduty was handed the agent token it has no use for: env = %v", pd.Env)
	}

	// The control: a user-registered server declares its own environment and is given nothing
	// else. Forwarding AF's credentials into a member's arbitrary stdio command would be a
	// credential handed to code AF does not own.
	user := mcpServerConfig(mcpreg.ServerDef{
		Name: "wiki", Transport: mcpreg.TransportStdio, Command: "/usr/bin/wiki-mcp",
		Env: map[string]string{"WIKI_TOKEN": "theirs"},
	}, "")
	if user.Env["WIKI_TOKEN"] != "theirs" {
		t.Errorf("the definition's own env was dropped: %v", user.Env)
	}
	if _, leaked := user.Env["AGENT_TOKEN"]; leaked {
		t.Errorf("a user-registered server was handed AF's agent token: %v", user.Env)
	}

	// A definition that names one of these itself keeps its own value: it is the one the
	// member configured, and silently overwriting it would be the bug in the other direction.
	own := mcpServerConfig(mcpreg.ServerDef{
		Name: "af", Origin: mcpreg.OriginBuiltin, ID: mcpreg.BuiltinAF,
		Transport: mcpreg.TransportStdio, Command: "workspace-agent",
		Env: map[string]string{"AGENT_TOKEN": "explicit"},
	}, "")
	if own.Env["AGENT_TOKEN"] != "explicit" {
		t.Errorf("the definition's own value lost to the environment: %v", own.Env)
	}
}

// TestBuiltinAFCarriesItsOwnSessionName pins the one channel a Managed muse session's af server
// has for learning which session it serves: muse scrubs its MCP children's environment, and the
// Agent's own environment belongs to no session, so the name has to ride the per-session wire.
// Without it every session-bound af tool guesses from the working folder.
func TestBuiltinAFCarriesItsOwnSessionName(t *testing.T) {
	// A stray value in the Agent's environment is some OTHER session's; it must not win.
	t.Setenv("AF_SESSION_NAME", "someone-else")

	af := mcpServerConfig(mcpreg.ServerDef{
		Name: "af", Origin: mcpreg.OriginBuiltin, ID: mcpreg.BuiltinAF,
		Transport: mcpreg.TransportStdio, Command: "workspace-agent",
	}, "s-owner")
	if got := af.Env["AF_SESSION_NAME"]; got != "s-owner" {
		t.Errorf("af server told AF_SESSION_NAME=%q, want the session it serves", got)
	}

	// Another builtin and a user-registered server are not the af server: neither reads the
	// variable, and a member's command has no business being told the session name.
	for _, d := range []mcpreg.ServerDef{
		{Name: "pagerduty", Origin: mcpreg.OriginBuiltin, ID: mcpreg.BuiltinPagerDuty,
			Transport: mcpreg.TransportStdio, Command: "workspace-agent"},
		{Name: "af", Transport: mcpreg.TransportStdio, Command: "/usr/bin/their-af"},
	} {
		if v, ok := mcpServerConfig(d, "s-owner").Env["AF_SESSION_NAME"]; ok {
			t.Errorf("%s (origin %q) was given AF_SESSION_NAME=%q", d.Name, d.Origin, v)
		}
	}

	// No name, nothing stamped — and still not the Agent's own value.
	if v, ok := mcpServerConfig(mcpreg.ServerDef{
		Name: "af", Origin: mcpreg.OriginBuiltin, ID: mcpreg.BuiltinAF,
		Transport: mcpreg.TransportStdio, Command: "workspace-agent",
	}, "").Env["AF_SESSION_NAME"]; ok {
		t.Errorf("an unnamed session's af server was given AF_SESSION_NAME=%q", v)
	}
}

// resumeCapture makes the host answer session/resume and hands back what it was sent.
func resumeCapture(t *testing.T, host *msptest.Host) <-chan msp.SessionResumeParams {
	t.Helper()
	ch := make(chan msp.SessionResumeParams, 1)
	host.Handle(msp.MethodSessionResume, func(m msptest.Message) (any, *msp.Error) {
		var p msp.SessionResumeParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			t.Errorf("session/resume params: %v", err)
		}
		ch <- p
		return resumedSession(), nil
	})
	refuseSessionRead(t, host)
	return ch
}

func resumedSession() map[string]any {
	return map[string]any{
		"session": map[string]any{
			"sessionId": "01a0c1d6-0000-7000-8000-0000000000c8", "path": "/tmp/s.jsonl",
			"status": "idle", "createdAt": "", "updatedAt": "", "turnCount": 1,
		},
		"history": map[string]any{"mode": "inline", "items": []any{}}, "pendingRequests": []any{}, "viewCursor": "c1",
	}
}

// refuseSessionRead fails the test on a session/read: the resume serves inline history, so a
// read means the resume path changed — and without a handler the fake host would just let the
// call time out, which reads as a slow test rather than a failing one.
func refuseSessionRead(t *testing.T, host *msptest.Host) {
	t.Helper()
	host.Handle(msp.MethodSessionRead, func(msptest.Message) (any, *msp.Error) {
		t.Error("session/read was called on a resume that served inline history")
		return nil, &msp.Error{Code: msp.ErrCodeInvalidParams, Message: "unexpected"}
	})
}

// A reopened session is the common case, not the edge: every idle stop ends the host, and the
// next prompt resumes the stored session in a new one. The host does not keep a session's MCP
// config across processes (measured: the resumed run's toolset held muse's built-ins only), so
// a resume without the servers leaves the session with no af tool at all — it cannot report,
// message a peer or read its memory, and tells its user the tools "do not exist".
func TestSessionResumeCarriesTheRegistrysServers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	if _, err := mcpreg.Create(mcpreg.ServerDef{
		Name: "wiki", Transport: mcpreg.TransportStdio, Command: "/bin/true",
		Enabled: true, Targets: mcpreg.Targets{Session: true}, Kinds: []string{session.KindMuse},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	h := &threadHandle{slotSid: "00000000-0000-5000-8000-0000000000c3", sessionMCP: true}
	host := newTestHandle(t, h)
	writeSession(h.slotSid, museSession{ID: "01a0c1d6-0000-7000-8000-0000000000c8", Path: "/tmp/s.jsonl"})
	sent := resumeCapture(t, host)

	if err := h.openSession(h.cl, agents.ThreadSettings{Model: "muse-spark-1.3"}); err != nil {
		t.Fatalf("openSession: %v", err)
	}
	p := <-sent
	if p.SessionID != "01a0c1d6-0000-7000-8000-0000000000c8" {
		t.Fatalf("resumed %q, not the stored session", p.SessionID)
	}
	if p.Config == nil {
		t.Fatal("session/resume carried no MCP servers: the reopened session has none of af's tools")
	}
	if _, ok := p.Config.MCPServers["wiki"]; !ok {
		t.Errorf("servers = %+v", p.Config.MCPServers)
	}
	// The builtin af server is the one whose absence was observed, and the one that must be
	// told which session it serves.
	var af *msp.SessionMCPServerConfig
	for name, cfg := range p.Config.MCPServers {
		if name == mcpreg.AFServerName() {
			c := cfg
			af = &c
		}
	}
	if af == nil {
		t.Fatalf("the af server is not on the resume: %+v", p.Config.MCPServers)
	}
	if af.Env[agents.SessionNameEnvVar] != h.name {
		t.Errorf("af server on resume serves %q, want %q", af.Env[agents.SessionNameEnvVar], h.name)
	}
}

// The grant rule holds on the resume path as well: a host that did not grant `sessionMcp` gets
// no config there either.
func TestNoServersOnResumeWithoutTheGrant(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	h := &threadHandle{slotSid: "00000000-0000-5000-8000-0000000000c4"} // sessionMCP false
	host := newTestHandle(t, h)
	writeSession(h.slotSid, museSession{ID: "01a0c1d6-0000-7000-8000-0000000000c8", Path: "/tmp/s.jsonl"})
	sent := resumeCapture(t, host)

	if err := h.openSession(h.cl, agents.ThreadSettings{Model: "muse-spark-1.3"}); err != nil {
		t.Fatalf("openSession: %v", err)
	}
	if p := <-sent; p.Config != nil {
		t.Errorf("servers were sent on resume to a host that did not grant sessionMcp: %+v", p.Config)
	}
}

// A config the host will not decode refuses the whole resume, and before the servers rode the
// resume that same session reopened fine. The resume is retried once without them, so one bad
// member integration degrades the session to "no MCP tools" instead of failing to open it.
func TestResumeRefusedForItsConfigReopensWithoutServers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	if _, err := mcpreg.Create(mcpreg.ServerDef{
		Name: "wiki", Transport: mcpreg.TransportStdio, Command: "/bin/true",
		Enabled: true, Targets: mcpreg.Targets{Session: true}, Kinds: []string{session.KindMuse},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	h := &threadHandle{slotSid: "00000000-0000-5000-8000-0000000000c5", sessionMCP: true}
	host := newTestHandle(t, h)
	writeSession(h.slotSid, museSession{ID: "01a0c1d6-0000-7000-8000-0000000000c8", Path: "/tmp/s.jsonl"})
	var withConfig, without int
	host.Handle(msp.MethodSessionResume, func(m msptest.Message) (any, *msp.Error) {
		var p msp.SessionResumeParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			t.Errorf("session/resume params: %v", err)
		}
		if p.Config != nil {
			withConfig++
			return nil, &msp.Error{Code: msp.ErrCodeInvalidParams, Message: "invalid session/resume config"}
		}
		without++
		return resumedSession(), nil
	})
	refuseSessionRead(t, host)

	if err := h.openSession(h.cl, agents.ThreadSettings{Model: "muse-spark-1.3"}); err != nil {
		t.Fatalf("one refused integration failed the whole open: %v", err)
	}
	if withConfig != 1 || without != 1 {
		t.Errorf("resumes with config = %d, without = %d; want one of each", withConfig, without)
	}
	h.mu.Lock()
	sid := h.sid
	h.mu.Unlock()
	if sid != "01a0c1d6-0000-7000-8000-0000000000c8" {
		t.Errorf("sid = %q: the stored session was not reopened", sid)
	}
}

// A stored session the host still knows is reopened as is: same id, no session/start. Without
// this, a driver that skipped the resume and always started fresh would still pass every live
// test, because a host that keeps no turnless session cannot tell the two apart.
func TestOpenSessionResumeSuccessKeepsTheStoredSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	const stored = "01a0c1d6-0000-7000-8000-0000000000c9"
	h := &threadHandle{slotSid: "00000000-0000-5000-8000-0000000000c7"}
	host := newTestHandle(t, h)
	writeSession(h.slotSid, museSession{ID: stored, Path: "/tmp/s.jsonl"})
	var resumed, started int
	host.Handle(msp.MethodSessionResume, func(m msptest.Message) (any, *msp.Error) {
		var p msp.SessionResumeParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			t.Errorf("session/resume params: %v", err)
		}
		if p.SessionID != stored {
			t.Errorf("resumed %q, want the stored %q", p.SessionID, stored)
		}
		resumed++
		return resumedSession(), nil
	})
	host.Handle(msp.MethodSessionStart, func(msptest.Message) (any, *msp.Error) {
		started++
		return nil, &msp.Error{Code: msp.ErrCodeInternal, Message: "unexpected session/start"}
	})
	refuseSessionRead(t, host)

	if err := h.openSession(h.cl, agents.ThreadSettings{Model: "muse-spark-1.3"}); err != nil {
		t.Fatalf("openSession: %v", err)
	}
	h.mu.Lock()
	sid := h.sid
	h.mu.Unlock()
	if sid != stored || resumed != 1 || started != 0 {
		t.Errorf("sid=%q resumed=%d started=%d; want the stored sid, one resume, no start", sid, resumed, started)
	}
}

// Only a refusal of the config is retried bare. Any other failure keeps its own meaning:
// sessionNotFound still falls back to a fresh start, and anything else still fails the open
// rather than being papered over by a second call.
func TestResumeOtherFailuresAreNotRetriedWithoutServers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SECRET_KEY", "")
	if _, err := mcpreg.Create(mcpreg.ServerDef{
		Name: "wiki", Transport: mcpreg.TransportStdio, Command: "/bin/true",
		Enabled: true, Targets: mcpreg.Targets{Session: true}, Kinds: []string{session.KindMuse},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	h := &threadHandle{slotSid: "00000000-0000-5000-8000-0000000000c6", sessionMCP: true}
	host := newTestHandle(t, h)
	writeSession(h.slotSid, museSession{ID: "01a0c1d6-0000-7000-8000-0000000000c8", Path: "/tmp/s.jsonl"})
	calls := 0
	host.Handle(msp.MethodSessionResume, func(msptest.Message) (any, *msp.Error) {
		calls++
		return nil, &msp.Error{Code: msp.ErrCodeInternal, Message: "boom"}
	})
	if err := h.openSession(h.cl, agents.ThreadSettings{Model: "muse-spark-1.3"}); err == nil {
		t.Fatal("an internal error on resume opened the session")
	}
	if calls != 1 {
		t.Errorf("session/resume called %d times, want 1", calls)
	}
}
