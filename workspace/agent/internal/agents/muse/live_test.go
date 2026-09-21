package muse

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/msp"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Live verification against a real `muse serve`, run only with MUSE_LIVE=1 (the shape kiro's
// live_test.go and codex's live_drift_test.go already use). Not run in CI: the binary is
// proprietary and not in the image.
//
// It covers exactly what msptest cannot: that the spawn argv, the child environment, the
// handshake and session/start actually work against the vendor's own host. It stops short of
// a turn — `--provider echo` is an exec startup flag with no serve equivalent, so a turn
// costs a member's subscription quota (ADR 0095 P2-1).
func liveGate(t *testing.T) {
	t.Helper()
	if os.Getenv("MUSE_LIVE") != "1" {
		t.Skip("MUSE_LIVE=1 to run against a real muse serve")
	}
	if os.Getenv("AGENT_MUSE_BIN") == "" {
		if _, err := exec.LookPath("muse"); err != nil {
			t.Skip("no muse binary: set AGENT_MUSE_BIN")
		}
	}
}

// liveMeta builds a session in a throwaway HOME. The throwaway is the point, not tidiness:
// without the foreign-context clamp a real turn ships the member's own ~/.claude/CLAUDE.md to
// Meta, so a live test must never run against the member's home.
func liveMeta(t *testing.T) session.Meta {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	dir := filepath.Join(home, "ws")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Resume refuses without a stored credential (a host with none ends every turn
	// `authRequired`, so the session could never answer — driver.go). These tests stop short of
	// a turn and `session/start` needs no valid credential, so a FAKE one satisfies the gate.
	//
	// Deliberately not the member's own: copying their real auth.json into a temp directory to
	// make a test pass would leave their Meta token in /tmp, which is shared across every
	// session in this container.
	writeAuth(t, keyOnlyJSON)
	name := "muse-live-" + filepath.Base(home)
	t.Cleanup(func() { DropHandle(name) })
	return session.Meta{Kind: session.KindMuse, Name: name, Dir: dir, Driver: session.DriverManaged}
}

func TestLiveResumeStartsAHostAndASession(t *testing.T) {
	liveGate(t)
	m := liveMeta(t)

	th, err := NewDriver().Resume(m)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !ManagedAlive(m.Name) {
		t.Error("the session is not alive after Resume")
	}

	h := th.(*threadHandle)
	h.mu.Lock()
	sid, path := h.sid, h.path
	h.mu.Unlock()
	if sid == "" {
		t.Fatal("no muse session id after Resume")
	}
	// Decision 4: the path is RECORDED, not computed — the store is partitioned by date, so
	// deriving it means guessing which day the session was created.
	if path == "" {
		t.Fatal("session/start returned no transcript path")
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Errorf("the recorded path does not exist: %v", err)
	}

	stored, ok := readSession(slotSid(m))
	if !ok || stored.ID != sid || stored.Path != path {
		t.Errorf("the session was not persisted for the next resume: %+v (ok=%v)", stored, ok)
	}
}

// A second Resume must reattach to the same conversation, not mint a second one — and it must
// reuse the live host rather than spawning a second child for one session.
func TestLiveResumeIsIdempotent(t *testing.T) {
	liveGate(t)
	m := liveMeta(t)

	first, err := NewDriver().Resume(m)
	if err != nil {
		t.Fatalf("first resume: %v", err)
	}
	second, err := NewDriver().Resume(m)
	if err != nil {
		t.Fatalf("second resume: %v", err)
	}
	if first != second {
		t.Error("the second Resume built a new handle instead of reusing the live one")
	}
}

// Dropping the handle and resuming again has to RELOAD the stored session, which is the path
// a workspace restart takes. A fresh id here would mean the member's history silently split.
func TestLiveResumeAfterDropReloadsTheSameSession(t *testing.T) {
	liveGate(t)
	m := liveMeta(t)

	th, err := NewDriver().Resume(m)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	h := th.(*threadHandle)
	h.mu.Lock()
	firstSid := h.sid
	h.mu.Unlock()

	DropHandle(m.Name)
	if ManagedAlive(m.Name) {
		t.Fatal("the session is still alive after DropHandle")
	}

	th2, err := NewDriver().Resume(m)
	if err != nil {
		t.Fatalf("resume after drop: %v", err)
	}
	h2 := th2.(*threadHandle)
	h2.mu.Lock()
	secondSid := h2.sid
	h2.mu.Unlock()
	if secondSid != firstSid {
		t.Errorf("the reload started a new conversation: %s then %s", firstSid, secondSid)
	}
}

// A dead child must turn into a dead handle, or the Console shows a live session with nothing
// behind it and the next Send hangs until its own timeout.
func TestLiveChildDeathMarksTheHandleDead(t *testing.T) {
	liveGate(t)
	m := liveMeta(t)

	th, err := NewDriver().Resume(m)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	h := th.(*threadHandle)
	h.mu.Lock()
	cmd := h.cmd
	h.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		t.Fatal("no child process")
	}
	_ = cmd.Process.Kill()

	deadline := time.After(10 * time.Second)
	for ManagedAlive(m.Name) {
		select {
		case <-deadline:
			t.Fatal("the handle is still alive 10s after the child was killed")
		case <-time.After(20 * time.Millisecond):
		}
	}
	if err := h.Send(agents.TurnInput{Prompt: "hi"}); err == nil {
		t.Error("Send succeeded against a dead host")
	}
}

