package lcpp

// mcp_test.go covers the ADR 0093 段2 MCP wiring (mcp.go / driver.go's runTurn):
//   - unchanged behavior with no server enabled (the baseline the acceptance conditions demand);
//   - a server enabled for lcpp reaches the Registry as mcp__<server>__<tool> and an actual
//     tools/call is routed to it;
//   - that tool is gated behind approval like every other Mutates tool (decision 1), and runs
//     unattended only with AutoApprove/SkipPermissions;
//   - a broken server's Sync error never fails the turn, and is recorded (deduplicated across
//     turns rather than repeated forever);
//   - DropHandle actually kills the stdio child — no leftover process.
//
// The fake server below is the same self-re-exec trampoline internal/mcpc's own
// helper_process_test.go uses (Go's own os/exec tests use the identical trick): this test
// binary re-execs itself with -test.run=TestHelperProcess, gated on an env var so an ordinary
// `go test` run treats it as a no-op.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/harness"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpc"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const (
	mcpHelperEnvFlag    = "LCPP_MCP_TEST_HELPER"
	mcpHelperEnvPidFile = "LCPP_MCP_TEST_HELPER_PIDFILE"
)

// TestMain guards EVERY test in this package, not just this file's own, against a hazard
// mcpTestHome's own doc comment explains in full: mcpreg.ForSession(session.KindLcpp) always
// includes the "af" builtin (BuiltinAF.ready is unconditionally true), and driver.go's runTurn
// dials whatever it returns — so ANY test in this package that runs a turn is affected, whether
// or not its author knew MCP was involved at all.
//
// Two defenses, not one:
//   - mcpServersForSession (mcp.go) is stubbed to return no servers at all, package-wide, by
//     default. This is what makes existing driver_test.go tests see EXACTLY the same records
//     they did before this PR (a real af that a test can't reach would still leave a NoteMCPError
//     record behind, breaking their exact-record-count assertions, even once "no real spawn"
//     is handled — this was found live: the first fix here only closed the recursive-spawn
//     hazard below and PASSED locally, then broke driver_test.go's own record-count checks
//     because af now legitimately, deterministically fails to connect in any test run).
//   - AF_AGENT_INSTALLED_BIN=/bin/false is still set, belt and suspenders, for the few tests in
//     THIS file that restore the real mcpServersForSession (mcpTestHome) to exercise it.
//
// Measured live: PR #869's first CI run had no /usr/local/bin/workspace-agent installed, so
// paths.ConfigExePath() fell back to the volatile path — the TEST BINARY ITSELF — and every one
// of driver_test.go's pre-existing tests (none of which know anything about MCP) exec'd this
// same test binary as "af" with no -test.run filter: a full, unfiltered `go test` re-entry FROM
// INSIDE a test, recursively. All ten blocked (no mcpSyncBudget existed yet at the time) and
// failed. Setting both escape hatches once here, for the whole binary, before any test runs —
// rather than per test — is the only way this stays closed for every test in this package,
// present and future, not just the ones a change happens to touch.
func TestMain(m *testing.M) {
	os.Setenv("AF_AGENT_INSTALLED_BIN", "/bin/false")
	mcpServersForSession = func(string) ([]mcpreg.ServerDef, error) { return nil, nil }
	os.Exit(m.Run())
}

// TestHelperProcess is the trampoline: a normal `go test` run returns immediately (the env
// flag is unset), so this adds no visible test of its own.
func TestHelperProcess(t *testing.T) {
	if os.Getenv(mcpHelperEnvFlag) != "1" {
		return
	}
	runFakeMCPServer()
}

