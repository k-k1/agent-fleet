package kiro

// The kiro-side artifact of the user instructions layer (docs/log/60).
//
// kiro's persistent context is steering: a directory of markdown. Besides the workspace-side
// `.kiro/steering/` (inside the repository, i.e. the project layer) there is a global
// `~/.kiro/steering/*.md`, and that one is the user layer.
//
// Measured (2026-08-13, kiro 2.16.0, behavioural canary): `kiro-cli chat --no-interactive`
// answered as instructed by a md file placed in `~/.kiro/steering/` (confirmed from a
// directory with no project-side .kiro). No front-matter is needed; plain markdown is read.
//
// AF keeps files under names of its own (the same shape as copilot) — one for the user
// instructions and one for the fleet policy. Any other steering in the directory belongs to
// the user or the team, so AF neither enumerates nor deletes it. The names sort "guide" before
// "user", but load order is not guaranteed, so the precedence is also spelled out in the body
// (docs/log/60 §60.5-4).

import (
	"os"
	"path/filepath"
)

// UserInstructionsPath is the AF-owned file inside kiro's global steering directory.
func UserInstructionsPath() string {
	return filepath.Join(Home(), "steering", "agent-fleet-user.md")
}

// FleetNotesPath is the AF-owned steering file carrying the baked workspace guide
// (docs/log/60 §60.13 P2 — kiro read no fleet policy at all until now).
func FleetNotesPath() string {
	return filepath.Join(Home(), "steering", "agent-fleet-guide.md")
}

// ApplyFleetNotes writes the workspace guide. An empty guide is a no-op.
func ApplyFleetNotes(fleet string) error {
	if fleet == "" {
		return nil
	}
	return writeOwnedFile(FleetNotesPath(), fleet)
}

// ApplyUserInstructions writes (or removes, when body is empty) the user file.
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
