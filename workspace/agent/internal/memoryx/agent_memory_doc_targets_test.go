package memoryx

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// docTargetSetup supersedes the memory release-steps by AGENTS.md's "Release" section, merged
// as one commit on the default branch, and answers the project id and that commit.
func docTargetSetup(t *testing.T) (clone, project, merge string) {
	t.Helper()
	clone, a, _ := promoteRepo(t)
	promoteSave(t, a, "release-steps", "procedure", "Run scripts/release.sh.", "scripts/release.sh")
	agentMemLintOrigin(t, clone, map[string]string{"AGENTS.md": "# Agents\n\n## Build\n\nRun make.\n\n## Release\n\nRun scripts/release.sh.\n"})
	merge = promoteHead(t, clone, "HEAD")
	code, out := supersedeDoc(t, agentMemSupersedeDocReq{Project: a.Project.ID, Name: "release-steps", Revision: 1, Path: "AGENTS.md",
		Heading: "Release", Commit: merge, BodySHA256: agentMemSHA256("Run scripts/release.sh.")})
	if code != http.StatusOK {
		t.Fatalf("supersede: %d %s", code, out)
	}
	return clone, a.Project.ID, merge
}

// docTargetLint reads the member's lint report of the project.
func docTargetLint(t *testing.T, project string) agentMemLintWire {
	t.Helper()
	code, b := memberDo(t, http.MethodGet, "/agents/memory/entries/lint?scope=project&project="+project, nil)
	var w agentMemLintWire
	if code != http.StatusOK || json.Unmarshal(b, &w) != nil {
		t.Fatalf("lint: %d %s", code, b)
	}
	return w
}

// docTargetOne answers the one document finding of the report, or fails.
func docTargetOne(t *testing.T, w agentMemLintWire, problem string) agentMemDocFinding {
	t.Helper()
	if w.Documents == nil || w.Documents.Checked != 1 || len(w.Documents.Findings) != 1 || w.Documents.Findings[0].Problem != problem {
		t.Fatalf("want one %q finding: %+v (skipped %v)", problem, w.Documents, w.Report.Skipped)
	}
	f := w.Documents.Findings[0]
	if f.Memory != "release-steps" {
		t.Fatalf("finding: %+v", f)
	}
	return f
}

func docTargetClean(t *testing.T, w agentMemLintWire) {
	t.Helper()
	if w.Documents == nil || w.Documents.Checked != 1 || len(w.Documents.Findings) != 0 {
		t.Fatalf("want a clean check of one target: %+v (skipped %v)", w.Documents, w.Report.Skipped)
	}
}

func docRepair(t *testing.T, route string, req agentMemDocRepairReq) (int, string) {
	t.Helper()
	b, _ := json.Marshal(req)
	code, out := markedDoJSON(t, "/agents/memory/entries/"+route, string(b))
	return code, string(out)
}

func docTargetGit(t *testing.T, clone string, args ...[]string) string {
	t.Helper()
	for _, a := range args {
		if err := agentMemGit(clone, a...); err != nil {
			t.Fatal(err)
		}
	}
	if err := agentMemGit(clone, "update-ref", "refs/remotes/origin/main", "HEAD"); err != nil {
		t.Fatal(err)
	}
	return promoteHead(t, clone, "HEAD")
}

