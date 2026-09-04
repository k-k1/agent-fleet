package session

import "testing"

// TestLabelRoundTrip covers building a label and reading it back.
//
// What matters most is that the old `[AF] <title>` form is stripped too: a label is baked
// into meta at creation, so existing sessions keep the old form and both shapes appear on
// screen at once. A strip that only handles the new form leaves the tag on old rows.
func TestLabelRoundTrip(t *testing.T) {
	for _, c := range []struct{ label, strip, name string }{
		{"[AF:s6bbilu] 94-freeze 試走2本", "94-freeze 試走2本", "s6bbilu"},
		{"[AF:s6bbilu] agent-fleet @0831-1922", "agent-fleet @0831-1922", "s6bbilu"},
		{"[AF] 旧形式のまま残ったラベル", "旧形式のまま残ったラベル", ""},
		{"[AF]タイトル", "タイトル", ""}, // stripped without the space too
		{"どこか他所で付いた --name", "どこか他所で付いた --name", ""},
		{"", "", ""},
		// Even when a title starts with "[AF:", only ValidName's character set is accepted as
		// a name. Loosen this and part of a user's title could be shown as the sender name.
		{"[AF:日本語] t", "[AF:日本語] t", ""},
	} {
		if got := StripLabel(c.label); got != c.strip {
			t.Errorf("StripLabel(%q) = %q, want %q", c.label, got, c.strip)
		}
		if got := LabelSessionName(c.label); got != c.name {
			t.Errorf("LabelSessionName(%q) = %q, want %q", c.label, got, c.name)
		}
	}

	// Build then read back must agree. An invalid name falls back to the old tag rather than
	// blocking the launch.
	if got := LabelPrefix("s6bbilu"); got != "[AF:s6bbilu] " {
		t.Errorf("LabelPrefix = %q", got)
	}
	if got := LabelPrefix("../etc"); got != "[AF] " {
		t.Errorf("LabelPrefix(invalid name) = %q, want %q", got, "[AF] ")
	}
	if got := LabelSessionName(LabelPrefix("sabc123") + "タイトル"); got != "sabc123" {
		t.Errorf("round trip = %q", got)
	}
}

// TestDisplayStripsLabelTag pins that with no title set the display name is the label with
// its tag stripped.
func TestDisplayStripsLabelTag(t *testing.T) {
	if got := Display(Meta{Label: "[AF:s6bbilu] agent-fleet @0831-1922"}); got != "agent-fleet @0831-1922" {
		t.Errorf("Display = %q", got)
	}
	if got := Display(Meta{Title: "題", Label: "[AF:s6bbilu] 別"}); got != "題" {
		t.Errorf("Display (title wins) = %q", got)
	}
}
