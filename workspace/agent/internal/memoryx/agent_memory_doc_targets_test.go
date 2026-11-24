package memoryx

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
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
		agentMemDocTarget{Repo: "github.com/other/repo", Path: "AGENTS.md", Commit: merge}, nil)
	if err != nil || !bad || f.Problem != agentMemDocOtherRepo {
		t.Fatalf("other repository: %+v %v %v", f, bad, err)
	}
}

// Only a sentence git revert writes counts: a commit that quotes it indented in prose, or names
// a longer id or other text after it, is no evidence. A revert -m of a real merge commit (", reversing
// changes made to …") is.
func TestAgentMemoryDocumentTargetRevertMessages(t *testing.T) {
	full := strings.Repeat("ab", 20)
	for msg, want := range map[string]bool{
		"Revert \"x\"\n\nThis reverts commit " + full + ".\n":                                               true,
		"Revert \"x\"\n\nThis reverts commit " + full + ", reversing\nchanges made to " + full + ".\n":      true,
		"docs: explain\n\n    This reverts commit " + full + ".\n":                                          false,
		"docs: explain\n\nThis reverts commit " + full + "cd.\n":                                            false,
		"docs: explain\n\nThis reverts commit " + full + " and more\n":                                      false,
		"docs: explain\n\n```\nThis reverts commit " + full + ".\n```\n":                                    false,
		"docs: explain\n\n~~~\nThis reverts commit " + full + ".\n~~~\nThis reverts commit " + full + ".\n": true,
		"docs: explain\n\n````\n```sh\nThis reverts commit " + full + ".\n````\n":                           false,
		"docs: explain\n\n```\nx\n``` not a close\nThis reverts commit " + full + ".\n```\n":                false,
		"Revert\n\nThis reverts commit " + full + ", reversing\n":                                           false,
		"Revert\n\nThis reverts commit " + full + ", reversing\nsomething else.\n":                          false,
		"Revert\n\nThis reverts commit " + full + ",\n":                                                     false,
	} {
		if got := agentMemDocSaysRevert(msg, full); got != want {
			t.Errorf("%q: %v, want %v", msg, got, want)
		}
	}

	// On real history: a quoting commit is not taken for a revert; a revert -m of a merge is.
	clone, project, _ := docTargetSetup(t)
	merge := promoteHead(t, clone, "HEAD")
	docTargetGit(t, clone, []string{"commit", "--quiet", "--allow-empty", "-m", "docs: history\n\n    This reverts commit " + merge + "."})
	docTargetClean(t, docTargetLint(t, project))

	// A target merged by a real merge commit.
	mclone, mproject := docTargetMergeSetup(t)
	mmerge := promoteHead(t, mclone, "HEAD")
	rev := docTargetGit(t, mclone, []string{"revert", "--no-edit", "-m", "1", mmerge})
	if f := docTargetOne(t, docTargetLint(t, mproject), agentMemDocReverted); f.Commit != rev {
		t.Fatalf("revert -m: %+v, want %s", f, rev)
	}
}

