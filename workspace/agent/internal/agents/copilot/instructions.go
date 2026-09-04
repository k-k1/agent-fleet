package copilot

// The copilot-side artifact of the user instructions layer (docs/log/60).
//
// copilot reads user-scoped instructions from three places (measured 2026-08-13 on 1.0.79
// with a behavioural canary; it refuses to disclose their content, so "is this word in your
// instructions" is never answered):
//
//	$COPILOT_HOME/copilot-instructions.md              … the user's own file. AF does not own it
//	$COPILOT_HOME/instructions/**/*.instructions.md    … the one AF-owned file goes here
//	COPILOT_CUSTOM_INSTRUCTIONS_DIRS=<dir>             … env. Works, but needs wiring per launch path
//
// A dedicated file in the directory is chosen because the env variant needs the export
// delivered on all three launch paths (the tmux launch, the managed ACP driver, and a
// `copilot` the user types by hand); miss one and that session alone silently loses its
// instructions. A file is read the same way whatever the launch path. Keeping an AF-owned
// file under $COPILOT_HOME is the same shape as rtk (hooks/rtk.json).
//
// The fleet policy goes in the same directory as a separate file (docs/log/60 §60.13 P2).
// copilot had been reading no workspace operating policy at all (measured: it is absent from
// the 15.4k-token system prompt). Two files rather than one because one of them is what the
// user switches (the user instructions) and the other is an operator-owned fixture (the fleet
// policy). The names sort "guide" before "user" (load order is not guaranteed, but the
// precedence is also spelled out in the body of the user instructions, so nothing depends on
// the ordering).

import (
	"os"
	"path/filepath"
)

// UserInstructionsPath is the AF-owned file inside copilot's user instructions dir.
// The name is af's alone, so the whole file can be written and removed as a unit —
// no markers, nothing of the user's to preserve.
func UserInstructionsPath() string {
	return filepath.Join(Home(), "instructions", "agent-fleet-user.instructions.md")
}

// FleetNotesPath is the AF-owned file carrying the baked workspace guide.
func FleetNotesPath() string {
	return filepath.Join(Home(), "instructions", "agent-fleet-guide.instructions.md")
}

// ApplyFleetNotes writes the workspace guide. An empty guide is a no-op (an image
// without one must not silently drop a guide that is already in place).
func ApplyFleetNotes(fleet string) error {
	if fleet == "" {
		return nil
	}
	return writeOwnedFile(FleetNotesPath(), fleet)
}

// ApplyUserInstructions writes (or removes, when body is empty) that file.
func ApplyUserInstructions(body string) error {
	return writeOwnedFile(UserInstructionsPath(), body)
}

// writeOwnedFile writes a file agent-fleet owns outright, removing it when the body
// is empty so nothing stale is left behind. Writes only when the content changes.
func writeOwnedFile(path, body string) error {
	if body == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if cur, err := os.ReadFile(path); err == nil && string(cur) == body {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".af-tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
