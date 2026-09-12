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
//   - claude:   .claude/skills + .claude/commands (project = the CWD chain, see claudeSkillDirs /
//               user = claude.ConfigDir()) → "/name", plus the CLI's bundled skills (dataviz,
//               simplify, …) from the SDK init frame as "cli" (§9)
//   - codex:    .codex/skills (project = the CWD chain up to the git root, see codexSkillDirs) +
//               $CODEX_HOME/skills (user; the bundled .system counts as cli) → "$name" mention
//   - opencode: .opencode/command(s) (project) + ~/.config/opencode/command(s) (user) → "/name"
//   - cursor:   the ACP advertised list (builtin skills + global + project, all of it) is authoritative.
//               With no runtime, fall back to the project's .cursor/commands + .cursor/skills → "/name"
// On top of that, every chat kind also gets the other conventions' SKILL.md trees as foreign
// entries (cross-skill injection — §8): a skill the CLI does not discover by itself can still be
// run through a "read Path and follow its instructions" prompt, which writes no files and cares
// about neither kind nor driver. shell/ssm come back empty.
// Read-only and rescanned each time — the picker calls this once on open, so no cache.
//
// Everything below starts from meta.CWD(), not meta.Dir: a session launched with a Subdir runs
// BELOW the working copy root, and each CLI resolves its project scope from its own CWD. Scanning
// meta.Dir alone missed the folders in between (claude) and offered skills the CLI cannot resolve
// at all (codex outside a git repo) — measured 2026-09-12, docs/log/50 §10.

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
	// CLI does not discover by itself. Path is the SKILL.md as seen from the session's CWD
	// (relative when it lives there, absolute when it sits above — §10), Origin the convention
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
	cwd := meta.CWD()
	switch meta.Kind {
	case session.KindClaude:
		skills = claudeSkills(cwd, meta.Dir)
		skills = appendBundledSkills(skills, claudeBundledSkills())
		nativeConvs = []string{".claude/skills"}
	case session.KindCodex:
		skills = codexSkills(cwd, meta.Dir)
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
	skills = appendForeignSkills(skills, chainUp(cwd, meta.Dir), cwd, nativeConvs)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"skills": skills})
}

// claudeBundledSkills is the probe for the skills the claude CLI ships (docs/log/50 §9); a
// variable so tests can stand in for it instead of starting a real claude.
var claudeBundledSkills = claude.BundledSkills

