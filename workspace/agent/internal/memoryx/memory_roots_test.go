package memoryx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func memoryMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

// docs/log/39 ★1: pins that the allowlist is the only decision, against the root
// declarations themselves. Loosen it and transcripts / credentials / derived state gain a
// path into the repo.
func TestMemoryAllowedAllowlist(t *testing.T) {
	claudeRoot, codexRoot := memoryRootDecls()[0], memoryRootDecls()[1]
	if claudeRoot.Kind != "claude" || codexRoot.Kind != "codex" {
		t.Fatalf("root decl order changed: %q %q", claudeRoot.Kind, codexRoot.Kind)
	}
	cases := []struct {
		root memoryRoot
		rel  string
		want bool
	}{
		// claude: only projects/<slug>/memory/** is in scope.
		{claudeRoot, "-home-dev-repos-foo/memory/MEMORY.md", true},
		{claudeRoot, "-home-dev-repos-foo/memory/nested/topic.md", true},
		{claudeRoot, "-home-dev-repos-foo/abc-def.jsonl", false},           // transcript
		{claudeRoot, "-home-dev-repos-foo/subagents/agent-1.jsonl", false}, // subagent transcript
		{claudeRoot, "-home-dev-repos-foo/memoryfoo/x.md", false},          // not matched by prefix
		{claudeRoot, "memory/x.md", false},                                 // the slug level is required
		{claudeRoot, "../.credentials.json", false},
		{claudeRoot, "", false},
		// codex: everything under memories, minus its own .git, the intermediates and sqlite.
		{codexRoot, "MEMORY.md", true},
		{codexRoot, "memory_summary.md", true},
		{codexRoot, "skills/foo/SKILL.md", true},
		{codexRoot, ".git/config", false},
		{codexRoot, ".git/objects/ab/cdef", false},
		{codexRoot, "phase2_workspace_diff.md", false},
		{codexRoot, "memories_1.sqlite", false},
		{codexRoot, "memories_1.sqlite-wal", false},
	}
	for _, c := range cases {
		if got := memoryAllowed(c.root, c.rel); got != c.want {
			t.Errorf("memoryAllowed(%s, %q) = %v, want %v", c.root.Kind, c.rel, got, c.want)
		}
	}
}

func TestMemoryGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"**", "a", true},
		{"**", "a/b/c", true},
		{"*/memory/**", "s/memory/a.md", true},
		{"*/memory/**", "s/memory/x/y/a.md", true},
		{"*/memory/**", "s/other/a.md", false},
		{"*/memory/**", "s/a.md", false},
		{"*/memory/**", "a/s/memory/a.md", false}, // * does not cross a separator
		{".git/**", ".git/config", true},
		{".git/**", "x/.git/config", false},
		{"*.sqlite", "memories_1.sqlite", true},
		{"*.sqlite", "sub/memories_1.sqlite", false},
	}
	for _, c := range cases {
		if got := memoryGlobMatch(c.pattern, c.name); got != c.want {
			t.Errorf("memoryGlobMatch(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}

func TestMemoryScopeSlug(t *testing.T) {
	cases := []struct {
		path, slug string
		ok         bool
	}{
		{"claude/projects/-home-dev-repos-foo/memory/MEMORY.md", "-home-dev-repos-foo", true},
		{"claude/projects/-home-dev-repos-foo", "", false}, // a bare slug is not a scope
		{"codex/MEMORY.md", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		slug, ok := memoryScopeSlug(c.path)
		if slug != c.slug || ok != c.ok {
			t.Errorf("memoryScopeSlug(%q) = (%q,%v), want (%q,%v)", c.path, slug, ok, c.slug, c.ok)
		}
	}
}

// ★6: never show the raw slug. Use the real directory under ~/repos when there is one,
// otherwise whatever follows "-repos-".
func TestMemorySlugDisplay(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	memoryMkdirAll(t, filepath.Join(home, "repos", "agent-fleet"))
	realSlug := strings.ReplaceAll(filepath.Join(home, "repos", "agent-fleet"), "/", "-")
	if got := memorySlugDisplay(realSlug); got != "agent-fleet" {
		t.Errorf("real repo slug: got %q want %q", got, "agent-fleet")
	}
	if got := memorySlugDisplay("-home-someone-repos-other-repo"); got != "other-repo" {
		t.Errorf("fallback slug: got %q", got)
	}
	if got := memorySlugDisplay("-opt-elsewhere"); got != "opt-elsewhere" {
		t.Errorf("unmatched slug: got %q", got)
	}
}

// memoryRoots adds or drops the codex root according to whether ~/.codex/memories exists
// (the memories feature is off by default, so on an environment that never enabled it the
// root itself never appears — docs/log/39 resolution #4).
func TestMemoryRootsCodexPresenceGated(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude-config"))
	if kinds := memoryRootKinds(); len(kinds) != 1 || kinds[0] != "claude" {
		t.Fatalf("without ~/.codex/memories: kinds=%v", kinds)
	}
	memoryMkdirAll(t, filepath.Join(home, ".codex", "memories"))
	kinds := memoryRootKinds()
	if len(kinds) != 2 || kinds[0] != "claude" || kinds[1] != "codex" {
		t.Fatalf("with ~/.codex/memories: kinds=%v", kinds)
	}
	if _, ok := memoryRootByKind("codex"); !ok {
		t.Fatal("memoryRootByKind(codex) not found once the dir exists")
	}
}

func memoryRootKinds() []string {
	var out []string
	for _, r := range memoryRoots() {
		out = append(out, r.Kind)
	}
	return out
}

// docs/log/39 P4: dropping the codex root silently on an environment that has not enabled
// memories leaves the Console unable to say either why codex memory is missing or how to
// turn it on. This pins that inactive carries that reason.
func TestMemoryInactiveRootsExplainCodex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude-config"))

	find := func(t *testing.T, kind string) memoryInactiveRoot {
		t.Helper()
		for _, v := range memoryInactiveRoots() {
			if v.Kind == kind {
				return v
			}
		}
		t.Fatalf("kind %q missing from inactive roots: %+v", kind, memoryInactiveRoots())
		return memoryInactiveRoot{}
	}

	// 1. Feature off, no workspace = the state where enabling it can be suggested.
	v := find(t, "codex")
	if v.Reason != "codex_memories_disabled" || !v.Toggleable || v.Enabled {
		t.Fatalf("disabled codex root described wrongly: %+v", v)
	}

	// 2. Just enabled (config is on, but codex has not created ~/.codex/memories yet).
	// Confused with "the setting had no effect", the user toggles it pointlessly over and over.
	memoryWrite(t, filepath.Join(home, ".codex", "config.toml"), "[features]\nmemories = true\n")
	if v := find(t, "codex"); v.Reason != "codex_memories_pending" || !v.Enabled {
		t.Fatalf("enabled-but-unmaterialized codex root described wrongly: %+v", v)
	}

	// 3. Once codex creates the workspace, the root leaves inactive and becomes a normal root.
	memoryMkdirAll(t, filepath.Join(home, ".codex", "memories"))
	for _, v := range memoryInactiveRoots() {
		if v.Kind == "codex" {
			t.Fatalf("materialized codex root is still reported inactive: %+v", v)
		}
	}
	if _, ok := memoryRootByKind("codex"); !ok {
		t.Fatal("codex root did not become active once ~/.codex/memories existed")
	}
}
