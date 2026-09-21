package muse

// The muse-side artifact of the instruction layers (ADR 0095 decision 12).
//
// 🔴 Muse has ONE user-scope rules file and both of AF's apply paths have to share it.
// Every other kind gets either a directory of steering files (kiro: `agent-fleet-guide.md`
// and `agent-fleet-user.md`) or two separate artefacts; here the fleet policy and the
// member's own instructions land in the same `~/.config/muse/AGENTS.md`. Decision 12 left
// that as an explicit Phase 2 choice — own the file outright with delimited sections, or
// merge by markers — and this is the answer:
//
// **Markers, because the file is not AF's.** `~/.config/muse/AGENTS.md` is where a member
// writes their own personal rules for Muse Code, with or without Agent Fleet; owning it
// outright would delete that text on the next reconcile. It is the same reasoning decision 6
// applies to `settings.json`, and the repository has already paid for the other answer once
// (docs/log/60 damage 1, where AF `cp -f`'d a CLI's file away on every start).
//
// The mechanism is codex's, unchanged, because the situation is codex's: one `AGENTS.md`
// carrying several AF-owned blocks in reconcile's call order (fleet → user), with everything
// outside the markers preserved. `mdblock` exists so that the spelling of those markers and
// the way a block is removed cannot drift between kinds.
//
// Where the file is was measured rather than assumed (gate B1-5): markers planted in candidate
// locations came back in the model's own answer from `$XDG_CONFIG_HOME/muse/AGENTS.md`, with a
// syscall trace as the second witness. Nothing under muse's data home is read. The skills half
// needs no code at all — a hand-dropped `~/.config/muse/skills/<name>/SKILL.md` is listed by
// `muse skills list --source user` with no install step and no lock-file entry, which is
// exactly what `fleetskills.Apply` produces.

import (
	"os"
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mdblock"
)

// AgentsPath is muse's user-scope rules file.
//
// ⚠️ `~/.config/muse/CLAUDE.md` is probed too, and in the project layer the measured
// precedence is "AGENTS.md wins, CLAUDE.md is ignored this session". AF writes only AGENTS.md:
// writing both would mean writing a file whose own content muse announces it is discarding.
func AgentsPath() string { return filepath.Join(ConfigHome(), "AGENTS.md") }

// SkillsDir is the user-scope skills root the fleet topic files are dropped into.
func SkillsDir() string { return filepath.Join(ConfigHome(), "skills") }

// ApplyFleetNotes composes the baked workspace guide into AGENTS.md as an agent-fleet-owned
// block. An empty guide (an image without one) is a no-op rather than a removal: dropping the
// policy because we could not read it would be worse than leaving the previous copy.
func ApplyFleetNotes(fleet string) error {
	if fleet == "" {
		return nil
	}
	return editAgents(func(s string) string { return mdblock.Set(s, "fleet", fleet) })
}

// ApplyUserInstructions writes (or removes, when the body is empty) the user-notes block.
func ApplyUserInstructions(body string) error {
	return editAgents(func(s string) string { return mdblock.Set(s, "user-notes", body) })
}

// editAgents applies an edit to AGENTS.md and writes it back atomically. An edit that changes
// nothing writes nothing — this runs on every Console save and on every Agent start, and a
// rewrite with identical bytes would still move the mtime muse's own caching may look at.
func editAgents(edit func(string) string) error {
	path := AgentsPath()
	orig := ""
	if b, err := os.ReadFile(path); err == nil {
		orig = string(b)
	} else if !os.IsNotExist(err) {
		return err
	}
	out := edit(orig)
	if out == orig || out == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".af-tmp"
	if err := os.WriteFile(tmp, []byte(out), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
