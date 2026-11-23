// Package autorunfile parses .agent-fleet/autorun.yaml, the memo queue settings a repository
// carries in its default branch (#4881). It checks the file's shape: the schema version, the
// keys an entry may hold and the type of each value, values that look like credentials, and the
// settings only the Console may change. Value ranges and the rules between fields stay with the
// Control Plane's queue settings validation, which every Console write goes through too, so a
// file can never hold a value the Console could not save.
//
// Nothing here applies anything: a member approves a difference before it is saved.
package autorunfile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Path is where the file sits in the repository.
const Path = ".agent-fleet/autorun.yaml"

// Version is the only schema version this parser reads.
const Version = 1

// MaxBytes bounds the file. The Agent that reads it refuses a larger one before sending it.
const MaxBytes = 64 << 10

// Type is the YAML type a key takes.
type Type int

const (
	String Type = iota
	Int
	Number // an integer or a decimal
	Bool
)

// Key is one setting a category entry may hold.
type Key struct {
	Name string
	Type Type
}

// Keys are the settings a file may set, in the order a difference lists them. The names are the
// queue settings' own, in snake_case.
var Keys = []Key{
	{"agent_kind", String},
	{"model", String},
	{"effort", String},
	{"route", String},
	{"spend_cap_usd", Number},
	{"max_active", Int},
	{"batch", Int},
	{"open_cron", String},
	{"open_minutes", Int},
	{"tz", String},
	{"open_after", Int},
	{"max_fires_per_day", Int},
	{"max_task_hours", Int},
	{"worktree", Bool},
	{"tidy", Bool},
	{"continuation_max", Int},
	{"instruction", String},
	{"instruction_file", String},
}

// consoleOnly are the switches that turn a gate off or turn auto-run on (owner decision on
// #4881): never read from a file, whatever their value.
var consoleOnly = map[string]string{
	"enabled":             "turning auto-run on or off",
	"accept_own_sessions": "accepting messages from your own sessions without a release",
	"tidy_destinations":   "the queues a tidy run may move messages to as pending",
}

// consoleOnlyValue are the values of an ordinary key that switch something on or a gate off:
// the opposite value may come from the file.
var consoleOnlyValue = map[string]struct {
	value  bool
	reason string
}{
	"worktree": {false, "running in your working copy instead of a worktree"},
	"tidy":     {true, "turning the tidy run on"},
}

// Issue is one finding, with the 1-based line it is on (0 when it is about the whole file).
type Issue struct {
	Line   int    `json:"line"`
	Key    string `json:"key,omitempty"`
	Reason string `json:"reason"`
}

// Value is one setting as the file holds it.
type Value struct {
	Key  string
	Line int
	Str  string
	Int  int
	Num  float64
	Bool bool
}

// Entry is one category's settings. Values holds only the keys that may apply; Warnings name
// what the file set and will not be applied; Errors make the whole entry unusable.
type Entry struct {
	Name     string
	Line     int
	Values   map[string]Value
	Warnings []Issue
	Errors   []Issue
}

// Has says whether the entry sets key.
func (e *Entry) Has(key string) bool { _, ok := e.Values[key]; return ok }

// File is a parsed file. Errors are about the file as a whole and make every entry unusable.
type File struct {
	Categories map[string]*Entry
	Errors     []Issue
}

// Usable says whether the entry for name may be compared: the file and the entry are both free
// of errors.
func (f File) Usable(name string) bool {
	e := f.Categories[name]
	return len(f.Errors) == 0 && e != nil && len(e.Errors) == 0
}

var keyType = func() map[string]Type {
	m := make(map[string]Type, len(Keys))
	for _, k := range Keys {
		m[k.Name] = k.Type
	}
	return m
}()

