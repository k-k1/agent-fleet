package sessionx

import "testing"

func TestParseCompactProgress(t *testing.T) {
	cases := []struct {
		name    string
		pane    string
		wantPct int
		wantEl  string
	}{
		{
			name:    "percent and elapsed",
			pane:    "✳ Compacting conversation… (2m 3s)\n  ▐███████░░░░░ 74%\n  L Tip: Use /btw ...\n",
			wantPct: 74,
			wantEl:  "2m 3s",
		},
		{
			name:    "seconds-only elapsed, no percent yet",
			pane:    "✳ Compacting conversation… (45s)\n  starting up\n",
			wantPct: -1,
			wantEl:  "45s",
		},
		{
			name:    "percent but no elapsed rendered",
			pane:    "Compacting conversation…\n  ▐██░░░░ 12%\n",
			wantPct: 12,
			wantEl:  "",
		},
		{
			name:    "a stray parenthetical elsewhere is not mistaken for elapsed",
			pane:    "some earlier line (9s) done\n✳ Compacting conversation… (1m 0s)\n 100%\n",
			wantPct: 100,
			wantEl:  "1m 0s",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseCompactProgress(c.pane)
			if got.Pct != c.wantPct {
				t.Errorf("pct = %d, want %d", got.Pct, c.wantPct)
			}
			if got.Elapsed != c.wantEl {
				t.Errorf("elapsed = %q, want %q", got.Elapsed, c.wantEl)
			}
		})
	}
}

func TestClassifyClaudePane(t *testing.T) {
	cases := []struct {
		name      string
		pane      string
		wantState string
	}{
		{
			name:      "real compaction line",
			pane:      "✳ Compacting conversation… (2m 3s)\n  ▐███████░░░░░ 74%\n",
			wantState: "compacting",
		},
		{
			name:      "resume menu",
			pane:      "❯ 1. Resume from summary (recommended)\n  2. Resume full session as-is\n  3. Start fresh\n",
			wantState: "resume",
		},
		{
			// Regression: an agent editing an i18n catalog whose value happens to be
			// "Compacting…" must NOT be mistaken for auto-compaction (the bare word
			// "Compacting" used to match; the full CLI phrase does not).
			name:      "i18n catalog value is not compaction",
			pane:      "845 +  \"state.compacting\": \"Compacting…\",\n846 +  \"state.working\": \"Working…\",\n",
			wantState: "",
		},
		{
			// Regression: MirrorView.tsx quotes the menu's option 2 verbatim ("2. Resume
			// full session as-is"). An agent editing that file (e.g. i18n work) must NOT
			// be flagged as parked at the resume menu — the bare "Resume full session"
			// substring used to trip it; the numbered option-1 anchor does not.
			name:      "console source quoting option 2 is not the menu",
			pane:      "            「2. Resume full session as-is」を選んでください。\n",
			wantState: "",
		},
		{
			// Regression: this detector's own doc comment lists both phrases inline
			// ("Resume from summary / Resume full session") with no numeric prefix.
			name:      "prose listing both phrases is not the menu",
			pane:      "//  \"resume\" — parked at \"Resume from summary / Resume full session / ...\"\n",
			wantState: "",
		},
		{
			name:      "ordinary working pane",
			pane:      "Now update the two launchHint consumers. Let me look at LaunchModal.\n",
			wantState: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := classifyClaudePane(c.pane)
			if got != c.wantState {
				t.Errorf("state = %q, want %q", got, c.wantState)
			}
		})
	}
}

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
	if isCodexUpdateMenu("› reply with exactly: pong\n• pong\n") {
		t.Error("ordinary conversation must not match")
	}
}