// runFakeMCPServer is a minimal stateless-era MCP stdio server (mcpc's own two-era detection,
// mcpc.go): two tools, "echo" (answers args.msg back) and "touch" (creates the file named by
// args.path) — "touch" exists only so the approval-denied test below can prove the real server
// was never reached, the same convention driver_test.go's own
// TestDriverApprovalGateDeniedNeverRuns already uses for the bash builtin.
func runFakeMCPServer() {
	if pf := os.Getenv(mcpHelperEnvPidFile); pf != "" {
		_ = os.WriteFile(pf, []byte(fmt.Sprintf("%d", os.Getpid())), 0o600)
	}

	r := newLineReader(os.Stdin)
	write := func(v any) {
		b, _ := json.Marshal(v)
		os.Stdout.Write(append(b, '\n'))
	}
	tools := []map[string]any{
		{"name": "echo", "description": "echoes msg back", "inputSchema": map[string]any{
			"type": "object", "properties": map[string]any{"msg": map[string]any{"type": "string"}},
		}},
		{"name": "touch", "description": "creates an empty file at path", "inputSchema": map[string]any{
			"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}},
		}},
		{"name": "whoami", "description": "returns this child's own AF_SESSION_NAME env var", "inputSchema": map[string]any{
			"type": "object", "properties": map[string]any{},
		}},
	}
	for {
		line, ok := r()
		if !ok {
			return
		}
		var req map[string]any
		if json.Unmarshal(line, &req) != nil {
			continue
		}
		method, _ := req["method"].(string)
		id := req["id"]
		switch method {
		case "server/discover":
			write(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
				"supportedVersions": []string{mcpc.ProtocolVersion},
				"capabilities":      map[string]any{"tools": map[string]any{}},
				"serverInfo":        map[string]any{"name": "lcpp-fake", "version": "1"},
			}})
		case "tools/list":
			write(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"tools": tools}})
		case "tools/call":
			params, _ := req["params"].(map[string]any)
			name, _ := params["name"].(string)
			args, _ := params["arguments"].(map[string]any)
			switch name {
			case "echo":
				write(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": fmt.Sprint(args["msg"])}},
					"isError": false,
				}})
			case "touch":
				path, _ := args["path"].(string)
				_ = os.WriteFile(path, []byte("touched"), 0o600)
				write(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": "touched"}},
					"isError": false,
				}})
			case "whoami":
				write(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": os.Getenv(sessionNameEnvVar)}},
					"isError": false,
				}})
			default:
				write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32602, "message": "unknown tool"}})
			}
		}
	}
}

// newLineReader returns a closure that reads one newline-terminated line at a time from r,
// ok=false once the stream is exhausted — small enough not to need bufio's own type in the
// signature above.
func newLineReader(f *os.File) func() ([]byte, bool) {
	buf := make([]byte, 0, 4096)
	one := make([]byte, 1)
	return func() ([]byte, bool) {
		buf = buf[:0]
		for {
			n, err := f.Read(one)
			if n > 0 {
				if one[0] == '\n' {
					return buf, true
				}
				buf = append(buf, one[0])
			}
			if err != nil {
				return nil, false
			}
		}
	}
}

// fakeMCPServerDef registers name as an lcpp-scoped, stdio, enabled server that runs this same
// test binary as the fake server (mcpc's own fakeStdioDef, in a different package). Callers must
// call testHome(t) first (mcpreg.Create needs HOME resolved).
func fakeMCPServerDef(t *testing.T, name, pidFile string) mcpreg.ServerDef {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	env := map[string]string{mcpHelperEnvFlag: "1"}
	if pidFile != "" {
		env[mcpHelperEnvPidFile] = pidFile
	}
	return mcpreg.ServerDef{
		Name: name, Transport: mcpreg.TransportStdio, Command: bin,
		Args: []string{"-test.run=TestHelperProcess"}, Env: env,
		Enabled: true, Targets: mcpreg.Targets{Session: true}, Kinds: []string{session.KindLcpp},
	}
}

