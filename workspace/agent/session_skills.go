package main

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// セッション単位のスキル一覧（docs/50 / ADR0034）: ミラービューのスキルピッカーが
// 「いま話しているセッション」で呼べるスラッシュ起動を列挙する。起動モーダルの
// repo_prompts.go（repo 名 → ~/repos/<name> 固定）とは別物で、こちらは
//   - meta.Dir（worktree 実パス — @branch 作業コピーでもそのセッションの実体）
//   - claude.ConfigDir()（ユーザーレベル ~/.claude 相当。$CLAUDE_CONFIG_DIR を尊重）
// の 2 ルートから .claude/skills + .claude/commands を読む。読み取り専用・都度走査
// （ピッカーを開いた時に 1 回呼ぶだけなのでキャッシュ不要）。プラグイン由来
// （plugins/<mp>/…/skills）と組み込みコマンドは v1 では扱わない（docs/50 積み残し）。

type sessionSkill struct {
	Name         string `json:"name"` // スラッシュ名（先頭の "/" は含まない）
	Description  string `json:"description,omitempty"`
	ArgumentHint string `json:"argumentHint,omitempty"` // frontmatter argument-hint（あれば）
	Source       string `json:"source"`                 // project | user
	Type         string `json:"type"`                   // skill | command
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
	// v1 は claude のみ（.claude/ 規約のスキャン）。他 kind はエラーでなく空を返す —
	// Console 側の caps ゲートが第一防壁で、こちらは前方互換な契約（将来 cursor/kiro の
	// ACP available_commands を同じ形で流し込める）。
	skills := []sessionSkill{}
	if meta.Kind == session.KindClaude {
		skills = scanSlashSkills(filepath.Join(meta.Dir, ".claude"), claude.ConfigDir())
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"skills": skills})
}

// scanSlashSkills は project（<worktree>/.claude）→ user（claude.ConfigDir()）の順で
// skills/ と commands/ を読み、スラッシュ名で重複排除する（先勝ち＝project 優先、
// 同一ルート内では skill 優先 — claude の実挙動どおりスラッシュ名前空間は 1 つ）。
func scanSlashSkills(projectBase, userBase string) []sessionSkill {
	out := []sessionSkill{}
	seen := map[string]bool{}
	add := func(items []sessionSkill) {
		for _, it := range items {
			if it.Name == "" || seen[it.Name] || len(out) >= maxSessionSkills {
				continue
			}
			seen[it.Name] = true
			out = append(out, it)
		}
	}
	for _, root := range []struct{ base, source string }{
		{projectBase, "project"},
		{userBase, "user"},
	} {
		if root.base == "" {
			continue
		}
		add(readSkillEntries(filepath.Join(root.base, "skills"), root.source))
		add(readCommandEntries(filepath.Join(root.base, "commands"), root.source))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// readSkillEntries は <root>/*/SKILL.md を読む。frontmatter の name（無ければディレクトリ名）
// がスラッシュ名。`user-invocable: false` のスキルはユーザーから呼べないので除外する。
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

// readCommandEntries は <root>/**/*.md を読む。スラッシュ名はファイル名（拡張子抜き）—
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
