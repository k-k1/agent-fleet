package mcpreg

import "testing"

// Past-session search reaches the session-side server only through --session-search (ADR 0110
// decision 6). The preference defaults on in uiprefs, but an unwired hook here still reads off.
func TestSessionSearchRunArg(t *testing.T) {
	old := SessionSearchEnabled
	t.Cleanup(func() { SessionSearchEnabled = old })

	for _, c := range []struct {
		name string
		hook func() bool
		want bool
	}{
		{"no hook", nil, false},
		{"off", func() bool { return false }, false},
		{"on", func() bool { return true }, true},
	} {
		SessionSearchEnabled = c.hook
		args, _ := BuiltinRunArgs(BuiltinAF)
		if contains(args, "--session-search") != c.want {
			t.Errorf("%s: args = %v", c.name, args)
		}
	}
	SessionSearchEnabled = func() bool { return true }
	if args, _ := BuiltinRunArgs(BuiltinAWS); contains(args, "--session-search") {
		t.Fatalf("a non-af builtin took the flag: %v", args)
	}
}
