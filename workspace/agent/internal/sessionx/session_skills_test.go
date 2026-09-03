package sessionx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
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
	// ソート済み（name 昇順）・invoke はスラッシュ形であること
	for i := 1; i < len(got); i++ {
		if got[i-1].Name > got[i].Name {
			t.Errorf("not sorted: %s > %s", got[i-1].Name, got[i].Name)
		}
	}
	for _, sk := range got {
		if sk.Invoke != "/"+sk.Name+" " {
			t.Errorf("invoke = %q for %s", sk.Invoke, sk.Name)
		}
	}
}

// codex: SKILL.md 規約は claude 互換だが起動は "$name" メンション。同梱 .system は
// source "cli"、project .codex/skills > user $CODEX_HOME/skills の先勝ち。
func TestCodexSkills(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	writeFile(t, filepath.Join(dir, ".codex", "skills", "deploy", "SKILL.md"),
		"---\nname: deploy\ndescription: project 側\n---\nbody")
	writeFile(t, filepath.Join(home, "skills", "deploy", "SKILL.md"),
		"---\nname: deploy\ndescription: user 側（負けるべき）\n---\nbody")
	writeFile(t, filepath.Join(home, "skills", "research", "SKILL.md"),
		"---\nname: research\ndescription: user スキル\n---\nbody")
	writeFile(t, filepath.Join(home, "skills", ".system", "imagegen", "SKILL.md"),
		"---\nname: imagegen\ndescription: 同梱\n---\nbody")

	got := codexSkills(dir)
	byName := map[string]sessionSkill{}
	for _, sk := range got {
		byName[sk.Name] = sk
	}
	if len(got) != 3 {
		t.Fatalf("want 3, got %d: %#v", len(got), got)
	}
	if sk := byName["deploy"]; sk.Source != "project" || sk.Invoke != "$deploy " {
		t.Errorf("deploy = %#v", sk)
	}
	if sk := byName["research"]; sk.Source != "user" || sk.Invoke != "$research " {
		t.Errorf("research = %#v", sk)
	}
	if sk := byName["imagegen"]; sk.Source != "cli" {
		t.Errorf("imagegen = %#v", sk)
	}
}

// opencode: command md（単複両ディレクトリ名）を project > user で列挙。
func TestOpencodeSkills(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFile(t, filepath.Join(dir, ".opencode", "command", "deploy.md"),
		"---\ndescription: デプロイ\n---\nDeploy it.")
	writeFile(t, filepath.Join(dir, ".opencode", "commands", "lint.md"), "Lint it.")
	writeFile(t, filepath.Join(home, ".config", "opencode", "command", "deploy.md"),
		"---\ndescription: user 側（負けるべき）\n---\nbody")
	writeFile(t, filepath.Join(home, ".config", "opencode", "command", "share.md"),
		"---\ndescription: 共有\n---\nbody")

	got := opencodeSkills(dir)
	byName := map[string]sessionSkill{}
	for _, sk := range got {
		byName[sk.Name] = sk
	}
	if len(got) != 3 {
		t.Fatalf("want 3, got %d: %#v", len(got), got)
	}
	if sk := byName["deploy"]; sk.Source != "project" || sk.Description != "デプロイ" || sk.Invoke != "/deploy " {
		t.Errorf("deploy = %#v", sk)
	}
	if sk := byName["share"]; sk.Source != "user" {
		t.Errorf("share = %#v", sk)
	}
}

