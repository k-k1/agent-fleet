package mcpx

// The equivalence proof for the tool registration (ADR 0124 Phase 2): what every audience is
// advertised, and what every tool name answers on every audience for a fixed set of argument
// shapes, are held to goldens under testdata/mcp_tools. A refactor of where a tool is declared
// must leave both byte-identical; a golden diff is a behaviour change and is reviewed as one.
//
//	go test -run 'TestToolListGolden|TestToolCallGolden' -update-tool-golden .

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpreg"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

var updateToolGolden = flag.Bool("update-tool-golden", false, "rewrite testdata/mcp_tools")

// toolGoldenDir is absolute because the tests chdir into their own HOME.
var toolGoldenDir, _ = filepath.Abs(filepath.Join("testdata", "mcp_tools"))

// toolAudience is one way the server can be started. studio binds the owning session to a
// studio, which is what turns the studio tools on (and generate_image off).
type toolAudience struct {
	name   string
	args   []string
	studio bool
}

var sessionAllFlags = []string{"--self-report", "--chromium-attach", "--peer-messaging", "--image-gen",
	"--fleet-spawn", "--session-search", "--agent-memory"}

// toolAudiences is every flag the surface reads, alone and together. The capability flags
// without --self-report are in it because the conjunction in parseStdioFlags is part of the
// boundary being proved.
var toolAudiences = []toolAudience{
	{name: "assistant-read"},
	{name: "assistant-write", args: []string{"--write", "--conv", "conv1"}},
	{name: "assistant-read-nobrowser", args: []string{mcpreg.BrowserUnavailableFlag, "kubernetes"}},
	{name: "assistant-write-nobrowser", args: []string{"--write", "--conv", "conv1", mcpreg.BrowserUnavailableFlag, "kubernetes"}},
	{name: "assistant-session-flags", args: append([]string{"--conv", "conv1"}, sessionAllFlags[1:]...)},
	{name: "session", args: []string{"--self-report"}},
	{name: "session-write", args: []string{"--self-report", "--write"}},
	{name: "session-chromium", args: []string{"--self-report", "--chromium-attach"}},
	{name: "session-chromium-nobrowser", args: []string{"--self-report", "--chromium-attach", mcpreg.BrowserUnavailableFlag, "ecs"}},
	{name: "session-peer", args: []string{"--self-report", "--peer-messaging"}},
	{name: "session-memory", args: []string{"--self-report", "--agent-memory"}},
	{name: "session-search", args: []string{"--self-report", "--session-search"}},
	{name: "session-spawn", args: []string{"--self-report", "--fleet-spawn"}},
	{name: "session-imagegen", args: []string{"--self-report", "--image-gen"}},
	{name: "session-studio", args: []string{"--self-report", "--image-gen"}, studio: true},
	{name: "session-all", args: sessionAllFlags},
	{name: "session-all-write", args: append([]string{"--write"}, sessionAllFlags...)},
}

const goldenSelf, goldenChild = "slot01", "child01"

// goldenFake is the Agent and the CP at once: every request is recorded, and each answers the
// smallest body that lets a handler run to its end.
type goldenFake struct {
	mu   sync.Mutex
	log  []string
	host string
}

func (f *goldenFake) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.log
	f.log = nil
	return out
}

func (f *goldenFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	line := r.Method + " " + r.URL.RequestURI()
	if len(body) > 0 {
		line += " " + string(body)
	}
	if r.URL.Path != "/imagegen/status" && !strings.HasSuffix(r.URL.Path, "/studio") {
		f.log = append(f.log, line)
	}
	f.mu.Unlock()
	switch {
	case r.URL.Path == "/imagegen/status":
		_, _ = w.Write([]byte(`{"enabled":true,"ready":true,"kind":"claude","providers":[{"id":"agy","ops":["generate"]}]}`))
	case strings.HasPrefix(r.URL.Path, "/sessions/") && strings.HasSuffix(r.URL.Path, "/status"):
		_, _ = w.Write([]byte(`{"alive":true,"ready":true,"state":"idle","questions":[1],"plan":"p"}`))
	case strings.HasSuffix(r.URL.Path, "/input"):
		_, _ = w.Write([]byte(`{"sent":"x"}`))
	default:
		_, _ = w.Write([]byte(`{}`))
	}
}

