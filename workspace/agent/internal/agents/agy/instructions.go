package agy

// The agy-side artifact of the user instructions (docs/log/60).
//
// Same shape as codex: agy has no setting that points at an extra instruction file, so the
// body is composed into ~/.gemini/AGENTS.md — the same file as the rtk block — between
// markers. That path is the only place agy reads global context from (measured, docs/log/32
// Track A: ~/.gemini/antigravity-cli/AGENTS.md is not read, and a project-root AGENTS.md is
// picked up in interactive mode only). Everything outside the markers is preserved, so
// whatever the user wrote in the same file survives.

import (
	"os"
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/mdblock"
)

// AgentsPath is agy's global instruction file.
func AgentsPath() string { return agentsPath() }

// ApplyFleetNotes composes the baked workspace guide into AGENTS.md as an
// agent-fleet-owned block (docs/log/60 §60.13 P2 — agy read no fleet policy at all until
// now: its AGENTS.md held nothing but the 450 B rtk block). An empty guide is a no-op
// rather than a removal, same as codex's twin.
func ApplyFleetNotes(fleet string) error {
	if fleet == "" {
		return nil
	}
	return editAgents(func(s string) string {
		if !mdblock.Has(s, "fleet") {
			s = mdblock.StripLegacyPrefix(s, fleet)
		}
		return mdblock.Set(s, "fleet", fleet)
	})
}

// ApplyUserInstructions writes (or removes, when body is empty) the user-notes block.
func ApplyUserInstructions(body string) error {
	return editAgents(func(s string) string { return mdblock.Set(s, "user-notes", body) })
}

// editAgents is the single read-modify-write for ~/.gemini/AGENTS.md, shared with
// ApplyRTK so the two writers cannot race each other into a half-written file.
func editAgents(edit func(string) string) error {
	path := agentsPath()
	orig := ""
	if b, err := os.ReadFile(path); err == nil {
		orig = string(b)
	} else if !os.IsNotExist(err) {
		return err
	}
	out := edit(orig)
	if out == orig || out == "" {
		return nil // no change, or nothing to write
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".af-tmp"
	if err := os.WriteFile(tmp, []byte(out), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
