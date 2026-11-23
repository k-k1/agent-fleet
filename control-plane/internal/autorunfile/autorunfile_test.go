package autorunfile

import (
	"errors"
	"math"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func hasIssue(is []Issue, line int, key, part string) bool {
	for _, i := range is {
		if i.Line == line && i.Key == key && strings.Contains(i.Reason, part) {
			return true
		}
	}
	return false
}

func TestParseAFullEntry(t *testing.T) {
	f := Parse([]byte(`version: 1
categories:
  issue-rounds:
    agent_kind: claude
    model: claude-opus-5-5
    effort: high
    route: anthropic-api
    spend_cap_usd: 30
    max_active: 2
    batch: 1
    open_cron: "0 9 * * 1-5"
    open_minutes: 120
    tz: Asia/Tokyo
    max_fires_per_day: 10
    max_task_hours: 8
    worktree: true
    tidy: false
    continuation_max: 4
    instruction: |
      Use the queue-implementer skill.
  empty:
`))
	if len(f.Errors) != 0 || !f.Usable("issue-rounds") || !f.Usable("empty") {
		t.Fatalf("errors: %+v / %+v", f.Errors, f.Categories["issue-rounds"])
	}
	e := f.Categories["issue-rounds"]
	if e.Line != 3 || len(e.Warnings) != 0 || len(e.Values) != 16 {
		t.Fatalf("entry %+v", e)
	}
	if v := e.Values["spend_cap_usd"]; v.Num != 30 || v.Line != 8 {
		t.Errorf("spend_cap_usd %+v", v)
	}
	if v := e.Values["open_cron"]; v.Str != "0 9 * * 1-5" {
		t.Errorf("open_cron %+v", v)
	}
	if v := e.Values["worktree"]; !v.Bool || !e.Has("tidy") || e.Values["tidy"].Bool {
		t.Errorf("bools %+v %+v", v, e.Values["tidy"])
	}
	if v := e.Values["instruction"]; v.Str != "Use the queue-implementer skill.\n" {
		t.Errorf("instruction %q", v.Str)
	}
	if len(f.Categories["empty"].Values) != 0 {
		t.Errorf("empty entry has values")
	}
}

// The switches only the Console may turn are warned about and never reach Values, and so are
// the values of an ordinary key that switch something on or a gate off.
func TestParseConsoleOnlySwitchesAreWarnedAndDropped(t *testing.T) {
	f := Parse([]byte(`version: 1
categories:
  q:
    enabled: true
    accept_own_sessions: true
    tidy_destinations: [a, b]
    worktree: false
    tidy: true
    max_active: 3
`))
	e := f.Categories["q"]
	if !f.Usable("q") {
		t.Fatalf("errors %+v %+v", f.Errors, e.Errors)
	}
	for _, w := range []struct {
		line int
		key  string
	}{{4, "enabled"}, {5, "accept_own_sessions"}, {6, "tidy_destinations"}, {7, "worktree"}, {8, "tidy"}} {
		if !hasIssue(e.Warnings, w.line, w.key, "Console only") {
			t.Errorf("no warning for %s on line %d: %+v", w.key, w.line, e.Warnings)
		}
		if e.Has(w.key) {
			t.Errorf("%s reached Values", w.key)
		}
	}
	if len(e.Values) != 1 || e.Values["max_active"].Int != 3 {
		t.Errorf("values %+v", e.Values)
	}
	// The opposite values are ordinary.
	f = Parse([]byte("version: 1\ncategories:\n  q:\n    enabled: false\n    worktree: true\n    tidy: false\n"))
	e = f.Categories["q"]
	if !e.Has("worktree") || !e.Has("tidy") || e.Has("enabled") || len(e.Warnings) != 1 {
		t.Errorf("opposite values: %+v / %+v", e.Values, e.Warnings)
	}
}

func TestParseRefusesUnknownKeysAndWrongTypes(t *testing.T) {
	f := Parse([]byte(`version: 1
categories:
  q:
    max_actve: 2
    max_active: "2"
    spend_cap_usd: lots
    worktree: yes
    model: 4
    batch: 1.5
    open_after: 99999999999
    instruction: [a]
    max_active: 3
`))
	e := f.Categories["q"]
	if f.Usable("q") || len(f.Errors) != 0 {
		t.Fatalf("usable with errors? file %+v entry %+v", f.Errors, e.Errors)
	}
	for _, w := range []struct {
		line      int
		key, part string
	}{
		{4, "max_actve", "unknown key"},
		{5, "max_active", "whole number"},
		{6, "spend_cap_usd", "number"},
		{7, "worktree", "true or false"},
		{8, "model", "string"},
		{9, "batch", "whole number"},
		{10, "open_after", "out of range"},
		{11, "instruction", "single value"},
		{12, "max_active", "twice"},
	} {
		if !hasIssue(e.Errors, w.line, w.key, w.part) {
			t.Errorf("no error %q for %s on line %d: %+v", w.part, w.key, w.line, e.Errors)
		}
	}
}

func TestParseFileLevelErrors(t *testing.T) {
	for _, c := range []struct {
		name, src string
		line      int
		key, part string
	}{
		{"empty", "", 0, "", "empty"},
		{"syntax", "version: 1\ncategories:\n  q: [\n", 3, "", ""},
		{"no version", "categories: {}\n", 1, "version", "missing"},
		{"version 2", "version: 2\ncategories: {}\n", 1, "version", "not supported"},
		{"version as string", "version: \"1\"\ncategories: {}\n", 1, "version", "not supported"},
		{"unknown top key", "version: 1\ncategories: {}\ndefaults: {}\n", 3, "defaults", "unknown key"},
		{"no categories", "version: 1\n", 1, "categories", "missing"},
		{"categories a list", "version: 1\ncategories: [a]\n", 2, "categories", "map each"},
		{"two documents", "version: 1\ncategories: {}\n---\nversion: 1\n", 3, "", "more than one"},
		{"duplicate category", "version: 1\ncategories:\n  q: {}\n  q: {}\n", 4, "", "twice"},
		{"not a mapping", "- a\n", 1, "", "must be a mapping"},
		{"too large", "version: 1\n" + strings.Repeat("#", MaxBytes), 0, "", "larger"},
	} {
		f := Parse([]byte(c.src))
		if len(f.Errors) == 0 {
			t.Errorf("%s: no error", c.name)
			continue
		}
		if c.part != "" && !hasIssue(f.Errors, c.line, c.key, c.part) {
			t.Errorf("%s: want line %d key %q %q, got %+v", c.name, c.line, c.key, c.part, f.Errors)
		}
		if c.part == "" && f.Errors[0].Line != c.line {
			t.Errorf("%s: want line %d, got %+v", c.name, c.line, f.Errors)
		}
		for n := range f.Categories {
			if f.Usable(n) {
				t.Errorf("%s: %s usable in a file with errors", c.name, n)
			}
		}
	}
}

func TestParseRefusesCredentials(t *testing.T) {
	for _, v := range []string{
		"AKIA" + "IOSFODNN7EXAMPLQ",
		"ghp_" + strings.Repeat("a", 36),
		"call it with sk-ant-" + strings.Repeat("b", 30),
		`password: "correcthorsebattery42"`,
		"https://user:hunter2hunter2@git.invalid/x.git",
	} {
		f := Parse([]byte("version: 1\ncategories:\n  q:\n    instruction: '" + strings.ReplaceAll(v, "'", "''") + "'\n"))
		e := f.Categories["q"]
		if f.Usable("q") || !hasIssue(e.Errors, 4, "instruction", "credential") {
			t.Errorf("%q: not refused: %+v", v, e.Errors)
		}
		for _, i := range e.Errors {
			if strings.Contains(i.Reason, v) {
				t.Errorf("the reason echoes the value: %q", i.Reason)
			}
		}
	}
	// Prose about tokens is not a credential.
	f := Parse([]byte("version: 1\ncategories:\n  q:\n    instruction: 'Never paste an api key or password: ask the user.'\n"))
	if !f.Usable("q") {
		t.Errorf("prose refused: %+v", f.Categories["q"].Errors)
	}
}

func TestParseInstructionFile(t *testing.T) {
	ok := Parse([]byte("version: 1\ncategories:\n  q:\n    instruction_file: .agent-fleet/q.md\n"))
	if !ok.Usable("q") || ok.Categories["q"].Values["instruction_file"].Str != ".agent-fleet/q.md" {
		t.Fatalf("%+v", ok.Categories["q"])
	}
	for _, p := range []string{"/etc/passwd", "../x.md", "a/../../x.md", "./x.md", "a\\b.md", "a/", ""} {
		f := Parse([]byte("version: 1\ncategories:\n  q:\n    instruction_file: '" + p + "'\n"))
		if f.Usable("q") {
			t.Errorf("%q accepted", p)
		}
	}
	both := Parse([]byte("version: 1\ncategories:\n  q:\n    instruction: x\n    instruction_file: q.md\n"))
	if both.Usable("q") || !hasIssue(both.Categories["q"].Errors, 5, "instruction_file", "not both") {
		t.Errorf("both: %+v", both.Categories["q"].Errors)
	}
}

// An anchor and its alias read as the value they name, so a shared block is allowed.
func TestParseAliases(t *testing.T) {
	f := Parse([]byte(`version: 1
categories:
  a: &base
    max_active: 3
  b: *base
`))
	if !f.Usable("b") || f.Categories["b"].Values["max_active"].Int != 3 {
		t.Fatalf("%+v %+v", f.Errors, f.Categories["b"])
	}
}

// Many categories aliasing one settings node read it once: their findings share one slice, so
// a small file cannot multiply the parser's work or the answer's size.
func TestParseAliasedEntriesAreReadOnce(t *testing.T) {
	var b strings.Builder
	b.WriteString("version: 1\ncategories:\n  c0: &bad\n")
	for i := 0; i < 200; i++ {
		b.WriteString("    k" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ": 1\n")
	}
	for i := 1; i < 2000; i++ {
		b.WriteString("  c" + strings.Repeat("1", 0) + string(rune('A'+i%26)) + string(rune('A'+(i/26)%26)) + string(rune('A'+i/676)) + ": *bad\n")
	}
	f := Parse([]byte(b.String()))
	if len(f.Categories) < 1000 {
		t.Fatalf("only %d categories (errors %+v)", len(f.Categories), f.Errors)
	}
	first := f.Categories["c0"]
	if len(first.Errors) != 200 {
		t.Fatalf("c0 has %d errors", len(first.Errors))
	}
	for name, e := range f.Categories {
		if len(e.Errors) != 200 || &e.Errors[0] != &first.Errors[0] {
			t.Fatalf("%s: its findings were made again", name)
		}
	}
}

func TestParseRefusesMergeKeysTagsAndNonStringKeys(t *testing.T) {
	for _, c := range []struct {
		name, src string
		line      int
		part      string
		entry     bool
	}{
		{"merge into categories", "version: 1\nbase: &b\n  q: {}\ncategories:\n  <<: *b\n", 2, "unknown key", false},
		{"merge key in categories", "version: 1\ncategories:\n  <<: {q: {}}\n", 3, "merge keys", false},
		{"merge key in an entry", "version: 1\ncategories:\n  q:\n    <<: {max_active: 2}\n", 4, "merge keys", true},
		{"number as a category name", "version: 1\ncategories:\n  1: {}\n", 3, "must be a string", false},
		{"bool as a key", "version: 1\ncategories:\n  q:\n    true: 1\n", 4, "must be a string", true},
		{"tagged categories", "version: 1\ncategories: !custom\n  q: {}\n", 2, "map each", false},
		{"tagged entry", "version: 1\ncategories:\n  q: !custom\n    max_active: 2\n", 3, "must be a mapping", true},
		{"tagged root", "!custom\nversion: 1\ncategories: {}\n", 1, "must be a mapping", false},
	} {
		f := Parse([]byte(c.src))
		is := f.Errors
		if c.entry {
			if f.Categories["q"] == nil {
				t.Errorf("%s: no entry: %+v", c.name, f.Errors)
				continue
			}
			is = f.Categories["q"].Errors
		}
		if !hasIssue(is, c.line, "", c.part) && !hasIssue(is, c.line, "base", c.part) && !hasIssue(is, c.line, "categories", c.part) {
			t.Errorf("%s: want line %d %q, got %+v", c.name, c.line, c.part, is)
		}
		if f.Usable("q") {
			t.Errorf("%s: q usable", c.name)
		}
	}
}

func TestParseNumbersThatAreNotLimits(t *testing.T) {
	for _, v := range []string{"1e-400", "-1e-400", ".inf", "-.inf", ".nan", "1e400"} {
		f := Parse([]byte("version: 1\ncategories:\n  q:\n    spend_cap_usd: " + v + "\n"))
		if f.Usable("q") {
			t.Errorf("%s accepted as %v", v, f.Categories["q"].Values["spend_cap_usd"].Num)
		}
	}
	for v, want := range map[string]float64{"0": 0, "0.0": 0, "-0": 0, "0e10": 0, "12.5": 12.5, "1e2": 100} {
		f := Parse([]byte("version: 1\ncategories:\n  q:\n    spend_cap_usd: " + v + "\n"))
		if !f.Usable("q") || f.Categories["q"].Values["spend_cap_usd"].Num != want {
			t.Errorf("%s: %+v", v, f.Categories["q"])
		}
	}
	if f := Parse([]byte("version: 1\ncategories:\n  q:\n    max_active: 99999999999999999999\n")); f.Usable("q") {
		t.Errorf("a huge integer accepted")
	}
}

// A credential pasted where a key or the version belongs is refused without being repeated.
func TestParseNeverEchoesAPastedCredential(t *testing.T) {
	tok := "ghp_" + strings.Repeat("Z", 36)
	for _, src := range []string{
		"version: " + tok + "\ncategories: {}\n",
		"version: 1\n" + tok + ": 1\ncategories: {}\n",
		"version: 1\ncategories:\n  " + tok + ": {}\n",
		"version: 1\ncategories:\n  q:\n    " + tok + ": 1\n",
	} {
		f := Parse([]byte(src))
		all := append([]Issue{}, f.Errors...)
		for _, e := range f.Categories {
			all = append(all, e.Errors...)
		}
		if len(all) == 0 {
			t.Errorf("%q: no error", src)
		}
		for _, i := range all {
			if strings.Contains(i.Key+i.Reason, "ZZZZ") {
				t.Errorf("%q: echoed in %+v", src, i)
			}
		}
		if _, ok := f.Categories[tok]; ok {
			t.Errorf("a credential became a category name")
		}
	}
}

// A value an alias names from many settings maps is read once, whatever its type, and a key
// cannot be an alias at all.
func TestParseReadsAnAliasedValueOnce(t *testing.T) {
	var b strings.Builder
	b.WriteString("version: 1\nlong: &long '" + strings.Repeat("y", 10000) + "'\n" +
		"num: &num 1" + strings.Repeat("0", 2000) + "\npath: &path " + strings.Repeat("a/", 3000) + "x.md\ncategories:\n")
	for i := 0; i < 300; i++ {
		b.WriteString("  c" + string(rune('A'+i%26)) + string(rune('A'+i/26)) + ":\n    instruction: *long\n    spend_cap_usd: *num\n")
		b.WriteString("  p" + string(rune('A'+i%26)) + string(rune('A'+i/26)) + ":\n    instruction_file: *path\n")
	}
	reads := 0
	saved := valueRead
	valueRead = func() { reads++ }
	t.Cleanup(func() { valueRead = saved })
	f := Parse([]byte(b.String()))
	if len(f.Categories) != 600 {
		t.Fatalf("%d categories, errors %+v", len(f.Categories), f.Errors)
	}
	if reads != 3 {
		t.Fatalf("%d value reads for three aliased values", reads)
	}
	// The number is too large for a float: the same error on every category that names it.
	if e := f.Categories["cAA"]; e == nil || !hasIssue(e.Errors, 3, "spend_cap_usd", "number") ||
		len(f.Categories["pAA"].Errors) != 0 || f.Categories["pAA"].Values["instruction_file"].Str == "" {
		t.Fatalf("aliased values not read: %d errors", len(f.Categories["cAA"].Errors))
	}

	k := Parse([]byte("version: 1\ncategories:\n  q:\n    &k max_active: 1\n    *k : 2\n"))
	if k.Usable("q") || !hasIssue(k.Categories["q"].Errors, 5, "", "alias") {
		t.Fatalf("alias key: %+v", k.Categories["q"])
	}
}

func TestParseZerosAreZero(t *testing.T) {
	for _, v := range []string{"0b0", "0o0", "0x0", "-0", "-0.0", "0.0e-400"} {
		f := Parse([]byte("version: 1\ncategories:\n  q:\n    spend_cap_usd: " + v + "\n"))
		n := f.Categories["q"].Values["spend_cap_usd"].Num
		if !f.Usable("q") || n != 0 || math.Signbit(n) {
			t.Errorf("%s: usable=%v num=%v errors %+v", v, f.Usable("q"), n, f.Categories["q"].Errors)
		}
	}
}

func TestParseSyntaxErrorDoesNotEchoACredential(t *testing.T) {
	tok := "ghp_" + strings.Repeat("Z", 36)
	f := Parse([]byte("version: 1\ncategories:\n  q: *" + tok + "\n"))
	if len(f.Errors) == 0 {
		t.Fatalf("no error")
	}
	for _, i := range f.Errors {
		if strings.Contains(i.Reason, "ZZZZ") {
			t.Fatalf("echoed: %+v", i)
		}
	}
}

// Format writes each value as its key's type, so the file parses back to the same values, also
// for strings YAML would otherwise read as numbers or booleans.
func TestFormatParsesBack(t *testing.T) {
	vals := []Value{
		{Key: "model", Str: "123"},
		{Key: "effort", Str: "true"},
		{Key: "route", Str: ""},
		{Key: "spend_cap_usd", Num: 12.5},
		{Key: "max_active", Int: 3},
		{Key: "worktree", Bool: true},
		{Key: "instruction", Str: "line one\n  indented: two\n"},
	}
	b, err := Format("issue: rounds", vals)
	if err != nil {
		t.Fatal(err)
	}
	f := Parse(b)
	e := f.Categories["issue: rounds"]
	if len(f.Errors) != 0 || e == nil || len(e.Errors) != 0 || len(e.Warnings) != 0 {
		t.Fatalf("%s\n%+v %+v", b, f.Errors, e)
	}
	for _, v := range vals {
		got := e.Values[v.Key]
		got.Line = 0
		if got != v {
			t.Errorf("%s: got %+v, want %+v", v.Key, got, v)
		}
	}
	if !strings.Contains(string(b), "instruction: |") {
		t.Errorf("a multi-line instruction is not a literal block:\n%s", b)
	}
	if _, err := Format("c", []Value{{Key: "enabled", Bool: true}}); err == nil {
		t.Error("a Console-only key was written")
	}
	if _, err := Format("c", []Value{{Key: "spend_cap_usd", Num: math.Inf(1)}}); err == nil {
		t.Error("an infinite number was written")
	}
}

// An instruction of many short lines grows past the limit as a literal block (every line is
// indented); Format then writes it quoted, and it still parses back to the same value. Values
// that cannot fit are ErrTooLarge rather than a file the reader refuses.
func TestFormatStaysWithinTheLimit(t *testing.T) {
	short := strings.Repeat("x\n", 8191) + "x"
	if lit, _ := format("rounds", []Value{{Key: "instruction", Str: short}}, yaml.LiteralStyle); len(lit) <= MaxBytes {
		t.Fatalf("the case no longer needs the quoted form: %d bytes", len(lit))
	}
	b, err := Format("rounds", []Value{{Key: "model", Str: "m"}, {Key: "instruction", Str: short}})
	if err != nil || len(b) > MaxBytes {
		t.Fatalf("%v, %d bytes", err, len(b))
	}
	f := Parse(b)
	if !f.Usable("rounds") || f.Categories["rounds"].Values["instruction"].Str != short {
		t.Fatalf("does not parse back: %+v", f.Errors)
	}
	if _, err := Format("rounds", []Value{{Key: "instruction", Str: strings.Repeat("y", MaxBytes)}}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("too large: %v", err)
	}
}