// The issue's acceptance, first half: rename the file, then delete the heading. Each is reported
// with what the repair needs, and each re-point clears it. The memory stays superseded throughout
// and its body never comes back by itself.
func TestAgentMemoryDocumentTargetRenameAndHeadingLoss(t *testing.T) {
	clone, project, merge := docTargetSetup(t)
	docTargetClean(t, docTargetLint(t, project))

	rename := docTargetGit(t, clone, []string{"mv", "AGENTS.md", "docs/agents.md"}, []string{"commit", "--quiet", "-m", "move agents"})
	f := docTargetOne(t, docTargetLint(t, project), agentMemDocRenamed)
	if f.Path != "docs/agents.md" || !f.HeadingFound || f.Commit != rename || f.Target.Path != "AGENTS.md" || f.Target.Commit != merge || f.Revision != 2 {
		t.Fatalf("renamed: %+v", f)
	}
	// A repair from a report that is out of date is refused and changes nothing.
	if code, out := docRepair(t, "document-retarget", agentMemDocRepairReq{Project: project, Name: "release-steps", Revision: 1,
		Path: f.Path, Heading: "Release", Commit: f.Commit}); code != http.StatusConflict {
		t.Fatalf("stale revision: %d %s", code, out)
	}
	// A target that does not hold the heading is refused as at the supersede.
	if code, out := docRepair(t, "document-retarget", agentMemDocRepairReq{Project: project, Name: "release-steps", Revision: f.Revision,
		Path: f.Path, Heading: "Nope", Commit: f.Commit}); code != http.StatusConflict {
		t.Fatalf("missing heading: %d %s", code, out)
	}
	code, out := docRepair(t, "document-retarget", agentMemDocRepairReq{Project: project, Name: "release-steps", Revision: f.Revision,
		Path: f.Path, Heading: "Release", Commit: f.Commit[:10]})
	if code != http.StatusOK {
		t.Fatalf("retarget: %d %s", code, out)
	}
	e, err := agentMemMemberRead(agentMemScopeProject, project, "release-steps")
	want := agentMemDocTarget{Repo: "github.com/acme/demo", Path: "docs/agents.md", Heading: "Release", Commit: rename}
	if err != nil || e.SupersededBy == nil || e.SupersededBy.Document == nil || *e.SupersededBy.Document != want || !agentMemExcluded(e) ||
		e.Revision != 3 || e.Body != "Run scripts/release.sh." {
		t.Fatalf("after retarget: %+v %v", e, err)
	}
	docTargetClean(t, docTargetLint(t, project))

	// The heading goes; the file stays.
	agentMemLintOrigin(t, clone, map[string]string{"docs/agents.md": "# Agents\n\n## Build\n\nRun make, then scripts/release.sh.\n"})
	head := promoteHead(t, clone, "HEAD")
	f = docTargetOne(t, docTargetLint(t, project), agentMemDocHeading)
	if f.Target != want || f.Path != "" {
		t.Fatalf("heading: %+v", f)
	}
	if code, out := docRepair(t, "document-retarget", agentMemDocRepairReq{Project: project, Name: "release-steps", Revision: f.Revision,
		Path: "docs/agents.md", Heading: "Build", Commit: head}); code != http.StatusOK {
		t.Fatalf("retarget to another heading: %d %s", code, out)
	}
	docTargetClean(t, docTargetLint(t, project))

	// The re-point is a change of its own in the list.
	code, lb := memberDo(t, http.MethodGet, "/agents/memory/entries/changes?scope=project&project="+project, nil)
	var list struct {
		Changes []agentMemChangeView `json:"changes"`
	}
	if code != http.StatusOK || json.Unmarshal(lb, &list) != nil || len(list.Changes) < 2 || list.Changes[0].Op != agentMemOpRetarget {
		t.Fatalf("changes: %d %s", code, lb)
	}
	// Reverting the re-point puts the earlier target back.
	rb, _ := json.Marshal(map[string]any{"commit": list.Changes[0].Commit})
	if code, out := markedDoJSON(t, "/agents/memory/entries/revert", string(rb)); code != http.StatusOK {
		t.Fatalf("revert: %d %s", code, out)
	}
	if e, err := agentMemMemberRead(agentMemScopeProject, project, "release-steps"); err != nil || e.SupersededBy == nil ||
		e.SupersededBy.Document == nil || *e.SupersededBy.Document != want {
		t.Fatalf("after reverting the re-point: %+v %v", e, err)
	}
}

