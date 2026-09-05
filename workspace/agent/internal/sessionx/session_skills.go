package sessionx

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

// Per-session skill list (docs/log/50 / ADR0034, made cross-agent in v2): what the mirror view's
// skill picker can offer for the session the user is talking to right now. Source and invocation
// form differ per kind (all measured 2026-07-28, docs/log/50 §7):
//   - claude:   .claude/skills + .claude/commands (project = meta.Dir / user = claude.ConfigDir()) → "/name"
//   - codex:    .codex/skills (project) + $CODEX_HOME/skills (user; the bundled .system counts as cli) → "$name" mention
//   - opencode: .opencode/command(s) (project) + ~/.config/opencode/command(s) (user) → "/name"
//   - cursor:   the ACP advertised list (builtin skills + global + project, all of it) is authoritative.
//               With no runtime, fall back to the project's .cursor/commands + .cursor/skills → "/name"
// On top of that, every chat kind also gets the other conventions' SKILL.md trees as foreign
// entries (cross-skill injection — §8): a skill the CLI does not discover by itself can still be
// run through a "read Path and follow its instructions" prompt, which writes no files and cares
// about neither kind nor driver. shell/ssm come back empty.
// Read-only and rescanned each time — the picker calls this once on open, so no cache.

type sessionSkill struct {
	Name         string `json:"name"` // invocation name (without the "/" or "$" prefix)
	Description  string `json:"description,omitempty"`
	ArgumentHint string `json:"argumentHint,omitempty"` // frontmatter argument-hint, when present
	Source       string `json:"source"`                 // project | user | cli (bundled / CLI-advertised)
	Type         string `json:"type"`                   // skill | command
	// Native invocation: the string to drop into the composer, trailing space included
	// ("/name " / "$name "). Empty on foreign entries (below).
	Invoke string `json:"invoke,omitempty"`
	// Cross-skill injection (docs/log/50 §8): a skill from another convention that this kind's
	// CLI does not discover by itself. Path is the repo-relative SKILL.md, Origin the convention
	// directory (".claude" and friends). The Console turns these into a "read Path and follow its
	// instructions" prompt, which is plain text and so works for any kind and any driver.
	Path   string `json:"path,omitempty"`
	Origin string `json:"origin,omitempty"`
}

const maxSessionSkills = 200 // overall cap, the same safety valve as repo_prompts' maxPromptItems

func HandleSessionSkills(w http.ResponseWriter, r *http.Request) {
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
	// Native enumeration (what the kind's own CLI can discover and invoke) plus the other
	// conventions' SKILL.md trees as foreign entries (the injection route — §8). shell/ssm have
	// no chat, so they come back empty; the Console's caps gate is the first line of defence.
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
		// No native enumeration: no user-invocable mechanism confirmed yet (§7). Foreign only.
	default:
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"skills": skills})
		return
	}
	skills = appendForeignSkills(skills, meta.Dir, nativeConvs)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"skills": skills})
}

// foreignConvs are the in-repo SKILL.md tree conventions consulted as foreign (§8). The commands
// families (md whose body is the prompt) are out of scope for v1.
var foreignConvs = []string{".claude/skills", ".codex/skills", ".agents/skills"}

// appendForeignSkills adds skills from OTHER conventions' SKILL.md trees as injection candidates
// (empty Invoke, Path/Origin set). A name that also exists natively keeps the native entry, and
// `user-invocable: false` is excluded here too.
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

// scanSkillRoots reads roots in order and dedupes by name, first one wins. Callers order the
// roots so that user ranks below project and a skill beats a command. invokePrefix is the kind's
// invocation form ("/" or "$").
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

// scanSlashSkills covers claude: project (<worktree>/.claude) then user (claude.ConfigDir()),
// skills before commands within one root — claude has a single slash namespace.
func scanSlashSkills(projectBase, userBase string) []sessionSkill {
	return scanSkillRoots([]skillRoot{
		{filepath.Join(projectBase, "skills"), "project", "skills"},
		{filepath.Join(projectBase, "commands"), "project", "commands"},
		{filepath.Join(userBase, "skills"), "user", "skills"},
		{filepath.Join(userBase, "commands"), "user", "commands"},
	}, "/")
}

// codexSkills: the SKILL.md convention is claude-compatible (measured on 0.145 — frontmatter
// name/description, $CODEX_HOME/skills auto-discovered, and on the repo side both .codex/skills
// and .agents/skills, both recognized by `codex exec`). The bundled .system does not show up in a
// flat scan of the user root (its SKILL.md files are not directly below it), so it is picked up
// as a root of its own and marked with source "cli". Invocation is a "$name" mention, not a slash
// — measured in the binary's system prompt: "names an available skill (with $SkillName …)".
// .codex/skills also holds the links the skill bridge (docs/log/50 §8) laid to claude skills;
// os.ReadFile follows them like any other file, so they need no special case.
func codexSkills(dir string) []sessionSkill {
	home := paths.CodexHome()
	return scanSkillRoots([]skillRoot{
		{filepath.Join(dir, ".codex", "skills"), "project", "skills"},
		{filepath.Join(dir, ".agents", "skills"), "project", "skills"},
		{filepath.Join(home, "skills"), "user", "skills"},
		{filepath.Join(home, "skills", ".system"), "cli", "skills"},
	}, "$")
}

// opencodeSkills enumerates command md files (the body is the prompt, the description comes from
// frontmatter). Both the singular and the plural directory name exist in the wild (measured in
// the 1.18.8 binary: the strings .opencode/command/deploy.md and .opencode/commands/).
// .opencode/skills is for model invocation and slash invocation is unverified, so it is out of
// scope (docs/log/50 §7).
func opencodeSkills(dir string) []sessionSkill {
	cfg := paths.OpencodeConfigDir()
	return scanSkillRoots([]skillRoot{
		{filepath.Join(dir, ".opencode", "command"), "project", "commands"},
		{filepath.Join(dir, ".opencode", "commands"), "project", "commands"},
		{filepath.Join(cfg, "command"), "user", "commands"},
		{filepath.Join(cfg, "commands"), "user", "commands"},
	}, "/")
}

// cursorSkills: the CLI's advertised list (ACP available_commands_update, shared by the driver
// through agents.PublishCommands) is the only complete source — builtin skills, global, and the
// project's commands/skills, all of it (measured 2026-07-28). Until it arrives (runtime not
// started, or just after an agent restart) fall back to the project's filesystem conventions.
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

// readSkillEntries reads <root>/*/SKILL.md. The invocation name is the frontmatter name, or the
// directory name when there is none. `user-invocable: false` is excluded, since the user cannot
// call it. `disable-model-invocation` is NOT excluded: it only means the model must not reach for
// the skill on its own, and user invocation stays allowed (measured on cursor's bundled review
// skill).
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

// readCommandEntries reads <root>/**/*.md. The invocation name is the file name without its
// extension: in claude a subdirectory only shows up as a namespace label and is not part of the
// name you type.
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

// isNo reports whether a frontmatter value is a negative one (false/no/off/0).
func isNo(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "false", "no", "off", "0":
		return true
	}
	return false
}
