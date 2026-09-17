package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The whole point of the table: an argument nobody recognises costs an exit code, not an Agent.
func TestDispatchCLIRejectsUnknownArguments(t *testing.T) {
	for _, args := range [][]string{
		{"--versoin"},
		{"-x"},
		{"install-jd"},
		{"af-db-"},
		{"--", "cred"},
		{"install-jdk21"},
	} {
		var out, errOut bytes.Buffer
		code, handled := dispatchCLI(args, &out, &errOut)
		if !handled || code != 2 {
			t.Fatalf("dispatchCLI(%q) = (%d, %v), want (2, true) — an unknown argument must not reach the boot path", args, code, handled)
		}
		if got := errOut.String(); !strings.Contains(got, "unknown subcommand") || !strings.Contains(got, "Usage:") {
			t.Fatalf("dispatchCLI(%q) stderr = %q, want the unknown-subcommand line and usage", args, got)
		}
		if out.Len() != 0 {
			t.Fatalf("dispatchCLI(%q) wrote %q to stdout; diagnostics belong on stderr", args, out.String())
		}
	}
}

func TestDispatchCLIVersion(t *testing.T) {
	for _, spelling := range []string{"version", "--version", "-v"} {
		var out, errOut bytes.Buffer
		code, handled := dispatchCLI([]string{spelling}, &out, &errOut)
		if !handled || code != 0 {
			t.Fatalf("dispatchCLI(%q) = (%d, %v), want (0, true)", spelling, code, handled)
		}
		line := strings.TrimSpace(out.String())
		if !strings.HasPrefix(line, "workspace-agent ") || !strings.Contains(line, buildVersion) {
			t.Fatalf("dispatchCLI(%q) stdout = %q, want a line naming the binary and %q", spelling, line, buildVersion)
		}
		if strings.Contains(line, "\n") {
			t.Fatalf("dispatchCLI(%q) stdout = %q, want a single line (version probes read the first line only)", spelling, line)
		}
	}
}

func TestDispatchCLIHelp(t *testing.T) {
	for _, spelling := range []string{"help", "--help", "-h"} {
		var out, errOut bytes.Buffer
		code, handled := dispatchCLI([]string{spelling}, &out, &errOut)
		if !handled || code != 0 {
			t.Fatalf("dispatchCLI(%q) = (%d, %v), want (0, true)", spelling, code, handled)
		}
		got := out.String()
		for _, want := range []string{"Usage:", "--version", "af-db", "install-jdk"} {
			if !strings.Contains(got, want) {
				t.Fatalf("usage for %q is missing %q:\n%s", spelling, want, got)
			}
		}
		for _, sc := range subcommands {
			if sc.hidden && strings.Contains(got, "  "+sc.name+" ") {
				t.Fatalf("usage advertises internal subcommand %q", sc.name)
			}
			if !sc.hidden && !strings.Contains(got, sc.name) {
				t.Fatalf("usage is missing subcommand %q", sc.name)
			}
		}
	}
}

// Only these two inputs may fall through to the server boot.
func TestDispatchCLIBootsOnlyWithoutArguments(t *testing.T) {
	for _, args := range [][]string{{}, {"serve"}} {
		var out, errOut bytes.Buffer
		if _, handled := dispatchCLI(args, &out, &errOut); handled {
			t.Fatalf("dispatchCLI(%q) was handled; it must fall through to the boot", args)
		}
	}
}

func TestSubcommandTableWellFormed(t *testing.T) {
	seen := map[string]bool{}
	// Reserved by dispatchCLI itself: a table entry with one of these names would never run.
	for _, n := range []string{"serve", "version", "--version", "-v", "help", "--help", "-h"} {
		seen[n] = true
	}
	for _, sc := range subcommands {
		if sc.run == nil {
			t.Fatalf("subcommand %q has no run function", sc.name)
		}
		if sc.summary == "" {
			t.Fatalf("subcommand %q has no summary", sc.name)
		}
		for _, n := range append([]string{sc.name}, sc.aliases...) {
			if seen[n] {
				t.Fatalf("subcommand name %q is declared twice (or shadowed by a reserved one)", n)
			}
			seen[n] = true
			if got, ok := lookupSubcommand(n); !ok || got.name != sc.name {
				t.Fatalf("lookupSubcommand(%q) did not resolve to %q", n, sc.name)
			}
		}
	}
	if _, ok := lookupSubcommand("nope"); ok {
		t.Fatal("lookupSubcommand resolved a name that is not in the table")
	}
}

// The defect this guards against is not a wrong branch but an extra one: the trap was twenty
// `os.Args[1] == "…"` tests with no default, and one new test added outside the table brings it
// straight back — for that argument alone, which is exactly how it stays unnoticed.
func TestNoArgumentDispatchOutsideTheTable(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	// Indexing (`os.Args[1]`) is a branch on an argument; slicing (`os.Args[1:]`, what main
	// hands to dispatchCLI) is not.
	argIndex := regexp.MustCompile(`os\.Args\[[0-9]+\]`)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if argIndex.MatchString(line) {
				t.Errorf("%s:%d dispatches on an argument outside the cli.go table: %s", name, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// Every `workspace-agent <verb>` the container spells must be a verb the table answers. Before
// the guard a typo here booted a second Agent; now it exits 2, and this is where it should be
// caught instead.
func TestContainerCallersSpellKnownSubcommands(t *testing.T) {
	call := regexp.MustCompile(`workspace-agent ([a-z][a-z0-9-]*)`)
	for _, path := range []string{"../entrypoint.sh", "../Dockerfile"} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", filepath.Base(path), err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			// Comments are prose ("the workspace-agent binary", and the Japanese notes); both
			// file types comment with #, and only what survives the cut is executed.
			if c := strings.Index(line, "#"); c >= 0 {
				line = line[:c]
			}
			for _, m := range call.FindAllStringSubmatch(line, -1) {
				if _, ok := lookupSubcommand(m[1]); !ok {
					t.Errorf("%s:%d calls `workspace-agent %s`, which no subcommand answers", filepath.Base(path), i+1, m[1])
				}
			}
		}
	}
}
