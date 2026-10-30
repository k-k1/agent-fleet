package sessionx

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/muse"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/fleetskills"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/gitx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/harness"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/pathguard"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Per-session skill list (docs/log/50 / ADR0034, made cross-agent in v2): what the mirror view's
// skill picker can offer for the session the user is talking to right now. Source and invocation
// form differ per kind (all measured 2026-07-28, docs/log/50 §7); each kind states its own as an
// agents.SkillsLayout (internal/agents/<kind>/capabilities.go):
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
// about neither kind nor driver. The other kinds' user-level roots (claude, codex, muse) are
// offered the same way (appendUserForeignSkills). shell/ssm come back empty.
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
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"skills": enumerateSkills(meta.Kind, meta.Dir, meta.CWD(), meta.Name)})
}

// HandleRepoSkills is the same list for a session that does not exist yet: the launch modal's
// first prompt offers what a session of ?kind= would see when started in the working
// copy (plus ?subdir=). A worktree launch is answered from the repository's own tree, since
// the worktree is only created by the launch. The kinds whose native list lives in a running
// process (cursor's advertised commands, muse's skill/list) fall back exactly as a stopped
// session of theirs does.
func HandleRepoSkills(w http.ResponseWriter, r *http.Request) {
	dir, ok := gitx.ResolveRepoDir(r.PathValue("name"))
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_repo", "invalid repo name")
		return
	}
	if !session.DirExists(dir) {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such repo: "+r.PathValue("name"))
		return
	}
	sub, ok := session.CleanSubdir(r.URL.Query().Get("subdir"))
	// CleanSubdir is lexical: a symlink inside the repo pointing elsewhere would otherwise list
	// that tree's skills. A subdir that does not exist yet still passes (CWD falls back to dir).
	if ok && sub != "" {
		_, ok = pathguard.ResolveUnder(filepath.Join(dir, filepath.FromSlash(sub)), dir)
	}
	if !ok {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_subdir", "subdir must be a relative path inside the working copy")
		return
	}
	cwd := session.Meta{Dir: dir, Subdir: sub}.CWD()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"skills": enumerateSkills(r.URL.Query().Get("kind"), dir, cwd, "")})
}

// enumerateSkills is the list for a kind running in cwd beneath the working copy dir. live
// names the session whose running process can be asked for its native list; "" means there is
// none, and those kinds answer from the filesystem alone.
func enumerateSkills(kind, dir, cwd, live string) []sessionSkill {
	// Native enumeration (what the kind's own CLI can discover and invoke, agents.SkillsLayout)
	// plus the other conventions' SKILL.md trees as foreign entries (the injection route — §8).
	// A kind without a layout (shell, ssm) has no chat and comes back empty; the Console's caps
	// gate is the first line of defence.
	l, ok := skillsLayoutOf(kind)
	if !ok {
		return []sessionSkill{}
	}
	skills, nativeConvs := layoutNativeSkills(l, dir, cwd, live)
	ownNative := len(skills) > 0 // a scanned root, or the live list when there is one
	// A kind fenced to its CWD (lcpp's file tools refuse any path outside it, harness
	// resolvePath) would be offered a tree above a Subdir CWD, a SKILL.md symlinked out of it or
	// a user root, and then fail to open it. Every other kind reads through its CLI, which has no
	// such fence.
	var readable func(string) bool
	if l.FenceToCWD {
		readable = func(p string) bool { return harness.Readable(cwd, p) }
	}
	skills = appendForeignSkills(skills, chainUp(cwd, dir), cwd, nativeConvs, readable)
	if !l.FenceToCWD {
		skills = appendUserForeignSkills(skills, kind, ownNative)
	}
	return skills
}

