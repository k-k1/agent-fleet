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

// セッション単位のスキル一覧（docs/log/50 / ADR0034、v2 でクロスエージェント化）:
// ミラービューのスキルピッカーが「いま話しているセッション」で呼べる起動可能な
// スキル/コマンドを列挙する。kind ごとにソースと起動形が違う（全て 2026-07-28 実測、
// docs/log/50 §7）:
//   - claude:   .claude/skills + .claude/commands（project = meta.Dir / user = claude.ConfigDir()）→ "/name"
//   - codex:    .codex/skills（project）+ $CODEX_HOME/skills（user・同梱 .system は cli 扱い）→ "$name" メンション
//   - opencode: .opencode/command(s)（project）+ ~/.config/opencode/command(s)（user）→ "/name"
//   - cursor:   ACP 広告リスト（builtin skill ＋ global ＋ project 全部入り）が正。
//               runtime 不在時は project の .cursor/commands + .cursor/skills へフォールバック → "/name"
// さらに全 chat kind へ、他規約の SKILL.md ツリーを **foreign（クロススキル注入 — §8）**
// として足す: CLI が自力で発見しないスキルも「Path を読んで指示に従え」プロンプトで
// 実行できる（ファイル無書込・kind/ドライバ不問）。shell/ssm は空。
// 読み取り専用・都度走査（ピッカーを開いた時に 1 回呼ぶだけなのでキャッシュ不要）。

type sessionSkill struct {
	Name         string `json:"name"` // 起動名（"/" や "$" のプレフィックスは含まない）
	Description  string `json:"description,omitempty"`
	ArgumentHint string `json:"argumentHint,omitempty"` // frontmatter argument-hint（あれば）
	Source       string `json:"source"`                 // project | user | cli（同梱/CLI 広告）
	Type         string `json:"type"`                   // skill | command
	// ネイティブ起動: コンポーザーへ差し込む起動文字列（末尾空白込み。"/name " / "$name "）。
	// foreign エントリ（下記）では空。
	Invoke string `json:"invoke,omitempty"`
	// クロススキル注入（docs/log/50 §8）: この kind の CLI が自力では発見しない他規約の
	// スキル。Path は repo 相対の SKILL.md、Origin は規約ディレクトリ（".claude" 等）。
	// Console はこれを「Path を読んで指示に従え」というプロンプトに組んで差し込む —
	// ただの指示文なので kind もドライバも選ばない。
	Path   string `json:"path,omitempty"`
	Origin string `json:"origin,omitempty"`
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
	// ネイティブ列挙（kind の CLI が自力で発見・起動できるもの）に加え、他規約の
	// SKILL.md ツリーを foreign（注入方式 — §8）として足す。shell/ssm はチャットが
	// 無いので空。Console 側の caps ゲートが第一防壁。
	skills := []sessionSkill{}
	var nativeConvs []string
	switch meta.Kind {
	case session.KindClaude:
		skills = scanSlashSkills(filepath.Join(meta.Dir, ".claude"), claude.ConfigDir())
		nativeConvs = []string{".claude/skills"}
	case session.KindCodex:
		skills = codexSkills(meta.Dir)
		nativeConvs = []string{".codex/skills", ".agents/skills"}
	case session.KindOpencode:
		skills = opencodeSkills(meta.Dir)
	case session.KindCursor:
		skills = cursorSkills(meta)
	case session.KindKiro, session.KindCopilot, session.KindAgy:
		// ネイティブ列挙なし（ユーザー起動可能な機構が未確認/未検証 — §7）。foreign 注入のみ。
	default:
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"skills": skills})
		return
	}
	skills = appendForeignSkills(skills, meta.Dir, nativeConvs)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"skills": skills})
}

// foreignConvs — foreign として見に行く repo 内の SKILL.md ツリー規約（§8）。
// commands 系（本文がプロンプトの md）は v1 対象外。
var foreignConvs = []string{".claude/skills", ".codex/skills", ".agents/skills"}

// appendForeignSkills adds skills from OTHER conventions' SKILL.md trees as
// injection candidates（Invoke 空・Path/Origin 付き）。ネイティブと同名は
// ネイティブ勝ち。`user-invocable: false` はここでも除外する。
func appendForeignSkills(native []sessionSkill, dir string, nativeConvs []string) []sessionSkill {
	seen := map[string]bool{}
	for _, s := range native {
		seen[s.Name] = true
	}
	skip := map[string]bool{}
	for _, c := range nativeConvs {
		skip[c] = true
	}
	out := native
	for _, conv := range foreignConvs {
		if skip[conv] {
			continue
		}
		root := filepath.Join(dir, filepath.FromSlash(conv))
		ents, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			b, err := os.ReadFile(filepath.Join(root, e.Name(), "SKILL.md"))
			if err != nil {
				continue
			}
			fm, _ := splitFrontmatter(string(b))
			if isNo(fm["user-invocable"]) {
				continue
			}
			nm := fm["name"]
			if nm == "" {
				nm = e.Name()
			}
			if nm == "" || seen[nm] || len(out) >= maxSessionSkills {
				continue
			}
			seen[nm] = true
			out = append(out, sessionSkill{
				Name:         nm,
				Description:  fm["description"],
				ArgumentHint: fm["argument-hint"],
				Source:       "project",
				Type:         "skill",
				Path:         conv + "/" + e.Name() + "/SKILL.md",
				Origin:       strings.SplitN(conv, "/", 2)[0], // ".claude" | ".codex" | ".agents"
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
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
// $CODEX_HOME/skills が auto-discover、repo 側は .codex/skills と .agents/skills の両方
// — `codex exec` でどちらも認識されることを実測）。同梱の .system は user ルートの直下
// 走査には掛からない（SKILL.md がディレクトリ直下に無い）ので別ルートとして拾い、
// source "cli" で区別する。起動は "$name" メンション（スラッシュではない — バイナリの
// システムプロンプト実測「names an available skill (with $SkillName …)」）。
// .codex/skills にはスキルブリッジ（docs/log/50 §8）が張った claude スキルへのリンクも
// 含まれる — 通常ファイルと同様に os.ReadFile が辿るので特別扱い不要。
func codexSkills(dir string) []sessionSkill {
	home := paths.CodexHome()
	return scanSkillRoots([]skillRoot{
		{filepath.Join(dir, ".codex", "skills"), "project", "skills"},
		{filepath.Join(dir, ".agents", "skills"), "project", "skills"},
		{filepath.Join(home, "skills"), "user", "skills"},
		{filepath.Join(home, "skills", ".system"), "cli", "skills"},
	}, "$")
}

// opencodeSkills: コマンド md（本文がプロンプト・frontmatter description）を列挙する。
// ディレクトリ名は単複両方が実在する（1.18.8 バイナリ実測: .opencode/command/deploy.md と
// .opencode/commands/ の両文字列）。.opencode/skills は model 起動用でスラッシュ起動が
// 未検証のため対象外（docs/log/50 §7）。
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
