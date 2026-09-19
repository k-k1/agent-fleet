package mcpc

// TestHelperProcess is the standard exec_test.go trampoline (Go's own os/exec tests use
// the same trick): the test binary re-execs ITSELF with -test.run=TestHelperProcess, and
// this test's body — gated on an env var so a normal `go test` run treats it as a no-op
// — becomes a tiny, fully controllable fake stdio MCP server. That gives the other
// tests in this package BOTH protocol eras and a server that can misbehave on purpose
// (ignore stdin closing) without shipping a second real binary.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

const (
	helperEnvFlag = "MCPC_TEST_HELPER"
	helperEnvMode = "MCPC_TEST_HELPER_MODE"
)

func TestHelperProcess(t *testing.T) {
	if os.Getenv(helperEnvFlag) != "1" {
		return // ordinary `go test` invocation: do nothing
	}
	runFakeStdioServer(os.Getenv(helperEnvMode))
}

// fakeToolsV1/V2 model a server whose tools/list grows after a "bump" call, so the
// notifications/tools/list_changed path has something real to observe.
func fakeTools(version int) []map[string]any {
	tools := []map[string]any{
		{"name": "echo", "description": "echoes msg back", "inputSchema": map[string]any{
			"type": "object", "properties": map[string]any{"msg": map[string]any{"type": "string"}},
		}},
		{"name": "boom", "description": "always answers isError:true", "inputSchema": map[string]any{"type": "object"}},
		{"name": "bump", "description": "grows the tool list and announces it", "inputSchema": map[string]any{"type": "object"}},
	}
	if version >= 2 {
		tools = append(tools, map[string]any{"name": "extra", "description": "only after bump", "inputSchema": map[string]any{"type": "object"}})
	}
	return tools
}

func runFakeStdioServer(mode string) {
	r := bufio.NewReader(os.Stdin)
	w := bufio.NewWriter(os.Stdout)
	write := func(v any) {
		b, _ := json.Marshal(v)
		_, _ = w.Write(b)
		_, _ = w.Write([]byte{'\n'})
		_ = w.Flush()
	}
	version := 1

	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var req map[string]any
			if json.Unmarshal(bytes.TrimSpace(line), &req) == nil {
				handleFakeReq(mode, req, &version, write)
			}
		}
		if err != nil {
			if mode == "hang" {
				// Deliberately ignore stdin closing — this is the misbehaving server
				// stdio.go's close() must still recover a process from (Kill escalation).
				time.Sleep(time.Hour)
			}
			return
		}
	}
}

func handleFakeReq(mode string, req map[string]any, version *int, write func(any)) {
	method, _ := req["method"].(string)
	id := req["id"]

	switch method {
	case "server/discover":
		if mode == "legacy" {
			write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": "Method not found"}})
			return
		}
		write(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
			"supportedVersions": []string{ProtocolVersion},
			"capabilities":      map[string]any{"tools": map[string]any{"listChanged": true}},
			"serverInfo":        map[string]any{"name": "fake-" + mode, "version": "1"},
		}})
	case "initialize":
		write(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
			"protocolVersion": ProtocolVersionLegacy,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}},
			"serverInfo":      map[string]any{"name": "fake-" + mode, "version": "1"},
		}})
	case "notifications/initialized":
		// no reply
	case "tools/list":
		write(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"tools": fakeTools(*version)}})
	case "tools/call":
		handleFakeToolCall(id, req, version, write)
	case "ping":
		if id != nil {
			write(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{}})
		}
	}
}

func handleFakeToolCall(id any, req map[string]any, version *int, write func(any)) {
	params, _ := req["params"].(map[string]any)
	name, _ := params["name"].(string)
	args, _ := params["arguments"].(map[string]any)
	switch name {
	case "echo":
		write(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
			"content": []map[string]any{{"type": "text", "text": fmt.Sprint(args["msg"])}},
			"isError": false,
		}})
	case "boom":
		write(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
			"content": []map[string]any{{"type": "text", "text": "boom happened"}},
			"isError": true,
		}})
	case "bump":
		*version = 2
		write(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
			"content": []map[string]any{{"type": "text", "text": "bumped"}},
			"isError": false,
		}})
		write(map[string]any{"jsonrpc": "2.0", "method": "notifications/tools/list_changed"})
	default:
		write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32602, "message": "unknown tool"}})
	}
}
