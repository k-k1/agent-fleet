package mcpreg

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// The call a 2026-07-28 CLI sends (claude 2.1.332 and opencode 2.4.1 both declare form and url
// elicitation themselves, measured 2026-11-26). The arguments are spaced oddly on purpose: they
// must reach the server byte for byte.
const elicitCallBody = `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"send_mail","arguments":{"to": "a@example.com",  "body":"hi","n":1.50},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"cli","version":"1"},"io.modelcontextprotocol/clientCapabilities":{"roots":{},"elicitation":{"form":{},"url":{}}},"progressToken":3}}}`

const elicitArgs = `{"to": "a@example.com",  "body":"hi","n":1.50}`

// googleInputRequired is the shape Google's Workspace servers answer before a sensitive call
// (developers.google.com/workspace/guides/mcp-elicitation, read 2026-11-26).
func googleInputRequired(id string) string {
	return `{"jsonrpc":"2.0","id":` + id + `,"result":{"resultType":"input_required","inputRequests":{"approval_choice":{"method":"elicitation/create","params":{"mode":"form","message":"Send **mail** to a@example.com","requestedSchema":{"type":"object","required":["approval_choice"],"properties":{"approval_choice":{"type":"string","title":"Allow?","enum":["ALLOW_ONCE","ALLOW_TOOL_ALWAYS","DENY_ONCE"],"enumNames":["Allow once","Always allow this tool","Deny"]}}}}}},"requestState":"{\"elicitationId\":\"e1\",\"requestBinding\":\"b1\"}"}}`
}

const completeResult = `{"jsonrpc":"2.0","id":7,"result":{"resultType":"complete","content":[{"type":"text","text":"sent"}]}}`

func flagElicitation(t *testing.T, mode string) {
	t.Helper()
	err := secrets.Update(func(s *secrets.Data) error {
		for i := range s.MCP {
			if s.MCP[i].ID == relayTestID {
				s.MCP[i].Elicitation = mode
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// resetElicitations empties the package's pending list around a test.
func resetElicitations(t *testing.T) {
	t.Helper()
	clear := func() {
		elicitations.mu.Lock()
		elicitations.pending = map[string]*pendingElicit{}
		elicitations.mu.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

func setElicitTimes(t *testing.T, wait, keepalive time.Duration) {
	t.Helper()
	ow, ok := elicitWait, elicitKeepalive
	elicitWait, elicitKeepalive = wait, keepalive
	t.Cleanup(func() { elicitWait, elicitKeepalive = ow, ok })
}

// jsonAnswer answers every request with the body chosen for its index.
func jsonAnswer(bodies func(n int) string) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, r *http.Request, n int) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, bodies(n))
	}
}

// waitPending waits for n held calls.
func waitPending(t *testing.T, n int) []PendingElicitation {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		p := PendingElicitations()
		if len(p) == n {
			return p
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending confirmations: %d, want %d", len(p), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type callResult struct {
	resp *http.Response
	body string
}

// startCall sends the CLI's call in the background.
func (e *relayEnv) startCall(body string, hdr map[string]string) <-chan callResult {
	ch := make(chan callResult, 1)
	go func() {
		resp := e.do(http.MethodPost, relayPath(), strings.NewReader(body), hdr)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		ch <- callResult{resp, string(b)}
	}()
	return ch
}

func waitCall(t *testing.T, ch <-chan callResult) callResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("the CLI got no answer")
		return callResult{}
	}
}

var sseHeaders = map[string]string{"Accept": "application/json, text/event-stream", "Content-Type": "application/json"}

// sseMessages are the data payloads of an event stream, in order.
func sseMessages(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	var data []string
	flush := func() {
		if len(data) == 0 {
			return
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &m); err != nil {
			t.Fatalf("event data is not JSON: %q", strings.Join(data, "\n"))
		}
		out = append(out, m)
		data = nil
	}
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			flush()
			continue
		}
		if v, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimPrefix(v, " "))
		}
	}
	flush()
	return out
}

