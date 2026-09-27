package claude

import "testing"

// list_models is where a parent session learns which efforts a claude child accepts, and
// create_session refuses anything outside them, so the catalog must carry them — except for
// Haiku, which takes no effort, whether named by alias or by a registered full id.
func TestModelsDeclareEfforts(t *testing.T) {
	for _, m := range Models() {
		if got := len(m.Efforts); (m.ID == "haiku") != (got == 0) {
			t.Errorf("%s: %d efforts", m.ID, got)
		}
	}
	if EffortsFor("claude-haiku-4-5") != nil {
		t.Error("a registered Haiku id was given efforts")
	}
	if len(EffortsFor("claude-opus-5-5")) == 0 {
		t.Error("a registered Opus id was given no efforts")
	}
}
