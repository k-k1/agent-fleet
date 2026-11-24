package memoryx

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memorypromote"
)

// promoteHook is the repository's pre-commit hook in the fixtures: it refuses a staged change that
// adds the forbidden token, and says it ran either way, so a pass is told from a hook never run.
const promoteHook = `#!/bin/sh
echo "scan ran in $(basename "$(git rev-parse --show-toplevel)")"
if git diff --cached | grep -q 'forbidden\.example'; then
  echo "pre-commit: a forbidden token is staged" >&2
  exit 1
fi
`

// promoteRepo sets up a project whose repository has an origin URL, an origin/HEAD default branch
// with a guide and AGENTS.md, and core.hooksPath pointing at the in-tree hook. It answers the two
// confirming sessions.
func promoteRepo(t *testing.T) (clone string, a, b agentMemCaller) {
	t.Helper()
	_, clone, _ = agentMemTestEnv(t)
	promoteBackdate(t)
	// The throwaway clones go under TMPDIR: one per test, so a check that none is left behind
	// does not see another test's.
	t.Setenv("TMPDIR", t.TempDir())
	for _, args := range [][]string{
		{"remote", "add", "origin", "https://github.com/acme/demo.git"},
		{"config", "core.hooksPath", ".githooks"},
	} {
		if err := agentMemGit(clone, args...); err != nil {
			t.Fatal(err)
		}
	}
	hook := filepath.Join(clone, ".githooks", "pre-commit")
	if err := os.MkdirAll(filepath.Dir(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte(promoteHook), 0o755); err != nil {
		t.Fatal(err)
	}
	agentMemLintOrigin(t, clone, map[string]string{
		"AGENTS.md":          "# Agents\n\n## Build\n\nRun make.\n\n## Other\n\nx\n",
		"docs/guide.md":      "# Guide\n\nIntro.\n",
		"scripts/release.sh": "#!/bin/sh\n",
	})
	a, b = agentMemCallerT(t, "claude-main"), agentMemCallerT(t, "codex-wt")
	if a.Project == nil || a.Project.Remote != "github.com/acme/demo" {
		t.Fatalf("the fixture's project has no remote: %+v", a.Project)
	}
	return clone, a, b
}

func promotePropose(t *testing.T, req agentMemProposeReq) (int, agentMemProposeWire, string) {
	t.Helper()
	code, body := memberDo(t, http.MethodPost, "/agents/memory/entries/promotion-proposal", req)
	var w agentMemProposeWire
	if code == http.StatusOK {
		if err := json.Unmarshal(body, &w); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
	}
	return code, w, string(body)
}

// One candidate becomes a patch over the default branch: the file from its Markdown anchor, a new
// section from its description, the text, the memory and revision it came from, and the
// repository's own hook run on it. Nothing is written to the store or the working copy.
func TestAgentMemoryPromoteProposalBuildsAPatchAndRunsTheHook(t *testing.T) {
	clone, a, b := promoteRepo(t)
	promoteSave(t, a, "release-steps", "procedure", "Run scripts/release.sh from the root.", "scripts/release.sh", "docs/guide.md")
	promoteConfirm(t, "release-steps", 1, a, b)
	if err := agentMemGit(clone, "status", "--porcelain"); err != nil {
		t.Fatal(err)
	}
	storeBefore := agentMemLintSnapshot(t, agentMemDir())
	cloneBefore := agentMemLintSnapshot(t, clone)

	code, w, raw := promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps"})
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, raw)
	}
	if w.Target != (agentMemProposeTarget{Repo: "github.com/acme/demo", Path: "docs/guide.md", Heading: "d"}) ||
		!w.FileExists || w.SectionExists || w.Ref != "origin/main" || len(w.Commit) != 40 {
		t.Fatalf("target: %+v exists=%v section=%v ref=%s commit=%s", w.Target, w.FileExists, w.SectionExists, w.Ref, w.Commit)
	}
	if len(w.Sources) != 1 || w.Sources[0] != (agentMemProposeSource{Name: "release-steps", Scope: "project", Revision: 1, Description: "d"}) {
		t.Fatalf("sources: %+v", w.Sources)
	}
	if w.Text != "Run scripts/release.sh from the root." ||
		!strings.Contains(w.Patch, "+++ b/docs/guide.md") || !strings.Contains(w.Patch, "+## d\n+\n+Run scripts/release.sh from the root.\n") {
		t.Fatalf("text %q patch:\n%s", w.Text, w.Patch)
	}
	pc := w.Scans.PreCommit
	if !w.Ready || len(w.Scans.Secret) != 0 || !pc.Ran || !pc.Passed || pc.Absent || pc.Hook != "core.hooksPath .githooks" ||
		!strings.Contains(pc.Output, "scan ran in repo") {
		t.Fatalf("scans: ready=%v %+v", w.Ready, w.Scans)
	}
	if len(w.Warnings) != 0 {
		t.Fatalf("warnings: %v", w.Warnings)
	}
	if !reflectEq(storeBefore, agentMemLintSnapshot(t, agentMemDir())) {
		t.Fatal("the proposal wrote to the store")
	}
	if !reflectEq(cloneBefore, agentMemLintSnapshot(t, clone)) {
		t.Fatal("the proposal touched the working copy or its repository")
	}
	if left, _ := filepath.Glob(filepath.Join(os.TempDir(), "af-memory-promote-*")); len(left) != 0 {
		t.Fatalf("throwaway clones left behind: %v", left)
	}

	// The member names the file and an existing section: the text goes at the end of that
	// section, before the next heading.
	code, w, raw = promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps", File: "AGENTS.md", Section: "Build"})
	if code != http.StatusOK || !w.SectionExists || w.Target.Path != "AGENTS.md" || w.Target.Heading != "Build" {
		t.Fatalf("%d %s", code, raw)
	}
	if !strings.Contains(w.Patch, " Run make.\n \n+Run scripts/release.sh from the root.\n+\n ## Other") {
		t.Fatalf("patch:\n%s", w.Patch)
	}
	// A new file is created and flagged.
	code, w, raw = promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps", File: "docs/new.md", Section: "Release"})
	if code != http.StatusOK || w.FileExists || !strings.Contains(strings.Join(w.Warnings, ","), memorypromote.WarnNewFile) ||
		!strings.Contains(w.Patch, "new file mode") {
		t.Fatalf("%d %s", code, raw)
	}
}

