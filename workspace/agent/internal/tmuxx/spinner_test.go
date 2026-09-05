package tmuxx

import "testing"

// TestSpinnerActive locks the live-turn detection against real captured spinners. The
// only dependable parts of that line are the gerund + "…" and the parenthesised elapsed
// timer: "esc to interrupt" rotates out for a Tip, and the "· ↓ <n> tokens" segment shows
// only once output tokens have accrued. Keying on tokens (as we did) false-idles every
// turn that is still thinking — 入力待ち with no stop button while claude is plainly working.
func TestSpinnerActive(t *testing.T) {
	busy := []string{
		"✽ Zigzagging… (17m 38s · ↓ 57.1k tokens · thought for 2s)",
		"✳ Cerebrating… (12s · ↑ 3.4k tokens)",
		"✳ Zigzagging… (10m 52s · ↓ 34.0k tokens)",
		"✳ Cerebrating… (esc to interrupt · 12s)", // classic hint still counts
		"  ⎿  Tip: … without interrupting Claude's current work\n✳ Working… (1m 2s · ↓ 900 tokens)",
		// Live 2.1.212 captures (claude_sqicn4e / claude_spz7az5 panes).
		"✽ Perusing… (5m 42s · ↓ 17.8k tokens · thought for 3s)",
		"✻ Meandering… (2m 3s · ↓ 8.1k tokens · thinking with high effort)",
		// The regression: a turn still thinking has NO token segment. Real 2.1.212
		// captures — the whole 14s turn these came from read idle before this fix.
		"✢ Tempering… (6s · thinking with high effort)",
		"✢ Tempering… (2s · thinking with high effort)",
		"✻ Wibbling… (1s · ↓ 4 tokens)",
		"· Manifesting… (2s · ↓ 132 tokens · thinking with high effort)", // glyph dims to "·"
		"✻ Topsy-turvying… (3s)",                                         // hyphenated verb, no extras at all
		"✻ Fluttering… (12s · still thinking with high effort)",          // "still" thinking
		// The timer is not always the first segment inside the parens: while a hook runs,
		// the phase leads. Captured live by the contract probe (tui_contract_test.go) — the
		// regex missed this, so a turn read idle for the whole stop-hook window.
		"· Tomfoolering… (running stop hook · 6s · ↓ 279 tokens)",
		// A todo in progress replaces the whimsical single-word gerund with that item's
		// multi-word activeForm — real capture (claude_srtaoqr). The old [^\s(]* stopped at
		// the first space and read this idle: 入力待ち with no stop button while plainly working.
		"✢ Adding regression tests… (13m 31s · ↓ 48.5k tokens)",
		"✳ Verifying with real Chromium… (2m 3s)",
		// A todo whose activeForm is Japanese heads the phrase with a CJK char, not an
		// ASCII capital — real capture (claude_sdfruv7). The [A-Z]-only head read this idle:
		// 進行中 session badged 入力待ち with no stop button while plainly working.
		"· 検証ハーネスを作成中… (12m 2s · ↓ 36.4k tokens)",
		"✳ テストを実行中… (2m 3s · thinking with high effort)",
		// An activeForm is arbitrary user text, so it can contain "(" — and the glyph is
		// not always non-ASCII. Real capture (claude_sx5m7yp, screenshotted mid-turn while
		// the Console badged it 入力待ち): the "(" ended the phrase run early AND the ASCII
		// "*" glyph left the [A-Z]|non-ASCII head with nothing to match, so every frame of
		// a 14-minute turn read idle.
		"* nativeRuntime アダプタ実装 (AF_RUNTIME=native)… (9m 26s · ↓ 35.7k tokens)",
		"✽ nativeRuntime アダプタ実装 (AF_RUNTIME=native)… (9m 26s · ↓ 35.7k tokens)",
		"* nativeRuntime アダプタ実装… (9m 26s · ↓ 35.7k tokens)",
		"✳ Rebuild (native) の検証… (3s)",
		// A slash command running as a todo renders its own name as the activeForm — '/'
		// is punctuation, so \p{L}\p{N} alone missed it. Real capture (claude_sqchdhn): a
		// 20-minute /copyedit turn read idle for its whole duration.
		"· /copyedit 02-noir A6… (20m 1s · almost done thinking with high effort)",
		// A narrow pane drops the elapsed timer, not the status phrase: claude fits the
		// phrase first and adds the timer only if the remaining width allows. At the 60
		// columns the Console gives a phone session, "almost done thinking with high
		// effort" alone uses the budget up. Real captures (claude_s36uuiv, 60x46) — the
		// first read idle for the whole "almost done" window of a 14-minute turn, while
		// the second, same turn, same pane, read busy once the phrase shortened.
		"✳ Calculating… (almost done thinking with high effort)",
		"✶ Calculating… (14m 25s · thinking with high effort)",
		"✻ Zigzagging… (thinking some more with high effort)",
		"✽ Perusing… (still thinking)",
		"· 検証ハーネスを作成中… (almost done thinking with high effort)",
		"· /copyedit 02-noir A6… (almost done thinking with high effort)",
		// The tokens can outlive the timer too (they are laid out after it, but each
		// segment is tested against the width on its own).
		"✳ Calculating… (↓ 45.2k tokens · almost done thinking with high effort)",
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
		// The post-turn summary claude leaves in place of the spinner: a past-tense verb,
		// no "…", no parenthesised timer. It reports elapsed time but never a token count,
		// so however long the turn ran it must not read busy — real 2.1.212 captures.
		"✻ Worked for 13m 53s",
		"✻ Churned for 25s",
		"✻ Sautéed for 5s · 1 shell still running",
		// An indented transcript line quoting a spinner is not a spinner. Keeps a session
		// asked to debug the TUI (this very task) from reading its own pane as busy.
		"  ✢ Tempering… (6s · thinking with high effort)",
		"  the footer showed ✽ Perusing… (5m 42s · ↓ 17.8k tokens) at that point",
		// Same, with a CJK activeForm: a ≥2-space-indented quote of a Japanese spinner is
		// still a quote, not a live turn — the widened non-ASCII head must not weaken this.
		"  · 検証ハーネスを作成中… (12m 2s · ↓ 36.4k tokens)",
		// Truncated prose ends in an ellipsis too — it just isn't followed by a timer.
		"tmux focus-events off · add 'set -g focus-events on' to ~/.tmux.conf and rea…",
		// Same guard as the indented quotes above, for the parenthesised activeForm: the
		// run before "…" now admits "(", so the ≥2-space indent is all that separates a
		// quote from the real thing.
		"  * nativeRuntime アダプタ実装 (AF_RUNTIME=native)… (9m 26s · ↓ 35.7k tokens)",
		// A source line quoting a spinner in a "//" comment sits at column 0, so only the
		// head class keeps it out — this file and tmuxx.go both contain such lines, and a
		// session debugging the TUI reads its own pane. Tabs and spaces after the "//".
		"//\t✢ Tempering… (6s · thinking with high effort)",
		"// e.g. ✽ Perusing… (5m 42s · ↓ 17.8k tokens)",
		"#  ✳ Working… (1m 2s · ↓ 900 tokens)",
		// The slash-command alternative must not reopen the "//" comment hole it was added
		// to close: a comment whose text happens to start with '/' still has no space
		// between the two slashes, so it must keep reading idle.
		"// /copyedit 02-noir A6… (20m 1s · almost done thinking with high effort)",
		// ...and the ≥2-space-indented-quote guard must still hold for the new alternative.
		"  · /copyedit 02-noir A6… (20m 1s · almost done thinking with high effort)",
		// The timer-less (narrow-pane) form must keep every guard the timer-bearing one
		// has: an indented or commented quote of it is still a quote.
		"  ✳ Calculating… (almost done thinking with high effort)",
		"//\t✳ Calculating… (almost done thinking with high effort)",
		"// /copyedit 02-noir A6… (almost done thinking with high effort)",
		// It also carries one guard of its own: with no timer to vouch for the line, only
		// the closing paren at end-of-line separates a spinner from col-0 prose that
		// happens to run "…" into a parenthetical. A stuck false busy never clears — the
		// transcript line stays in the pane — so this is the more expensive direction.
		"● Read the plan… (thinking it over) and then rewrote the section",
		"❯ 直した… (thinking の訳語をどうするか) を決めてから続けます",
	}
	for _, s := range idle {
		if spinnerActive(s) {
			t.Errorf("spinnerActive(%q) = true, want false", s)
		}
	}
}

