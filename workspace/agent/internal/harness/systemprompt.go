package harness

// systemprompt.go is segment G's system-prompt half (ADR 0093 decision 5 / docs/log/99 §4.9):
// a CLI-driven kind gets the instruction-file layer for free because the vendor CLI reads
// AGENTS.md/CLAUDE.md and its own config itself. lcpp drives no CLI, so this package reads
// them and folds them into one system message, in the SAME fleet -> user -> project order
// agent_instructions.go's own composition already applies (see that file's ":21" comment,
// "Order matters: the order within the file IS the order it is applied in") — reused here via
// internal/userinstr rather than re-derived, so the order is written down in exactly one place.
//
// rtk plays no part in this file. Decision 5's own last sentence limits rtk, for this kind, to
// the bash-exec-time rewrite tools_bash.go's caller already applies (agent_rtk.go's shape for
// every other kind) — there is no rtk-owned FILE for lcpp to fold into a prompt, so "fleet ->
// user -> project -> rtk" (the general order other kinds compose a single file in) has nothing
// for this function to add at the rtk position.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/userinstr"
)

// SystemPrompt composes decision 5's system prompt for one turn: the baked fleet policy, the
// workspace owner's own instructions, the working copy's own AGENTS.md/CLAUDE.md chain (cwd
// upward to the nearest git root), and a listing of the foreign SKILL.md trees this kind has no
// native way to invoke (decision 5: "スキルは foreign のみ"). Sections that have nothing to say
// are omitted rather than emitted empty.
//
// kind is the userinstr per-target name (userinstr.State.Body's map key / its Targets map).
// "lcpp" is not one of userinstr's known kinds — it writes no file, so agent_instructions.go
// never lists it as a distribution target (decision 5) — but that needs no special case here:
// State.TargetOn defaults an unrecognised kind to on, exactly like a kind nobody has ever
// bothered to turn off.
func SystemPrompt(cwd, kind string) string {
	var parts []string
	if fleet := strings.TrimSpace(userinstr.FleetNotes()); fleet != "" {
		parts = append(parts, fleet)
	}
	if user := strings.TrimSpace(userinstr.Load().Body(kind)); user != "" {
		parts = append(parts, user)
	}
	if proj := projectInstructions(cwd); proj != "" {
		parts = append(parts, proj)
	}
	if skills := foreignSkillsPrompt(cwd); skills != "" {
		parts = append(parts, skills)
	}
	return strings.Join(parts, "\n\n")
}

// projectInstructionFiles is the per-directory filenames decision 5's project layer reads, in
// the order they compose WITHIN one directory: AGENTS.md before CLAUDE.md, the same order
// opencode's own project file chain uses (docs/log/60, the `global`/`project` arrays around
// line 110: `["AGENTS.md", ["CLAUDE.md"], "CONTEXT.md", …]`).
var projectInstructionFiles = []string{"AGENTS.md", "CLAUDE.md"}