// cursor: ACP 広告リストが最優先（builtin+global+project 全部入り）、無ければ
// project の .cursor/commands + .cursor/skills へフォールバック。
func TestCursorSkills(t *testing.T) {
	dir := t.TempDir()
	meta := session.Meta{Name: "sk_cursor_scan", Dir: dir, Kind: session.KindCursor}
	writeFile(t, filepath.Join(dir, ".cursor", "commands", "probe.md"), "Say probe-ok.")
	writeFile(t, filepath.Join(dir, ".cursor", "skills", "helper", "SKILL.md"),
		"---\nname: helper\ndescription: 補助\n---\nbody")

	// 広告リスト未着 → FS フォールバック
	got := cursorSkills(meta)
	if len(got) != 2 || got[0].Name != "helper" || got[1].Name != "probe" {
		t.Fatalf("fallback = %#v", got)
	}

	// 広告リスト到着後はそちらが正（先頭スラッシュは publish 側で剥がされている前提の素の名前）
	agents.PublishCommands(meta.Name, []agents.AdvertisedCommand{
		{Name: "simplify", Description: "Find cleanups (global)"},
		{Name: "probe", Description: "AF probe (project)"},
	})
	got = cursorSkills(meta)
	if len(got) != 2 {
		t.Fatalf("advertised = %#v", got)
	}
	for _, sk := range got {
		if sk.Source != "cli" || sk.Invoke != "/"+sk.Name+" " {
			t.Errorf("advertised entry = %#v", sk)
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
		HandleSessionSkills(rec, req)
		return rec
	}

	// claude セッション → worktree のスキルが出る
	session.WriteMeta(session.Meta{Name: "sk_claude", Dir: dir, Kind: session.KindClaude})
	rec := get("sk_claude")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct{ Skills []sessionSkill }
	resp.Skills = nil // omitempty フィールドが前回要素の値を引き継がないようリセット
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Skills) != 1 || resp.Skills[0].Name != "scout" || resp.Skills[0].Source != "project" {
		t.Fatalf("skills = %#v", resp.Skills)
	}

	// codex セッション → ネイティブ（.codex/skills を "$name" 起動）＋ foreign
	// （.claude/skills の scout — 注入候補として path/origin 付き・invoke 空）
	t.Setenv("CODEX_HOME", t.TempDir())
	writeFile(t, filepath.Join(dir, ".codex", "skills", "probe", "SKILL.md"),
		"---\nname: probe\ndescription: 検証\n---\nbody")
	session.WriteMeta(session.Meta{Name: "sk_codex", Dir: dir, Kind: session.KindCodex})
	rec = get("sk_codex")
	if rec.Code != http.StatusOK {
		t.Fatalf("codex status = %d", rec.Code)
	}
	resp.Skills = nil // omitempty フィールドが前回要素の値を引き継がないようリセット
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Skills) != 2 {
		t.Fatalf("codex skills = %#v", resp.Skills)
	}
	for _, sk := range resp.Skills {
		switch sk.Name {
		case "probe":
			if sk.Invoke != "$probe " || sk.Path != "" {
				t.Errorf("native probe = %#v", sk)
			}
		case "scout":
			if sk.Invoke != "" || sk.Path != ".claude/skills/scout/SKILL.md" || sk.Origin != ".claude" {
				t.Errorf("foreign scout = %#v", sk)
			}
		default:
			t.Errorf("unexpected %#v", sk)
		}
	}

	// kiro セッション → ネイティブ無し・foreign のみ（注入方式は kind 不問）
	session.WriteMeta(session.Meta{Name: "sk_kiro", Dir: dir, Kind: session.KindKiro})
	rec = get("sk_kiro")
	resp.Skills = nil // omitempty フィールドが前回要素の値を引き継がないようリセット
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Skills) != 2 { // .claude/scout + .codex/probe（どちらも foreign）
		t.Fatalf("kiro skills = %#v", resp.Skills)
	}
	for _, sk := range resp.Skills {
		if sk.Invoke != "" || sk.Path == "" {
			t.Errorf("kiro entry should be foreign: %#v", sk)
		}
	}

	// 未対応 kind（shell）→ エラーでなく空（前方互換な契約）
	session.WriteMeta(session.Meta{Name: "sk_shell", Dir: dir, Kind: session.KindShell})
	rec = get("sk_shell")
	if rec.Code != http.StatusOK {
		t.Fatalf("shell status = %d", rec.Code)
	}
	resp.Skills = nil // omitempty フィールドが前回要素の値を引き継がないようリセット
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Skills) != 0 {
		t.Fatalf("shell skills should be empty: %#v", resp.Skills)
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
