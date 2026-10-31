package mcpx

import (
	"net/http"
	"strings"
	"testing"
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
