package muse

import (
	"crypto/sha256"
	"fmt"
	"io"
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
	// 🔴 And the half that is about a secret rather than a spelling: AF puts the Agent token
	// and the CP's memo token in this map, because muse scrubs an MCP child's environment
	// (P2-14). The wire takes VALUES, so the question "does a value handed to the vendor's
	// host end up on disk" has to be answered by measurement rather than by the schema's
	// promise that configuration diagnostics never echo a command or a URL.
	const marker = "AFPROBE-ENV-VALUE-MUST-NOT-LAND-ON-DISK"
	sidEnv := msp.NewCommandID()
	withEnv := params
	withEnv.CommandID = msp.NewCommandID()
	withEnv.SessionID = &sidEnv
	withEnv.Config = &msp.SessionConfig{MCPServers: map[string]msp.SessionMCPServerConfig{
		"afprobe-env": {Transport: msp.SessionMCPServerConfigTransportStdio, Command: &cmd, Mode: &stdioMode,
			Env: map[string]string{"AFPROBE_SECRET": marker}},
	}}
	if err := cl.CallInto(msp.MethodSessionStart, withEnv, callTimeout, nil); err != nil {
		t.Fatalf("session/start with an env-carrying server: %v", err)
	}
	filepath.Walk(filepath.Join(os.Getenv("HOME"), ".local", "share", "muse"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), marker) {
			t.Errorf("a value passed in the wire env was written to %s: AF's tokens must not reach muse's store", p)
		}
		return nil
	})

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

// Open question 6, answered for free: one host per session, but ONE store per user — so a
// host can list conversations it never started. `session/list` is a query (no commandId, no
// model call), and two hosts in one throwaway HOME reproduce production's shape exactly,
// because production's hosts share the member's real HOME the same way.
//
// What it decides is a Console rule, not a driver one: if the vendor's own listing crosses
// AF's session boundary, then no AF surface may ever offer a muse session it did not start.
func TestLiveSessionListCrossesTheSessionBoundary(t *testing.T) {
	liveGate(t)
	a := liveMeta(t)
	b := a
	b.Name = a.Name + "-second"
	t.Cleanup(func() { DropHandle(b.Name) })

	ha, err := NewDriver().Resume(a)
	if err != nil {
		t.Fatalf("resume a: %v", err)
	}
	hb, err := NewDriver().Resume(b)
	if err != nil {
		t.Fatalf("resume b: %v", err)
	}
	if ha == hb {
		t.Fatal("the two session names share one host, so this proves nothing about two hosts")
	}
	cl, aSid := clientAndSid(ha)
	_, bSid := clientAndSid(hb)
	if aSid == bSid {
		t.Fatalf("both sessions are %s", aSid)
	}

	// BOTH directions, because the order matters and one of them would hide the answer: host A
	// was already running when B's session was created, so asking only A would confuse "the
	// store is not shared" with "the index was read at boot". B is the host that started last
	// and therefore the one that can see everything durable before it.
	bClient, _ := clientAndSid(hb)
	list := func(who string, cl *msp.Client) map[string]msp.Session {
		t.Helper()
		var res msp.SessionListResult
		if err := cl.CallInto(msp.MethodSessionList, msp.SessionListParams{}, callTimeout, &res); err != nil {
			t.Fatalf("session/list from %s: %v", who, err)
		}
		seen := map[string]msp.Session{}
		for _, s := range res.Sessions {
			seen[s.SessionID] = s
		}
		return seen
	}
	fromA, fromB := list("a", cl), list("b", bClient)

	// The positive control: a host that listed nothing at all would make the interesting arms
	// below silent for the wrong reason.
	if _, ok := fromA[aSid]; !ok {
		t.Fatalf("host a does not list its OWN session %s, so its silence about the other one means nothing", aSid)
	}
	if _, ok := fromB[bSid]; !ok {
		t.Fatalf("host b does not list its OWN session %s", bSid)
	}
	for _, arm := range []struct {
		who   string
		seen  map[string]msp.Session
		other string
	}{{"a", fromA, bSid}, {"b", fromB, aSid}} {
		if row, ok := arm.seen[arm.other]; ok {
			// Not a defect of AF's: the store is the member's, so the vendor's listing is
			// user-wide by design. What it obliges is the guard in muse_test.go — the schema's
			// own note that a session loaded by ANOTHER host reads `notLoaded` is the tell that
			// this row came from the shared store.
			t.Logf("session/list from host %s crosses the AF session boundary: %s (status %q, name %v) belongs to the other session",
				arm.who, arm.other, row.Status, row.Name)
			continue
		}
		t.Logf("session/list from host %s returned %d row(s) and none of them is the other session's %s",
			arm.who, len(arm.seen), arm.other)
	}
}

