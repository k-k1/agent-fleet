package main

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// セッション単位のスキル一覧（docs/50 / ADR0034、v2 でクロスエージェント化）:
// ミラービューのスキルピッカーが「いま話しているセッション」で呼べる起動可能な
// スキル/コマンドを列挙する。kind ごとにソースと起動形が違う（全て 2026-07-28 実測、
// docs/50 §7）:
//   - claude:   .claude/skills + .claude/commands（project = meta.Dir / user = claude.ConfigDir()）→ "/name"
//   - codex:    .codex/skills（project）+ $CODEX_HOME/skills（user・同梱 .system は cli 扱い）→ "$name" メンション
//   - opencode: .opencode/command(s)（project）+ ~/.config/opencode/command(s)（user）→ "/name"
//   - cursor:   ACP 広告リスト（builtin skill ＋ global ＋ project 全部入り）が正。
//               runtime 不在時は project の .cursor/commands + .cursor/skills へフォールバック → "/name"
// その他の kind はエラーでなく空（kiro は広告ペイロードの user 定義形が未検証 — docs/50 §7）。
// 読み取り専用・都度走査（ピッカーを開いた時に 1 回呼ぶだけなのでキャッシュ不要）。

type sessionSkill struct {
	Name         string `json:"name"` // 起動名（"/" や "$" のプレフィックスは含まない）
	Description  string `json:"description,omitempty"`
	ArgumentHint string `json:"argumentHint,omitempty"` // frontmatter argument-hint（あれば）
	Source       string `json:"source"`                 // project | user | cli（同梱/CLI 広告）
	Type         string `json:"type"`                   // skill | command
	Invoke       string `json:"invoke"`                 // コンポーザーへ差し込む起動文字列（末尾空白込み。例 "/name " / "$name "）
}

const maxSessionSkills = 200 // 全体の上限（repo_prompts の maxPromptItems と同じ発想の安全弁）

func handleSessionSkills(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	meta, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	// Console 側の caps ゲートが第一防壁で、こちらは前方互換な契約（未対応 kind は空）。
	skills := []sessionSkill{}
	switch meta.Kind {
	case session.KindClaude:
		skills = scanSlashSkills(filepath.Join(meta.Dir, ".claude"), claude.ConfigDir())
	case session.KindCodex:
		skills = codexSkills(meta.Dir)
	case session.KindOpencode:
		skills = opencodeSkills(meta.Dir)
	case session.KindCursor:
		skills = cursorSkills(meta)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"skills": skills})
}

// skillRoot is one directory to scan: form "skills" = <dir>/*/SKILL.md tree,
// form "commands" = <dir>/**/*.md flat command files.
type skillRoot struct {
	dir    string
	source string // project | user | cli
	form   string // skills | commands
}