// startToolAudience parses the audience's argv over a fresh HOME with the owning session and
// one child of it on disk, and points the Agent and the CP at the fake.
func startToolAudience(t *testing.T, aud toolAudience) *goldenFake {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))
	t.Chdir(home)
	fake := &goldenFake{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	fake.host = u.Host
	t.Setenv("AGENT_ADDR", u.Host)
	t.Setenv("AF_CP_INTERNAL_URL", srv.URL)
	t.Setenv("AF_MEMO_TOKEN", "memo-token")
	t.Setenv("AF_SCHEDULE_TOKEN", "schedule-token")
	self := session.Meta{Name: goldenSelf, Dir: home, Kind: "claude", Origin: session.OriginUser}
	if aud.studio {
		self.Studio = "st-1"
	}
	session.WriteMeta(self)
	session.WriteMeta(session.Meta{Name: goldenChild, Dir: home, Kind: "codex",
		Origin: session.OriginSession, OriginSession: goldenSelf})

	oldSource := mcpSourceSession
	t.Cleanup(func() {
		mcpSourceSession = oldSource
		parseStdioFlags(nil)
		forgetAdvertised()
	})
	forgetAdvertised()
	mcpSourceSession = goldenSelf
	parseStdioFlags(aud.args)
	return fake
}

// goldenJSON is indented and does not escape <, > and &, so a description reads as written.
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
	path := filepath.Join(toolGoldenDir, name)
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

