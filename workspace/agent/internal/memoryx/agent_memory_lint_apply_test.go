package memoryx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var laNow = time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)

type lintApplyEnv struct {
	t     *testing.T
	clone string
	c     agentMemCaller
	pid   string
	rel   string
}

func newLintApplyEnv(t *testing.T, tree map[string]string) *lintApplyEnv {
	_, clone, _ := agentMemTestEnv(t)
	agentMemLintOrigin(t, clone, tree)
	c := agentMemCallerT(t, "claude-main")
	rel, _ := agentMemScopeDir(agentMemScopeProject, c)
	return &lintApplyEnv{t: t, clone: clone, c: c, pid: c.Project.ID, rel: rel}
}

func (e *lintApplyEnv) save(name, body string, rev int) {
	e.t.Helper()
	if _, err := agentMemSave(e.c, agentMemSaveReq{Name: name, Description: "d", Type: "project", Body: body, Revision: rev}, laNow); err != nil {
		e.t.Fatal(err)
	}
}

func (e *lintApplyEnv) apply() agentMemLintApplyOut {
	e.t.Helper()
	out, err := agentMemLintApply(agentMemScopeProject, e.pid, laNow)
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

func (e *lintApplyEnv) stored(name string) agentMemEntry {
	return lifeStored(e.t, e.c, agentMemScopeProject, name)
}

func (e *lintApplyEnv) head() string {
	h, _ := memoryGitRun("rev-parse", memoryBranch)
	return h
}

func lintReasons(en agentMemEntry, by string) []agentMemReason {
	var out []agentMemReason
	for _, r := range en.Reasons {
		if r.By == by {
			out = append(out, r)
		}
	}
	return out
}

func TestLintApplyWritesReasonsAsLintAndIsIdempotent(t *testing.T) {
	e := newLintApplyEnv(t, map[string]string{"docs/real.md": "x\n"})
	e.save("m", "see docs/real.md and docs/gone/file.md", 0)
	e.save("clean", "see docs/real.md", 0)
	out := e.apply()
	if out.Marked != 1 {
		t.Fatalf("out = %+v", out)
	}
	got := e.stored("m")
	want := agentMemReason{By: "lint", Rule: "missing-path", Note: "docs/gone/file.md", Revision: 2}
	if len(got.Reasons) != 1 || got.Reasons[0] != want || got.Revision != 2 || got.AuthorKind != "lint" {
		t.Fatalf("m = revision %d author %s reasons %+v", got.Revision, got.AuthorKind, got.Reasons)
	}
	if c := e.stored("clean"); c.Revision != 1 || len(c.Reasons) != 0 {
		t.Errorf("a clean memory was written: %+v", c)
	}
	msg, _ := memoryGitRun("log", "-1", "--format=%B")
	if !strings.Contains(msg, "AF-Op: lint\n") || !strings.Contains(msg, "AF-Author-Kind: lint\n") {
		t.Errorf("commit message = %q", msg)
	}
	head := e.head()
	if out := e.apply(); out.Marked != 0 || e.head() != head {
		t.Errorf("a second run wrote: %+v", out)
	}
	// The change is revertible like any other.
	if _, err := agentMemRevert(agentMemRevertReq{Commit: head}, laNow); err != nil {
		t.Fatal(err)
	}
	if r := e.stored("m"); len(r.Reasons) != 0 {
		t.Errorf("revert kept the reason: %+v", r.Reasons)
	}
}

// ADR 0130 decision 3, acceptance: a lint reason and a feedback reason together, then a body
// change.
func TestLintReasonClearingAfterABodyChange(t *testing.T) {
	setup := func(t *testing.T) *lintApplyEnv {
		e := newLintApplyEnv(t, map[string]string{"docs/real.md": "x\n"})
		e.save("m", "uses docs/gone.md", 0)
		e.apply() // lint reason at revision 1; the memory is at revision 2
		if _, err := agentMemFeedback(e.c, agentMemFeedbackReq{Scope: agentMemScopeProject, Name: "m", Revision: 2, Verdict: "stale", Note: "also wrong"}, laNow); err != nil {
			t.Fatal(err)
		}
		if g := e.stored("m"); len(g.Reasons) != 2 {
			t.Fatalf("setup reasons = %+v", g.Reasons)
		}
		return e
	}
	t.Run("a change that does not fix the cause", func(t *testing.T) {
		e := setup(t)
		e.save("m", "uses docs/gone.md, punctuation fixed.", 2)
		g := e.stored("m")
		if len(g.Reasons) != 1 || g.Reasons[0].By != "lint" {
			t.Fatalf("after the change: %+v", g.Reasons)
		}
		e.apply()
		g = e.stored("m")
		if lr := lintReasons(g, "lint"); len(lr) != 1 || lr[0].Rule != "missing-path" || len(g.Reasons) != 1 {
			t.Errorf("after the next lint run: %+v", g.Reasons)
		}
	})
	t.Run("a change that also fixes it", func(t *testing.T) {
		e := setup(t)
		e.save("m", "uses docs/real.md", 2)
		if g := e.stored("m"); len(lintReasons(g, "lint")) != 1 {
			t.Fatalf("the lint reason must stay until the next run: %+v", g.Reasons)
		}
		e.apply()
		if g := e.stored("m"); len(g.Reasons) != 0 {
			t.Errorf("the next run left %+v", g.Reasons)
		}
	})
}

// A lint reason is not cleared on the revision it was found on (nothing newer was checked), and a
// rule that did not run clears nothing.
func TestLintReasonStaysWhenNotRecheckedOrRuleDidNotRun(t *testing.T) {
	e := newLintApplyEnv(t, map[string]string{"docs/real.md": "x\n"})
	e.save("m", "uses docs/gone.md", 0)
	e.apply()
	// The path appears on the default branch; the memory itself did not move.
	agentMemLintOrigin(t, e.clone, map[string]string{"docs/gone.md": "now here\n"})
	e.apply()
	if g := e.stored("m"); len(g.Reasons) != 1 {
		t.Errorf("cleared on the revision it was found on: %+v", g.Reasons)
	}
	// Seen from the user scope there is no repository: a path reason there is never cleared.
	if _, err := agentMemSave(e.c, agentMemSaveReq{Scope: agentMemScopeUser, Name: "u", Description: "d", Type: "user", Body: "plain"}, laNow); err != nil {
		t.Fatal(err)
	}
	lifeSet(t, e.c, agentMemScopeUser, "u", func(m *agentMemEntry) {
		m.Reasons = []agentMemReason{{By: "lint", Rule: "missing-path", Note: "docs/x.md", Revision: 0}}
	})
	if _, err := agentMemLintApply(agentMemScopeUser, "", laNow); err != nil {
		t.Fatal(err)
	}
	if g := lifeStored(t, e.c, agentMemScopeUser, "u"); len(g.Reasons) != 1 {
		t.Errorf("a rule that did not run cleared its reason: %+v", g.Reasons)
	}
}

// The revision the check read is a compare-and-swap: a memory an agent saved meanwhile is left.
func TestLintWriteIsACompareAndSwapOnTheCheckedRevision(t *testing.T) {
	e := newLintApplyEnv(t, map[string]string{"docs/real.md": "x\n"})
	e.save("m", "v1", 0)
	checked := e.stored("m")
	e.save("m", "v2", 1) // the agent saves after the check
	head := e.head()
	wrote, err := agentMemLintWrite(agentMemLintCaller(e.c), agentMemScopeProject, checked, laNow,
		func(cur *agentMemEntry) (bool, []string) {
			cur.Reasons = []agentMemReason{{By: "lint", Rule: "missing-path", Note: "x", Revision: 1}}
			return true, nil
		}, agentMemOpLint)
	if err != errAgentMemLintMoved || wrote || e.head() != head {
		t.Fatalf("wrote=%v err=%v", wrote, err)
	}
	if g := e.stored("m"); g.Body != "v2" || len(g.Reasons) != 0 || g.Revision != 2 {
		t.Errorf("m = %+v", g)
	}
}

// Lint applying while agents save: no save is lost, no write is torn, and the store ends parseable.
func TestLintApplyWhileAgentsSave(t *testing.T) {
	e := newLintApplyEnv(t, map[string]string{"docs/real.md": "x\n"})
	for i := 0; i < 6; i++ {
		e.save(fmt.Sprintf("m%d", i), "uses docs/gone.md", 0)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 3; i++ {
			if _, err := agentMemLintApply(agentMemScopeProject, e.pid, laNow); err != nil {
				t.Error(err)
			}
		}
	}()
	final := map[string]string{}
	var mu sync.Mutex
	go func() {
		defer wg.Done()
		for i := 0; i < 6; i++ {
			name := fmt.Sprintf("m%d", i)
			for try := 0; try < 20; try++ {
				cur := e.stored(name)
				body := fmt.Sprintf("saved by agent %d", i)
				_, err := agentMemSave(e.c, agentMemSaveReq{Name: name, Description: "d", Type: "project", Body: body, Revision: cur.Revision}, laNow)
				if err == nil {
					mu.Lock()
					final[name] = body
					mu.Unlock()
					break
				}
				if agentMemCode(err) != errCodeMemoryConflict {
					t.Error(err)
					return
				}
			}
		}
	}()
	wg.Wait()
	for name, body := range final {
		if g := e.stored(name); g.Body != body {
			t.Errorf("%s: the agent's save was lost: %q", name, g.Body)
		}
	}
	if len(final) != 6 {
		t.Errorf("only %d saves landed", len(final))
	}
}

// A first run over a big store is a bounded number of commits, and the next run continues.
func TestLintApplyBoundsTheWriteStorm(t *testing.T) {
	old := agentMemLintApplyMax
	agentMemLintApplyMax = 3
	t.Cleanup(func() { agentMemLintApplyMax = old })
	e := newLintApplyEnv(t, map[string]string{"docs/real.md": "x\n"})
	for i := 0; i < 8; i++ {
		e.save(fmt.Sprintf("m%d", i), "uses docs/gone.md", 0)
	}
	if out := e.apply(); out.Marked != 3 || out.Deferred != 5 {
		t.Fatalf("first run = %+v", out)
	}
	if out := e.apply(); out.Marked != 3 || out.Deferred != 2 {
		t.Fatalf("second run = %+v", out)
	}
	if out := e.apply(); out.Marked != 2 || out.Deferred != 0 {
		t.Fatalf("third run = %+v", out)
	}
	head := e.head()
	if out := e.apply(); out.Marked != 0 || e.head() != head {
		t.Fatalf("fourth run = %+v", out)
	}
}

func lintRenameFixture(t *testing.T) *lintApplyEnv {
	_, clone, _ := agentMemTestEnv(t)
	for p, body := range map[string]string{"docs/old.md": "same text\n", "docs/keep.md": "k\n"} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(clone, p)), 0o755)
		_ = os.WriteFile(filepath.Join(clone, p), []byte(body), 0o644)
	}
	steps := [][]string{{"add", "-A"}, {"commit", "--quiet", "-m", "add"}, {"mv", "docs/old.md", "docs/new.md"}, {"commit", "--quiet", "-m", "rename"},
		{"update-ref", "refs/remotes/origin/main", "HEAD"}, {"symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main"}}
	for _, a := range steps {
		if err := agentMemGit(clone, a...); err != nil {
			t.Fatal(err)
		}
	}
	c := agentMemCallerT(t, "claude-main")
	rel, _ := agentMemScopeDir(agentMemScopeProject, c)
	return &lintApplyEnv{t: t, clone: clone, c: c, pid: c.Project.ID, rel: rel}
}