// Parse reads a file's content. It never fails: what is wrong is in the Errors it returns.
func Parse(b []byte) File {
	f := File{Categories: map[string]*Entry{}}
	fail := func(line int, key, format string, args ...any) {
		f.Errors = append(f.Errors, Issue{Line: line, Key: key, Reason: fmt.Sprintf(format, args...)})
	}
	if len(b) > MaxBytes {
		fail(0, "", "the file is larger than %d bytes", MaxBytes)
		return f
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			fail(0, "", "the file is empty")
		} else {
			line, reason := yamlErrLine(err)
			fail(line, "", "%s", reason)
		}
		return f
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		fail(extra.Line, "", "the file holds more than one YAML document")
		return f
	}
	root := deref(&doc)
	if root.Kind == yaml.DocumentNode && len(root.Content) == 1 {
		root = deref(root.Content[0])
	}
	if !isMap(root) {
		fail(root.Line, "", "the file must be a mapping with version and categories")
		return f
	}
	var version, categories *yaml.Node
	seen := map[string]bool{}
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], deref(root.Content[i+1])
		key, msg := mapKey(k)
		if msg != "" {
			fail(k.Line, "", "%s", msg)
			continue
		}
		if seen[key] {
			fail(k.Line, key, "%s appears twice", key)
			continue
		}
		seen[key] = true
		switch key {
		case "version":
			version = v
		case "categories":
			categories = v
		default:
			fail(k.Line, key, "unknown key %q (this file knows version and categories)", key)
		}
	}
	switch {
	case version == nil:
		fail(root.Line, "version", "version is missing (write version: %d)", Version)
	case version.Kind != yaml.ScalarNode || version.Tag != "!!int" || version.Value != strconv.Itoa(Version):
		// The value is not repeated: whatever was pasted there stays out of the answer.
		fail(version.Line, "version", "this version is not supported (this Control Plane reads version %d)", Version)
	}
	if categories == nil {
		fail(root.Line, "categories", "categories is missing")
		return f
	}
	if categories.Kind == yaml.ScalarNode && categories.Tag == "!!null" {
		return f
	}
	if !isMap(categories) {
		fail(categories.Line, "categories", "categories must map each category name to its settings")
		return f
	}
	// An alias makes many categories share one settings node. Each node is read once and its
	// result shared, so a small file of aliases cannot multiply the work or the findings.
	parsed := map[*yaml.Node]*Entry{}
	for i := 0; i+1 < len(categories.Content); i += 2 {
		k, v := categories.Content[i], deref(categories.Content[i+1])
		name, msg := mapKey(k)
		if msg == "" && strings.TrimSpace(name) == "" {
			msg = "a category name must not be empty"
		}
		if msg != "" {
			fail(k.Line, "", "%s", msg)
			continue
		}
		if _, dup := f.Categories[name]; dup {
			fail(k.Line, "", "category %q appears twice", name)
			continue
		}
		shared, ok := parsed[v]
		if !ok {
			shared = parseEntry(v)
			parsed[v] = shared
		}
		e := *shared
		e.Name, e.Line = name, k.Line
		f.Categories[name] = &e
	}
	return f
}

// isMap accepts a plain mapping: a custom tag would give the mapping a meaning this parser
// does not know.
func isMap(n *yaml.Node) bool { return n.Kind == yaml.MappingNode && n.Tag == "!!map" }

// mapKey reads a mapping key. msg is "" when it is a plain string key; it never repeats a key
// that looks like a credential.
func mapKey(k *yaml.Node) (string, string) {
	k = deref(k)
	switch {
	case k.Kind != yaml.ScalarNode:
		return "", "a key must be a plain string"
	case k.Tag == "!!merge":
		return "", "merge keys (<<) are not supported: write the settings out, or alias a whole entry"
	case k.Tag != "!!str":
		return "", "a key must be a string (quote it)"
	case credentialRule(k.Value) != "":
		return "", "a key looks like a credential: the file holds names only, never a secret"
	}
	return k.Value, ""
}