// appendBundledSkills adds the CLI-advertised skill names that the filesystem scan did not
// already produce, as source "cli" entries (same treatment as codex's .system and cursor's
// builtin list). The advertised list mixes in the user-level skills too, which the scan already
// knows by name, hence the dedupe. Names only: the Console supplies descriptions for the
// well-known ones.
func appendBundledSkills(native []sessionSkill, advertised []string) []sessionSkill {
	if len(advertised) == 0 {
		return native
	}
	seen := map[string]bool{}
	for _, s := range native {
		seen[s.Name] = true
	}
	out := native
	for _, nm := range advertised {
		if nm == "" || seen[nm] || len(out) >= maxSessionSkills {
			continue
		}
		seen[nm] = true
		out = append(out, sessionSkill{Name: nm, Source: "cli", Type: "skill", Invoke: "/" + nm + " "})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// foreignConvs are the in-repo SKILL.md tree conventions consulted as foreign (§8). The commands
// families (md whose body is the prompt) are out of scope for v1.
var foreignConvs = []string{".claude/skills", ".codex/skills", ".agents/skills"}

// appendForeignSkills adds skills from OTHER conventions' SKILL.md trees as injection candidates
// (empty Invoke, Path/Origin set). dirs is the CWD chain (deepest first), cwd the directory the
// agent actually runs in — the Path has to resolve from there, since the Console pastes it into a
// "read Path" prompt. A name that also exists natively keeps the native entry, and
// `user-invocable: false` is excluded here too.
func appendForeignSkills(native []sessionSkill, dirs []string, cwd string, nativeConvs []string) []sessionSkill {
	seen := map[string]bool{}
	for _, s := range native {
		seen[s.Name] = true
	}
	skip := map[string]bool{}
	for _, c := range nativeConvs {
		skip[c] = true
	}
	out := native
	for _, dir := range dirs {
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
				// Relative to the CWD when the tree is right there (the usual case, and what the
				// picker has always shown); absolute for a tree that sits above it, where a
				// relative path would be read against the wrong directory.
				path := conv + "/" + e.Name() + "/SKILL.md"
				if filepath.Clean(dir) != filepath.Clean(cwd) {
					path = filepath.Join(root, e.Name(), "SKILL.md")
				}
				out = append(out, sessionSkill{
					Name:         nm,
					Description:  fm["description"],
					ArgumentHint: fm["argument-hint"],
					Source:       "project",
					Type:         "skill",
					Path:         path,
					Origin:       strings.SplitN(conv, "/", 2)[0], // ".claude" | ".codex" | ".agents"
				})
			}
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

// chainUp walks from start upward, deepest first, and stops after stop (inclusive). A start that
// is not below stop yields start alone rather than the whole filesystem.
func chainUp(start, stop string) []string {
	start, stop = filepath.Clean(start), filepath.Clean(stop)
	out := []string{}
	for d := start; ; {
		out = append(out, d)
		if d == stop {
			return out
		}
		p := filepath.Dir(d)
		if p == d {
			return []string{start}
		}
		d = p
	}
}

// under reports whether p is base itself or below it.
func under(base, p string) bool {
	rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(p))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// claudeSkillDirs mirrors what the CLI itself discovers (measured 2026-09-12, docs/log/50 §10):
// claude reads <d>/.claude/skills and <d>/.claude/commands for the CWD and EVERY ancestor,
// stopping before $HOME — $HOME/.claude is the personal root, which AF redirects to
// CLAUDE_CONFIG_DIR, so it is scanned separately below. Deepest first: the nearer copy of a name
// is the one the CLI runs, and scanSkillRoots keeps the first it sees.
// A CWD outside the home tree (tests, odd mounts) falls back to the working copy chain, so a
// scan never wanders off into shared directories like /tmp.
func claudeSkillDirs(cwd, dir string) []string {
	home, _ := os.UserHomeDir()
	if home = filepath.Clean(home); home != "." && under(home, cwd) && home != filepath.Clean(cwd) {
		if ds := chainUp(cwd, home); len(ds) > 1 {
			return ds[:len(ds)-1] // $HOME itself is the personal root, not a project one
		}
	}
	return chainUp(cwd, dir)
}

// claudeSkills covers claude: the CWD chain's .claude directories (skills before commands within
// one of them — claude has a single slash namespace) and then the user root. Levels above the
// working copy are real to the CLI but belong to the environment rather than to this repo, so
// they are labelled "user".
func claudeSkills(cwd, dir string) []sessionSkill {
	roots := []skillRoot{}
	for _, d := range claudeSkillDirs(cwd, dir) {
		src := "project"
		if !under(dir, d) {
			src = "user"
		}
		base := filepath.Join(d, ".claude")
		roots = append(roots,
			skillRoot{filepath.Join(base, "skills"), src, "skills"},
			skillRoot{filepath.Join(base, "commands"), src, "commands"},
		)
	}
	userBase := claude.ConfigDir()
	return scanSkillRoots(append(roots,
		skillRoot{filepath.Join(userBase, "skills"), "user", "skills"},
		skillRoot{filepath.Join(userBase, "commands"), "user", "commands"},
	), "/")
}

// codexSkillDirs: codex resolves project skills from the CWD up to the GIT ROOT and no further,
// and with no .git above the CWD it reads the CWD alone (measured 2026-09-12 on 0.154 — a
// .codex/skills one level up was invisible, and visible right after a `git init`). So this cannot
// use meta.Dir: an SVN working copy has no .git at all, and offering its skills as "$name" would
// offer something codex cannot resolve.
func codexSkillDirs(cwd string) []string {
	if root, ok := gitRoot(cwd); ok {
		return chainUp(cwd, root)
	}
	return []string{filepath.Clean(cwd)}
}

// gitRoot finds the nearest ancestor of p (p included) holding a .git entry — a directory in a
// plain clone, a file in a worktree, so one Stat answers both.
func gitRoot(p string) (string, bool) {
	for d := filepath.Clean(p); ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d, true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", false
		}
		d = parent
	}
}

// codexSkills: the SKILL.md convention is claude-compatible (measured on 0.145 — frontmatter
// name/description, $CODEX_HOME/skills auto-discovered, and on the repo side both .codex/skills
// and .agents/skills, both recognized by `codex exec`). The bundled .system does not show up in a
// flat scan of the user root (its SKILL.md files are not directly below it), so it is picked up
// as a root of its own and marked with source "cli". Invocation is a "$name" mention, not a slash
// — measured in the binary's system prompt: "names an available skill (with $SkillName …)".
// .codex/skills also holds the links the skill bridge (docs/log/50 §8) laid to claude skills;
// os.ReadFile follows them like any other file, so they need no special case.
func codexSkills(cwd, dir string) []sessionSkill {
	roots := []skillRoot{}
	for _, d := range codexSkillDirs(cwd) {
		src := "project"
		if !under(dir, d) {
			src = "user"
		}
		roots = append(roots,
			skillRoot{filepath.Join(d, ".codex", "skills"), src, "skills"},
			skillRoot{filepath.Join(d, ".agents", "skills"), src, "skills"},
		)
	}
	home := paths.CodexHome()
	return scanSkillRoots(append(roots,
		skillRoot{filepath.Join(home, "skills"), "user", "skills"},
		skillRoot{filepath.Join(home, "skills", ".system"), "cli", "skills"},
	), "$")
}

// opencodeSkills enumerates command md files (the body is the prompt, the description comes from
// frontmatter). Both the singular and the plural directory name exist in the wild (measured in
// the 1.18.8 binary: the strings .opencode/command/deploy.md and .opencode/commands/).
// .opencode/skills is for model invocation and slash invocation is unverified, so it is out of
// scope (docs/log/50 §7). Still the working copy root rather than the CWD chain: opencode's own
// command discovery under a Subdir has not been measured, and guessing either way would trade one
// wrong answer for another (docs/log/50 §10, open point). Same for cursor, whose advertised list
// is authoritative anyway.
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
