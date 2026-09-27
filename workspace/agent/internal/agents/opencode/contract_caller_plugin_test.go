//go:build clicontract

// Does the AF caller plugin (workspace/opencode-plugin/agent-fleet-caller.js) really hand
// af's MCP child the calling opencode session, per call? contract_mcp_identity_test.go
// measured that no per-session CONFIG point exists; #989's route is the plugin hook instead,
// and mcpx.mcpCallerSession trusts it only as far as the facts pinned here:
//
//   - tool.execute.before fires for MCP tools, and the args it mutates are what reaches the
//     child's tools/call — for two sessions sharing one child, each with its own id;
//   - a value the model wrote under the reserved name is overwritten, and another server's
//     tools never see the key;
//   - the child runs in the session's directory (the scope mcpCallerSession matches in).
//
// No real model is involved: a stub OpenAI-compatible endpoint scripts the tool calls, so
// this runs in tier A with no billing and no flake from an external service.
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
	"testing"
	"time"
)

const (
	callerProbeAFServer    = "af_0123abcd" // the shape mcpreg mints per boot
	callerProbeOtherServer = "other"
	callerProbeArg         = "_af_caller_sid"
)

func TestContractOpencodeCallerPluginStampsAFTools(t *testing.T) {
	requireOpencode(t)
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is needed for the stub MCP server")
	}
	plugin, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "opencode-plugin", "agent-fleet-caller.js"))
	if err != nil {
		t.Fatalf("read the shipped plugin: %v", err)
	}

	llm := httptest.NewServer(http.HandlerFunc(callerProbeLLM))
	t.Cleanup(llm.Close)

	home, work := t.TempDir(), t.TempDir()
	log := filepath.Join(work, "calls.log")
	script := filepath.Join(work, "mcp.py")
	if err := os.WriteFile(script, []byte(callerProbeMCP), 0o600); err != nil {
		t.Fatal(err)
	}
	pluginDir := filepath.Join(home, ".config", "opencode", "plugin")
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "agent-fleet-caller.js"), plugin, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"provider": map[string]any{"stub": map[string]any{
			"npm": "@ai-sdk/openai-compatible", "name": "stub",
			"options": map[string]any{"baseURL": llm.URL + "/v1", "apiKey": "x"},
			"models":  map[string]any{"m": map[string]any{"name": "m", "tool_call": true}},
		}},
		"model":      "stub/m",
		"permission": map[string]any{"*": "allow"},
		"mcp": map[string]any{
			callerProbeAFServer:    map[string]any{"type": "local", "enabled": true, "command": []string{py, script, log, callerProbeAFServer}},
			callerProbeOtherServer: map[string]any{"type": "local", "enabled": true, "command": []string{py, script, log, callerProbeOtherServer}},
		},
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(filepath.Join(home, ".config", "opencode", "opencode.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	addr, _ := startServeIn(t, home)

	shared := t.TempDir()
	// The first request for a directory boots its instance — plugins and both MCP children —
	// which outlasts serveClient's 10 s; take that hit here with a longer client.
	if res, err := (&http.Client{Timeout: 60 * time.Second}).Get(addr + "/mcp?directory=" + shared); err == nil {
		res.Body.Close()
	} else {
		t.Logf("warm-up GET /mcp: %v", err)
	}
	var ids []string
	for _, title := range []string{"first", "second"} {
		ses, err := serveCreateSession(addr, shared, title)
		if err != nil {
			t.Fatalf("serveCreateSession(%s) against opencode %s: %v", title, opencodeVersion(t), err)
		}
		callerProbeTurn(t, addr, shared, ses)
		ids = append(ids, ses)
	}

	calls := callerProbeCalls(t, log)
	other := 0
	var afSids []string
	for _, c := range calls {
		sid, stamped := c.Args[callerProbeArg]
		switch c.Server {
		case callerProbeAFServer:
			afSids = append(afSids, fmt.Sprint(sid))
			if c.Args["x"] != "1" {
				t.Errorf("af call lost the model's own argument: %v", c.Args)
			}
			if resolved, err := filepath.EvalSymlinks(shared); err == nil && c.Cwd != resolved && c.Cwd != shared {
				t.Errorf("af child runs in %q, not the session directory %q: mcpCallerSession's "+
					"folder scope no longer holds", c.Cwd, shared)
			}
		case callerProbeOtherServer:
			other++
			if stamped {
				t.Errorf("a non-af MCP server received %s=%v: the plugin must stamp af's tools only", callerProbeArg, sid)
			}
		}
	}
	if strings.Join(afSids, ",") != strings.Join(ids, ",") {
		t.Fatalf("af tools/call carried %s = %v, want each calling session's own id %v in turn "+
			"(the model's forged value must be overwritten) — opencode %s, calls=%+v",
			callerProbeArg, afSids, ids, opencodeVersion(t), calls)
	}

	if other == 0 {
		t.Fatalf("the other server was never called, so \"untouched\" proves nothing: calls=%+v", calls)
	}

	// Recorded, not asserted: the stamped args are also the stored tool input (and so replayed
	// to the model). The mirror only renders chosen keys, so this is harmless today.
	if in := callerProbeStoredInput(t, addr, shared, ids[0]); in != nil {
		t.Logf("stored af tool input: %v", in)
	}
	t.Logf("opencode %s: af tools/call stamped per session %v; other server untouched", opencodeVersion(t), ids)
}

// callerProbeLLM scripts one step: the first request of a turn asks for one af tool call
// (with a forged stamp) and one call to the other server; the follow-up carrying the tool
// results ends the turn.
func callerProbeLLM(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
		Tools []json.RawMessage `json:"tools"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	answered := false
	for _, m := range body.Messages {
		answered = answered || m.Role == "tool"
	}
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(delta map[string]any, finish any) {
		b, _ := json.Marshal(map[string]any{
			"id": "c", "object": "chat.completion.chunk", "created": 0, "model": "m",
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
	}
	if answered || len(body.Tools) == 0 {
		send(map[string]any{"role": "assistant", "content": "done"}, nil)
		send(map[string]any{}, "stop")
	} else {
		call := func(i int, name, args string) map[string]any {
			return map[string]any{"index": i, "id": fmt.Sprintf("call_%d", i), "type": "function",
				"function": map[string]any{"name": name, "arguments": args}}
		}
		send(map[string]any{"role": "assistant", "tool_calls": []any{
			call(0, callerProbeAFServer+"_probe_tool", `{"x":"1","`+callerProbeArg+`":"ses_FORGED"}`),
			call(1, callerProbeOtherServer+"_probe_tool", `{"x":"1"}`),
		}}, nil)
		send(map[string]any{}, "tool_calls")
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

// callerProbeMCP is a stdio MCP server with one closed-schema tool that logs each
// tools/call's arguments with its server name and working directory.
const callerProbeMCP = `import json, os, sys
log, name = sys.argv[1], sys.argv[2]
for line in sys.stdin:
    if not line.strip():
        continue
    req = json.loads(line)
    if req.get("id") is None:
        continue
    m = req.get("method")
    if m == "initialize":
        res = {"protocolVersion": req["params"].get("protocolVersion", "2025-06-18"),
               "capabilities": {"tools": {}}, "serverInfo": {"name": name, "version": "0"}}
    elif m == "tools/list":
        res = {"tools": [{"name": "probe_tool", "description": "probe", "inputSchema": {
            "type": "object", "additionalProperties": False,
            "properties": {"x": {"type": "string"}}, "required": ["x"]}}]}
    elif m == "tools/call":
        with open(log, "a") as f:
            f.write(json.dumps({"server": name, "cwd": os.getcwd(), "args": req["params"].get("arguments")}) + "\n")
        res = {"content": [{"type": "text", "text": "ok"}]}
    else:
        res = {}
    sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": req["id"], "result": res}) + "\n")
    sys.stdout.flush()
`

type callerProbeCall struct {
	Server string         `json:"server"`
	Cwd    string         `json:"cwd"`
	Args   map[string]any `json:"args"`
}

func callerProbeTurn(t *testing.T, addr, dir, ses string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"model": map[string]string{"providerID": "stub", "modelID": "m"},
		"parts": []any{map[string]string{"type": "text", "text": "call the probe"}},
	})
	res, err := (&http.Client{Timeout: 90 * time.Second}).Post(
		addr+"/session/"+ses+"/message?directory="+dir, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("turn on %s: %v", ses, err)
	}
	defer res.Body.Close()
	if b, _ := io.ReadAll(res.Body); res.StatusCode != http.StatusOK {
		t.Fatalf("turn on %s = %d: %s", ses, res.StatusCode, truncateProbe(string(b)))
	}
}

func callerProbeCalls(t *testing.T, log string) []callerProbeCall {
	t.Helper()
	var out []callerProbeCall
	for _, l := range probeLines(log) {
		var c callerProbeCall
		if err := json.Unmarshal([]byte(l), &c); err != nil {
			t.Fatalf("probe log line %q: %v", l, err)
		}
		out = append(out, c)
	}
	return out
}

func callerProbeStoredInput(t *testing.T, addr, dir, ses string) map[string]any {
	t.Helper()
	var msgs []struct {
		Parts []struct {
			Type  string `json:"type"`
			Tool  string `json:"tool"`
			State struct {
				Input map[string]any `json:"input"`
			} `json:"state"`
		} `json:"parts"`
	}
	if json.Unmarshal(getJSON(t, addr+"/session/"+ses+"/message?directory="+dir), &msgs) != nil {
		return nil
	}
	for _, m := range msgs {
		for _, p := range m.Parts {
			if p.Type == "tool" && strings.HasPrefix(p.Tool, callerProbeAFServer+"_") {
				return p.State.Input
			}
		}
	}
	return nil
}
