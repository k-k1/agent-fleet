package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// edits builds a fold function over a fixed script of edit calls, and counts how many
// times the caller actually asked for a range — that count is what proves the fold is
// incremental rather than a full rescan on every poll.
type editScript struct {
	all   []transcript.FileEdit
	calls [][2]int
}

func (s *editScript) fn(from, to int) []transcript.FileEdit {
	s.calls = append(s.calls, [2]int{from, to})
	var out []transcript.FileEdit
	for _, e := range s.all {
		if e.Idx >= from && e.Idx < to {
			out = append(out, e)
		}
	}
	return out
}

func edit(idx int, path, ts, verb string, add, del int) transcript.FileEdit {
	return transcript.FileEdit{Path: path, Cwd: "/h/repos/r", Verb: verb, Added: add, Removed: del, Idx: idx, TS: ts}
}

func setupHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", "/h")
	t.Setenv("AF_BROWSE_ROOT", "")
	forgetSessionFiles(t.Name())
	t.Cleanup(func() { forgetSessionFiles(t.Name()) })
}

func TestSessionFileTouchesFoldsPerFile(t *testing.T) {
	setupHome(t)
	s := &editScript{all: []transcript.FileEdit{
		edit(0, "/h/repos/r/a.ts", "2026-08-17T10:00:00Z", "add", 10, 0),
		edit(1, "/h/repos/r/b.ts", "2026-08-17T10:01:00Z", "edit", 2, 1),
		edit(2, "/h/repos/r/a.ts", "2026-08-17T10:02:00Z", "edit", 3, 4),
	}}
	got := sessionFileTouches(t.Name(), "/t.jsonl", "head", 3, 3, s.fn)
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(got), got)
	}
	// Newest touch first: a.ts was edited last even though b.ts appeared later in the map.
	if got[0].Rel != "a.ts" || got[1].Rel != "b.ts" {
		t.Fatalf("order = %q, %q; want a.ts then b.ts", got[0].Rel, got[1].Rel)
	}
	a := got[0]
	if a.Count != 2 || a.Added != 13 || a.Removed != 4 {
		t.Fatalf("a.ts folded wrong: %+v", a)
	}
	if a.Verb != "edit" {
		t.Fatalf("verb = %q, want the LAST call's verb (edit)", a.Verb)
	}
	if a.Repo != "r" || a.Path != "repos/r/a.ts" {
		t.Fatalf("coordinates = repo %q path %q", a.Repo, a.Path)
	}
	if a.LastIdx != 2 || a.LastTS != "2026-08-17T10:02:00Z" {
		t.Fatalf("last touch = idx %d ts %q", a.LastIdx, a.LastTS)
	}
}

func TestSessionFileTouchesIsIncremental(t *testing.T) {
	setupHome(t)
	s := &editScript{all: []transcript.FileEdit{
		edit(0, "/h/repos/r/a.ts", "2026-08-17T10:00:00Z", "edit", 1, 0),
		edit(1, "/h/repos/r/a.ts", "2026-08-17T10:01:00Z", "edit", 1, 0),
	}}
	sessionFileTouches(t.Name(), "/t.jsonl", "head", 1, 1, s.fn)
	got := sessionFileTouches(t.Name(), "/t.jsonl", "head", 2, 2, s.fn)
	if len(got) != 1 || got[0].Count != 2 || got[0].Added != 2 {
		t.Fatalf("second poll = %+v", got)
	}
	// The second poll must only have asked for the NEW range — re-running the line
	// differ over the whole transcript at poll rate is what this cache exists to avoid.
	if len(s.calls) != 2 || s.calls[1] != [2]int{1, 2} {
		t.Fatalf("fold ranges = %v, want the second to be [1 2]", s.calls)
	}
}

