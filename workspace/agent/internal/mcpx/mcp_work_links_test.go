package mcpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Naming a branch after an item in the caller's own working copy links the caller to it as
// working; the name is returned whatever the link answers. (A name for another working copy
// links nothing: TestBranchNameForwardsExplicitInput pins that it makes one request.)
func TestBranchNameWithAnItemLinksTheCallerAsWorking(t *testing.T) {
	seen := branchTestEnv(t, http.StatusOK, `{"name":"fix/PROJ-7-crash"}`)

	got := callBranchName(t, map[string]any{"item": map[string]any{"provider": "jira", "key": "PROJ-7", "title": "Crash"}})
	if got.IsError || got.Text != `{"name":"fix/PROJ-7-crash"}` {
		t.Fatalf("branch_name = %+v, want the resolver's answer", got)
	}
	want := `POST /sessions/owner01/links {"provider":"jira","key":"PROJ-7","role":"working","title":"Crash"}`
	if len(*seen) != 2 || (*seen)[1] != want {
		t.Fatalf("Agent saw %q, want the branch-name call then %q", *seen, want)
	}
}

func TestLinkWorkItemLinksTheCallingSessionOnly(t *testing.T) {
	seen := branchTestEnv(t, http.StatusOK, `{"name":"owner01","changed":true,"links":[]}`)

	got := callSelfToolResult(t, mcpToolLinkWorkItem, map[string]any{"provider": "github", "key": "#45", "role": "reviewing"})
	if got.IsError {
		t.Fatalf("link_work_item failed: %s", got.Text)
	}
	want := `POST /sessions/owner01/links {"provider":"github","key":"#45","role":"reviewing"}`
	if len(*seen) != 1 || (*seen)[0] != want {
		t.Fatalf("Agent saw %q, want %q", *seen, want)
	}
}

func TestLinkWorkItemOnAnOlderAgentSaysNothingWasRecorded(t *testing.T) {
	branchTestEnv(t, http.StatusNotFound, "404 page not found")

	got := callSelfToolResult(t, mcpToolLinkWorkItem, map[string]any{"provider": "jira", "key": "PROJ-1"})
	if !got.IsError || !strings.Contains(got.Text, "predates") {
		t.Fatalf("result = %+v, want the older-Agent refusal", got)
	}
}

func callSelfToolResult(t *testing.T, name string, args map[string]any) branchToolResult {
	t.Helper()
	return parseToolResult(t, callSelfTool(t, name, args))
}

// create_session's work_item is normalized into the inbox's key shape, refused when it cannot
// be, and part of the idempotency key: the same launch for another ticket is another child.
func TestCreateSessionWorkItem(t *testing.T) {
	withFleetSpawn(t, true)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AF_SESSIONS_DIR", t.TempDir())
	session.WriteMeta(session.Meta{Name: "parent1", Kind: session.KindClaude, Origin: session.OriginUser})

	var body map[string]any
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		body = nil
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"name":"slot09"}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	t.Setenv("AGENT_ADDR", u.Host)
	call := func(args map[string]any) string {
		t.Helper()
		a, _ := json.Marshal(args)
		params, _ := json.Marshal(map[string]any{"name": "create_session", "arguments": json.RawMessage(a)})
		return string(mcpStdioCall(mcpReq{ID: json.RawMessage(`1`), Params: params}))
	}
	base := map[string]any{"dir": "/repos/app", "initial_prompt": "fix it"}
	with := func(wi map[string]any) map[string]any {
		out := map[string]any{"work_item": wi}
		for k, v := range base {
			out[k] = v
		}
		return out
	}

	call(base)
	plainKey, _ := body["idempotency_key"].(string)
	if _, sent := body["work_item"]; sent {
		t.Fatalf("work_item sent although none was given: %v", body["work_item"])
	}
	if resp := call(with(map[string]any{"provider": "Jira", "key": "proj-1", "title": "Login"})); strings.Contains(resp, `"isError":true`) {
		t.Fatalf("create with a Jira item failed: %s", resp)
	}
	if wi, _ := body["work_item"].(map[string]any); wi["provider"] != "jira" || wi["key"] != "PROJ-1" {
		t.Fatalf("work_item = %v, want jira PROJ-1", body["work_item"])
	}
	jiraKey, _ := body["idempotency_key"].(string)
	call(with(map[string]any{"provider": "jira", "key": "PROJ-2"}))
	if k, _ := body["idempotency_key"].(string); jiraKey == plainKey || k == jiraKey {
		t.Fatalf("idempotency keys plain=%s PROJ-1=%s PROJ-2=%s: a work item must change it", plainKey, jiraKey, k)
	}
	before := hits
	for _, bad := range []map[string]any{
		{"provider": "github", "key": "45"}, // a bare number: the child's repository is not settled
		{"provider": "gitlab", "key": "o/r#1"},
		{"provider": "", "key": "o/r#1"},
	} {
		if resp := call(with(bad)); !strings.Contains(resp, `"isError":true`) || !strings.Contains(resp, "work_item") {
			t.Errorf("work_item %v was not refused: %s", bad, resp)
		}
	}
	if hits != before {
		t.Fatalf("a refused work_item still reached the Agent (%d calls)", hits-before)
	}
}

func TestBranchNameInfersJiraFromTheKeyWhenProviderIsOmitted(t *testing.T) {
	seen := branchTestEnv(t, http.StatusOK, `{"name":"feature/PROJ-7-x"}`)
	callBranchName(t, map[string]any{"item": map[string]any{"key": "PROJ-7"}})
	if len(*seen) != 2 || !strings.Contains((*seen)[1], `"provider":"jira"`) {
		t.Fatalf("Agent saw %q, want the link as jira", *seen)
	}
	*seen = nil
	callBranchName(t, map[string]any{"item": map[string]any{"key": "o/r#3"}})
	if len(*seen) != 2 || !strings.Contains((*seen)[1], `"provider":"github"`) {
		t.Fatalf("Agent saw %q, want the link as github", *seen)
	}
}
