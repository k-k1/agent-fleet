package sessionx

// claude's mode display (the chip label) and the readiness gate behind it.
//
// The strings were captured from claude 2.1.241 in a real tmux, one launch per
// --permission-mode with capture-pane on the footer. manual alone prints no
// "(shift+tab to cycle)", and manual is exactly where a launch with permission prompts
// (docs/log/76) lands, so an empty answer here delays first-prompt delivery by 30 seconds.

import "testing"

func TestClaudeModeLabel(t *testing.T) {
	cases := []struct{ name, tail, want string }{
		// Footers as measured, kept verbatim down to the trailing "· ← for agents".
		{"manual (the default, no --permission-mode)", "  ⏸ manual mode on · ← for agents", "Manual"},
		{"auto", "  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents", "Auto"},
		{"acceptEdits", "  ⏵⏵ accept edits on (shift+tab to cycle) · ← for agents", "Accept Edits"},
		{"bypassPermissions", "  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents", "Bypass"},
		{"dontAsk", "  ⏵⏵ don't ask on (shift+tab to cycle) · ← for agents", "Don't ask"},
		{"plan", "  ⏸ plan mode on (shift+tab to cycle) · ← for agents", "Plan"},
		// With background work the hint is replaced by another segment (same shape as tmuxx's
		// measurement notes).
		{"background work hides the hint", "  ⏵⏵ bypass permissions on · 1 shell · ← for agents", "Bypass"},
		// Future names, added or renamed: as long as the footer band is visible, do not fall back
		// to "not yet drawn".
		{"unknown mode name still counts as drawn when the footer band is there", "  ⏵⏵ something new on (shift+tab to cycle)", "Default"},
		{"only an older build's catchphrase", "  ? for shortcuts · shift+tab to cycle", "Default"},
		// Booting, or a modal on top: genuinely not drawn yet.
		{"boot screen", "Loading…", ""},
		{"body text that is not the mode band", "私は計画について説明します", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := claudeModeLabel(tc.tail); got != tc.want {
				t.Fatalf("claudeModeLabel(%q) = %q, want %q", tc.tail, got, tc.want)
			}
		})
	}
}
