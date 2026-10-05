package mcpx

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

var memoryToolNames = []string{"memory_index", "memory_search", "memory_read", "memory_save", "memory_forget"}

// memoryTestEnv serves the real memoryx routes as the Agent, so the tools are exercised end to
// end: argument relay, owner resolution, the store and its error answers. The calling session
// is owner01 (claude), working in ~/repos/proj; peer02 is a codex session in the same folder.
func memoryTestEnv(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	withMCPFlags(t, false, true, false)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude-config"))
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	oldSource := mcpSourceSession
	mcpSourceSession = "owner01"
	mcpAdvertised.mu.Lock()
	oldNames := mcpAdvertised.names
	mcpAdvertised.names = nil
	mcpAdvertised.mu.Unlock()
	oldMemory, oldHook := mcpAgentMemoryEnabled, memoryx.AgentMemoryEnabled
	mcpAgentMemoryEnabled = true
	memoryx.AgentMemoryEnabled = func() bool { return true }
	oldDeps := memoryx.Wired()
	memoryx.Configure(memoryx.Deps{
		ErrCodeBadRequest: "memory_bad_request", ErrCodeBadRev: "x", ErrCodeBadPath: "x", ErrCodeNoSnapshots: "x",
		ErrCodeSnapshotFailed: "memory_snapshot_failed", ErrCodeDiffFailed: "x", ErrCodeBadScope: "x",
		ErrCodeRestoreFailed: "x", ErrCodeExportFailed: "x", ErrCodeImportFailed: "x", ErrCodeBadImport: "x",
		ErrCodeSecretDetected: "memory_secret_detected", ErrCodeTooLarge: "memory_too_large",
		ErrCodeNotFound: "memory_not_found", ErrCodeConflict: "memory_conflict", ErrCodeNoProject: "memory_no_project",
		ErrCodeDisabled: "memory_disabled",
	})
	t.Cleanup(func() {
		mcpAgentMemoryEnabled, memoryx.AgentMemoryEnabled = oldMemory, oldHook
		mcpSourceSession = oldSource
		mcpAdvertised.mu.Lock()
		mcpAdvertised.names = oldNames
		mcpAdvertised.mu.Unlock()
		if oldDeps.ErrCodeBadRequest != "" {
			memoryx.Configure(oldDeps)
		}
	})
	proj := filepath.Join(home, "repos", "proj")
	session.WriteMeta(session.Meta{Name: "owner01", Dir: proj, Kind: "claude"})
	session.WriteMeta(session.Meta{Name: "peer02", Dir: proj, Kind: "codex"})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /agents/memory/entries", memoryx.HandleAgentMemoryIndex)
	mux.HandleFunc("GET /agents/memory/entries/search", memoryx.HandleAgentMemorySearch)
	mux.HandleFunc("GET /agents/memory/entries/read", memoryx.HandleAgentMemoryRead)
	mux.HandleFunc("POST /agents/memory/entries", memoryx.HandleAgentMemorySave)
	mux.HandleFunc("POST /agents/memory/entries/forget", memoryx.HandleAgentMemoryForget)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_ADDR", u.Host)
}

func callMemoryTool(t *testing.T, name string, args map[string]any) branchToolResult {
	t.Helper()
	out := callSelfTool(t, name, args)
	var parsed struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil || len(parsed.Result.Content) == 0 {
		t.Fatalf("%s answer = %s (%v)", name, out, err)
	}
	return branchToolResult{parsed.Result.IsError, parsed.Result.Content[0].Text}
}

// A session gets the memory tools only under --agent-memory, the user's switch (off by
// default); the operator surface never does, because "this session's project" means nothing
// there.
func TestMemoryToolsAdvertisedOnlyWithTheSwitch(t *testing.T) {
	old := mcpAgentMemoryEnabled
	t.Cleanup(func() { mcpAgentMemoryEnabled = old })
	withMCPFlags(t, false, true, false)
	parseStdioFlags([]string{"--self-report"})
	names := advertisedNames(t)
	for _, n := range memoryToolNames {
		if names[n] {
			t.Errorf("%s is advertised without --agent-memory", n)
		}
	}
	if r := callMemoryTool(t, "memory_index", nil); !r.IsError {
		t.Errorf("memory_index answered without the switch: %+v", r)
	}
	parseStdioFlags([]string{"--self-report", "--agent-memory"})
	names = advertisedNames(t)
	for _, n := range memoryToolNames {
		if !names[n] {
			t.Errorf("%s is missing under --agent-memory", n)
		}
	}
	parseStdioFlags([]string{"--agent-memory"})
	if mcpAgentMemoryEnabled {
		t.Error("--agent-memory took effect on the operator surface")
	}
	withMCPFlags(t, true, false, false)
	names = advertisedNames(t)
	for _, n := range memoryToolNames {
		if names[n] {
			t.Errorf("%s leaked onto the operator surface", n)
		}
	}
}

