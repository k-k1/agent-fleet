package mcpsrv

import "testing"

// startAfter of another type is refused, not dropped: a number of seconds would otherwise leave a
// note that waits for nothing, and an update would go through without its time.
func TestArgMemoStartAfter(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
		want *string
		bad  bool
	}{
		{name: "absent", args: map[string]any{}},
		{name: "null", args: map[string]any{"startAfter": nil}},
		{name: "empty clears", args: map[string]any{"startAfter": ""}, want: ptr("")},
		{name: "a time", args: map[string]any{"startAfter": "2026-12-04T09:00:00+09:00"}, want: ptr("2026-12-04T09:00:00+09:00")},
		{name: "a number", args: map[string]any{"startAfter": float64(1796000000)}, bad: true},
		{name: "an object", args: map[string]any{"startAfter": map[string]any{}}, bad: true},
	} {
		got, err := argMemoStartAfter(tc.args)
		if tc.bad {
			if err == nil {
				t.Errorf("%s: no error", tc.name)
			}
			continue
		}
		if err != nil || (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Errorf("%s: got %v %v", tc.name, got, err)
		}
	}
}

func ptr(s string) *string { return &s }
