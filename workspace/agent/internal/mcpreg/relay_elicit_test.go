package mcpreg

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
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

const elicitState = `"{\"elicitationId\":\"e1\",\"requestBinding\":\"b1\"}"`

// googleInputRequired is the shape Google's Workspace servers answer before a sensitive call
// (developers.google.com/workspace/guides/mcp-elicitation, read 2026-11-26).
func googleInputRequired(id string) string {
	return `{"jsonrpc":"2.0","id":` + id + `,"result":{"resultType":"input_required","inputRequests":{"approval_choice":{"method":"elicitation/create","params":{"mode":"form","message":"Send **mail** to a@example.com","requestedSchema":{"type":"object","required":["approval_choice"],"properties":{"approval_choice":{"type":"string","title":"Allow?","enum":["ALLOW_ONCE","ALLOW_TOOL_ALWAYS","DENY_ONCE"],"enumNames":["Allow once","Always allow this tool","Deny"]}}}}}},"requestState":` + elicitState + `}}`
}

func completeFor(id string) string {
	return `{"jsonrpc":"2.0","id":` + id + `,"result":{"resultType":"complete","content":[{"type":"text","text":"sent"}]}}`
}

var allowOnce = ElicitAnswer{Choices: map[string]map[string]string{"approval_choice": {"approval_choice": "ALLOW_ONCE"}}}

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

// reqID is the JSON-RPC id of the request the upstream is answering.
func reqID(r *http.Request) string {
	b, _ := io.ReadAll(r.Body)
	var m struct {
		ID json.RawMessage `json:"id"`
	}
	_ = json.Unmarshal(b, &m)
	return string(m.ID)
}

// jsonAnswer answers each request with the body chosen for its index and id.
func jsonAnswer(bodies func(n int, id string) string) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, r *http.Request, n int) {
		id := reqID(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, bodies(n, id))
	}
}

// askThenComplete is a server that asks once and then completes.
func askThenComplete(n int, id string) string {
	if n == 0 {
		return googleInputRequired(id)
	}
	return completeFor(id)
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

func lastMessage(t *testing.T, body string) map[string]any {
	t.Helper()
	msgs := sseMessages(t, body)
	if len(msgs) == 0 {
		t.Fatalf("no message in %q", body)
	}
	return msgs[len(msgs)-1]
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

func TestElicitRelayDeclaresFormOnlyOnToolCalls(t *testing.T) {
	resetElicitations(t)
	up := newUpstream(t, jsonAnswer(func(_ int, id string) string { return completeFor(id) }))
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	res := waitCall(t, e.startCall(elicitCallBody, sseHeaders))
	if res.resp.StatusCode != 200 || !strings.Contains(res.body, `"sent"`) {
		t.Fatalf("status %d body %q", res.resp.StatusCode, res.body)
	}
	caps := capsOf(t, up.at(0).Body)
	if !reflect.DeepEqual(caps["elicitation"], map[string]any{"form": map[string]any{}}) {
		t.Fatalf("elicitation capability %v, want form only", caps["elicitation"])
	}
	if _, ok := caps["roots"]; !ok {
		t.Fatalf("the CLI's other capabilities were lost: %v", caps)
	}
	if up.at(0).Header.Get("Accept-Encoding") != "identity" {
		t.Fatalf("Accept-Encoding %q: a compressed answer could not be read", up.at(0).Header.Get("Accept-Encoding"))
	}

	// prompts/get and resources/read can come back input_required too, but the relay does not
	// handle them, so it must not declare anything for them: their bytes pass as they are.
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":2,"method":"prompts/get","params":{"name":"p","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"resources/read","params":{"uri":"x:y","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`,
	} {
		n := up.count()
		readAll(t, e.do(http.MethodPost, relayPath(), strings.NewReader(body), sseHeaders))
		if got := up.at(n).Body; got != body {
			t.Fatalf("body changed: %q", got)
		}
	}
}

func TestElicitRelayLeavesOtherRequestsAsBytes(t *testing.T) {
	resetElicitations(t)
	up := newUpstream(t, jsonAnswer(func(_ int, id string) string { return googleInputRequired(id) }))
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

// A confirmation reaches the server only through a person: a request carrying its own
// requestState or inputResponses (anything holding the relay key could send one) is refused under
// every spelling a case-insensitive decoder would match, in any era and for any method.
func TestElicitRelayRefusesAContinuationTheClientWrote(t *testing.T) {
	resetElicitations(t)
	setElicitTimes(t, 200*time.Millisecond, time.Hour) // a call held by mistake ends quickly
	up := newUpstream(t, jsonAnswer(askThenComplete))
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	accept := `{"approval_choice":{"action":"accept","content":{"approval_choice":"ALLOW_ONCE"}}}`
	for _, field := range []string{`"requestState":"stolen"`, `"inputResponses":` + accept, `"RequestState":"stolen"`,
		`"inputresponses":` + accept, "\"reque\u017ftState\":\"stolen\"", "\"requeſtState\":\"stolen\""} {
		for _, body := range []string{
			strings.Replace(elicitCallBody, `"name":"send_mail",`, `"name":"send_mail",`+field+`,`, 1),
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send_mail",` + field + `}}`,
			`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"x:y",` + field + `}}`,
		} {
			resp := e.do(http.MethodPost, relayPath(), strings.NewReader(body), sseHeaders)
			got := readAll(t, resp)
			if resp.StatusCode != http.StatusBadRequest || !strings.Contains(got, "answers a confirmation itself") {
				t.Fatalf("%s: status %d body %q", body, resp.StatusCode, got)
			}
		}
	}
	if up.count() != 0 {
		t.Fatalf("%d requests reached the server", up.count())
	}
}

// JSON the server may read differently from the relay is refused, never guessed at.
func TestElicitRelayRefusesAmbiguousRequests(t *testing.T) {
	resetElicitations(t)
	up := newUpstream(t, jsonAnswer(askThenComplete))
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	meta := `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}`
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","Method":"tools/call","params":{"name":"x",` + meta + `}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","method":"tools/call","params":{"name":"x",` + meta + `}}`,
		`{"jsonrpc":"2.0","id":1,"Method":"tools/call","params":{"name":"x",` + meta + `}}`,
		`{"jsonrpc":"2.0","ID":1,"method":"tools/call","params":{"name":"x",` + meta + `}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"Name":"x",` + meta + `}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x","_META":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x","_meta":{"io.modelcontextprotocol/protocolversion":"2026-07-28"}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":[]}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x",` + meta + `}} trailing`,
		`[{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x",` + meta + `}}]`,
	} {
		resp := e.do(http.MethodPost, relayPath(), strings.NewReader(body), sseHeaders)
		got := readAll(t, resp)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d body %q", body, resp.StatusCode, got)
		}
	}
	if up.count() != 0 {
		t.Fatalf("%d requests reached the server", up.count())
	}
}