func toolListResult(t *testing.T) []any {
	t.Helper()
	var res struct {
		Result struct {
			Tools []any `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(dispatchMCPStdio([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)), &res); err != nil {
		t.Fatal(err)
	}
	return res.Result.Tools
}

// TestToolListGolden: the tools/list answer of each audience, in order, byte for byte. Each
// distinct schema is written once, under its name and a variant number when one name is
// advertised in more than one form (the operator's text and a session's), and each audience is
// its ordered list of those keys.
func TestToolListGolden(t *testing.T) {
	audiences := map[string][]string{}
	tools := map[string]any{}
	variants := map[string][]string{}
	for _, aud := range toolAudiences {
		func() {
			startToolAudience(t, aud)
			keys := []string{}
			for _, tool := range toolListResult(t) {
				name := tool.(map[string]any)["name"].(string)
				enc := string(goldenJSON(t, tool))
				i := slices.Index(variants[name], enc)
				if i < 0 {
					variants[name] = append(variants[name], enc)
					i = len(variants[name]) - 1
				}
				key := fmt.Sprintf("%s#%d", name, i+1)
				tools[key] = tool
				keys = append(keys, key)
			}
			audiences[aud.name] = keys
		}()
	}
	checkGolden(t, "list.json", goldenJSON(t, map[string]any{"audiences": audiences, "tools": tools}))
}

// goldenArgValue is a value of the schema's own type, the first enum member when it has one.
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
		items, _ := prop["items"].(map[string]any)
		if items == nil {
			return []any{}
		}
		return []any{goldenArgValue(items)}
	case "object":
		return map[string]any{}
	}
	return "v"
}

// goldenKinds is every registered kind, and spellings that are not one.
var goldenKinds = []string{"", "claude", "codex", "opencode", "agy", "copilot", "cursor", "kiro", "lcpp", "muse",
	"shell", "ssm", "Codex", "nope"}

// goldenArgSets is what each tool is called with: no arguments, an empty object, a misspelt
// key, a value of the wrong JSON type for each property, and every property filled with a
// value of its type (name = the owning session's child, so the child-only gates let it pass),
// and, for a tool with a kind, each of goldenKinds.
func goldenArgSets(schema map[string]any) [][2]string {
	sets := [][2]string{{"absent", ""}, {"empty", "{}"}, {"unknown", `{"zz_unknown":1}`}, {"not-object", `[1]`}}
	var props map[string]any
	if s, ok := schema["inputSchema"].(map[string]any); ok {
		props, _ = s["properties"].(map[string]any)
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sets = append(sets, [2]string{"badtype-" + k, fmt.Sprintf(`{%q:[[{}]]}`, k)})
	}
	if len(keys) > 0 {
		full := map[string]any{}
		for _, k := range keys {
			p, _ := props[k].(map[string]any)
			full[k] = goldenArgValue(p)
		}
		for _, k := range []string{"name", "session"} {
			if _, ok := full[k]; ok {
				full[k] = goldenChild
			}
		}
		b, _ := json.Marshal(full)
		sets = append(sets, [2]string{"full", string(b)})
	}
	if _, ok := props["kind"]; ok {
		for _, k := range goldenKinds {
			sets = append(sets, [2]string{"kind=" + k, fmt.Sprintf(`{"kind":%q}`, k)})
		}
	}
	return sets
}

// toolSchemasEverywhere is every advertised tool by name, the first audience's schema winning,
// plus a name no audience offers.
func toolSchemasEverywhere(t *testing.T) ([]string, map[string]map[string]any) {
	t.Helper()
	schemas := map[string]map[string]any{"no_such_tool": nil}
	for _, aud := range toolAudiences {
		func() {
			startToolAudience(t, aud)
			for _, tool := range toolListResult(t) {
				m := tool.(map[string]any)
				name := m["name"].(string)
				if _, ok := schemas[name]; !ok {
					schemas[name] = m
				}
			}
		}()
	}
	names := make([]string, 0, len(schemas))
	for n := range schemas {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, schemas
}

// TestToolCallGolden: every tool name on every audience, after that audience's tools/list,
// with each argument set — the result the model reads and every request that reached the Agent
// or the CP. It is the proof that the call gates and the argument checks did not move. Each
// tool and argument set is one block, its audiences grouped by identical answers.
func TestToolCallGolden(t *testing.T) {
	names, schemas := toolSchemasEverywhere(t)
	type call struct{ tool, set, args string }
	var calls []call
	for _, name := range names {
		for _, set := range goldenArgSets(schemas[name]) {
			calls = append(calls, call{name, set[0], set[1]})
		}
	}
	answers := make([][]string, len(calls)) // [call][audience]
	for i := range answers {
		answers[i] = make([]string, len(toolAudiences))
	}
	for ai, aud := range toolAudiences {
		func() {
			fake := startToolAudience(t, aud)
			toolListResult(t)
			for ci, c := range calls {
				params := map[string]any{"name": c.tool}
				if c.args != "" {
					params["arguments"] = json.RawMessage(c.args)
				}
				p, _ := json.Marshal(params)
				resp := mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: p})
				var a strings.Builder
				for _, l := range fake.take() {
					fmt.Fprintf(&a, "  -> %s\n", l)
				}
				fmt.Fprintf(&a, "  <- %s\n", resp)
				ans := strings.ReplaceAll(a.String(), fake.host, "FAKE")
				answers[ci][ai] = strings.ReplaceAll(ans, os.Getenv("HOME"), "$HOME")
			}
		}()
	}
	var out bytes.Buffer
	for ci, c := range calls {
		fmt.Fprintf(&out, "## %s %s %s\n", c.tool, c.set, c.args)
		var order []string
		groups := map[string][]string{}
		for ai, aud := range toolAudiences {
			ans := answers[ci][ai]
			if _, ok := groups[ans]; !ok {
				order = append(order, ans)
			}
			groups[ans] = append(groups[ans], aud.name)
		}
		for _, ans := range order {
			fmt.Fprintf(&out, "[%s]\n%s", strings.Join(groups[ans], " "), ans)
		}
	}
	checkGolden(t, "calls.txt", out.Bytes())
}

// operatorWriteTools is what --write adds to the assistant surface.
func operatorWriteTools() []map[string]any {
	return operatorTools(true)[len(operatorTools(false)):]
}
