package skillbridge

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v %s", err, out)
	}
}

func TestSyncBridgesBothWays(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	// claude 側の実スキル（scripts 込み）と codex 側の実スキル
	write(t, filepath.Join(dir, ".claude", "skills", "proofread", "SKILL.md"), "---\nname: proofread\n---\nclaude body")
	write(t, filepath.Join(dir, ".claude", "skills", "proofread", "scripts", "check.py"), "print('ok')")
	write(t, filepath.Join(dir, ".codex", "skills", "deploy", "SKILL.md"), "---\nname: deploy\n---\ncodex body")
	// SKILL.md を持たない dir はスキルではない
	if err := os.MkdirAll(filepath.Join(dir, ".claude", "skills", "notaskill"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 同名衝突: 両側に実体 → どちらも触らない
	write(t, filepath.Join(dir, ".claude", "skills", "both", "SKILL.md"), "claude version")
	write(t, filepath.Join(dir, ".codex", "skills", "both", "SKILL.md"), "codex version")

	copied, pruned := Sync(dir)
	if copied != 2 || pruned != 0 {
		t.Fatalf("first run: copied=%d pruned=%d", copied, pruned)
	}
	// claude→codex: コピー＋マーカー＋中身（scripts まで）
	if b, err := os.ReadFile(filepath.Join(dir, ".codex", "skills", "proofread", "SKILL.md")); err != nil || !strings.Contains(string(b), "claude body") {
		t.Fatalf("proofread not bridged: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".codex", "skills", "proofread", "scripts", "check.py")); err != nil {
		t.Fatalf("scripts not copied: %v", err)
	}
	if m := readMarker(filepath.Join(dir, ".codex", "skills", "proofread")); m != ".claude/skills/proofread" {
		t.Fatalf("marker = %q", m)
	}
	// codex→claude
	if b, err := os.ReadFile(filepath.Join(dir, ".claude", "skills", "deploy", "SKILL.md")); err != nil || !strings.Contains(string(b), "codex body") {
		t.Fatalf("deploy not bridged: %v", err)
	}
	// 衝突・非スキルは無傷
	if b, _ := os.ReadFile(filepath.Join(dir, ".codex", "skills", "both", "SKILL.md")); string(b) != "codex version" {
		t.Fatalf("native 'both' clobbered: %q", b)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".codex", "skills", "notaskill")); err == nil {
		t.Fatal("notaskill should not be bridged")
	}

	// 2 回目は定常（新規 0・ブリッジのブリッジも作らない）
	if copied, pruned = Sync(dir); copied != 0 || pruned != 0 {
		t.Fatalf("steady state: copied=%d pruned=%d", copied, pruned)
	}

	// 内容追随: 元を書き換え → 次の Sync でコピーが更新される
	write(t, filepath.Join(dir, ".claude", "skills", "proofread", "SKILL.md"), "---\nname: proofread\n---\nrevised")
	Sync(dir)
	if b, _ := os.ReadFile(filepath.Join(dir, ".codex", "skills", "proofread", "SKILL.md")); !strings.Contains(string(b), "revised") {
		t.Fatalf("copy not refreshed: %q", b)
	}

	// 剪定: 元を消す → コピーも消える（実体 deploy は残る）
	if err := os.RemoveAll(filepath.Join(dir, ".claude", "skills", "proofread")); err != nil {
		t.Fatal(err)
	}
	if copied, pruned = Sync(dir); pruned != 1 {
		t.Fatalf("prune: copied=%d pruned=%d", copied, pruned)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".codex", "skills", "proofread")); err == nil {
		t.Fatal("orphan copy should be pruned")
	}
}

func TestSyncGitExclude(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	write(t, filepath.Join(dir, ".claude", "skills", "proofread", "SKILL.md"), "body")
	// ユーザーの未コミット実スキル（codex 側）— これは exclude してはいけない
	write(t, filepath.Join(dir, ".codex", "skills", "native", "SKILL.md"), "native")

	Sync(dir)
	b, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "/.codex/skills/proofread/") {
		t.Fatalf("bridged copy not excluded: %q", b)
	}
	// native の実体（.codex 側）は exclude されない。逆方向ブリッジで生まれた
	// コピー（.claude 側）だけが載る。
	if strings.Contains(string(b), "/.codex/skills/native/") {
		t.Fatalf("native skill must NOT be excluded: %q", b)
	}
	if !strings.Contains(string(b), "/.claude/skills/native/") {
		t.Fatalf("reverse-bridged copy should be excluded: %q", b)
	}
	// status: ブリッジ産は見えず、実スキルは見える
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), ".codex/skills/proofread") {
		t.Fatalf("bridged copy leaked into status: %q", out)
	}
	if !strings.Contains(string(out), ".codex") { // native が untracked として見える
		t.Fatalf("native skill vanished from status: %q", out)
	}

	// 全ブリッジ剪定後はブロックごと消える
	if err := os.RemoveAll(filepath.Join(dir, ".claude", "skills", "proofread")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, ".codex", "skills", "native")); err != nil {
		t.Fatal(err)
	}
	Sync(dir)
	b, _ = os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	if strings.Contains(string(b), "skill-bridge") || strings.Contains(string(b), "proofread") {
		t.Fatalf("exclude block should be gone: %q", b)
	}
}

func TestSyncNonGitAndEmpty(t *testing.T) {
	// git repo でなくても、スキルが無くても、落ちず・余計な dir も作らない
	dir := t.TempDir()
	if copied, pruned := Sync(dir); copied != 0 || pruned != 0 {
		t.Fatalf("empty: %d %d", copied, pruned)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".codex")); err == nil {
		t.Fatal("no dirs should be created in an empty repo")
	}
	write(t, filepath.Join(dir, ".claude", "skills", "a", "SKILL.md"), "body")
	if copied, _ := Sync(dir); copied != 1 {
		t.Fatal("non-git dir should still bridge")
	}
}
