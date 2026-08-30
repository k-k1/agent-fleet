package main

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

// docs/log/39 ★1: allowlist が唯一の判定であることを、ルート宣言そのものに対して固定する。
// ここが緩むと transcript / 資格情報 / 派生状態が repo へ入る経路が生まれる。
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
		// claude: projects/<slug>/memory/** だけが対象。
		{claudeRoot, "-home-dev-repos-foo/memory/MEMORY.md", true},
		{claudeRoot, "-home-dev-repos-foo/memory/nested/topic.md", true},
		{claudeRoot, "-home-dev-repos-foo/abc-def.jsonl", false},           // transcript
		{claudeRoot, "-home-dev-repos-foo/subagents/agent-1.jsonl", false}, // subagent transcript
		{claudeRoot, "-home-dev-repos-foo/memoryfoo/x.md", false},          // 前方一致では通さない
		{claudeRoot, "memory/x.md", false},                                 // slug 階層が要る
		{claudeRoot, "../.credentials.json", false},
		{claudeRoot, "", false},
		// codex: memories 配下は全部だが、自前 .git と中間生成物と sqlite は除く。
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
		{"*/memory/**", "a/s/memory/a.md", false}, // * はセパレータを跨がない
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
		{"claude/projects/-home-dev-repos-foo", "", false}, // slug 単体はスコープにならない
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

// ★6: slug をそのまま見せない。~/repos に実体があればそれを、無ければ "-repos-" 以降を採る。
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

// memoryRoots は codex のルートを ~/.codex/memories の存在で出し入れする（memories 機能は
// 既定 OFF なので、有効化していない環境ではルート自体が現れない — docs/log/39 決着 #4）。
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

// docs/log/39 P4: memories を有効化していない環境で codex ルートを黙って落とすと、Console は
// 「なぜ codex のメモリが出てこないか」も「どう有効化するか」も示せない。inactive が
// その理由を持つことを固定する。
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

	// ① 機能 OFF・ワークスペース無し = 有効化を勧められる状態。
	v := find(t, "codex")
	if v.Reason != "codex_memories_disabled" || !v.Toggleable || v.Enabled {
		t.Fatalf("disabled codex root described wrongly: %+v", v)
	}

	// ② 有効化直後（config は ON だが codex がまだ ~/.codex/memories を作っていない）。
	// 「設定が効いていない」と混同されると、利用者は無意味に何度も切り替える。
	memoryWrite(t, filepath.Join(home, ".codex", "config.toml"), "[features]\nmemories = true\n")
	if v := find(t, "codex"); v.Reason != "codex_memories_pending" || !v.Enabled {
		t.Fatalf("enabled-but-unmaterialized codex root described wrongly: %+v", v)
	}

	// ③ codex がワークスペースを作ったら inactive から消え、通常のルートになる。
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
