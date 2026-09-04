package memoryx

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// memoryTestEnv builds an isolated HOME mimicking the claude/codex live trees. Next to the
// memory md files it always places, as in production, the things that must not be swept in:
// transcripts, credentials, settings, the derived-state sqlite, codex's own .git, and a
// symlink escaping the allowlist.
func memoryTestEnv(t *testing.T) (home, cfg, slug string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home = t.TempDir()
	cfg = filepath.Join(home, "claude-config")
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(home, "sessions"))

	slug = "-home-dev-repos-demo"
	proj := filepath.Join(cfg, "projects", slug)
	memoryMkdirAll(t, filepath.Join(proj, "memory", "nested"))
	memoryMkdirAll(t, filepath.Join(proj, "subagents"))
	memoryWrite(t, filepath.Join(proj, "memory", "MEMORY.md"), "- [a](a.md) — hook\n")
	memoryWrite(t, filepath.Join(proj, "memory", "a.md"), "first\n")
	memoryWrite(t, filepath.Join(proj, "memory", "nested", "b.md"), "nested\n")

	// --- must never end up in the repo (★1) ---
	memoryWrite(t, filepath.Join(proj, "abcd-1234.jsonl"), `{"type":"user"}`)                 // transcript
	memoryWrite(t, filepath.Join(proj, "subagents", "agent-1.jsonl"), `{"type":"assistant"}`) // subagent transcript
	memoryWrite(t, filepath.Join(cfg, ".credentials.json"), `{"token":"SECRET"}`)
	memoryWrite(t, filepath.Join(cfg, "settings.json"), `{"statusLine":{}}`)
	memoryWrite(t, filepath.Join(cfg, "af-usage.json"), `{"five_hour":{}}`)
	// A symlink leading from inside the allowlist to the outside; following it reaches the
	// credentials.
	_ = os.Symlink(filepath.Join(cfg, ".credentials.json"), filepath.Join(proj, "memory", "leak.md"))

	// codex side: mimic an environment where the memories feature is enabled.
	codex := filepath.Join(home, ".codex", "memories")
	memoryMkdirAll(t, filepath.Join(codex, ".git", "objects"))
	memoryMkdirAll(t, filepath.Join(codex, "skills", "demo"))
	memoryWrite(t, filepath.Join(codex, "MEMORY.md"), "codex index\n")
	memoryWrite(t, filepath.Join(codex, "skills", "demo", "SKILL.md"), "skill\n")
	memoryWrite(t, filepath.Join(codex, ".git", "config"), "[core]\n")
	memoryWrite(t, filepath.Join(codex, ".git", "objects", "pack"), "binary")
	memoryWrite(t, filepath.Join(codex, "phase2_workspace_diff.md"), "intermediate\n")
	memoryWrite(t, filepath.Join(home, ".codex", "memories_1.sqlite"), "sqlite")
	return home, cfg, slug
}