// fakeBuiltinAFDef is fakeMCPServerDef's twin for the ONE def identified as the builtin af
// server (Origin+ID, the pair injectSessionName and attach.go's own extraEnvVars key on — see
// injectSessionName's own doc comment for why not Name). It is never handed to mcpreg.Create:
// Create always stamps Origin=OriginUser and mints a fresh ID (store.go), so a builtin row
// cannot be produced through the registry's own write path at all — these tests hand it
// straight to the mcpServersForSession stub instead, the same seam TestMain itself uses.
func fakeBuiltinAFDef(t *testing.T, pidFile string) mcpreg.ServerDef {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	env := map[string]string{mcpHelperEnvFlag: "1"}
	if pidFile != "" {
		env[mcpHelperEnvPidFile] = pidFile
	}
	return mcpreg.ServerDef{
		ID: mcpreg.BuiltinAF, Origin: mcpreg.OriginBuiltin, Name: mcpreg.AFServerName(),
		Transport: mcpreg.TransportStdio, Command: bin,
		Args: []string{"-test.run=TestHelperProcess"}, Env: env,
		Enabled: true, Targets: mcpreg.Targets{Session: true}, Kinds: []string{session.KindLcpp},
	}
}

// registerFakeMCPServer is fakeMCPServerDef plus the mcpreg.Create call every test below needs.
func registerFakeMCPServer(t *testing.T, name, pidFile string) {
	t.Helper()
	if _, err := mcpreg.Create(fakeMCPServerDef(t, name, pidFile)); err != nil {
		t.Fatalf("mcpreg.Create: %v", err)
	}
}

// mcpTestHome is testHome plus AF_SECRET_KEY isolation (mcpreg.Create needs both). It does NOT
// set AF_AGENT_INSTALLED_BIN itself — TestMain (above) already does that once for the whole
// package, which is what makes it safe for the pre-existing driver_test.go tests too, not just
// this file's own.
//
// Why the isolation is needed at all: mcpreg.ForSession(session.KindLcpp) ALWAYS includes the
// builtin "af" server (builtin.go's BuiltinAF.ready is unconditionally true, and
// knownKinds/ServedKinds now list lcpp — this PR's own def.go/materialize.go changes) — there is
// no opt-out for a builtin (compose's own opted map only ever applies to TENANT rows). For every
// other served kind that is harmless: af only gets WRITTEN into a config file, and it is the
// real CLI's own choice whether to ever launch it. lcpp is different — mcp.go's syncMCPServers
// calls mcpc.Manager.Sync, which dials (execs) a def it does not already hold an open connection
// for. Left alone, that would exec paths.ConfigExePath() — the real installed workspace-agent
// binary in this dev container, or (in a CI container with none installed, as PR #869's own
// first CI run measured) THIS TEST BINARY ITSELF, recursively.
func mcpTestHome(t *testing.T) {
	t.Helper()
	testHome(t)
	t.Setenv("AF_SECRET_KEY", "")
	prev := mcpServersForSession
	mcpServersForSession = mcpreg.ForSession
	t.Cleanup(func() { mcpServersForSession = prev })
}

