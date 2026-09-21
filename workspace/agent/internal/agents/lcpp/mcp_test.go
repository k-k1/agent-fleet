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

// registerFakeMCPServer is fakeMCPServerDef plus the mcpreg.Create call every test below needs.
func registerFakeMCPServer(t *testing.T, name, pidFile string) {
	t.Helper()
	if _, err := mcpreg.Create(fakeMCPServerDef(t, name, pidFile)); err != nil {
		t.Fatalf("mcpreg.Create: %v", err)
	}
}

// mcpTestHome is testHome plus the isolation every test in this file needs on top of it:
// mcpreg.ForSession(session.KindLcpp) ALWAYS includes the builtin "af" server (builtin.go's
// BuiltinAF.ready is unconditionally true, and knownKinds/ServedKinds now list lcpp — see this
// PR's own def.go/materialize.go changes) — there is no opt-out for a builtin (compose's own
// opted map only ever applies to TENANT rows). For every other served kind that is harmless: af
// only gets WRITTEN into a config file, and it is the real CLI's own choice whether to ever
// launch it. lcpp is different — mcp.go's syncMCPServers calls mcpc.Manager.Sync, which DIALS
// (execs) every returned def immediately, every turn. Left alone, that would exec
// paths.ConfigExePath() — the real installed workspace-agent binary in this dev container, or
// (worse, in a CI container with none installed) THIS TEST BINARY ITSELF, which would run as a
// plain, unfiltered `go test` invocation and execute every test in this package all over again.
// AF_AGENT_INSTALLED_BIN overrides that resolution (paths.InstalledExePath's own env escape
// hatch) to /bin/false: a real, harmless binary that starts and exits(1) immediately, so Sync
// gets a fast, deterministic connect failure for "af" instead of spawning anything real.
func mcpTestHome(t *testing.T) {
	t.Helper()
	testHome(t)
	t.Setenv("AF_SECRET_KEY", "")
	t.Setenv("AF_AGENT_INSTALLED_BIN", "/bin/false")
}

// TestMCPToolsAbsentWithNoServerEnabled is the acceptance condition's own baseline: with no MCP
// server enabled, the tools sent to the engine are unaffected (no mcp__ prefixed entries), and
// the turn behaves exactly as driver_test.go's plain TestDriverSendPersistsTurnAndCompletes.
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