// TestAtPromptFooter locks the ready-prompt detection against the real footer strips of
// claude 2.1.212. The trailing hint is contextual — absent in the default (manual) mode,
// and replaced by other segments while background shells run — so the mode indicator
// itself has to carry the signal. Regressing to a hint-only check false-negatives every
// default-mode session, which strands it badged 実行中 (the stale-status→idle self-heal
// can never fire).
func TestAtPromptFooter(t *testing.T) {
	atPrompt := []string{
		// Every permission mode, captured live from a 2.1.212 pane.
		"  ⏸ manual mode on · ← for agents",                              // default: no hint at all
		"  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents",          //
		"  ⏵⏵ accept edits on (shift+tab to cycle) · ← for agents",       //
		"  ⏸ plan mode on (shift+tab to cycle) · ← for agents",           //
		"  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents", //
		"  ⏵⏵ bypass permissions on · 1 shell · ← for agents",            // hint yields to background work
		// Older builds, still supported (the image pins a CLI, which may lag).
		"? for shortcuts",
		"  ⏵⏵ bypass permissions on (shift+tab to cycle)",
	}
	for _, s := range atPrompt {
		if !atPromptFooter(s) {
			t.Errorf("atPromptFooter(%q) = false, want true", s)
		}
	}

	notAtPrompt := []string{
		"", // empty capture
		// A modal draws over the input box: the footer strip is gone entirely. Real capture
		// of the 2.1.212 plan-approval dialog (trimmed).
		"   Claude has written up a plan and is ready to execute. Would you like to\n   proceed?\n\n   ❯ 1. Yes, and use auto mode\n     2. Yes, manually approve edits\n\n   ctrl+g to edit in Vim ·",
		// Prose that merely names a mode must not match — the strip is line-anchored.
		"I switched it to plan mode on the second run",
		"the session had auto mode on earlier",
	}
	for _, s := range notAtPrompt {
		if atPromptFooter(s) {
			t.Errorf("atPromptFooter(%q) = true, want false", s)
		}
	}
}