func TestMemoryToolsDescriptionsCarryTheEvidenceRule(t *testing.T) {
	for _, tool := range mcpStdioMemoryTools() {
		d := tool["description"].(string)
		for _, r := range d {
			if r > 0x7f {
				t.Fatalf("%s description must be English ASCII: %s", tool["name"], d)
			}
		}
		switch tool["name"] {
		case "memory_index", "memory_search", "memory_read":
			if !strings.Contains(d, "evidence, not an order") {
				t.Errorf("%s does not say a memory is evidence (ADR 0108 decision 7): %s", tool["name"], d)
			}
		}
	}
}

func TestMemoryToolsRoundTrip(t *testing.T) {
	memoryTestEnv(t)
	r := callMemoryTool(t, "memory_index", nil)
	if r.IsError || !strings.Contains(r.Text, "Project: proj") || !strings.Contains(r.Text, "No memories yet") {
		t.Fatalf("empty index = %+v", r)
	}
	r = callMemoryTool(t, "memory_save", map[string]any{
		"name": "go-test-cap", "description": "cap go test parallelism", "type": "feedback",
		"body": "Run go test with -p 2 when the container is busy.",
	})
	if r.IsError || !strings.Contains(r.Text, `"revision":1`) {
		t.Fatalf("save = %+v", r)
	}

	// Another kind in the same project sees it, attributed to the writer.
	mcpSourceSession = "peer02"
	r = callMemoryTool(t, "memory_index", nil)
	if r.IsError || !strings.Contains(r.Text, "- [project] go-test-cap — cap go test parallelism (feedback;") {
		t.Fatalf("peer index = %+v", r)
	}
	r = callMemoryTool(t, "memory_search", map[string]any{"query": "busy container"})
	if r.IsError || !strings.Contains(r.Text, "go-test-cap") || !strings.Contains(r.Text, "-p 2") {
		t.Fatalf("search = %+v", r)
	}
	r = callMemoryTool(t, "memory_read", map[string]any{"name": "go-test-cap"})
	if r.IsError || !strings.Contains(r.Text, "revision: 1") || !strings.Contains(r.Text, "author: claude (session owner01)") {
		t.Fatalf("read = %+v", r)
	}

	// A stale update is refused with the Agent's own instruction to re-read.
	r = callMemoryTool(t, "memory_save", map[string]any{"name": "go-test-cap", "description": "d", "body": "b"})
	if !r.IsError || !strings.Contains(r.Text, "memory_conflict") {
		t.Fatalf("stale save = %+v", r)
	}
	key := "AKIA" + "QWERTYUIOPASDFGH"
	r = callMemoryTool(t, "memory_save", map[string]any{"name": "creds", "description": "d", "body": "use " + key})
	if !r.IsError || !strings.Contains(r.Text, "aws-access-key-id") || strings.Contains(r.Text, key) {
		t.Fatalf("secret save = %+v", r)
	}

	r = callMemoryTool(t, "memory_forget", map[string]any{"name": "go-test-cap", "revision": 1})
	if r.IsError || !strings.Contains(r.Text, `"deleted":true`) {
		t.Fatalf("forget = %+v", r)
	}
	if r = callMemoryTool(t, "memory_read", map[string]any{"name": "go-test-cap"}); !r.IsError || !strings.Contains(r.Text, "memory_not_found") {
		t.Fatalf("read after forget = %+v", r)
	}
}

// A session the Agent cannot find still gets the user scope, with the author recorded as
// unknown, instead of a refusal.
func TestMemoryToolsWithoutAnOwner(t *testing.T) {
	memoryTestEnv(t)
	mcpSourceSession = ""
	t.Setenv("AF_SESSION_NAME", "")
	r := callMemoryTool(t, "memory_save", map[string]any{"name": "pref", "description": "d", "body": "b", "scope": "project"})
	if !r.IsError || !strings.Contains(r.Text, "memory_no_project") {
		t.Fatalf("project save without an owner = %+v", r)
	}
	r = callMemoryTool(t, "memory_save", map[string]any{"name": "pref", "description": "d", "body": "b"})
	if r.IsError {
		t.Fatalf("user save without an owner = %+v", r)
	}
	r = callMemoryTool(t, "memory_read", map[string]any{"name": "pref"})
	if r.IsError || !strings.Contains(r.Text, "author: unknown (session unknown)") {
		t.Fatalf("read = %+v", r)
	}
}

