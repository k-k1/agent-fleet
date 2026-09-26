package awsx

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The AWS CLI reads its files with Python's configparser and then maps section names to
// profiles with botocore's build_profile_map. Every guard here that asks "what does the
// CLI think profile X is" has to read the files the same way; each divergence found so
// far (":" delimiters, [DEFAULT], quoted headers) was a way to be wrong about which
// account a name means. TestProfileKeysAgreesWithTheRealAWSCLI holds this to the real CLI.

// iniLine is one meaningful line of an INI file: a section header, or a top-level key.
type iniLine struct {
	header     bool
	section    string // the raw section name the line belongs to (or opens)
	key, value string // key lower-cased as configparser's optionxform does
	cont       bool   // re-reported after a continuation line, not a new key
}

// scanINI walks text the way configparser does: lines are stripped before matching; a
// section header is "[" + name + "]" with the name running to the last "]"; comments
// start with # or ; after stripping; a line indented deeper than the key above it
// continues that key's value (s3 = / nested settings), joined with a newline, and is
// never a key or header of its own; a key is split at the first "=" or ":". A key is
// reported again, with its value so far, after each continuation line.
func scanINI(text string, fn func(iniLine)) {
	section := ""
	optIndent := -1 // indent of the last key, -1 when there is none to continue
	var last iniLine
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	for _, raw := range strings.Split(text, "\n") {
		// Python strips and measures indent on Unicode whitespace: a line led by a
		// no-break space (pasted from a web page) continues the value above it.
		t := strings.TrimFunc(raw, unicode.IsSpace)
		if t == "" || t[0] == '#' || t[0] == ';' {
			continue
		}
		// In characters, as Python counts: a no-break space is two bytes but one column.
		indent := utf8.RuneCountInString(raw) - utf8.RuneCountInString(strings.TrimLeftFunc(raw, unicode.IsSpace))
		if optIndent >= 0 && indent > optIndent {
			if last.value == "" {
				last.value = t
			} else {
				last.value += "\n" + t
			}
			last.cont = true
			fn(last)
			continue
		}
		if t[0] == '[' {
			if j := strings.LastIndexByte(t, ']'); j > 1 {
				section = t[1:j]
				optIndent = -1
				fn(iniLine{header: true, section: section})
				continue
			}
		}
		i := strings.IndexAny(t, "=:")
		if i < 0 {
			continue
		}
		optIndent = indent
		last = iniLine{section: section, key: strings.ToLower(strings.TrimSpace(t[:i])), value: strings.TrimSpace(t[i+1:])}
		fn(last)
	}
}

// configSection maps a config-file section name to what botocore makes of it: a
// profile ("default", or anything starting with "profile" that shlex-splits into two
// words — so [profile "prod"] and [profile 'prod'] are profile prod), an sso-session
// (same rule), or neither.
func configSection(name string) (kind, profile string) {
	if name == "default" {
		return "profile", "default"
	}
	for _, k := range []string{"profile", "sso-session"} {
		if strings.HasPrefix(name, k) {
			if parts, err := shlexSplit(name); err == nil && len(parts) == 2 {
				return k, parts[1]
			}
			return "", ""
		}
	}
	return "", ""
}

// readINISection collects the keys of the section that match picks in the file at path,
// with configparser's [DEFAULT] keys under them. When several sections match, the last
// one replaces the others, as botocore's profile map does. A file the AWS CLI itself
// refuses to parse is an error here too: acting on our reading of a file the CLI rejects
// would be guessing.
func readINISection(path string, pick func(section string) bool, keys map[string]string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	if err := iniStrict(string(b)); err != nil {
		return fmt.Errorf("the AWS CLI cannot read %s: %w", path, err)
	}
	defaults := map[string]string{}
	var sect map[string]string
	scanINI(string(b), func(l iniLine) {
		switch {
		case l.header && pick(l.section):
			sect = map[string]string{}
		case l.header:
		case l.section == "DEFAULT":
			if l.value != "" {
				defaults[l.key] = l.value
			}
		case sect != nil && pick(l.section) && l.value != "":
			sect[l.key] = l.value
		}
	})
	if sect == nil {
		return nil
	}
	for k, v := range defaults {
		keys[k] = v
	}
	for k, v := range sect {
		keys[k] = v
	}
	return nil
}

// iniStrict reports what configparser's strict mode (the CLI's) rejects: a section
// name that appears twice ([DEFAULT] may repeat) and a key given twice in one section
// (measured with aws-cli 2.36.46: "Unable to parse config file", exit 255).
func iniStrict(text string) error {
	sections := map[string]bool{}
	options := map[[2]string]bool{}
	var err error
	scanINI(text, func(l iniLine) {
		switch {
		case err != nil || l.cont:
		case l.header:
			if sections[l.section] && l.section != "DEFAULT" {
				err = fmt.Errorf("section [%s] appears twice", l.section)
			}
			sections[l.section] = true
		default:
			k := [2]string{l.section, l.key}
			if options[k] {
				err = fmt.Errorf("%s is set twice in [%s]", l.key, l.section)
			}
			options[k] = true
		}
	})
	return err
}

// configPicker selects the config-file sections botocore reads as profile (or
// sso-session) name.
func configPicker(kind, name string) func(string) bool {
	return func(section string) bool {
		k, n := configSection(section)
		return k == kind && n == name
	}
}

// credentialsPicker selects the credentials-file section for profile name: there the
// section name is the profile name verbatim.
func credentialsPicker(name string) func(string) bool {
	return func(section string) bool { return section == name }
}

var errShlex = errors.New("unbalanced quotes")

// shlexSplit is Python's shlex.split in its default POSIX mode, for section names:
// whitespace separates words, single quotes are literal, double quotes allow \ to escape
// \ " $ ` and newline, and a backslash outside quotes escapes the next character.
func shlexSplit(s string) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		case c == '\'':
			inWord = true
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil, errShlex
			}
			cur.WriteString(s[i+1 : i+1+j])
			i += j + 1
		case c == '"':
			inWord = true
			i++
			for ; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("\\\"$`\n", s[i+1]) >= 0 {
					i++
				}
				cur.WriteByte(s[i])
			}
			if i >= len(s) {
				return nil, errShlex
			}
		case c == '\\':
			inWord = true
			if i+1 >= len(s) {
				return nil, errShlex
			}
			i++
			cur.WriteByte(s[i])
		default:
			inWord = true
			cur.WriteByte(c)
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}
