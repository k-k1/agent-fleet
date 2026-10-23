//go:build contract

// The v2 session-state readers (v2state.go) and the turn path (serveapi.go sendTurn) against the
// REAL opencode 2 binary on PATH, with a loopback stub OpenAI-compatible provider standing in
// for the model (no credentials, no billing). Skipped on a v1 binary.
//
//	PATH=<dir with opencode 2>:$PATH go test -tags contract -run V2State ./internal/agents/opencode/
//
// Like contract_serveapi_test.go it is not run in CI: opencode-contract.yml pins opencode-ai only.
package opencode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const stubSummary = "## Objective\n- o\n\n## Requirements\n- (none)\n\n## Decisions\n- (none)\n\n## Work State\n### Completed\n- (none)\n\n### Active\n- (none)\n\n### Blocked\n- (none)\n\n## Next Move\n1. (none)\n\n## Relevant Files\n- (none)\n\n## Important Context\n- (none)"

// stubProvider answers by the last user text: FAIL → 401, SLOW → a stream that takes ~8 s,
// TOOL → one shell call first, a compaction request → the template filled in, anything else →
// "ok". It records every request, so the test can say the stub saw them.
type stubProvider struct {
	mu   sync.Mutex
	seen int
}

func (s *stubProvider) handler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	s.seen++
	s.mu.Unlock()
	text, afterTool := "", false
	for _, m := range body.Messages {
		if m.Role == "user" {
			text = fmt.Sprint(m.Content)
		}
		afterTool = m.Role == "tool"
	}
	if strings.Contains(text, "FAIL") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"stub: invalid api key","type":"invalid_request_error"}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(delta map[string]any, fin any) {
		b, _ := json.Marshal(map[string]any{"id": "c", "object": "chat.completion.chunk", "created": 0, "model": "m",
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": fin}}})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	usage := func() {
		b, _ := json.Marshal(map[string]any{"id": "c", "object": "chat.completion.chunk", "created": 0, "model": "m",
			"choices": []any{}, "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120}})
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", b)
	}
	switch {
	case strings.Contains(text, "You MUST summarize"):
		send(map[string]any{"role": "assistant", "content": stubSummary}, nil)
		send(map[string]any{}, "stop")
	case strings.Contains(text, "ASK") && !afterTool:
		// The question tool: ASKMULTI makes two questions, the second one multiple-choice.
		qs := []any{map[string]any{"question": "Pick one", "header": "Pick", "options": []any{
			map[string]any{"label": "A", "description": "a"}, map[string]any{"label": "B", "description": "b"}}}}
		if strings.Contains(text, "ASKMULTI") {
			qs = append(qs, map[string]any{"question": "Which toppings?", "header": "Toppings", "multiple": true, "options": []any{
				map[string]any{"label": "Cheese", "description": "c"}, map[string]any{"label": "Ham", "description": "h"}}})
		}
		args, _ := json.Marshal(map[string]any{"questions": qs})
		send(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "call_q", "type": "function",
			"function": map[string]any{"name": "question", "arguments": string(args)}}}}, nil)
		send(map[string]any{}, "tool_calls")
	case strings.Contains(text, "TODO") && !afterTool:
		args, _ := json.Marshal(map[string]any{"todos": []any{
			map[string]any{"content": "write the probe", "status": "completed", "priority": "high"},
			map[string]any{"content": "read the result", "status": "in_progress", "priority": "medium"},
			map[string]any{"content": "report", "status": "pending", "priority": "low"}}})
		send(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "call_t", "type": "function",
			"function": map[string]any{"name": "todowrite", "arguments": string(args)}}}}, nil)
		send(map[string]any{}, "tool_calls")
	case strings.Contains(text, "TOOL") && !afterTool:
		send(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "call_1", "type": "function",
			"function": map[string]any{"name": "shell", "arguments": `{"command":"echo hi","description":"say hi"}`}}}}, nil)
		send(map[string]any{}, "tool_calls")
	default:
		send(map[string]any{"role": "assistant", "content": "ok "}, nil)
		if strings.Contains(text, "SLOW") {
			for i := 0; i < 8; i++ {
				time.Sleep(time.Second)
				send(map[string]any{"content": "."}, nil)
			}
		}
		send(map[string]any{"content": "done"}, nil)
		send(map[string]any{}, "stop")
	}
	usage()
}