// Open question 7, answered for free: with a `Meta.Subdir` the workspaceRoot AF sends is the
// SUBDIRECTORY, not the working copy — `h.dir` is `m.CWD()` (driver.go) and the same value is
// the child's cwd and what `--trust-workspace` trusts. The half only the host can answer is
// what it RECORDS, and `session/list`'s `workspaceRoot` filter answers it with a query.
//
// The filter is also the control: a listing keyed to the working copy must NOT return the
// session, or "the subdirectory is the root" would be a claim about a filter that ignores its
// argument.
func TestLiveWorkspaceRootIsTheSubdirectoryNotTheWorkingCopy(t *testing.T) {
	liveGate(t)
	m := liveMeta(t)
	sub := filepath.Join(m.Dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	m.Subdir = "sub"

	th, err := NewDriver().Resume(m)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	h := th.(*threadHandle)
	h.mu.Lock()
	dir := h.dir
	h.mu.Unlock()
	if dir != sub {
		t.Fatalf("the handle runs in %s, want the subdirectory %s", dir, sub)
	}
	cl, sid := clientAndSid(th)

	listed := func(root string) bool {
		t.Helper()
		var res msp.SessionListResult
		if err := cl.CallInto(msp.MethodSessionList, msp.SessionListParams{WorkspaceRoot: &root}, callTimeout, &res); err != nil {
			t.Fatalf("session/list(%s): %v", root, err)
		}
		for _, s := range res.Sessions {
			if s.SessionID == sid {
				return true
			}
		}
		return false
	}
	if !listed(sub) {
		t.Errorf("the host did not record %s as the session's workspace root", sub)
	}
	if listed(m.Dir) {
		t.Errorf("the host records the working copy %s as the root too, so the filter proves nothing", m.Dir)
	}
}

// clientAndSid reads the two fields the live tests need off a handle under its lock.
func clientAndSid(th agents.ThreadHandle) (*msp.Client, string) {
	h := th.(*threadHandle)
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cl, h.sid
}

// isolateHomeKeepMuseAuth points HOME at a throwaway dir (isolating AF state and the
// member's ~/.claude/CLAUDE.md) while symlinking the muse config directory so the CLI
// still logs in. We never copy the credential — the symlink lets muse read its OWN file.
// XDG_DATA_HOME is also thrown away so no existing muse sessions pollute the store.
func isolateHomeKeepMuseAuth(t *testing.T) {
	t.Helper()
	real, _ := os.UserHomeDir()
	home := t.TempDir()
	if real != "" {
		src := filepath.Join(real, ".config", "muse")
		if _, err := os.Stat(src); err == nil {
			dst := filepath.Join(home, ".config", "muse")
			_ = os.MkdirAll(filepath.Dir(dst), 0o755)
			_ = os.Symlink(src, dst)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
}

// fileSHA256 returns the hex SHA-256 digest of the named file.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// waitMuseTurn polls h.Snapshot until the turn finishes (completed or failed) or timeout.
// It waits for TurnRunning first (the handle starts in TurnCompleted/idle after Resume),
// so it will not exit early on the initial idle state.
func waitMuseTurn(t *testing.T, h *threadHandle, timeout time.Duration) agents.TurnState {
	t.Helper()
	deadline := time.Now().Add(timeout)
	seenRunning := false
	for time.Now().Before(deadline) {
		snap, _ := h.Snapshot()
		switch snap.TurnState {
		case agents.TurnRunning, agents.TurnWaitingInteraction, agents.TurnInterrupting:
			seenRunning = true
		case agents.TurnCompleted, agents.TurnFailed:
			if seenRunning {
				return snap.TurnState
			}
			// Initial idle state — keep polling until the turn actually starts.
		}
		time.Sleep(200 * time.Millisecond)
	}
	snap, _ := h.Snapshot()
	t.Logf("turn timed out after %v (last state: %s, seenRunning: %v)", timeout, snap.TurnState, seenRunning)
	return snap.TurnState
}

// TestLiveContextUsage spends exactly ONE subscription turn to verify that MSP
// emits session/contextUsage and that ManagedContext returns real data after the turn.
// This is the P2-16 end-to-end live check: caps.contextBar must not be flipped until
// this test passes.
//
// Security invariants:
//   - The member's ~/.config/muse/auth.json is accessed via symlink only — never copied.
//   - HOME is thrown away so ~/.claude/CLAUDE.md is not sent to Meta as context.
//   - SHA-256 of auth.json is compared before and after to confirm no modification.
func TestLiveContextUsage(t *testing.T) {
	liveGate(t)
	if !readCredential().Present {
		t.Skip("not signed in to muse: a real turn requires valid credentials")
	}

	// Capture the real auth.json path and hash BEFORE HOME is swapped out.
	realHome, _ := os.UserHomeDir()
	realAuthJSON := filepath.Join(realHome, ".config", "muse", "auth.json")
	hashBefore, err := fileSHA256(realAuthJSON)
	if err != nil {
		t.Fatalf("sha256 before: %v", err)
	}

	isolateHomeKeepMuseAuth(t) // HOME now points at a throwaway with a symlink to muse config

	dir := filepath.Join(os.Getenv("HOME"), "ws")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "muse-live-ctx-" + filepath.Base(os.Getenv("HOME"))
	t.Cleanup(func() { DropHandle(name) })
	m := session.Meta{Kind: session.KindMuse, Name: name, Dir: dir, Driver: session.DriverManaged}

	th, err := NewDriver().Resume(m)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if err := th.Send(agents.TurnInput{Prompt: "1"}); err != nil {
		t.Fatalf("send: %v", err)
	}

	finalState := waitMuseTurn(t, th.(*threadHandle), 90*time.Second)
	t.Logf("turn finished with state: %s", finalState)

	// The oracle: ManagedContext must return ok=true with real data.
	used, win, ok := ManagedContext(name)
	if !ok {
		t.Error("ManagedContext ok=false after a real turn: session/contextUsage did not arrive on the wire")
	} else {
		winDesc, winSource := "absent (nil)", "estimated → MuseDefaultWindow fallback"
		if win != nil {
			winDesc = fmt.Sprintf("%d", *win)
			winSource = "recorded (wire carried windowTokens)"
		}
		t.Logf("session/contextUsage confirmed: usedTokens=%d windowTokens=%s windowSource=%s",
			used, winDesc, winSource)
	}

	// Safety check: auth.json must be identical before and after.
	hashAfter, err := fileSHA256(realAuthJSON)
	if err != nil {
		t.Fatalf("sha256 after: %v", err)
	}
	if hashBefore != hashAfter {
		t.Errorf("auth.json was modified during the test (before=%s after=%s)", hashBefore, hashAfter)
	}
}

// The fork, against the real host, for free: a whole-conversation fork of a session with no
// completed turns needs no cut point and no model call. What it proves is the part msptest
// cannot — that the vendor accepts AF's `session/fork` and returns a NEW session whose
// `forkedFrom` names the source, which is what the driver reads back and stores.
func TestLiveSessionForkReturnsANewSession(t *testing.T) {
	liveGate(t)
	m := liveMeta(t)

	th, err := NewDriver().Resume(m)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	h := th.(*threadHandle)
	h.mu.Lock()
	cl, srcID := h.cl, h.sid
	h.mu.Unlock()

	var res msp.SessionForkResult
	err = cl.CallInto(msp.MethodSessionFork, msp.SessionForkParams{
		CommandID: msp.NewCommandID(),
		SessionID: srcID,
	}, callTimeout, &res)
	if err != nil {
		t.Fatalf("session/fork: %v", err)
	}
	if res.Session.SessionID == "" || res.Session.SessionID == srcID {
		t.Fatalf("fork returned %q for source %q", res.Session.SessionID, srcID)
	}
	// The provenance the driver relies on being there: AF does not mint the id, so the result
	// is the only place the new session's identity exists.
	if res.Session.ForkedFrom == nil || res.Session.ForkedFrom.SessionID != srcID {
		t.Errorf("forkedFrom = %+v, want the source %q", res.Session.ForkedFrom, srcID)
	}
	if res.Session.ForkedFrom != nil && res.Session.ForkedFrom.CutExplicit {
		t.Error("a fork with no cutPoint reported an explicit cut")
	}
	t.Logf("forked %s -> %s (path %s)", srcID, res.Session.SessionID, res.Session.Path)
}

// TestLiveImagePasteReachesTheModel spends exactly ONE subscription turn on the last thing
// standing between the image-paste work (P2-15) and its row in the capability table: whether a
// pasted image actually REACHES the model. The driver half — reading the file and sending an
// MSP `image` part — is pinned by unit tests; what only the vendor can answer is whether the
// host accepts those bytes and shows them to the model.
//
// 🔴 Two oracles, and the wire one comes first. The host echoes attachment METADATA back on the
// userMessage item (`Item.Attachments`, base64 payloads deliberately not echoed), and AF stores
// the whole item — so "the host took an image" is answerable without reading a word the model
// wrote. The model's reply is the second arm, and it is admissible here in a way it is not for
// a clamp: the token exists ONLY inside the PNG's pixels, never in the prompt, so reproducing it
// is positive evidence that the bytes were rendered for the model rather than absence-evidence.
//
// Same security invariants as TestLiveContextUsage: throwaway HOME (so no ~/.claude rules go to
// Meta), muse config reached by symlink and never copied, auth.json hashed before and after.
func TestLiveImagePasteReachesTheModel(t *testing.T) {
	liveGate(t)
	if !readCredential().Present {
		t.Skip("not signed in to muse: a real turn requires valid credentials")
	}

	realHome, _ := os.UserHomeDir()
	realAuthJSON := filepath.Join(realHome, ".config", "muse", "auth.json")
	hashBefore, err := fileSHA256(realAuthJSON)
	if err != nil {
		t.Fatalf("sha256 before: %v", err)
	}

	// 🔴 The image is rendered BEFORE HOME is thrown away. Pillow lives in the member's user
	// site-packages (~/.local/lib/python3*/site-packages), so a throwaway HOME hides it and the
	// whole test skips itself with "no module named PIL" — a live check that silently stops
	// running is worse than one that fails.
	// ⚠️ Not named `token`: gitleaks' generic-api-key rule fires on `token = "<high entropy>"`
	// and turned the secret-scan job red for a word that is drawn into a PNG.
	const probeWord = "AFPROBE-IMG-7K2"
	png := renderProbeWordPNG(t, filepath.Join(t.TempDir(), "probe.png"), probeWord)

	isolateHomeKeepMuseAuth(t)

	dir := filepath.Join(os.Getenv("HOME"), "ws")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	name := "muse-live-img-" + filepath.Base(os.Getenv("HOME"))
	t.Cleanup(func() { DropHandle(name) })
	m := session.Meta{Kind: session.KindMuse, Name: name, Dir: dir, Driver: session.DriverManaged}

	th, err := NewDriver().Resume(m)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	// ⚠️ The prompt must never contain the token: it is what makes the model's answer evidence
	// rather than an echo.
	err = th.Send(agents.TurnInput{
		Prompt:      "Read the attached image and reply with only the characters written in it. No other words.",
		Attachments: []string{png},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	finalState := waitMuseTurn(t, th.(*threadHandle), 3*time.Minute)
	t.Logf("turn finished with state: %s", finalState)

	// Arm 1 — the wire: the host echoed an image attachment back on the user's own item.
	items, err := openStore(slotSid(m)).Items()
	if err != nil {
		t.Fatalf("read the item store: %v", err)
	}
	attached := false
	for _, it := range items {
		if it.Kind != msp.ItemKindUserMessage {
			continue
		}
		for _, a := range it.Attachments {
			attached = true
			t.Logf("host echoed an attachment: type=%q mediaType=%q width=%s height=%s",
				a.Type, a.MediaType, intPtr(a.Width), intPtr(a.Height))
			if a.MediaType != "image/png" {
				t.Errorf("mediaType = %q, want image/png (what the driver sent)", a.MediaType)
			}
		}
	}
	if !attached {
		t.Error("no attachment on any userMessage item: the host recorded no image for this turn")
	}

	// Arm 2 — the model: the token lives only in the PNG's pixels.
	td, ok := New().Transcript(m)
	if !ok {
		t.Fatal("no transcript for the session that just ran a turn")
	}
	read := false
	for _, turn := range td.Turns {
		if turn.Role == "assistant" && strings.Contains(turn.Text, probeWord) {
			read = true
		}
	}
	if !read {
		var got []string
		for _, turn := range td.Turns {
			if turn.Role == "assistant" {
				got = append(got, turn.Text)
			}
		}
		t.Errorf("the model did not report %q, so the pixels did not reach it; assistant said: %q", probeWord, got)
	}

	hashAfter, err := fileSHA256(realAuthJSON)
	if err != nil {
		t.Fatalf("sha256 after: %v", err)
	}
	if hashBefore != hashAfter {
		t.Errorf("auth.json was modified during the test (before=%s after=%s)", hashBefore, hashAfter)
	}
}

// renderProbeWordPNG draws word into a PNG sized to the text. It shells out to python3/Pillow
// because what this test needs is an image a MODEL can read, and a hand-built PNG of coloured
// squares would test the transport while leaving "did it actually see it" to a guess.
func renderProbeWordPNG(t *testing.T, path, word string) string {
	t.Helper()
	const script = `
import sys
from PIL import Image, ImageDraw, ImageFont
path, word = sys.argv[1], sys.argv[2]
f = ImageFont.truetype("/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf", 72)
box = ImageDraw.Draw(Image.new("RGB", (10, 10))).textbbox((0, 0), word, font=f)
img = Image.new("RGB", (box[2]-box[0]+80, box[3]-box[1]+80), "white")
ImageDraw.Draw(img).text((40-box[0], 40-box[1]), word, fill="black", font=f)
img.save(path)
`
	cmd := exec.Command("python3", "-c", script, path, word)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot render the probe image (python3/Pillow/DejaVu needed): %v\n%s", err, out)
	}
	st, err := os.Stat(path)
	if err != nil || st.Size() == 0 {
		t.Skipf("the probe image was not written: %v", err)
	}
	return path
}

// intPtr renders an optional wire integer for a log line. The schema marks width/height "when
// known", so "absent" has to be distinguishable from 0 — and printing the pointer itself (what
// %v does) puts an address in the measurement record.
func intPtr(p *int64) string {
	if p == nil {
		return "absent"
	}
	return fmt.Sprintf("%d", *p)
}

// TestLiveNativeSkillFires spends ONE subscription turn on the half of the skill picker that no
// free probe can answer (ADR 0095 P2-23).
//
// `skill/list` costs nothing — `session/start` accepts a fake credential, so enumeration was
// measured without touching the account. What needs a real turn is the other direction: whether
// the HOST expands a `skill` input part, which is the only route AF has (a leading slash is
// plain text over MSP; the expansion the TUI does is the TUI's).
//
// 🔥 The oracle is a word that exists ONLY in the skill's body. Asking the model whether it ran
// the skill would be asking it to describe its own prompt; a marker it can only have read is the
// difference between "expanded" and "was handed the literal string /af-probe".
func TestLiveNativeSkillFires(t *testing.T) {
	liveGate(t)
	if !readCredential().Present {
		t.Skip("not signed in to muse: a real turn requires valid credentials")
	}

	realHome, _ := os.UserHomeDir()
	realAuthJSON := filepath.Join(realHome, ".config", "muse", "auth.json")
	hashBefore, err := fileSHA256(realAuthJSON)
	if err != nil {
		t.Fatalf("sha256 before: %v", err)
	}

	isolateHomeKeepMuseAuth(t)

	dir := filepath.Join(os.Getenv("HOME"), "ws")
	skillDir := filepath.Join(dir, ".agents", "skills", "af-probe")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const marker = "PLUM-ORBIT"
	body := "---\nname: af-probe\ndescription: Agent Fleet probe skill\n---\n\n" +
		"Reply with exactly the word " + marker + " and nothing else. Use no tools.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	name := "muse-live-skill-" + filepath.Base(os.Getenv("HOME"))
	t.Cleanup(func() { DropHandle(name) })
	m := session.Meta{Kind: session.KindMuse, Name: name, Dir: dir, Driver: session.DriverManaged}

	th, err := NewDriver().Resume(m)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}

	// Free half: the project skill has to be in the catalogue before there is any point
	// invoking it, and its selector is what the picker will send.
	var selectors []string
	found := false
	for _, s := range Skills(name) {
		selectors = append(selectors, s.Selector+"("+s.Source+")")
		if s.Selector == "af-probe" {
			found = true
			if s.Source != "project" {
				t.Errorf("af-probe source = %q, want project", s.Source)
			}
		}
	}
	t.Logf("skill/list: %v", selectors)
	if !found {
		t.Fatal("the project skill is not in skill/list: there is nothing to invoke")
	}

	// The turn. What the composer sends is exactly what the picker inserts.
	if err := th.Send(agents.TurnInput{Prompt: "/af-probe"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if st := waitMuseTurn(t, th.(*threadHandle), 120*time.Second); st != agents.TurnCompleted {
		t.Fatalf("turn finished in state %s", st)
	}

	td, ok := agentImpl{}.Transcript(m)
	if !ok {
		t.Fatal("no transcript after the turn")
	}
	var reply string
	for _, turn := range td.Turns {
		if turn.Role == "assistant" {
			reply += turn.Text
		}
	}
	t.Logf("assistant said: %q", strings.TrimSpace(reply))
	if !strings.Contains(strings.ToUpper(reply), marker) {
		t.Errorf("the skill did not fire: the reply carries no %s, so the host was handed text rather than a skill part", marker)
	}

	hashAfter, err := fileSHA256(realAuthJSON)
	if err != nil {
		t.Fatalf("sha256 after: %v", err)
	}
	if hashBefore != hashAfter {
		t.Errorf("auth.json was modified during the test (before=%s after=%s)", hashBefore, hashAfter)
	}
}
