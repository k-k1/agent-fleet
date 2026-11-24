package memoryx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memorypromote"
)

// promoteHook is the repository's pre-commit hook in the fixtures: it refuses a staged change that
// adds the forbidden token, and says it ran either way, so a pass is told from a hook never run.
const promoteHook = `#!/bin/sh
echo "scan ran in $(basename "$(git rev-parse --show-toplevel)")" >> "$PROMOTE_MARK"
if git diff --cached | grep -q 'forbidden\.example'; then
  echo "pre-commit: a forbidden token is staged in $PWD ($TMPDIR)" >&2
  exit 1
fi
`

// promoteMark reads what the fixture hooks wrote to $PROMOTE_MARK since the last read.
func promoteMark(t *testing.T) string {
	t.Helper()
	p := os.Getenv("PROMOTE_MARK")
	b, _ := os.ReadFile(p)
	_ = os.Remove(p)
	return string(b)
}

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
	t.Setenv("PROMOTE_MARK", filepath.Join(t.TempDir(), "mark"))
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
	promoteMark(t) // the fixture's own commit ran the hook
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
	if len(w.Sources) != 1 || w.Sources[0] != (agentMemProposeSource{Name: "release-steps", Scope: "project", Revision: 1,
		BodySHA256: agentMemSHA256("Run scripts/release.sh from the root."), Description: "d"}) {
		t.Fatalf("sources: %+v", w.Sources)
	}
	if w.Text != "Run scripts/release.sh from the root." ||
		!strings.Contains(w.Patch, "+++ b/docs/guide.md") || !strings.Contains(w.Patch, "+## d\n+\n+Run scripts/release.sh from the root.\n") {
		t.Fatalf("text %q patch:\n%s", w.Text, w.Patch)
	}
	pc := w.Scans.PreCommit
	if !w.Ready || len(w.Scans.Secret) != 0 || !pc.Ran || !pc.Passed || pc.Absent || pc.Changed || pc.Hook != "core.hooksPath .githooks" ||
		pc.Output != "" || promoteMark(t) != "scan ran in repo\n" {
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
	if w.Ready || !pc.Ran || pc.Passed || !strings.Contains(pc.Output, "a forbidden token is staged in <clone> (<tmp>/tmp)") ||
		strings.Contains(pc.Output, "af-memory-promote-") || pc.Note != "the hook failed (exit 1)" {
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
	// The working copy's hook is the one git runs: not executable there, it is not run, whatever
	// the default branch carries.
	wcHook := filepath.Join(clone, ".githooks", "pre-commit")
	if err := os.Chmod(wcHook, 0o644); err != nil {
		t.Fatal(err)
	}
	_, w, _ := promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps"})
	if pc := w.Scans.PreCommit; pc.Ran || !pc.Absent || !w.Ready || !strings.Contains(pc.Note, "not executable") || promoteMark(t) != "" {
		t.Fatalf("non-executable in the working copy: %+v", pc)
	}
	// core.hooksPath names a directory the tree does not carry: the working copy's hook runs.
	untracked := filepath.Join(clone, ".hooks-gen", "pre-commit")
	if err := os.MkdirAll(filepath.Dir(untracked), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(untracked, []byte("#!/bin/sh\necho from-working-copy >> \"$PROMOTE_MARK\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := agentMemGit(clone, "config", "core.hooksPath", ".hooks-gen"); err != nil {
		t.Fatal(err)
	}
	code, w, raw := promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps"})
	if pc := w.Scans.PreCommit; code != http.StatusOK || !pc.Ran || !pc.Passed || promoteMark(t) != "from-working-copy\n" ||
		pc.Hook != "core.hooksPath .hooks-gen" {
		t.Fatalf("untracked hooks dir: %d %s", code, raw)
	}
	if err := agentMemGit(clone, "config", "--unset", "core.hooksPath"); err != nil {
		t.Fatal(err)
	}
	gitHook := filepath.Join(clone, ".git", "hooks", "pre-commit")
	if err := os.MkdirAll(filepath.Dir(gitHook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gitHook, []byte("#!/bin/sh\necho from-git-dir >> \"$PROMOTE_MARK\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	code, w, raw = promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps"})
	pc := w.Scans.PreCommit
	if code != http.StatusOK || !pc.Ran || !pc.Passed || promoteMark(t) != "from-git-dir\n" || pc.Hook != "hooks/ of the repository" {
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
		{Project: a.Project.ID, Name: "release-steps", Section: "Build #"},
		{Project: a.Project.ID, Name: "release-steps", File: ".git/x.md"},
		{Project: a.Project.ID, Name: "release-steps", File: "docs/.GIT/x.md"},
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
	// The file name can be what the scan flags: the findings name a fixed label, and the target
	// path is withheld too.
	code, w, raw = promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps", File: "docs/" + key + ".md"})
	if code != http.StatusOK || w.Ready || len(w.Scans.Secret) == 0 || strings.Contains(raw, key) {
		t.Fatalf("secret file name: %d %s", code, raw)
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
		Path: w.Target.Path, Heading: w.Target.Heading, Commit: merge[:10], BodySHA256: w.Sources[0].BodySHA256})
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
	ok := agentMemSupersedeDocReq{Project: a.Project.ID, Name: "release-steps", Revision: 1, Path: "AGENTS.md", Heading: "Release", Commit: merge,
		BodySHA256: w.Sources[0].BodySHA256}

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
	if strings.HasPrefix(merge, "deadbeef") {
		t.Skip("the merge commit happens to start with the branch name the test uses")
	}
	if err := agentMemGit(clone, "branch", "deadbeef", merge); err != nil {
		t.Fatal(err)
	}

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
		{with(func(r *agentMemSupersedeDocReq) { r.BodySHA256 = "" }), http.StatusBadRequest, "bodySha256"},
		// A branch spelled like a commit id resolves, but to another commit: refused.
		{with(func(r *agentMemSupersedeDocReq) { r.Commit = "deadbeef" }), http.StatusConflict, "not in the repository"},
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
	ok.Revision, ok.BodySHA256 = 2, agentMemSHA256("Run scripts/release.sh twice.")

	// The store edited by hand without a new revision: the text is not the one quoted.
	file := filepath.Join(agentMemDir(), "projects", a.Project.ID, "release-steps.md")
	orig, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(strings.Replace(string(orig), "twice.", "three times.", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out := supersedeDoc(t, ok); code != http.StatusConflict || !strings.Contains(out, "not the one the proposal quoted") {
		t.Fatalf("hand edit: %d %s", code, out)
	}
	if err := os.WriteFile(file, orig, 0o644); err != nil {
		t.Fatal(err)
	}

	// The working copy's origin now names another repository (a fork).
	if err := agentMemGit(clone, "remote", "set-url", "origin", "https://github.com/someone/fork.git"); err != nil {
		t.Fatal(err)
	}
	if code, out := supersedeDoc(t, ok); code != http.StatusConflict || !strings.Contains(out, "github.com/someone/fork") {
		t.Fatalf("fork: %d %s", code, out)
	}
	if err := agentMemGit(clone, "remote", "set-url", "origin", "https://github.com/acme/demo.git"); err != nil {
		t.Fatal(err)
	}
	if code, out := supersedeDoc(t, with(func(r *agentMemSupersedeDocReq) { r.Revision, r.BodySHA256 = ok.Revision, ok.BodySHA256 })); code != http.StatusOK {
		// The positive control is taken on a copy: undo it so the checks below still see revision 2.
		t.Fatalf("control: %d %s", code, out)
	}
	lifeSet(t, a, agentMemScopeProject, "release-steps", func(e *agentMemEntry) { e.SupersededBy, e.InvalidAt = nil, "" })
	ok.Revision += 2 // the supersede and its undo

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
	if err != nil || e.SupersededBy != nil || e.Revision != ok.Revision {
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
	if _, err := agentMemSupersedeDocCheck(context.Background(), &agentMemProject{ID: p.ID, Root: p.Root, VCS: p.VCS}, "AGENTS.md", "Build", "abcdef1"); err == nil ||
		!strings.Contains(err.Error(), "no origin URL") {
		t.Fatalf("no remote: %v", err)
	}
}

// promoteOriginRemove deletes paths from the fixture's default branch.
func promoteOriginRemove(t *testing.T, clone string, paths ...string) {
	t.Helper()
	for _, args := range [][]string{
		append([]string{"rm", "--quiet", "--"}, paths...),
		{"-c", "core.hooksPath=/dev/null", "commit", "--quiet", "-m", "rm"},
		{"update-ref", "refs/remotes/origin/main", "HEAD"},
	} {
		if err := agentMemGit(clone, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

// promoteSetHook replaces the working copy's pre-commit hook.
func promoteSetHook(t *testing.T, clone, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(clone, ".githooks", "pre-commit"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// promoteGone waits briefly for a process to be gone (or a zombie nobody waits for).
func promoteGone(t *testing.T, pidFile string) {
	t.Helper()
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the hook did not record its background child: %v", err)
	}
	pid := strings.TrimSpace(string(b))
	for i := 0; i < 50; i++ {
		st, err := os.ReadFile("/proc/" + pid + "/stat")
		if err != nil || strings.Contains(string(st), ") Z ") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the hook's background child %s outlived the proposal", pid)
}

func promoteNoTemp(t *testing.T) {
	t.Helper()
	if left, _ := filepath.Glob(filepath.Join(os.TempDir(), "af-memory-promote-*")); len(left) != 0 {
		t.Fatalf("throwaway directories left behind: %v", left)
	}
	if left, _ := filepath.Glob(filepath.Join(os.TempDir(), "tmp.*")); len(left) != 0 {
		t.Fatalf("the hook's temporary files were left outside the throwaway directory: %v", left)
	}
}

// The clone and the hook do not see the Agent's GIT_* variables, its global git configuration
// (a smudge filter that would fail the checkout) or its credentials; the hook's TMPDIR is inside
// the throwaway directory, and a child it leaves in the background is killed with it.
func TestAgentMemoryPromoteProposalIsolatesTheCloneAndTheHook(t *testing.T) {
	clone, a, b := promoteRepo(t)
	agentMemLintOrigin(t, clone, map[string]string{".gitattributes": "*.md filter=boom\n"})
	promoteSave(t, a, "release-steps", "procedure", "Run scripts/release.sh.", "scripts/release.sh")
	promoteConfirm(t, "release-steps", 1, a, b)
	promoteMark(t)
	pidFile := filepath.Join(t.TempDir(), "pid")
	t.Setenv("PROMOTE_PID", pidFile)
	promoteSetHook(t, clone, `#!/bin/sh
fail() { echo "isolation: $1" >&2; exit 3; }
[ -z "$AGENT_TOKEN" ] || fail AGENT_TOKEN
[ -z "$AF_PROMOTE_TEST_SECRET" ] || fail AF_
[ -z "$GIT_CONFIG_COUNT" ] || fail GIT_CONFIG_COUNT
case "$TMPDIR" in */af-memory-promote-*/tmp) ;; *) fail "TMPDIR=$TMPDIR" ;; esac
[ "$(git rev-parse --show-toplevel)" = "$PWD" ] || fail worktree
mktemp >/dev/null
mkdir "$TMPDIR/ro" && touch "$TMPDIR/ro/f" && chmod 500 "$TMPDIR/ro"
sleep 60 >/dev/null 2>&1 &
echo $! > "$PROMOTE_PID"
echo ok >> "$PROMOTE_MARK"
`)
	t.Setenv("AGENT_TOKEN", "agent-token-for-the-test")
	t.Setenv("AF_PROMOTE_TEST_SECRET", "x")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.worktree")
	t.Setenv("GIT_CONFIG_VALUE_0", t.TempDir())
	// A required smudge filter that fails: a checkout that reads the global configuration fails.
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".gitconfig"),
		[]byte("[filter \"boom\"]\n\tsmudge = false\n\trequired = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	storeBefore := agentMemLintSnapshot(t, agentMemDir())
	start := time.Now()
	code, w, raw := promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps"})
	if pc := w.Scans.PreCommit; code != http.StatusOK || !pc.Passed || !w.Ready || promoteMark(t) != "ok\n" {
		t.Fatalf("%d %s", code, raw)
	}
	if time.Since(start) > 30*time.Second {
		t.Fatal("the proposal waited for the hook's background child")
	}
	promoteGone(t, pidFile)
	promoteNoTemp(t)
	if !reflectEq(storeBefore, agentMemLintSnapshot(t, agentMemDir())) {
		t.Fatal("the proposal wrote to the store")
	}
}

// A hook past its time, or a request that ends, is stopped with everything it started, the slot
// is free again, and nothing is left behind.
func TestAgentMemoryPromoteProposalStopsTheHook(t *testing.T) {
	clone, a, b := promoteRepo(t)
	promoteSave(t, a, "release-steps", "procedure", "Run scripts/release.sh.", "scripts/release.sh")
	promoteConfirm(t, "release-steps", 1, a, b)
	pidFile := filepath.Join(t.TempDir(), "pid")
	t.Setenv("PROMOTE_PID", pidFile)
	promoteSetHook(t, clone, "#!/bin/sh\nsleep 60 >/dev/null 2>&1 &\necho $! > \"$PROMOTE_PID\"\nsleep 60\n")
	old := agentMemProposeHookTimeout
	agentMemProposeHookTimeout = time.Second
	t.Cleanup(func() { agentMemProposeHookTimeout = old })

	start := time.Now()
	code, w, raw := promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps"})
	if pc := w.Scans.PreCommit; code != http.StatusOK || !pc.Ran || pc.Passed || w.Ready || !strings.Contains(pc.Note, "did not finish") {
		t.Fatalf("%d %s", code, raw)
	}
	if time.Since(start) > 30*time.Second {
		t.Fatal("the timeout did not stop the hook")
	}
	promoteGone(t, pidFile)
	promoteNoTemp(t)

	// The request ends: the hook stops with it, and the slot is free.
	agentMemProposeHookTimeout = time.Minute
	_ = os.Remove(pidFile)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for i := 0; i < 600; i++ {
			if _, err := os.Stat(pidFile); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		cancel()
	}()
	start = time.Now()
	res, err := agentMemPropose(ctx, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps"}, time.Now())
	t.Logf("cancelled: %+v %v", res.Scans.PreCommit, err)
	if err == nil || time.Since(start) > 90*time.Second {
		t.Fatalf("a cancelled request kept the hook running: %v", err)
	}
	promoteGone(t, pidFile)
	promoteNoTemp(t)
	if !agentMemProposeBusy.TryLock() {
		t.Fatal("the slot is still held")
	}
	agentMemProposeBusy.Unlock()
}

// The patch is collected apart from the hook's output and is never cut: a change next to a long
// line applies as answered. A hook that rewrites the staged change makes the proposal not ready
// and answers what it left.
func TestAgentMemoryPromoteProposalPatchIsWholeAndChecked(t *testing.T) {
	clone, a, b := promoteRepo(t)
	long := strings.Repeat("long line ", 2500) // 25 KB of context
	agentMemLintOrigin(t, clone, map[string]string{"AGENTS.md": "# Agents\n\n## Build\n\n" + long + "\n"})
	promoteSave(t, a, "release-steps", "procedure", "Run scripts/release.sh.", "scripts/release.sh")
	promoteConfirm(t, "release-steps", 1, a, b)
	// A successful hook that talks a lot is still a pass.
	promoteSetHook(t, clone, "#!/bin/sh\nhead -c 40000 /dev/zero | tr '\\0' x\necho more chatter >&2\n")
	code, w, raw := promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps", Section: "Build"})
	if code != http.StatusOK || !w.Ready || len(w.Patch) < 20000 || strings.Contains(w.Patch, "chatter") || w.Scans.PreCommit.Output != "" {
		t.Fatalf("%d ready=%v patch %d bytes", code, w.Ready, len(w.Patch))
	}
	_ = raw
	promoteMerge(t, clone, w.Patch) // git apply --index: it applies as answered

	// A hook that rewrites the change, and stages a file the scan never saw: not ready, and the
	// answer keeps the patch that was scanned.
	key := "ghp_" + strings.Repeat("q7Zx", 9) // built at run time, never a literal in the tree
	t.Setenv("PROMOTE_KEY", key)
	promoteSetHook(t, clone, "#!/bin/sh\necho formatted >> AGENTS.md && echo \"$PROMOTE_KEY\" > gen.txt && git add AGENTS.md gen.txt\n")
	code, w, raw = promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps", Section: "Release"})
	if pc := w.Scans.PreCommit; code != http.StatusOK || w.Ready || !pc.Passed || !pc.Changed || strings.Contains(w.Patch, "formatted") ||
		strings.Contains(raw, key) || !strings.Contains(w.Patch, "+## Release") {
		t.Fatalf("%d %s", code, raw)
	}
}

// A new document under a directory that is a symlink on the default branch, or inside .git, is
// refused before anything is written; the symlink's target is untouched.
func TestAgentMemoryPromoteProposalStaysInTheTree(t *testing.T) {
	clone, a, b := promoteRepo(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(clone, "docs-link")); err != nil {
		t.Fatal(err)
	}
	agentMemLintOrigin(t, clone, map[string]string{"x.txt": "x\n"})
	promoteSave(t, a, "release-steps", "procedure", "Run scripts/release.sh.", "scripts/release.sh")
	promoteConfirm(t, "release-steps", 1, a, b)
	code, _, raw := promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps", File: "docs-link/new.md"})
	if code != http.StatusBadRequest || !strings.Contains(raw, "is not a directory") {
		t.Fatalf("%d %s", code, raw)
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatalf("written through the symlink: %v", ents)
	}
}

// Without --file: the first Markdown anchor, else AGENTS.md, CLAUDE.md, README.md in that order,
// else a new AGENTS.md. A document ending in an unclosed fence cannot take a new section.
func TestAgentMemoryPromoteProposalPicksTheDocument(t *testing.T) {
	clone, a, b := promoteRepo(t)
	agentMemLintOrigin(t, clone, map[string]string{"CLAUDE.md": "# Claude\n", "README.md": "# Readme\n"})
	promoteSave(t, a, "guide-steps", "procedure", "Read the guide.", "scripts/release.sh", "docs/guide.md")
	promoteConfirm(t, "guide-steps", 1, a, b)
	promoteSave(t, a, "release-steps", "procedure", "Run scripts/release.sh.", "scripts/release.sh")
	promoteConfirm(t, "release-steps", 1, a, b)
	pick := func(name string) agentMemProposeWire {
		t.Helper()
		code, w, raw := promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: name})
		if code != http.StatusOK {
			t.Fatalf("%d %s", code, raw)
		}
		return w
	}
	if w := pick("guide-steps"); w.Target.Path != "docs/guide.md" {
		t.Fatalf("anchor: %+v", w.Target)
	}
	if w := pick("release-steps"); w.Target.Path != "AGENTS.md" {
		t.Fatalf("first: %+v", w.Target)
	}
	for _, c := range []struct{ rm, want string }{{"AGENTS.md", "CLAUDE.md"}, {"CLAUDE.md", "README.md"}, {"README.md", "AGENTS.md"}} {
		promoteOriginRemove(t, clone, c.rm)
		w := pick("release-steps")
		if w.Target.Path != c.want || (c.want == "AGENTS.md") == w.FileExists {
			t.Fatalf("without %s: %+v exists=%v", c.rm, w.Target, w.FileExists)
		}
	}
	// A document that ends inside an unclosed fence would swallow a new section.
	agentMemLintOrigin(t, clone, map[string]string{"AGENTS.md": "# Agents\n\n```sh\nmake\n"})
	code, _, raw := promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps"})
	if code != http.StatusConflict || !strings.Contains(raw, "would not stand as its own blocks") {
		t.Fatalf("unclosed fence: %d %s", code, raw)
	}
}

// The working copy's origin is read again: removed, or pointing at a fork, the proposal is
// refused rather than recorded under the project's name. An oversized request is refused before
// it is decoded.
func TestAgentMemoryPromoteProposalChecksTheOriginAndTheRequest(t *testing.T) {
	clone, a, b := promoteRepo(t)
	promoteSave(t, a, "release-steps", "procedure", "Run scripts/release.sh.", "scripts/release.sh")
	promoteConfirm(t, "release-steps", 1, a, b)
	if err := agentMemGit(clone, "remote", "set-url", "origin", "https://github.com/someone/fork.git"); err != nil {
		t.Fatal(err)
	}
	if code, _, raw := promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps"}); code != http.StatusConflict ||
		!strings.Contains(raw, "github.com/someone/fork") {
		t.Fatalf("fork: %d %s", code, raw)
	}
	// The URL goes, the remote-tracking refs stay (git remote remove would delete them too).
	if err := agentMemGit(clone, "config", "--unset", "remote.origin.url"); err != nil {
		t.Fatal(err)
	}
	if code, _, raw := promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps"}); code != http.StatusConflict ||
		!strings.Contains(raw, "origin could not be read") {
		t.Fatalf("no origin: %d %s", code, raw)
	}
	// The padding is a field the handler ignores: only the size limit refuses it.
	big := `{"project":"` + a.Project.ID + `","name":"release-steps","padding":"` + strings.Repeat("x", agentMemProposeMaxRequest) + `"}`
	if code, b := memberDo(t, http.MethodPost, "/agents/memory/entries/promotion-proposal", json.RawMessage(big)); code != http.StatusBadRequest {
		t.Fatalf("oversized: %d %s", code, b)
	}
}

// A heading the secret scan flags is refused as a document target, and nothing is written.
func TestAgentMemorySupersedeByDocumentScansTheTarget(t *testing.T) {
	clone, a, b := promoteRepo(t)
	promoteSave(t, a, "release-steps", "procedure", "Run scripts/release.sh.", "scripts/release.sh")
	promoteConfirm(t, "release-steps", 1, a, b)
	// A quoted assignment: in the stored frontmatter the quotes become \" and the raw bytes no
	// longer match; only the decoded value does. Built at run time, never a literal in the tree.
	key := strings.Repeat("Zq8x", 5)
	heading := `Set password = "` + key + `"`
	agentMemLintOrigin(t, clone, map[string]string{"AGENTS.md": "# Agents\n\n## " + heading + "\n\nx\n"})
	merge := promoteHead(t, clone, "HEAD")
	code, out := supersedeDoc(t, agentMemSupersedeDocReq{Project: a.Project.ID, Name: "release-steps", Revision: 1, Path: "AGENTS.md",
		Heading: heading, Commit: merge, BodySHA256: agentMemSHA256("Run scripts/release.sh.")})
	if code != http.StatusUnprocessableEntity || strings.Contains(out, key) {
		t.Fatalf("%d %s", code, out)
	}
	e, err := agentMemMemberRead(agentMemScopeProject, a.Project.ID, "release-steps")
	if err != nil || e.SupersededBy != nil || e.Revision != 1 {
		t.Fatalf("written: %+v %v", e, err)
	}
}

// git decides where the hook is: a relative core.hooksPath keeps its spaces, an empty one and
// /dev/null leave nothing to run. A failing hook named by an absolute path does not put that path,
// the working copy's or the home directory in the scans of the answer.
func TestAgentMemoryPromoteProposalResolvesTheHookAsGitDoes(t *testing.T) {
	clone, a, b := promoteRepo(t)
	promoteSave(t, a, "release-steps", "procedure", "Run scripts/release.sh.", "scripts/release.sh")
	promoteConfirm(t, "release-steps", 1, a, b)
	propose := func() agentMemPreCommit {
		t.Helper()
		code, w, raw := promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps"})
		if code != http.StatusOK {
			t.Fatalf("%d %s", code, raw)
		}
		if pc := w.Scans.PreCommit; (pc.Passed || pc.Absent) != w.Ready {
			t.Fatalf("ready %v with %+v", w.Ready, pc)
		}
		return w.Scans.PreCommit
	}
	spaced := filepath.Join(clone, "hooks dir ")
	if err := os.MkdirAll(spaced, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spaced, "pre-commit"), []byte("#!/bin/sh\necho spaced >> \"$PROMOTE_MARK\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		value  string
		ran    bool
		absent bool
		mark   string
	}{
		{"hooks dir ", true, false, "spaced\n"},
		{"", false, true, ""},
		{"/dev/null", false, true, ""},
	} {
		if err := agentMemGit(clone, "config", "core.hooksPath", c.value); err != nil {
			t.Fatal(err)
		}
		pc := propose()
		if pc.Ran != c.ran || pc.Absent != c.absent || promoteMark(t) != c.mark {
			t.Errorf("core.hooksPath %q: %+v", c.value, pc)
		}
	}

	// "link/../hooks": the kernel follows the link before "..", so the hooks are those beside the
	// link's target, not a "hooks" beside the link. Lexical cleaning would pick the wrong one.
	real := filepath.Join(t.TempDir(), "real")
	for _, d := range []string{filepath.Join(real, "sub"), filepath.Join(real, "hooks")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(real, "hooks", "pre-commit"), []byte("#!/bin/sh\necho through-the-link >> \"$PROMOTE_MARK\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(real, "sub"), filepath.Join(clone, "tool-link")); err != nil {
		t.Fatal(err)
	}
	if err := agentMemGit(clone, "config", "core.hooksPath", "tool-link/../hooks"); err != nil {
		t.Fatal(err)
	}
	if pc := propose(); !pc.Ran || !pc.Passed || promoteMark(t) != "through-the-link\n" {
		t.Fatalf("link/..: %+v", pc)
	}

	abs := filepath.Join(t.TempDir(), "abs-hooks")
	if err := os.MkdirAll(abs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(abs, "pre-commit"), []byte("#!/bin/sh\necho \"$0 in $PWD, home $HOME, wc "+clone+"\" >&2\n"+
		"echo \"real $(readlink -f \"$0\")\" >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Reached through a link: the hook's own realpath is the target's, and is hidden too.
	absLink := filepath.Join(t.TempDir(), "abs-link")
	if err := os.Symlink(abs, absLink); err != nil {
		t.Fatal(err)
	}
	if err := agentMemGit(clone, "config", "core.hooksPath", absLink); err != nil {
		t.Fatal(err)
	}
	pc := propose()
	scans, _ := json.Marshal(pc)
	home, _ := os.UserHomeDir()
	for _, p := range []string{abs, absLink, clone, home, os.TempDir()} {
		if strings.Contains(string(scans), p) {
			t.Errorf("the scans carry the path %s: %s", p, scans)
		}
	}
	if !pc.Ran || pc.Passed || pc.Hook != "core.hooksPath (an absolute path)" || !strings.Contains(pc.Output, "<hooks>/pre-commit in <clone>") ||
		!strings.Contains(pc.Output, "real <hooks>/pre-commit") {
		t.Fatalf("%+v", pc)
	}
}

// A hook that outlives the clone's time limit but not its own still passes: the diff after it has
// its own limit. A request that ended before the hook runs nothing more and frees the slot.
func TestAgentMemoryPromoteProposalLimitsAreSeparate(t *testing.T) {
	clone, a, b := promoteRepo(t)
	promoteSave(t, a, "release-steps", "procedure", "Run scripts/release.sh.", "scripts/release.sh")
	promoteConfirm(t, "release-steps", 1, a, b)
	old := agentMemProposeCloneTimeout
	agentMemProposeCloneTimeout = 15 * time.Second
	t.Cleanup(func() { agentMemProposeCloneTimeout = old })
	promoteSetHook(t, clone, "#!/bin/sh\nsleep 17\necho slow >> \"$PROMOTE_MARK\"\n")
	code, w, raw := promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps"})
	if code != http.StatusOK || !w.Ready || !w.Scans.PreCommit.Passed || promoteMark(t) != "slow\n" {
		t.Fatalf("%d %s", code, raw)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// The checks after the selection take the request's context too.
	if err := agentMemRemoteCheck(ctx, a.Project); err == nil {
		t.Fatal("the origin check ran for an ended request")
	}
	if _, err := agentMemSupersedeDocCheck(ctx, a.Project, "AGENTS.md", "Build", strings.Repeat("a", 40)); err == nil {
		t.Fatal("the supersede's checks ran for an ended request")
	}
	// The steps after the origin check stop with the request too.
	if repo, _, _ := agentMemLintRepoForCtx(ctx, a.Project); repo != nil {
		t.Fatal("the repository was read for an ended request")
	}
	if repo, ref, _ := agentMemLintRepoFor(a.Project); repo == nil || agentMemPromoteDefaultKnownCtx(ctx, repo, ref) ||
		!agentMemPromoteDefaultKnown(repo, ref) {
		t.Fatal("the default-branch check ignores the request's end, or the positive control fails")
	}
	if _, err := agentMemPropose(ctx, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps"}, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("an ended request: %v", err)
	}
	if promoteMark(t) != "" {
		t.Fatal("the hook ran for an ended request")
	}
	if !agentMemProposeBusy.TryLock() {
		t.Fatal("the slot is still held")
	}
	agentMemProposeBusy.Unlock()
}

// The change is staged as the bytes it was built from: a working-tree-encoding attribute does not
// re-encode the document, so existing non-ASCII lines stay as they are and only the text is added.
func TestAgentMemoryPromoteProposalStagesTheBlobAsBuilt(t *testing.T) {
	clone, a, b := promoteRepo(t)
	agentMemLintOrigin(t, clone, map[string]string{"AGENTS.md": "# Agents\n\n## Build\n\nUn café avant la build.\n"})
	// The attribute comes after the blob, so the blob above is UTF-8 as written; only the
	// attributes file is staged (git add -A could re-read AGENTS.md through the new attribute).
	if err := os.WriteFile(filepath.Join(clone, ".gitattributes"), []byte("*.md working-tree-encoding=ISO-8859-1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "--", ".gitattributes"},
		{"-c", "core.hooksPath=/dev/null", "commit", "--quiet", "-m", "attributes"},
		{"update-ref", "refs/remotes/origin/main", "HEAD"},
	} {
		if err := agentMemGit(clone, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if blob := agentMemGitOut(t, clone, "cat-file", "blob", "HEAD:AGENTS.md"); !strings.Contains(blob, "Un café avant") {
		t.Fatalf("the fixture's blob is not UTF-8: %q", blob)
	}
	promoteSave(t, a, "release-steps", "procedure", "Run scripts/release.sh.", "scripts/release.sh")
	promoteConfirm(t, "release-steps", 1, a, b)
	code, w, raw := promotePropose(t, agentMemProposeReq{Project: a.Project.ID, Name: "release-steps", Section: "Build"})
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, raw)
	}
	for _, ln := range strings.Split(w.Patch, "\n") {
		if strings.HasPrefix(ln, "-") && !strings.HasPrefix(ln, "---") {
			t.Fatalf("the patch changes an existing line: %q\n%s", ln, w.Patch)
		}
	}
	if !strings.Contains(w.Patch, " Un café avant la build.\n+\n+Run scripts/release.sh.\n") {
		t.Fatalf("patch:\n%s\n%s", w.Patch, raw)
	}
}
