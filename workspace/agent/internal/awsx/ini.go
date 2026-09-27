package awsx

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
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
	bad        bool   // neither a header nor a key: configparser's ParsingError
	line       int    // 1-based line number, for messages that must not quote content
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
	for n, raw := range strings.Split(text, "\n") {
		lineNo := n + 1
		// Python strips and measures indent on Unicode whitespace: a line led by a
		// no-break space (pasted from a web page) continues the value above it.
		t := strings.TrimFunc(raw, pySpace)
		if t == "" || t[0] == '#' || t[0] == ';' {
			continue
		}
		// In characters, as Python counts: a no-break space is two bytes but one column.
		indent := utf8.RuneCountInString(raw) - utf8.RuneCountInString(strings.TrimLeftFunc(raw, pySpace))
		if optIndent >= 0 && indent > optIndent {
			// Joined as configparser does, also after an empty first value: "region ="
			// followed by an indented line is "\neu-west-1", which botocore reads as a
			// nested map, not as the region (see scalar).
			last.value += "\n" + t
			last.cont, last.line = true, lineNo
			fn(last)
			continue
		}
		if t[0] == '[' {
			if j := strings.LastIndexByte(t, ']'); j > 1 {
				section = t[1:j]
				optIndent = -1
				fn(iniLine{header: true, section: section, line: lineNo})
				continue
			}
		}
		i := strings.IndexAny(t, "=:")
		key := pyLower(strings.TrimFunc(t[:max(i, 0)], pySpace))
		if i < 0 || key == "" {
			// No delimiter, or nothing before it ("= v"): configparser's ParsingError.
			fn(iniLine{section: section, bad: true, value: t, line: lineNo})
			continue
		}
		optIndent = indent
		last = iniLine{section: section, key: key, value: strings.TrimFunc(t[i+1:], pySpace), line: lineNo}
		fn(last)
	}
}

// pySpace is Python's str.isspace, which strip() and configparser's indent use: Go's
// unicode.IsSpace plus the C0 separators U+001C..U+001F. Leaving those out let a
// "\x1crole_arn = ..." line be a live key to the CLI and not to this reader.
func pySpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

// scalar is the value as a plain setting: "" when botocore would read it as a nested
// map (a value that starts on the line after its key) or it spans lines, neither of
// which the CLI uses as a region or an SSO field.
func scalar(v string) string {
	if strings.Contains(v, "\n") {
		return ""
	}
	return v
}

// pyLower is Python's str.lower, which configparser applies to key names: the full
// Unicode lowercase mapping, not strings.ToLower's simple one. They differ where it can
// change which key a line is: U+0130 (İ) lowers to "i" + U+0307 (so "regİon" is not
// region to the CLI), and a capital sigma ending a word lowers to final sigma. The
// language-neutral mapping of x/text/cases implements both, Final_Sigma's
// case-ignorable set included.
func pyLower(s string) string {
	if isASCII(s) {
		return strings.ToLower(s)
	}
	return cases.Lower(language.Und).String(s)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
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
	return readINISectionFrom(path, pick, keys, nil, "")
}

// readINISectionFrom is readINISection that also records, in origin (when not nil),
// where each key came from: "[DEFAULT]", the section's own header, or label when given
// (the credentials file). Messages use it so a user is sent to the line that set it.
func readINISectionFrom(path string, pick func(section string) bool, keys, origin map[string]string, label string) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		// Unreadable is not absent: a profile there could decide who the CLI runs as.
		return fmt.Errorf("cannot read %s: %w", path, err)
	}
	if err := iniStrict(string(b)); err != nil {
		return fmt.Errorf("the AWS CLI cannot read %s: %w", path, err)
	}
	defaults := map[string]string{}
	var sect map[string]string
	sectName := ""
	scanINI(string(b), func(l iniLine) {
		switch {
		case l.bad:
		case l.header && pick(l.section):
			sect, sectName = map[string]string{}, l.section
		case l.header:
		// Empty values are kept: `sso_account_id =` in a profile overrides the [DEFAULT]
		// one and leaves it empty, as the CLI reads it (skipping it let the default
		// account through, verified with aws-cli 2.36.46).
		case l.section == "DEFAULT":
			defaults[l.key] = l.value
		case sect != nil && pick(l.section):
			sect[l.key] = l.value
		}
	})
	if sect == nil {
		return nil
	}
	own := "[" + sectName + "]"
	if label != "" {
		own = label
	}
	for k, v := range defaults {
		keys[k] = v
		if origin != nil {
			origin[k] = "[DEFAULT]"
			if label != "" {
				origin[k] = "[DEFAULT] of " + label
			}
		}
	}
	for k, v := range sect {
		keys[k] = v
		if origin != nil {
			origin[k] = own
		}
	}
	return nil
}

// iniStrict reports what configparser's strict mode (the CLI's) rejects: a section
// name that appears twice ([DEFAULT] may repeat) and a key given twice in one section
// (measured with aws-cli 2.36.46: "Unable to parse config file", exit 255).
func iniStrict(text string) error {
	// The CLI decodes the files as UTF-8 without stripping a BOM: invalid bytes fail the
	// read, and a BOM leaves the first line as no header at all.
	if !utf8.ValidString(text) {
		return errors.New("it is not valid UTF-8")
	}
	if strings.HasPrefix(text, "\ufeff") {
		return errors.New("it starts with a byte-order mark (save it as UTF-8 without BOM)")
	}
	sections := map[string]bool{}
	options := map[[2]string]bool{}
	final := map[[2]string]string{}  // each key's value after its continuation lines
	firstLine := map[[2]string]int{} // where each key starts
	// Messages give line numbers, never line content: these files hold secret keys, and a
	// malformed line is as likely to be one as anything else.
	var err error
	seenHeader := false
	scanINI(text, func(l iniLine) {
		if !l.header && !l.bad {
			final[[2]string{l.section, l.key}] = l.value
			if !l.cont {
				firstLine[[2]string{l.section, l.key}] = l.line
			}
		}
		switch {
		case err != nil || l.cont:
		case l.bad:
			err = fmt.Errorf("line %d is neither a [section] nor a key = value", l.line)
		case !l.header && !seenHeader:
			err = fmt.Errorf("line %d sets a key before any [section]", l.line)
		case l.header:
			seenHeader = true
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
	if err != nil {
		return err
	}
	// botocore reads a value that starts on the next line as nested "k = v" lines and
	// fails the whole file on a line without "=" (measured: "region =" followed by an
	// indented "eu-west-1" is "Unable to parse config file").
	for k, v := range final {
		if !strings.HasPrefix(v, "\n") {
			continue
		}
		for _, line := range strings.Split(v, "\n") {
			if t := strings.TrimFunc(line, pySpace); t != "" && !strings.Contains(t, "=") {
				return fmt.Errorf("%s in [%s] (line %d) continues on the next line with a line that is not a key = value setting",
					k[1], k[0], firstLine[k])
			}
		}
	}
	return nil
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