// The repository's hook refuses a text that names what the shared repository must not; the
// member gets the hook's output and the patch to fix, and the proposal is not ready.
func TestAgentMemoryPromoteProposalReportsAFailingHook(t *testing.T) {
	_, a, b := promoteRepo(t)
	promoteSave(t, a, "deploy-host", "lesson", "Deploy goes to forbidden.example, see scripts/release.sh.", "scripts/release.sh")
	promoteConfirm(t, "deploy-host", 1, a, b)
	code, w, raw := promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "deploy-host"})
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, raw)
	}
	pc := w.Scans.PreCommit
	if w.Ready || !pc.Ran || pc.Passed || !strings.Contains(pc.Output, "a forbidden token is staged") || pc.Note != "the hook failed (exit 1)" {
		t.Fatalf("ready=%v %+v", w.Ready, pc)
	}
	// No anchor is a Markdown file: the first of the shared documents present is used.
	if w.Target.Path != "AGENTS.md" || w.Patch == "" {
		t.Fatalf("target %+v patch %q", w.Target, w.Patch)
	}
}

// Without core.hooksPath the hook is the repository's hooks/pre-commit; with none at all, or one
// git would not run, there is nothing to pass and the proposal says so.
func TestAgentMemoryPromoteProposalHookFromTheGitDirOrNone(t *testing.T) {
	clone, a, b := promoteRepo(t)
	promoteSave(t, a, "release-steps", "procedure", "Run scripts/release.sh.", "scripts/release.sh")
	promoteConfirm(t, "release-steps", 1, a, b)
	// core.hooksPath names a directory the tree does not carry: the working copy's hook runs.
	untracked := filepath.Join(clone, ".hooks-gen", "pre-commit")
	if err := os.MkdirAll(filepath.Dir(untracked), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(untracked, []byte("#!/bin/sh\necho from-working-copy\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := agentMemGit(clone, "config", "core.hooksPath", ".hooks-gen"); err != nil {
		t.Fatal(err)
	}
	code, w, raw := promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps"})
	if pc := w.Scans.PreCommit; code != http.StatusOK || !pc.Ran || !pc.Passed || !strings.Contains(pc.Output, "from-working-copy") ||
		pc.Hook != "core.hooksPath .hooks-gen (from the working copy)" {
		t.Fatalf("untracked hooks dir: %d %s", code, raw)
	}
	if err := agentMemGit(clone, "config", "--unset", "core.hooksPath"); err != nil {
		t.Fatal(err)
	}
	gitHook := filepath.Join(clone, ".git", "hooks", "pre-commit")
	if err := os.MkdirAll(filepath.Dir(gitHook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gitHook, []byte("#!/bin/sh\necho from-git-dir\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	code, w, raw = promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps"})
	pc := w.Scans.PreCommit
	if code != http.StatusOK || !pc.Ran || !pc.Passed || !strings.Contains(pc.Output, "from-git-dir") || pc.Hook != "hooks/ of the repository" {
		t.Fatalf("%d %s", code, raw)
	}
	if err := os.Chmod(gitHook, 0o644); err != nil {
		t.Fatal(err)
	}
	_, w, _ = promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps"})
	if pc := w.Scans.PreCommit; pc.Ran || !pc.Absent || !w.Ready || !strings.Contains(pc.Note, "not executable") {
		t.Fatalf("%+v", w.Scans.PreCommit)
	}
	if err := os.Remove(gitHook); err != nil {
		t.Fatal(err)
	}
	_, w, _ = promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps"})
	if pc := w.Scans.PreCommit; pc.Ran || !pc.Absent || !w.Ready || !strings.Contains(pc.Note, "no pre-commit hook") {
		t.Fatalf("%+v", w.Scans.PreCommit)
	}
}

// What the proposal refuses: a memory that is not a candidate (and why), bad input, and a text the
// secret scan flags, for which neither the text nor a patch is answered and no hook runs.
func TestAgentMemoryPromoteProposalRefusals(t *testing.T) {
	_, a, b := promoteRepo(t)
	promoteSave(t, a, "release-steps", "procedure", "Run scripts/release.sh.", "scripts/release.sh")
	promoteConfirm(t, "release-steps", 1, a, b)
	promoteSave(t, a, "one-session", "lesson", "Run scripts/release.sh.", "scripts/release.sh")
	promoteConfirm(t, "one-session", 1, a)

	code, _, raw := promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "one-session"})
	if code != http.StatusConflict || !strings.Contains(raw, "held: "+memorypromote.RuleConfirmation) {
		t.Fatalf("held: %d %s", code, raw)
	}
	code, _, raw = promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "nobody"})
	if code != http.StatusConflict || !strings.Contains(raw, "not a promotion candidate") {
		t.Fatalf("missing: %d %s", code, raw)
	}
	for _, req := range []agentMemProposeReq{
		{Name: "release-steps"},
		{Project: a.Project.ID, Name: "Bad Name"},
		{Project: a.Project.ID, Name: "release-steps", File: "../AGENTS.md"},
		{Project: a.Project.ID, Name: "release-steps", File: "/AGENTS.md"},
		{Project: a.Project.ID, Name: "release-steps", File: "main.go"},
		{Project: a.Project.ID, Name: "release-steps", Section: "# Build"},
		{Project: a.Project.ID, Name: "release-steps", Section: "two\nlines"},
		{Project: a.Project.ID, Name: "release-steps", Section: strings.Repeat("x", memorypromote.MaxSection+1)},
		{Project: a.Project.ID, Name: "release-steps", Days: 91},
		{Project: a.Project.ID, Name: "release-steps", Days: -1},
	} {
		if code, _, raw := promotePropose(t, req); code != http.StatusBadRequest {
			t.Errorf("%+v: %d %s", req, code, raw)
		}
	}
	// A directory is not a document.
	if code, _, raw := promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps", File: "docs.md/x.md"}); code != http.StatusOK {
		t.Fatalf("a new nested file: %d %s", code, raw)
	}

	key := "ghp_" + strings.Repeat("q7Zx", 9) // built at run time, never a literal in the tree
	code, w, raw := promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps", Section: "token " + key})
	if code != http.StatusOK || w.Ready || len(w.Scans.Secret) == 0 || w.Text != "" || w.Patch != "" || w.Scans.PreCommit.Ran {
		t.Fatalf("secret: %d %s", code, raw)
	}
	if strings.Contains(raw, key) {
		t.Fatal("the answer repeats the secret")
	}
}