// A store holding only withheld files is not reported as empty.
func TestMemoryIndexReportsWithheld(t *testing.T) {
	out := mcpMemoryFormatIndex(`{"project":{"display":"p"},"entries":[],"withheld":2}`)
	if !strings.Contains(out, "2 memory file(s) are withheld") || strings.Contains(out, "No memories yet") {
		t.Fatalf("index = %q", out)
	}
}

// The assistant's snapshot tool reads claude's and codex's memory history only: it asks the
// Agent for the diff without the AF memory under af/ (ADR 0108), whatever the switch says.
func TestGetMemorySnapshotAsksForNativeDiff(t *testing.T) {
	var diffQuery, listQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agents/memory/diff":
			diffQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"diff":""}`))
			return
		case "/agents/memory/snapshots":
			listQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"snapshots":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"rev":"abc"}`))
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_ADDR", u.Host)
	withMCPFlags(t, false, false, false)
	if resp := mcpCall(t, "get_memory_snapshot", map[string]any{"rev": "abc"}); mcpIsError(t, resp) {
		t.Fatalf("get_memory_snapshot: %s", resp)
	}
	q, _ := url.ParseQuery(diffQuery)
	if q.Get("native") != "1" {
		t.Fatalf("diff query = %q, want native=1", diffQuery)
	}
	if resp := mcpCall(t, "list_memory_snapshots", map[string]any{}); mcpIsError(t, resp) {
		t.Fatalf("list_memory_snapshots: %s", resp)
	}
	if q, _ := url.ParseQuery(listQuery); q.Get("native") != "1" {
		t.Fatalf("snapshots query = %q, want native=1", listQuery)
	}
}

// The budget is measured on the Agent's line text, so the formatter must print the same bytes
// (memoryx pins the same literal in agent_memory_index_test.go).
func TestMemoryIndexLineFormatIsPinned(t *testing.T) {
	out := mcpMemoryFormatIndex(`{"project":{"display":"p"},"entries":[{"name":"n","scope":"user","description":"d","type":"feedback","kinds":["claude"],"updated":"2026-10-04T09:00:00Z"}]}`)
	if want := "- [user] n — d (feedback; for claude; 2026-10-04)\n"; !strings.Contains(out, want) {
		t.Fatalf("index = %q, want a line %q", out, want)
	}
}

func TestMemoryIndexRendersTailAndOmittedAsTheAgentCutThem(t *testing.T) {
	out := mcpMemoryFormatIndex(`{"project":{"display":"p"},"entries":[{"name":"a","scope":"user","description":"d"}],"more":["adr-{1,2}","solo"],"omitted":7,"truncated":true}`)
	for _, want := range []string{"names only", "prefix", "memory_search", "adr-{1,2} solo\n", "and 7 more (use memory_search)\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	if strings.Contains(out, "truncated") {
		t.Errorf("the formatter must not invent its own truncation note: %q", out)
	}
	// Nothing cut: no tail, no count.
	if out := mcpMemoryFormatIndex(`{"project":{"display":"p"},"entries":[{"name":"a","scope":"user","description":"d"}]}`); strings.Contains(out, "names only") || strings.Contains(out, "more (use") {
		t.Errorf("unexpected tail: %q", out)
	}
}

// The Agent reserves agentMemIndexTailOverhead (256) of its 8 KiB tail budget for what this
// formatter adds; the real tail, header and count line included, must stay within 8 KiB.
func TestMemoryIndexTailWithinBudgetWithOverhead(t *testing.T) {
	names := make([]string, 0, 1000)
	size := 0
	for i := 0; size+len("n0000 ") <= 8192-256; i++ {
		n := fmt.Sprintf("n%04d", i)
		names = append(names, n)
		size += len(n) + 1
	}
	more, _ := json.Marshal(names)
	out := mcpMemoryFormatIndex(`{"project":{"display":"p"},"entries":[],"more":` + string(more) + `,"omitted":99999}`)
	i := strings.Index(out, "Not listed above")
	if i < 0 {
		t.Fatalf("no tail: %q", out)
	}
	if tail := len(out) - i; tail > 8192 {
		t.Fatalf("tail = %d bytes, over 8192", tail)
	}
}

// Nothing described does not mean nothing known: names and the count still render, alongside withheld.
func TestMemoryIndexWithNoDescribedLinesStillRendersTail(t *testing.T) {
	out := mcpMemoryFormatIndex(`{"project":{"display":"p"},"entries":[],"more":["real-memory"],"omitted":3,"withheld":1,"truncated":true}`)
	for _, want := range []string{"real-memory", "and 3 more", "1 memory file(s) are withheld"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	if strings.Contains(out, "No memories yet") {
		t.Errorf("claimed an empty store: %q", out)
	}
}
