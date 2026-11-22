package uiprefs

import "testing"

// The Go build-cache trim is on unless the member switched it off: a missing or malformed key
// must not turn it off.
func TestGoCacheAutoTrimDefaultsOn(t *testing.T) {
	for _, tc := range []struct {
		name  string
		prefs map[string]any
		want  bool
	}{
		{"missing", map[string]any{}, true},
		{"on", map[string]any{"goCacheAutoTrim": true}, true},
		{"off", map[string]any{"goCacheAutoTrim": false}, false},
		{"malformed", map[string]any{"goCacheAutoTrim": "no"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writePrefs(t, tc.prefs)
			if got := GoCacheAutoTrim(); got != tc.want {
				t.Fatalf("GoCacheAutoTrim() = %v, want %v", got, tc.want)
			}
		})
	}
}
