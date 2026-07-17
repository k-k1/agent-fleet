package tmuxx

import "testing"

// TestSpinnerActive locks the live-turn detection against real captured footers. Newer
// claude builds rotate "esc to interrupt" out for a Tip and keep "shift+tab to cycle"
// visible mid-turn, so the elapsed-time/token header must be recognised as busy too —
// otherwise a working session false-idles (入力待ち with no 停止 button).
func TestSpinnerActive(t *testing.T) {
	busy := []string{
		"✽ Zigzagging… (17m 38s · ↓ 57.1k tokens · thought for 2s)",
		"✳ Cerebrating… (12s · ↑ 3.4k tokens)",
		"✳ Zigzagging… (10m 52s · ↓ 34.0k tokens)",
		"✳ Cerebrating… (esc to interrupt · 12s)", // classic hint still counts
		"  ⎿  Tip: … without interrupting Claude's current work\n✳ Working… (1m 2s · ↓ 900 tokens)",
	}
	for _, s := range busy {
		if !spinnerActive(s) {
			t.Errorf("spinnerActive(%q) = false, want true", s)
		}
	}

	idle := []string{
		"❯ ",
		"  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents",
		"? for shortcuts",
		"some transcript body that merely mentions 500 tokens in prose",
		"", // empty capture
	}
	for _, s := range idle {
		if spinnerActive(s) {
			t.Errorf("spinnerActive(%q) = true, want false", s)
		}
	}
}
