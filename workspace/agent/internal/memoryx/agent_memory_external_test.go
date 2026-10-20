package memoryx

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func externalMode(t *testing.T, mode string) {
	t.Helper()
	old := ExternalMemoryMode
	ExternalMemoryMode = func() string { return mode }
	t.Cleanup(func() { ExternalMemoryMode = old })
}

const extBase = "/agents/memory/external/"

// Every external route refuses while the setting is off or unset, whatever the in-workspace
// switch says, and writes need the third state.
func TestAgentMemoryExternalGate(t *testing.T) {
	_, _, _ = agentMemTestEnv(t) // agentMemory switch ON
	reads := []string{"projects", "entries", "search?q=x", "read?name=x"}
	for _, mode := range []string{"", "off", "bogus"} {
		externalMode(t, mode)
		for _, p := range reads {
			code, b := memberDo(t, http.MethodGet, extBase+p, nil)
			if code != http.StatusForbidden || !strings.Contains(string(b), errCodeMemoryExternalOff) {
				t.Errorf("mode %q GET %s: %d %s", mode, p, code, b)
			}
		}
	}
	old := ExternalMemoryMode
	ExternalMemoryMode = nil
	if code, _ := memberDo(t, http.MethodGet, extBase+"projects", nil); code != http.StatusForbidden {
		t.Errorf("nil hook: %d", code)
	}
	ExternalMemoryMode = old

	externalMode(t, ExternalMemoryRead)
	if code, _ := memberDo(t, http.MethodGet, extBase+"entries", nil); code != http.StatusOK {
		t.Errorf("read mode index: %d", code)
	}
	for _, p := range []string{"entries", "forget"} {
		code, b := memberDo(t, http.MethodPost, extBase+p, map[string]any{"name": "x", "description": "d", "type": "project", "body": "b"})
		if code != http.StatusForbidden || !strings.Contains(string(b), "read-only") {
			t.Errorf("read mode POST %s: %d %s", p, code, b)
		}
	}
}

// The switch for in-workspace sessions does not open the external routes' counterpart the other
// way round either: the sessions' routes keep answering to agentMemory alone.
func TestAgentMemoryExternalIsSeparateFromAgentMemory(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	externalMode(t, ExternalMemoryWrite)
	AgentMemoryEnabled = func() bool { return false }
	if code, _ := memberDo(t, http.MethodGet, extBase+"entries", nil); code != http.StatusOK {
		t.Errorf("external with agentMemory off: %d", code)
	}
	if code, _ := memberDo(t, http.MethodGet, "/agents/memory/entries", nil); code != http.StatusForbidden {
		t.Errorf("session route with agentMemory off: %d", code)
	}
}

// An outside client lists projects without paths, names one explicitly, saves as author kind
// "external" with its (sanitised) client name in the commit, reads, and forgets.
func TestAgentMemoryExternalRoundTrip(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	agentMemSaveN(t, c, "seed", "project", time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC))
	externalMode(t, ExternalMemoryWrite)

	code, b := memberDo(t, http.MethodGet, extBase+"projects", nil)
	var pl struct {
		Projects []map[string]any `json:"projects"`
	}
	if code != 200 || json.Unmarshal(b, &pl) != nil || len(pl.Projects) != 1 || pl.Projects[0]["id"] != c.Project.ID {
		t.Fatalf("projects: %d %s", code, b)
	}
	if strings.Contains(string(b), c.Project.Root) || strings.Contains(string(b), "root") {
		t.Fatalf("projects leak the working copy path: %s", b)
	}

	code, b = memberDo(t, http.MethodPost, extBase+"entries", map[string]any{
		"project": c.Project.ID, "scope": "project", "name": "from-outside", "description": "d", "type": "project",
		"body": "hello", "client": "claude-code\n/1.2 <x>", "session": "claude-main",
	})
	var wr agentMemWriteResult
	if code != 200 || json.Unmarshal(b, &wr) != nil || wr.Revision != 1 {
		t.Fatalf("save: %d %s", code, b)
	}
	code, b = memberDo(t, http.MethodGet, extBase+"read?project="+c.Project.ID+"&scope=project&name=from-outside", nil)
	var e agentMemEntry
	if code != 200 || json.Unmarshal(b, &e) != nil || e.AuthorKind != "external" || e.AuthorSession != "unknown" || e.Body != "hello" {
		t.Fatalf("read: %d %s", code, b)
	}
	msg, err := memoryGitRun("log", "-1", "--format=%B")
	if err != nil || !strings.Contains(msg, "AF-Author-Kind: external") || !strings.Contains(msg, "AF-External-Client: claude-code/1.2 x") {
		t.Fatalf("commit message: %q %v", msg, err)
	}
	if strings.Contains(msg, "<") {
		t.Fatalf("client name not sanitised: %q", msg)
	}

	if code, b = memberDo(t, http.MethodGet, extBase+"search?project="+c.Project.ID+"&q=hello", nil); code != 200 || !strings.Contains(string(b), "from-outside") {
		t.Fatalf("search: %d %s", code, b)
	}
	// Without a project only the user scope is visible.
	if code, b = memberDo(t, http.MethodGet, extBase+"entries", nil); code != 200 || strings.Contains(string(b), "from-outside") {
		t.Fatalf("index without project: %d %s", code, b)
	}
	code, b = memberDo(t, http.MethodPost, extBase+"forget", map[string]any{
		"project": c.Project.ID, "scope": "project", "name": "from-outside", "revision": 1,
	})
	if code != 200 || !strings.Contains(string(b), `"deleted":true`) {
		t.Fatalf("forget: %d %s", code, b)
	}
}

// A project the store does not hold is refused, not created; the secret scan runs unchanged.
func TestAgentMemoryExternalRefusals(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	externalMode(t, ExternalMemoryWrite)
	code, _ := memberDo(t, http.MethodPost, extBase+"entries", map[string]any{
		"project": "nosuch", "scope": "project", "name": "x", "description": "d", "type": "project", "body": "b"})
	if code != http.StatusNotFound {
		t.Errorf("unknown project: %d", code)
	}
	code, b := memberDo(t, http.MethodPost, extBase+"entries", map[string]any{
		"scope": "user", "name": "leak", "description": "d", "type": "user", "body": "token ghp_" + strings.Repeat("a1B2", 9)})
	if code != http.StatusUnprocessableEntity || !strings.Contains(string(b), errCodeMemorySecretDetected) {
		t.Errorf("secret: %d %s", code, b)
	}
}

// A memory limited to other kinds is not readable by name from outside, and the refused read is not
// a use; one limited to "external" is.
func TestAgentMemoryExternalReadHonoursKinds(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	externalMode(t, ExternalMemoryRead)
	for name, kinds := range map[string][]string{"only-claude": {"claude"}, "for-outside": {"external"}} {
		if _, err := agentMemSave(c, agentMemSaveReq{Name: name, Description: "d", Type: "project", Body: "b", Kinds: kinds}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	q := extBase + "read?project=" + c.Project.ID + "&scope=project&name="
	if code, b := memberDo(t, http.MethodGet, q+"only-claude", nil); code != http.StatusNotFound || strings.Contains(string(b), `"body"`) {
		t.Fatalf("kind-limited read: %d %s", code, b)
	}
	if got := agentMemLoadUsage("projects/" + c.Project.ID)["only-claude"]; got != 0 {
		t.Fatalf("a refused read counted as a use: %d", got)
	}
	if code, _ := memberDo(t, http.MethodGet, q+"for-outside", nil); code != http.StatusOK {
		t.Fatalf("external-kind read: %d", code)
	}
}