// museSkills is the native half for muse: MSP's `skill/list`, asked of the session's own live
// host (ADR 0095 P2-23). Empty when the session is not running — the caller then leaves
// `.agents/skills` to the foreign (injection) route, which is what muse had before this.
//
// The wire's scope vocabulary is mapped onto the picker's three sources rather than passed
// through: `bundled` and `plugin` are both "came with the tool, not with your repository",
// which is what this column means to a reader, and an unknown future scope lands there too
// rather than inventing a fourth label the Console has no string for.
func museSkills(live string) []sessionSkill {
	if live == "" {
		return nil
	}
	native := museNativeSkills(live)
	out := make([]sessionSkill, 0, len(native))
	for _, s := range native {
		if s.Selector == "" || len(out) >= maxSessionSkills {
			continue
		}
		out = append(out, sessionSkill{
			Name:         s.Selector,
			Description:  s.Description,
			ArgumentHint: s.ArgumentHint,
			Source:       museSkillSource(s.Source),
			Type:         "skill",
			Invoke:       "/" + s.Selector + " ",
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func museSkillSource(scope string) string {
	switch scope {
	case "project", "user":
		return scope
	default: // bundled, plugin, and anything a later release adds
		return "cli"
	}
}

// museNativeSkills is the `skill/list` call, as a variable so tests can stand in for it — the
// real one needs a running muse host, which a unit test has no business starting.
var museNativeSkills = muse.Skills

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
// `user-invocable: false` is excluded here too. readable, when non-nil, drops every entry whose
// Path it rejects, before the entry can claim its name.
func appendForeignSkills(native []sessionSkill, dirs []string, cwd string, nativeConvs []string, readable func(string) bool) []sessionSkill {
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
			// Relative to the CWD when the tree is right there (the usual case, and what the
			// picker has always shown); absolute for a tree that sits above it, where a
			// relative path would be read against the wrong directory.
			rel := filepath.Clean(dir) == filepath.Clean(cwd)
			out = appendForeignRoot(out, seen, root, "project", strings.SplitN(conv, "/", 2)[0], func(name string) string {
				p := filepath.Join(root, name, "SKILL.md")
				if rel {
					p = conv + "/" + name + "/SKILL.md"
				}
				if readable != nil && !readable(p) {
					return ""
				}
				return p
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// userSkillRoot is another kind's user-level skills root, offered as foreign entries.
type userSkillRoot struct {
	kind   string // the session kind that owns the root, so a session never gets its own back
	origin string // the Origin badge (".claude" | ".codex" | ".muse")
	dir    func() string
}

// appendUserForeignSkills adds the OTHER kinds' user-level skills the same way
// appendForeignSkills adds the repository's: a skill set up for claude is then one pick away
// from a codex or muse session too, with no copy anywhere. After the project entries, so a
// project skill of the same name wins, and after the native ones, so the CLI's own copy wins.
// ownNative says the session's own root is already covered by its native list; when it is not
// (a muse session with no live host to ask), that root is offered like any other.
//
// The fleet topic skills (fleetskills) are skipped: AF writes the same set into every root, and
// the policy text already points every kind at the files they wrap.
func appendUserForeignSkills(skills []sessionSkill, kind string, ownNative bool) []sessionSkill {
	seen := map[string]bool{}
	for _, s := range skills {
		seen[s.Name] = true
	}
	out := skills
	for _, r := range sharedSkillRoots() {
		if r.kind == kind && ownNative {
			continue
		}
		root := r.dir()
		out = appendForeignRoot(out, seen, root, "user", r.origin, func(name string) string {
			return filepath.Join(root, name, "SKILL.md")
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// appendForeignRoot adds one <root>/*/SKILL.md tree as foreign entries, skipping names already
// in seen (and recording the ones it adds), `user-invocable: false` and AF's own fleet skills.
// pathOf turns a skill's directory name into the Path the injection prompt will read; "" skips
// the entry.
func appendForeignRoot(out []sessionSkill, seen map[string]bool, root, source, origin string, pathOf func(string) string) []sessionSkill {
	ents, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, e.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		if strings.Contains(string(b), fleetskills.Marker) {
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
		path := pathOf(e.Name())
		if nm == "" || path == "" || seen[nm] || len(out) >= maxSessionSkills {
			continue
		}
		seen[nm] = true
		out = append(out, sessionSkill{
			Name:         nm,
			Description:  fm["description"],
			ArgumentHint: fm["argument-hint"],
			Source:       source,
			Type:         "skill",
			Path:         path,
			Origin:       origin,
		})
	}
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

// claudeSkillDirs (agents.SkillDirsCWDToHome) mirrors what the claude CLI itself discovers
// (measured 2026-09-12, docs/log/50 §10):
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

// projectSkillRoots is the repository side of kind's discovery (its agents.SkillsLayout), in the
// CLI's precedence order; the Repository "Skills" view (#2795) reads it, and the picker reads the
// same layout, so the two cannot drift. Kinds with no repository roots return nothing.
func projectSkillRoots(kind, cwd, dir string) []skillRoot {
	l, _ := skillsLayoutOf(kind)
	return layoutProjectRoots(l, cwd, dir)
}

// codexSkillDirs (agents.SkillDirsCWDToGitRoot): codex resolves project skills from the CWD up to the GIT ROOT and no further,
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

// advertisedEntries turns a CLI's advertised commands into "cli" picker entries.
func advertisedEntries(adv []agents.AdvertisedCommand, invoke string) []sessionSkill {
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
			Invoke:      invoke + c.Name + " ",
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
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
	out := readCommandEntriesWithPath(root)
	for i := range out {
		// Path on a picker entry means "foreign, inject this file" — a native command has none.
		out[i].Path, out[i].Source, out[i].Type = "", source, "command"
	}
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