func TestSessionFileTouchesRefoldsWhenTheTranscriptIsReplaced(t *testing.T) {
	setupHome(t)
	s := &editScript{all: []transcript.FileEdit{edit(0, "/h/repos/r/a.ts", "2026-08-17T10:00:00Z", "edit", 1, 0)}}
	sessionFileTouches(t.Name(), "/t.jsonl", "head", 1, 1, s.fn)

	// Same path and same length, but the first record changed: the conversation was
	// rewritten in place, so the previous fold describes a transcript that no longer exists.
	got := sessionFileTouches(t.Name(), "/t.jsonl", "OTHER-head", 1, 1, s.fn)
	if len(got) != 1 || got[0].Count != 1 {
		t.Fatalf("after a rewrite = %+v (a stale fold would double the count)", got)
	}
	if s.calls[1] != [2]int{0, 1} {
		t.Fatalf("fold ranges = %v, want a restart from 0", s.calls)
	}

	// A transcript that shrank below what we folded (reset / replaced session) restarts too.
	got = sessionFileTouches(t.Name(), "/other.jsonl", "head", 1, 1, s.fn)
	if len(got) != 1 || got[0].Count != 1 {
		t.Fatalf("after a path change = %+v", got)
	}
}

func TestSessionFileTouchesDoesNotDoubleCountAMutableTail(t *testing.T) {
	setupHome(t)
	// opencode keeps appending parts to its last message, so that turn is re-folded on
	// every poll. It must not accumulate: the same turn seen twice is still one edit.
	s := &editScript{all: []transcript.FileEdit{
		edit(0, "/h/repos/r/a.ts", "2026-08-17T10:00:00Z", "edit", 1, 0),
		edit(1, "/h/repos/r/b.ts", "2026-08-17T10:01:00Z", "edit", 5, 0),
	}}
	first := sessionFileTouches(t.Name(), "/t.jsonl", "head", 2, 1, s.fn)
	second := sessionFileTouches(t.Name(), "/t.jsonl", "head", 2, 1, s.fn)
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("rows: %d then %d", len(first), len(second))
	}
	for _, r := range second {
		if r.Count != 1 {
			t.Fatalf("%s counted %d times across two polls", r.Rel, r.Count)
		}
	}
	if second[0].Rel != "b.ts" || second[0].Added != 5 {
		t.Fatalf("mutable tail row = %+v", second[0])
	}
}

func TestSessionFileTouchesSidechainOnlyWhenNoMainThreadEdit(t *testing.T) {
	setupHome(t)
	sub := edit(0, "/h/repos/r/a.ts", "2026-08-17T10:00:00Z", "edit", 1, 0)
	sub.Sidechain = true
	subOnly := edit(1, "/h/repos/r/b.ts", "2026-08-17T10:01:00Z", "edit", 1, 0)
	subOnly.Sidechain = true
	main := edit(2, "/h/repos/r/a.ts", "2026-08-17T10:02:00Z", "edit", 1, 0)
	s := &editScript{all: []transcript.FileEdit{sub, subOnly, main}}
	got := sessionFileTouches(t.Name(), "/t.jsonl", "head", 3, 3, s.fn)
	for _, r := range got {
		if r.Rel == "a.ts" && r.Sidechain {
			t.Fatal("a.ts was also edited on the main thread — it is not a subagent-only file")
		}
		if r.Rel == "b.ts" && !r.Sidechain {
			t.Fatal("b.ts was only ever touched by a subagent")
		}
	}
}

func TestSessionFileTouchesRelativePathsAndOutsideRepos(t *testing.T) {
	setupHome(t)
	rel := transcript.FileEdit{Path: "src/x.ts", Cwd: "/h/repos/r", Verb: "edit", Idx: 0, TS: "1"}
	noCwd := transcript.FileEdit{Path: "src/y.ts", Cwd: "", Verb: "edit", Idx: 1, TS: "2"}
	outside := transcript.FileEdit{Path: "/h/.claude/settings.json", Verb: "edit", Idx: 2, TS: "3"}
	s := &editScript{all: []transcript.FileEdit{rel, noCwd, outside}}
	got := sessionFileTouches(t.Name(), "/t.jsonl", "head", 3, 3, s.fn)
	if len(got) != 2 {
		// A relative path with no cwd has nothing to anchor it; guessing would open the
		// same-named file in whichever working copy happened to be first.
		t.Fatalf("got %d rows, want 2 (the cwd-less relative path is dropped): %+v", len(got), got)
	}
	byRel := map[string]transcript.FileTouch{}
	for _, r := range got {
		byRel[r.Path] = r
	}
	if r, ok := byRel["repos/r/src/x.ts"]; !ok || r.Repo != "r" || r.Rel != "src/x.ts" {
		t.Fatalf("relative path not anchored on cwd: %+v", got)
	}
	// Outside ~/repos: still listed (it WAS edited), but with no git side to join against.
	if r, ok := byRel[".claude/settings.json"]; !ok || r.Repo != "" || r.Rel != "" {
		t.Fatalf("outside-repos row = %+v", got)
	}
}