// scanSkillRoots reads roots in order and dedupes by name — 先勝ち。呼び出し側は
// 「project より user が弱い」「skill が command に勝つ」順で roots を並べる。
// invokePrefix はその kind の起動形（"/" か "$"）。
func scanSkillRoots(roots []skillRoot, invokePrefix string) []sessionSkill {
	out := []sessionSkill{}
	seen := map[string]bool{}
	for _, r := range roots {
		if r.dir == "" {
			continue
		}
		var items []sessionSkill
		if r.form == "skills" {
			items = readSkillEntries(r.dir, r.source)
		} else {
			items = readCommandEntries(r.dir, r.source)
		}
		for _, it := range items {
			if it.Name == "" || seen[it.Name] || len(out) >= maxSessionSkills {
				continue
			}
			seen[it.Name] = true
			it.Invoke = invokePrefix + it.Name + " "
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// scanSlashSkills covers claude: project（<worktree>/.claude）→ user（claude.ConfigDir()）、
// 同一ルート内では skill 優先（claude のスラッシュ名前空間は 1 つ）。
func scanSlashSkills(projectBase, userBase string) []sessionSkill {
	return scanSkillRoots([]skillRoot{
		{filepath.Join(projectBase, "skills"), "project", "skills"},
		{filepath.Join(projectBase, "commands"), "project", "commands"},
		{filepath.Join(userBase, "skills"), "user", "skills"},
		{filepath.Join(userBase, "commands"), "user", "commands"},
	}, "/")
}

// codexSkills: SKILL.md 規約は claude 互換（0.145 実測 — frontmatter name/description、
// $CODEX_HOME/skills が auto-discover、repo 側 skills root は .codex/skills）。同梱の
// .system は user ルートの直下走査には掛からない（SKILL.md がディレクトリ直下に無い）
// ので別ルートとして拾い、source "cli" で区別する。起動は "$name" メンション（スラッシュ
// ではない — バイナリのシステムプロンプト実測「names an available skill (with $SkillName …)」）。
func codexSkills(dir string) []sessionSkill {
	home := paths.CodexHome()
	return scanSkillRoots([]skillRoot{
		{filepath.Join(dir, ".codex", "skills"), "project", "skills"},
		{filepath.Join(home, "skills"), "user", "skills"},
		{filepath.Join(home, "skills", ".system"), "cli", "skills"},
	}, "$")
}

// opencodeSkills: コマンド md（本文がプロンプト・frontmatter description）を列挙する。
// ディレクトリ名は単複両方が実在する（1.18.8 バイナリ実測: .opencode/command/deploy.md と
// .opencode/commands/ の両文字列）。.opencode/skills は model 起動用でスラッシュ起動が
// 未検証のため対象外（docs/50 §7）。
func opencodeSkills(dir string) []sessionSkill {
	cfg := paths.OpencodeConfigDir()
	return scanSkillRoots([]skillRoot{
		{filepath.Join(dir, ".opencode", "command"), "project", "commands"},
		{filepath.Join(dir, ".opencode", "commands"), "project", "commands"},
		{filepath.Join(cfg, "command"), "user", "commands"},
		{filepath.Join(cfg, "commands"), "user", "commands"},
	}, "/")
}

// cursorSkills: CLI 広告リスト（ACP available_commands_update — driver が
// agents.PublishCommands で共有）が唯一の完全ソース（builtin skill ＋ global ＋
// project の commands/skills 全部入り、2026-07-28 実測）。未着（runtime 未起動・
// agent 再起動直後）は project の FS 規約へフォールバックする。
func cursorSkills(meta session.Meta) []sessionSkill {
	if adv := agents.AdvertisedCommands(meta.Name); len(adv) > 0 {
		out := make([]sessionSkill, 0, len(adv))
		for _, c := range adv {
			if c.Name == "" || len(out) >= maxSessionSkills {
				continue
			}
			out = append(out, sessionSkill{
				Name:        c.Name,
				Description: c.Description,
				Source:      "cli",
				Type:        "command",
				Invoke:      "/" + c.Name + " ",
			})
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return out
	}
	return scanSkillRoots([]skillRoot{
		{filepath.Join(meta.Dir, ".cursor", "commands"), "project", "commands"},
		{filepath.Join(meta.Dir, ".cursor", "skills"), "project", "skills"},
	}, "/")
}

// readSkillEntries は <root>/*/SKILL.md を読む。frontmatter の name（無ければディレクトリ名）
// が起動名。`user-invocable: false` のスキルはユーザーから呼べないので除外する
// （`disable-model-invocation` は「モデルが勝手に呼ばない」の意味で、ユーザー起動は可 —
// 除外しない。cursor 同梱 review スキル実測）。
func readSkillEntries(root, source string) []sessionSkill {
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	out := []sessionSkill{}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, e.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		meta, _ := splitFrontmatter(string(b))
		if isNo(meta["user-invocable"]) {
			continue
		}
		nm := meta["name"]
		if nm == "" {
			nm = e.Name()
		}
		out = append(out, sessionSkill{
			Name:         nm,
			Description:  meta["description"],
			ArgumentHint: meta["argument-hint"],
			Source:       source,
			Type:         "skill",
		})
	}
	return out
}

// readCommandEntries は <root>/**/*.md を読む。起動名はファイル名（拡張子抜き）—
// サブディレクトリは claude では名前空間表示に使われるだけで起動名には入らない。
func readCommandEntries(root, source string) []sessionSkill {
	out := []sessionSkill{}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable subtree — skip, don't abort the walk
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		meta, _ := splitFrontmatter(string(b))
		out = append(out, sessionSkill{
			Name:         strings.TrimSuffix(d.Name(), ".md"),
			Description:  meta["description"],
			ArgumentHint: meta["argument-hint"],
			Source:       source,
			Type:         "command",
		})
		if len(out) >= maxSessionSkills {
			return filepath.SkipAll
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// isNo は frontmatter の否定値（false/no/off/0）を判定する。
func isNo(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "false", "no", "off", "0":
		return true
	}
	return false
}
