package sessionx

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/uiprefs"
)

// legacyCreateRefusal is the pair of kind-literal gates checkModel used to carry, with the
// refusal texts copied verbatim.
func legacyCreateRefusal(kind, model string, lcppEnabled bool) (int, string, string) {
	if kind == session.KindLcpp && !lcppEnabled {
		return http.StatusForbidden, "lcpp_disabled",
			"llama.cpp は設定でオフになっています。設定 > エージェント > llama.cpp でオンにしてください。"
	}
	if kind == session.KindLcpp && strings.TrimSpace(model) == "" {
		return http.StatusBadRequest, "bad_model",
			"llama.cpp セッションにはモデルの指定が必須です（CLI 自身の既定は存在しません）"
	}
	return 0, "", ""
}

func writeCreatePrefs(t *testing.T, prefs map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(uiprefs.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(prefs)
	if err := os.WriteFile(uiprefs.Path(), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCreateRefusalMatchesLegacyGates(t *testing.T) {
	isolateAgentState(t)
	kinds := append(RegisteredKinds(), "", "Lcpp", "LCPP", "lcpp ", "unknown")
	for _, enabled := range []bool{true, false} {
		writeCreatePrefs(t, map[string]any{"lcppEnabled": enabled})
		for _, kind := range kinds {
			for _, model := range []string{"", "  ", "\t", "m", " m "} {
				ws, wc, wm := legacyCreateRefusal(kind, model, enabled)
				gs, gc, gm := createRefusal(kind, model)
				if gs != ws || gc != wc || gm != wm {
					t.Errorf("createRefusal(%q, %q) lcppEnabled=%v = %d %q, legacy %d %q", kind, model, enabled, gs, gc, ws, wc)
				}
			}
		}
	}
	// A missing key keeps lcpp launchable (the opt-out default).
	writeCreatePrefs(t, map[string]any{})
	if s, _, _ := createRefusal(session.KindLcpp, "m"); s != 0 {
		t.Errorf("lcpp with no lcppEnabled key refused with %d", s)
	}
}

func TestContributorModeOnMatchesLegacy(t *testing.T) {
	isolateAgentState(t)
	kinds := append(RegisteredKinds(), "", "Muse", "MUSE", "muse ", "unknown")
	for _, on := range []any{true, false, "true", nil} {
		prefs := map[string]any{}
		if on != nil {
			prefs["museContributorMode"] = on
		}
		writeCreatePrefs(t, prefs)
		for _, kind := range kinds {
			want := kind == session.KindMuse && uiprefs.MuseContributorMode()
			if got := contributorModeOn(kind); got != want {
				t.Errorf("contributorModeOn(%q) with pref %v = %v, legacy %v", kind, on, got, want)
			}
		}
	}
}

// createPolicyKinds are the kinds that declare agents.CreatePolicy. Declaring it means listing
// the kind here after checking what the traits gate; a kind that declares nothing has no gate.
var createPolicyKinds = []string{session.KindLcpp, session.KindMuse}

func TestCreatePolicyClassifiesEveryRegisteredKind(t *testing.T) {
	for _, kind := range RegisteredKinds() {
		_, impl := AgentOf(kind).(agents.CreatePolicy)
		if listed := slices.Contains(createPolicyKinds, kind); impl != listed {
			t.Errorf("%s: implements agents.CreatePolicy = %v, createPolicyKinds lists it = %v", kind, impl, listed)
		}
	}
}

func TestCreatePolicyPrefsHaveReaders(t *testing.T) {
	for _, kind := range RegisteredKinds() {
		tr, ok := createTraitsOf(kind)
		if !ok {
			continue
		}
		for _, pref := range []string{tr.EnabledPref, tr.ContributorModePref} {
			if pref != "" && prefReaders[pref] == nil {
				t.Errorf("%s names ui-prefs key %q, which prefReaders cannot read", kind, pref)
			}
		}
		if tr.EnabledPref != "" && (tr.DisabledCode == "" || tr.DisabledMessage == "") {
			t.Errorf("%s: EnabledPref without a refusal code and message", kind)
		}
	}
}

func TestCreateTraitsRefuseAlternateSpellings(t *testing.T) {
	for _, kind := range []string{"Lcpp", "LCPP", "lcpp ", "Muse", ""} {
		if _, ok := createTraitsOf(kind); ok {
			t.Errorf("createTraitsOf(%q) resolved; the lookup must be exact", kind)
		}
	}
}