func TestRepoRelOf(t *testing.T) {
	t.Setenv("HOME", "/h")
	cases := []struct{ abs, repo, rel string }{
		{"/h/repos/r/a.ts", "r", "a.ts"},
		{"/h/repos/agent-fleet@wip-x/console/src/a.ts", "agent-fleet@wip-x", "console/src/a.ts"},
		{"/h/repos/r", "", ""},    // the working copy itself, not a file in it
		{"/h/other/a.ts", "", ""}, // outside ~/repos
		{"/etc/passwd", "", ""},   //
	}
	for _, c := range cases {
		repo, rel := repoRelOf(filepath.Clean(c.abs))
		if repo != c.repo || rel != c.rel {
			t.Fatalf("repoRelOf(%q) = %q,%q want %q,%q", c.abs, repo, rel, c.repo, c.rel)
		}
	}
}

func TestCommittedSinceDegradesToEmpty(t *testing.T) {
	// バッジは「肯定できるときだけ」出す。git 作業コピーでない・時刻が読めない・
	// そもそも dir が無い、のいずれでも空を返して「差分なし」のままにする。
	cases := []struct{ name, dir, since string }{
		{"dir が空", "", "2026-08-17T10:00:00Z"},
		{"時刻が空", t.TempDir(), ""},
		{"時刻が壊れている", t.TempDir(), "きのう"},
		{"git 作業コピーではない", t.TempDir(), "2026-08-17T10:00:00Z"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := committedSince(c.dir, c.since); len(got) != 0 {
				t.Fatalf("committedSince = %v, want empty", got)
			}
		})
	}
}

func TestCommittedSinceListsPathsFromCommitsInTheWindow(t *testing.T) {
	dir := t.TempDir()
	if _, err := gitx.Run(dir, "init", "-q"); err != nil {
		t.Skipf("git を実行できない環境: %v", err)
	}
	git := func(args ...string) {
		t.Helper()
		if out, err := gitx.Run(dir, args...); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	git("config", "user.email", "t@example.com")
	git("config", "user.name", "T")
	write := func(rel, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// --since は COMMITTER date を見るので、そこを動かす（--date は author 側だけ）。
	commitAt := func(when, msg string) {
		t.Helper()
		t.Setenv("GIT_COMMITTER_DATE", when)
		t.Setenv("GIT_AUTHOR_DATE", when)
		git("add", "-A")
		git("commit", "-q", "-m", msg)
	}

	write("old.txt", "1")
	commitAt("2020-01-01T00:00:00Z", "before") // セッションが始まる前
	write("src/a.ts", "x")
	write("docs/b.md", "y")
	commitAt("2026-01-01T00:00:00Z", "after") // セッション開始以降

	got := committedSince(dir, "2021-01-01T00:00:00Z")
	set := map[string]bool{}
	for _, p := range got {
		set[p] = true
	}
	if !set["src/a.ts"] || !set["docs/b.md"] {
		t.Fatalf("窓の中のコミットのパスが落ちている: %v", got)
	}
	// ⚠️ 窓の外は入れない。入れると「コミット済み」が常に真になり、バッジが情報を失う。
	if set["old.txt"] {
		t.Fatalf("セッション開始より前のコミットまで拾っている: %v", got)
	}
}
