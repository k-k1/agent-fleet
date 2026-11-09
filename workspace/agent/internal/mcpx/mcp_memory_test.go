package mcpx

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memoryx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

var memoryToolNames = []string{"memory_index", "memory_search", "memory_read", "memory_save", "memory_patch", "memory_feedback", "memory_forget"}

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
	isolateAdvertised(t)
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
	mux.HandleFunc("POST /agents/memory/entries/patch", memoryx.HandleAgentMemoryPatch)
	mux.HandleFunc("POST /agents/memory/entries/feedback", memoryx.HandleAgentMemoryFeedback)
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
	r = callMemoryTool(t, "memory_search", map[string]any{"query": "busy nosuchword"})
	if r.IsError || !strings.Contains(r.Text, "No memory contains every word") || !strings.Contains(r.Text, "go-test-cap") || !strings.Contains(r.Text, "[matched: busy]") {
		t.Fatalf("fallback search = %+v", r)
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

// memory_patch end to end: the edits array and the optional description reach the Agent, an
// omitted description keeps the stored one, and refusals come back as the Agent worded them.
func TestMemoryPatchTool(t *testing.T) {
	memoryTestEnv(t)
	r := callMemoryTool(t, "memory_save", map[string]any{"name": "go-test-cap", "description": "cap parallelism", "type": "feedback", "body": "Run go test with -p 2.\nSecond line."})
	if r.IsError {
		t.Fatalf("save = %+v", r)
	}
	r = callMemoryTool(t, "memory_patch", map[string]any{"name": "go-test-cap", "revision": 1,
		"edits": []map[string]string{{"old": "-p 2", "new": "-p 1"}}})
	if r.IsError || !strings.Contains(r.Text, `"revision":2`) {
		t.Fatalf("patch = %+v", r)
	}
	r = callMemoryTool(t, "memory_read", map[string]any{"name": "go-test-cap"})
	if !strings.Contains(r.Text, "Run go test with -p 1.\nSecond line.") || !strings.Contains(r.Text, "description: cap parallelism") {
		t.Fatalf("read after patch = %+v", r)
	}
	r = callMemoryTool(t, "memory_patch", map[string]any{"name": "go-test-cap", "revision": 2, "description": "new words"})
	if r.IsError || !strings.Contains(r.Text, `"revision":3`) {
		t.Fatalf("description-only patch = %+v", r)
	}
	r = callMemoryTool(t, "memory_patch", map[string]any{"name": "go-test-cap", "revision": 1, "edits": []map[string]string{{"old": "Second", "new": "Third"}}})
	if !r.IsError || !strings.Contains(r.Text, "memory_conflict") {
		t.Fatalf("stale patch = %+v", r)
	}
	r = callMemoryTool(t, "memory_patch", map[string]any{"name": "absent", "revision": 1, "edits": []map[string]string{{"old": "a", "new": "b"}}})
	if !r.IsError || !strings.Contains(r.Text, "memory_not_found") {
		t.Fatalf("missing patch = %+v", r)
	}
	key := "AKIA" + "QWERTYUIOPASDFGH"
	r = callMemoryTool(t, "memory_patch", map[string]any{"name": "go-test-cap", "revision": 3, "edits": []map[string]string{{"old": "Third", "new": "use " + key}}})
	if !r.IsError || strings.Contains(r.Text, key) {
		t.Fatalf("secret patch = %+v", r)
	}
	// An omitted `new` is refused (not turned into a deletion); an explicit "" deletes.
	r = callMemoryTool(t, "memory_patch", map[string]any{"name": "go-test-cap", "revision": 3, "edits": []map[string]any{{"old": "Run"}}})
	if !r.IsError || !strings.Contains(r.Text, "new is required") {
		t.Fatalf("patch without new = %+v", r)
	}
	// A given empty description is refused even next to valid edits: nothing is written.
	r = callMemoryTool(t, "memory_patch", map[string]any{"name": "go-test-cap", "revision": 3, "description": "", "edits": []map[string]string{{"old": "Run", "new": "Execute"}}})
	if !r.IsError || !strings.Contains(r.Text, "description must not be empty") {
		t.Fatalf("empty description patch = %+v", r)
	}
	if r = callMemoryTool(t, "memory_read", map[string]any{"name": "go-test-cap"}); !strings.Contains(r.Text, "revision: 3") || !strings.Contains(r.Text, "Run go test") {
		t.Fatalf("a refused patch wrote: %+v", r)
	}
	r = callMemoryTool(t, "memory_patch", map[string]any{"name": "go-test-cap", "revision": 3, "edits": []map[string]string{{"old": "Run ", "new": ""}}})
	if r.IsError || !strings.Contains(r.Text, `"revision":4`) {
		t.Fatalf("explicit empty new = %+v", r)
	}
	// The memory the new-name hint points at.
	r = callMemoryTool(t, "memory_save", map[string]any{"name": "go-test-cap-limit", "description": "d", "body": "b"})
	if r.IsError || !strings.Contains(r.Text, "prefer memory_patch on go-test-cap") {
		t.Fatalf("near-name save = %+v", r)
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

// The Agent reserves agentMemIndexTailOverhead (320) of its 8 KiB tail budget for what this
// formatter adds; the real tail, header and count line included, must stay within 8 KiB.
func TestMemoryIndexTailWithinBudgetWithOverhead(t *testing.T) {
	names := make([]string, 0, 1000)
	size := 0
	for i := 0; size+len("n0000 ") <= 8192-320; i++ {
		n := fmt.Sprintf("n%04d", i)
		names = append(names, n)
		size += len(n) + 1
	}
	more, _ := json.Marshal(names)
	out := mcpMemoryFormatIndex(`{"project":{"display":"p"},"entries":[],"more":` + string(more) + `,"omitted":99999,"pinnedOmitted":99999}`)
	if !strings.Contains(out, "99999 of these are pinned") {
		t.Fatalf("the pinned count is not printed: %q", out)
	}
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

// cursor has no local user layer for the 0042 distributor (ADR 0108 decision 5), so the guidance
// on when to call the tools lives in their descriptions; they must keep saying it.
func TestMemoryToolDescriptionsCarryTheWhenToCallGuidance(t *testing.T) {
	desc := map[string]string{}
	for _, tool := range mcpStdioMemoryTools() {
		desc[tool["name"].(string)], _ = tool["description"].(string)
	}
	if !strings.Contains(desc["memory_index"], "start work") {
		t.Errorf("memory_index no longer says when to call it: %q", desc["memory_index"])
	}
	if !strings.Contains(desc["memory_search"], "before re-deriving") {
		t.Errorf("memory_search no longer says when to call it: %q", desc["memory_search"])
	}
}

// The authoring guidance is said in the save answer's text and in the tool description, and a
// memory over it is still saved.
func TestMemorySaveReturnsGuidanceWarnings(t *testing.T) {
	memoryTestEnv(t)
	r := callMemoryTool(t, "memory_save", map[string]any{
		"name": "wordy", "description": strings.Repeat("d", 500), "body": strings.Repeat("b", 5000),
	})
	if r.IsError || !strings.Contains(r.Text, `"revision":1`) || !strings.Contains(r.Text, `"warnings":[`) ||
		!strings.Contains(r.Text, "Saved, with guidance:") || !strings.Contains(r.Text, "500 characters") || !strings.Contains(r.Text, "4096 bytes") {
		t.Fatalf("save = %+v", r)
	}
	r = callMemoryTool(t, "memory_save", map[string]any{"name": "tidy", "description": "short", "body": "b"})
	if r.IsError || strings.Contains(r.Text, "warnings") || strings.Contains(r.Text, "guidance") {
		t.Fatalf("a memory inside the guidance must carry no warning: %+v", r)
	}
	for _, tool := range mcpStdioMemoryTools() {
		if tool["name"] == "memory_save" {
			d := tool["description"].(string)
			if !strings.Contains(d, "150 characters") || !strings.Contains(d, "4,096 bytes") {
				t.Errorf("memory_save description lacks the authoring guidance: %s", d)
			}
		}
	}
}

// memory_read says that an omitted scope searches the project, then the user scope (#2775).
func TestMemoryReadDescriptionSaysOmittedScopeSearchesBoth(t *testing.T) {
	for _, tool := range mcpStdioMemoryTools() {
		if tool["name"] == "memory_read" {
			if d := tool["description"].(string); !strings.Contains(d, "Omit scope when unsure: the project is searched first, then the user scope") {
				t.Errorf("memory_read description: %s", d)
			}
			return
		}
	}
	t.Fatal("no memory_read tool")
}

// A suspect memory carries its reasons on every line; memoryx pins the same literal
// (TestVisibilityIndexAndInjection) because the index budget is measured on it.
func TestMemoryIndexLineCarriesReasons(t *testing.T) {
	out := mcpMemoryFormatIndex(`{"project":{"display":"p"},"entries":[{"name":"sus","scope":"project","description":"d","updated":"2026-11-08T00:00:00Z","reasons":[{"by":"lint","rule":"path-missing","note":"gone.md is not on main","revision":1}]}]}`)
	if want := "- [project] sus — [suspect: lint path-missing: gone.md is not on main] d (2026-11-08)\n"; !strings.Contains(out, want) {
		t.Fatalf("index = %q, want a line %q", out, want)
	}
	hits := mcpMemoryFormatSearch(`{"hits":[{"name":"sus","scope":"project","description":"d","reasons":[{"by":"feedback","rule":"stale","note":""}],"snippets":[]}]}`)
	if !strings.Contains(hits, "- [project] sus — [suspect: feedback stale] d") {
		t.Fatalf("search = %q", hits)
	}
	// A memory with no reasons prints as it always did.
	if out := mcpMemoryFormatIndex(`{"project":{"display":"p"},"entries":[{"name":"n","scope":"user","description":"d"}]}`); !strings.Contains(out, "- [user] n — d\n") {
		t.Fatalf("plain line changed: %q", out)
	}
}

// An archived or superseded memory reads as a stub: why it is gone, never its text. A suspect one
// reads in full, led by its reasons.
func TestMemoryReadStubAndSuspect(t *testing.T) {
	stub := mcpMemoryFormatRead(`{"name":"old","scope":"project","revision":4,"status":"archived","supersededBy":{"memory":"new"},"stub":true,"reasons":[{"by":"member","rule":"wrong","note":"n"}]}`)
	for _, want := range []string{"name: old", "status: archived", "superseded by: memory new", "open doubt: member wrong: n", "text is not shown"} {
		if !strings.Contains(stub, want) {
			t.Errorf("stub lacks %q:\n%s", want, stub)
		}
	}
	doc := mcpMemoryFormatRead(`{"name":"old","scope":"project","revision":4,"status":"archived","stub":true,"supersededBy":{"document":{"repo":"r","path":"docs/a.md","commit":"0123abc"}}}`)
	if !strings.Contains(doc, "superseded by: document r:docs/a.md (0123abc)") {
		t.Errorf("document target: %s", doc)
	}
	sus := mcpMemoryFormatRead(`{"name":"n","scope":"user","revision":2,"description":"d","body":"the text","kind":"lesson","anchors":["a.go","b.go"],"reasons":[{"by":"lint","rule":"path-missing","note":"x"}]}`)
	if !strings.HasPrefix(sus, "This memory is suspect: [suspect: lint path-missing: x]") || !strings.Contains(sus, "the text") ||
		!strings.Contains(sus, "kind: lesson") || !strings.Contains(sus, "anchors: a.go, b.go") {
		t.Errorf("suspect read:\n%s", sus)
	}
}

// kind and anchors reach the store from memory_save, a save that names neither keeps them, and a
// bad value is refused with the field named.
func TestMemorySaveKindAndAnchors(t *testing.T) {
	memoryTestEnv(t)
	r := callMemoryTool(t, "memory_save", map[string]any{
		"name": "k1", "description": "d", "body": "b", "kind": "procedure", "anchors": []string{"docs/a.md", "go test ./..."},
	})
	if r.IsError {
		t.Fatalf("save = %+v", r)
	}
	r = callMemoryTool(t, "memory_read", map[string]any{"name": "k1"})
	if !strings.Contains(r.Text, "kind: procedure") || !strings.Contains(r.Text, "anchors: docs/a.md, go test ./...") {
		t.Fatalf("read = %s", r.Text)
	}
	if r = callMemoryTool(t, "memory_save", map[string]any{"name": "k1", "description": "d", "body": "b2", "revision": 1}); r.IsError {
		t.Fatalf("resave = %+v", r)
	}
	if r = callMemoryTool(t, "memory_read", map[string]any{"name": "k1"}); !strings.Contains(r.Text, "kind: procedure") || !strings.Contains(r.Text, "anchors: docs/a.md") {
		t.Fatalf("a save that names neither lost them: %s", r.Text)
	}
	if r = callMemoryTool(t, "memory_save", map[string]any{"name": "k1", "description": "d", "body": "b3", "revision": 2, "kind": "mood"}); !r.IsError || !strings.Contains(r.Text, "kind must be one of") {
		t.Fatalf("bad kind = %+v", r)
	}
}

// memory_feedback end to end: a stale report on the current revision marks the memory (the read
// shows the suspect reason), one on an older revision is an observation only, and a confirmation
// changes nothing visible.
func TestMemoryFeedbackTool(t *testing.T) {
	memoryTestEnv(t)
	r := callMemoryTool(t, "memory_save", map[string]any{"name": "fb", "description": "d", "type": "feedback", "body": "v1"})
	if r.IsError {
		t.Fatalf("save = %+v", r)
	}
	r = callMemoryTool(t, "memory_save", map[string]any{"name": "fb", "description": "d", "type": "feedback", "body": "v2", "revision": 1})
	if r.IsError {
		t.Fatalf("save v2 = %+v", r)
	}
	r = callMemoryTool(t, "memory_feedback", map[string]any{"name": "fb", "revision": 1, "verdict": "stale", "note": "v1 text"})
	if r.IsError || !strings.Contains(r.Text, `"outcome":"observation"`) {
		t.Fatalf("older report = %+v", r)
	}
	if r = callMemoryTool(t, "memory_read", map[string]any{"name": "fb"}); strings.Contains(r.Text, "suspect") {
		t.Fatalf("an observation marked the memory: %+v", r)
	}
	r = callMemoryTool(t, "memory_feedback", map[string]any{"name": "fb", "revision": 2, "verdict": "wrong", "note": "does not hold"})
	if r.IsError || !strings.Contains(r.Text, `"outcome":"counted"`) {
		t.Fatalf("current report = %+v", r)
	}
	if r = callMemoryTool(t, "memory_read", map[string]any{"name": "fb"}); !strings.Contains(r.Text, "suspect") || !strings.Contains(r.Text, "does not hold") {
		t.Fatalf("read after a report = %+v", r)
	}
	r = callMemoryTool(t, "memory_feedback", map[string]any{"name": "fb", "revision": 2, "verdict": "stale"})
	if !r.IsError {
		t.Fatalf("a duplicate or noteless report was accepted: %+v", r)
	}
}

// The Control Plane's external memory_save advertises kind and anchors exactly as this tool does;
// its tool golden is the other half of the contract (control-plane/internal/mcpsrv/testdata).
func TestMemorySaveKindAnchorsMatchControlPlane(t *testing.T) {
	raw, err := os.ReadFile("../../../../control-plane/internal/mcpsrv/testdata/mcp_tools/list.json")
	if err != nil {
		t.Fatal(err)
	}
	var cp struct {
		Tools map[string]struct {
			InputSchema struct {
				Properties map[string]any `json:"properties"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &cp); err != nil {
		t.Fatal(err)
	}
	cpProps := cp.Tools["memory_save"].InputSchema.Properties
	var agent map[string]any
	for _, ts := range agentMemoryTools() {
		if ts.name == mcpToolMemorySave {
			agent = ts.session()["inputSchema"].(map[string]any)["properties"].(map[string]any)
		}
	}
	if agent == nil {
		t.Fatal("agent memory_save not found")
	}
	for _, f := range []string{"kind", "anchors"} {
		want, _ := json.Marshal(agent[f])
		got, _ := json.Marshal(cpProps[f])
		if string(want) != string(got) {
			t.Errorf("%s differs:\n agent: %s\n cp:    %s", f, want, got)
		}
	}
	for _, f := range []string{"status", "superseded_by", "invalid_at", "reasons"} {
		if _, ok := cpProps[f]; ok {
			t.Errorf("CP memory_save advertises lifecycle field %s", f)
		}
		if _, ok := agent[f]; ok {
			t.Errorf("agent memory_save advertises lifecycle field %s", f)
		}
	}
}
