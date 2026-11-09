package memoryx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lintHistory builds a clone whose origin/main is the result of the steps: each step is a commit
// message and the shell-free edits to make before it (a file map to write, then git arguments).
type lintHistoryStep struct {
	msg   string
	write map[string]string
	git   [][]string
}

func lintHistoryFixture(t *testing.T, steps []lintHistoryStep) *lintApplyEnv {
	t.Helper()
	_, clone, _ := agentMemTestEnv(t)
	for _, st := range steps {
		for p, body := range st.write {
			abs := filepath.Join(clone, filepath.FromSlash(p))
			_ = os.MkdirAll(filepath.Dir(abs), 0o755)
			if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		for _, a := range st.git {
			if err := agentMemGit(clone, a...); err != nil {
				t.Fatal(err)
			}
		}
		for _, a := range [][]string{{"add", "-A"}, {"commit", "--quiet", "--allow-empty", "-m", st.msg}} {
			if err := agentMemGit(clone, a...); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, a := range [][]string{{"update-ref", "refs/remotes/origin/main", "HEAD"}, {"symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main"}} {
		if err := agentMemGit(clone, a...); err != nil {
			t.Fatal(err)
		}
	}
	c := agentMemCallerT(t, "claude-main")
	rel, _ := agentMemScopeDir(agentMemScopeProject, c)
	return &lintApplyEnv{t: t, clone: clone, c: c, pid: c.Project.ID, rel: rel}
}

func (e *lintApplyEnv) saveWith(name, body string, anchors ...string) {
	e.t.Helper()
	if _, err := agentMemSave(e.c, agentMemSaveReq{Name: name, Description: "d", Type: "project", Body: body, Anchors: &anchors}, laNow); err != nil {
		e.t.Fatal(err)
	}
}

func dirRenameHistory() []lintHistoryStep {
	return []lintHistoryStep{
		{msg: "add", write: map[string]string{
			"docs/old/a.md": "alpha\n", "docs/old/sub/b.md": "beta beta\n", "docs/old2/x.md": "x\n", "docs/keep.md": "k\n",
			"olddir/inner/p.go": "package p\n", "olddir/inner/q.go": "package q\n", "README.md": "r\n",
		}},
		{msg: "rename dirs", git: [][]string{{"mv", "docs/old", "docs/new"}, {"mv", "olddir", "newdir"}}},
	}
}

func TestLintRenameFollowsADirectoryRename(t *testing.T) {
	e := lintHistoryFixture(t, dirRenameHistory())
	e.saveWith("m", "See docs/old and docs/old/ and `docs/old/a.md`, also docs/old/sub/b.md; but docs/old2/x.md stays. Prefix docs/older/zz.md is nothing.",
		"docs/old", "docs/old/sub/b.md", "docs/old2/x.md")
	out := e.apply()
	if out.Renamed != 1 {
		t.Fatalf("out = %+v", out)
	}
	g := e.stored("m")
	want := "See docs/new and docs/new/ and `docs/new/a.md`, also docs/new/sub/b.md; but docs/old2/x.md stays. Prefix docs/older/zz.md is nothing."
	if g.Body != want {
		t.Errorf("body = %q\nwant   %q", g.Body, want)
	}
	if strings.Join(g.Anchors, ",") != "docs/new,docs/new/sub/b.md,docs/old2/x.md" {
		t.Errorf("anchors = %v", g.Anchors)
	}
	log, _ := memoryGitRun("log", "--format=%B%x00", "-n", "3")
	for _, w := range []string{"AF-Op: lint-rename\n", "AF-Lint-Rename: docs/old -> docs/new @ ", "AF-Lint-Rename: docs/old/sub/b.md -> docs/new/sub/b.md @ "} {
		if !strings.Contains(log, w) {
			t.Errorf("log lacks %q: %q", w, log)
		}
	}
}

func TestLintRenameFollowsATopLevelDirectoryRename(t *testing.T) {
	e := lintHistoryFixture(t, dirRenameHistory())
	// olddir is gone from the tree, so it only counts as a path because a rename moved files out of it.
	e.saveWith("m", "Code lives in olddir/inner and olddir/inner/p.go.", "olddir/inner")
	if out := e.apply(); out.Renamed != 1 {
		t.Fatalf("out = %+v", out)
	}
	g := e.stored("m")
	if g.Body != "Code lives in newdir/inner and newdir/inner/p.go." || g.Anchors[0] != "newdir/inner" {
		t.Errorf("m = %q %v", g.Body, g.Anchors)
	}
}

func TestLintRenameDirectoryRenameIsWholeSegmentOnly(t *testing.T) {
	steps := append(dirRenameHistory(), lintHistoryStep{msg: "unrelated", write: map[string]string{"docs/old-notes/n.md": "n\n"}})
	e := lintHistoryFixture(t, steps)
	// docs/old-notes exists and docs/oldx never did: neither shares the renamed directory's prefix in the sense that matters.
	e.saveWith("m", "docs/old-notes/n.md and docs/oldx/q.md and docs/old/a.md")
	e.apply()
	g := e.stored("m")
	if g.Body != "docs/old-notes/n.md and docs/oldx/q.md and docs/new/a.md" {
		t.Errorf("body = %q", g.Body)
	}
}

func TestLintResolveRenameDoesNotMatchAPrefixThatIsNotASegment(t *testing.T) {
	ren := agentMemLintRenames{
		File: map[string]agentMemLintRename{},
		Dir:  map[string]agentMemLintDirRename{"dir": {agentMemLintRename{To: "other", Commit: "c1"}, 1}},
	}
	all := func(string) bool { return true }
	moved := func(string, string, int) bool { return true }
	if _, ok := agentMemLintResolveRename("dir2/x", ren, all, moved); ok {
		t.Error("dir2/x resolved through dir")
	}
	if _, ok := agentMemLintResolveRename("dir-x", ren, all, moved); ok {
		t.Error("dir-x resolved through dir")
	}
	if r, ok := agentMemLintResolveRename("dir/x/y", ren, all, moved); !ok || r.To != "other/x/y" {
		t.Errorf("dir/x/y -> %+v %v", r, ok)
	}
	if r, ok := agentMemLintResolveRename("dir", ren, all, moved); !ok || r.To != "other" {
		t.Errorf("dir -> %+v %v", r, ok)
	}
	// A directory the commit did not move whole is not renamed.
	if _, ok := agentMemLintResolveRename("dir/x", ren, all, func(string, string, int) bool { return false }); ok {
		t.Error("resolved although the directory was not moved whole")
	}
}

func TestLintRenameDirectoryRenamedThenFileReaddedAtTheOldPath(t *testing.T) {
	steps := append(dirRenameHistory(), lintHistoryStep{msg: "re-add", write: map[string]string{"docs/old/c.md": "new file\n"}})
	e := lintHistoryFixture(t, steps)
	// docs/old exists again, as a different directory: it is not missing and is left alone. The
	// file that did move is still found by its own rename, and the re-added file exists.
	e.saveWith("m", "docs/old, docs/old/c.md and docs/old/a.md", "docs/old", "docs/old/c.md")
	out := e.apply()
	g := e.stored("m")
	if g.Body != "docs/old, docs/old/c.md and docs/new/a.md" || strings.Join(g.Anchors, ",") != "docs/old,docs/old/c.md" {
		t.Errorf("out %+v: body = %q anchors %v", out, g.Body, g.Anchors)
	}
}

func TestLintRenameDirectorySplitOrPartlyLeftBehindIsNotRenamed(t *testing.T) {
	steps := []lintHistoryStep{
		{msg: "add", write: map[string]string{"split/a.go": "package a\n", "split/b.go": "package b\n", "left/a.go": "package a2\n", "left/b.go": "package b2\n", "docs/keep.md": "k\n"}},
		{msg: "split", git: [][]string{{"mv", "split/a.go", "one/a.go"}, {"mv", "split/b.go", "two/b.go"}}},
		{msg: "partly", git: [][]string{{"mv", "left/a.go", "moved/a.go"}}},
		{msg: "drop the rest", git: [][]string{{"rm", "-q", "left/b.go"}}},
	}
	// git mv needs the target directories to exist.
	e := lintHistoryFixtureWithDirs(t, steps, "one", "two", "moved")
	e.saveWith("m", "see split and left and split/a.go", "split", "left")
	out := e.apply()
	g := e.stored("m")
	// split/a.go has its own file rename and is rewritten; the directories are not.
	if g.Body != "see split and left and one/a.go" || strings.Join(g.Anchors, ",") != "split,left" {
		t.Errorf("out %+v: body = %q anchors %v", out, g.Body, g.Anchors)
	}
}

func lintHistoryFixtureWithDirs(t *testing.T, steps []lintHistoryStep, dirs ...string) *lintApplyEnv {
	t.Helper()
	pre := lintHistoryStep{msg: "dirs", write: map[string]string{}}
	for _, d := range dirs {
		pre.write[d+"/.keep"] = "\n"
	}
	return lintHistoryFixture(t, append([]lintHistoryStep{pre}, steps...))
}

func TestLintRenameIgnoresRenamesOutsideTheCommitRange(t *testing.T) {
	old := agentMemLintRenameCommits
	agentMemLintRenameCommits = 2
	t.Cleanup(func() { agentMemLintRenameCommits = old })
	steps := append(dirRenameHistory(),
		// The bound counts the commits that carry renames, newest first.
		lintHistoryStep{msg: "later 1", git: [][]string{{"mv", "README.md", "READ.md"}}},
		lintHistoryStep{msg: "later 2", git: [][]string{{"mv", "docs/keep.md", "docs/kept.md"}}})
	e := lintHistoryFixture(t, steps)
	e.saveWith("m", "docs/old and docs/old/a.md", "docs/old")
	out := e.apply()
	g := e.stored("m")
	if out.Renamed != 0 || g.Body != "docs/old and docs/old/a.md" || g.Anchors[0] != "docs/old" {
		t.Errorf("out %+v: renames from before the range were applied: %q %v", out, g.Body, g.Anchors)
	}
}

func TestLintRenameRewritesAnAnchorThatIsNotInTheBody(t *testing.T) {
	e := lintHistoryFixture(t, dirRenameHistory())
	e.saveWith("m", "Nothing about paths here.", "docs/old/a.md", "docs/old")
	if out := e.apply(); out.Renamed != 1 {
		t.Fatalf("out = %+v", out)
	}
	g := e.stored("m")
	if g.Body != "Nothing about paths here." || strings.Join(g.Anchors, ",") != "docs/new/a.md,docs/new" {
		t.Errorf("m = %q %v", g.Body, g.Anchors)
	}
}

func TestLintApplyMarksAStoredSymbolAnchorTheBodyDoesNotName(t *testing.T) {
	e := newLintApplyEnv(t, map[string]string{"docs/a.md": "x\n", "main.go": "package main\nfunc liveSym() {}\n"})
	e.saveWith("m", "prose only", "goneSymbol", "liveSym", "TestVanished")
	out := e.apply()
	if out.Marked != 1 {
		t.Fatalf("out = %+v", out)
	}
	notes := map[string]bool{}
	for _, r := range lintReasons(e.stored("m"), agentMemReasonByLint) {
		notes[r.Note] = true
	}
	if !notes["goneSymbol"] || !notes["TestVanished"] || notes["liveSym"] || len(notes) != 2 {
		t.Errorf("reasons = %v", notes)
	}
}

func TestLintReplacePathDirectoryWithTrailingSlash(t *testing.T) {
	cases := []struct{ in, want string }{
		{"see docs/old/ now", "see docs/new/ now"},
		{"docs/old/", "docs/new/"},
		{"docs/old/x.md", "docs/old/x.md"},
		{"docs/old/.hidden", "docs/old/.hidden"},
		{"docs/old/\n", "docs/new/\n"},
	}
	for _, tc := range cases {
		if got, _ := agentMemLintReplacePath(tc.in, "docs/old", "docs/new"); got != tc.want {
			t.Errorf("%q -> %q, want %q", tc.in, got, tc.want)
		}
	}
}
