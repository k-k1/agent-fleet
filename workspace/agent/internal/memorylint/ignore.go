package memorylint

import (
	"regexp"
	"strings"
)

// ignoreRule is one parsed .gitignore line.
type ignoreRule struct {
	re      *regexp.Regexp // matched against the path relative to the .gitignore's directory
	negate  bool
	dirOnly bool
}

// ignoreFile is the rules of one .gitignore blob and the directory it governs ("" = root).
type ignoreFile struct {
	dir   string
	rules []ignoreRule
}

// ignoreSet answers whether a revision's committed .gitignore files ignore a path. It reads no
// working tree, no .git/info/exclude and no global excludes: the answer is a property of the
// revision alone.
type ignoreSet struct {
	files []ignoreFile // shallowest first
}

// parseIgnore parses the text of one .gitignore (gitignore(5)).
func parseIgnore(text string) []ignoreRule {
	var out []ignoreRule
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		// Trailing spaces are dropped unless escaped with a backslash.
		for strings.HasSuffix(line, " ") && !strings.HasSuffix(line, "\\ ") {
			line = line[:len(line)-1]
		}
		if line == "" || line[0] == '#' {
			continue
		}
		var r ignoreRule
		if line[0] == '!' {
			r.negate = true
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			r.dirOnly = true
			line = strings.TrimRight(line, "/")
		}
		if line == "" {
			continue
		}
		anchored := strings.Contains(line, "/")
		line = strings.TrimPrefix(line, "/")
		re, err := regexp.Compile(globToRegex(line, anchored))
		if err != nil {
			continue
		}
		r.re = re
		out = append(out, r)
	}
	return out
}

// globToRegex translates one gitignore pattern. A pattern with no slash matches at any depth.
func globToRegex(p string, anchored bool) string {
	var b strings.Builder
	b.WriteString("^")
	if !anchored {
		b.WriteString("(?:.*/)?")
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch c {
		case '*':
			if i+1 < len(p) && p[i+1] == '*' {
				atStart := i == 0 || p[i-1] == '/'
				if atStart && i+2 < len(p) && p[i+2] == '/' {
					b.WriteString("(?:.*/)?") // "**/"
					i += 2
				} else if atStart && i+2 == len(p) {
					b.WriteString(".*") // trailing "/**"
					i++
				} else {
					b.WriteString("[^/]*")
					i++
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '[':
			j := i + 1
			if j < len(p) && (p[j] == '!' || p[j] == '^') {
				j++
			}
			if j < len(p) && p[j] == ']' {
				j++
			}
			for j < len(p) && p[j] != ']' {
				j++
			}
			if j >= len(p) {
				b.WriteString(`\[`)
				continue
			}
			class := p[i+1 : j]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			b.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			i = j
		case '\\':
			if i+1 < len(p) {
				i++
				b.WriteString(regexp.QuoteMeta(string(p[i])))
			}
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return b.String()
}

// ignored reports whether path (slash-separated, relative to the root) is ignored. isDir says
// whether its last component is a directory. A path under an ignored directory is ignored: git
// never re-includes below one.
func (s *ignoreSet) ignored(path string, isDir bool) bool {
	if s == nil || len(s.files) == 0 {
		return false
	}
	parts := strings.Split(path, "/")
	for i := range parts {
		prefix := strings.Join(parts[:i+1], "/")
		if s.matches(prefix, i < len(parts)-1 || isDir) {
			return true
		}
	}
	return false
}

// matches is the verdict of the last rule that matches one path: deeper files override shallower
// ones, and a later line overrides an earlier one.
func (s *ignoreSet) matches(path string, isDir bool) bool {
	verdict := false
	for _, f := range s.files {
		rel := path
		if f.dir != "" {
			r, ok := strings.CutPrefix(path, f.dir+"/")
			if !ok {
				continue
			}
			rel = r
		}
		for _, r := range f.rules {
			if r.dirOnly && !isDir {
				continue
			}
			if r.re.MatchString(rel) {
				verdict = !r.negate
			}
		}
	}
	return verdict
}