func TestElicitRelayRefusesACompressedRequest(t *testing.T) {
	resetElicitations(t)
	up := newUpstream(t, jsonAnswer(askThenComplete))
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	for _, codings := range [][]string{{"gzip"}, {"identity", "gzip"}, {"", "br"}, {"identity, gzip"}} {
		req, _ := http.NewRequest(http.MethodPost, e.base+relayPath(), strings.NewReader("\x1f\x8b..."))
		req.Header.Set(RelayKeyHeader, e.key)
		for _, c := range codings {
			req.Header.Add("Content-Encoding", c)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		readAll(t, resp)
		if resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Fatalf("%q: status %d", codings, resp.StatusCode)
		}
	}
	if up.count() != 0 {
		t.Fatalf("%d requests reached the server", up.count())
	}
}

func TestElicitRelayRefusesACompressedAnswer(t *testing.T) {
	for _, codings := range [][]string{{"gzip"}, {"identity", "gzip"}} {
		resetElicitations(t)
		up := newUpstream(t, func(w http.ResponseWriter, r *http.Request, n int) {
			w.Header().Set("Content-Type", "application/json")
			for _, c := range codings {
				w.Header().Add("Content-Encoding", c)
			}
			_, _ = io.WriteString(w, "\x1f\x8b compressed input_required")
		})
		e := newRelayEnv(t, up)
		flagElicitation(t, ElicitationRelay)
		res := waitCall(t, e.startCall(elicitCallBody, sseHeaders))
		if res.resp.StatusCode != http.StatusBadGateway || strings.Contains(res.body, "\x1f\x8b") {
			t.Fatalf("%q: status %d body %q", codings, res.resp.StatusCode, res.body)
		}
	}
}

func TestElicitRelayHoldsAndRetriesWithThePersonsChoice(t *testing.T) {
	resetElicitations(t)
	up := newUpstream(t, jsonAnswer(askThenComplete))
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

	if _, err := AnswerElicitation(p.ID, allowOnce); err != nil {
		t.Fatal(err)
	}
	res := waitCall(t, ch)
	last := lastMessage(t, res.body)
	if res.resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("CLI got %q (%s)", res.body, res.resp.Header.Get("Content-Type"))
	}
	if fmt.Sprint(last["id"]) != "7" || !strings.Contains(fmt.Sprint(last["result"]), "sent") {
		t.Fatalf("final message %v: the CLI must get its own id back", last)
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
	if string(retry["requestState"]) != elicitState {
		t.Fatalf("requestState %s", retry["requestState"])
	}
	var ir map[string]map[string]any
	if err := json.Unmarshal(retry["inputResponses"], &ir); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ir, map[string]map[string]any{"approval_choice": {"action": "accept", "content": map[string]any{"approval_choice": "ALLOW_ONCE"}}}) {
		t.Fatalf("inputResponses %v", ir)
	}
	// MRTR: the retry is an independent request with its own id.
	if string(retry["id"]) == "7" || !strings.HasPrefix(string(retry["id"]), `"af-relay-`) || string(retry["name"]) != `"send_mail"` {
		t.Fatalf("id %s name %s", retry["id"], retry["name"])
	}
	if caps := capsOf(t, up.at(1).Body); !reflect.DeepEqual(caps["elicitation"], map[string]any{"form": map[string]any{}}) {
		t.Fatalf("retry capabilities %v", caps)
	}
	if n := len(PendingElicitations()); n != 0 {
		t.Fatalf("%d still pending", n)
	}
}

