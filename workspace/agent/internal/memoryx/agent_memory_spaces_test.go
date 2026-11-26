package memoryx

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/spacex"
)

// A session in a space gets the one shared project, two spaces get the same one, and a folder
// under ~/spaces without the marker gets none (#5221).
func TestAgentMemSpacesShareOneProject(t *testing.T) {
	home, clone, _ := agentMemTestEnv(t)
	a, err := spacex.Create("First")
	if err != nil {
		t.Fatal(err)
	}
	b, err := spacex.Create("Second")
	if err != nil {
		t.Fatal(err)
	}
	unmarked := filepath.Join(home, "spaces", "by-hand")
	if err := os.MkdirAll(unmarked, 0o755); err != nil {
		t.Fatal(err)
	}
	session.WriteMeta(session.Meta{Name: "claude-sp-a", Dir: a.Dir, Kind: "claude"})
	session.WriteMeta(session.Meta{Name: "codex-sp-b", Dir: b.Dir, Kind: "codex"})
	session.WriteMeta(session.Meta{Name: "shell-by-hand", Dir: unmarked, Kind: "shell"})

	ca, cb := agentMemCallerT(t, "claude-sp-a"), agentMemCallerT(t, "codex-sp-b")
	if ca.Project == nil || cb.Project == nil {
		t.Fatalf("a space session has no project: %+v / %+v", ca.Project, cb.Project)
	}
	if ca.Project.ID != cb.Project.ID || ca.Project.ID != agentMemSpacesProjectID {
		t.Fatalf("two spaces must share %q: %q vs %q", agentMemSpacesProjectID, ca.Project.ID, cb.Project.ID)
	}
	if ca.Project.Display != "Spaces" {
		t.Errorf("display = %q, want Spaces", ca.Project.Display)
	}
	if c := agentMemCallerT(t, "shell-by-hand"); c.Project != nil {
		t.Errorf("a folder under ~/spaces without the marker has a project: %+v", c.Project)
	}
	if ca.Project.ID == agentMemProjectFor(clone).ID {
		t.Errorf("the spaces project collides with a repository's")
	}

	// A save from one space is read, searched and indexed from the other.
	now := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	res, err := agentMemSave(ca, agentMemSaveReq{Name: "tone", Description: "how the member likes answers", Type: "feedback", Body: "short answers", Scope: agentMemScopeProject}, now)
	if err != nil {
		t.Fatalf("project save in a space: %v", err)
	}
	if res.Scope != agentMemScopeProject {
		t.Fatalf("save result = %+v", res)
	}
	got, err := agentMemRead(cb, agentMemScopeProject, "tone")
	if err != nil || got.Body != "short answers" {
		t.Fatalf("read from the other space = %+v, %v", got, err)
	}
	if hits, err := agentMemSearch(cb, "answers", 10); err != nil || len(hits) == 0 {
		t.Fatalf("search from the other space = %+v, %v", hits, err)
	}
	if idx, err := agentMemListIndex(cb, 0); err != nil || idx.Project == nil || idx.Project.ID != agentMemSpacesProjectID {
		t.Fatalf("index from the other space = %+v, %v", idx, err)
	}
	// A repository session does not see it.
	if _, err := agentMemRead(agentMemCallerT(t, "claude-main"), agentMemScopeProject, "tone"); err == nil {
		t.Errorf("a repository session read the spaces project's memory")
	}

	// The Console side: the record carries the display name, the project is live and never
	// orphaned, and the lint path agrees that there is no repository to check.
	mc, err := agentMemMemberCaller(agentMemScopeProject, agentMemSpacesProjectID)
	if err != nil || mc.Project == nil || mc.Project.Display != "Spaces" {
		t.Fatalf("member caller = %+v, %v", mc.Project, err)
	}
	if _, _, why := agentMemLintRepoFor(mc.Project); why != "project has no git repository" {
		t.Errorf("lint repo reason = %q", why)
	}
	rows, err := agentMemMemberProjects()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows.Projects {
		if r.Project.ID == agentMemSpacesProjectID {
			found = true
			if r.Orphaned || !r.Live || r.Project.Display != "Spaces" {
				t.Errorf("spaces row = %+v", r)
			}
		}
	}
	if !found {
		t.Errorf("the spaces project is not listed: %+v", rows.Projects)
	}
}

// Only a directory spacex calls a space maps to the project: not the spaces root, not a folder
// below a space's direct children, not the home directory.
func TestAgentMemSpacesProjectOnlyForSpaces(t *testing.T) {
	home, _, _ := agentMemTestEnv(t)
	sp, err := spacex.Create("Only")
	if err != nil {
		t.Fatal(err)
	}
	if p := agentMemProjectFor(sp.Dir); p == nil || p.ID != agentMemSpacesProjectID {
		t.Errorf("the space itself: %+v", p)
	}
	sub := filepath.Join(sp.Dir, "notes")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Join(home, "spaces"), sub, home, ""} {
		if p := agentMemProjectFor(dir); p != nil {
			t.Errorf("%q has a project: %+v", dir, p)
		}
	}
}
