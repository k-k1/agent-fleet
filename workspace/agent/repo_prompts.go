package main

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// Prompt templates surfaced in the repo row's 起動 modal (LaunchModal). Aggregated
// read-only from files already in the working copy, so teams curate them in-repo
// (committed, versioned) rather than in a separate store:
//   - .claude/commands/**/*.md  → コマンド   (Claude Code slash commands; the body IS the prompt)
//   - .claude/skills/*/SKILL.md → スキル     (seed "/name " to invoke it; claude-flavored)
//   - .agent-fleet/launch-prompts.md → テンプレート (one entry per "## heading" section; agent-neutral)
//
// Recent-prompt history is client-side (localStorage), added by the modal — not here.
// Variable expansion ({{repo}}/{{branch}}/{{path}}) is done client-side too; the bodies
// are returned verbatim.

type promptItem struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Body  string `json:"body"`
}

type promptGroup struct {
	Source string       `json:"source"` // command | skill | file
	Label  string       `json:"label"`
	Items  []promptItem `json:"items"`
}

const maxPromptItems = 200 // per source; a sanity cap so a huge tree can't balloon the response

func handleRepoPromptTemplates(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	dir, ok := gitx.ResolveRepoDir(name)
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_repo", "invalid repo name")
		return
	}
	groups := []promptGroup{}
	if cmds := readCommandTemplates(dir); len(cmds) > 0 {
		groups = append(groups, promptGroup{Source: "command", Label: "コマンド", Items: cmds})
	}
	if sk := readSkillTemplates(dir); len(sk) > 0 {
		groups = append(groups, promptGroup{Source: "skill", Label: "スキル", Items: sk})
	}
	if f := readFileTemplates(dir); len(f) > 0 {
		groups = append(groups, promptGroup{Source: "file", Label: "テンプレート", Items: f})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"groups": groups})
}

// splitFrontmatter peels a leading "---\n … \n---" YAML block off a Markdown file and
// returns its top-level `key: value` pairs (lowercased keys, quotes trimmed) plus the
// remaining body. No YAML dependency — commands/skills only use flat scalar frontmatter.
func splitFrontmatter(s string) (map[string]string, string) {
	meta := map[string]string{}
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return meta, s
	}
	rest := s[strings.IndexByte(s, '\n')+1:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return meta, s // unterminated block — treat the whole file as body
	}
	fm := rest[:end]
	body := rest[end+len("\n---"):]
	if i := strings.IndexByte(body, '\n'); i >= 0 { // drop the rest of the closing --- line
		body = body[i+1:]
	} else {
		body = ""
	}
	for _, ln := range strings.Split(fm, "\n") {
		ln = strings.TrimRight(ln, "\r")
		if i := strings.IndexByte(ln, ':'); i > 0 {
			k := strings.ToLower(strings.TrimSpace(ln[:i]))
			v := strings.Trim(strings.TrimSpace(ln[i+1:]), `"'`)
			meta[k] = v
		}
	}
	return meta, strings.TrimLeft(body, "\n")
}

// readCommandTemplates lists .claude/commands/**/*.md — Claude Code custom slash
// commands. The id keeps the namespace ("git/commit"), the label is the frontmatter
// description (falling back to the namespaced name), and the body (frontmatter stripped)
// is the prompt itself.
func readCommandTemplates(dir string) []promptItem {
	root := filepath.Join(dir, ".claude", "commands")
	out := []promptItem{}
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
		rel, _ := filepath.Rel(root, p)
		id := strings.TrimSuffix(filepath.ToSlash(rel), ".md")
		meta, body := splitFrontmatter(string(b))
		label := meta["description"]
		if label == "" {
			label = strings.ReplaceAll(id, "/", ":")
		}
		out = append(out, promptItem{ID: id, Label: label, Body: strings.TrimSpace(body)})
		if len(out) >= maxPromptItems {
			return filepath.SkipAll
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// readSkillTemplates lists .claude/skills/*/SKILL.md. A skill is a capability, not a
// task, so we seed its slash-invocation ("/name ") and let the user append specifics;
// the label is the skill's description. Claude-flavored (gated to claude in the modal).
func readSkillTemplates(dir string) []promptItem {
	root := filepath.Join(dir, ".claude", "skills")
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	out := []promptItem{}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, e.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		meta, _ := splitFrontmatter(string(b))
		nm := meta["name"]
		if nm == "" {
			nm = e.Name()
		}
		label := meta["description"]
		if label == "" {
			label = nm
		}
		out = append(out, promptItem{ID: nm, Label: label, Body: "/" + nm + " "})
		if len(out) >= maxPromptItems {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// readFileTemplates parses .agent-fleet/launch-prompts.md — a team-curated, launch-only
// file — into one template per "## heading" section (heading = label, following lines =
// body). A file with no "##" headings becomes a single template.
func readFileTemplates(dir string) []promptItem {
	b, err := os.ReadFile(filepath.Join(dir, ".agent-fleet", "launch-prompts.md"))
	if err != nil {
		return nil
	}
	_, content := splitFrontmatter(string(b))
	out := []promptItem{}
	var curLabel string
	var buf []string
	flush := func() {
		if curLabel == "" {
			return
		}
		out = append(out, promptItem{ID: curLabel, Label: curLabel, Body: strings.TrimSpace(strings.Join(buf, "\n"))})
		buf = nil
	}
	for _, ln := range strings.Split(content, "\n") {
		trimmed := strings.TrimRight(ln, "\r")
		if h := strings.TrimPrefix(trimmed, "## "); h != trimmed {
			flush()
			curLabel = strings.TrimSpace(h)
			if len(out) >= maxPromptItems {
				curLabel = ""
				break
			}
			continue
		}
		if curLabel != "" {
			buf = append(buf, trimmed)
		}
	}
	flush()
	if len(out) == 0 {
		if body := strings.TrimSpace(content); body != "" {
			out = append(out, promptItem{ID: "launch-prompts", Label: "launch-prompts", Body: body})
		}
	}
	return out
}