func TestElicitRelayEchoesRequestStateOnlyWhenSent(t *testing.T) {
	for name, tc := range map[string]struct {
		state   string // the result's requestState member, "" for none
		inputs  bool
		pending bool
	}{
		"no requestState":         {state: "", inputs: true, pending: true},
		"empty string":            {state: `,"requestState":""`, inputs: true, pending: true},
		"escapes":                 {state: `,"requestState":"a\"b\\cé"`, inputs: true, pending: true},
		"requestState and no ask": {state: `,"requestState":"s1"`, inputs: false, pending: false},
	} {
		t.Run(name, func(t *testing.T) {
			resetElicitations(t)
			up := newUpstream(t, jsonAnswer(func(n int, id string) string {
				if n > 0 {
					return completeFor(id)
				}
				reqs := ""
				if tc.inputs {
					reqs = `"inputRequests":{"k":{"method":"elicitation/create","params":{"message":"m","requestedSchema":{"type":"object","properties":{"c":{"type":"string","enum":["ALLOW_ONCE"]}}}}}},`
				}
				return `{"jsonrpc":"2.0","id":` + id + `,"result":{` + reqs + `"resultType":"input_required"` + tc.state + `}}`
			}))
			e := newRelayEnv(t, up)
			flagElicitation(t, ElicitationRelay)
			ch := e.startCall(elicitCallBody, sseHeaders)
			if tc.pending {
				p := waitPending(t, 1)[0]
				if _, err := AnswerElicitation(p.ID, ElicitAnswer{Choices: map[string]map[string]string{"k": {}}}); err != nil {
					t.Fatal(err)
				}
			}
			res := waitCall(t, ch)
			if !strings.Contains(res.body, `"sent"`) || up.count() != 2 {
				t.Fatalf("CLI got %q after %d requests", res.body, up.count())
			}
			retry := paramsOf(t, up.at(1).Body)
			got, has := retry["requestState"]
			want := strings.TrimPrefix(tc.state, `,"requestState":`)
			switch {
			case tc.state == "" && has:
				t.Fatalf("a requestState the server never sent: %s", got)
			case tc.state != "" && string(got) != want:
				t.Fatalf("requestState %s, want %s byte for byte", got, want)
			}
			if _, has := retry["inputResponses"]; has != tc.inputs {
				t.Fatalf("inputResponses present %t", has)
			}
		})
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
			up := newUpstream(t, jsonAnswer(func(n int, id string) string {
				if n == 0 {
					return googleInputRequired(id)
				}
				return `{"jsonrpc":"2.0","id":` + id + `,"result":{"resultType":"complete","isError":true,"content":[{"type":"text","text":"Action canceled"}]}}`
			}))
			e := newRelayEnv(t, up)
			flagElicitation(t, ElicitationRelay)
			ch := e.startCall(elicitCallBody, sseHeaders)
			p := waitPending(t, 1)[0]
			if tc.answer {
				// A decline carries no choice, whatever the Console sent along.
				if _, err := AnswerElicitation(p.ID, ElicitAnswer{Decline: true, Choices: allowOnce.Choices}); err != nil {
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

func TestElicitAnswerAfterTheDeadlineOrTheCLIIsRefused(t *testing.T) {
	resetElicitations(t)
	ir := inputRequired{inputs: []ElicitInput{{Key: "a", Fields: []ElicitField{{Name: "c", Options: []ElicitOption{{"ALLOW_ONCE", "Allow"}}}}}}}
	ok := ElicitAnswer{Choices: map[string]map[string]string{"a": {"c": "ALLOW_ONCE"}}}

	setElicitTimes(t, 20*time.Millisecond, time.Hour)
	late, err := elicitations.add(context.Background(), ServerDef{ID: "s"}, elicitCall{params: map[string]json.RawMessage{}}, ir)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond) // past the deadline, before the relay's timer took it
	if _, err := AnswerElicitation(late.ID, ok); !errors.Is(err, ErrElicitationNotFound) {
		t.Fatalf("an answer after the deadline: %v", err)
	}

	setElicitTimes(t, time.Hour, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	left, _ := elicitations.add(ctx, ServerDef{ID: "s"}, elicitCall{params: map[string]json.RawMessage{}}, ir)
	cancel() // the CLI went away, before the relay removed the entry
	if _, err := AnswerElicitation(left.ID, ok); !errors.Is(err, ErrElicitationNotFound) {
		t.Fatalf("an answer after the CLI left: %v", err)
	}
}

func TestElicitRelayDropsTheQuestionWhenTheCLILeaves(t *testing.T) {
	resetElicitations(t)
	setElicitTimes(t, time.Hour, 20*time.Millisecond)
	up := newUpstream(t, jsonAnswer(func(_ int, id string) string { return googleInputRequired(id) }))
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
	if _, err := AnswerElicitation(p.ID, ElicitAnswer{Decline: true}); !errors.Is(err, ErrElicitationNotFound) {
		t.Fatalf("answer after the CLI left: %v", err)
	}
	if up.count() != 1 {
		t.Fatalf("the call was retried after the CLI left (%d)", up.count())
	}
}

// Claude Code 2.1.332 ends an HTTP tool call after 60 s unless a progress notification arrives
// (measured 2026-11-26). The held call sends them on the CLI's own token, and progress on that
// token only ever grows, across the server's own notifications of both attempts.
func TestElicitRelaySendsGrowingProgressWhileHeld(t *testing.T) {
	resetElicitations(t)
	setElicitTimes(t, time.Hour, 20*time.Millisecond)
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request, n int) {
		id := reqID(r)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":3,\"progress\":50,\"total\":100}}\n\n")
		if n == 0 {
			fmt.Fprintf(w, "data: %s\n\n", googleInputRequired(id))
			return
		}
		fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{\"progressToken\":3,\"progress\":1}}\n\n")
		fmt.Fprintf(w, "data: %s\n\n", completeFor(id))
	})
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	ch := e.startCall(elicitCallBody, sseHeaders)
	p := waitPending(t, 1)[0]
	time.Sleep(100 * time.Millisecond)
	if _, err := AnswerElicitation(p.ID, allowOnce); err != nil {
		t.Fatal(err)
	}
	msgs := sseMessages(t, waitCall(t, ch).body)
	last := -1.0
	relays := 0
	for _, m := range msgs[:len(msgs)-1] {
		params, _ := m["params"].(map[string]any)
		if m["method"] != "notifications/progress" || fmt.Sprint(params["progressToken"]) != "3" {
			t.Fatalf("unexpected message before the result: %v", m)
		}
		v, _ := params["progress"].(float64)
		if v <= last {
			t.Fatalf("progress went from %v to %v: %v", last, v, msgs)
		}
		last = v
		if strings.Contains(fmt.Sprint(params["message"]), "Agent Fleet") {
			relays++
		}
	}
	if relays < 2 {
		t.Fatalf("%d progress notifications from the relay", relays)
	}
	if fmt.Sprint(msgs[len(msgs)-1]["id"]) != "7" {
		t.Fatalf("last message %v", msgs[len(msgs)-1])
	}
}

func TestElicitRelaySendsCommentsWithoutAToken(t *testing.T) {
	resetElicitations(t)
	setElicitTimes(t, time.Hour, 20*time.Millisecond)
	up := newUpstream(t, jsonAnswer(askThenComplete))
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	ch := e.startCall(strings.Replace(elicitCallBody, `,"progressToken":3`, "", 1), sseHeaders)
	p := waitPending(t, 1)[0]
	time.Sleep(60 * time.Millisecond)
	if _, err := AnswerElicitation(p.ID, allowOnce); err != nil {
		t.Fatal(err)
	}
	res := waitCall(t, ch)
	if !strings.Contains(res.body, ": waiting for a person") || strings.Contains(res.body, "notifications/progress") {
		t.Fatalf("CLI got %q", res.body)
	}
}

func TestElicitRelayAnswersJSONToACLIWithoutStreams(t *testing.T) {
	resetElicitations(t)
	setElicitTimes(t, time.Hour, 20*time.Millisecond)
	up := newUpstream(t, jsonAnswer(askThenComplete))
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	ch := e.startCall(elicitCallBody, map[string]string{"Accept": "application/json", "Content-Type": "application/json"})
	p := waitPending(t, 1)[0]
	time.Sleep(60 * time.Millisecond) // keepalive ticks must not write into a JSON answer
	if _, err := AnswerElicitation(p.ID, allowOnce); err != nil {
		t.Fatal(err)
	}
	res := waitCall(t, ch)
	var m map[string]any
	if err := json.Unmarshal([]byte(res.body), &m); err != nil || res.resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("CLI got %q (%s)", res.body, res.resp.Header.Get("Content-Type"))
	}
	if fmt.Sprint(m["id"]) != "7" || !strings.Contains(res.body, `"sent"`) {
		t.Fatalf("CLI got %q", res.body)
	}
}

func TestElicitRelayPassesAStreamAndHoldsItsInputRequiredEvent(t *testing.T) {
	note := `{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info","data":"checking"}}`
	for name, frame := range map[string]func(n int, id string) string{
		"LF": func(n int, id string) string {
			if n == 0 {
				return "event: message\ndata: " + note + "\n\nevent: message\ndata: " + googleInputRequired(id) + "\n\n"
			}
			return "id: 9\nevent: message\ndata: " + completeFor(id) + "\n\n"
		},
		"CR, a BOM, data split over lines": func(n int, id string) string {
			if n == 0 {
				msg := googleInputRequired(id)
				half := strings.Index(msg, `,"requestState"`) // between two tokens: data lines join with a line feed
				return "\uFEFFdata: " + note + "\r\rdata: " + msg[:half] + "\rdata:" + msg[half:] + "\r\r"
			}
			return "id: 9\r\ndata: " + completeFor(id) + "\r\n\r\n"
		},
		"pretty JSON over data lines": func(n int, id string) string {
			if n == 0 {
				return "data: " + note + "\n\ndata: " + googleInputRequired(id) + "\n\n"
			}
			pretty := strings.ReplaceAll(completeFor(id), ",", ",\n  ")
			return "id: 9\ndata: " + strings.ReplaceAll(pretty, "\n", "\ndata: ") + "\n\n"
		},
	} {
		t.Run(name, func(t *testing.T) {
			resetElicitations(t)
			up := newUpstream(t, func(w http.ResponseWriter, r *http.Request, n int) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Set-Cookie", "s=1")
				_, _ = io.WriteString(w, frame(n, reqID(r)))
			})
			e := newRelayEnv(t, up)
			flagElicitation(t, ElicitationRelay)
			ch := e.startCall(elicitCallBody, sseHeaders)
			p := waitPending(t, 1)[0]
			if _, err := AnswerElicitation(p.ID, allowOnce); err != nil {
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
		})
	}
}

