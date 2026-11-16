package memoryx

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
)

type museImportEnv struct {
	t     *testing.T
	home  string
	clone string
	wt    string
	dir   string // the clone's directory name under ~/repos
	pid   string
}

func newMuseImportEnv(t *testing.T) *museImportEnv {
	home, clone, wt := agentMemTestEnv(t)
	return &museImportEnv{t: t, home: home, clone: clone, wt: wt, dir: filepath.Base(clone), pid: agentMemProjectFor(clone).ID}
}

// note writes a muse note: name / description / type at the top level, as muse writes them.
func (e *museImportEnv) note(name, desc, typ, body string) string {
	e.t.Helper()
	return e.raw(e.clone, name+".md", fmt.Sprintf("---\nname: %s\ndescription: %s\ntype: %s\n---\n\n%s\n", name, desc, typ, body))
}

func (e *museImportEnv) raw(wc, file, text string) string {
	e.t.Helper()
	p := filepath.Join(wc, ".agents", "memory", file)
	memoryWrite(e.t, p, text)
	return p
}

func (e *museImportEnv) preview() *agentMemMusePreview {
	e.t.Helper()
	pv, err := agentMemMusePreviewFor("project", e.dir, "")
	if err != nil {
		e.t.Fatal(err)
	}
	return pv
}

func (e *museImportEnv) item(pv *agentMemMusePreview, name string) agentMemImportItem {
	e.t.Helper()
	it := pv.byName[name]
	if it == nil {
		e.t.Fatalf("no item %q in %v", name, pv.Counts)
	}
	return *it
}