func paramsOf(t *testing.T, body string) map[string]json.RawMessage {
	t.Helper()
	var top struct {
		ID     json.RawMessage            `json:"id"`
		Params map[string]json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal([]byte(body), &top); err != nil {
		t.Fatalf("server got unreadable JSON: %q", body)
	}
	top.Params["id"] = top.ID
	return top.Params
}

func capsOf(t *testing.T, body string) map[string]any {
	t.Helper()
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(paramsOf(t, body)["_meta"], &meta); err != nil {
		t.Fatal(err)
	}
	var caps map[string]any
	if err := json.Unmarshal(meta[metaClientCaps], &caps); err != nil {
		t.Fatal(err)
	}
	return caps
}

func TestElicitRelayDeclaresFormOnlyAndKeepsTheRest(t *testing.T) {
	resetElicitations(t)
	up := newUpstream(t, jsonAnswer(func(int) string { return `{"jsonrpc":"2.0","id":1,"result":{"resultType":"complete","tools":[]}}` }))
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{"roots":{"listChanged":true},"elicitation":{"form":{},"url":{}}}}}}`
	resp := e.do(http.MethodPost, relayPath(), strings.NewReader(body), sseHeaders)
	if got := readAll(t, resp); resp.StatusCode != 200 || !strings.Contains(got, `"tools":[]`) {
		t.Fatalf("status %d body %q", resp.StatusCode, got)
	}
	caps := capsOf(t, up.at(0).Body)
	if !reflect.DeepEqual(caps["elicitation"], map[string]any{"form": map[string]any{}}) {
		t.Fatalf("elicitation capability %v, want form only", caps["elicitation"])
	}
	if !reflect.DeepEqual(caps["roots"], map[string]any{"listChanged": true}) {
		t.Fatalf("the CLI's other capabilities were lost: %v", caps)
	}

	// A request with no capabilities at all gets one.
	body = `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`
	readAll(t, e.do(http.MethodPost, relayPath(), strings.NewReader(body), sseHeaders))
	if caps := capsOf(t, up.at(1).Body); !reflect.DeepEqual(caps, map[string]any{"elicitation": map[string]any{"form": map[string]any{}}}) {
		t.Fatalf("capabilities %v", caps)
	}
}

func TestElicitRelayLeavesOtherRequestsAsBytes(t *testing.T) {
	resetElicitations(t)
	up := newUpstream(t, jsonAnswer(func(int) string { return googleInputRequired("7") }))
	e := newRelayEnv(t, up)
	legacy := `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{"elicitation":{"form":{},"url":{}}}}}`

	// An unflagged definition: nothing is read, the input_required result reaches the CLI as is.
	resp := e.do(http.MethodPost, relayPath(), strings.NewReader(elicitCallBody), sseHeaders)
	if got := readAll(t, resp); got != googleInputRequired("7") {
		t.Fatalf("unflagged: CLI got %q", got)
	}
	if up.at(0).Body != elicitCallBody {
		t.Fatalf("unflagged: body changed: %q", up.at(0).Body)
	}

	// A flagged definition, an older-era request: bytes too.
	flagElicitation(t, ElicitationRelay)
	readAll(t, e.do(http.MethodPost, relayPath(), strings.NewReader(legacy), sseHeaders))
	if up.at(1).Body != legacy {
		t.Fatalf("older era: body changed: %q", up.at(1).Body)
	}
	if n := len(PendingElicitations()); n != 0 {
		t.Fatalf("%d calls held", n)
	}
}

func TestElicitRelayHoldsAndRetriesWithThePersonsChoice(t *testing.T) {
	resetElicitations(t)
	up := newUpstream(t, jsonAnswer(func(n int) string {
		if n == 0 {
			return googleInputRequired("7")
		}
		return completeResult
	}))
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	ch := e.startCall(elicitCallBody, sseHeaders)

	p := waitPending(t, 1)[0]
	if p.Tool != "send_mail" || p.ServerID != relayTestID || p.ServerName != "hosted" || string(p.Arguments) != elicitArgs {
		t.Fatalf("pending %+v (arguments %s)", p, p.Arguments)
	}
	want := []ElicitInput{{Key: "approval_choice", Message: "Send **mail** to a@example.com", Fields: []ElicitField{{
		Name: "approval_choice", Title: "Allow?", Required: true,
		Options: []ElicitOption{{"ALLOW_ONCE", "Allow once"}, {"ALLOW_TOOL_ALWAYS", "Always allow this tool"}, {"DENY_ONCE", "Deny"}},
	}}}}
	if !reflect.DeepEqual(p.Inputs, want) {
		t.Fatalf("inputs %+v", p.Inputs)
	}
	if !p.Deadline.After(p.CreatedAt) {
		t.Fatalf("deadline %v, created %v", p.Deadline, p.CreatedAt)
	}
	if up.count() != 1 {
		t.Fatalf("the call was retried before anyone answered (%d)", up.count())
	}

	if err := AnswerElicitation(p.ID, ElicitAnswer{Choices: map[string]map[string]string{"approval_choice": {"approval_choice": "ALLOW_ONCE"}}}); err != nil {
		t.Fatal(err)
	}
	res := waitCall(t, ch)
	msgs := sseMessages(t, res.body)
	if res.resp.Header.Get("Content-Type") != "text/event-stream" || len(msgs) == 0 {
		t.Fatalf("CLI got %q (%s)", res.body, res.resp.Header.Get("Content-Type"))
	}
	last := msgs[len(msgs)-1]
	if fmt.Sprint(last["id"]) != "7" || !strings.Contains(fmt.Sprint(last["result"]), "sent") {
		t.Fatalf("final message %v", last)
	}
	if strings.Contains(res.body, "input_required") {
		t.Fatal("the CLI saw input_required")
	}

	if up.count() != 2 {
		t.Fatalf("server got %d requests, want 2", up.count())
	}
	retry := paramsOf(t, up.at(1).Body)
	if string(retry["arguments"]) != elicitArgs {
		t.Fatalf("arguments changed: %s", retry["arguments"])
	}
	if string(retry["requestState"]) != `"{\"elicitationId\":\"e1\",\"requestBinding\":\"b1\"}"` {
		t.Fatalf("requestState %s", retry["requestState"])
	}
	var ir map[string]map[string]any
	if err := json.Unmarshal(retry["inputResponses"], &ir); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ir, map[string]map[string]any{"approval_choice": {"action": "accept", "content": map[string]any{"approval_choice": "ALLOW_ONCE"}}}) {
		t.Fatalf("inputResponses %v", ir)
	}
	if string(retry["id"]) != "7" || string(retry["name"]) != `"send_mail"` {
		t.Fatalf("id %s name %s", retry["id"], retry["name"])
	}
	if caps := capsOf(t, up.at(1).Body); !reflect.DeepEqual(caps["elicitation"], map[string]any{"form": map[string]any{}}) {
		t.Fatalf("retry capabilities %v", caps)
	}
	if n := len(PendingElicitations()); n != 0 {
		t.Fatalf("%d still pending", n)
	}
}

func TestElicitRelayDeclineAndTimeLimitSendDecline(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer bool
	}{{"person declines", true}, {"time limit", false}} {
		t.Run(tc.name, func(t *testing.T) {
			resetElicitations(t)
			setElicitTimes(t, 300*time.Millisecond, time.Hour)
			if tc.answer {
				setElicitTimes(t, time.Hour, time.Hour)
			}
			up := newUpstream(t, jsonAnswer(func(n int) string {
				if n == 0 {
					return googleInputRequired("7")
				}
				return `{"jsonrpc":"2.0","id":7,"result":{"resultType":"complete","isError":true,"content":[{"type":"text","text":"Action canceled"}]}}`
			}))
			e := newRelayEnv(t, up)
			flagElicitation(t, ElicitationRelay)
			ch := e.startCall(elicitCallBody, sseHeaders)
			p := waitPending(t, 1)[0]
			if tc.answer {
				// A decline carries no choice, whatever the Console sent along.
				if err := AnswerElicitation(p.ID, ElicitAnswer{Decline: true, Choices: map[string]map[string]string{"approval_choice": {"approval_choice": "ALLOW_ONCE"}}}); err != nil {
					t.Fatal(err)
				}
			}
			res := waitCall(t, ch)
			if !strings.Contains(res.body, "Action canceled") {
				t.Fatalf("CLI got %q", res.body)
			}
			var ir map[string]map[string]any
			if err := json.Unmarshal(paramsOf(t, up.at(1).Body)["inputResponses"], &ir); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(ir, map[string]map[string]any{"approval_choice": {"action": "decline"}}) {
				t.Fatalf("inputResponses %v", ir)
			}
		})
	}
}

func TestElicitRelayDropsTheQuestionWhenTheCLILeaves(t *testing.T) {
	resetElicitations(t)
	setElicitTimes(t, time.Hour, 20*time.Millisecond)
	up := newUpstream(t, jsonAnswer(func(int) string { return googleInputRequired("7") }))
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	req, _ := http.NewRequest(http.MethodPost, e.base+relayPath(), strings.NewReader(elicitCallBody))
	req.Header.Set(RelayKeyHeader, e.key)
	for k, v := range sseHeaders {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	p := waitPending(t, 1)[0]
	resp.Body.Close() // the CLI gives up (a tool-call timeout)
	waitPending(t, 0)
	if err := AnswerElicitation(p.ID, ElicitAnswer{Decline: true}); !errors.Is(err, ErrElicitationNotFound) {
		t.Fatalf("answer after the CLI left: %v", err)
	}
	if up.count() != 1 {
		t.Fatalf("the call was retried after the CLI left (%d)", up.count())
	}
}

// Claude Code 2.1.332 ends an HTTP tool call after 60 s unless a progress notification arrives
// (measured 2026-11-26); the held call must send them on the CLI's own token.
func TestElicitRelaySendsProgressWhileHeld(t *testing.T) {
	resetElicitations(t)
	setElicitTimes(t, time.Hour, 20*time.Millisecond)
	up := newUpstream(t, jsonAnswer(func(n int) string {
		if n == 0 {
			return googleInputRequired("7")
		}
		return completeResult
	}))
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	ch := e.startCall(elicitCallBody, sseHeaders)
	p := waitPending(t, 1)[0]
	time.Sleep(150 * time.Millisecond)
	if err := AnswerElicitation(p.ID, ElicitAnswer{Choices: map[string]map[string]string{"approval_choice": {"approval_choice": "DENY_ONCE"}}}); err != nil {
		t.Fatal(err)
	}
	msgs := sseMessages(t, waitCall(t, ch).body)
	progress := 0
	for _, m := range msgs[:len(msgs)-1] {
		params, _ := m["params"].(map[string]any)
		if m["method"] != "notifications/progress" || fmt.Sprint(params["progressToken"]) != "3" {
			t.Fatalf("unexpected message before the result: %v", m)
		}
		progress++
	}
	if progress < 2 {
		t.Fatalf("%d progress notifications", progress)
	}
	if fmt.Sprint(msgs[len(msgs)-1]["id"]) != "7" {
		t.Fatalf("last message %v", msgs[len(msgs)-1])
	}
}

func TestElicitRelayAnswersJSONToACLIWithoutStreams(t *testing.T) {
	resetElicitations(t)
	setElicitTimes(t, time.Hour, 20*time.Millisecond)
	up := newUpstream(t, jsonAnswer(func(n int) string {
		if n == 0 {
			return googleInputRequired("7")
		}
		return completeResult
	}))
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	ch := e.startCall(elicitCallBody, map[string]string{"Accept": "application/json", "Content-Type": "application/json"})
	p := waitPending(t, 1)[0]
	time.Sleep(60 * time.Millisecond) // keepalive ticks must not write into a JSON answer
	if err := AnswerElicitation(p.ID, ElicitAnswer{Choices: map[string]map[string]string{"approval_choice": {"approval_choice": "ALLOW_ONCE"}}}); err != nil {
		t.Fatal(err)
	}
	res := waitCall(t, ch)
	if res.body != completeResult || res.resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("CLI got %q (%s)", res.body, res.resp.Header.Get("Content-Type"))
	}
}

func TestElicitRelayPassesAStreamAndHoldsItsInputRequiredEvent(t *testing.T) {
	resetElicitations(t)
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request, n int) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Set-Cookie", "s=1")
		if n == 0 {
			fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\",\"params\":{\"level\":\"info\",\"data\":\"checking\"}}\n\n")
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", googleInputRequired("7"))
			return
		}
		fmt.Fprintf(w, "id: 9\nevent: message\ndata: %s\n\n", completeResult)
	})
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	ch := e.startCall(elicitCallBody, sseHeaders)
	p := waitPending(t, 1)[0]
	if err := AnswerElicitation(p.ID, ElicitAnswer{Choices: map[string]map[string]string{"approval_choice": {"approval_choice": "ALLOW_ONCE"}}}); err != nil {
		t.Fatal(err)
	}
	res := waitCall(t, ch)
	msgs := sseMessages(t, res.body)
	if len(msgs) != 2 || msgs[0]["method"] != "notifications/message" || fmt.Sprint(msgs[1]["id"]) != "7" {
		t.Fatalf("CLI got %q", res.body)
	}
	if strings.Contains(res.body, "input_required") || res.resp.Header.Get("Set-Cookie") != "" {
		t.Fatalf("CLI got %q with headers %v", res.body, res.resp.Header)
	}
	if !strings.Contains(res.body, "id: 9\n") {
		t.Fatalf("the server's event was not passed whole: %q", res.body)
	}
}

func TestElicitRelayRefusesInputItCannotAskFor(t *testing.T) {
	cases := map[string]string{
		"free text": `{"type":"object","properties":{"note":{"type":"string"}}}`,
		"a number":  `{"type":"object","properties":{"n":{"type":"number","enum":[1,2]}}}`,
	}
	for name, schema := range cases {
		t.Run(name, func(t *testing.T) {
			resetElicitations(t)
			up := newUpstream(t, jsonAnswer(func(int) string {
				return `{"jsonrpc":"2.0","id":7,"result":{"resultType":"input_required","inputRequests":{"k":{"method":"elicitation/create","params":{"mode":"form","message":"m","requestedSchema":` + schema + `}}},"requestState":"s"}}`
			}))
			e := newRelayEnv(t, up)
			flagElicitation(t, ElicitationRelay)
			res := waitCall(t, e.startCall(elicitCallBody, sseHeaders))
			if !strings.Contains(res.body, `"isError":true`) || !strings.Contains(res.body, "cannot ask a person for") {
				t.Fatalf("CLI got %q", res.body)
			}
			if up.count() != 1 || len(PendingElicitations()) != 0 {
				t.Fatalf("requests %d, pending %d", up.count(), len(PendingElicitations()))
			}
		})
	}
	t.Run("url mode and sampling", func(t *testing.T) {
		for _, req := range []string{
			`{"method":"elicitation/create","params":{"mode":"url","message":"m","url":"https://x.example"}}`,
			`{"method":"sampling/createMessage","params":{}}`,
		} {
			if _, err := parseInputRequired(json.RawMessage(`{"resultType":"input_required","inputRequests":{"k":` + req + `}}`)); err == nil {
				t.Errorf("accepted %s", req)
			}
		}
	})
}

func TestElicitRelayStopsAServerThatKeepsAsking(t *testing.T) {
	resetElicitations(t)
	up := newUpstream(t, jsonAnswer(func(int) string { return googleInputRequired("7") }))
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	ch := e.startCall(elicitCallBody, sseHeaders)
	for i := 0; i < elicitMaxRounds; i++ {
		p := waitPending(t, 1)[0]
		if err := AnswerElicitation(p.ID, ElicitAnswer{Choices: map[string]map[string]string{"approval_choice": {"approval_choice": "ALLOW_ONCE"}}}); err != nil {
			t.Fatal(err)
		}
	}
	res := waitCall(t, ch)
	if !strings.Contains(res.body, "kept asking") || strings.Contains(res.body, "input_required") {
		t.Fatalf("CLI got %q", res.body)
	}
	if up.count() != elicitMaxRounds+1 {
		t.Fatalf("server got %d requests", up.count())
	}
}

func TestAnswerElicitationChecksTheChoices(t *testing.T) {
	resetElicitations(t)
	ir, err := parseInputRequired(json.RawMessage(`{"resultType":"input_required","inputRequests":{"a":{"method":"elicitation/create","params":{"requestedSchema":{"type":"object","required":["c"],"properties":{"c":{"type":"string","enum":["ALLOW_ONCE","DENY_ONCE"]},"o":{"type":"string","oneOf":[{"const":"x","title":"Ex"}]}}}}}},"requestState":"s"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := ir.inputs[0].Fields[1]; got.Name != "o" || got.Required || !reflect.DeepEqual(got.Options, []ElicitOption{{"x", "Ex"}}) {
		t.Fatalf("oneOf field %+v", got)
	}
	p, err := elicitations.add(ServerDef{ID: "s", Name: "n"}, elicitCall{tool: "t", params: map[string]json.RawMessage{}}, ir)
	if err != nil {
		t.Fatal(err)
	}
	bad := []map[string]map[string]string{
		nil, // a required field without a choice
		{"a": {"c": "ALLOW_EVERYTHING"}},
		{"a": {"c": "ALLOW_ONCE", "extra": "x"}},
		{"a": {"c": "ALLOW_ONCE"}, "b": {"c": "ALLOW_ONCE"}},
		{"a": {"c": "ALLOW_ONCE", "o": "y"}},
	}
	for _, c := range bad {
		if err := AnswerElicitation(p.ID, ElicitAnswer{Choices: c}); !errors.Is(err, ErrElicitationAnswer) {
			t.Errorf("%v: %v", c, err)
		}
	}
	if err := AnswerElicitation(p.ID, ElicitAnswer{Choices: map[string]map[string]string{"a": {"c": "DENY_ONCE"}}}); err != nil {
		t.Fatal(err)
	}
	if got := <-p.answer; !reflect.DeepEqual(got.Choices, map[string]map[string]string{"a": {"c": "DENY_ONCE"}}) {
		t.Fatalf("delivered %+v", got)
	}
	if err := AnswerElicitation(p.ID, ElicitAnswer{Decline: true}); !errors.Is(err, ErrElicitationNotFound) {
		t.Fatalf("second answer: %v", err)
	}
}

