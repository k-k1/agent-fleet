package sessionx

import (
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/harness"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/kinds"
)

// skillsLayoutOf is kind's agents.SkillsLayout, by exact registry key: an unknown or alternate
// spelling has no skills.
func skillsLayoutOf(kind string) (agents.SkillsLayout, bool) {
	a, _ := RegisteredAgent(kind)
	l, ok := a.(agents.SkillsLayouter)
	if !ok {
		return agents.SkillsLayout{}, false
	}
	return l.SkillsLayout(), true
}

// layoutProjectRoots is a layout's repository roots for a kind running in cwd beneath the
// working copy dir, in the CLI's precedence order. The picker and the Repository "Skills" view
// (#2795) both start from it, so the two cannot drift. A level above the working copy is real to
// the CLI but belongs to the environment, hence source "user".
func layoutProjectRoots(l agents.SkillsLayout, cwd, dir string) []skillRoot {
	var dirs []string
	switch l.ProjectDirs {
	case agents.SkillDirsCWDToHome:
		dirs = claudeSkillDirs(cwd, dir)
	case agents.SkillDirsCWDToGitRoot:
		dirs = codexSkillDirs(cwd)
	default:
		dirs = []string{dir}
	}
	var roots []skillRoot
	for _, d := range dirs {
		src := "project"
		if !under(dir, d) {
			src = "user"
		}
		for _, r := range l.Project {
			roots = append(roots, skillRoot{filepath.Join(d, filepath.FromSlash(r.Rel)), src, r.Form})
		}
	}
	return roots
}

// layoutNativeSkills is what the kind's own CLI can discover and invoke, and the SKILL.md
// conventions that list already covers.
func layoutNativeSkills(l agents.SkillsLayout, dir, cwd, live string) (skills []sessionSkill, nativeConvs []string) {
	skills = []sessionSkill{}
	switch l.Native {
	case agents.SkillsScan:
		roots := layoutProjectRoots(l, cwd, dir)
		for _, u := range l.User {
			roots = append(roots, skillRoot{u.Dir(), u.Source, u.Form})
		}
		skills = scanSkillRoots(roots, l.Invoke)
		if l.Bundled {
			skills = appendBundledSkills(skills, claudeBundledSkills())
		}
		nativeConvs = l.NativeConvs
	case agents.SkillsAdvertised:
		skills = advertisedSkills(l, live, dir)
	case agents.SkillsHosted:
		// The fallback is the reason nativeConvs is set conditionally: with no live host there
		// is no session to ask, and NativeConvs must then go back to being offered as foreign
		// entries (a plain "read this and follow it" prompt) rather than disappearing from the
		// picker because a native list that does not exist claims to own them.
		skills = museSkills(live)
		if len(skills) > 0 {
			nativeConvs = l.NativeConvs
		}
	}
	return skills, nativeConvs
}

// layoutEnumerateSkills is enumerateSkills over the kind's agents.SkillsLayout.
func layoutEnumerateSkills(kind, dir, cwd, live string) []sessionSkill {
	l, ok := skillsLayoutOf(kind)
	if !ok {
		return []sessionSkill{}
	}
	skills, nativeConvs := layoutNativeSkills(l, dir, cwd, live)
	ownNative := len(skills) > 0 // a scanned root, or the live list when there is one
	// A kind fenced to its CWD would be offered a tree above a Subdir CWD, a SKILL.md symlinked
	// out of it or a user root, and then fail to open it. Every other kind reads through its CLI,
	// which has no such fence.
	var readable func(string) bool
	if l.FenceToCWD {
		readable = func(p string) bool { return harness.Readable(cwd, p) }
	}
	skills = appendForeignSkills(skills, chainUp(cwd, dir), cwd, nativeConvs, readable)
	if !l.FenceToCWD {
		skills = appendUserForeignSkillsFrom(sharedSkillRoots(), skills, kind, ownNative)
	}
	return skills
}

// sharedSkillRoots are the member's own skills of the kinds whose layout shares a user root
// holding claude-compatible SKILL.md trees (docs/log/50 §7.6), in registration order.
func sharedSkillRoots() []userSkillRoot {
	var out []userSkillRoot
	for _, k := range kinds.All() {
		if l, ok := skillsLayoutOf(k); ok && l.Shared != nil {
			out = append(out, userSkillRoot{k, l.Shared.Origin, l.Shared.Dir})
		}
	}
	return out
}

// advertisedSkills: the list the CLI advertised (ACP available_commands_update, shared by the
// driver through agents.PublishCommands) is the only complete source — builtin skills, global,
// and the project's commands/skills, all of it. Until it arrives (runtime not started, or just
// after an agent restart) fall back to the layout's project roots.
func advertisedSkills(l agents.SkillsLayout, live, dir string) []sessionSkill {
	if adv := agents.AdvertisedCommands(live); len(adv) > 0 {
		return advertisedEntries(adv, l.Invoke)
	}
	return scanSkillRoots(layoutProjectRoots(l, dir, dir), l.Invoke)
}