// An event the stream cut off before its blank line is not dispatched (WHATWG): it is neither held
// nor passed on, and the call ends with an error since its response never came.
func TestElicitRelayDoesNotHoldACutOffEvent(t *testing.T) {
	resetElicitations(t)
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request, n int) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+googleInputRequired(reqID(r))+"\n")
	})
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	res := waitCall(t, e.startCall(elicitCallBody, sseHeaders))
	if n := len(PendingElicitations()); n != 0 || up.count() != 1 || strings.Contains(res.body, "input_required") {
		t.Fatalf("pending %d, requests %d, CLI got %q", n, up.count(), res.body)
	}
	if !strings.Contains(fmt.Sprint(lastMessage(t, res.body)["error"]), "without the call's response") {
		t.Fatalf("CLI got %q", res.body)
	}
}

func TestElicitRelayBoundsAStreamEvent(t *testing.T) {
	resetElicitations(t)
	old := relayMaxBody
	relayMaxBody = 4 << 10
	t.Cleanup(func() { relayMaxBody = old })
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request, n int) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 1000; i++ { // short lines, never a blank one
			if _, err := io.WriteString(w, "data: x\n"); err != nil {
				return
			}
		}
	})
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	res := waitCall(t, e.startCall(elicitCallBody, sseHeaders))
	last := lastMessage(t, res.body)
	if last["error"] == nil || !strings.Contains(fmt.Sprint(last["error"]), "could not be read") {
		t.Fatalf("CLI got %v", last)
	}
}