func memoryWrite(t *testing.T, p, s string) {
	t.Helper()
	memoryMkdirAll(t, filepath.Dir(p))
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

// memoryTree returns the repo's HEAD tree (every path).
func memoryTree(t *testing.T) []string {
	t.Helper()
	out, err := memoryGitRun("ls-tree", "-r", "--name-only", "HEAD")
	if err != nil {
		t.Fatalf("ls-tree: %v", err)
	}
	var paths []string
	for _, p := range strings.Split(out, "\n") {
		if p = strings.TrimSpace(p); p != "" {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths
}

func memoryCommitCount(t *testing.T) int {
	t.Helper()
	out, err := memoryGitRun("rev-list", "--count", memoryBranch)
	if err != nil {
		return 0
	}
	n := 0
	for _, r := range out {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// Regression detection for the ★1 collateral-capture accident. None of transcripts,
// credentials, settings, the derived-state sqlite, codex's own .git or a symlink escaping the
// allowlist may appear in the repo. If this fails the allowlist is broken - fix the allowlist,
// do not add a deny.
func TestMemorySnapshotOnlyCapturesAllowlistedFiles(t *testing.T) {
	_, _, slug := memoryTestEnv(t)

	res, err := memorySnapshot(memoryTriggerManual, time.Now())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !res.Committed {
		t.Fatal("first snapshot did not commit")
	}

	got := memoryTree(t)
	want := []string{
		"claude/projects/" + slug + "/memory/MEMORY.md",
		"claude/projects/" + slug + "/memory/a.md",
		"claude/projects/" + slug + "/memory/nested/b.md",
		"codex/MEMORY.md",
		"codex/skills/demo/SKILL.md",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("captured tree mismatch\n got: %v\nwant: %v", got, want)
	}

	// Explicit negative check by name, so the intent survives even if the tree comparison is
	// later relaxed.
	joined := strings.Join(got, "\n")
	for _, forbidden := range []string{
		".credentials.json", "settings.json", "af-usage.json",
		".jsonl", "memories_1.sqlite", "codex/.git", "phase2_workspace_diff.md", "leak.md",
	} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("forbidden entry %q leaked into the repo tree: %v", forbidden, got)
		}
	}
	// No trace is left in the live tree: no .git is created, and codex's own .git is untouched.
	if _, err := os.Stat(filepath.Join(claudeProjectsDirForTest(), slug, "memory", ".git")); err == nil {
		t.Error("snapshot created a .git inside the live memory dir")
	}
	// The credential contents are nowhere in the repo, blob contents included.
	if blobs, _ := memoryGitRun("grep", "-I", "-l", "SECRET", memoryBranch); strings.TrimSpace(blobs) != "" {
		t.Errorf("credential contents reachable in repo: %s", blobs)
	}
}

// Snapshot round trip: the second run has no changes, so it must not commit (empty commits
// would pollute the history). After a change it is stacked, and the trigger and the changed
// projects can be recovered from the trailers.
func TestMemorySnapshotSkipsWhenUnchanged(t *testing.T) {
	_, cfg, slug := memoryTestEnv(t)

	first, err := memorySnapshot(memoryTriggerAuto, time.Now())
	if err != nil || !first.Committed {
		t.Fatalf("first snapshot: %+v err=%v", first, err)
	}
	if n := memoryCommitCount(t); n != 1 {
		t.Fatalf("after first snapshot: %d commits", n)
	}

	second, err := memorySnapshot(memoryTriggerAuto, time.Now())
	if err != nil {
		t.Fatalf("second snapshot: %v", err)
	}
	if second.Committed {
		t.Fatalf("unchanged snapshot committed anyway: %+v", second)
	}
	if n := memoryCommitCount(t); n != 1 {
		t.Fatalf("unchanged snapshot changed the history: %d commits", n)
	}

	// Rewriting one memory file gets a snapshot stacked.
	memoryWrite(t, filepath.Join(cfg, "projects", slug, "memory", "a.md"), "second\n")
	third, err := memorySnapshot(memoryTriggerManual, time.Now())
	if err != nil || !third.Committed {
		t.Fatalf("changed snapshot: %+v err=%v", third, err)
	}
	if n := memoryCommitCount(t); n != 2 {
		t.Fatalf("after change: %d commits", n)
	}
	wantPath := "claude/projects/" + slug + "/memory/a.md"
	if len(third.Changed) != 1 || third.Changed[0] != wantPath {
		t.Fatalf("changed paths = %v, want [%s]", third.Changed, wantPath)
	}
	if len(third.Projects) != 1 || third.Projects[0].Slug != slug {
		t.Fatalf("changed projects = %+v", third.Projects)
	}

	// A deletion on the live side also shows up as a diff, i.e. staging is rebuilt every time.
	if err := os.Remove(filepath.Join(cfg, "projects", slug, "memory", "nested", "b.md")); err != nil {
		t.Fatal(err)
	}
	fourth, err := memorySnapshot(memoryTriggerAuto, time.Now())
	if err != nil || !fourth.Committed {
		t.Fatalf("deletion snapshot: %+v err=%v", fourth, err)
	}
	for _, p := range memoryTree(t) {
		if strings.HasSuffix(p, "nested/b.md") {
			t.Fatalf("deleted memory still present in tree: %v", memoryTree(t))
		}
	}
}

// The history API is assembled from git log alone: newest first, with the trigger and the
// changed projects recoverable. ★5: commits use a dedicated identity and inherit nothing from
// the user's ~/.gitconfig.
func TestMemoryListSnapshotsAndIdentity(t *testing.T) {
	_, cfg, slug := memoryTestEnv(t)
	if _, err := memorySnapshot(memoryTriggerAuto, time.Now()); err != nil {
		t.Fatal(err)
	}
	memoryWrite(t, filepath.Join(cfg, "projects", slug, "memory", "a.md"), "again\n")
	if _, err := memorySnapshot(memoryTriggerManual, time.Now()); err != nil {
		t.Fatal(err)
	}

	list, err := memoryListSnapshots(10, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 snapshots, got %d: %+v", len(list), list)
	}
	if list[0].Trigger != memoryTriggerManual || list[1].Trigger != memoryTriggerAuto {
		t.Fatalf("triggers not recovered from trailers: %q %q", list[0].Trigger, list[1].Trigger)
	}
	if list[0].Files != 1 || len(list[0].Projects) != 1 || list[0].Projects[0].Slug != slug {
		t.Fatalf("newest snapshot summary: %+v", list[0])
	}
	if list[0].Projects[0].Display != "demo" {
		t.Errorf("slug should be shown as a repo name, got %q", list[0].Projects[0].Display)
	}
	if len(list[1].Kinds) != 2 {
		t.Errorf("first snapshot should touch both kinds, got %v", list[1].Kinds)
	}
	if _, err := time.Parse(time.RFC3339, list[0].At); err != nil {
		t.Errorf("At is not RFC3339: %q", list[0].At)
	}

	who, err := memoryGitRun("log", "-1", "--pretty=%an <%ae> / %cn")
	if err != nil {
		t.Fatal(err)
	}
	if who != "af-memory <af-memory@agent-fleet.local> / af-memory" {
		t.Errorf("commit identity leaked from the user's gitconfig: %q", who)
	}

	// diff: the change the newest snapshot introduced is readable.
	diff, err := memoryDiff("", list[0].Rev, "")
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if !strings.Contains(diff, "+again") {
		t.Errorf("diff does not carry the change:\n%s", diff)
	}
	// The first snapshot, which has no parent, can be diffed too.
	if d, err := memoryDiff("", list[1].Rev, ""); err != nil || !strings.Contains(d, "codex index") {
		t.Errorf("root-commit diff failed: err=%v\n%s", err, d)
	}
	// The path scope stays inside the declared prefixes.
	if _, err := memoryDiff("", list[0].Rev, "../escape"); err == nil {
		t.Error("path scope escaped the declared roots")
	}
}

// claudeProjectsDirForTest is the live projects directory, for locating things inside tests.
func claudeProjectsDirForTest() string { return memoryRootDecls()[0].Dir }
