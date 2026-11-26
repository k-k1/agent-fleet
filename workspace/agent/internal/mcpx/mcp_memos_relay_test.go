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

const testRefJSON = `{"provider":"github","key":"acme/app#12","url":"https://github.com/acme/app/issues/12"}`

// testRefSorted is testRefJSON as the fake Agent records it (keys sorted).
const testRefSorted = `{"key":"acme/app#12","provider":"github","url":"https://github.com/acme/app/issues/12"}`

func setUpMemoSession(t *testing.T) *memoFake {
	t.Helper()
	asSession(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	t.Setenv("AF_CP_BASE_URL", memoTripwireCP(t))
	t.Setenv("AF_MEMO_TOKEN", "tripwire-token")
	agent := memoFakeAgent(t)
	mcpSourceSession = "slot01"
	t.Cleanup(func() { mcpSourceSession = "" })
	return agent
}

// add_memo and update_memo advertise the reference argument, in both language variants, and the
// session's own text says what it is, that the item is not copied and what a duplicate answers.
func TestMemoToolsAdvertiseTheReference(t *testing.T) {
	for _, spec := range memoTools() {
		if spec.name != "add_memo" && spec.name != "update_memo" {
			continue
		}
		for variant, f := range map[string]func() map[string]any{"operator": spec.operator, "session": spec.session} {
			tool := f()
			props := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
			ref, ok := props["reference"].(map[string]any)
			if !ok {
				t.Errorf("%s/%s has no reference argument", spec.name, variant)
				continue
			}
			rp := ref["properties"].(map[string]any)
			for _, k := range []string{"provider", "key", "url"} {
				if _, ok := rp[k]; !ok {
					t.Errorf("%s/%s reference lacks %s", spec.name, variant, k)
				}
			}
		}
	}
	for _, spec := range memoTools() {
		if spec.name != "add_memo" {
			continue
		}
		d := spec.session()["description"].(string)
		for _, want := range []string{"reference", "not copied", "existing: true"} {
			if !strings.Contains(d, want) {
				t.Errorf("add_memo's text does not say %q", want)
			}
		}
	}
}

// The reference leaves the MCP process as the model wrote it, on a create and on an update, and
// an update's {} stays {}.
func TestMemoToolsSendTheReference(t *testing.T) {
	agent := setUpMemoSession(t)
	agent.answer = `{"id":"m1","state":"held","reference":` + testRefJSON + `}`
	callMemoTool(t, "add_memo", map[string]any{"kind": "text", "category": "q", "reference": json.RawMessage(testRefJSON)})
	callMemoTool(t, "update_memo", map[string]any{"id": "m1", "reference": json.RawMessage(testRefJSON)})
	agent.answer = `{"id":"m1","state":"held"}`
	callMemoTool(t, "update_memo", map[string]any{"id": "m1", "reference": map[string]any{}})
	callMemoTool(t, "update_memo", map[string]any{"id": "m1", "body": "b"})
	want := []string{
		`POST /memos {"from":"slot01","memo":{"category":"q","kind":"text","reference":` + testRefSorted + `}}`,
		`PATCH /memos/m1 {"id":"m1","reference":` + testRefSorted + `}`,
		`PATCH /memos/m1 {"id":"m1","reference":{}}`,
		`PATCH /memos/m1 {"body":"b","id":"m1"}`,
	}
	if got := agent.memoCalls(); !reflect.DeepEqual(got, want) {
		t.Errorf("the Agent got\n%q\nwant\n%q", got, want)
	}
}

// update_memo names this server's own session in the query, as add_memo does in its body, so the
// relay can file a repo the model names under the session's space; the operator names none.
func TestUpdateMemoNamesTheServersOwnSession(t *testing.T) {
	agent := setUpMemoSession(t)
	callMemoTool(t, "update_memo", map[string]any{"id": "m1", "repo": "app"})
	if got := agent.memoQueries(); !reflect.DeepEqual(got, []string{"from=slot01"}) {
		t.Errorf("a session's update_memo sent the queries %q, want from=slot01", got)
	}

	oldWrite := writeEnabled()
	t.Cleanup(func() { setWriteEnabled(oldWrite) })
	setSelfReportOnly(false)
	setWriteEnabled(true)
	agent = memoFakeAgent(t)
	callMemoTool(t, "update_memo", map[string]any{"id": "m1", "repo": "app"})
	if got := agent.memoQueries(); !reflect.DeepEqual(got, []string{""}) {
		t.Errorf("the operator's update_memo sent the queries %q, want none", got)
	}
}

// The answer of the Control Plane, `existing` and the state, is what the session reads.
func TestMemoToolPassesTheAnswerBack(t *testing.T) {
	agent := setUpMemoSession(t)
	agent.answer = `{"id":"m1","state":"held","existing":true,"reference":` + testRefJSON + `}`
	resp := callMemoTool(t, "add_memo", map[string]any{"kind": "text", "reference": json.RawMessage(testRefJSON)})
	for _, want := range []string{`existing`, `held`} {
		if !strings.Contains(resp, want) {
			t.Errorf("the result lost %q: %s", want, resp)
		}
	}
	if strings.Contains(resp, `"isError":true`) {
		t.Errorf("a kept reference was reported as an error: %s", resp)
	}
}

// A Control Plane from before references ignores the field: the answer has no reference, and the
// tool says so instead of reporting success. An update that clears one, or sent none, is fine.
func TestMemoToolSaysWhenAnOlderControlPlaneDroppedTheReference(t *testing.T) {
	agent := setUpMemoSession(t)
	agent.answer = `{"id":"m1","state":"held","body":"note"}`
	for _, c := range []struct {
		tool string
		args map[string]any
		bad  bool
	}{
		{"add_memo", map[string]any{"kind": "text", "body": "note", "reference": json.RawMessage(testRefJSON)}, true},
		{"update_memo", map[string]any{"id": "m1", "reference": json.RawMessage(testRefJSON)}, true},
		{"update_memo", map[string]any{"id": "m1", "reference": map[string]any{}}, false},
		{"update_memo", map[string]any{"id": "m1", "body": "x"}, false},
		{"add_memo", map[string]any{"kind": "text", "body": "note"}, false},
	} {
		resp := callMemoTool(t, c.tool, c.args)
		isErr := strings.Contains(resp, `"isError":true`)
		if isErr != c.bad {
			t.Errorf("%s %v: error=%v, want %v: %s", c.tool, c.args, isErr, c.bad, resp)
		}
		if c.bad && !strings.Contains(resp, "NOT kept") {
			t.Errorf("%s: the result does not say the reference was not kept: %s", c.tool, resp)
		}
	}
}