func TestElicitRelayRefusesInputItCannotAskFor(t *testing.T) {
	cases := map[string]string{
		"free text":                     `{"type":"object","properties":{"note":{"type":"string"}}}`,
		"a number":                      `{"type":"object","properties":{"n":{"type":"number","enum":[1,2]}}}`,
		"a null":                        `{"type":"object","properties":{"c":{"type":"string","enum":["ALLOW_ONCE",null]}}}`,
		"no const":                      `{"type":"object","properties":{"c":{"type":"string","oneOf":[{"title":"Allow"}]}}}`,
		"enum and oneOf":                `{"type":"object","properties":{"c":{"type":"string","enum":["ALLOW_ONCE","ALLOW_TOOL_ALWAYS"],"oneOf":[{"const":"ALLOW_ONCE"}]}}}`,
		"an undescribed required field": `{"type":"object","required":["c","d"],"properties":{"c":{"type":"string","enum":["ALLOW_ONCE"]}}}`,
	}
	for name, schema := range cases {
		t.Run(name, func(t *testing.T) {
			resetElicitations(t)
			up := newUpstream(t, jsonAnswer(func(_ int, id string) string {
				return `{"jsonrpc":"2.0","id":` + id + `,"result":{"resultType":"input_required","inputRequests":{"k":{"method":"elicitation/create","params":{"mode":"form","message":"m","requestedSchema":` + schema + `}}},"requestState":"s"}}`
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
	t.Run("url mode, sampling, a requestState that is not a string, nothing at all", func(t *testing.T) {
		for _, res := range []string{
			`{"inputRequests":{"k":{"method":"elicitation/create","params":{"mode":"url","message":"m","url":"https://x.example"}}}}`,
			`{"inputRequests":{"k":{"method":"sampling/createMessage","params":{}}}}`,
			`{"requestState":{"a":1}}`,
			`{"resultType":"input_required"}`,
		} {
			if _, err := parseInputRequired(json.RawMessage(res)); err == nil {
				t.Errorf("accepted %s", res)
			}
		}
	})
}

func TestElicitRelayStopsAServerThatKeepsAsking(t *testing.T) {
	resetElicitations(t)
	up := newUpstream(t, jsonAnswer(func(_ int, id string) string { return googleInputRequired(id) }))
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	ch := e.startCall(elicitCallBody, sseHeaders)
	for i := 0; i < elicitMaxRounds; i++ {
		p := waitPending(t, 1)[0]
		if _, err := AnswerElicitation(p.ID, allowOnce); err != nil {
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

// The retry goes through the same renewal and the same filters as any relayed request, and on a
// stream already open its failures arrive as JSON-RPC errors under the CLI's id.
func TestElicitRelayRetryFailures(t *testing.T) {
	for _, tc := range []struct {
		name  string
		retry func(w http.ResponseWriter, r *http.Request, n int)
		check func(t *testing.T, e *relayEnv, res callResult)
	}{
		{"401 renews and retries once", func(w http.ResponseWriter, r *http.Request, n int) {
			if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") == "tok-initial" {
				w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, completeFor(reqID(r)))
		}, func(t *testing.T, e *relayEnv, res callResult) {
			if last := lastMessage(t, res.body); fmt.Sprint(last["id"]) != "7" || !strings.Contains(res.body, `"sent"`) {
				t.Fatalf("CLI got %q", res.body)
			}
			if e.as.tokenCount() != 1 {
				t.Fatalf("%d renewals", e.as.tokenCount())
			}
		}},
		{"403 records the scope and drops the challenge", func(w http.ResponseWriter, r *http.Request, n int) {
			w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="mail.send"`)
			w.WriteHeader(http.StatusForbidden)
		}, func(t *testing.T, e *relayEnv, res callResult) {
			if res.resp.Header.Get("WWW-Authenticate") != "" || lastMessage(t, res.body)["error"] == nil {
				t.Fatalf("CLI got %q %v", res.body, res.resp.Header)
			}
			if st, _ := OAuthStatusOf(relayTestID); st.StepUpScope != "mail.send" {
				t.Fatalf("status %+v", st)
			}
		}},
		{"a redirect is refused", func(w http.ResponseWriter, r *http.Request, n int) {
			w.Header().Set("Location", "https://elsewhere.example/")
			w.WriteHeader(http.StatusTemporaryRedirect)
		}, func(t *testing.T, e *relayEnv, res callResult) {
			if !strings.Contains(fmt.Sprint(lastMessage(t, res.body)["error"]), "redirect") || strings.Contains(res.body, "elsewhere") {
				t.Fatalf("CLI got %q", res.body)
			}
		}},
		{"a JSON-RPC error keeps its code", func(w http.ResponseWriter, r *http.Request, n int) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":`+reqID(r)+`,"error":{"code":-32602,"message":"requestState expired","data":{"x":1}}}`)
		}, func(t *testing.T, e *relayEnv, res callResult) {
			last := lastMessage(t, res.body)
			errObj, _ := last["error"].(map[string]any)
			if fmt.Sprint(last["id"]) != "7" || fmt.Sprint(errObj["code"]) != "-32602" || errObj["data"] == nil {
				t.Fatalf("CLI got %v", last)
			}
		}},
		{"an HTTP error without a message", func(w http.ResponseWriter, r *http.Request, n int) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusNotFound)
		}, func(t *testing.T, e *relayEnv, res callResult) {
			if !strings.Contains(fmt.Sprint(lastMessage(t, res.body)["error"]), "404") {
				t.Fatalf("CLI got %q", res.body)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetElicitations(t)
			up := newUpstream(t, func(w http.ResponseWriter, r *http.Request, n int) {
				if n == 0 {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, googleInputRequired(reqID(r)))
					return
				}
				tc.retry(w, r, n)
			})
			e := newRelayEnv(t, up)
			flagElicitation(t, ElicitationRelay)
			ch := e.startCall(elicitCallBody, sseHeaders)
			p := waitPending(t, 1)[0]
			if _, err := AnswerElicitation(p.ID, allowOnce); err != nil {
				t.Fatal(err)
			}
			tc.check(t, e, waitCall(t, ch))
		})
	}
}

func TestElicitRelayDoesNotRetryAServerSwitchedOffMeanwhile(t *testing.T) {
	resetElicitations(t)
	up := newUpstream(t, jsonAnswer(askThenComplete))
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	ch := e.startCall(elicitCallBody, sseHeaders)
	p := waitPending(t, 1)[0]
	if err := secrets.Update(func(s *secrets.Data) error {
		s.MCP[0].Enabled = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := AnswerElicitation(p.ID, allowOnce); err != nil {
		t.Fatal(err)
	}
	res := waitCall(t, ch)
	if !strings.Contains(fmt.Sprint(lastMessage(t, res.body)["error"]), "switched off") || up.count() != 1 {
		t.Fatalf("CLI got %q after %d requests", res.body, up.count())
	}
}

func TestElicitRelayCapsTheHeldCalls(t *testing.T) {
	resetElicitations(t)
	ir := inputRequired{inputs: []ElicitInput{{Key: "a"}}}
	for i := 0; i < elicitMaxPending; i++ {
		if _, err := elicitations.add(context.Background(), ServerDef{ID: "s"}, elicitCall{params: map[string]json.RawMessage{}}, ir); err != nil {
			t.Fatal(err)
		}
	}
	up := newUpstream(t, jsonAnswer(askThenComplete))
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	res := waitCall(t, e.startCall(elicitCallBody, sseHeaders))
	if !strings.Contains(res.body, "too many calls") || up.count() != 1 || len(PendingElicitations()) != elicitMaxPending {
		t.Fatalf("CLI got %q after %d requests", res.body, up.count())
	}
}

func TestAnswerElicitationChecksTheChoices(t *testing.T) {
	resetElicitations(t)
	ir, err := parseInputRequired(json.RawMessage(`{"resultType":"input_required","inputRequests":{` +
		`"a":{"method":"elicitation/create","params":{"message":"m","requestedSchema":{"type":"object","required":["c"],"properties":{"c":{"type":"string","enum":["ALLOW_ONCE","DENY_ONCE",""]},"o":{"type":"string","oneOf":[{"const":"x","title":"Ex"}]}}}}},` +
		`"b":{"method":"elicitation/create","params":{"message":"m","requestedSchema":{"type":"object","properties":{"opt":{"type":"string","enum":["y"]}}}}}` +
		`},"requestState":"s"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := ir.inputs[0].Fields[1]; got.Name != "o" || got.Required || !reflect.DeepEqual(got.Options, []ElicitOption{{"x", "Ex"}}) {
		t.Fatalf("oneOf field %+v", got)
	}
	p, err := elicitations.add(context.Background(), ServerDef{ID: "s", Name: "n"}, elicitCall{tool: "t", params: map[string]json.RawMessage{}}, ir)
	if err != nil {
		t.Fatal(err)
	}
	bad := []map[string]map[string]string{
		nil,                        // nothing answered
		{"a": {"c": "ALLOW_ONCE"}}, // the optional form b is not answered: it is not taken as an empty accept
		{"a": {}, "b": {}},         // a required field without a choice
		{"a": {"c": "ALLOW_EVERYTHING"}, "b": {}},
		{"a": {"c": "ALLOW_ONCE", "extra": "x"}, "b": {}},
		{"a": {"c": "ALLOW_ONCE"}, "b": {}, "z": {}},
		{"a": {"c": "ALLOW_ONCE"}, "b": nil}, // a null is not an empty accept
		{"a": {"c": "ALLOW_ONCE", "o": "y"}, "b": {}},
	}
	for _, c := range bad {
		if _, err := AnswerElicitation(p.ID, ElicitAnswer{Choices: c}); !errors.Is(err, ErrElicitationAnswer) {
			t.Errorf("%v: %v", c, err)
		}
	}
	// An empty string the server offered is a real choice.
	if _, err := AnswerElicitation(p.ID, ElicitAnswer{Choices: map[string]map[string]string{"a": {"c": ""}, "b": {}}}); err != nil {
		t.Fatal(err)
	}
	if got := <-p.answer; !reflect.DeepEqual(got.Choices, map[string]map[string]string{"a": {"c": ""}, "b": {}}) {
		t.Fatalf("delivered %+v", got)
	}
	if _, err := AnswerElicitation(p.ID, ElicitAnswer{Decline: true}); !errors.Is(err, ErrElicitationNotFound) {
		t.Fatalf("second answer: %v", err)
	}
}

// At the time limit the relay declines only a call still on the list: an answer that got there
// first is the one sent.
func TestElicitTimeLimitYieldsToAnAnswerThatWon(t *testing.T) {
	resetElicitations(t)
	ir := inputRequired{inputs: []ElicitInput{{Key: "a", Fields: []ElicitField{{Name: "c", Required: true, Options: []ElicitOption{{"ALLOW_ONCE", "Allow"}}}}}}}
	p, err := elicitations.add(context.Background(), ServerDef{ID: "s"}, elicitCall{params: map[string]json.RawMessage{}}, ir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AnswerElicitation(p.ID, ElicitAnswer{Choices: map[string]map[string]string{"a": {"c": "ALLOW_ONCE"}}}); err != nil {
		t.Fatal(err)
	}
	if elicitations.take(p.ID) {
		t.Fatal("an answered call was still taken as unanswered")
	}
	if got := <-p.answer; got.Decline {
		t.Fatalf("delivered %+v", got)
	}
	q, _ := elicitations.add(context.Background(), ServerDef{ID: "s"}, elicitCall{params: map[string]json.RawMessage{}}, ir)
	if !elicitations.take(q.ID) {
		t.Fatal("an unanswered call was not taken")
	}
}

func TestPendingElicitationsAreCopies(t *testing.T) {
	resetElicitations(t)
	ir := inputRequired{inputs: []ElicitInput{{Key: "a", Fields: []ElicitField{{Name: "c", Options: []ElicitOption{{"ALLOW_ONCE", "Allow"}}}}}}}
	_, _ = elicitations.add(context.Background(), ServerDef{ID: "s"}, elicitCall{params: map[string]json.RawMessage{"arguments": json.RawMessage(`{"a":1}`)}}, ir)
	got := PendingElicitations()[0]
	got.Arguments[2] = 'X'
	got.Inputs[0].Fields[0].Options[0].Value = "ALLOW_EVERYTHING"
	again := PendingElicitations()[0]
	if string(again.Arguments) != `{"a":1}` || again.Inputs[0].Fields[0].Options[0].Value != "ALLOW_ONCE" {
		t.Fatalf("a caller's change reached the held call: %s %+v", again.Arguments, again.Inputs)
	}
}

func TestSameJSONID(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{`7`, `7`, true},
		{`"7"`, `7`, false},
		{`"a"`, `"a"`, true},
		{`9007199254740993`, `9007199254740992`, false}, // equal as float64
		{`null`, `null`, false},
		{`-0`, `0`, true},
		{`1.0`, `1`, true},
	} {
		if got := sameJSONID(json.RawMessage(tc.a), json.RawMessage(tc.b)); got != tc.want {
			t.Errorf("%s vs %s: %t", tc.a, tc.b, got)
		}
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

// On a held call the CLI gets nothing but notifications and the call's own response: a server
// that answers the retry under the CLI's original id, or with a result on a non-200, ends the
// call with an error instead of handing the CLI an input_required meant for no one.
func TestElicitRelayRefusesMessagesForAnotherRequest(t *testing.T) {
	for name, retry := range map[string]func(w http.ResponseWriter, r *http.Request){
		"stream, the old id": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\n", googleInputRequired("7"))
		},
		"json, the old id": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, googleInputRequired("7"))
		},
		"json, a result on a 500": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, googleInputRequired(reqID(r)))
		},
		"stream, a server request": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"id\":99,\"method\":\"elicitation/create\",\"params\":{}}\n\n")
		},
		"stream, not JSON": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"id\":7,\n\n")
		},
	} {
		t.Run(name, func(t *testing.T) {
			resetElicitations(t)
			up := newUpstream(t, func(w http.ResponseWriter, r *http.Request, n int) {
				if n == 0 {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, googleInputRequired(reqID(r)))
					return
				}
				retry(w, r)
			})
			e := newRelayEnv(t, up)
			flagElicitation(t, ElicitationRelay)
			ch := e.startCall(elicitCallBody, sseHeaders)
			p := waitPending(t, 1)[0]
			if _, err := AnswerElicitation(p.ID, allowOnce); err != nil {
				t.Fatal(err)
			}
			res := waitCall(t, ch)
			if strings.Contains(res.body, "input_required") || strings.Contains(res.body, "elicitation/create") {
				t.Fatalf("CLI got %q", res.body)
			}
			if last := lastMessage(t, res.body); last["error"] == nil || fmt.Sprint(last["id"]) != "7" {
				t.Fatalf("CLI got %v", last)
			}
		})
	}
}

// A server's SSE comment reaches the CLI as it arrives, before the call's response: an idle
// connection between them must not be cut.
func TestElicitRelayPassesCommentsAtOnce(t *testing.T) {
	resetElicitations(t)
	release := make(chan struct{})
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request, n int) {
		id := reqID(r)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": ping\n")
		http.NewResponseController(w).Flush()
		<-release
		fmt.Fprintf(w, "data: %s\n\n", completeFor(id))
	})
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	resp := e.do(http.MethodPost, relayPath(), strings.NewReader(elicitCallBody), sseHeaders)
	defer resp.Body.Close()
	rd := bufio.NewReader(resp.Body)
	got := make(chan string, 1)
	go func() {
		line, _ := rd.ReadString('\n')
		got <- line
	}()
	select {
	case line := <-got:
		if line != ": ping\n" {
			t.Fatalf("first line %q", line)
		}
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the comment was held back")
	}
	close(release)
	rest, _ := io.ReadAll(rd)
	if !strings.Contains(string(rest), `"sent"`) {
		t.Fatalf("CLI got %q", rest)
	}
}

// A call confirmed for one sign-in is never retried on another: signing in again at the same URL
// (perhaps as another account) while the call waits ends it.
func TestElicitRelayDoesNotRetryOnAnotherSignIn(t *testing.T) {
	resetElicitations(t)
	up := newUpstream(t, jsonAnswer(askThenComplete))
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	ch := e.startCall(elicitCallBody, sseHeaders)
	p := waitPending(t, 1)[0]
	if err := secrets.Update(func(s *secrets.Data) error {
		s.MCPOAuth[0].ConnectedAt += 60
		s.MCPOAuth[0].AccessToken = "tok-other-account"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := AnswerElicitation(p.ID, allowOnce); err != nil {
		t.Fatal(err)
	}
	res := waitCall(t, ch)
	if up.count() != 1 || lastMessage(t, res.body)["error"] == nil {
		t.Fatalf("CLI got %q after %d requests", res.body, up.count())
	}
}

func TestElicitRelayRefusesANullRequestState(t *testing.T) {
	resetElicitations(t)
	up := newUpstream(t, jsonAnswer(func(_ int, id string) string {
		return strings.Replace(googleInputRequired(id), `"requestState":`+elicitState, `"requestState":null`, 1)
	}))
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	res := waitCall(t, e.startCall(elicitCallBody, sseHeaders))
	if !strings.Contains(res.body, "not a string") || up.count() != 1 || len(PendingElicitations()) != 0 {
		t.Fatalf("CLI got %q after %d requests", res.body, up.count())
	}
}

func TestElicitRelayDropsDigestsOfARewrittenBody(t *testing.T) {
	resetElicitations(t)
	up := newUpstream(t, jsonAnswer(func(_ int, id string) string { return completeFor(id) }))
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	hdr := map[string]string{"Content-Digest": "sha-256=:AAAA:", "Accept": "application/json, text/event-stream"}
	waitCall(t, e.startCall(elicitCallBody, hdr))
	if up.at(0).Header.Get("Content-Digest") != "" {
		t.Fatal("the digest of the CLI's body went out with a rewritten one")
	}
}

func TestNextProgressAlwaysGrows(t *testing.T) {
	for _, last := range []float64{0, 1, 1 << 53, 1e300} {
		v, ok := nextProgress(last)
		if !ok || v <= last {
			t.Errorf("after %v: %v %t", last, v, ok)
		}
	}
	if _, ok := nextProgress(math.MaxFloat64); ok {
		t.Error("a value past the largest float64 was offered")
	}
}

func TestAcceptsEventStream(t *testing.T) {
	for _, tc := range []struct {
		accept string
		want   bool
	}{
		{"application/json, text/event-stream", true},
		{"text/event-stream;q=0.5", true},
		{"application/json, text/event-stream;q=0", false},
		{"application/json", false},
	} {
		if got := acceptsEventStream([]string{tc.accept}); got != tc.want {
			t.Errorf("%q: %t", tc.accept, got)
		}
	}
}

// Only exactly one JSON object is read for a flagged server; anything else is refused, since a
// lenient decoder on the server (one that skips a byte-order mark) could still read a call in it.
func TestElicitRelayRefusesWhatIsNotOneJSONObject(t *testing.T) {
	resetElicitations(t)
	up := newUpstream(t, jsonAnswer(askThenComplete))
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	cont := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send_mail","requestState":"s","inputResponses":{}}}`
	for _, body := range []string{"\uFEFF" + cont, "\uFEFF[" + cont + "]", "not json", ""} {
		resp := e.do(http.MethodPost, relayPath(), strings.NewReader(body), sseHeaders)
		readAll(t, resp)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%q: status %d", body, resp.StatusCode)
		}
	}
	if up.count() != 0 {
		t.Fatalf("%d requests reached the server", up.count())
	}
}

// An answer whose control members fold together ("result" and "Result") may be read one way by
// the relay and the other by the CLI: it ends the call, and the CLI sees no confirmation.
func TestElicitRelayRefusesAmbiguousAnswers(t *testing.T) {
	ir := `{"resultType":"input_required","inputRequests":{"k":{"method":"elicitation/create","params":{"message":"m","requestedSchema":{"type":"object","properties":{"c":{"type":"string","enum":["ALLOW_ONCE"]}}}}}},"requestState":"s"}`
	done := `{"resultType":"complete","content":[]}`
	for name, tc := range map[string]struct{ ct, body string }{
		"json, result and Result":   {"application/json", `{"jsonrpc":"2.0","id":7,"result":` + ir + `,"Result":` + done + `}`},
		"json, resultType twice":    {"application/json", `{"jsonrpc":"2.0","id":7,"result":{"resultType":"input_required","ResultType":"complete"}}`},
		"json, a folded resultType": {"application/json", `{"jsonrpc":"2.0","id":7,"result":{"resultType":"complete","ResultType":"input_required","content":[]}}`},
		"stream, result and Result": {"text/event-stream",
			"data: " + `{"jsonrpc":"2.0","id":7,"result":` + ir + `,"Result":` + done + `}` + "\n\n"},
	} {
		t.Run(name, func(t *testing.T) {
			resetElicitations(t)
			up := newUpstream(t, func(w http.ResponseWriter, r *http.Request, n int) {
				w.Header().Set("Content-Type", tc.ct)
				_, _ = io.WriteString(w, tc.body)
			})
			e := newRelayEnv(t, up)
			flagElicitation(t, ElicitationRelay)
			res := waitCall(t, e.startCall(elicitCallBody, sseHeaders))
			if strings.Contains(res.body, "input_required") || len(PendingElicitations()) != 0 || up.count() != 1 {
				t.Fatalf("CLI got %q", res.body)
			}
		})
	}
}

// A first answer that is compressed, or an event stream on an error status, may carry the call's
// result, so neither is passed through as a plain HTTP error is.
func TestElicitRelayDoesNotPassMessagesOnAnErrorStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		ct, ce string
		body   string
		status int // what the CLI must get
	}{
		"compressed":   {"text/plain", "gzip", "\x1f\x8b", http.StatusBadGateway},
		"event stream": {"text/event-stream", "", "data: " + googleInputRequired("7") + "\n\n", http.StatusBadGateway},
		"plain text":   {"text/plain", "", "server error", http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			resetElicitations(t)
			up := newUpstream(t, func(w http.ResponseWriter, r *http.Request, n int) {
				w.Header().Set("Content-Type", tc.ct)
				if tc.ce != "" {
					w.Header().Set("Content-Encoding", tc.ce)
				}
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, tc.body)
			})
			e := newRelayEnv(t, up)
			flagElicitation(t, ElicitationRelay)
			res := waitCall(t, e.startCall(elicitCallBody, sseHeaders))
			if res.resp.StatusCode != tc.status || strings.Contains(res.body, "input_required") || strings.Contains(res.body, "\x1f\x8b") {
				t.Fatalf("status %d body %q", res.resp.StatusCode, res.body)
			}
		})
	}
}

// Two sign-ins in the same second are told apart: the retry is not sent on the second, whether
// it is found before the retry or by the renewal after the server refused the first's token.
func TestElicitRelayDoesNotRetryOnASignInOfTheSameSecond(t *testing.T) {
	signInAgain := func(t *testing.T) {
		t.Helper()
		if err := secrets.Update(func(s *secrets.Data) error {
			s.MCPOAuth[0].SignInID = "another-sign-in"
			s.MCPOAuth[0].AccessToken = "tok-other-account"
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("before the retry", func(t *testing.T) {
		resetElicitations(t)
		up := newUpstream(t, jsonAnswer(askThenComplete))
		e := newRelayEnv(t, up)
		flagElicitation(t, ElicitationRelay)
		ch := e.startCall(elicitCallBody, sseHeaders)
		p := waitPending(t, 1)[0]
		signInAgain(t)
		if _, err := AnswerElicitation(p.ID, allowOnce); err != nil {
			t.Fatal(err)
		}
		if res := waitCall(t, ch); up.count() != 1 || lastMessage(t, res.body)["error"] == nil {
			t.Fatalf("CLI got %q after %d requests", res.body, up.count())
		}
	})
	t.Run("at the renewal", func(t *testing.T) {
		resetElicitations(t)
		up := newUpstream(t, func(w http.ResponseWriter, r *http.Request, n int) {
			if n == 0 {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, googleInputRequired(reqID(r)))
				return
			}
			signInAgain(t) // the sign-in lands while the retry is out
			w.WriteHeader(http.StatusUnauthorized)
		})
		e := newRelayEnv(t, up)
		flagElicitation(t, ElicitationRelay)
		ch := e.startCall(elicitCallBody, sseHeaders)
		p := waitPending(t, 1)[0]
		if _, err := AnswerElicitation(p.ID, allowOnce); err != nil {
			t.Fatal(err)
		}
		if res := waitCall(t, ch); up.count() != 2 || lastMessage(t, res.body)["error"] == nil {
			t.Fatalf("CLI got %q after %d requests", res.body, up.count())
		}
	})
}

func TestElicitRelayRefusesConstraintsItDoesNotCheck(t *testing.T) {
	for name, field := range map[string]string{
		"enum and const":  `{"type":"string","enum":["ALLOW_ONCE","ALLOW_TOOL_ALWAYS"],"const":"ALLOW_ONCE"}`,
		"a pattern":       `{"type":"string","enum":["ALLOW_ONCE","ALLOW_TOOL_ALWAYS"],"pattern":"^ALLOW_ONCE$"}`,
		"a folded member": `{"type":"string","enum":["ALLOW_ONCE"],"Enum":["DENY_ONCE"]}`,
		"an option's own": `{"type":"string","oneOf":[{"const":"ALLOW_ONCE","deprecated":true}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			res := `{"inputRequests":{"k":{"method":"elicitation/create","params":{"message":"m","requestedSchema":{"type":"object","properties":{"c":` + field + `}}}}}}`
			if _, err := parseInputRequired(json.RawMessage(res)); err == nil {
				t.Fatalf("accepted %s", field)
			}
		})
	}
	if _, err := parseInputRequired(json.RawMessage(`{"inputRequests":{"k":{"method":"elicitation/create","params":{"message":"m","requestedSchema":{"type":"object","properties":{"c":{"type":"string","enum":["A"]}},"additionalProperties":true}}}}}`)); err == nil {
		t.Fatal("accepted a schema member the relay does not check")
	}
}

// One request or answer must not buy unbounded work: many member names are checked in linear
// time, and a numeric id is never expanded, however large its exponent.
func TestElicitRelayParsingStaysCheap(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{`)
	for i := 0; i < 50000; i++ { // pairwise, this many names take minutes
		fmt.Fprintf(&b, `"k%d":0,`, i)
	}
	b.WriteString(`"z":0}}`)
	start := time.Now()
	if _, v, _ := readFlagged([]byte(b.String())); v != flaggedPass {
		t.Fatalf("verdict %v", v)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("50000 names took %v", d)
	}
	start = time.Now()
	if sameJSONID(json.RawMessage(`1e999999999999`), json.RawMessage(`1`)) || sameJSONID(json.RawMessage(strings.Repeat("9", 1<<20)), json.RawMessage(`7`)) {
		t.Fatal("different ids compared equal")
	}
	if !sameJSONID(json.RawMessage(`1e3`), json.RawMessage(`1000`)) || !sameJSONID(json.RawMessage(`10.50`), json.RawMessage(`1.05e1`)) {
		t.Fatal("equal numbers compared different")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("comparing ids took %v", d)
	}
}

// A CLI that takes no event stream gets the call's response alone, as JSON, even when the server
// streams it.
func TestElicitRelayAnswersJSONWhenTheServerStreams(t *testing.T) {
	resetElicitations(t)
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request, n int) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": ping\n\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\",\"params\":{}}\n\n")
		fmt.Fprintf(w, "data: %s\n\n", completeFor(reqID(r)))
	})
	e := newRelayEnv(t, up)
	flagElicitation(t, ElicitationRelay)
	res := waitCall(t, e.startCall(elicitCallBody, map[string]string{"Accept": "application/json, text/event-stream;q=0"}))
	var m map[string]any
	if err := json.Unmarshal([]byte(res.body), &m); err != nil || res.resp.Header.Get("Content-Type") != "application/json" || fmt.Sprint(m["id"]) != "7" {
		t.Fatalf("CLI got %q (%s)", res.body, res.resp.Header.Get("Content-Type"))
	}
}

func TestElicitRelayRefusesFormsThatMisleadThePerson(t *testing.T) {
	form := func(params string) string {
		return `{"inputRequests":{"k":{"method":"elicitation/create","params":` + params + `}}}`
	}
	schema := `"requestedSchema":{"type":"object","properties":{"c":{"type":"string","enum":["ALLOW_ONCE","ALLOW_TOOL_ALWAYS","DENY_ONCE"]}}}`
	for name, res := range map[string]string{
		"no message":          form(`{` + schema + `}`),
		"a folded message":    form(`{"Message":"Send mail",` + schema + `}`),
		"a folded schema":     form(`{"message":"m","RequestedSchema":{"type":"object","properties":{"c":{"type":"string","enum":["A"]}}}}`),
		"a label missing":     form(`{"message":"m","requestedSchema":{"type":"object","properties":{"c":{"type":"string","enum":["ALLOW_ONCE","ALLOW_TOOL_ALWAYS","DENY_ONCE"],"enumNames":["Allow once","Deny"]}}}}`),
		"a label not text":    form(`{"message":"m","requestedSchema":{"type":"object","properties":{"c":{"type":"string","enum":["ALLOW_ONCE","DENY_ONCE"],"enumNames":["Allow once",null]}}}}`),
		"a const twice":       form(`{"message":"m","requestedSchema":{"type":"object","properties":{"c":{"type":"string","oneOf":[{"const":"ALLOW_ONCE","title":"Deny"},{"const":"ALLOW_ONCE","title":"Allow"},{"const":"DENY_ONCE"}]}}}}`),
		"an enum value twice": form(`{"message":"m","requestedSchema":{"type":"object","properties":{"c":{"type":"string","enum":["ALLOW_ONCE","ALLOW_ONCE"]}}}}`),
		"another dialect":     form(`{"message":"m","requestedSchema":{"$schema":"http://json-schema.org/draft-04/schema#","type":"object","properties":{"c":{"type":"string","enum":["A"]}}}}`),
	} {
		if _, err := parseInputRequired(json.RawMessage(res)); err == nil {
			t.Errorf("%s: accepted %s", name, res)
		}
	}
	// The canonical form, with mode left out and the default dialect named, is accepted.
	ok := form(`{"message":"Send **mail**","requestedSchema":{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"c":{"type":"string","enum":["ALLOW_ONCE","DENY_ONCE"],"enumNames":["Allow once","Deny"]}}}}`)
	ir, err := parseInputRequired(json.RawMessage(ok))
	if err != nil || ir.inputs[0].Message != "Send **mail**" || ir.inputs[0].Fields[0].Options[1].Label != "Deny" {
		t.Fatalf("%+v %v", ir, err)
	}
}

// The response goes back under the id the CLI sent, as it was written, even when the server wrote
// the same number another way.
func TestElicitRelayAnswersUnderTheCLIsOwnSpelling(t *testing.T) {
	for name, ct := range map[string]string{"json": "application/json", "stream": "text/event-stream"} {
		t.Run(name, func(t *testing.T) {
			resetElicitations(t)
			up := newUpstream(t, func(w http.ResponseWriter, r *http.Request, n int) {
				w.Header().Set("Content-Type", ct)
				body := completeFor("7.0")
				if ct == "text/event-stream" {
					body = "data: " + body + "\n\n"
				}
				_, _ = io.WriteString(w, body)
			})
			e := newRelayEnv(t, up)
			flagElicitation(t, ElicitationRelay)
			res := waitCall(t, e.startCall(elicitCallBody, sseHeaders))
			if !strings.Contains(res.body, `"id":7,`) || strings.Contains(res.body, "7.0") {
				t.Fatalf("CLI got %q", res.body)
			}
		})
	}
}

func TestIDKeyBoundsTheExponent(t *testing.T) {
	if sameJSONID(json.RawMessage(`1e1099511627777`), json.RawMessage(`1e1099511627777`)) {
		t.Error("an exponent past the bound was compared")
	}
	if !sameJSONID(json.RawMessage(`1e1099511627775`), json.RawMessage(`10e1099511627774`)) {
		t.Error("equal numbers at the bound compared different")
	}
}

// Each completed sign-in gets its own SignInID, also within one second, and a renewal keeps it:
// that is what lets the relay refuse to retry a confirmed call on another sign-in.
func TestCompleteGivesEachSignInItsOwnID(t *testing.T) {
	resetOAuthState(t)
	f := newASFake(t, nil)
	now := time.Now()
	oauthNow = func() time.Time { return now } // both sign-ins in the same second
	id := signedIn(t, f)
	first := storedGrant(t, id).SignInID
	req := startReq(nil)
	mustStart(t, id, req)
	if _, err := CompleteOAuth(context.Background(), id, CompleteRequest{State: req.State, Code: "c2", Iss: f.url("")}); err != nil {
		t.Fatal(err)
	}
	second := storedGrant(t, id)
	if first == "" || second.SignInID == "" || first == second.SignInID {
		t.Fatalf("sign-in ids %q and %q", first, second.SignInID)
	}
	oauthNow = func() time.Time { return now.Add(2 * time.Hour) }
	if _, err := AccessToken(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if g := storedGrant(t, id); g.SignInID != second.SignInID || g.RefreshedAt == 0 {
		t.Fatalf("after renewal: %+v", g)
	}
}