// parseEntry reads one category's settings node; the caller sets Name and Line.
func parseEntry(v *yaml.Node) *Entry {
	e := &Entry{Values: map[string]Value{}}
	bad := func(line int, key, format string, args ...any) {
		e.Errors = append(e.Errors, Issue{Line: line, Key: key, Reason: fmt.Sprintf(format, args...)})
	}
	if v.Kind == yaml.ScalarNode && v.Tag == "!!null" {
		return e
	}
	if !isMap(v) {
		bad(v.Line, "", "a category's settings must be a mapping")
		return e
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(v.Content); i += 2 {
		k, n := v.Content[i], deref(v.Content[i+1])
		key, msg := mapKey(k)
		if msg != "" {
			bad(k.Line, "", "%s", msg)
			continue
		}
		if seen[key] {
			bad(k.Line, key, "%s appears twice", key)
			continue
		}
		seen[key] = true
		if why, ok := consoleOnly[key]; ok {
			e.Warnings = append(e.Warnings, Issue{Line: k.Line, Key: key,
				Reason: key + " is set in the Console only (" + why + "); the file's value is not applied"})
			continue
		}
		typ, ok := keyType[key]
		if !ok {
			bad(k.Line, key, "unknown key %q", key)
			continue
		}
		val, err := scalar(key, typ, n)
		if err != "" {
			bad(n.Line, key, "%s", err)
			continue
		}
		val.Line = k.Line
		if rule := credentialRule(val.Str); rule != "" {
			bad(n.Line, key, "%s looks like a credential (%s): the file holds names only, never a secret", key, rule)
			continue
		}
		if c, ok := consoleOnlyValue[key]; ok && val.Bool == c.value {
			e.Warnings = append(e.Warnings, Issue{Line: k.Line, Key: key,
				Reason: fmt.Sprintf("%s: %t is set in the Console only (%s); the file's value is not applied", key, c.value, c.reason)})
			continue
		}
		if key == "instruction_file" {
			if msg := checkRepoPath(val.Str); msg != "" {
				bad(n.Line, key, "%s", msg)
				continue
			}
		}
		e.Values[key] = val
	}
	if e.Has("instruction") && e.Has("instruction_file") {
		bad(e.Values["instruction_file"].Line, "instruction_file", "set instruction or instruction_file, not both")
	}
	return e
}

// scalar reads one value of its key's type. The message is "" when it reads.
func scalar(key string, typ Type, n *yaml.Node) (Value, string) {
	v := Value{Key: key}
	if n.Kind != yaml.ScalarNode {
		return v, key + " must be a single value"
	}
	switch typ {
	case String:
		if n.Tag != "!!str" {
			return v, key + " must be a string (quote it)"
		}
		v.Str = n.Value
	case Int:
		var i int32
		if n.Tag != "!!int" {
			return v, key + " must be a whole number"
		}
		if err := n.Decode(&i); err != nil {
			return v, key + " is out of range"
		}
		v.Int = int(i)
	case Number:
		if n.Tag != "!!int" && n.Tag != "!!float" {
			return v, key + " must be a number"
		}
		if err := n.Decode(&v.Num); err != nil || math.IsNaN(v.Num) || math.IsInf(v.Num, 0) {
			return v, key + " must be a finite number"
		}
		// A value too small for a float reads as 0, which is "no cap": refuse it rather than
		// turn an intended limit into none.
		if v.Num == 0 && nonZeroMantissa(n.Value) {
			return v, key + " is too small to be told from 0"
		}
	case Bool:
		if n.Tag != "!!bool" || n.Decode(&v.Bool) != nil {
			return v, key + " must be true or false"
		}
	}
	return v, ""
}

// checkRepoPath accepts a relative path that stays inside the repository.
func checkRepoPath(p string) string {
	switch {
	case p == "":
		return "instruction_file is empty"
	case strings.HasPrefix(p, "/") || strings.Contains(p, "\\"):
		return "instruction_file must be a relative path inside the repository, with / as the separator"
	case path.Clean(p) != p || p == "." || p == ".." || strings.HasPrefix(p, "../"):
		return "instruction_file must be a clean path inside the repository (no .., no ./, no trailing /)"
	}
	return ""
}

// nonZeroMantissa says whether a YAML number's digits before its exponent hold anything but 0.
func nonZeroMantissa(lit string) bool {
	m := strings.ToLower(lit)
	if i := strings.IndexByte(m, 'e'); i >= 0 && !strings.HasPrefix(m, "0x") {
		m = m[:i]
	}
	return strings.ContainsAny(strings.TrimPrefix(m, "0x"), "123456789abcdef")
}

func deref(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	return n
}

var yamlLineRe = regexp.MustCompile(`^yaml: line (\d+): (.*)$`)

func yamlErrLine(err error) (int, string) {
	msg := err.Error()
	if m := yamlLineRe.FindStringSubmatch(msg); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n, m[2]
	}
	return 0, strings.TrimPrefix(msg, "yaml: ")
}

// credentialRules are high-signature shapes only: an instruction is prose, and a rule that could
// match prose by accident would refuse an honest file. The list follows the Agent's memory scan
// (workspace/agent/internal/memoryx/memory_secrets.go).
var credentialRules = []struct {
	name string
	re   *regexp.Regexp
}{
	{"aws-access-key-id", regexp.MustCompile(`\b(?:A3T[A-Z0-9]|AKIA|ABIA|ACCA|ASIA)[0-9A-Z]{16}\b`)},
	{"github-token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`)},
	{"github-fine-grained-pat", regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{60,}\b`)},
	{"gitlab-token", regexp.MustCompile(`\bglpat-[A-Za-z0-9_\-]{20,}\b`)},
	{"slack-token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9\-]{10,}\b`)},
	{"anthropic-api-key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_\-]{24,}`)},
	{"openai-api-key", regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9_\-]{32,}\b`)},
	{"google-api-key", regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`)},
	{"private-key-block", regexp.MustCompile(`-----BEGIN (?:RSA |DSA |EC |OPENSSH |PGP )?PRIVATE KEY(?: BLOCK)?-----`)},
	{"json-web-token", regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{10,}\.eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}`)},
	{"url-basic-auth", regexp.MustCompile(`\b[a-zA-Z][a-zA-Z0-9+.\-]*://[^\s/:@]+:[^\s/@"']{8,}@[^\s/"']+`)},
	// Quotes and a 16-plus-character value are required so prose like "token: usage count" passes.
	{"secret-assignment", regexp.MustCompile(`(?i)\b(?:api[_\-]?key|secret[_\-]?key|access[_\-]?token|auth[_\-]?token|client[_\-]?secret|password|passwd)\b\s*[:=]\s*["'` + "`" + `]([^"'` + "`" + `\s]{16,})["'` + "`" + `]`)},
}

// credentialRule names the first rule s matches, "" for none.
func credentialRule(s string) string {
	if s == "" {
		return ""
	}
	for _, r := range credentialRules {
		if r.re.MatchString(s) {
			return r.name
		}
	}
	return ""
}
