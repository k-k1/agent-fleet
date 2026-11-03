package uiprefs

import (
	"strings"
	"testing"
)

func TestZoneNameResolutionOrder(t *testing.T) {
	long := strings.Repeat("A", 200)
	cases := []struct {
		name string
		p    map[string]any
		want string
	}{
		{"explicit wins over the published zone", map[string]any{"timeZone": "Asia/Tokyo", "browserTimeZone": "America/New_York"}, "Asia/Tokyo"},
		{"auto uses the published zone", map[string]any{"timeZone": "auto", "browserTimeZone": "America/New_York"}, "America/New_York"},
		{"unset timeZone uses the published zone", map[string]any{"browserTimeZone": "America/New_York"}, "America/New_York"},
		{"auto and nothing published", map[string]any{"timeZone": "auto"}, ""},
		{"empty published zone", map[string]any{"timeZone": "auto", "browserTimeZone": ""}, ""},
		{"invalid published zone", map[string]any{"timeZone": "auto", "browserTimeZone": "Mars/Olympus"}, ""},
		{"Local is not a zone", map[string]any{"timeZone": "auto", "browserTimeZone": "Local"}, ""},
		{"path-like value", map[string]any{"browserTimeZone": "../../etc/passwd"}, ""},
		{"oversized value", map[string]any{"browserTimeZone": long}, ""},
		{"non-string published zone", map[string]any{"timeZone": "auto", "browserTimeZone": 7}, ""},
		{"unloadable explicit falls through to the published zone", map[string]any{"timeZone": "Mars/Olympus", "browserTimeZone": "Asia/Tokyo"}, "Asia/Tokyo"},
		{"auto is never loaded as a zone name", map[string]any{"timeZone": "auto"}, ""},
		{"empty prefs", map[string]any{}, ""},
		{"nil prefs", nil, ""},
	}
	for _, c := range cases {
		if got := ZoneName(c.p); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}