func TestLintRenameReplacesOnlyTheRenamedPathWithEvidence(t *testing.T) {
	e := lintRenameFixture(t)
	body := "Read docs/old.md first. Not docs/sub/docs/old.md, nor docs/old.md.bak, nor docs/old.mdx. Also docs/gone.md."
	if _, err := agentMemSave(e.c, agentMemSaveReq{Name: "m", Description: "d", Type: "project", Body: body, Anchors: &[]string{"docs/old.md"}}, laNow); err != nil {
		t.Fatal(err)
	}
	out := e.apply()
	if out.Renamed != 1 || out.Marked != 1 {
		t.Fatalf("out = %+v", out)
	}
	g := e.stored("m")
	wantBody := "Read docs/new.md first. Not docs/sub/docs/old.md, nor docs/old.md.bak, nor docs/old.mdx. Also docs/gone.md."
	if g.Body != wantBody || g.Anchors[0] != "docs/new.md" {
		t.Fatalf("body = %q anchors %v", g.Body, g.Anchors)
	}
	// The rename is its own commit, with the renaming commit as evidence; the reasons are another.
	log, _ := memoryGitRun("log", "--format=%B%x00", "-n", "2")
	if !strings.Contains(log, "AF-Op: lint-rename\n") || !strings.Contains(log, "AF-Lint-Rename: docs/old.md -> docs/new.md @ ") || !strings.Contains(log, "AF-Op: lint\n") {
		t.Errorf("log = %q", log)
	}
	// The old path is gone from the repository and from what lint still reports as missing.
	for _, r := range g.Reasons {
		if r.Note == "docs/old.md" {
			t.Errorf("a reason for the replaced path: %+v", r)
		}
	}
}

