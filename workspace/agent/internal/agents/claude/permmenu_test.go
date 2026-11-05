package claude

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func readPermFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "permmenu", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestParsePermissionMenuMeasured pins the three shapes captured from claude 2.1.318. The
// two-row one is the reason this parser exists: Down Enter there is "No", and Down Down
// wraps back to "Yes".
func TestParsePermissionMenuMeasured(t *testing.T) {
	cases := []struct {
		file    string
		rows    int
		keys    map[string]string
		choices []string
	}{
		{"bash_ask_rule_two_options.txt", 2, map[string]string{PermAllow: "1", PermDeny: "2"}, []string{PermAllow, PermDeny}},
		{"bash_four_options.txt", 4, map[string]string{PermAllow: "1", PermAlways: "2", PermDeny: "4"}, []string{PermAllow, PermAlways, PermDeny}},
		{"write_three_options.txt", 3, map[string]string{PermAllow: "1", PermAlways: "2", PermDeny: "3"}, []string{PermAllow, PermAlways, PermDeny}},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			m, ok := ParsePermissionMenu(readPermFixture(t, c.file))
			if !ok {
				t.Fatal("menu not recognised")
			}
			if len(m.Rows) != c.rows {
				t.Fatalf("rows = %d, want %d: %+v", len(m.Rows), c.rows, m.Rows)
			}
			for _, ch := range []string{PermAllow, PermAlways, PermDeny} {
				got, ok := m.Key(ch)
				want, wantOK := c.keys[ch]
				if ok != wantOK || got != want {
					t.Errorf("Key(%s) = %q,%v; want %q,%v", ch, got, ok, want, wantOK)
				}
			}
			if got := m.Choices(); !reflect.DeepEqual(got, c.choices) {
				t.Errorf("Choices = %v, want %v", got, c.choices)
			}
		})
	}
}

// The Bash menu's "switch to auto mode" row must never be taken for "always": it hands every
// later prompt to a classifier, which is not what the card's button promises.
func TestParsePermissionMenuAutoModeIsNotAlways(t *testing.T) {
	m, _ := ParsePermissionMenu(readPermFixture(t, "bash_four_options.txt"))
	if m.Rows[2].Choice != "" {
		t.Fatalf("row 3 %q classified as %q", m.Rows[2].Label, m.Rows[2].Choice)
	}
}

func TestParsePermissionMenuRefusesWhatIsNotTheLiveDialog(t *testing.T) {
	cases := map[string]string{
		"no dialog": "❯ hello\n────\n  ⏸ manual mode on\n",
		// A transcript that quotes a dialog, with the composer drawn under it: the footer the
		// rows must end at is missing.
		"quoted without footer": " Do you want to proceed?\n ❯ 1. Yes\n   2. No\n────\n❯ \n",
		"gap in numbering":      " Do you want to proceed?\n ❯ 1. Yes\n   3. No\n Esc to cancel\n",
		"single row":            " Do you want to proceed?\n ❯ 1. Yes\n Esc to cancel\n",
		"no question line":      " some other screen\n ❯ 1. Yes\n   2. No\n Esc to cancel\n",
		// AskUserQuestion's footer mentions Esc too, but not at the start of the line.
		"question menu footer":  " Do you want to proceed?\n ❯ 1. Yes\n   2. No\n Enter to select · Esc to cancel\n",
		"rows below the footer": " Do you want to proceed?\n ❯ 1. Yes\n   2. No\n Esc to cancel\n❯ \n",
	}
	for name, pane := range cases {
		if m, ok := ParsePermissionMenu(pane); ok {
			t.Errorf("%s: parsed %+v", name, m.Rows)
		}
	}
}

