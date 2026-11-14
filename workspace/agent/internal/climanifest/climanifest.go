// Package climanifest is the Go reader of the CLI manifest, cli-manifest.psv in this
// directory: the one list of every CLI the project pins, watches and contract-tests.
//
// The file is embedded, not copied: the shell tools (deploy/local/cli-manifest.sh) read this
// very file, so there is no second copy to drift. Parsing needs no network and no
// dependency. The format and its columns are documented in the file's header; a row that
// does not parse is an error, never skipped.
package climanifest

import (
	_ "embed"
	"fmt"
	"strings"
)

//go:embed cli-manifest.psv
var raw string

// Entry is one CLI.
type Entry struct {
	Name       string   // the kind; also the versions.json key
	Arg        string   // the Dockerfile ARG that pins its version
	Source     string   // npm | github | agy | cursor | kiro | muse
	Locator    string   // package, repo or release endpoint for Source
	Contract   string   // contract workflow file, "" when none gates the CLI
	Install    string   // npm | agy | cursor | kiro, "" when the action does not install it
	Dispatch   string   // auto | manual, "" when there is no contract
	Input      string   // workflow_dispatch input that takes `latest`, "" when none
	Secrets    []string // CI secrets, any one of which lets the contract run
	SecretHint string   // extra words the update issue adds after the secret names
	Flags      []string // major-manual
}

// HasFlag reports whether the entry carries flag.
func (e Entry) HasFlag(flag string) bool {
	for _, f := range e.Flags {
		if f == flag {
			return true
		}
	}
	return false
}

const fields = 11

// Parse reads manifest text. Blank lines and `#` comments are skipped.
func Parse(text string) ([]Entry, error) {
	none := func(s string) string {
		if s == "-" {
			return ""
		}
		return s
	}
	list := func(s string) []string {
		if s = none(s); s == "" {
			return nil
		}
		return strings.Split(s, ",")
	}
	var out []Entry
	seen := map[string]bool{}
	for i, line := range strings.Split(text, "\n") {
		if line = strings.TrimRight(line, "\r"); line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "|")
		if len(f) != fields {
			return nil, fmt.Errorf("cli-manifest.psv:%d: want %d '|' separated fields, got %d", i+1, fields, len(f))
		}
		e := Entry{
			Name: f[0], Arg: f[1], Source: f[2], Locator: f[3], Contract: none(f[4]),
			Install: none(f[5]), Dispatch: none(f[6]), Input: none(f[7]), Secrets: list(f[8]),
			SecretHint: none(f[9]), Flags: list(f[10]),
		}
		if e.Name == "" || e.Arg == "" || e.Source == "" {
			return nil, fmt.Errorf("cli-manifest.psv:%d: name, arg and source are required", i+1)
		}
		if seen[e.Name] {
			return nil, fmt.Errorf("cli-manifest.psv:%d: duplicate %q", i+1, e.Name)
		}
		seen[e.Name] = true
		if (e.Contract == "") != (e.Dispatch == "") {
			return nil, fmt.Errorf("cli-manifest.psv:%d: contract and dispatch must both be set or both '-'", i+1)
		}
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("cli-manifest.psv lists no CLI")
	}
	return out, nil
}

// Entries returns the embedded manifest, in file order. It panics only if the file shipped
// in the binary is malformed, which the package's test makes impossible to merge.
func Entries() []Entry {
	e, err := Parse(raw)
	if err != nil {
		panic(err)
	}
	return e
}

// Lookup returns the entry for a kind.
func Lookup(name string) (Entry, bool) {
	for _, e := range Entries() {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}