// One proposal at a time: a second one while the first holds the slot is refused, not queued.
func TestAgentMemoryPromoteProposalOneAtATime(t *testing.T) {
	_, a, b := promoteRepo(t)
	promoteSave(t, a, "release-steps", "procedure", "Run scripts/release.sh.", "scripts/release.sh")
	promoteConfirm(t, "release-steps", 1, a, b)
	agentMemProposeBusy.Lock()
	code, _, raw := promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps"})
	agentMemProposeBusy.Unlock()
	if code != http.StatusConflict || !strings.Contains(raw, "another promotion proposal") {
		t.Fatalf("%d %s", code, raw)
	}
	if code, _, raw := promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps"}); code != http.StatusOK {
		t.Fatalf("after: %d %s", code, raw)
	}
}

// ---- supersede by a document -------------------------------------------------------------------

// promoteMerge commits the proposal's patch on the default branch of the fixture, as a merged pull
// request would, and answers the commit.
func promoteMerge(t *testing.T, clone, patch string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "proposal.patch")
	if err := os.WriteFile(p, []byte(patch), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"apply", "--index", p},
		{"-c", "core.hooksPath=/dev/null", "commit", "--quiet", "-m", "docs: promote a memory"},
		{"update-ref", "refs/remotes/origin/main", "HEAD"},
	} {
		if err := agentMemGit(clone, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	return promoteHead(t, clone, "HEAD")
}

func promoteHead(t *testing.T, clone, rev string) string {
	t.Helper()
	return strings.TrimSpace(agentMemGitOut(t, clone, "rev-parse", rev))
}

func supersedeDoc(t *testing.T, req agentMemSupersedeDocReq) (int, string) {
	t.Helper()
	b, _ := json.Marshal(req)
	code, out := markedDoJSON(t, "/agents/memory/entries/supersede-document", string(b))
	return code, string(out)
}

// Propose, merge, supersede: the memory names the document (repository, path, heading, the full
// merge commit), leaves agents' view, is one change in the list, and reverting it brings the
// memory back as it was.
func TestAgentMemorySupersedeByDocumentAfterTheMerge(t *testing.T) {
	clone, a, b := promoteRepo(t)
	promoteSave(t, a, "release-steps", "procedure", "Run scripts/release.sh from the root.", "scripts/release.sh")
	promoteConfirm(t, "release-steps", 1, a, b)
	_, w, raw := promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps", Section: "Release"})
	if !w.Ready {
		t.Fatalf("proposal: %s", raw)
	}
	merge := promoteMerge(t, clone, w.Patch)

	// The default branch moves on after the merge; the target is still there.
	agentMemLintOrigin(t, clone, map[string]string{"later.txt": "x\n"})

	code, out := supersedeDoc(t, agentMemSupersedeDocReq{Project: a.Project.ID, Name: "release-steps", Revision: w.Sources[0].Revision,
		Path: w.Target.Path, Heading: w.Target.Heading, Commit: merge[:10]})
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, out)
	}
	var res agentMemWriteResult
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.Revision != 2 || res.Commit == "" {
		t.Fatalf("%s", out)
	}
	e, err := agentMemMemberRead(agentMemScopeProject, a.Project.ID, "release-steps")
	if err != nil {
		t.Fatal(err)
	}
	want := agentMemDocTarget{Repo: "github.com/acme/demo", Path: "AGENTS.md", Heading: "Release", Commit: merge}
	if e.SupersededBy == nil || e.SupersededBy.Document == nil || *e.SupersededBy.Document != want || e.SupersededBy.Memory != "" ||
		e.InvalidAt == "" || e.Body != "Run scripts/release.sh from the root." || e.MemKind != "procedure" || e.AuthorKind != "member" {
		t.Fatalf("%+v", e)
	}
	// Agents see a stub naming the document.
	read, err := agentMemRead(a, agentMemScopeProject, "release-steps")
	if err != nil || !read.Stub || read.Body != "" || read.SupersededBy == nil || read.SupersededBy.Document == nil {
		t.Fatalf("agent read: %+v %v", read, err)
	}

	// One change, of one memory, listed as a supersede; its revert restores revision 1's text
	// and fields.
	code, lb := memberDo(t, http.MethodGet, "/agents/memory/entries/changes?scope=project&project="+a.Project.ID, nil)
	var list struct {
		Changes []agentMemChangeView `json:"changes"`
	}
	if code != http.StatusOK || json.Unmarshal(lb, &list) != nil || len(list.Changes) == 0 || list.Changes[0].Op != agentMemOpSupersede {
		t.Fatalf("changes: %d %s", code, lb)
	}
	rb, _ := json.Marshal(map[string]any{"commit": list.Changes[0].Commit})
	if code, out := markedDoJSON(t, "/agents/memory/entries/revert", string(rb)); code != http.StatusOK {
		t.Fatalf("revert: %d %s", code, out)
	}
	e, err = agentMemMemberRead(agentMemScopeProject, a.Project.ID, "release-steps")
	if err != nil || e.SupersededBy != nil || e.InvalidAt != "" || agentMemExcluded(e) {
		t.Fatalf("after revert: %+v %v", e, err)
	}
}