// startStubbedV2 runs a real opencode 2 serve on a free loopback port in an isolated HOME whose only
// provider is the stub, and returns its address and a warmed-up project directory. It skips the test
// on a v1 binary.
func startStubbedV2(t *testing.T) (stub *stubProvider, addr, dir string) {
	t.Helper()
	return startStubbedV2With(t, nil)
}

// startStubbedV2With is startStubbedV2 with extra top-level keys in the opencode config.
func startStubbedV2With(t *testing.T, extra map[string]any) (stub *stubProvider, addr, dir string) {
	t.Helper()
	requireOpencode(t)
	if !strings.HasPrefix(strings.TrimPrefix(opencodeVersion(t), "opencode v"), "2.") {
		skipUnlessV2Required(t, "needs opencode 2")
	}
	stub = &stubProvider{}
	llm := httptest.NewServer(http.HandlerFunc(stub.handler))
	t.Cleanup(func() { llm.CloseClientConnections(); llm.Close() })

	home := t.TempDir()
	cfgDir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgMap := map[string]any{"provider": map[string]any{"stub": map[string]any{
		"npm": "@ai-sdk/openai-compatible", "name": "stub",
		"options": map[string]any{"baseURL": llm.URL + "/v1", "apiKey": "x"},
		"models": map[string]any{"m": map[string]any{"name": "m", "tool_call": true,
			"cost": map[string]any{"input": 1, "output": 2}, "limit": map[string]any{"context": 8000, "output": 1000}}},
	}}, "permission": map[string]any{"*": "allow"}}
	for k, v := range extra {
		cfgMap[k] = v
	}
	cfg, _ := json.Marshal(cfgMap)
	if err := os.WriteFile(filepath.Join(cfgDir, "opencode.json"), cfg, 0o600); err != nil {
		t.Fatal(err)
	}

	port := freePort(t)
	addr = "http://127.0.0.1:" + port
	t.Setenv(serveAddrEnv, addr)
	t.Setenv("HOME", home)
	password, err := newServePassword()
	if err != nil {
		t.Fatal(err)
	}
	detectedMajor.Store(int32(apiV1))
	t.Cleanup(func() { detectedMajor.Store(int32(apiV1)); resetServeAuth("") })
	cmd := exec.Command("opencode", "serve", "--hostname", "127.0.0.1", "--port", port)
	cmd.Env = append(envWithHome(home), "OPENCODE_SERVER_USERNAME="+serveUser, "OPENCODE_SERVER_PASSWORD="+password)
	cmd.Dir = home
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	for deadline := time.Now().Add(60 * time.Second); !healthy(addr); time.Sleep(300 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("serve never became healthy")
		}
	}
	if !isV2() {
		t.Fatal("probe did not detect v2")
	}
	// Warm the instance up: the first request for a directory boots plugins, which outlasts serveClient.
	dir = t.TempDir()
	if res, err := (&http.Client{Timeout: 60 * time.Second, Transport: authTransport{}}).Get(addr + "/api/session?directory=" + dir); err == nil {
		res.Body.Close()
	}
	return stub, addr, dir
}