func (e *museImportEnv) apply(now time.Time, names ...string) agentMemImportApplied {
	e.t.Helper()
	pv := e.preview()
	req := agentMemMuseReq{Project: e.pid, Dir: e.dir}
	for _, n := range names {
		req.Items = append(req.Items, agentMemImportReqItem{Name: n, SourceHash: pv.byName[n].SourceHash})
	}
	out, err := agentMemMuseApply(req, now)
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

func (e *museImportEnv) git(args ...string) {
	e.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = e.clone
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	if out, err := cmd.CombinedOutput(); err != nil {
		e.t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func TestMuseImportSourcesAndPreview(t *testing.T) {
	e := newMuseImportEnv(t)
	e.note("pref", "a preference", "feedback", "use tabs")
	e.raw(e.clone, "MEMORY.md", "- [Pref](pref.md) - a preference\n")
	e.raw(e.clone, "sub/nested.md", "---\ndescription: d\n---\nb\n")
	// A plain directory under ~/repos is its own project and nothing in it is shared through git.
	plain := filepath.Join(e.home, "repos", "plain")
	memoryMkdirAll(t, plain)
	e.raw(plain, "x.md", "---\ndescription: d\n---\nb\n")
	// A working copy with only index links lists nothing.
	e.raw(e.wt, "MEMORY.md", "# Memory\n\n- [Idx](idx.md) - an index line\n")

	by := map[string]agentMemMuseSource{}
	for _, s := range agentMemMuseList().Sources {
		by[s.Dir] = s
	}
	if s := by[e.dir]; s.Count != 2 || s.Project == nil || s.Project.ID != e.pid {
		t.Errorf("clone source = %+v", s)
	}
	if _, ok := by[filepath.Base(e.wt)]; ok {
		t.Errorf("index-only working copy listed")
	}
	if s := by["plain"]; s.Project == nil || s.Shared != 0 {
		t.Errorf("plain source = %+v", s)
	}

	pv := e.preview()
	if len(pv.Items) != 2 {
		t.Fatalf("items = %+v", pv.Items)
	}
	if it := e.item(pv, "sub--nested"); it.Status != claudeImportNew || it.Path != "sub/nested.md" {
		t.Errorf("nested = %+v", it)
	}
	if it := e.item(pv, "pref"); it.Status != claudeImportNew || it.Type != "feedback" || it.Shared {
		t.Errorf("pref = %+v", it)
	}
	if pv, err := agentMemMusePreviewFor("project", "plain", ""); err != nil || pv.Project == nil || pv.Items[0].Shared {
		t.Errorf("plain preview = %+v, %v", pv, err)
	}
	for _, bad := range []string{"", "..", "a/b", "nope"} {
		if _, err := agentMemMusePreviewFor("project", bad, ""); err == nil {
			t.Errorf("preview of %q must fail", bad)
		}
	}
}

func TestMuseImportAppliesToProjectOnly(t *testing.T) {
	e := newMuseImportEnv(t)
	e.note("pref", "a preference", "user", "use tabs")
	out := e.apply(time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC), "pref")
	if len(out.Results) != 1 || out.Results[0].Result != "imported" {
		t.Fatalf("apply = %+v", out)
	}
	got, err := agentMemRead(agentMemCallerT(t, "claude-main"), "", "pref")
	if err != nil {
		t.Fatal(err)
	}
	if got.Scope != agentMemScopeProject || got.AuthorKind != agentMemUnknown || got.AuthorSession != agentMemUnknown ||
		got.Source != "muse:project/.agents/memory/pref.md" || got.SourceHash == "" || got.Revision != 1 || got.Body != "use tabs" {
		t.Errorf("entry = %+v", got)
	}
	files, _ := memoryGitRun("log", "--name-only", "--format=", memoryBranch)
	for _, f := range strings.Fields(files) {
		if !strings.HasPrefix(f, "af/projects/"+e.pid+"/") {
			t.Errorf("import wrote outside the project: %s", f)
		}
	}
	msg, _ := memoryGitRun("log", "-1", "--format=%B", memoryBranch)
	if !strings.Contains(msg, "AF-Op: import") || !strings.Contains(msg, "AF-Author-Kind: member") {
		t.Errorf("commit = %s", msg)
	}
	if pv := e.preview(); pv.Counts[claudeImportUnchanged] != 1 || len(pv.Counts) != 1 {
		t.Errorf("not idempotent: %v", pv.Counts)
	}
}

func TestMuseImportSharedThroughGit(t *testing.T) {
	e := newMuseImportEnv(t)
	e.note("team", "shared note", "project", "b")
	e.note("mine", "private note", "project", "b")
	e.git("add", ".agents/memory/team.md")
	e.git("commit", "--quiet", "-m", "add team note")
	pv := e.preview()
	if !e.item(pv, "team").Shared || e.item(pv, "mine").Shared {
		t.Errorf("shared flags: team=%v mine=%v", e.item(pv, "team").Shared, e.item(pv, "mine").Shared)
	}
	for _, s := range agentMemMuseList().Sources {
		if s.Dir == e.dir && s.Shared != 1 {
			t.Errorf("source shared = %d, want 1", s.Shared)
		}
	}
}

func TestMuseImportUpdateOnlyReplacesIntactImport(t *testing.T) {
	e := newMuseImportEnv(t)
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for _, n := range []string{"keep", "edited", "claudes", "afown"} {
		p := e.note(n, "d", "user", "v1")
		_ = os.Chtimes(p, t0, t0)
	}
	e.apply(t0.Add(time.Hour), "keep", "edited")
	c := agentMemCallerT(t, "claude-main")
	// edited: the member saves their own revision in AF.
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "edited", Description: "d", Type: "user", Body: "af text", Revision: 1}, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// afown: written in AF, never imported. claudes: stored by another importer.
	if _, err := agentMemSave(c, agentMemSaveReq{Name: "afown", Description: "d", Type: "user", Body: "af own"}, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	later := t0.Add(5 * time.Hour)
	for _, n := range []string{"keep", "edited", "afown"} {
		p := e.note(n, "d", "user", "v2")
		_ = os.Chtimes(p, later, later)
	}
	pv := e.preview()
	if it := e.item(pv, "keep"); it.Status != claudeImportUpdate {
		t.Errorf("keep = %+v", it)
	}
	if it := e.item(pv, "edited"); it.Status != museImportDiverged || it.Reason != "af_changed" {
		t.Errorf("edited = %+v", it)
	}
	if it := e.item(pv, "afown"); it.Status != museImportDiverged || it.Reason != "other_source" {
		t.Errorf("afown = %+v", it)
	}
	out := e.apply(later.Add(time.Hour), "keep", "edited", "afown")
	if r := out.Results[0]; r.Name != "keep" || r.Result != "updated" {
		t.Errorf("keep result = %+v", r)
	}
	for _, r := range out.Results[1:] {
		if r.Result != "skipped" || r.Reason != "status_diverged" {
			t.Errorf("%s result = %+v", r.Name, r)
		}
	}
	if got, _ := agentMemRead(c, "", "edited"); got.Body != "af text" {
		t.Errorf("the member's AF text was overwritten: %+v", got)
	}
	if got, _ := agentMemRead(c, "", "keep"); got.Body != "v2" || got.Revision != 2 {
		t.Errorf("keep = %+v", got)
	}
	// An older source file never replaces anything.
	older := e.note("keep", "d", "user", "v0")
	_ = os.Chtimes(older, t0, t0)
	if it := e.item(e.preview(), "keep"); it.Status != claudeImportUnchanged {
		t.Errorf("older source = %+v", it)
	}
}

func TestMuseImportForgottenAndSecrets(t *testing.T) {
	e := newMuseImportEnv(t)
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	e.note("gone", "d", "user", "b")
	e.note("fresh", "d", "user", "b")
	e.note("leaky", "d", "user", "token is ghp_"+strings.Repeat("q7Zx", 9))
	e.apply(now, "gone")
	if _, err := agentMemForget(agentMemCallerT(t, "claude-main"), agentMemForgetReq{Name: "gone", Revision: 1}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	pv := e.preview()
	if it := e.item(pv, "gone"); it.Status != claudeImportForgotten {
		t.Errorf("gone = %+v", it)
	}
	if it := e.item(pv, "leaky"); it.Status != claudeImportSecret || len(it.Findings) == 0 {
		t.Fatalf("leaky = %+v", it)
	}
	out := e.apply(now.Add(time.Hour), "gone", "fresh", "leaky")
	want := map[string]string{"gone": "status_forgotten", "leaky": "status_secret"}
	for _, r := range out.Results {
		switch r.Name {
		case "fresh":
			if r.Result != "imported" {
				t.Errorf("fresh = %+v", r)
			}
		default:
			if r.Result != "skipped" || r.Reason != want[r.Name] {
				t.Errorf("%s = %+v", r.Name, r)
			}
		}
	}
	if all, _ := memoryGitRun("log", "-p", memoryBranch); strings.Contains(all, "ghp_") {
		t.Errorf("the secret reached the store")
	}
}

func TestMuseImportSymlinkedDirectoriesRefused(t *testing.T) {
	e := newMuseImportEnv(t)
	outside := filepath.Join(e.home, "outside")
	memoryWrite(t, filepath.Join(outside, "memory", "x.md"), "---\ndescription: d\n---\nb\n")
	if err := os.Symlink(outside, filepath.Join(e.clone, ".agents")); err != nil {
		t.Fatal(err)
	}
	for _, s := range agentMemMuseList().Sources {
		if s.Dir == e.dir {
			t.Errorf("a symlinked .agents was listed: %+v", s)
		}
	}
	if _, err := agentMemMusePreviewFor("project", e.dir, ""); agentMemCode(err) != errCodeMemoryNotFound {
		t.Errorf("preview through a symlink: %v", err)
	}
}

func TestMuseImportHandlers(t *testing.T) {
	e := newMuseImportEnv(t)
	e.note("via-http", "d", "user", "b")
	do := func(method, target, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		w := httptest.NewRecorder()
		switch {
		case method == http.MethodPost:
			HandleAgentMemoryMuseApply(w, r)
		case strings.Contains(target, "preview"):
			HandleAgentMemoryMusePreview(w, r)
		default:
			HandleAgentMemoryMuseSources(w, r)
		}
		return w
	}
	hash := e.preview().byName["via-http"].SourceHash
	post := fmt.Sprintf(`{"project":%q,"dir":%q,"items":[{"name":"via-http","sourceHash":%q}]}`, e.pid, e.dir, hash)
	AgentMemoryEnabled = func() bool { return false }
	if w := do("GET", "/agents/memory/muse-import", ""); w.Code != 200 {
		t.Errorf("sources off: %d", w.Code)
	}
	if w := do("GET", "/agents/memory/muse-import/preview?dir="+e.dir, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"new"`) {
		t.Errorf("preview off: %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/agents/memory/muse-import", post); w.Code != 403 || !strings.Contains(w.Body.String(), errCodeMemoryDisabled) {
		t.Errorf("apply off: %d %s", w.Code, w.Body)
	}
	if memoryHasCommits() {
		t.Errorf("a refused apply committed")
	}
	AgentMemoryEnabled = func() bool { return true }
	if w := do("POST", "/agents/memory/muse-import", post); w.Code != 200 || !strings.Contains(w.Body.String(), `"imported"`) {
		t.Errorf("apply on: %d %s", w.Code, w.Body)
	}
}

// A repository's own config must not run a command on preview: core.fsmonitor would, for ls-files.
func TestMuseImportPreviewDoesNotRunFsmonitor(t *testing.T) {
	e := newMuseImportEnv(t)
	e.note("team", "shared note", "project", "b")
	e.git("add", ".agents/memory/team.md")
	marker := filepath.Join(e.home, "fsmonitor-ran")
	script := filepath.Join(e.home, "fsm.sh")
	memoryWrite(t, script, "#!/bin/sh\ntouch "+marker+"\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	e.git("config", "core.fsmonitor", script)
	pv := e.preview()
	agentMemMuseList()
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("core.fsmonitor ran during preview/list")
	}
	if !e.item(pv, "team").Shared {
		t.Errorf("a tracked file must still be shared")
	}
}

// A symlink under ~/repos must not carry the import to a directory outside it.
func TestMuseImportRefusesSymlinkedWorkingCopy(t *testing.T) {
	e := newMuseImportEnv(t)
	outside := filepath.Join(e.home, "outside")
	memoryWrite(t, filepath.Join(outside, ".agents", "memory", "note.md"), "---\nname: note\ndescription: d\ntype: user\n---\nb\n")
	if err := os.Symlink(outside, filepath.Join(e.home, "repos", "linked")); err != nil {
		t.Fatal(err)
	}
	for _, s := range agentMemMuseList().Sources {
		if s.Dir == "linked" {
			t.Errorf("a symlinked working copy was listed: %+v", s)
		}
	}
	if _, err := agentMemMusePreviewFor("project", "linked", ""); agentMemCode(err) != errCodeMemoryNotFound {
		t.Errorf("preview: %v", err)
	}
	if _, err := agentMemMuseApply(agentMemMuseReq{Project: e.pid, Dir: "linked",
		Items: []agentMemImportReqItem{{Name: "note"}}}, time.Now()); agentMemCode(err) != errCodeMemoryNotFound {
		t.Errorf("apply: %v", err)
	}
	// A normal "@" worktree still maps to the main project.
	e.raw(e.wt, "wt.md", "---\nname: wt\ndescription: d\ntype: user\n---\nb\n")
	pv, err := agentMemMusePreviewFor("project", filepath.Base(e.wt), "")
	if err != nil || pv.Project == nil || pv.Project.ID != e.pid {
		t.Errorf("worktree preview = %+v, %v", pv, err)
	}
}

// The same bytes under another file name are still the memory the member forgot.
func TestMuseImportForgottenSurvivesRename(t *testing.T) {
	e := newMuseImportEnv(t)
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	p := e.note("gone", "d", "user", "b")
	e.apply(now, "gone")
	if _, err := agentMemForget(agentMemCallerT(t, "claude-main"), agentMemForgetReq{Name: "gone", Revision: 1}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(p, filepath.Join(filepath.Dir(p), "renamed.md")); err != nil {
		t.Fatal(err)
	}
	pv := e.preview()
	if it := e.item(pv, "renamed"); it.Status != claudeImportForgotten {
		t.Fatalf("renamed = %+v", it)
	}
	out := e.apply(now.Add(time.Hour), "renamed")
	if r := out.Results[0]; r.Result != "skipped" || r.Reason != "status_forgotten" {
		t.Errorf("renamed result = %+v", r)
	}
}

// An AF memory that the claude import wrote is not replaced by a newer muse file of that name.
func TestMuseImportDoesNotOverwriteClaudeImport(t *testing.T) {
	e := newMuseImportEnv(t)
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	cp := filepath.Join(claude.ConfigDir(), "projects", claude.ProjectKey(e.clone), "memory", "shared-name.md")
	memoryWrite(t, cp, "---\nname: shared-name\ndescription: d\nmetadata:\n  type: user\n---\n\nfrom claude\n")
	_ = os.Chtimes(cp, t0, t0)
	pv, err := agentMemImportPreviewFor(claude.ProjectKey(e.clone))
	if err != nil {
		t.Fatal(err)
	}
	out, err := agentMemImportApply(agentMemImportReq{Project: e.pid, Slug: claude.ProjectKey(e.clone),
		Items: []agentMemImportReqItem{{Name: "shared-name", SourceHash: pv.byName["shared-name"].SourceHash}}}, t0.Add(time.Hour))
	if err != nil || out.Results[0].Result != "imported" {
		t.Fatalf("claude import = %+v, %v", out, err)
	}
	later := t0.Add(5 * time.Hour)
	_ = os.Chtimes(e.note("shared-name", "d", "user", "from muse"), later, later)
	it := e.item(e.preview(), "shared-name")
	if it.Status != museImportDiverged || it.Reason != "other_source" {
		t.Fatalf("shared-name = %+v", it)
	}
	e.apply(later.Add(time.Hour), "shared-name")
	if got, _ := agentMemRead(agentMemCallerT(t, "claude-main"), "", "shared-name"); got.Body != "from claude" {
		t.Errorf("the claude import was overwritten: %+v", got)
	}
}

// The project mapping and the git query run against the opened directory, not the path: swapping
// another repository in under the name for the duration of the queries (and back afterwards)
// must not pair this working copy's notes with that repository's project.
func TestMuseImportProjectMappingFollowsPinnedDirectory(t *testing.T) {
	e := newMuseImportEnv(t)
	e.note("note", "d", "user", "from the original repo")
	other := filepath.Join(e.home, "repos", "other")
	replacement := filepath.Join(e.home, "repos", "other-wt")
	saved := filepath.Join(e.home, "repos", "demo-saved")
	e.git("init", "--quiet", "-b", "main", other)
	e.git("-C", other, "commit", "--quiet", "--allow-empty", "-m", "init")
	e.git("-C", other, "worktree", "add", "--quiet", "-b", "temp", replacement)
	otherPID := agentMemProjectFor(other).ID
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	// Before the first git call the clone is moved away and the other repository's worktree takes
	// its name; after ls-files everything is put back.
	script := fmt.Sprintf(`#!/bin/sh
if [ ! -f '%[1]s/once' ]; then
  touch '%[1]s/once'
  mv '%[2]s' '%[3]s'
  mv '%[4]s' '%[2]s'
fi
'%[5]s' "$@"
rc=$?
case "$*" in
  *ls-files*)
    if [ -d '%[3]s' ]; then
      mv '%[2]s' '%[4]s'
      mv '%[3]s' '%[2]s'
    fi
    ;;
esac
exit "$rc"
`, e.home, e.clone, saved, replacement, realGit)
	memoryWrite(t, filepath.Join(e.home, "bin", "git"), script)
	if err := os.Chmod(filepath.Join(e.home, "bin", "git"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Join(e.home, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	pv, err := agentMemMusePreviewFor("project", e.dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, serr := os.Stat(filepath.Join(e.home, "once")); serr != nil {
		t.Fatalf("the swap never ran, the probe proves nothing")
	}
	// The directory was asked while it sat under another name, so its path-derived id may differ
	// from e.pid (apply then refuses on the project check); what must never happen is the other
	// repository's id.
	if pv.Project == nil || pv.Project.ID == otherPID || len(pv.Items) != 1 {
		t.Errorf("project = %+v, items = %d; must not be %s", pv.Project, len(pv.Items), otherPID)
	}
}

// A forgotten text renamed in muse onto the name of a memory that exists in AF must not replace it,
// and a rename onto a free name is refused as well.
func TestMuseImportForgottenRenameIntoLiveMemory(t *testing.T) {
	e := newMuseImportEnv(t)
	t0 := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	gone := e.note("gone", "d", "user", "forgotten text")
	live := e.note("live", "d", "user", "other text")
	e.apply(t0, "gone", "live")
	if _, err := agentMemForget(agentMemCallerT(t, "claude-main"), agentMemForgetReq{Name: "gone", Revision: 1}, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// muse's gone.md becomes live.md with the forgotten bytes, newer than AF's copy.
	if err := os.Rename(gone, live); err != nil {
		t.Fatal(err)
	}
	later := t0.Add(5 * time.Hour)
	_ = os.Chtimes(live, later, later)
	pv := e.preview()
	if it := e.item(pv, "live"); it.Status != claudeImportForgotten {
		t.Fatalf("live = %+v, want forgotten", it)
	}
	out := e.apply(later.Add(time.Hour), "live")
	if r := out.Results[0]; r.Result != "skipped" || r.Reason != "status_forgotten" {
		t.Errorf("result = %+v", r)
	}
	if got, _ := agentMemRead(agentMemCallerT(t, "claude-main"), "", "live"); got.Body != "other text" {
		t.Errorf("the forgotten text replaced the live memory: %+v", got)
	}
}

// muse stores short facts in MEMORY.md itself (measured on muse 1.4.20); the lines that are not
// index links are one note per store.
func TestMuseImportMemoryMdFacts(t *testing.T) {
	e := newMuseImportEnv(t)
	e.note("pref", "a preference", "feedback", "use tabs")
	e.raw(e.clone, "MEMORY.md", "# Memory\n\n- [Pref](pref.md) - a preference\n- the deploy window is Friday\n")
	e.raw(e.clone, "sub/MEMORY.md", "- a nested fact\n")

	pv := e.preview()
	if len(pv.Items) != 2 {
		t.Fatalf("items = %+v", pv.Items)
	}
	it := e.item(pv, museIndexNoteName)
	if it.Status != claudeImportNew || it.Path != "MEMORY.md" || it.Description != "- the deploy window is Friday" {
		t.Fatalf("MEMORY.md note = %+v", it)
	}
	if strings.Contains(it.entry.Body, "pref.md") || strings.Contains(it.entry.Body, "#") {
		t.Errorf("index link or heading leaked into the body: %q", it.entry.Body)
	}
	if s := agentMemMuseList(); s.Sources[0].Count != 2 {
		t.Errorf("sources = %+v", s.Sources)
	}

	now := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	if out := e.apply(now, museIndexNoteName); out.Results[0].Result != "imported" {
		t.Fatalf("apply = %+v", out)
	}
	got, err := agentMemRead(agentMemCallerT(t, "claude-main"), "", museIndexNoteName)
	if err != nil || got.Source != "muse:project/.agents/memory/MEMORY.md" || !strings.Contains(got.Body, "Friday") {
		t.Fatalf("entry = %+v, %v", got, err)
	}
	// An index edit that leaves the facts alone is not a change; re-runs are idempotent.
	e.raw(e.clone, "MEMORY.md", "# Memory\n- [Pref](pref.md) - reworded\n- the deploy window is Friday\n")
	if pv := e.preview(); pv.byName[museIndexNoteName].Status != claudeImportUnchanged {
		t.Errorf("not idempotent: %v", pv.Counts)
	}
	// Forgotten stays forgotten, and an AF-side memory of that name is never replaced.
	if _, err := agentMemForget(agentMemCallerT(t, "claude-main"), agentMemForgetReq{Name: museIndexNoteName, Revision: 1}, now); err != nil {
		t.Fatal(err)
	}
	if pv := e.preview(); pv.byName[museIndexNoteName].Status != claudeImportForgotten {
		t.Errorf("forgotten MEMORY.md note came back: %v", pv.Counts)
	}
}

func TestMuseImportMemoryMdIndexOnlyAndSecret(t *testing.T) {
	e := newMuseImportEnv(t)
	e.raw(e.clone, "MEMORY.md", "# Memory\n\n- [A](a.md) - a\n* [B](b/c.md) - b\n")
	if len(agentMemMuseList().Sources) != 0 {
		t.Fatalf("index-only MEMORY.md listed: %+v", agentMemMuseList().Sources)
	}
	e.raw(e.clone, "MEMORY.md", "- token ghp_"+strings.Repeat("a1", 18)+"\n")
	if it := e.item(e.preview(), museIndexNoteName); it.Status != claudeImportSecret {
		t.Errorf("secret = %+v", it)
	}
}

// A topic file named like the MEMORY.md note must not be overwritten by it.
func TestMuseImportMemoryMdNameCollision(t *testing.T) {
	e := newMuseImportEnv(t)
	e.raw(e.clone, "MEMORY.md", "- a fact\n")
	e.raw(e.clone, museIndexNoteName+".md", "topic")
	for _, it := range e.preview().Items {
		if it.Status != claudeImportInvalid || it.Reason != "name_collision" {
			t.Errorf("%s = %+v", it.Path, it)
		}
	}
}