// projectInstructions reads a working copy's own instructions, walking from cwd UP to the
// nearest git root and no further — the same boundary codex's own project-skill scope uses
// (internal/sessionx's codexSkillDirs/gitRoot, session_skills.go:306-324: "codex resolves
// project skills from the CWD up to the GIT ROOT and no further … with no .git above the CWD
// it reads the CWD alone"). lcpp drives no CLI of its own to crib a boundary from, and
// AGENTS.md is itself the codex/opencode convention, so this reuses codex's own choice rather
// than inventing a third one (claude's own chain instead stops at $HOME, which would pull in
// directories that have nothing to do with this repository).
//
// Directories are composed root-first, cwd-nearest last: the file closest to the actual work
// is the most specific one, and reading it last gives it the same "read after, so it reads as
// the more specific layer" position fleet -> user -> project already has for the outer three
// layers.
func projectInstructions(cwd string) string {
	if cwd == "" {
		return ""
	}
	dirs := chainUpToGitRoot(cwd)
	var parts []string
	for i := len(dirs) - 1; i >= 0; i-- {
		for _, name := range projectInstructionFiles {
			b, err := os.ReadFile(filepath.Join(dirs[i], name))
			if err != nil {
				continue
			}
			text := strings.TrimSpace(string(b))
			if text == "" {
				continue
			}
			parts = append(parts, "# "+name+" ("+dirs[i]+")\n\n"+text)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "# Project instructions\n\n" + strings.Join(parts, "\n\n")
}

// chainUpToGitRoot walks from start up to (and including) the nearest ancestor holding a .git
// entry, deepest first. With no .git anywhere above start, it returns start alone — matching
// codexSkillDirs's own rule that a directory outside any repo reads only itself, rather than
// wandering up to $HOME or /.
func chainUpToGitRoot(start string) []string {
	start = filepath.Clean(start)
	root, ok := gitRootAbove(start)
	if !ok {
		return []string{start}
	}
	out := []string{}
	for d := start; ; {
		out = append(out, d)
		if d == root {
			return out
		}
		parent := filepath.Dir(d)
		if parent == d {
			return out
		}
		d = parent
	}
}

// gitRootAbove finds the nearest ancestor of p (p included) holding a .git entry — a directory
// in a plain clone, a file in a worktree, so one Stat answers both (the same test
// session_skills.go's own gitRoot uses).
func gitRootAbove(p string) (string, bool) {
	for d := p; ; {
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

// foreignSkillConvs are the SKILL.md tree conventions lcpp has no native way to discover (it
// drives no CLI at all), advertised instead the same "read Path and follow its instructions"
// way the Console already offers other kinds' foreign entries (docs/log/50 §8). The three
// names are duplicated from internal/sessionx's own foreignConvs (session_skills.go:127-129)
// rather than imported: this ADR's own layering has P2's kind wiring import internal/harness
// (the managed driver wraps this package), not the other way round, and importing sessionx
// from here for a 3-string slice is not worth risking that cycle later.
var foreignSkillConvs = []string{".claude/skills", ".codex/skills", ".agents/skills"}

// foreignSkill is one entry foreignSkillsPrompt advertises: enough for a model to decide
// whether it is relevant and then read it itself.
type foreignSkill struct {
	Name        string
	Description string
	Path        string
}

// foreignSkills scans every foreignSkillConvs tree from cwd up to the git root (the same
// boundary projectInstructions uses) for a SKILL.md, skipping any marked `user-invocable:
// false` in its frontmatter — the same flag session_skills.go's appendForeignSkills honours.
func foreignSkills(cwd string) []foreignSkill {
	if cwd == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []foreignSkill
	for _, dir := range chainUpToGitRoot(cwd) {
		for _, conv := range foreignSkillConvs {
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
				fm := skillFrontmatter(string(b))
				if isDisabledSkill(fm["user-invocable"]) {
					continue
				}
				name := fm["name"]
				if name == "" {
					name = e.Name()
				}
				if name == "" || seen[name] {
					continue
				}
				seen[name] = true
				path := conv + "/" + e.Name() + "/SKILL.md"
				if filepath.Clean(dir) != filepath.Clean(cwd) {
					// A tree above cwd needs an absolute path: relative-to-cwd would resolve
					// against the wrong directory once the read tool actually opens it.
					path = filepath.Join(root, e.Name(), "SKILL.md")
				}
				out = append(out, foreignSkill{Name: name, Description: fm["description"], Path: path})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// foreignSkillsPrompt renders foreignSkills as the system-prompt section that tells the model
// how to use one: "" when there is nothing to advertise, so SystemPrompt can omit the section
// entirely instead of emitting an empty heading.
func foreignSkillsPrompt(cwd string) string {
	skills := foreignSkills(cwd)
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Skills available\n\n")
	b.WriteString("This session has no built-in skill picker. To use one of the skills below, " +
		"read its file with the read tool and follow the instructions it contains before " +
		"proceeding with the task it covers.\n\n")
	for _, s := range skills {
		if s.Description != "" {
			b.WriteString("- " + s.Name + ": " + s.Description + " (" + s.Path + ")\n")
		} else {
			b.WriteString("- " + s.Name + " (" + s.Path + ")\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// skillFrontmatter reads a SKILL.md's `---`-fenced frontmatter into a lowercased-key map — the
// same shape package main's own splitFrontmatter (repo_prompts.go) produces, reimplemented
// here rather than imported: that function lives in package main, which internal/harness
// cannot import (main imports this package, not the other way round).
func skillFrontmatter(s string) map[string]string {
	meta := map[string]string{}
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return meta
	}
	rest := s[strings.IndexByte(s, '\n')+1:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return meta
	}
	fm := rest[:end]
	for _, ln := range strings.Split(fm, "\n") {
		ln = strings.TrimRight(ln, "\r")
		if i := strings.IndexByte(ln, ':'); i > 0 {
			k := strings.ToLower(strings.TrimSpace(ln[:i]))
			v := strings.Trim(strings.TrimSpace(ln[i+1:]), `"'`)
			meta[k] = v
		}
	}
	return meta
}

func isDisabledSkill(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "false", "no", "off", "0":
		return true
	}
	return false
}
