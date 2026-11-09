package status

import "testing"

// Records written before the tool_use id was kept still read, and the id never leaks into
// the command (#3725).
func TestCallRecordRoundTrip(t *testing.T) {
	cases := []struct{ detail, command, id string }{
		{"Bash · ls", "ls", "toolu_1"},
		{"Edit · a.go", "", "toolu_2"},
		{"Bash · ls", "ls", ""},
		{"Claude needs your permission", "", ""},
	}
	for _, c := range cases {
		d, cmd, id := splitCall(joinCall(c.detail, c.command, c.id))
		if d != c.detail || cmd != c.command || id != c.id {
			t.Errorf("%+v read back as %q %q %q", c, d, cmd, id)
		}
	}
	if d, cmd, id := splitCall("Bash · ls" + recordSep + "ls"); d != "Bash · ls" || cmd != "ls" || id != "" {
		t.Errorf("old record: %q %q %q", d, cmd, id)
	}
}
