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
	// project skill with complete frontmatter (argument-hint / user-invocable are read too)
	writeFile(t, filepath.Join(project, "skills", "proofread", "SKILL.md"),
		"---\nname: proofread\ndescription: 原稿の形式整備\nargument-hint: \"<章番号>\"\n---\nbody")
	// user-invocable: false is excluded
	writeFile(t, filepath.Join(project, "skills", "internal-only", "SKILL.md"),
		"---\nname: internal-only\nuser-invocable: false\n---\nbody")
	// no name: fall back to the directory name
	writeFile(t, filepath.Join(project, "skills", "ledger", "SKILL.md"), "no frontmatter")
	// project command: a subdirectory is not part of the slash name (the file name is the invocation name)
	writeFile(t, filepath.Join(project, "commands", "git", "commit.md"),
		"---\ndescription: ステージ済みをコミット\nargument-hint: \"[message]\"\n---\nbody")
	// user skill: same name as a project one loses to it; a different name survives
	writeFile(t, filepath.Join(user, "skills", "proofread", "SKILL.md"),
		"---\nname: proofread\ndescription: user 側の同名（負けるべき）\n---\nbody")
	writeFile(t, filepath.Join(user, "skills", "handoff", "SKILL.md"),
		"---\nname: handoff\ndescription: 引き継ぎ\n---\nbody")
	// within one root, a skill beats a command
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
	// sorted by name ascending, and invoke must be the slash form
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

// codex: the SKILL.md convention is claude-compatible but invocation is a "$name" mention.
// The bundled .system entries get source "cli"; project .codex/skills wins over user
// $CODEX_HOME/skills, first one wins.
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

// opencode: enumerates command md files (both the singular and plural directory names),
// project before user.
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

// cursor: the ACP advertised list wins (it holds builtin + global + project together);
// without it, fall back to the project's .cursor/commands + .cursor/skills.
func TestCursorSkills(t *testing.T) {
	dir := t.TempDir()
	meta := session.Meta{Name: "sk_cursor_scan", Dir: dir, Kind: session.KindCursor}
	writeFile(t, filepath.Join(dir, ".cursor", "commands", "probe.md"), "Say probe-ok.")
	writeFile(t, filepath.Join(dir, ".cursor", "skills", "helper", "SKILL.md"),
		"---\nname: helper\ndescription: 補助\n---\nbody")

	// advertised list has not arrived: fall back to the filesystem
	got := cursorSkills(meta)
	if len(got) != 2 || got[0].Name != "helper" || got[1].Name != "probe" {
		t.Fatalf("fallback = %#v", got)
	}

	// once the advertised list arrives it is authoritative (bare names, on the assumption
	// the publisher already stripped the leading slash)
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
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir()) // the user root is empty

	get := func(name string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/sessions/"+name+"/skills", nil)
		req.SetPathValue("name", name)
		rec := httptest.NewRecorder()
		HandleSessionSkills(rec, req)
		return rec
	}

	// a claude session lists the worktree's skills
	session.WriteMeta(session.Meta{Name: "sk_claude", Dir: dir, Kind: session.KindClaude})
	rec := get("sk_claude")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct{ Skills []sessionSkill }
	resp.Skills = nil // reset so an omitempty field cannot inherit the previous element's value
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Skills) != 1 || resp.Skills[0].Name != "scout" || resp.Skills[0].Source != "project" {
		t.Fatalf("skills = %#v", resp.Skills)
	}

	// a codex session lists the native ones (.codex/skills, invoked as "$name") plus the
	// foreign ones (scout from .claude/skills — an injection candidate, so it carries
	// path/origin and an empty invoke)
	t.Setenv("CODEX_HOME", t.TempDir())
	writeFile(t, filepath.Join(dir, ".codex", "skills", "probe", "SKILL.md"),
		"---\nname: probe\ndescription: 検証\n---\nbody")
	session.WriteMeta(session.Meta{Name: "sk_codex", Dir: dir, Kind: session.KindCodex})
	rec = get("sk_codex")
	if rec.Code != http.StatusOK {
		t.Fatalf("codex status = %d", rec.Code)
	}
	resp.Skills = nil // reset so an omitempty field cannot inherit the previous element's value
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

	// a kiro session has no native skills, only foreign ones (injection works for any kind)
	session.WriteMeta(session.Meta{Name: "sk_kiro", Dir: dir, Kind: session.KindKiro})
	rec = get("sk_kiro")
	resp.Skills = nil // reset so an omitempty field cannot inherit the previous element's value
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Skills) != 2 { // .claude/scout + .codex/probe, both foreign
		t.Fatalf("kiro skills = %#v", resp.Skills)
	}
	for _, sk := range resp.Skills {
		if sk.Invoke != "" || sk.Path == "" {
			t.Errorf("kiro entry should be foreign: %#v", sk)
		}
	}

	// an unsupported kind (shell) yields empty rather than an error: a forward-compatible contract
	session.WriteMeta(session.Meta{Name: "sk_shell", Dir: dir, Kind: session.KindShell})
	rec = get("sk_shell")
	if rec.Code != http.StatusOK {
		t.Fatalf("shell status = %d", rec.Code)
	}
	resp.Skills = nil // reset so an omitempty field cannot inherit the previous element's value
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Skills) != 0 {
		t.Fatalf("shell skills should be empty: %#v", resp.Skills)
	}

	// unknown session: 404
	if rec := get("sk_nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing session status = %d", rec.Code)
	}
}

// A missing registration on the CP side is caught by the control-plane test of the same
// shape (session_skills_routes). This one pins the registration in the Agent's own route table.
func TestSessionSkillsRouteRegistered(t *testing.T) {
	mux := buildMux()
	req := httptest.NewRequest(http.MethodGet, "/sessions/x/skills", nil)
	if _, pattern := mux.Handler(req); pattern != "GET /sessions/{name}/skills" {
		t.Errorf("resolved to %q", pattern)
	}
}
