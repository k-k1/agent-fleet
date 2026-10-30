package mcpsrv

// The equivalence proof for the tool tables (ADR 0124 Phase 2): the tools/list every audience
// is given, and what every tool name answers a member for a fixed set of argument shapes, are
// held to goldens under testdata/mcp_tools. A golden diff is a behaviour change and is
// reviewed as one.
//
//	go test ./internal/mcpsrv -run 'TestToolListGolden|TestToolCallGolden' -update-tool-golden

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/control-plane/internal/runtime"
	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

var updateToolGolden = flag.Bool("update-tool-golden", false, "rewrite testdata/mcp_tools")

var goldenScopes = []string{"read", "write", "admin:dangerous"}

// goldenCP is memCP with every outward call recorded in order (the Agent, the memo queue and
// the schedule guard), so every member tool runs to its end.
type goldenCP struct {
	*memCP
	log *[]string
}

func (c goldenCP) record(op string, args ...any) {
	b, _ := json.Marshal(args)
	*c.log = append(*c.log, op+" "+string(b))
}

func (c goldenCP) AgentText(_ context.Context, _ runtime.Runtime, method, path string, body []byte) (string, error) {
	c.record("AGENT", method, path, string(body))
	return `{"ok":true}`, nil
}

func (c goldenCP) ScheduleGuardErr(_ context.Context, _, repo, sess string) error {
	c.record("SCHEDULE-GUARD", repo, sess)
	return nil
}

func (c goldenCP) MemoList(_ context.Context, _ string) (any, error) {
	c.record("MEMO-LIST")
	return map[string]any{"ok": true}, nil
}

func (c goldenCP) MemoCreate(_ context.Context, _ store.MembershipView, repo, category, kind, body, refPath string) (any, error) {
	c.record("MEMO-CREATE", repo, category, kind, body, refPath)
	return map[string]any{"ok": true}, nil
}

func (c goldenCP) MemoUpdate(_ context.Context, _, id string, repo, category, body, refPath *string, position *int) (any, error) {
	c.record("MEMO-UPDATE", id, repo, category, body, refPath, position)
	return map[string]any{"ok": true}, nil
}

func (c goldenCP) MemoFlush(_ context.Context, _ runtime.Runtime, _, sessionName string, ids []string) (map[string]any, error) {
	c.record("MEMO-FLUSH", sessionName, ids)
	return map[string]any{"ok": true}, nil
}

func goldenJSON(t *testing.T, v any) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "mcp_tools", name)
	if *updateToolGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update-tool-golden to create it)", err)
	}
	if !bytes.Equal(got, want) {
		gl, wl := strings.Split(string(got), "\n"), strings.Split(string(want), "\n")
		for i := 0; i < len(gl) || i < len(wl); i++ {
			var g, w string
			if i < len(gl) {
				g = gl[i]
			}
			if i < len(wl) {
				w = wl[i]
			}
			if g != w {
				t.Fatalf("%s differs from the golden at line %d:\n got: %s\nwant: %s", path, i+1, g, w)
			}
		}
	}
}

// goldenPrincipals seeds a tenant admin and a deployment super admin next to newMemCP's member.
func goldenPrincipals(t *testing.T, cp *memCP) map[string]mcpPrincipal {
	t.Helper()
	ctx := context.Background()
	st := cp.Store()
	mk := func(email, key, hint, role string) mcpPrincipal {
		id, err := st.UpsertIdentity(ctx, email, key, hint)
		if err != nil {
			t.Fatal(err)
		}
		m, err := st.EnsureMembership(ctx, id.ID, cp.tenantID, role)
		if err != nil {
			t.Fatal(err)
		}
		return mcpPrincipal{patID: "pat1", identityID: id.ID, membershipID: m.ID}
	}
	return map[string]mcpPrincipal{
		"member":       cp.prin,
		"tenant_admin": mk("t@example.com", "t-example-com", "", "tenant_admin"),
		"super_admin":  mk("s@example.com", "s-example-com", "super_admin", "member"),
	}
}

