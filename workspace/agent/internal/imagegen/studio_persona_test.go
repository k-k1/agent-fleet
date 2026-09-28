package imagegen

import "testing"

// The title suggester drops the persona by recognising it; both languages, with and without a
// studio title, must be recognised, and a member's own words must not be.
func TestIsStudioPersona(t *testing.T) {
	for _, lang := range []string{"ja", "en"} {
		for _, title := range []string{"", "港の夕暮れ"} {
			if p := studioPersona(lang, title); !IsStudioPersona(p) {
				t.Errorf("persona (%s, %q) not recognised: %.60s", lang, title, p)
			}
		}
	}
	for _, own := range []string{
		"夕暮れの港にいる三毛猫を描きたい",
		"このセッションでは、Agent Fleet のログ画面を直します",
		"In this session we fix the login page",
		"",
	} {
		if IsStudioPersona(own) {
			t.Errorf("a member's message taken for the persona: %q", own)
		}
	}
}
