package autorunfile

import (
	"strings"
	"testing"
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
		"AKIAIOSFODNN7EXAMPLQ",
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
