package memoryx

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func projectDirExists(id string) bool {
	_, err := os.Lstat(filepath.Join(agentMemDir(), "projects", id))
	return err == nil
}

// A save naming a repository that is cloned in the workspace but holds no memory starts its
// project from the working copy, recording what a session's first save would record, and a
// worktree of that clone maps to the same project.
func TestAgentMemoryExternalStartsProjectFromWorkingCopy(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	dir := remoteRepo(t, home, "fresh", "git@github.com:k-k1/fresh.git")
	want := agentMemProjectFor(dir)
	if want == nil || projectDirExists(want.ID) {
		t.Fatalf("setup: %+v", want)
	}
	externalMode(t, ExternalMemoryWrite)

	code, out := extSave(t, map[string]any{"scope": "project", "repository": "https://github.com/k-k1/fresh", "name": "first"})
	if code != 200 || !strings.Contains(out, `"project":"`+want.ID+`"`) {
		t.Fatalf("save: %d %s", code, out)
	}
	var got agentMemProject
	if err := json.Unmarshal([]byte(projectJSON(t, want.ID)), &got); err != nil || got != *want {
		t.Fatalf("project record %+v (%v), want %+v", got, err, *want)
	}
	e, err := agentMemRead(agentMemCaller{Session: agentMemUnknown, Kind: "x", Project: want}, "project", "first")
	if err != nil || e.AuthorKind != agentMemExternalKind {
		t.Fatalf("read: %+v %v", e, err)
	}

	// A second save and a worktree of the clone land in the same project; nothing else appears.
	remoteGit(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--allow-empty", "-m", "x", "--quiet")
	wt := filepath.Join(home, "repos", "fresh-wt")
	remoteGit(t, dir, "worktree", "add", "--quiet", "-b", "wt", wt)
	if p := agentMemProjectFor(wt); p == nil || p.ID != want.ID {
		t.Fatalf("worktree project %+v", p)
	}
	if code, out = extSave(t, map[string]any{"scope": "project", "repository": "https://github.com/k-k1/fresh", "name": "second"}); code != 200 {
		t.Fatalf("second save: %d %s", code, out)
	}
	ents, _ := os.ReadDir(filepath.Join(agentMemDir(), "projects"))
	if len(ents) != 1 {
		t.Fatalf("projects after two saves and a worktree: %d", len(ents))
	}
}

// Read-only mode refuses a write before anything is created, and reads of a project without
// memory answer empty rather than create it.
func TestAgentMemoryExternalReadOnlyStartsNothing(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	want := agentMemProjectFor(remoteRepo(t, home, "fresh", "https://github.com/k-k1/fresh.git"))
	externalMode(t, ExternalMemoryRead)
	if code, out := extSave(t, map[string]any{"scope": "project", "repository": "https://github.com/k-k1/fresh"}); code != http.StatusForbidden {
		t.Fatalf("read-only save: %d %s", code, out)
	}
	code, out := memberDo(t, http.MethodGet, extBase+"entries?repository="+url.QueryEscape("https://github.com/k-k1/fresh"), nil)
	if code != 200 {
		t.Fatalf("index of a project without memory: %d %s", code, out)
	}
	if projectDirExists(want.ID) {
		t.Fatal("a refused write or a read created the project")
	}
}

// No clone, two clones, and a clone that already holds memory.
func TestAgentMemoryExternalStartRefusalsAndPrecedence(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	externalMode(t, ExternalMemoryWrite)

	code, out := extSave(t, map[string]any{"scope": "project", "repository": "https://github.com/k-k1/absent"})
	if code != 404 || !strings.Contains(out, "cloned in the workspace") {
		t.Errorf("no clone: %d %s", code, out)
	}

	a := agentMemProjectFor(remoteRepo(t, home, "twin-a", "https://github.com/k-k1/twin.git"))
	b := agentMemProjectFor(remoteRepo(t, home, "twin-b", "git@github.com:k-k1/twin.git"))
	code, out = extSave(t, map[string]any{"scope": "project", "repository": "https://github.com/k-k1/twin"})
	if code != http.StatusConflict || !strings.Contains(out, a.ID) || !strings.Contains(out, b.ID) {
		t.Errorf("two clones: %d %s", code, out)
	}
	if projectDirExists(a.ID) || projectDirExists(b.ID) {
		t.Error("an ambiguous save created a project")
	}
	// The ids memory_projects lists resolve the ambiguity, and a mismatch with repository is refused.
	code, out = extSave(t, map[string]any{"scope": "project", "project": b.ID, "name": "pick"})
	if code != 200 || !strings.Contains(out, `"project":"`+b.ID+`"`) {
		t.Errorf("by id: %d %s", code, out)
	}
	code, pb := memberDo(t, http.MethodGet, extBase+"projects", nil)
	if code != 200 || !strings.Contains(string(pb), a.ID) || !strings.Contains(string(pb), `"count":0`) {
		t.Errorf("projects list should show the memory-less clone: %d %s", code, pb)
	}

	// Now b holds memory: it wins over a, the live one with the same remote.
	code, out = extSave(t, map[string]any{"scope": "project", "repository": "https://github.com/k-k1/twin", "name": "again"})
	if code != 200 || !strings.Contains(out, `"project":"`+b.ID+`"`) || projectDirExists(a.ID) {
		t.Errorf("memory wins: %d %s", code, out)
	}
	if code, out = extSave(t, map[string]any{"scope": "project", "project": "nosuch-0123456789ab", "name": "x"}); code != 404 {
		t.Errorf("unknown id: %d %s", code, out)
	}
	if code, out = extSave(t, map[string]any{"scope": "project", "project": a.ID, "repository": "https://github.com/k-k1/absent"}); code != 404 {
		t.Errorf("disagreeing: %d %s", code, out)
	}
}

// Saves racing to start one project leave one intact record and every memory.
func TestAgentMemoryExternalStartRace(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	want := agentMemProjectFor(remoteRepo(t, home, "fresh", "https://github.com/k-k1/fresh.git"))
	externalMode(t, ExternalMemoryWrite)
	var wg sync.WaitGroup
	codes := make([]int, 6)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, _ := extSave(t, map[string]any{"scope": "project", "repository": "https://github.com/k-k1/fresh", "name": "m" + string(rune('a'+i))})
			codes[i] = c
		}()
	}
	wg.Wait()
	for i, c := range codes {
		if c != 200 {
			t.Errorf("save %d: %d", i, c)
		}
	}
	var got agentMemProject
	if err := json.Unmarshal([]byte(projectJSON(t, want.ID)), &got); err != nil || got != *want {
		t.Fatalf("record %+v %v", got, err)
	}
	if es, _, err := agentMemLoadDir(agentMemScopeProject, "projects/"+want.ID); err != nil || len(es) != len(codes) {
		t.Fatalf("memories: %d %v", len(es), err)
	}
}

// A save by project id that fails after creating the project directory (a stale revision) must not
// leave a project the retry saves into without its record.
func TestAgentMemoryExternalRetryAfterRefusedStartKeepsRecord(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	want := agentMemProjectFor(remoteRepo(t, home, "fresh", "https://github.com/k-k1/fresh.git"))
	externalMode(t, ExternalMemoryWrite)
	if code, out := extSave(t, map[string]any{"scope": "project", "project": want.ID, "revision": 7}); code == 200 {
		t.Fatalf("stale revision accepted: %s", out)
	}
	if code, out := extSave(t, map[string]any{"scope": "project", "project": want.ID}); code != 200 {
		t.Fatalf("retry: %d %s", code, out)
	}
	var got agentMemProject
	if err := json.Unmarshal([]byte(projectJSON(t, want.ID)), &got); err != nil || got != *want {
		t.Fatalf("record %+v %v, want %+v", got, err, *want)
	}
	if out, err := memoryGitRun("show", "--stat", "--format=", "HEAD"); err != nil || !strings.Contains(out, "project.json") || !strings.Contains(out, "fact.md") {
		t.Fatalf("commit should hold record and memory: %q %v", out, err)
	}
}
