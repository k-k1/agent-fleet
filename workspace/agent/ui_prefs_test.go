package main

import (
	"encoding/json"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/mcpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeUIPrefs(t *testing.T, body string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir := filepath.Join(homeDir(), ".config", "agent-fleet")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ui-prefs.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAssistantAgentOrderPref(t *testing.T) {
	tests := []struct {
		name  string
		prefs string
		want  []string
	}{
		{"unset", `{}`, []string{"claude", "codex", "opencode", "cursor", "agy"}},
		{"full order", `{"assistantAgentOrder":["agy","opencode","codex","claude"]}`,
			[]string{"agy", "opencode", "codex", "claude", "cursor"}},
		{"partial order appends the rest in default order",
			`{"assistantAgentOrder":["opencode"]}`,
			[]string{"opencode", "claude", "codex", "cursor", "agy"}},
		{"junk and dupes dropped",
			`{"assistantAgentOrder":["gemini","codex",42,"codex"]}`,
			[]string{"codex", "claude", "opencode", "cursor", "agy"}},
		{"legacy pin promotes to front", `{"assistantAgent":"opencode"}`,
			[]string{"opencode", "claude", "codex", "cursor", "agy"}},
		{"legacy auto falls through to default", `{"assistantAgent":"auto"}`,
			[]string{"claude", "codex", "opencode", "cursor", "agy"}},
		{"order wins over legacy pin",
			`{"assistantAgent":"opencode","assistantAgentOrder":["codex"]}`,
			[]string{"codex", "claude", "opencode", "cursor", "agy"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writeUIPrefs(t, tt.prefs)
			if got := assistantAgentOrderPref(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("order = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestAssistantTokenSavingPrefs pins the token-saving knobs (Settings > Assistant):
// defaults, set values, clamping, and "the pref beats env".
func TestAssistantTokenSavingPrefs(t *testing.T) {
	// Unset → default.
	writeUIPrefs(t, `{}`)
	if got := chatAutoTurnModel(); got != "" {
		t.Fatalf("auto-turn model default = %q", got)
	}
	if uiprefs.ChatQuietCompletion() {
		t.Fatal("quiet completion must default OFF")
	}
	if got := chatx.ChatAutoCompactTokenThreshold(); got != chatx.ChatCtxAutoCompactTokens {
		t.Fatalf("compact tokens default = %d", got)
	}
	if got := chatx.ChatAutoTurnDelay(); got != chatx.ChatAutoTurnDelayDefault {
		t.Fatalf("auto-turn delay default = %v", got)
	}
	if got := mcpx.SessionOutputTail(); got != mcpx.SessionOutputTailBytes {
		t.Fatalf("output tail default = %d", got)
	}

	// The configured values take effect (the model is trimmed).
	writeUIPrefs(t, `{"assistantAutoTurnModel":" haiku ","assistantQuietCompletion":true,
		"assistantAutoCompactTokens":80000,"assistantAutoTurnDelay":120,"assistantOutputTailKiB":64}`)
	if got := chatAutoTurnModel(); got != "haiku" {
		t.Fatalf("auto-turn model = %q", got)
	}
	if !uiprefs.ChatQuietCompletion() {
		t.Fatal("quiet completion should be ON")
	}
	if got := chatx.ChatAutoCompactTokenThreshold(); got != 80000 {
		t.Fatalf("compact tokens = %d", got)
	}
	if got := chatx.ChatAutoTurnDelay(); got != 120*time.Second {
		t.Fatalf("auto-turn delay = %v", got)
	}
	if got := mcpx.SessionOutputTail(); got != 64<<10 {
		t.Fatalf("output tail = %d", got)
	}

	// The pref beats env (which is there for deployments / E2E).
	t.Setenv("AF_CHAT_AUTOCOMPACT_TOKENS", "999999")
	t.Setenv("AF_CHAT_AUTOTURN_DELAY", "1")
	if got := chatx.ChatAutoCompactTokenThreshold(); got != 80000 {
		t.Fatalf("pref should beat env: %d", got)
	}
	if got := chatx.ChatAutoTurnDelay(); got != 120*time.Second {
		t.Fatalf("pref should beat env: %v", got)
	}

	// Clamping: floor on the compaction threshold, cap on the batching delay, floor
	// and cap on the output tail.
	writeUIPrefs(t, `{"assistantAutoCompactTokens":5000,"assistantAutoTurnDelay":100000,"assistantOutputTailKiB":100000}`)
	if got := chatx.ChatAutoCompactTokenThreshold(); got != chatx.ChatCtxAutoCompactTokensMin {
		t.Fatalf("compact tokens floor = %d", got)
	}
	if got := chatx.ChatAutoTurnDelay(); got != chatx.ChatAutoTurnDelayMax {
		t.Fatalf("auto-turn delay cap = %v", got)
	}
	if got := mcpx.SessionOutputTail(); got != 1<<20 {
		t.Fatalf("output tail cap = %d", got)
	}
	writeUIPrefs(t, `{"assistantOutputTailKiB":1}`)
	if got := mcpx.SessionOutputTail(); got != 4<<10 {
		t.Fatalf("output tail floor = %d", got)
	}
}

func TestAssistantModelPrefs(t *testing.T) {
	writeUIPrefs(t, `{
		"assistantModels":{"opencode":"opencode-go/glm-5.2"},
		"aiShortModels":{"opencode":"","claude":"haiku"},
		"aiProseModels":{"claude":"sonnet"}
	}`)
	if got, ok := assistantChatModelPref("opencode"); !ok || got != "opencode-go/glm-5.2" {
		t.Fatalf("chat model = %q, %v", got, ok)
	}
	if got, ok := aiShortModelPref("opencode"); !ok || got != "" {
		t.Fatalf("explicit short default = %q, %v", got, ok)
	}
	// A missing backend reads as "recommended" — what the Console's settings row shows for it
	// (fillRecommendedModelMaps) — and stays distinguishable from the explicit default "" above
	// (#972 review, round 3: it used to be a third state the screen could not name).
	if got, ok := aiShortModelPref("codex"); !ok || got != chatx.AssistantRecommendedModel {
		t.Fatalf("missing backend = %q, %v, want recommended", got, ok)
	}
	if got, ok := aiProseModelPref("claude"); !ok || got != "sonnet" {
		t.Fatalf("prose model = %q, %v", got, ok)
	}
	// Different purpose, different value. That neither setting leaks into the other is
	// the whole point of the split.
	if got, _ := aiShortModelPref("claude"); got != "haiku" {
		t.Fatalf("short model = %q", got)
	}
	// A kind missing from a map that exists does not reach back to the legacy key: the Console
	// copies the legacy map only when the new key is absent altogether.
	writeUIPrefs(t, `{"assistantUtilityModels":{"codex":"gpt-5.4-mini"},"aiShortModels":{"claude":"haiku"}}`)
	if got, _ := aiShortModelPref("codex"); got != chatx.AssistantRecommendedModel {
		t.Fatalf("missing from the new map = %q, want recommended (not the legacy value)", got)
	}
	// Nothing saved at all is a new member's DEFAULTS: "recommended".
	writeUIPrefs(t, `{}`)
	if got, _ := aiProseModelPref("agy"); got != chatx.AssistantRecommendedModel {
		t.Fatalf("no prefs = %q, want recommended", got)
	}
}

// TestAiModelPrefsInheritLegacyForBothTiers: prefs holding only the legacy
// assistantUtilityModels are inherited by BOTH tiers, short and prose. Despite its name
// the legacy key really did drive both, so this is the starting value that carries the
// behaviour at the moment of the split over unchanged. The point of splitting them is
// that they can diverge from here on.
func TestAiModelPrefsInheritLegacyForBothTiers(t *testing.T) {
	writeUIPrefs(t, `{"assistantUtilityModels":{"claude":"haiku"}}`)
	for _, tc := range []struct {
		name string
		get  func(string) (string, bool)
	}{{"short", aiShortModelPref}, {"prose", aiProseModelPref}} {
		if got, ok := tc.get("claude"); !ok || got != "haiku" {
			t.Fatalf("%s must inherit the legacy key: %q, %v", tc.name, got, ok)
		}
	}
	// The tier that has the new key written is no longer dragged along by the legacy one.
	writeUIPrefs(t, `{"assistantUtilityModels":{"claude":"haiku"},"aiProseModels":{"claude":"sonnet"}}`)
	if got, _ := aiProseModelPref("claude"); got != "sonnet" {
		t.Fatalf("explicit prose model must win: %q", got)
	}
	if got, _ := aiShortModelPref("claude"); got != "haiku" {
		t.Fatalf("short must stay on the legacy value: %q", got)
	}
}

// TestAiAssistOrderPref: with no dedicated key, the assist-generation priority inherits
// the chat order, so nothing changes for someone who never notices the split. With a
// dedicated key, that key wins.
func TestAiAssistOrderPref(t *testing.T) {
	writeUIPrefs(t, `{"assistantAgentOrder":["codex","claude"]}`)
	if got := aiAssistOrderPref(); got[0] != "codex" {
		t.Fatalf("inherit chat order: %v", got)
	}
	writeUIPrefs(t, `{"assistantAgentOrder":["codex","claude"],"aiAssistOrder":["opencode"]}`)
	if got := aiAssistOrderPref(); got[0] != "opencode" {
		t.Fatalf("own key wins: %v", got)
	}
	if got := assistantAgentOrderPref(); got[0] != "codex" {
		t.Fatalf("chat order must not follow the assist key: %v", got)
	}
}

// docs/log/103 decision 1/4: a feature can be pinned to one agent, and a model chosen for it
// is stored under that agent's kind — never leaking into another feature or another kind's
// slot (the same kind-scoped shape aiShortModels/aiProseModels already use).
func TestAiFeatureAgentPref(t *testing.T) {
	writeUIPrefs(t, `{"aiFeatureAgents":{"title.session":"codex"}}`)
	if got := aiFeatureAgentPref("title.session"); got != "codex" {
		t.Fatalf("pin = %q, want codex", got)
	}
	if got := aiFeatureAgentPref("branch.suggest"); got != "" {
		t.Fatalf("an unpinned feature must read as \"\" (auto/priority order): %q", got)
	}
}

func TestAiFeatureModelPref(t *testing.T) {
	writeUIPrefs(t, `{"aiFeatureModels":{"title.session":{"codex":"gpt-5.4-mini"}}}`)
	if got, ok := aiFeatureModelPref("title.session", "codex"); !ok || got != "gpt-5.4-mini" {
		t.Fatalf("model = %q, %v", got, ok)
	}
	if _, ok := aiFeatureModelPref("title.session", "claude"); ok {
		t.Fatal("a different kind under the same feature must stay unset — kind-scoped storage " +
			"is what lets switching the pin back and forth recover each CLI's own value")
	}
	if _, ok := aiFeatureModelPref("branch.suggest", "codex"); ok {
		t.Fatal("a different feature must stay unset")
	}
}

// A model excluded by "models not to use" must fall back to unset even if the per-feature
// setting still names it — the same rule assistantModelPref enforces for the chat/tier
// settings (docs/log/103 §103.8-4).
func TestAiFeatureModelPrefHiddenModelFallsBackToUnset(t *testing.T) {
	writeUIPrefs(t, `{"aiFeatureModels":{"title.session":{"claude":"opus"}},"hiddenModels":{"claude":["opus"]}}`)
	if _, ok := aiFeatureModelPref("title.session", "claude"); ok {
		t.Fatal("a hidden model must not be adopted even if it is still in aiFeatureModels")
	}
}

// TestResolveOneShotHonoursHiddenModels is the chain version of the test above
// (103-impl-review 軽13): chatx's own test suite cannot exercise the real aiFeatureModelPref —
// its testDeps() stub is a hand-rolled copy with no hidden-models filter (chatx cannot import
// sessionx, which is where ModelHidden lives, without an import cycle). Routed through package
// main's real wiring (chat_wiring.go's init, chatx.ResolveOneShot), the filter is provable
// end to end instead of resting on the unit test alone.
func TestResolveOneShotHonoursHiddenModels(t *testing.T) {
	writeUIPrefs(t, `{"aiFeatureAgents":{"title.session":"claude"},`+
		`"aiFeatureModels":{"title.session":{"claude":"opus"}},"hiddenModels":{"claude":["opus"]}}`)
	t.Cleanup(chatx.SetHeadlessAvailableForTest("claude", true))
	kind, model, configured, _ := chatx.ResolveOneShot("title.session", chatx.OneShotShort)
	if kind != "claude" {
		t.Fatalf("kind = %q, want claude (the pin itself is not hidden)", kind)
	}
	if configured && model == "opus" {
		t.Fatalf("a hidden model reached resolveOneShot: model=%q configured=%v", model, configured)
	}
}

func TestPutUIPrefsBacksUpAShrinkingWrite(t *testing.T) {
	writeUIPrefs(t, `{"quickReplies":{"ok":{"text":"OK","count":9,"at":1}},"iconSet":"seti"}`)

	// A PUT of a whole set of defaults that has lost the learned data — the shape of the
	// accident. The write itself goes through.
	rec := httptest.NewRecorder()
	handlePutUIPrefs(rec, httptest.NewRequest("PUT", "/env/ui-prefs", strings.NewReader(`{"quickReplies":{},"iconSet":"vscode"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d", rec.Code)
	}
	if got := uiprefs.Read()["iconSet"]; got != "vscode" {
		t.Fatalf("write must not be refused: iconSet = %v", got)
	}
	// The version from just before survives, i.e. there is something to recover from.
	b, err := os.ReadFile(uiprefs.BackupPath())
	if err != nil {
		t.Fatalf("no backup kept: %v", err)
	}
	var prev map[string]any
	if err := json.Unmarshal(b, &prev); err != nil {
		t.Fatal(err)
	}
	learned, _ := prev["quickReplies"].(map[string]any)
	if len(learned) != 1 {
		t.Fatalf("backup lost the learned replies: %v", prev)
	}

	// A write that does not shrink must not overwrite the backup, so a later benign
	// version cannot flush away the one from just before the accident.
	rec = httptest.NewRecorder()
	handlePutUIPrefs(rec, httptest.NewRequest("PUT", "/env/ui-prefs", strings.NewReader(`{"quickReplies":{},"iconSet":"material"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("second PUT = %d", rec.Code)
	}
	b2, err := os.ReadFile(uiprefs.BackupPath())
	if err != nil || string(b2) != string(b) {
		t.Fatalf("backup must survive later benign writes: %v", err)
	}
}
