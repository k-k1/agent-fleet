package sessionx

import "testing"

func TestIsCodexUpdateMenu(t *testing.T) {
	// Both fixtures are real codex 0.144.3 pane captures (2026-07-14).
	menu := "  ✨ Update available! 0.144.3 -> 0.999.0\n" +
		"  Release notes: https://github.com/openai/codex/releases/latest\n" +
		"› 1. Update now (runs `npm install -g @openai/codex`)\n" +
		"  2. Skip\n" +
		"  3. Skip until next version\n" +
		"  Press enter to continue\n"
	if !isCodexUpdateMenu(menu) {
		t.Error("update menu not detected")
	}
	// After a skip the banner stays on screen but the menu footer is gone — the
	// state must clear, or the mirror would block the composer forever.
	afterSkip := "╭───────────────────────────────────────────╮\n" +
		"│ ✨ Update available! 0.144.3 -> 0.999.0   │\n" +
		"│ Run npm install -g @openai/codex to update.│\n" +
		"╰───────────────────────────────────────────╯\n" +
		"╭───────────────────────────────────────────╮\n" +
		"│ >_ OpenAI Codex (v0.144.3)                │\n" +
		"╰───────────────────────────────────────────╯\n"
	if isCodexUpdateMenu(afterSkip) {
		t.Error("banner without the menu must not re-trigger the state")
	}
	// 0.159.2 reworded the menu ("Update available · 0.159.2 → 0.999.0" … "enter continue ·
	// esc skip"), and the mirror lost it (#1263).
	current := "  Update available · 0.159.2 → 0.999.0\n" +
		"  Release notes: https://github.com/openai/codex/releases/latest\n\n" +
		"› 1. Update now (runs `npm install -g @openai/codex`)\n" +
		"  2. Skip\n" +
		"  3. Skip until next version\n\n" +
		"  enter continue · esc skip\n"
	if !isCodexUpdateMenu(current) {
		t.Error("0.159.2 update menu not detected")
	}
	if isCodexUpdateMenu("› reply with exactly: pong\n• pong\n") {
		t.Error("ordinary conversation must not match")
	}
}