// TestToolListGolden: tools/list for every role × scope × tenant memory switch × endpoint.
func TestToolListGolden(t *testing.T) {
	ctx := context.Background()
	cp := newMemCP(t, memStore(t), "running")
	prins := goldenPrincipals(t, cp)
	roles := []string{"member", "tenant_admin", "super_admin"}
	// Each tool is written once and each audience as its ordered names: the CP's schemas do
	// not vary by audience, which the loop below checks rather than assumes.
	audiences, schemas := map[string][]string{}, map[string]map[string]any{}
	for _, memory := range []bool{false, true} {
		if err := cp.Store().SetTenantExternalMemory(ctx, cp.tenantID, memory); err != nil {
			t.Fatal(err)
		}
		for _, endpoint := range []string{"mcp", "memory"} {
			a := New(cp)
			if endpoint == "memory" {
				a = a.MemoryOnly()
			}
			for _, role := range roles {
				for _, scope := range goldenScopes {
					p := prins[role]
					p.scope = scope
					key := fmt.Sprintf("%s memory=%v %s %s", endpoint, memory, role, scope)
					audiences[key] = []string{}
					for _, tool := range a.mcpToolList(ctx, &p) {
						name := tool["name"].(string)
						audiences[key] = append(audiences[key], name)
						if prev, ok := schemas[name]; ok && !bytes.Equal(goldenJSON(t, prev), goldenJSON(t, tool)) {
							t.Fatalf("%s: %s is advertised with a schema of its own", key, name)
						}
						schemas[name] = tool
					}
				}
			}
		}
	}
	checkGolden(t, "list.json", goldenJSON(t, map[string]any{"audiences": audiences, "tools": schemas}))
}

func goldenArgValue(prop map[string]any) any {
	if enum, ok := prop["enum"].([]any); ok && len(enum) > 0 {
		return enum[0]
	}
	switch prop["type"] {
	case "integer":
		return 2
	case "number":
		return 1.5
	case "boolean":
		return true
	case "array":
		return []any{}
	case "object":
		return map[string]any{}
	}
	return "v"
}

// TestToolCallGolden: every tool name, called by the member at each scope with the tenant's
// memory on, with no arguments, a misspelt key, every property filled, and each kind the
// catalog or a caller might name — the result text and the Agent request it made.
func TestToolCallGolden(t *testing.T) {
	ctx := context.Background()
	var log []string
	cp := goldenCP{newMemCP(t, memStore(t), "running"), &log}
	if err := cp.Store().SetTenantExternalMemory(ctx, cp.tenantID, true); err != nil {
		t.Fatal(err)
	}
	tools := []mcpTool{{name: "no_such_tool"}}
	tools = append(append(append(tools, memberTools()...), memoryTools()...), adminTools()...)
	sort.SliceStable(tools, func(i, j int) bool { return tools[i].name < tools[j].name })
	kinds := []string{"", "claude", "codex", "opencode", "agy", "copilot", "cursor", "kiro", "lcpp", "muse",
		"shell", "ssm", "Codex", "nope"}
	var out bytes.Buffer
	for _, scope := range goldenScopes {
		for _, tool := range tools {
			if tool.admin {
				// Refused for a member before anything runs; the run itself needs the store's
				// tenant data, which this proof does not change.
				continue
			}
			sets := [][2]string{{"absent", ""}, {"unknown", `{"zz_unknown":1}`}}
			props, _ := tool.schema["properties"].(map[string]any)
			keys := make([]string, 0, len(props))
			for k := range props {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			if len(keys) > 0 {
				full := map[string]any{}
				for _, k := range keys {
					p, _ := props[k].(map[string]any)
					full[k] = goldenArgValue(p)
				}
				b, _ := json.Marshal(full)
				sets = append(sets, [2]string{"full", string(b)})
			}
			if _, ok := props["kind"]; ok {
				for _, k := range kinds {
					sets = append(sets, [2]string{"kind=" + k, fmt.Sprintf(`{"kind":%q}`, k)})
				}
			}
			for _, set := range sets {
				params := map[string]any{"name": tool.name}
				if set[1] != "" {
					params["arguments"] = json.RawMessage(set[1])
				}
				raw, _ := json.Marshal(params)
				p := cp.prin
				p.scope = scope
				log = nil
				resp := New(cp).mcpToolCall(ctx, &p, rpcRequest{ID: json.RawMessage(`1`), Params: raw})
				// Trimmed: an argument-less set would leave trailing whitespace git diff --check flags.
				fmt.Fprintf(&out, "%s\n", strings.TrimRight(fmt.Sprintf("## %s %s %s %s", scope, tool.name, set[0], set[1]), " "))
				for _, l := range log {
					fmt.Fprintf(&out, "-> %s\n", l)
				}
				b, _ := json.Marshal(resp)
				fmt.Fprintf(&out, "<- %s\n", b)
			}
		}
	}
	checkGolden(t, "calls.txt", out.Bytes())
}
