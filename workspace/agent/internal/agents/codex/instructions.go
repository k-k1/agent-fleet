package codex

// The codex-side artifact of the instruction files (docs/log/60).
//
// codex has no setting that points at an additional instruction file (measured against the
// 0.147.0 key list: project_doc_max_bytes and project_doc_fallback_filenames exist, nothing of
// the instructions_file kind). So both the fleet policy and the user instructions have to be
// composed into the single $CODEX_HOME/AGENTS.md. They are fenced by markers, so text the user
// wrote into the same file survives.
//
// The order inside the file follows reconcile's call order: fleet -> user-notes -> rtk.
//
// Size is not a concern (measured): project_doc_max_bytes (32KiB by default) applies only to
// the TOTAL of the project document chain, and $CODEX_HOME/AGENTS.md is outside that budget
// with no cap of its own (a 42KB global was confirmed to arrive intact via
// `codex debug prompt-input`).

import "github.com/k-k1/agent-fleet/workspace/agent/internal/mdblock"

// ApplyFleetNotes composes the baked workspace guide into AGENTS.md as an
// agent-fleet-owned block. An empty guide (image without one) is a no-op rather than
// a removal — dropping the guide because we could not read it would be worse than
// leaving the previous copy in place.
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