func TestContractV2StateReadsTurnsThroughTheAPI(t *testing.T) {
	stub, addr, dir := startStubbedV2(t)

	ses, err := serveCreateSession(addr, dir, "contract")
	if err != nil {
		t.Fatal(err)
	}
	m := session.Meta{Name: "v2contract", Dir: dir, Kind: session.KindOpencode, Driver: session.DriverManaged}
	sids.Write(session.UUID(m.Dir, m.Name), ses)
	t.Cleanup(func() { sids.Remove(session.UUID(m.Dir, m.Name)) })
	st := agents.ThreadSettings{Model: "stub/m"}

	turn := func(prompt string) (messageError, bool) {
		t.Helper()
		req, err := newTurnRequest(addr, ses, dir, agents.TurnInput{Prompt: prompt}, st)
		if err != nil {
			t.Fatal(err)
		}
		res, err := sendTurn(addr, ses, dir, req, st)
		if err != nil {
			t.Fatalf("turn %q: %v", prompt, err)
		}
		defer res.Body.Close()
		return decodeTurnError(res.Body)
	}

	if LiveState(m) != "idle" {
		t.Fatalf("fresh session reads %q", LiveState(m))
	}
	// Success with a tool call: two steps, one turn in the mirror, cost on the turn.
	if e, has := turn("hello TOOL"); has {
		t.Fatalf("successful turn reported %+v", e)
	}
	td, _ := readTranscript(m)
	if len(td.Turns) != 2 || td.Turns[1].CostUSD <= 0 || !strings.Contains(td.Turns[1].Text, "done") {
		t.Fatalf("transcript after a tool turn: %+v", td.Turns)
	}
	var tool bool
	for _, p := range td.Turns[1].Parts {
		tool = tool || p.Kind == "tool" && p.Tool == "shell" && p.Output == "hi"
	}
	if !tool {
		t.Fatalf("the tool call is not in the mirror: %+v", td.Turns[1].Parts)
	}
	if LiveState(m) != "idle" {
		t.Fatalf("finished session reads %q", LiveState(m))
	}

	// Provider error: explicit, with the status, and visible in the mirror.
	if e, has := turn("please FAIL"); !has || !e.ok() || e.Data.StatusCode != 401 || e.retryable() {
		t.Fatalf("failed turn reported %+v (%v)", e, has)
	}
	td, _ = readTranscript(m)
	if n, ep := errorParts(td.Turns); n != 1 || ep.Info != "provider.auth (HTTP 401)" {
		t.Fatalf("failed turn in the mirror: %d error parts, %+v", n, ep)
	}

	// Interrupt while running, with input queued behind it.
	done := make(chan messageError, 1)
	go func() { e, _ := turn("go SLOW"); done <- e }()
	var working bool
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline) && !working; time.Sleep(200 * time.Millisecond) {
		working = LiveState(m) == "working"
	}
	if !working {
		t.Fatal("a running turn never read as working")
	}
	res, err := serveClient.Post(addr+"/api/session/"+ses+"/prompt", "application/json", strings.NewReader(`{"text":"queued behind"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	td, _ = readTranscript(m)
	if len(td.Queued) != 1 || td.Queued[0] != "queued behind" {
		t.Fatalf("queued = %q", td.Queued)
	}
	if err := serveAbort(addr, ses, dir); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-done:
		if !e.isAbort() {
			t.Fatalf("interrupted turn reported %+v", e)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("interrupt did not end the turn")
	}
	td, _ = readTranscript(m)
	if n, _ := errorParts(td.Turns); n != 1 { // still only the earlier provider error
		t.Fatalf("an interrupt rendered as a failure (%d error parts)", n)
	}

	// Compaction: its cost is billed once, the session total is assistant steps + compactions.
	res, err = serveClient.Post(addr+"/api/session/"+ses+"/compact", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
		if a, _ := (apiTarget{Addr: addr, Client: serveClient}).active(); !a[ses] {
			// the interrupted turn's queued input may run next; wait until nothing is running
			time.Sleep(time.Second)
			if a, _ := (apiTarget{Addr: addr, Client: serveClient}).active(); !a[ses] {
				break
			}
		}
	}
	var info struct {
		Data struct {
			Cost float64 `json:"cost"`
		} `json:"data"`
	}
	if err := (apiTarget{Addr: addr, Client: serveClient}).get("/api/session/"+ses, &info); err != nil {
		t.Fatal(err)
	}
	var sum float64
	for _, tn := range usageTurns(m) {
		sum += tn.CostUSD
	}
	if sum <= 0 || sum < info.Data.Cost-1e-9 || sum > info.Data.Cost+1e-9 {
		t.Fatalf("usage fold sums to %v, the daemon reports %v for the session", sum, info.Data.Cost)
	}
	stub.mu.Lock()
	seen := stub.seen
	stub.mu.Unlock()
	if seen < 4 {
		t.Fatalf("the stub provider saw %d requests; the turns did not reach it", seen)
	}
	t.Logf("opencode %s: tool turn, provider 401, interrupt with queued input and compaction read through the API; usage %.6f == session %.6f; stub saw %d requests",
		opencodeVersion(t), sum, info.Data.Cost, seen)
}

// TestContractV2Todo measures the ToDo list on the opencode 2 on PATH: the stub model calls
// `todowrite`, once on a default config and once with experimental.todo on, and the messages the
// daemon keeps are dumped (OPENCODE_V2_DUMP=<dir>) so the recorded fixtures can be refreshed.
func TestContractV2Todo(t *testing.T) {
	for _, on := range []bool{false, true} {
		name := "default"
		var extra map[string]any
		if on {
			name, extra = "experimental.todo", map[string]any{"experimental": map[string]any{"todo": true}}
		}
		t.Run(name, func(t *testing.T) {
			stub, addr, dir := startStubbedV2With(t, extra)
			ses, err := serveCreateSession(addr, dir, "todo")
			if err != nil {
				t.Fatal(err)
			}
			m := session.Meta{Name: "v2todo", Dir: dir, Kind: session.KindOpencode, Driver: session.DriverManaged}
			sids.Write(session.UUID(m.Dir, m.Name), ses)
			t.Cleanup(func() { sids.Remove(session.UUID(m.Dir, m.Name)) })
			st := agents.ThreadSettings{Model: "stub/m"}
			req, err := newTurnRequest(addr, ses, dir, agents.TurnInput{Prompt: "make a TODO list"}, st)
			if err != nil {
				t.Fatal(err)
			}
			res, err := sendTurn(addr, ses, dir, req, st)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if e, has := decodeTurnError(res.Body); has {
				t.Fatalf("the turn failed: %+v", e)
			}
			stub.mu.Lock()
			seen := stub.seen
			stub.mu.Unlock()
			if seen < 2 {
				t.Fatalf("the stub provider saw %d requests; the turn did not reach it", seen)
			}
			var raw json.RawMessage
			if err := (apiTarget{Addr: addr, Client: serveClient}).get("/api/session/"+ses+"/message?limit=50", &raw); err != nil {
				t.Fatal(err)
			}
			if dump := os.Getenv("OPENCODE_V2_DUMP"); dump != "" {
				_ = os.WriteFile(filepath.Join(dump, "todo-"+name+".messages.json"), raw, 0o644)
			}
			td, ok := readTranscript(m)
			if !ok {
				t.Fatal("the transcript could not be read")
			}
			// The tool part itself, as the daemon recorded it.
			var w struct {
				Data []v2Msg `json:"data"`
			}
			if err := json.Unmarshal(raw, &w); err != nil {
				t.Fatal(err)
			}
			var status, errText string
			for _, mm := range w.Data {
				for _, c := range mm.Content {
					if c.Type == "tool" && c.Name == "todowrite" {
						status = c.State.Status
						if c.State.Error != nil {
							errText = c.State.Error.Message
						}
					}
				}
			}
			if !on {
				if status != "error" || !strings.Contains(errText, "todowrite") {
					t.Fatalf("default config: todowrite part status %q error %q; want the tool to be unavailable", status, errText)
				}
				if len(td.Tasks) != 0 {
					t.Fatalf("default config: tasks = %+v", td.Tasks)
				}
				return
			}
			if status != "completed" {
				t.Fatalf("experimental.todo: todowrite part status %q (%s); the flag did not enable the tool", status, errText)
			}
			want := [][2]string{{"write the probe", "completed"}, {"read the result", "in_progress"}, {"report", "pending"}}
			if len(td.Tasks) != len(want) {
				t.Fatalf("experimental.todo: tasks = %+v", td.Tasks)
			}
			for i, wt := range want {
				if td.Tasks[i].Subject != wt[0] || td.Tasks[i].Status != wt[1] {
					t.Fatalf("task %d = %+v, want %v", i, td.Tasks[i], wt)
				}
			}
		})
	}
}
