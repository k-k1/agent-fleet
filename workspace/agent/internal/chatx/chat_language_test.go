package chatx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeUIPrefsLang points HOME at a temp dir and writes a ui-prefs.json carrying the
// given outputLanguage value (empty string writes no key), so chatOutputLanguage()
// reads it back. Returns nothing; the env override lasts for the test.
func writeUIPrefsLang(t *testing.T, lang string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "agent-fleet")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "{}"
	if lang != "" {
		body = `{"outputLanguage":"` + lang + `"}`
	}
	if err := os.WriteFile(filepath.Join(dir, "ui-prefs.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPersonaOfLanguageRule(t *testing.T) {
	cases := []struct {
		name        string
		lang        string
		assistantID string
		seedVerb    string
		want        string // substring that must appear; "" = neither rule
	}{
		{"unset follows input", "", "custom", "", ""},
		{"auto follows input", "auto", "custom", "", ""},
		{"ja forces japanese", "ja", "custom", "", langRuleJA},
		{"en forces english", "en", "custom", "", langRuleEN},
		{"invalid falls through", "fr", "custom", "", ""},
		{"translate verb is exempt even with ja", "ja", "", "translate", ""},
		{"summarize verb still forces language", "ja", "", "summarize", langRuleJA},
		{"legacy translate assistant stays exempt", "ja", "translate", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeUIPrefsLang(t, tc.lang)
			c := &ChatConversation{AssistantID: tc.assistantID, SeedVerb: tc.seedVerb}
			got := c.personaOf()
			// The base persona + global output rule are always present.
			if !strings.Contains(got, chatOutputRule) {
				t.Fatalf("personaOf missing the global output rule")
			}
			for _, rule := range []string{langRuleJA, langRuleEN} {
				has := strings.Contains(got, rule)
				wantHas := tc.want == rule
				if has != wantHas {
					t.Errorf("rule %q presence = %v, want %v\nprompt:\n%s", rule, has, wantHas, got)
				}
			}
		})
	}
}