func TestElicitRelayCapsTheHeldCalls(t *testing.T) {
	resetElicitations(t)
	ir := inputRequired{inputs: []ElicitInput{{Key: "a"}}}
	for i := 0; i < elicitMaxPending; i++ {
		if _, err := elicitations.add(ServerDef{ID: "s"}, elicitCall{params: map[string]json.RawMessage{}}, ir); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := elicitations.add(ServerDef{ID: "s"}, elicitCall{params: map[string]json.RawMessage{}}, ir); err == nil {
		t.Fatal("a call past the cap was held")
	}
}

func TestValidateElicitation(t *testing.T) {
	base := ServerDef{Name: "g", Origin: OriginUser, Transport: TransportHTTP, URL: "https://mcp.example/mcp"}
	oauth := base
	oauth.Auth = AuthOAuth
	for _, tc := range []struct {
		d    ServerDef
		mode string
		code string
	}{
		{oauth, ElicitationRelay, ""},
		{oauth, "", ""},
		{base, ElicitationRelay, CodeElicitOAuth},
		{oauth, "cli", CodeElicitUnknown},
	} {
		tc.d.Elicitation = tc.mode
		err := Validate(tc.d)
		var ve *ValidationError
		switch {
		case tc.code == "" && err != nil:
			t.Errorf("%q auth %q: %v", tc.mode, tc.d.Auth, err)
		case tc.code != "" && (!errors.As(err, &ve) || ve.Code != tc.code):
			t.Errorf("%q auth %q: %v, want %s", tc.mode, tc.d.Auth, err, tc.code)
		}
	}
}

// At the time limit the relay declines only a call still on the list: an answer that got there
// first is the one sent.
func TestElicitTimeLimitYieldsToAnAnswerThatWon(t *testing.T) {
	resetElicitations(t)
	ir := inputRequired{inputs: []ElicitInput{{Key: "a", Fields: []ElicitField{{Name: "c", Required: true, Options: []ElicitOption{{"ALLOW_ONCE", "Allow"}}}}}}}
	p, err := elicitations.add(ServerDef{ID: "s"}, elicitCall{params: map[string]json.RawMessage{}}, ir)
	if err != nil {
		t.Fatal(err)
	}
	if err := AnswerElicitation(p.ID, ElicitAnswer{Choices: map[string]map[string]string{"a": {"c": "ALLOW_ONCE"}}}); err != nil {
		t.Fatal(err)
	}
	if elicitations.take(p.ID) {
		t.Fatal("an answered call was still taken as unanswered")
	}
	if got := <-p.answer; got.Decline {
		t.Fatalf("delivered %+v", got)
	}
	q, _ := elicitations.add(ServerDef{ID: "s"}, elicitCall{params: map[string]json.RawMessage{}}, ir)
	if !elicitations.take(q.ID) {
		t.Fatal("an unanswered call was not taken")
	}
}
