package copilot

import (
	"os"
	"path/filepath"
	"testing"
)

// realWorkspaceYAML は実機に残る copilot v1.0.73 のセッションからそのまま写した形。
// 値にコロンを含む name 行と、ミリ秒 + Z の created_at が肝（素朴な分割で壊れやすい）。
const realWorkspaceYAML = `id: 254e5d40-17fb-4de1-9a29-3db2da0c9c36
cwd: /tmp/repo
client_name: github/cli
name: You must call the probe structured_probe tool exactly once. Then output a compact JSON object.
user_named: false
summary_count: 0
created_at: 2026-08-01T15:15:38.514Z
updated_at: 2026-08-01T15:15:40.479Z
`

func writeSession(t *testing.T, home, sid, yaml string) {
	t.Helper()
	dir := filepath.Join(home, "session-state", sid)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if yaml == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "workspace.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
}

// workspace.yaml の実形から id / cwd / created_at を取り出せること。ここが読めないと
// ドリフト回収は候補を一つも作れず、静かに何もしない機能になる。
func TestCliSessionsReadsRealWorkspaceYAML(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COPILOT_HOME", home)
	writeSession(t, home, "254e5d40-17fb-4de1-9a29-3db2da0c9c36", realWorkspaceYAML)

	got := cliSessions("/tmp/repo")
	if len(got) != 1 {
		t.Fatalf("cliSessions = %+v, want 1 session", got)
	}
	if got[0].ID != "254e5d40-17fb-4de1-9a29-3db2da0c9c36" {
		t.Fatalf("id = %q", got[0].ID)
	}
	if got[0].Created.IsZero() {
		t.Fatal("created_at を読めていない — 前任セッションとの区別が付かなくなる")
	}
	if y, m, d := got[0].Created.Date(); y != 2026 || m != 8 || d != 1 {
		t.Fatalf("created = %v, want 2026-08-01", got[0].Created)
	}
}

// 別 cwd のセッションは候補にしない。copilot の session-state は cwd で分かれておらず
// 全スロットが同じディレクトリに並ぶので、この絞り込みが唯一の帰属手段。
func TestCliSessionsFiltersByCwd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COPILOT_HOME", home)
	writeSession(t, home, "aaaaaaaa-0000-4000-8000-000000000001",
		"id: aaaaaaaa-0000-4000-8000-000000000001\ncwd: /tmp/repo\ncreated_at: 2026-08-01T15:15:38.514Z\n")
	writeSession(t, home, "bbbbbbbb-0000-4000-8000-000000000002",
		"id: bbbbbbbb-0000-4000-8000-000000000002\ncwd: /tmp/other\ncreated_at: 2026-08-01T15:15:38.514Z\n")

	got := cliSessions("/tmp/repo")
	if len(got) != 1 || got[0].ID != "aaaaaaaa-0000-4000-8000-000000000001" {
		t.Fatalf("cliSessions = %+v, want only the /tmp/repo one", got)
	}
}

// workspace.yaml が無い／形が変わって cwd を取れないセッションは候補から外す。
// 帰属を確かめられないものを拾うのは、他人の会話をミラーに映す事故に直結する。
func TestCliSessionsSkipsUnattributable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COPILOT_HOME", home)
	writeSession(t, home, "cccccccc-0000-4000-8000-000000000003", "") // workspace.yaml 無し

	if got := cliSessions("/tmp/repo"); len(got) != 0 {
		t.Fatalf("cliSessions = %+v, want none", got)
	}
}