// The second half: revert the merge. It is reported with the reverting commit; a revert of that
// revert brings the target back, and reverting again reports it again. Reverting the supersede
// is the repair, and the memory is back in agents' view with its body.
func TestAgentMemoryDocumentTargetMergeRevert(t *testing.T) {
	clone, project, merge := docTargetSetup(t)
	revert := docTargetGit(t, clone, []string{"revert", "--no-edit", merge})
	f := docTargetOne(t, docTargetLint(t, project), agentMemDocReverted)
	if f.Commit != revert {
		t.Fatalf("reverted: %+v, want commit %s", f, revert)
	}
	reapply := docTargetGit(t, clone, []string{"revert", "--no-edit", revert})
	docTargetClean(t, docTargetLint(t, project))
	again := docTargetGit(t, clone, []string{"revert", "--no-edit", reapply})
	f = docTargetOne(t, docTargetLint(t, project), agentMemDocReverted)
	if f.Commit != revert {
		// The merge's own revert is the evidence; it is in effect again because its revert was undone.
		t.Fatalf("reverted again (%s): %+v", again, f)
	}

	// A restore from a report that is out of date is refused.
	if code, out := docRepair(t, "document-restore", agentMemDocRepairReq{Project: project, Name: "release-steps", Revision: f.Revision + 1}); code != http.StatusConflict {
		t.Fatalf("stale revision: %d %s", code, out)
	}
	code, out := docRepair(t, "document-restore", agentMemDocRepairReq{Project: project, Name: "release-steps", Revision: f.Revision})
	if code != http.StatusOK {
		t.Fatalf("restore: %d %s", code, out)
	}
	e, err := agentMemMemberRead(agentMemScopeProject, project, "release-steps")
	if err != nil || e.SupersededBy != nil || e.InvalidAt != "" || agentMemExcluded(e) || e.Body != "Run scripts/release.sh." || e.MemKind != "procedure" {
		t.Fatalf("after restore: %+v %v", e, err)
	}
	if w := docTargetLint(t, project); w.Documents != nil {
		t.Fatalf("no target is left to check: %+v", w.Documents)
	}
	if code, out := docRepair(t, "document-restore", agentMemDocRepairReq{Project: project, Name: "release-steps", Revision: e.Revision}); code != http.StatusConflict ||
		!strings.Contains(out, "not superseded by a document") {
		t.Fatalf("restore of a memory in view: %d %s", code, out)
	}
	// The restore is a change the member can revert: the memory is superseded again.
	code, lb := memberDo(t, http.MethodGet, "/agents/memory/entries/changes?scope=project&project="+project, nil)
	var list struct {
		Changes []agentMemChangeView `json:"changes"`
	}
	if code != http.StatusOK || json.Unmarshal(lb, &list) != nil || len(list.Changes) == 0 || list.Changes[0].Op != agentMemOpRestore {
		t.Fatalf("changes: %d %s", code, lb)
	}
	rb, _ := json.Marshal(map[string]any{"commit": list.Changes[0].Commit})
	if code, out := markedDoJSON(t, "/agents/memory/entries/revert", string(rb)); code != http.StatusOK {
		t.Fatalf("revert: %d %s", code, out)
	}
	if e, err := agentMemMemberRead(agentMemScopeProject, project, "release-steps"); err != nil || !agentMemExcluded(e) || e.SupersededBy.Document.Commit != merge {
		t.Fatalf("after reverting the restore: %+v %v", e, err)
	}
}

// A merge commit the default branch no longer holds (a rewritten history) and a deleted file
// with no rename are reported; a target in another repository is reported as uncheckable.
func TestAgentMemoryDocumentTargetUnreachableMissingOtherRepo(t *testing.T) {
	clone, project, merge := docTargetSetup(t)
	docTargetGit(t, clone, []string{"rm", "--quiet", "AGENTS.md"}, []string{"commit", "--quiet", "-m", "drop agents"})
	docTargetOne(t, docTargetLint(t, project), agentMemDocMissing)

	if err := agentMemGit(clone, "update-ref", "refs/remotes/origin/main", merge+"^"); err != nil {
		t.Fatal(err)
	}
	docTargetOne(t, docTargetLint(t, project), agentMemDocUnreachable)

	repo, _, why := agentMemLintRepoFor(agentMemProjectInfo(project))
	if repo == nil {
		t.Fatal(why)
	}
	f, bad, err := agentMemDocCheckOne(t.Context(), repo.(agentMemLintRepo), "github.com/acme/demo",
		agentMemDocTarget{Repo: "github.com/other/repo", Path: "AGENTS.md", Commit: merge}, nil, nil)
	if err != nil || !bad || f.Problem != agentMemDocOtherRepo {
		t.Fatalf("other repository: %+v %v %v", f, bad, err)
	}
}