func TestLintRenameIsRevertible(t *testing.T) {
	e := lintRenameFixture(t)
	e.save("m", "Read docs/old.md first.", 0)
	if out := e.apply(); out.Renamed != 1 || out.Marked != 0 {
		t.Fatalf("out = %+v", out)
	}
	if g := e.stored("m"); g.Body != "Read docs/new.md first." || len(g.Reasons) != 0 {
		t.Fatalf("m = %+v", g)
	}
	if _, err := agentMemRevert(agentMemRevertReq{Commit: e.head()}, laNow); err != nil {
		t.Fatal(err)
	}
	if g := e.stored("m"); g.Body != "Read docs/old.md first." {
		t.Errorf("revert did not restore the body: %q", g.Body)
	}
}

func TestLintRenameLeavesAnAmbiguousMissingPathToTheReason(t *testing.T) {
	e := lintRenameFixture(t)
	e.save("m", "uses docs/never-existed.md", 0)
	out := e.apply()
	if out.Renamed != 0 || out.Marked != 1 {
		t.Fatalf("out = %+v", out)
	}
	if g := e.stored("m"); g.Body != "uses docs/never-existed.md" {
		t.Errorf("body = %q", g.Body)
	}
}

func TestLintReplacePathBoundaries(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a docs/old.md b", "a docs/new.md b"},
		{"(docs/old.md).", "(docs/new.md)."},
		{"docs/old.md.", "docs/new.md."},
		{"`docs/old.md`", "`docs/new.md`"},
		{"x/docs/old.md", "x/docs/old.md"},
		{"docs/old.md/sub", "docs/old.md/sub"},
		{"docs/old.mdx", "docs/old.mdx"},
		{"docs/old.md.bak", "docs/old.md.bak"},
		{"docs/old.md-2", "docs/old.md-2"},
	}
	for _, tc := range cases {
		if got, _ := agentMemLintReplacePath(tc.in, "docs/old.md", "docs/new.md"); got != tc.want {
			t.Errorf("%q -> %q, want %q", tc.in, got, tc.want)
		}
	}
}