// docTargetMergeSetup is docTargetSetup with the section brought in by a merge commit of a branch.
func docTargetMergeSetup(t *testing.T) (clone, project string) {
	t.Helper()
	clone, a, _ := promoteRepo(t)
	promoteSave(t, a, "release-steps", "procedure", "Run scripts/release.sh.", "scripts/release.sh")
	if err := os.WriteFile(filepath.Join(clone, "AGENTS.md"), []byte("# Agents\n\n## Release\n\nRun scripts/release.sh.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	merge := docTargetGit(t, clone,
		[]string{"switch", "--quiet", "-c", "docs-branch"},
		[]string{"commit", "--quiet", "-am", "docs: release"},
		[]string{"switch", "--quiet", "main"},
		[]string{"merge", "--quiet", "--no-ff", "-m", "Merge docs-branch", "docs-branch"})
	code, out := supersedeDoc(t, agentMemSupersedeDocReq{Project: a.Project.ID, Name: "release-steps", Revision: 1, Path: "AGENTS.md",
		Heading: "Release", Commit: merge, BodySHA256: agentMemSHA256("Run scripts/release.sh.")})
	if code != http.StatusOK {
		t.Fatalf("supersede: %d %s", code, out)
	}
	return clone, a.Project.ID
}

// Past the bound on reverts of reverts the target is not judged (the answer would depend on the
// bound's parity): the report says so for that memory and lists no finding for it. At the bound
// minus one the chain is still followed.
func TestAgentMemoryDocumentTargetRevertDepthBound(t *testing.T) {
	old := agentMemDocRevertDepth
	t.Cleanup(func() { agentMemDocRevertDepth = old })
	agentMemDocRevertDepth = 3
	clone, project, merge := docTargetSetup(t)
	r1 := docTargetGit(t, clone, []string{"revert", "--no-edit", merge})
	r2 := docTargetGit(t, clone, []string{"revert", "--no-edit", r1})
	docTargetClean(t, docTargetLint(t, project)) // depth 2: the merge is back
	docTargetGit(t, clone, []string{"revert", "--no-edit", r2})
	w := docTargetLint(t, project)
	if w.Documents == nil || w.Documents.Checked != 0 || len(w.Documents.Findings) != 0 || !strings.Contains(strings.Join(w.Report.Skipped, "\n"), "document target of release-steps: too many reverts") {
		t.Fatalf("past the bound: %+v %v", w.Documents, w.Report.Skipped)
	}
}

// A rename is followed only after the target's commit: an older move of another file by the same
// name is no lead. And a rename whose commit lacks the heading (renamed and retitled, the title
// restored later) is still repaired by re-pointing at the revision the report read.
func TestAgentMemoryDocumentTargetRenameHistory(t *testing.T) {
	clone, a, _ := promoteRepo(t)
	promoteSave(t, a, "release-steps", "procedure", "Run scripts/release.sh.", "scripts/release.sh")
	// Long before the target existed, an AGENTS.md was moved away.
	docTargetGit(t, clone, []string{"mv", "AGENTS.md", "old-agents.md"}, []string{"commit", "--quiet", "-m", "move the old agents file"})
	agentMemLintOrigin(t, clone, map[string]string{"AGENTS.md": "# Agents\n\n## Release\n\nRun scripts/release.sh.\n", "old-agents.md": "# Old\n\n## Release\n\nx\n"})
	merge := promoteHead(t, clone, "HEAD")
	if code, out := supersedeDoc(t, agentMemSupersedeDocReq{Project: a.Project.ID, Name: "release-steps", Revision: 1, Path: "AGENTS.md",
		Heading: "Release", Commit: merge, BodySHA256: agentMemSHA256("Run scripts/release.sh.")}); code != http.StatusOK {
		t.Fatalf("supersede: %d %s", code, out)
	}
	docTargetGit(t, clone, []string{"rm", "--quiet", "AGENTS.md"}, []string{"commit", "--quiet", "-m", "drop agents"})
	docTargetOne(t, docTargetLint(t, a.Project.ID), agentMemDocMissing)

}

// A rename whose commit lacks the heading (renamed and retitled together, the title restored later)
// is reported with the heading found, and re-pointing at the revision the report read works.
func TestAgentMemoryDocumentTargetRenameRetitled(t *testing.T) {
	clone, project, _ := docTargetSetup(t)
	if err := agentMemGit(clone, "mv", "AGENTS.md", "docs/agents.md"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "docs", "agents.md"), []byte("# Agents\n\n## Build\n\nRun make.\n\n## Shipping\n\nRun scripts/release.sh.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rename := docTargetGit(t, clone, []string{"commit", "--quiet", "-am", "move and retitle"})
	agentMemLintOrigin(t, clone, map[string]string{"docs/agents.md": "# Agents\n\n## Build\n\nRun make.\n\n## Release\n\nRun scripts/release.sh.\n"})
	w := docTargetLint(t, project)
	f := docTargetOne(t, w, agentMemDocRenamed)
	if f.Path != "docs/agents.md" || !f.HeadingFound || f.Commit != rename {
		t.Fatalf("%+v", f)
	}
	// The renaming commit lacks the heading, so it is no commit to re-point to; the report's is.
	if code, _ := docRepair(t, "document-retarget", agentMemDocRepairReq{Project: project, Name: "release-steps", Revision: f.Revision,
		Path: f.Path, Heading: "Release", Commit: f.Commit}); code != http.StatusConflict {
		t.Fatalf("the renaming commit was accepted: %d", code)
	}
	if code, out := docRepair(t, "document-retarget", agentMemDocRepairReq{Project: project, Name: "release-steps", Revision: f.Revision,
		Path: f.Path, Heading: "Release", Commit: w.Commit}); code != http.StatusOK {
		t.Fatalf("re-point at the report's commit: %d %s", code, out)
	}
	docTargetClean(t, docTargetLint(t, project))
}

// Without origin/HEAD the default branch is a guess: the targets are not judged (a sound target on
// the real default branch would read as unreachable), and the report says why.
func TestAgentMemoryDocumentTargetNeedsTheDefaultBranch(t *testing.T) {
	clone, project, _ := docTargetSetup(t)
	if err := agentMemGit(clone, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD"); err != nil {
		t.Fatal(err)
	}
	w := docTargetLint(t, project)
	if w.Documents != nil || !strings.Contains(strings.Join(w.Report.Skipped, "\n"), "document targets: origin/HEAD does not name the default branch") {
		t.Fatalf("%+v %v", w.Documents, w.Report.Skipped)
	}
}

// The redundancy check is in the member's report only: a memory that restates the document it
// anchors is reported there and stays a promotion candidate (the check is advice, and promotion
// must be able to propose it again after the merge).
func TestAgentMemoryRestatesIsReportOnly(t *testing.T) {
	clone, a, b := promoteRepo(t)
	long := "Run scripts/release.sh from the repository root after the changelog is merged; it tags the commit and pushes the images."
	agentMemLintOrigin(t, clone, map[string]string{"docs/release.md": "# Release\n\n" + long + "\n"})
	promoteSave(t, a, "release-steps", "procedure", long, "docs/release.md")
	promoteConfirm(t, "release-steps", 1, a, b)
	w := docTargetLint(t, a.Project.ID)
	found := false
	for _, f := range w.Report.Findings {
		found = found || (f.Memory == "release-steps" && f.Check == "restates-document" && f.Subject == "docs/release.md")
	}
	if !found || w.Report.Counts["restates-document"] != 1 {
		t.Fatalf("not reported: %+v", w.Report)
	}
	if names := promoteNames(promoteGet(t, "project="+a.Project.ID)); len(names) != 1 || names[0] != "release-steps" {
		t.Fatalf("candidates: %v", names)
	}
}

// A file followed forward: the target moved to docs/agents.md, and a new AGENTS.md made later and
// moved elsewhere is not its history.
func TestAgentMemoryDocumentTargetRenameFollowsTheFile(t *testing.T) {
	clone, project, _ := docTargetSetup(t)
	docTargetGit(t, clone, []string{"mv", "AGENTS.md", "docs/agents.md"}, []string{"commit", "--quiet", "-m", "move agents"})
	agentMemLintOrigin(t, clone, map[string]string{"AGENTS.md": "# New\n\n## Release\n\nOther text.\n"})
	docTargetGit(t, clone, []string{"mv", "AGENTS.md", "docs/reference.md"}, []string{"commit", "--quiet", "-m", "move the new one"})
	if f := docTargetOne(t, docTargetLint(t, project), agentMemDocRenamed); f.Path != "docs/agents.md" {
		t.Fatalf("followed the wrong file: %+v", f)
	}
}

// A target document grown past the bound is not read: that memory is left unjudged (and not
// counted as checked), and the report says so.
func TestAgentMemoryDocumentTargetTooLarge(t *testing.T) {
	old := agentMemDocMaxBytes
	t.Cleanup(func() { agentMemDocMaxBytes = old })
	clone, project, _ := docTargetSetup(t)
	agentMemDocMaxBytes = 64
	agentMemLintOrigin(t, clone, map[string]string{"AGENTS.md": "# Agents\n\n## Release\n\n" + strings.Repeat("x", 100) + "\n"})
	w := docTargetLint(t, project)
	if w.Documents == nil || w.Documents.Checked != 0 || len(w.Documents.Findings) != 0 ||
		!strings.Contains(strings.Join(w.Report.Skipped, "\n"), "document target of release-steps: the document is too large") {
		t.Fatalf("%+v %v", w.Documents, w.Report.Skipped)
	}
}

// A working copy whose origin now names another repository is not used to judge the targets.
func TestAgentMemoryDocumentTargetNeedsTheSameOrigin(t *testing.T) {
	clone, project, _ := docTargetSetup(t)
	if err := agentMemGit(clone, "remote", "set-url", "origin", "https://github.com/fork/demo.git"); err != nil {
		t.Fatal(err)
	}
	w := docTargetLint(t, project)
	if w.Documents != nil || !strings.Contains(strings.Join(w.Report.Skipped, "\n"), "document targets: the working copy's origin") {
		t.Fatalf("%+v %v", w.Documents, w.Report.Skipped)
	}
}

// The default branch's history is its first parents: a branch that deleted the file, merged
// without keeping the delete, is not the file's history; the later rename on the default branch is.
func TestAgentMemoryDocumentTargetRenameAfterAMergeThatKeptTheFile(t *testing.T) {
	clone, project, _ := docTargetSetup(t)
	docTargetGit(t, clone,
		[]string{"switch", "--quiet", "-c", "side"},
		[]string{"rm", "--quiet", "AGENTS.md"},
		[]string{"commit", "--quiet", "-m", "drop agents on a branch"},
		[]string{"switch", "--quiet", "main"},
		[]string{"commit", "--quiet", "--allow-empty", "-m", "main moves on"},
		[]string{"merge", "--quiet", "-s", "ours", "-m", "merge side, keeping AGENTS.md", "side"},
		[]string{"mv", "AGENTS.md", "docs/agents.md"},
		[]string{"commit", "--quiet", "-m", "move agents"})
	if f := docTargetOne(t, docTargetLint(t, project), agentMemDocRenamed); f.Path != "docs/agents.md" {
		t.Fatalf("%+v", f)
	}
}

// The lint report's read bound does not narrow what an explicit supersede accepts.
func TestAgentMemorySupersedeIgnoresTheReportBound(t *testing.T) {
	old := agentMemDocMaxBytes
	t.Cleanup(func() { agentMemDocMaxBytes = old })
	agentMemDocMaxBytes = 16
	docTargetSetup(t) // fails unless the supersede of a larger document goes through
}
