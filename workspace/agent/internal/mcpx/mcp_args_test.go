package mcpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func callTool(name string, args any) string {
	a, _ := json.Marshal(args)
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": json.RawMessage(a)})
	return string(mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: params}))
}

// spawnAgent stubs the Agent for create_session as a session caller and counts what reached it.
func spawnAgent(t *testing.T) (hits *int) {
	t.Helper()
	withFleetSpawn(t, true)
	mcpPeerMessagingEnabled = true
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	session.WriteMeta(session.Meta{Name: "parent1", Kind: session.KindClaude, Origin: session.OriginUser})
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		_, _ = w.Write([]byte(`{"name":"slot09"}`))
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	t.Setenv("AGENT_ADDR", u.Host)
	return &n
}

// The incident: `prompt` for `initial_prompt` used to start a child with no task.
func TestCreateSessionRefusesUnknownArgumentAndCreatesNothing(t *testing.T) {
	hits := spawnAgent(t)
	resp := callTool("create_session", map[string]any{"dir": "/repos/app", "prompt": "do the thing"})
	want := `unknown argument \"prompt\" for create_session (did you mean \"initial_prompt\"?)`
	if !strings.Contains(resp, `"isError":true`) || !strings.Contains(resp, want) {
		t.Fatalf("resp = %s, want an error containing %s", resp, want)
	}
	if *hits != 0 {
		t.Fatalf("the Agent was reached %d times for a refused call", *hits)
	}
}

func TestCreateSessionRefusesATypeMismatch(t *testing.T) {
	hits := spawnAgent(t)
	for _, args := range []map[string]any{
		{"dir": "/repos/app", "initial_prompt": "x", "worktree": "yes"},
		{"dir": "/repos/app", "initial_prompt": "x", "spend_cap_usd": "5"},
		{"dir": "/repos/app", "initial_prompt": 42},
	} {
		resp := callTool("create_session", args)
		if !strings.Contains(resp, `"isError":true`) || !strings.Contains(resp, "invalid argument") {
			t.Errorf("%v: resp = %s, want an invalid-argument error", args, resp)
		}
	}
	if *hits != 0 {
		t.Fatalf("the Agent was reached %d times for refused calls", *hits)
	}
	if resp := callTool("create_session", []string{"not", "an", "object"}); !strings.Contains(resp, "must be a JSON object") {
		t.Errorf("non-object arguments: %s", resp)
	}
}

// Known keys behave as before, including the protocol keys that may travel with a call:
// `_meta` (progress token) is a member of params, not of arguments, and the opencode caller
// stamp is taken out before the check.
func TestKnownArgumentsStillAcceptedWithProtocolKeys(t *testing.T) {
	hits := spawnAgent(t)
	a, _ := json.Marshal(map[string]any{"dir": "/repos/app", "initial_prompt": "task", mcpCallerSIDArg: "ses_x"})
	params, _ := json.Marshal(map[string]any{
		"name": "create_session", "arguments": json.RawMessage(a),
		"_meta": map[string]any{"progressToken": "tok"},
	})
	resp := string(mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: params}))
	if strings.Contains(resp, `"isError":true`) || *hits != 1 {
		t.Fatalf("hits=%d resp=%s", *hits, resp)
	}
	if strings.Contains(resp, "no task") {
		t.Fatalf("a call with a task carried the no-task warning: %s", resp)
	}
}

func TestCreateSessionFromSessionWithoutTaskSaysSo(t *testing.T) {
	hits := spawnAgent(t)
	resp := callTool("create_session", map[string]any{"dir": "/repos/app"})
	if *hits != 1 || strings.Contains(resp, `"isError":true`) || !strings.Contains(resp, "started with no task") {
		t.Fatalf("hits=%d resp=%s, want success carrying the no-task warning", *hits, resp)
	}
}

func TestClosestProp(t *testing.T) {
	props := map[string]bool{"initial_prompt": true, "dir": true, "worktree": true, "new_branch": true, "title": true}
	for key, want := range map[string]string{
		"prompt":      "initial_prompt",
		"worktre":     "worktree",
		"newbranch":   "new_branch",
		"directory":   "dir",
		"zzzzzzzz":    "",
		"unrelatedxx": "",
	} {
		if got := mcpClosestProp(key, props); got != want {
			t.Errorf("mcpClosestProp(%q) = %q, want %q", key, got, want)
		}
	}
}

// unionTags is the json tags of mcpCallArgs.
func unionTags() map[string]bool {
	tags := map[string]bool{}
	rt := reflect.TypeOf(mcpCallArgs{})
	for i := 0; i < rt.NumField(); i++ {
		if tag := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]; tag != "" && tag != "-" {
			tags[tag] = true
		}
	}
	return tags
}

// sampleFor builds a value of the type a schema property declares.
func sampleFor(schema map[string]any) any {
	switch schema["type"] {
	case "string":
		return "x"
	case "integer":
		return 1
	case "number":
		return 0.5
	case "boolean":
		return true
	case "array":
		return []any{}
	case "object":
		return map[string]any{}
	}
	return nil
}

// Schema and decoder cannot drift: every property an advertised tool declares must be a field
// of the union the handler decodes (otherwise it is advertised and silently dropped), and every
// field of the union must be declared by some advertised tool (otherwise it is dead, or a
// parameter nobody is told about). Each property is also decoded with a value of its declared
// type, which is what the type-mismatch refusal would otherwise turn on a legitimate call.
func TestMCPArgsSchemaAndDecoderAgree(t *testing.T) {
	tags := unionTags()
	declared := map[string]bool{}
	walkAdvertisedToolVariants(t, func(t *testing.T, tools []map[string]any) {
		for _, tool := range tools {
			name := tool["name"].(string)
			props := tool["inputSchema"].(map[string]any)["properties"]
			rawProps, _ := props.(map[string]any)
			if rawProps == nil {
				if typed, ok := props.(map[string]map[string]any); ok {
					rawProps = map[string]any{}
					for k, v := range typed {
						rawProps[k] = v
					}
				}
			}
			keys, ok := mcpSchemaProps(tool)
			if !ok {
				t.Errorf("%s: schema has no enumerable properties; the call-side check would skip it", name)
				continue
			}
			for k := range keys {
				declared[k] = true
				if mcpOwnDecoderTools[name] {
					continue
				}
				if !tags[k] {
					t.Errorf("%s: advertised property %q has no field in mcpCallArgs, so the decoder would drop it", name, k)
					continue
				}
				prop, _ := rawProps[k].(map[string]any)
				v := sampleFor(prop)
				if v == nil {
					continue
				}
				body, _ := json.Marshal(map[string]any{k: v})
				var a mcpCallArgs
				if msg := mcpCheckCallArgs(name, body, &a); msg != "" {
					t.Errorf("%s: %q declared as %v but the check refuses a value of that type: %s", name, k, prop["type"], msg)
				}
			}
		}
	})
	var dead []string
	for tag := range tags {
		if !declared[tag] {
			dead = append(dead, tag)
		}
	}
	sort.Strings(dead)
	if len(dead) > 0 {
		t.Errorf("mcpCallArgs fields no advertised tool declares: %v", dead)
	}
}