// Only the last question line counts: an earlier, quoted dialog above the live one must not
// shift the rows.
func TestParsePermissionMenuTakesTheLastDialog(t *testing.T) {
	pane := " Do you want to proceed?\n ❯ 1. Yes\n   2. Yes, and don't ask again for: ls *\n   3. No\n Esc to cancel\n" +
		"● later output\n" + readPermFixture(t, "bash_ask_rule_two_options.txt")
	m, ok := ParsePermissionMenu(pane)
	if !ok || len(m.Rows) != 2 {
		t.Fatalf("ok=%v rows=%+v", ok, m.Rows)
	}
	if _, ok := m.Key(PermAlways); ok {
		t.Fatal("always offered from the quoted dialog")
	}
}

func TestClassifyPermRow(t *testing.T) {
	cases := map[string]string{
		"Yes": PermAllow,
		"No":  PermDeny,
		"No, and tell Claude what to do differently (esc)":                 PermDeny,
		"Yes, and don’t ask again for: rtk jira *":                         PermAlways,
		"Yes, and don't ask again for jira commands in /repo":              PermAlways,
		"Yes, allow all edits during this session (shift+tab)":             PermAlways,
		"Yes, and switch to accept edits (…) for this session (shift+tab)": PermAlways,
		"Yes, and switch to auto mode · auto mode handles these prompts":   "",
		"Yes, and switch to auto mode for this session":                    "",
		"Nope": "",
	}
	for in, want := range cases {
		if got := classifyPermRow(in); got != want {
			t.Errorf("classifyPermRow(%q) = %q, want %q", in, got, want)
		}
	}
}

// A complete dialog quoted in the transcript (footer included) above a live one whose question
// wraps: the parser must still read the live dialog, not stop at the quoted footer. Reading the
// quote resolved deny to row 2, which on the live Write menu switches to accept edits (#3357 review).
func TestParsePermissionMenuQuotedDialogAboveAWrappedQuestion(t *testing.T) {
	pane := "● the test fixture reads:\n" +
		" Do you want to proceed?\n ❯ 1. Yes\n   2. No\n Esc to cancel · Tab to amend\n" +
		"────\n Create file\n" +
		" Do you want to create\n src/features/a/very/long/path/that/wraps/onto/the/next/line/component.tsx?\n" +
		" ❯ 1. Yes\n   2. Yes, and switch to accept edits (auto-approve file edits and common file commands)\n" +
		"   for this session (shift+tab)\n   3. No\n\n Esc to cancel · Tab to amend\n\n\n"
	m, ok := ParsePermissionMenu(pane)
	if !ok || len(m.Rows) != 3 {
		t.Fatalf("ok=%v rows=%+v", ok, m.Rows)
	}
	for c, want := range map[string]string{PermAllow: "1", PermAlways: "2", PermDeny: "3"} {
		if got, _ := m.Key(c); got != want {
			t.Errorf("Key(%s) = %q, want %q", c, got, want)
		}
	}
}

// A label wrapped by a narrow pane is classified on its joined text (#3357 review): "for this
// session" on the continuation line must still make the row "always".
func TestParsePermissionMenuJoinsWrappedLabels(t *testing.T) {
	pane := " Do you want to create note.txt?\n ❯ 1. Yes\n   2. Yes, and switch to accept edits\n" +
		"   (auto-approve file edits) for this\n   session (shift+tab)\n   3. No\n\n Esc to cancel · Tab to amend\n"
	m, ok := ParsePermissionMenu(pane)
	if !ok {
		t.Fatal("not parsed")
	}
	if got, _ := m.Key(PermAlways); got != "2" {
		t.Fatalf("always = %q, rows %+v", got, m.Rows)
	}
	// The auto-mode exclusion applies to the joined text as well.
	pane = " Do you want to proceed?\n ❯ 1. Yes\n   2. Yes, and switch to auto\n   mode for this session\n   3. No\n\n Esc to cancel\n"
	if m, ok = ParsePermissionMenu(pane); !ok {
		t.Fatal("not parsed")
	}
	if _, found := m.Key(PermAlways); found {
		t.Fatalf("auto mode taken for always: %+v", m.Rows)
	}
}
