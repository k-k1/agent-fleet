package chatx

// resolveOneShot's contract (docs/log/103 §103.5): a feature-level pin beats the shared
// priority order, but only while the pin is actually reachable — and once it falls through,
// the model half must follow the KIND it fell through to, never the pin's own kind (decision
// 4). §103.10 asks for exactly these two cases (plus the pin-wins case) to be hit directly.

import (
	"os"
	"path/filepath"
	"testing"
)

// forceHeadlessAvailable pins headlessAgentAvailable's 1-minute cache for kind to v, without
// touching real credentials or shelling out to a CLI. Restored via t.Cleanup so a later test in
// this package never observes a stale forced value. A thin wrapper over
// SetHeadlessAvailableForTest — that one is exported (non-`_test.go`) for package main's own
// pin-path tests (session_translate_test.go, 103-impl-review 中7); this package's tests are
// in-package, so t.Cleanup is the natural shape here instead of a manual restore call.
func forceHeadlessAvailable(t *testing.T, kind string, v bool) {
	t.Helper()
	t.Cleanup(SetHeadlessAvailableForTest(kind, v))
}

func writeResolvePrefs(t *testing.T, body string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "agent-fleet")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ui-prefs.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// aiAssistOrderPref is stubbed FIXED in this package's testDeps() (DefaultHeadlessOrder =
// claude, codex, opencode, cursor, agy) rather than reading ui-prefs, so these tests drive the
// order's outcome through availability alone, never through an "aiAssistOrder" key.
func TestOneShotKindPinBeatsPriorityOrder(t *testing.T) {
	writeResolvePrefs(t, `{"aiFeatureAgents":{"title.session":"codex"}}`)
	forceHeadlessAvailable(t, "claude", true) // would win the plain priority order (it is first)
	forceHeadlessAvailable(t, "codex", true)

	kind, source := oneShotKind("title.session")
	if kind != "codex" || source != OneShotSourcePin {
		t.Fatalf("kind=%q source=%q, want codex/pin (claude is available and would win the priority order)", kind, source)
	}
	// The same prefs leave an unpinned feature on the plain order's own winner.
	if kind, source := oneShotKind("branch.suggest"); kind != "claude" || source != OneShotSourceDefault {
		t.Fatalf("unpinned feature: kind=%q source=%q, want claude/default", kind, source)
	}
}

// The pin's own model must not leak onto the kind it fell through to (decision 4): the
// resolved model for a fallback kind comes from THAT kind's normal ② slot, not from whatever
// was configured for the pin.
func TestOneShotKindPinFallsBackWhenUnavailable(t *testing.T) {
	writeResolvePrefs(t, `{
		"aiFeatureAgents":{"title.session":"codex"},
		"aiFeatureModels":{"title.session":{"codex":"gpt-5.4-mini"}},
		"aiShortModels":{"claude":"haiku"}}`)
	forceHeadlessAvailable(t, "codex", false) // the pin is unreachable
	forceHeadlessAvailable(t, "claude", true)

	kind, model, configured, source := resolveOneShot("title.session", OneShotShort)
	if kind != "claude" || source != OneShotSourceDefault {
		t.Fatalf("kind=%q source=%q, want claude/default (fell through the unreachable pin)", kind, source)
	}
	if model != "haiku" || !configured {
		t.Fatalf("model=%q configured=%v, want claude's own ② value (haiku), not the pin's gpt-5.4-mini",
			model, configured)
	}
}

// A caller that forgot to tag a feature (feature == "") must not crash and must behave exactly
// like the pre-103 path: no pin can exist for "", so it is the priority order alone.
func TestResolveOneShotEmptyFeatureUsesDefaultPath(t *testing.T) {
	writeResolvePrefs(t, `{"aiFeatureAgents":{"title.session":"claude"}}`)
	forceHeadlessAvailable(t, "claude", false)
	forceHeadlessAvailable(t, "codex", true)

	kind, _, _, source := resolveOneShot("", OneShotShort)
	if kind != "codex" || source != OneShotSourceDefault {
		t.Fatalf("kind=%q source=%q, want codex/default for an untagged feature", kind, source)
	}
}
