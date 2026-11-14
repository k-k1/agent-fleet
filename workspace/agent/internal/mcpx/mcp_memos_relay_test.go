package mcpx

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memorelay"
)

// callTool runs one tools/call through the stdio dispatcher.
func callMemoTool(t *testing.T, name string, args map[string]any) string {
	t.Helper()
	a, _ := json.Marshal(args)
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": json.RawMessage(a)})
	return string(mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: params}))
}

// The model has no say in who added a memo: add_memo has no producer argument, a call that
// passes one is refused before anything is sent, and the caller the Agent is told is this
// server's own session.
func TestAddMemoProducerComesFromTheServerNotTheModel(t *testing.T) {
	asSession(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	t.Setenv("AF_CP_BASE_URL", memoTripwireCP(t))
	t.Setenv("AF_MEMO_TOKEN", "tripwire-token")
	agent := memoFakeAgent(t)
	mcpSourceSession = "slot01"
	t.Cleanup(func() { mcpSourceSession = "" })

	for _, forged := range []map[string]any{
		{"kind": "text", "body": "x", "producer": "someone-else"},
		{"kind": "text", "body": "x", "producerOrigin": "user"},
	} {
		resp := callMemoTool(t, "add_memo", forged)
		if !strings.Contains(resp, "unknown argument") {
			t.Errorf("add_memo with %v was not refused: %s", forged, resp)
		}
	}
	if got := agent.memoCalls(); len(got) != 0 {
		t.Fatalf("a refused call reached the Agent: %q", got)
	}
	callMemoTool(t, "add_memo", map[string]any{"kind": "text", "body": "x"})
	want := []string{`POST /memos {"from":"slot01","memo":{"body":"x","kind":"text"}}`}
	if got := agent.memoCalls(); !reflect.DeepEqual(got, want) {
		t.Errorf("add_memo reached the Agent as %q, want %q", got, want)
	}
}

// The operator assistant has no session: it names none, and the Agent stamps it as the
// operator.
func TestAddMemoFromTheOperatorNamesNoSession(t *testing.T) {
	oldWrite, oldSelf := writeEnabled(), selfReportOnly()
	t.Cleanup(func() { setWriteEnabled(oldWrite); setSelfReportOnly(oldSelf) })
	setWriteEnabled(true)
	setSelfReportOnly(false)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	t.Setenv("AF_CP_BASE_URL", memoTripwireCP(t))
	t.Setenv("AF_MEMO_TOKEN", "tripwire-token")
	agent := memoFakeAgent(t)

	callMemoTool(t, "add_memo", map[string]any{"kind": "text", "body": "x"})
	want := []string{`POST /memos {"from":"","memo":{"body":"x","kind":"text"}}`}
	if got := agent.memoCalls(); !reflect.DeepEqual(got, want) {
		t.Errorf("add_memo reached the Agent as %q, want %q", got, want)
	}
}

// The relay's "no bridge" answer reads as it did when the tools called the CP themselves.
func TestMemoRelayErrKeepsTheUnavailableWording(t *testing.T) {
	err := &agentHTTPError{StatusCode: 503, Body: `{"error":{"code":"memo_unavailable","message":"x"}}`}
	if got := memoRelayErr(err); got != "メモ機能はこの環境では利用できません（CP の公開URL/トークンが未設定）" {
		t.Errorf("memoRelayErr = %q", got)
	}
	err = &agentHTTPError{StatusCode: 409, Body: `{"error":{"code":"memo_locked"}}`}
	if got := memoRelayErr(err); !strings.Contains(got, "409") || !strings.Contains(got, "memo_locked") {
		t.Errorf("a CP refusal lost its status or code: %q", got)
	}
}

// A memo tool waits longer than the relay's two CP calls of GET /memos, or the relay's partial
// answer (memos without categories) never reaches it.
func TestMemoToolOutwaitsTheRelay(t *testing.T) {
	if memoAgentTimeout <= 2*memorelay.CPTimeoutDefault {
		t.Errorf("memoAgentTimeout %v is not longer than two CP calls (%v)", memoAgentTimeout, 2*memorelay.CPTimeoutDefault)
	}
}
