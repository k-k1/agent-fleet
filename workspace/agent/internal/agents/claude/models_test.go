package claude

import "testing"

// list_models is where a parent session learns which efforts a claude child accepts, and
// create_session refuses anything outside them, so the catalog must carry them for every
// built-in alias, the haiku alias (which tracks Haiku 5.x) included.
func TestModelsDeclareEfforts(t *testing.T) {
	for _, m := range Models() {
		if len(m.Efforts) != 5 {
			t.Errorf("%s: %d efforts", m.ID, len(m.Efforts))
		}
	}
}

func TestEffortsForHaikuGenerations(t *testing.T) {
	for model, want := range map[string]bool{
		"haiku":                     true,
		"claude-haiku-5-5":          true,
		"claude-haiku-5-5[1m]":      true,
		"claude-opus-5-5":           true,
		"claude-haiku-4-5":          false,
		"claude-haiku-4-5-20251001": false,
		"claude-3-5-haiku":          false,
		"claude-3-5-haiku-20241022": false,
		"claude-3-haiku-20240307":   false,
		"Claude-Haiku-4":            false,
	} {
		if got := len(EffortsFor(model)) > 0; got != want {
			t.Errorf("EffortsFor(%q) offered=%v, want %v", model, got, want)
		}
	}
}
