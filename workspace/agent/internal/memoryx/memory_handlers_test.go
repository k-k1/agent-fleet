package memoryx

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// Drives the docs/log/39 P1 REST through the real route table (buildMux). A registration
// missing on the CP side cannot be caught by a control-plane test, so this pins the Agent
// side's registration and response shapes.
func memoryAPIHandler(t *testing.T) http.Handler {
	t.Helper()
	memoryTestEnv(t)
	t.Setenv("AGENT_TOKEN", "smoke-token")
	return httpx.RequireToken(buildMux())
}

// TestMemoryRoutesRegistered stays in package main (memory_routes_test.go). It checks
// something only the real mux can show — that memory's ten routes do not swallow the
// existing `/agents/{kind}/models` — so moving it onto memoryx's own mux would leave it
// measuring nothing. The check on memoryx's copy is TestMemoryRoutesMatchAgentRouteTable in
// mux_test.go.

func TestMemoryAPIRoundTrip(t *testing.T) {
	h := memoryAPIHandler(t)

	// roots: claude is always present, codex only when a memories dir exists.
	w := smokeDo(t, h, "GET", "/agents/memory/roots", "smoke-token", "")
	if w.Code != http.StatusOK {
		t.Fatalf("roots: %d %s", w.Code, w.Body.String())
	}
	var roots struct {
		Roots []memoryRootView `json:"roots"`
		Auto  bool             `json:"auto"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &roots); err != nil {
		t.Fatalf("roots decode: %v (%s)", err, w.Body.String())
	}
	if len(roots.Roots) != 2 || !roots.Auto {
		t.Fatalf("roots = %+v auto=%v", roots.Roots, roots.Auto)
	}
	if roots.Roots[0].Kind != "claude" || !roots.Roots[0].Scopes || roots.Roots[0].Files != 3 {
		t.Errorf("claude root = %+v", roots.Roots[0])
	}
	if len(roots.Roots[0].Projects) != 1 || roots.Roots[0].Projects[0].Display != "demo" {
		t.Errorf("claude projects = %+v", roots.Roots[0].Projects)
	}
	if roots.Roots[1].Kind != "codex" || roots.Roots[1].Scopes || roots.Roots[1].Files != 2 {
		t.Errorf("codex root = %+v", roots.Roots[1])
	}
	// docs/log/39 P4: once memories are enabled and codex creates a workspace, the root
	// moves from inactive to active. The toggle state is carried on active roots too, so the
	// way back to turning it off does not disappear.
	if !roots.Roots[1].Toggleable {
		t.Errorf("the active codex root lost its enable toggle: %+v", roots.Roots[1])
	}

	// While there is not a single snapshot, the listing is empty and diff is a 404.
	if w := smokeDo(t, h, "GET", "/agents/memory/snapshots", "smoke-token", ""); w.Code != http.StatusOK ||
		w.Body.String() != "{\"snapshots\":[]}\n" {
		t.Fatalf("empty snapshots: %d %q", w.Code, w.Body.String())
	}
	if w := smokeDo(t, h, "GET", "/agents/memory/diff?to=HEAD", "smoke-token", ""); w.Code != http.StatusNotFound {
		t.Fatalf("diff before any snapshot: %d %s", w.Code, w.Body.String())
	}

	// A manual snapshot appears in the listing. The second one changed nothing, so
	// committed=false.
	var created memorySnapshotResult
	w = smokeDo(t, h, "POST", "/agents/memory/snapshots", "smoke-token", `{"trigger":"manual"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("manual snapshot: %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || !created.Committed || created.Rev == "" {
		t.Fatalf("manual snapshot result: %+v err=%v", created, err)
	}
	w = smokeDo(t, h, "POST", "/agents/memory/snapshots", "smoke-token", `{}`)
	var again memorySnapshotResult
	if err := json.Unmarshal(w.Body.Bytes(), &again); err != nil || again.Committed {
		t.Fatalf("unchanged manual snapshot committed: %+v err=%v", again, err)
	}

	var listed struct {
		Snapshots []memorySnapshotInfo `json:"snapshots"`
	}
	w = smokeDo(t, h, "GET", "/agents/memory/snapshots?limit=5", "smoke-token", "")
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil || len(listed.Snapshots) != 1 {
		t.Fatalf("snapshots: %s err=%v", w.Body.String(), err)
	}
	if listed.Snapshots[0].Trigger != memoryTriggerManual || listed.Snapshots[0].Rev != created.Rev {
		t.Fatalf("listed snapshot = %+v", listed.Snapshots[0])
	}

	// diff: the contents of the first snapshot can be read.
	var diff struct {
		Diff string `json:"diff"`
	}
	w = smokeDo(t, h, "GET", "/agents/memory/diff?to="+created.Rev, "smoke-token", "")
	if w.Code != http.StatusOK {
		t.Fatalf("diff: %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &diff); err != nil || diff.Diff == "" {
		t.Fatalf("diff payload: %s err=%v", w.Body.String(), err)
	}
}

// Validation of values that arrive from outside: a forged trigger, an invalid rev, and a
// path outside the declared roots are all refused.
func TestMemoryAPIRejectsBadInput(t *testing.T) {
	h := memoryAPIHandler(t)
	if w := smokeDo(t, h, "POST", "/agents/memory/snapshots", "smoke-token", `{"trigger":"restore"}`); w.Code != http.StatusBadRequest {
		t.Errorf("forged trigger: %d %s", w.Code, w.Body.String())
	}
	if w := smokeDo(t, h, "GET", "/agents/memory/snapshots?limit=-1", "smoke-token", ""); w.Code != http.StatusBadRequest {
		t.Errorf("negative limit: %d %s", w.Code, w.Body.String())
	}
	if w := smokeDo(t, h, "GET", "/agents/memory/snapshots?before=yesterday", "smoke-token", ""); w.Code != http.StatusBadRequest {
		t.Errorf("non-RFC3339 before: %d %s", w.Code, w.Body.String())
	}
	// From here on there is one snapshot in place, because without one the 404 comes first.
	if w := smokeDo(t, h, "POST", "/agents/memory/snapshots", "smoke-token", ""); w.Code != http.StatusOK {
		t.Fatalf("seed snapshot: %d %s", w.Code, w.Body.String())
	}
	for _, q := range []string{
		"?to=--upload-pack=evil",
		"?to=HEAD~1..HEAD",
		"?to=nope",
		"?at=not-a-time",
	} {
		if w := smokeDo(t, h, "GET", "/agents/memory/diff"+q, "smoke-token", ""); w.Code != http.StatusBadRequest {
			t.Errorf("diff%s: %d %s", q, w.Code, w.Body.String())
		}
	}
	for _, q := range []string{
		"?to=HEAD&path=../../etc",
		"?to=HEAD&path=/etc/passwd",
		"?to=HEAD&path=notaroot/x",
	} {
		if w := smokeDo(t, h, "GET", "/agents/memory/diff"+q, "smoke-token", ""); w.Code != http.StatusBadRequest {
			t.Errorf("diff%s: %d %s", q, w.Code, w.Body.String())
		}
	}
}