// The supersede is refused, and the memory left as it was, when the memory moved past the quoted
// revision, the commit is unknown or not on the default branch, the file or heading is missing at
// the merge or gone since, the default branch is a guess, or the request is malformed.
func TestAgentMemorySupersedeByDocumentRefusals(t *testing.T) {
	clone, a, b := promoteRepo(t)
	promoteSave(t, a, "release-steps", "procedure", "Run scripts/release.sh.", "scripts/release.sh")
	promoteConfirm(t, "release-steps", 1, a, b)
	_, w, raw := promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps", Section: "Release"})
	if !w.Ready {
		t.Fatalf("proposal: %s", raw)
	}
	merge := promoteMerge(t, clone, w.Patch)
	ok := agentMemSupersedeDocReq{Project: a.Project.ID, Name: "release-steps", Revision: 1, Path: "AGENTS.md", Heading: "Release", Commit: merge}

	// A commit only on another branch.
	if err := agentMemGit(clone, "checkout", "--quiet", "-b", "side"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "side.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "side.txt"}, {"-c", "core.hooksPath=/dev/null", "commit", "--quiet", "-m", "side"}} {
		if err := agentMemGit(clone, args...); err != nil {
			t.Fatal(err)
		}
	}
	side := promoteHead(t, clone, "HEAD")
	if err := agentMemGit(clone, "checkout", "--quiet", "main"); err != nil {
		t.Fatal(err)
	}
	before := promoteHead(t, clone, merge+"^")

	with := func(mut func(r *agentMemSupersedeDocReq)) agentMemSupersedeDocReq {
		r := ok
		mut(&r)
		return r
	}
	for _, c := range []struct {
		req  agentMemSupersedeDocReq
		code int
		want string
	}{
		{with(func(r *agentMemSupersedeDocReq) { r.Commit = strings.Repeat("ab", 20) }), http.StatusConflict, "not in the repository"},
		{with(func(r *agentMemSupersedeDocReq) { r.Commit = side }), http.StatusConflict, "is not on origin/main"},
		{with(func(r *agentMemSupersedeDocReq) { r.Commit = before }), http.StatusConflict, `has no heading \"Release\" at commit`},
		{with(func(r *agentMemSupersedeDocReq) { r.Heading = "Nowhere" }), http.StatusConflict, "has no heading"},
		{with(func(r *agentMemSupersedeDocReq) { r.Path = "docs/none.md" }), http.StatusConflict, "is not a file at commit"},
		{with(func(r *agentMemSupersedeDocReq) { r.Path = "docs" }), http.StatusConflict, "is not a file"},
		{with(func(r *agentMemSupersedeDocReq) { r.Revision = 0 }), http.StatusBadRequest, "revision"},
		{with(func(r *agentMemSupersedeDocReq) { r.Path = "../x.md" }), http.StatusBadRequest, "normalised"},
		{with(func(r *agentMemSupersedeDocReq) { r.Heading = "" }), http.StatusBadRequest, "heading"},
		{with(func(r *agentMemSupersedeDocReq) { r.Commit = "HEAD" }), http.StatusBadRequest, "hex commit"},
		{with(func(r *agentMemSupersedeDocReq) { r.Name = "nobody" }), http.StatusNotFound, "no memory"},
	} {
		code, out := supersedeDoc(t, c.req)
		if code != c.code || !strings.Contains(out, c.want) {
			t.Errorf("%+v: %d %s, want %d %q", c.req, code, out, c.code, c.want)
		}
	}

	// The memory changed while the pull request waited: the merged text is older than it.
	if _, err := agentMemSave(a, agentMemSaveReq{Name: "release-steps", Description: "d", Type: "project", Body: "Run scripts/release.sh twice.", Revision: 1}, promoteSaved); err != nil {
		t.Fatal(err)
	}
	if code, out := supersedeDoc(t, ok); code != http.StatusConflict || !strings.Contains(out, "at revision 2, not the 1 the proposal quoted") {
		t.Fatalf("moved: %d %s", code, out)
	}
	ok.Revision = 2

	// The heading is gone from the default branch since the merge.
	agentMemLintOrigin(t, clone, map[string]string{"AGENTS.md": "# Agents\n"})
	if code, out := supersedeDoc(t, ok); code != http.StatusConflict || !strings.Contains(out, `has no heading \"Release\" at origin/main`) {
		t.Fatalf("heading gone: %d %s", code, out)
	}
	// origin/HEAD unset: the default branch is only a guess.
	if err := agentMemGit(clone, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD"); err != nil {
		t.Fatal(err)
	}
	if code, out := supersedeDoc(t, ok); code != http.StatusConflict || !strings.Contains(out, "only a guess") {
		t.Fatalf("guess: %d %s", code, out)
	}

	e, err := agentMemMemberRead(agentMemScopeProject, a.Project.ID, "release-steps")
	if err != nil || e.SupersededBy != nil || e.Revision != 2 {
		t.Fatalf("a refusal wrote: %+v %v", e, err)
	}

	// Without the member's mark the route refuses before reading anything.
	bb, _ := json.Marshal(ok)
	if code, out := memberDo(t, http.MethodPost, "/agents/memory/entries/supersede-document", json.RawMessage(bb)); code != http.StatusForbidden {
		t.Fatalf("unmarked: %d %s", code, out)
	}
}

// A project whose repository has no origin URL cannot be named as a target, by the proposal or the
// supersede.
func TestAgentMemoryPromoteNeedsTheRepositoryIdentity(t *testing.T) {
	clone, a, b := promoteRepo(t)
	promoteSave(t, a, "release-steps", "procedure", "Run scripts/release.sh.", "scripts/release.sh")
	promoteConfirm(t, "release-steps", 1, a, b)
	p := agentMemProjectInfo(a.Project.ID)
	if p == nil {
		t.Fatal("no project record")
	}
	_ = clone
	if code, out := supersedeDoc(t, agentMemSupersedeDocReq{Project: "app-000000000000", Name: "release-steps", Revision: 1,
		Path: "AGENTS.md", Heading: "Build", Commit: strings.Repeat("a", 40)}); code != http.StatusNotFound && code != http.StatusBadRequest {
		t.Fatalf("unknown project: %d %s", code, out)
	}
	if _, err := agentMemSupersedeDocCheck(&agentMemProject{ID: p.ID, Root: p.Root, VCS: p.VCS}, "AGENTS.md", "Build", "abcdef1"); err == nil ||
		!strings.Contains(err.Error(), "no origin URL") {
		t.Fatalf("no remote: %v", err)
	}
}
