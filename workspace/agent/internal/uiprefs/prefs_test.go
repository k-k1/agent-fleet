package uiprefs

import (
	"testing"
)

// Accumulated data (learned reply suggestions, pins, usage history, key bindings, ...)
// is gone beyond recovery the moment an accidentally shrunken PUT arrives. It has
// actually happened once — reply suggestions went back to their initial state on every
// device — so keeping the version from just before a shrinking write in .prev is fixed
// as the specification. The write is not rejected: "clear all" under Settings > Keys is
// a legitimate user action, and rejecting it would make that button do nothing.
func TestShrunkPrefKeys(t *testing.T) {
	before := map[string]any{
		"quickReplies":       map[string]any{"ok": map[string]any{"text": "OK"}},
		"quickRepliesPinned": []any{"OK"},
		"ttsUserDict":        "af=エーエフ",
		"ssmHostUsage":       map[string]any{},
		"assistantAutoTurn":  false,
	}
	tests := []struct {
		name  string
		after map[string]any
		want  []string
	}{
		{"defaults over real data flags every populated key",
			map[string]any{"quickReplies": map[string]any{}, "quickRepliesPinned": []any{}, "ttsUserDict": ""},
			[]string{"quickReplies", "quickRepliesPinned", "ttsUserDict"}},
		{"a missing key counts as lost too (an older Console omits it)",
			map[string]any{},
			[]string{"quickReplies", "quickRepliesPinned", "ttsUserDict"}},
		{"carrying the same content through is not a loss",
			before,
			nil},
		{"growing is not a loss",
			map[string]any{
				"quickReplies":       map[string]any{"ok": map[string]any{"text": "OK"}, "go": map[string]any{"text": "続けて"}},
				"quickRepliesPinned": []any{"OK", "続けて"},
				"ttsUserDict":        "af=エーエフ",
			},
			[]string{}},
		{"an already-empty key cannot shrink", map[string]any{"ssmHostUsage": map[string]any{}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShrunkKeys(before, tt.after)
			if len(tt.want) == 0 && len(got) == 0 {
				return
			}
			// The order is accumulatedPrefKeys' order (stable).
			if len(got) < len(tt.want) {
				t.Fatalf("shrunk = %v, want %v", got, tt.want)
			}
			for _, k := range tt.want {
				found := false
				for _, g := range got {
					if g == k {
						found = true
					}
				}
				if !found {
					t.Fatalf("shrunk = %v, missing %q", got, k)
				}
			}
		})
	}
	// A boolean is a chosen value, not something that vanished, so flipping it to false
	// is never a reason to snapshot.
	if got := ShrunkKeys(before, map[string]any{"assistantAutoTurn": true}); len(got) != 3 {
		t.Fatalf("boolean flips must not be counted as accumulated loss: %v", got)
	}
}
