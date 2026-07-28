package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func TestScanSlashSkills(t *testing.T) {
	project := t.TempDir()
	user := t.TempDir()
	// project skill: frontmatter 完備（argument-hint / user-invocable も読む）
	writeFile(t, filepath.Join(project, "skills", "proofread", "SKILL.md"),
		"---\nname: proofread\ndescription: 原稿の形式整備\nargument-hint: \"<章番号>\"\n---\nbody")
	// user-invocable: false は除外
	writeFile(t, filepath.Join(project, "skills", "internal-only", "SKILL.md"),
		"---\nname: internal-only\nuser-invocable: false\n---\nbody")
	// name 無し → ディレクトリ名でフォールバック
	writeFile(t, filepath.Join(project, "skills", "ledger", "SKILL.md"), "no frontmatter")
	// project command: サブディレクトリはスラッシュ名に入らない（ファイル名が起動名）
	writeFile(t, filepath.Join(project, "commands", "git", "commit.md"),
		"---\ndescription: ステージ済みをコミット\nargument-hint: \"[message]\"\n---\nbody")
	// user skill: project と同名 → project 勝ち / 別名 → 生き残る
	writeFile(t, filepath.Join(user, "skills", "proofread", "SKILL.md"),
		"---\nname: proofread\ndescription: user 側の同名（負けるべき）\n---\nbody")
	writeFile(t, filepath.Join(user, "skills", "handoff", "SKILL.md"),
		"---\nname: handoff\ndescription: 引き継ぎ\n---\nbody")
	// 同一ルート内では skill が command に勝つ
	writeFile(t, filepath.Join(project, "commands", "proofread.md"), "command twin")

	got := scanSlashSkills(project, user)
	want := map[string][3]string{ // name → {source, type, description}
		"proofread": {"project", "skill", "原稿の形式整備"},
		"ledger":    {"project", "skill", ""},
		"commit":    {"project", "command", "ステージ済みをコミット"},
		"handoff":   {"user", "skill", "引き継ぎ"},
	}
	if len(got) != len(want) {
		t.Fatalf("want %d entries, got %d: %#v", len(want), len(got), got)
	}
	for _, sk := range got {
		w, ok := want[sk.Name]
		if !ok {
			t.Errorf("unexpected entry %#v", sk)
			continue
		}
		if sk.Source != w[0] || sk.Type != w[1] || sk.Description != w[2] {
			t.Errorf("%s = %#v, want source=%s type=%s desc=%q", sk.Name, sk, w[0], w[1], w[2])
		}
	}
	for _, sk := range got {
		if sk.Name == "proofread" && sk.ArgumentHint != "<章番号>" {
			t.Errorf("argument-hint not parsed: %#v", sk)
		}
	}
	// ソート済み（name 昇順）であること
	for i := 1; i < len(got); i++ {
		if got[i-1].Name > got[i].Name {
			t.Errorf("not sorted: %s > %s", got[i-1].Name, got[i].Name)
		}
	}
}

func TestHandleSessionSkills(t *testing.T) {
	t.Setenv("AF_SESSIONS_DIR", filepath.Join(t.TempDir(), "sessions"))
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".claude", "skills", "scout", "SKILL.md"),
		"---\nname: scout\ndescription: 調査\n---\nbody")
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir()) // user ルートは空

	get := func(name string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/sessions/"+name+"/skills", nil)
		req.SetPathValue("name", name)
		rec := httptest.NewRecorder()
		handleSessionSkills(rec, req)
		return rec
	}

	// claude セッション → worktree のスキルが出る
	session.WriteMeta(session.Meta{Name: "sk_claude", Dir: dir, Kind: session.KindClaude})
	rec := get("sk_claude")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct{ Skills []sessionSkill }
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Skills) != 1 || resp.Skills[0].Name != "scout" || resp.Skills[0].Source != "project" {
		t.Fatalf("skills = %#v", resp.Skills)
	}

	// claude 以外の kind → エラーでなく空（前方互換な契約）
	session.WriteMeta(session.Meta{Name: "sk_codex", Dir: dir, Kind: session.KindCodex})
	rec = get("sk_codex")
	if rec.Code != http.StatusOK {
		t.Fatalf("codex status = %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Skills) != 0 {
		t.Fatalf("codex skills should be empty: %#v", resp.Skills)
	}

	// 未知セッション → 404
	if rec := get("sk_nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing session status = %d", rec.Code)
	}
}

// CP 側の登録漏れは control-plane 側のテストで拾う（session_skills_routes 同型）。
// こちらは Agent 側の実ルート表への登録を固定する。
func TestSessionSkillsRouteRegistered(t *testing.T) {
	mux := buildMux()
	req := httptest.NewRequest(http.MethodGet, "/sessions/x/skills", nil)
	if _, pattern := mux.Handler(req); pattern != "GET /sessions/{name}/skills" {
		t.Errorf("resolved to %q", pattern)
	}
}