// TestMCPToolsAbsentWithNoServerEnabled is the acceptance condition's own baseline, but against
// the REAL mcpreg.ForSession (mcpTestHome, unlike every other package test, restores it) rather
// than TestMain's default stub: with no user server registered, "af" is still the one server
// ForSession always returns (BuiltinAF.ready is unconditionally true), and it fails to connect
// (AF_AGENT_INSTALLED_BIN=/bin/false) — the tools sent to the engine must still carry no mcp__
// prefixed entries, proving an unreachable af never leaks a half-built tool into the model's own
// list.
func TestMCPToolsAbsentWithNoServerEnabled(t *testing.T) {
	mcpTestHome(t)
	var gotTools []harness.ToolDef
	client := &scriptedClient{
		turns:  []harness.Turn{{Content: "hi"}},
		onSend: func(_ []harness.Message, tools []harness.ToolDef) { gotTools = tools },
	}
	wireEngine(t, client)

	h, err := NewDriver().Resume(testMeta(t, "sess-no-mcp"))
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := h.Send(agents.TurnInput{Prompt: "hi"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitState(t, h, agents.TurnCompleted, agents.TurnFailed)

	for _, td := range gotTools {
		if len(td.Name) >= 5 && td.Name[:5] == "mcp__" {
			t.Fatalf("no MCP server was enabled, but the model was offered %q", td.Name)
		}
	}
}

// TestMCPToolReachesRealServer is the positive control for the wiring itself: a server enabled
// for lcpp must appear in the Registry as mcp__<server>__echo AND an actual tools/call for it
// must reach the fake process (not a stub) and its result must land in the store.
func TestMCPToolReachesRealServer(t *testing.T) {
	mcpTestHome(t)
	registerFakeMCPServer(t, "fx", "")

	prefixed := mcpc.PrefixToolName("fx", "echo")
	var gotTools []harness.ToolDef
	client := &scriptedClient{
		turns: []harness.Turn{
			{ToolCalls: []harness.ToolCall{{ID: "call-1", Name: prefixed, Arguments: `{"msg":"from-lcpp"}`}}, Finish: harness.FinishToolCalls},
			{Content: "done"},
		},
		onSend: func(_ []harness.Message, tools []harness.ToolDef) {
			if gotTools == nil {
				gotTools = tools
			}
		},
	}
	wireEngine(t, client)

	m := testMeta(t, "sess-mcp-echo") // default SkipPermissions (true) — no approval needed
	h, err := NewDriver().Resume(m)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := h.Send(agents.TurnInput{Prompt: "echo it"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitState(t, h, agents.TurnCompleted, agents.TurnFailed)

	found := false
	for _, td := range gotTools {
		if td.Name == prefixed {
			found = true
		}
	}
	if !found {
		t.Fatalf("Registry never advertised %q: %+v", prefixed, gotTools)
	}

	recs, _, err := Open(sidFor(m)).Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	var toolResult string
	for _, r := range recs {
		if r.Kind == KindToolResult {
			toolResult = r.Content
		}
	}
	if toolResult != "from-lcpp" {
		t.Fatalf("tool_result content = %q, want the fake server's own echo", toolResult)
	}
}

// TestMCPToolRequiresApprovalByDefault is decision 1's own acceptance condition: every external
// MCP tool is Mutates, so with SkipPermissions explicitly false it must block on approval exactly
// like the bash builtin does (driver_test.go's TestDriverApprovalGateBlocksAndAnswers), and Deny
// must stop the call from ever reaching the real server.
func TestMCPToolRequiresApprovalByDefault(t *testing.T) {
	mcpTestHome(t)
	registerFakeMCPServer(t, "fx", "")
	marker := filepath.Join(t.TempDir(), "should-not-exist.txt")

	prefixed := mcpc.PrefixToolName("fx", "touch")
	client := &scriptedClient{turns: []harness.Turn{
		{ToolCalls: []harness.ToolCall{{ID: "call-1", Name: prefixed, Arguments: `{"path":"` + marker + `"}`}}, Finish: harness.FinishToolCalls},
		{Content: "acknowledged"},
	}}
	wireEngine(t, client)

	m := testMeta(t, "sess-mcp-deny")
	skip := false
	m.SkipPermissions = &skip
	h, err := NewDriver().Resume(m)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := h.Send(agents.TurnInput{Prompt: "touch it"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	snap := waitState(t, h, agents.TurnWaitingInteraction)
	if snap.Interaction == nil || snap.Interaction.Kind != "question" {
		t.Fatalf("Interaction = %+v, want a pending question (MCP tools must gate on approval)", snap.Interaction)
	}
	if err := h.Respond(agents.InteractionReply{ID: snap.Interaction.ID, Decision: agents.DecisionDeny}); err != nil {
		t.Fatalf("Respond: %v", err)
	}
	waitState(t, h, agents.TurnCompleted, agents.TurnFailed)

	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("denied MCP tool call still reached the real server: %s exists", marker)
	}
}

// TestMCPSyncErrorDoesNotFailTurnAndIsNotedOnce is the acceptance condition "Sync が返す
// map[string]error はセッションを落とさない": a server whose command cannot even start must not
// stop the turn, and the failure is recorded once (deduplicated) rather than once per turn.
func TestMCPSyncErrorDoesNotFailTurnAndIsNotedOnce(t *testing.T) {
	mcpTestHome(t)
	if _, err := mcpreg.Create(mcpreg.ServerDef{
		Name: "broken", Transport: mcpreg.TransportStdio,
		Command: "/nonexistent/agent-fleet-lcpp-fake-mcp-binary",
		Enabled: true, Targets: mcpreg.Targets{Session: true}, Kinds: []string{session.KindLcpp},
	}); err != nil {
		t.Fatalf("mcpreg.Create: %v", err)
	}

	client := &scriptedClient{turns: []harness.Turn{{Content: "turn one"}, {Content: "turn two"}}}
	wireEngine(t, client)

	m := testMeta(t, "sess-mcp-broken")
	h, err := NewDriver().Resume(m)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := h.Send(agents.TurnInput{Prompt: "one"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitState(t, h, agents.TurnCompleted, agents.TurnFailed)
	snap, _ := h.Snapshot()
	if snap.TurnState != agents.TurnCompleted {
		t.Fatalf("turn state = %v, want completed (a broken MCP server must not fail the turn)", snap.TurnState)
	}
	if err := h.Send(agents.TurnInput{Prompt: "two"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitState(t, h, agents.TurnCompleted, agents.TurnFailed)

	recs, _, err := Open(sidFor(m)).Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	notes := 0
	for _, r := range recs {
		if r.Kind == KindSystemNote && r.Note == NoteMCPError {
			notes++
		}
	}
	if notes != 1 {
		t.Fatalf("mcp_error system notes = %d across 2 turns of the same failure, want exactly 1 (dedup)", notes)
	}
}

// TestDropHandleKillsMCPStdioChild is the acceptance condition "セッション終了で stdio の子が
// 残らないこと": once a turn has connected a stdio server, DropHandle must actually reap that
// child process, not just forget the in-memory handle.
func TestDropHandleKillsMCPStdioChild(t *testing.T) {
	mcpTestHome(t)
	pidFile := filepath.Join(t.TempDir(), "fake-mcp.pid")
	registerFakeMCPServer(t, "fx", pidFile)

	prefixed := mcpc.PrefixToolName("fx", "echo")
	client := &scriptedClient{turns: []harness.Turn{
		{ToolCalls: []harness.ToolCall{{ID: "call-1", Name: prefixed, Arguments: `{"msg":"hi"}`}}, Finish: harness.FinishToolCalls},
		{Content: "done"},
	}}
	wireEngine(t, client)

	m := testMeta(t, "sess-mcp-drop")
	h, err := NewDriver().Resume(m)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := h.Send(agents.TurnInput{Prompt: "echo it"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitState(t, h, agents.TurnCompleted, agents.TurnFailed)

	pid := waitForPidFile(t, pidFile)
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("setup: fake MCP server (pid %d) is not alive yet: %v", pid, err)
	}

	DropHandle(m.Name)

	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); err != nil {
			return // reaped
		}
		if time.Now().After(deadline) {
			t.Fatalf("MCP stdio child (pid %d) is still alive after DropHandle", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestInjectSessionNameCopiesOnlyBuiltinAF is injectSessionName's own unit test (mcp.go): it
// must add AF_SESSION_NAME to exactly the def identified as builtin af (Origin+ID), leave every
// other def's Env byte-for-byte as ForSession returned it, and never mutate the CALLER's own
// Env map in place — a later reader of that same map (mcpreg.Load's row, a second call against
// the same registry snapshot, another session's copy of the same defs slice) must not see this
// call's name leak in.
func TestInjectSessionNameCopiesOnlyBuiltinAF(t *testing.T) {
	builtinEnv := map[string]string{"PRESET": "keep-me"}
	builtin := mcpreg.ServerDef{ID: mcpreg.BuiltinAF, Origin: mcpreg.OriginBuiltin, Name: "af", Env: builtinEnv}
	externalEnv := map[string]string{"OTHER": "untouched"}
	external := mcpreg.ServerDef{ID: "user-1", Origin: mcpreg.OriginUser, Name: "af", Env: externalEnv}

	out := injectSessionName([]mcpreg.ServerDef{builtin, external}, "sess-x")
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2", len(out))
	}
	if got := out[0].Env[sessionNameEnvVar]; got != "sess-x" {
		t.Fatalf("builtin af Env[%s] = %q, want %q", sessionNameEnvVar, got, "sess-x")
	}
	if got := out[0].Env["PRESET"]; got != "keep-me" {
		t.Fatalf("builtin af Env[PRESET] = %q, want preserved %q", got, "keep-me")
	}
	// A user-registered server named "af" by coincidence (Origin=OriginUser, not the builtin's
	// OriginBuiltin+BuiltinAF pair) must be left alone — identification is never by Name.
	if _, ok := out[1].Env[sessionNameEnvVar]; ok {
		t.Fatalf("non-builtin server (Name coincidentally \"af\") unexpectedly got %s injected: %+v", sessionNameEnvVar, out[1].Env)
	}
	if len(out[1].Env) != 1 || out[1].Env["OTHER"] != "untouched" {
		t.Fatalf("non-builtin server Env = %+v, want untouched {OTHER: untouched}", out[1].Env)
	}

	if _, ok := builtinEnv[sessionNameEnvVar]; ok {
		t.Fatalf("injectSessionName mutated the caller's own Env map in place: %+v", builtinEnv)
	}
	if len(builtinEnv) != 1 {
		t.Fatalf("original builtin Env map grew in place: %+v", builtinEnv)
	}

	// A second call against the SAME original defs, with a DIFFERENT name, must not see any
	// trace of the first call — the source defs stay clean for every later/concurrent caller.
	out2 := injectSessionName([]mcpreg.ServerDef{builtin, external}, "sess-y")
	if got := out2[0].Env[sessionNameEnvVar]; got != "sess-y" {
		t.Fatalf("second call: builtin af Env[%s] = %q, want %q", sessionNameEnvVar, got, "sess-y")
	}
}

// TestInjectSessionNameNoopWithoutAName covers the guard: an unresolved/empty session name (the
// same "" mcpOwningSession's own cwd fallback already tolerates) must leave defs untouched rather
// than stamping an empty AF_SESSION_NAME that would make mcpOwningSession fail worse than the
// pre-fix "unset" case did.
func TestInjectSessionNameNoopWithoutAName(t *testing.T) {
	defs := []mcpreg.ServerDef{{ID: mcpreg.BuiltinAF, Origin: mcpreg.OriginBuiltin, Env: map[string]string{"X": "1"}}}
	out := injectSessionName(defs, "")
	if len(out) != 1 || out[0].Env[sessionNameEnvVar] != "" || out[0].Env["X"] != "1" {
		t.Fatalf("empty session name must be a no-op, got %+v", out)
	}
}

// TestMCPBuiltinAFChildLearnsOwningSessionName is the process-boundary regression: the builtin
// af MCP child lcpp spawns (mcpc/stdio.go's dialStdio, exec'd directly from the Agent daemon's
// own goroutine — there is no vendor CLI or per-session process boundary here the way tmux gives
// a TERMINAL claude session, or a thread config gives codex) must see the OWNING session's own
// name, matching mcpOwningSession's contract (mcpx/mcp_stdio.go) so generate_image and the other
// session-bound af tools resolve instead of silently going missing from tools/list.
//
// An ordinary EXTERNAL server runs alongside it in the very same turn as the live negative
// control: its child must come back with the Agent daemon's own (unmodified) env, proving the
// injection is scoped to the one def identified as builtin af even at the real process boundary,
// not merely in the unit test above. Both defs are reused as the SAME Go values across two
// sequential sessions (the same shape mcpreg.ForSession/mcpreg.Load can return turn after turn),
// which is what proves cross-session isolation is real rather than an artifact of building a
// fresh def per session. The driving test process's own AF_SESSION_NAME is set to a THIRD,
// unrelated sentinel first, so none of these results could be explained by "os.Environ() already
// happened to carry the right value" — only by lcpp's own explicit hand-down (or its absence).
func TestMCPBuiltinAFChildLearnsOwningSessionName(t *testing.T) {
	mcpTestHome(t)
	const daemonSentinel = "daemon-own-env-must-never-leak-into-a-session-child"
	t.Setenv(sessionNameEnvVar, daemonSentinel)

	afDef := fakeBuiltinAFDef(t, "")
	extDef := fakeMCPServerDef(t, "ext", "")
	mcpServersForSession = func(string) ([]mcpreg.ServerDef, error) {
		return []mcpreg.ServerDef{afDef, extDef}, nil
	}

	afWhoami := mcpc.PrefixToolName(afDef.Name, "whoami")
	extWhoami := mcpc.PrefixToolName("ext", "whoami")

	run := func(sessionName string) (afGot, extGot string) {
		t.Helper()
		client := &scriptedClient{turns: []harness.Turn{
			{ToolCalls: []harness.ToolCall{
				{ID: "call-af", Name: afWhoami, Arguments: "{}"},
				{ID: "call-ext", Name: extWhoami, Arguments: "{}"},
			}, Finish: harness.FinishToolCalls},
			{Content: "done"},
		}}
		wireEngine(t, client)

		m := testMeta(t, sessionName)
		h, err := NewDriver().Resume(m)
		if err != nil {
			t.Fatalf("Resume: %v", err)
		}
		if err := h.Send(agents.TurnInput{Prompt: "whoami"}); err != nil {
			t.Fatalf("Send: %v", err)
		}
		waitState(t, h, agents.TurnCompleted, agents.TurnFailed)

		recs, _, err := Open(sidFor(m)).Records()
		if err != nil {
			t.Fatalf("Records: %v", err)
		}
		results := map[string]string{}
		for _, r := range recs {
			if r.Kind == KindToolResult {
				results[r.ToolCallID] = r.Content
			}
		}
		dropAndWait(t, m.Name)
		return results["call-af"], results["call-ext"]
	}

	afA, extA := run("sess-af-whoami-a")
	if afA != "sess-af-whoami-a" {
		t.Fatalf("session a: builtin af child's own AF_SESSION_NAME = %q, want the owning session's name %q", afA, "sess-af-whoami-a")
	}
	if extA != daemonSentinel {
		t.Fatalf("session a: external MCP server's own AF_SESSION_NAME = %q, want the daemon's unmodified env (%q) — injection must not reach non-builtin defs", extA, daemonSentinel)
	}

	afB, extB := run("sess-af-whoami-b")
	if afB != "sess-af-whoami-b" {
		t.Fatalf("session b: builtin af child's own AF_SESSION_NAME = %q, want %q (must not bleed session a's name or the daemon's own)", afB, "sess-af-whoami-b")
	}
	if extB != daemonSentinel {
		t.Fatalf("session b: external MCP server's own AF_SESSION_NAME = %q, want the daemon's unmodified env (%q)", extB, daemonSentinel)
	}
}

func waitForPidFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			var pid int
			if _, err := fmt.Sscanf(string(b), "%d", &pid); err == nil {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestMCPUnreachableServerDoesNotSlowDownLaterTurns is the negative control sikdmnv's review
// asked for: a server that never connects must not keep costing every future turn a real
// connect attempt. `sort` is the fake server here (not runFakeMCPServer's own trampoline):
// launched as a stdio child, it reads and buffers stdin but writes NOTHING until stdin closes
// (unlike `cat`, which echoes immediately — tried first, and it back-fires: cat's own echo of
// the client's request bounces back through dispatch's request/response split in stdio.go as a
// bogus "response" carrying the client's own auto-reply error code, -32601, which the era
// detection in handshake reads as a legitimate legacy-era signal and the handshake proceeds
// down a different path entirely instead of ever timing out). `sort` stays silent for the whole
// handshake wait — a real, budget-bounded timeout — and DOES exit the moment stdin closes
// (unlike a genuinely hung process), so this test is not at the mercy of mcpc's unexported
// stdioKillGrace escalation either (mcpSyncBudget's own doc comment explains why that would
// otherwise add a fixed few seconds regardless of any shrinking done here).
func TestMCPUnreachableServerDoesNotSlowDownLaterTurns(t *testing.T) {
	mcpTestHome(t)
	sortBin, err := exec.LookPath("sort")
	if err != nil {
		t.Skipf("sort not found: %v", err)
	}

	oldBudget, oldBackoff := mcpSyncBudget, mcpSyncBackoff
	mcpSyncBudget = 150 * time.Millisecond
	mcpSyncBackoff = 10 * time.Second // long enough to still be cooling down for turn 2 below
	t.Cleanup(func() { mcpSyncBudget, mcpSyncBackoff = oldBudget, oldBackoff })

	if _, err := mcpreg.Create(mcpreg.ServerDef{
		Name: "hangs", Transport: mcpreg.TransportStdio, Command: sortBin,
		Enabled: true, Targets: mcpreg.Targets{Session: true}, Kinds: []string{session.KindLcpp},
	}); err != nil {
		t.Fatalf("mcpreg.Create: %v", err)
	}

	client := &scriptedClient{turns: []harness.Turn{{Content: "one"}, {Content: "two"}}}
	wireEngine(t, client)

	h, err := NewDriver().Resume(testMeta(t, "sess-mcp-hang"))
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}

	turnDuration := func(prompt string) time.Duration {
		t.Helper()
		start := time.Now()
		if err := h.Send(agents.TurnInput{Prompt: prompt}); err != nil {
			t.Fatalf("Send: %v", err)
		}
		waitState(t, h, agents.TurnCompleted, agents.TurnFailed)
		return time.Since(start)
	}

	first := turnDuration("one")
	second := turnDuration("two")

	// The first turn genuinely pays (close to) the shrunk handshake budget — a sanity check
	// that this test is exercising the timeout path at all, not silently failing fast for an
	// unrelated reason (e.g. "sort" not being found would fail Resume/Create above instead).
	if first < mcpSyncBudget/2 {
		t.Fatalf("first turn = %v, expected it to pay close to the sync budget (%v) — is this test actually hitting the connect timeout?", first, mcpSyncBudget)
	}
	// ...and mcpSyncBudget actually CAPS that wait — without it, the first turn would instead
	// pay mcpc's own uncapped defaultHandshakeTimeout (10s) against this same silent server.
	if first > 10*mcpSyncBudget {
		t.Fatalf("first turn = %v, expected it capped near the sync budget (%v) — is syncMCPServers still wrapping Sync in a timeout?", first, mcpSyncBudget)
	}
	// The second turn must be backed off entirely — no connect attempt, so no handshake wait.
	if second >= first/2 {
		t.Fatalf("second turn (%v) was not meaningfully faster than the first (%v) — a still-broken server should have been skipped by backoff, not retried", second, first)
	}
}
