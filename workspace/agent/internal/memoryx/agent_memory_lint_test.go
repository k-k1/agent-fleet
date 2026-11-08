package memoryx

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/memberorigin/memberorigintest"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/memorylint"
)

// agentMemLintOrigin gives the clone an origin/main with the files of tree, the way a fetch
// would, and makes it the default branch. The working copy then differs from it on purpose.
func agentMemLintOrigin(t *testing.T, clone string, tree map[string]string) {
	t.Helper()
	for p, body := range tree {
		abs := filepath.Join(clone, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"add", "-A"},
		{"commit", "--quiet", "-m", "files"},
		{"update-ref", "refs/remotes/origin/main", "HEAD"},
		{"symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main"},
	} {
		if err := agentMemGit(clone, args...); err != nil {
			t.Fatal(err)
		}
	}
}

// agentMemLintSnapshot is every file under dir with its content and mode: two equal snapshots
// mean nothing was created, removed or rewritten.
func agentMemLintSnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			out[rel+"/"] = ""
			return nil
		}
		info, _ := d.Info()
		b, _ := os.ReadFile(p)
		out[rel] = info.Mode().String() + info.ModTime().String() + string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func agentMemLintGet(t *testing.T, q string) agentMemLintWire {
	t.Helper()
	code, b := memberDo(t, http.MethodGet, "/agents/memory/entries/lint?"+q, nil)
	var w agentMemLintWire
	if code != http.StatusOK || json.Unmarshal(b, &w) != nil {
		t.Fatalf("lint %s: %d %s", q, code, b)
	}
	return w
}

func agentMemLintSubjects(w agentMemLintWire, mem, check string) []string {
	var out []string
	for _, f := range w.Report.Findings {
		if f.Memory == mem && f.Check == check {
			out = append(out, f.Subject)
		}
	}
	return out
}