// The transcript path end to end against the vendor's own host, and it costs nothing: the
// member's own prompt comes back as a real `item/completed` with kind `userMessage` BEFORE
// the turn fails on the missing credential, so the wire shape, the store and the rendering
// are all exercised without a model call.
func TestLiveUserMessageItemReachesTheTranscript(t *testing.T) {
	liveGate(t)
	m := liveMeta(t)

	th, err := NewDriver().Resume(m)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	const prompt = "AFPROBE transcript round trip"
	if err := th.Send(agents.TurnInput{Prompt: prompt}); err != nil {
		t.Fatalf("send: %v", err)
	}

	deadline := time.After(30 * time.Second)
	for {
		td, ok := New().Transcript(m)
		if ok {
			for _, turn := range td.Turns {
				if turn.Role == "user" && turn.Text == prompt {
					if turn.AnchorID == "" {
						t.Error("the rendered turn carries no anchor id")
					}
					return
				}
			}
		}
		select {
		case <-deadline:
			t.Fatalf("the user message never reached the transcript")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// The launch model catalog against the real host, and it costs no quota: `model/list` is a
// query — no commandId, no durable record, no model call.
//
// It runs in the member's own home because the catalog IS the authenticated account's
// (`source: providerCatalog`), so a throwaway home has nothing to ask about. The one side
// effect is the clamp write dialProbe performs before spawning, which is byte for byte what
// every session start already writes to the same file.
func TestLiveModelCatalog(t *testing.T) {
	liveGate(t)
	if !readCredential().Present {
		t.Skip("not signed in to muse: model/list has no catalog to return")
	}
	list, safe, err := probeModels()
	if err != nil {
		t.Fatalf("model/list: %v", err)
	}
	if len(list) == 0 {
		// Schema-legal, so not a failure — but it means the picker shows Default alone, which
		// is the symptom this whole package exists to avoid, so it is worth saying out loud.
		t.Skip("the account's catalog is empty")
	}
	for _, m := range list {
		if m.ID == "" || m.Label == "" {
			t.Errorf("unselectable row: %+v", m)
		}
		// The wire values the picker will offer have to be ones a turn accepts. The unit test
		// pins offer==accept inside AF; this is the half only the vendor can answer.
		for _, e := range m.Efforts {
			if reasoningEffort(e) == nil {
				t.Errorf("%s offers effort %q, which the driver refuses", m.ID, e)
			}
		}
	}
	// 🔴 The half that matters for decision 6 clamp 8: the safe default has to be a real row of
	// the live catalog and it must not be one the vendor says it may learn from. An empty pick
	// here means every session AF starts without an explicit model falls back to the host's
	// default, which IS the contributor variant.
	if safe == "" {
		t.Error("no non-data-sharing model in the live catalog: sessions would fall back to the host's contributor default")
	}
	found := false
	for _, m := range list {
		if m.ID == safe {
			found = true
		}
	}
	if safe != "" && !found {
		t.Errorf("the safe default %q is not in the catalog it came from", safe)
	}
	ids := make([]string, 0, len(list))
	for _, m := range list {
		ids = append(ids, m.ID)
	}
	t.Logf("live catalog (%d): %s — AF starts on %q", len(list), strings.Join(ids, " "), safe)
}

// TestLiveCredentialShapeMatchesTheFixtures reads the member's REAL auth.json and checks it
// against what auth_test.go's fixtures claim. The fixtures are the whole basis for `metered`,
// and a vendor that renamed `mechanism` in 1.4 would leave every unit test green while the card
// started telling a subscription member they were billed per use.
//
// It runs in the member's own home on purpose — unlike the turn tests above, which must not —
// because the file under test IS the member's credential. It asserts no secret and prints none;
// the one thing it echoes is the mechanism, which is a vocabulary word.
func TestLiveCredentialShapeMatchesTheFixtures(t *testing.T) {
	liveGate(t)
	c := readCredential()
	if !c.Present {
		t.Skip("not signed in to muse: nothing to compare the fixtures against")
	}
	if c.Mechanism != mechanismAccount {
		t.Logf("mechanism = %q (the fixtures were measured on an account login, %q)", c.Mechanism, mechanismAccount)
	}
	if c.Mechanism == mechanismAccount {
		// The three fields the fixtures carry alongside the mechanism. Their ABSENCE would mean
		// the shape moved, so it is checked; their values are the member's and are not compared.
		if c.ObtainedVia == "" {
			t.Errorf("an account login with no obtained_via: the fixture's shape has changed")
		}
		if c.Email == "" {
			t.Errorf("an account login with no user_email: the card would show no identity")
		}
	}
	st := Status()
	if st["connected"] != true {
		t.Errorf("Status() says not connected while readCredential() found %q", c.Mechanism)
	}
	if st["metered"] != (c.Mechanism != mechanismAccount) {
		t.Errorf("metered = %v for mechanism %q", st["metered"], c.Mechanism)
	}
	t.Logf("live credential: mechanism=%q obtained_via=%q metered=%v", c.Mechanism, c.ObtainedVia, st["metered"])
}

// 🔴 The closed union, against the real host, for free. `session/start.config.mcpServers`
// rejects an undeclared `transport` by failing the whole command — not the one server — so the
// cost of the wrong spelling is every muse session refusing to start for a member who has one
// HTTP integration. A session-only run proves the decode: MCP servers are not spawned until
// the first turn (measured, gate B1-4), so this costs no quota and starts no child.
func TestLiveSessionStartAcceptsMCPServerConfig(t *testing.T) {
	liveGate(t)
	m := liveMeta(t)

	th, err := NewDriver().Resume(m)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	h := th.(*threadHandle)
	h.mu.Lock()
	cl, granted := h.cl, h.sessionMCP
	h.mu.Unlock()
	if !granted {
		t.Fatal("the host did not grant sessionMcp, so no server could ever be passed")
	}

	sid := msp.NewCommandID()
	root := m.CWD()
	stdioMode := msp.SessionMCPServerModeOptional
	httpMode := msp.SessionMCPServerModeOptional
	url := "https://mcp.example.invalid/mcp"
	cmd := "/bin/true"
	params := msp.SessionStartParams{
		CommandID:     msp.NewCommandID(),
		SessionID:     &sid,
		WorkspaceRoot: &root,
		Config: &msp.SessionConfig{MCPServers: map[string]msp.SessionMCPServerConfig{
			"afprobe-stdio": {Transport: msp.SessionMCPServerConfigTransportStdio, Command: &cmd, Mode: &stdioMode},
			"afprobe-http":  {Transport: msp.SessionMCPServerConfigTransportStreamableHTTP, URL: &url, Mode: &httpMode},
		}},
	}
	var res msp.SessionStartResult
	if err := cl.CallInto(msp.MethodSessionStart, params, callTimeout, &res); err != nil {
		t.Fatalf("the host refused AF's MCP server config: %v", err)
	}
	t.Logf("session/start accepted both transports (session %s)", res.Session.SessionID)

	// The control, and it is the whole reason the assertion above means anything: the file
	// route's spelling has to be REFUSED. Without this arm a host that accepted any string
	// would pass the positive test, and the generated constant would look load-bearing while
	// carrying nothing.
	//
	// ⚠️ A failure here is information rather than damage — it means the vendor widened the
	// union to accept both spellings, at which point the constant stops being a safety rail.
	sid2 := msp.NewCommandID()
	bad := params
	bad.CommandID = msp.NewCommandID()
	bad.SessionID = &sid2
	bad.Config = &msp.SessionConfig{MCPServers: map[string]msp.SessionMCPServerConfig{
		"afprobe-http": {Transport: "streamable_http", URL: &url, Mode: &httpMode},
	}}
	if err := cl.CallInto(msp.MethodSessionStart, bad, callTimeout, nil); err == nil {
		t.Error("the host accepted the settings file's transport spelling on the wire: the union is no longer closed")
	} else {
		t.Logf("the file spelling is refused on the wire, as the closed union declares: %v", err)
	}
}
