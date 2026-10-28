package sessionx

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/muse"
)

// The skills switches move onto agents.SkillsLayout (#2870). Until they go, the layout path must
// produce the same JSON as the switches for every kind, over a tree that has a skill in every
// convention at every level the kinds look at.
func TestSkillsLayoutEqualsTheKindSwitches(t *testing.T) {
	home := withTempHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude-cfg"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	repos := filepath.Join(home, "repos")
	dir := filepath.Join(repos, "r")
	sub := filepath.Join(dir, "pkg", "x")
	writeFile(t, filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/main\n")
	skill := func(base, conv, name string) {
		writeFile(t, filepath.Join(base, filepath.FromSlash(conv), name, "SKILL.md"), "---\nname: "+name+"\ndescription: d-"+name+"\n---\nbody")
	}
	cmd := func(base, conv, name string) {
		writeFile(t, filepath.Join(base, filepath.FromSlash(conv), name+".md"), "---\ndescription: c-"+name+"\n---\nbody")
	}
	for lvl, base := range map[string]string{"up": repos, "root": dir, "sub": sub} {
		for _, conv := range []string{".claude/skills", ".codex/skills", ".agents/skills", ".cursor/skills"} {
			skill(base, conv, lvl+"-"+filepath.Base(filepath.Dir(conv)))
		}
		for _, conv := range []string{".claude/commands", ".opencode/command", ".opencode/commands", ".cursor/commands"} {
			cmd(base, conv, lvl+"-cmd-"+filepath.Base(filepath.Dir(conv)))
		}
	}
	skill(filepath.Join(home, ".claude-cfg"), "skills", "user-claude")
	cmd(filepath.Join(home, ".claude-cfg"), "commands", "user-claude-cmd")
	skill(filepath.Join(home, ".codex"), "skills", "user-codex")
	skill(filepath.Join(home, ".codex", "skills"), ".system", "sys-codex")
	cmd(filepath.Join(home, ".config", "opencode"), "command", "user-oc")
	cmd(filepath.Join(home, ".config", "opencode"), "commands", "user-ocs")
	skill(filepath.Join(home, ".config", "muse"), "skills", "user-muse")

	origB, origM := claudeBundledSkills, museNativeSkills
	t.Cleanup(func() { claudeBundledSkills, museNativeSkills = origB, origM })
	claudeBundledSkills = func() []string { return []string{"simplify", "user-claude"} }
	museNativeSkills = func(live string) []muse.Skill {
		if live == "" {
			return nil
		}
		return []muse.Skill{{Selector: "hosted", Description: "h", Source: "bundled"}}
	}
	agents.PublishCommands("live-1", []agents.AdvertisedCommand{{Name: "adv", Description: "a"}})
	t.Cleanup(func() { agents.PublishCommands("live-1", nil) })

	nonEmpty := map[string]bool{}
	for _, k := range append(RegisteredKinds(), "", "Claude", "pi") {
		for _, cwd := range []string{dir, sub} {
			for _, live := range []string{"", "live-1"} {
				want, _ := json.Marshal(enumerateSkills(k, dir, cwd, live))
				got, _ := json.Marshal(layoutEnumerateSkills(k, dir, cwd, live))
				if string(got) != string(want) {
					t.Errorf("%q cwd=%s live=%q:\n layout %s\n switch %s", k, cwd, live, got, want)
				}
				if len(want) > 4 {
					nonEmpty[k] = true
				}
			}
			l, _ := skillsLayoutOf(k)
			if got, want := layoutProjectRoots(l, cwd, dir), projectSkillRoots(k, cwd, dir); !reflect.DeepEqual(got, want) {
				t.Errorf("%q cwd=%s: layout roots %v, switch %v", k, cwd, got, want)
			}
		}
	}
	for _, k := range []string{"claude", "codex", "opencode", "cursor", "muse", "kiro", "copilot", "agy", "lcpp"} {
		if !nonEmpty[k] {
			t.Errorf("%s answered no skills on any probe; the comparison does not exercise it", k)
		}
	}
	if got, want := sharedSkillRoots(), userSkillRoots; len(got) != len(want) {
		t.Fatalf("shared roots %d, userSkillRoots %d", len(got), len(want))
	} else {
		for i := range got {
			if got[i].kind != want[i].kind || got[i].origin != want[i].origin || got[i].dir() != want[i].dir() {
				t.Errorf("shared root %d = %s %s %s, userSkillRoots %s %s %s", i, got[i].kind, got[i].origin, got[i].dir(), want[i].kind, want[i].origin, want[i].dir())
			}
		}
	}
}