func agentMemLintSaveBody(t *testing.T, c agentMemCaller, name, body string) {
	t.Helper()
	if _, err := agentMemSave(c, agentMemSaveReq{Name: name, Description: "d", Type: "project", Body: body}, time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
}

// A positive control per check through the whole route, over a real repository read by ref: the
// working copy holds a file origin/main lacks and lacks one origin/main has, and only origin/main
// counts. A run changes no working copy, no memory file and no usage sidecar.
func TestAgentMemoryLintReportsEveryCheckByRefAndWritesNothing(t *testing.T) {
	home, clone, _ := agentMemTestEnv(t)
	agentMemLintOrigin(t, clone, map[string]string{
		"console/src/app.tsx":                      "export const App = 1\n",
		"workspace/agent/main.go":                  "package main\nfunc agentMemSave() {}\nfunc TestRealOne() {}\n",
		"docs/decisions/0130-cultivated-memory.md": "# 0130\n",
	})
	// Local-only state the lint must ignore: an untracked file, and a tracked one deleted in the tree.
	if err := os.WriteFile(filepath.Join(clone, "console", "only-local.go"), []byte("package x\nfunc localOnlyFn() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(clone, "console", "src", "app.tsx")); err != nil {
		t.Fatal(err)
	}
	c := agentMemCallerT(t, "claude-main")
	agentMemLintSaveBody(t, c, "target", "fine")
	agentMemLintSaveBody(t, c, "links", "see [[target]] and [[ghost]]")
	agentMemLintSaveBody(t, c, "paths", "console/src/app.tsx exists on origin/main; console/only-local.go and console/src/gone.tsx do not")
	agentMemLintSaveBody(t, c, "syms", "`agentMemSave` `localOnlyFn` TestRealOne TestGoneAway")
	agentMemLintSaveBody(t, c, "adrs", "ADR 0130 and ADR 4242")
	agentMemLintSaveBody(t, c, "terms", "uses the Retired Thing here")
	agentMemLintSaveBody(t, c, "big", strings.Repeat("y", 4097))
	if code, b := markedDoJSON(t, "/agents/memory/entries/lint-terms", `{"terms":["retired thing"," ","RETIRED THING"]}`); code != http.StatusOK {
		t.Fatalf("save terms: %d %s", code, b)
	}
	AgentMemoryEnabled = func() bool { return false } // the report is the member's, not gated by the sessions' switch

	storeBefore := agentMemLintSnapshot(t, agentMemDir())
	cloneBefore := agentMemLintSnapshot(t, clone)
	w := agentMemLintGet(t, "scope=project&project="+c.Project.ID)
	if !reflectEq(storeBefore, agentMemLintSnapshot(t, agentMemDir())) {
		t.Error("a lint run changed the memory store (a file or a usage sidecar)")
	}
	if !reflectEq(cloneBefore, agentMemLintSnapshot(t, clone)) {
		t.Error("a lint run changed the working copy")
	}
	_ = home

	if w.Ref != "origin/main" || w.Commit == "" || w.Terms != 1 || w.Report.Checked != 7 {
		t.Fatalf("header: ref=%q commit=%q terms=%d checked=%d skipped=%v", w.Ref, w.Commit, w.Terms, w.Report.Checked, w.Report.Skipped)
	}
	for _, tc := range []struct {
		mem, check string
		want       []string
	}{
		{"links", memorylint.CheckBrokenLink, []string{"ghost"}},
		{"paths", memorylint.CheckMissingPath, []string{"console/only-local.go", "console/src/gone.tsx"}},
		{"syms", memorylint.CheckMissingSym, []string{"TestGoneAway", "localOnlyFn"}},
		{"adrs", memorylint.CheckMissingADR, []string{"ADR 4242"}},
		{"terms", memorylint.CheckRetiredTerm, []string{"retired thing"}},
		{"big", memorylint.CheckOversizeBody, []string{"4097"}},
		{"target", memorylint.CheckBrokenLink, nil},
	} {
		got := agentMemLintSubjects(w, tc.mem, tc.check)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s/%s = %v, want %v", tc.mem, tc.check, got, tc.want)
		}
	}
	if len(w.Report.Skipped) != 0 {
		t.Errorf("skipped = %v", w.Report.Skipped)
	}
	if got := agentMemLoadUsage("projects/" + c.Project.ID)["links"]; got != 0 {
		t.Errorf("lint counted a use: %d", got)
	}
}

func reflectEq(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func markedDoJSON(t *testing.T, path, body string) (int, []byte) {
	t.Helper()
	w := markedDo(t, buildMux(), http.MethodPost, path, body)
	return w.Code, w.Body.Bytes()
}

// A project whose repository has no origin default branch, a project without git, an orphan and
// the user scope run the link, term and size checks only, and the report says what was skipped.
func TestAgentMemoryLintWithoutRepositoryRunsThreeChecks(t *testing.T) {
	home, clone, _ := agentMemTestEnv(t)
	c := agentMemCallerT(t, "claude-main")
	body := "[[ghost]] console/src/gone.tsx `agentMemVanished` ADR 4242 retiredword " + strings.Repeat("z", 4096)
	agentMemLintSaveBody(t, c, "m", body)
	if code, b := markedDoJSON(t, "/agents/memory/entries/lint-terms", `{"terms":["retiredword"]}`); code != http.StatusOK {
		t.Fatalf("%d %s", code, b)
	}
	check := func(label string, w agentMemLintWire) {
		t.Helper()
		got := map[string]int{}
		for _, f := range w.Report.Findings {
			got[f.Check]++
		}
		want := map[string]int{memorylint.CheckBrokenLink: 1, memorylint.CheckRetiredTerm: 1, memorylint.CheckOversizeBody: 1}
		if len(got) != 3 || got[memorylint.CheckBrokenLink] != 1 || got[memorylint.CheckRetiredTerm] != 1 || got[memorylint.CheckOversizeBody] != 1 {
			t.Errorf("%s: findings %v, want %v", label, got, want)
		}
		if len(w.Report.Skipped) != 1 || !strings.Contains(w.Report.Skipped[0], "repository checks") {
			t.Errorf("%s: skipped = %v", label, w.Report.Skipped)
		}
	}
	// A git project with no origin/HEAD and no origin/main.
	check("no origin", agentMemLintGet(t, "scope=project&project="+c.Project.ID))

	// An orphan: the working copy is gone (another directory keeps ~/repos non-empty).
	agentMemLintOrigin(t, clone, map[string]string{"console/a.go": "package a\n"})
	if err := os.MkdirAll(filepath.Join(home, "repos", "keep"), 0o755); err != nil {
		t.Fatal(err)
	}
	w := agentMemLintGet(t, "scope=project&project="+c.Project.ID)
	if w.Ref == "" {
		t.Fatalf("with origin/main the repository checks should run: %v", w.Report.Skipped)
	}
	if err := os.RemoveAll(clone); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(home, "repos", "demo@feature-x")); err != nil {
		t.Fatal(err)
	}
	w = agentMemLintGet(t, "scope=project&project="+c.Project.ID)
	check("orphan", w)
	if !strings.Contains(w.Report.Skipped[0], "orphaned") {
		t.Errorf("orphan reason: %v", w.Report.Skipped)
	}

	// The user scope.
	uc := agentMemCaller{Session: "claude-main", Kind: "claude"}
	if _, err := agentMemSave(uc, agentMemSaveReq{Name: "u", Description: "d", Scope: agentMemScopeUser, Type: "user", Body: body}, time.Now()); err != nil {
		t.Fatal(err)
	}
	check("user", agentMemLintGet(t, "scope=user"))
}

// The term list answers only a member-origin request, round-trips, and refuses oversized input.
func TestAgentMemoryLintTermsAreMemberEdited(t *testing.T) {
	_, _, _ = agentMemTestEnv(t)
	// Without a configured key the route answers 503 (the feature is off, not open); the 403 of
	// a refused mark needs one, so the test installs its own instead of relying on the host's.
	memberorigintest.Install(t)
	if code, _ := memberDo(t, http.MethodPost, "/agents/memory/entries/lint-terms", map[string]any{"terms": []string{"x"}}); code != http.StatusForbidden {
		t.Fatalf("unsigned save: %d, want 403", code)
	}
	if code, b := memberDo(t, http.MethodGet, "/agents/memory/entries/lint-terms", nil); code != http.StatusOK || !strings.Contains(string(b), `"terms":[]`) {
		t.Fatalf("empty list: %d %s", code, b)
	}
	if code, b := markedDoJSON(t, "/agents/memory/entries/lint-terms", `{"terms":["Alpha","alpha","  beta  ",""]}`); code != http.StatusOK || !strings.Contains(string(b), `["Alpha","beta"]`) {
		t.Fatalf("save: %d %s", code, b)
	}
	if _, b := memberDo(t, http.MethodGet, "/agents/memory/entries/lint-terms", nil); !strings.Contains(string(b), `"Alpha"`) {
		t.Fatalf("not persisted: %s", b)
	}
	long, _ := json.Marshal(map[string]any{"terms": []string{strings.Repeat("a", 101)}})
	if code, _ := markedDoJSON(t, "/agents/memory/entries/lint-terms", string(long)); code != http.StatusBadRequest {
		t.Fatalf("long term: %d, want 400", code)
	}
	if code, _ := memberDo(t, http.MethodGet, "/agents/memory/entries/lint?scope=project&project=nope", nil); code < 400 {
		t.Fatalf("unknown project: %d", code)
	}
}

// The repository checks read one revision: after origin/main moves, a lint of the same call reads
// the commit it resolved first. The reader is pinned to the commit id, and the report shows it.
func TestAgentMemoryLintReaderIsPinnedToACommit(t *testing.T) {
	_, clone, _ := agentMemTestEnv(t)
	agentMemLintOrigin(t, clone, map[string]string{"console/a.go": "package a\nfunc oldSym() {}\n"})
	c := agentMemCallerT(t, "claude-main")
	agentMemLintSaveBody(t, c, "m", "console/a.go `oldSym` console/b.go")
	p := agentMemProjectInfo(c.Project.ID)
	repo, ref, why := agentMemLintRepoFor(p)
	if repo == nil {
		t.Fatalf("no reader: %s", why)
	}
	if ref != "origin/main" {
		t.Errorf("displayed ref = %q", ref)
	}
	r := repo.(agentMemLintRepo)
	if len(r.ref) != 40 {
		t.Fatalf("reader ref %q is not a full commit id", r.ref)
	}
	// Move origin/main to a commit with a different tree; the pinned reader must not follow.
	agentMemLintOrigin(t, clone, map[string]string{"console/b.go": "package b\nfunc newSym() {}\n"})
	paths, err := r.Paths()
	if err != nil || !containsString(paths, "console/a.go") || containsString(paths, "console/b.go") {
		t.Fatalf("pinned reader saw the moved ref: %v %v", paths, err)
	}
	found, err := r.FindSymbols([]string{"oldSym", "newSym"})
	if err != nil || !found["oldSym"] || found["newSym"] {
		t.Fatalf("symbols after the ref moved: %v %v", found, err)
	}
	w := agentMemLintGet(t, "scope=project&project="+c.Project.ID)
	if len(w.Commit) != 12 {
		t.Errorf("commit = %q", w.Commit)
	}
}

// Numbered short references and git-ignored paths are not missing, judged by the .gitignore the
// revision commits: a working copy that edited or deleted its .gitignore changes nothing. A real
// miss and a number with no file are still reported.
func TestAgentMemoryLintShortRefsAndIgnoredPathsByRevision(t *testing.T) {
	_, clone, _ := agentMemTestEnv(t)
	agentMemLintOrigin(t, clone, map[string]string{
		".gitignore":              "node_modules\n/console/dist/\n",
		"console/src/app.tsx":     "export const App = 1\n",
		"docs/log/99-lcpp.md":     "# 99\n",
		"guide/admin/03-setup.md": "# 03\n",
	})
	if err := os.WriteFile(filepath.Join(clone, ".gitignore"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	c := agentMemCallerT(t, "claude-main")
	agentMemLintSaveBody(t, c, "m", "docs/log/99 docs/51 guide/admin/03 console/dist console/node_modules/x/y.js "+
		"docs/log/9999 console/src/gone.tsx")
	AgentMemoryEnabled = func() bool { return false }
	w := agentMemLintGet(t, "scope=project&project="+c.Project.ID)
	got := agentMemLintSubjects(w, "m", "missing-path")
	want := map[string]bool{"docs/51": true, "docs/log/9999": true, "console/src/gone.tsx": true}
	if len(got) != len(want) {
		t.Fatalf("missing-path = %v (skipped %v)", got, w.Report.Skipped)
	}
	for _, s := range got {
		if !want[s] {
			t.Errorf("unexpected %q in %v", s, got)
		}
	}
}
