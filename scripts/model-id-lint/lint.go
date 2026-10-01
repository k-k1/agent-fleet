package main

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Directives. A literal on the directive's own line or the line after it is allowed; the
// reason after the directive is required, because an allow without one is exactly the kind
// of silent exemption this lint exists to prevent.
const (
	allowDirective     = "model-id-lint:allow"
	allowFileDirective = "model-id-lint:allow-file"
)

// Status says why a finding is or is not a violation.
type Status string

const (
	StatusViolation Status = "violation"
	StatusRegistry  Status = "registry"
	StatusAllowed   Status = "allowed"
	StatusFile      Status = "allowed-file"
)

// Finding is one model-id-shaped string literal.
type Finding struct {
	Path    string // slash-separated, relative to the repository root
	Line    int
	Literal string // the unquoted value, shortened for display
	Match   string // the part the pattern matched
	Status  Status
	Reason  string // the directive's reason when Status is allowed / allowed-file
}

func (f Finding) String() string {
	s := fmt.Sprintf("%s:%d: %q (matched %q)", f.Path, f.Line, f.Literal, f.Match)
	if f.Reason != "" {
		s += " — " + f.Reason
	}
	return s
}

// LoadPattern reads patterns.txt into the combined expression both linters use.
func LoadPattern(path string) (*regexp.Regexp, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var alts []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		alts = append(alts, line)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(alts) == 0 {
		return nil, fmt.Errorf("%s: no patterns", path)
	}
	return regexp.Compile(`\b(?:` + strings.Join(alts, "|") + `)`)
}

// Linter scans Go sources for model-id-shaped string literals.
type Linter struct {
	Pattern *regexp.Regexp
	// Registry holds the files (relative to Root, slash-separated) that own the fallbacks;
	// literals there are reported as StatusRegistry, never as violations.
	Registry map[string]bool
	Root     string
}

// skipDir names directories that hold no product code: fixtures, vendored or installed
// dependencies.
func skipDir(name string) bool {
	switch name {
	case "testdata", "vendor", "node_modules", ".git":
		return true
	}
	return false
}

// Scan walks every dir (relative to Root) and returns the findings in path/line order.
func (l *Linter) Scan(dirs ...string) ([]Finding, []error) {
	var out []Finding
	var errs []error
	for _, d := range dirs {
		err := filepath.WalkDir(filepath.Join(l.Root, d), func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() {
				if skipDir(e.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			fs, ferrs := l.scanFile(p)
			out = append(out, fs...)
			errs = append(errs, ferrs...)
			return nil
		})
		if err != nil {
			errs = append(errs, err)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Line < out[j].Line
	})
	return out, errs
}

func (l *Linter) scanFile(path string) ([]Finding, []error) {
	rel, err := filepath.Rel(l.Root, path)
	if err != nil {
		return nil, []error{err}
	}
	rel = filepath.ToSlash(rel)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, []error{err}
	}
	if ast.IsGenerated(file) {
		return nil, nil
	}

	var errs []error
	allowed := map[int]string{} // line -> reason
	fileReason := ""
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			text := c.Text
			line := fset.Position(c.Pos()).Line
			if i := strings.Index(text, allowFileDirective); i >= 0 {
				reason := directiveReason(text[i+len(allowFileDirective):])
				if reason == "" {
					errs = append(errs, fmt.Errorf("%s:%d: %s needs a reason", rel, line, allowFileDirective))
					continue
				}
				fileReason = reason
				continue
			}
			if i := strings.Index(text, allowDirective); i >= 0 {
				reason := directiveReason(text[i+len(allowDirective):])
				if reason == "" {
					errs = append(errs, fmt.Errorf("%s:%d: %s needs a reason", rel, line, allowDirective))
					continue
				}
				end := fset.Position(c.End()).Line
				allowed[end] = reason
				allowed[end+1] = reason
			}
		}
	}

	var out []Finding
	ast.Inspect(file, func(n ast.Node) bool {
		var val string
		switch e := n.(type) {
		case *ast.BasicLit:
			v, ok := foldString(e)
			if !ok {
				return true
			}
			val = v
		case *ast.BinaryExpr:
			// A constant concatenation is judged as the string it builds — otherwise
			// "gpt-" + "5.6-luna" pins an id with neither half matching. Its parts are not
			// visited again, so a matching half is reported once, on the whole.
			v, ok := foldString(e)
			if !ok {
				return true
			}
			val = v
		default:
			return true
		}
		m := l.Pattern.FindString(val)
		if m == "" {
			return false
		}
		line := fset.Position(n.Pos()).Line
		f := Finding{Path: rel, Line: line, Literal: shorten(val, m), Match: m, Status: StatusViolation}
		switch {
		case l.Registry[rel]:
			f.Status = StatusRegistry
		case allowed[line] != "":
			f.Status, f.Reason = StatusAllowed, allowed[line]
		case fileReason != "":
			f.Status, f.Reason = StatusFile, fileReason
		}
		out = append(out, f)
		return false
	})
	return out, errs
}

// foldString evaluates e when it is built from string literals alone: a literal, or literals
// joined by + (parentheses allowed). ok is false for anything that needs a value at run time.
func foldString(e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(e.Value)
		return v, err == nil
	case *ast.ParenExpr:
		return foldString(e.X)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		x, ok := foldString(e.X)
		if !ok {
			return "", false
		}
		y, ok := foldString(e.Y)
		return x + y, ok
	}
	return "", false
}

// directiveReason is the text after a directive, with the separators people naturally put
// there trimmed off.
func directiveReason(s string) string {
	s = strings.TrimSuffix(strings.TrimSpace(s), "*/")
	return strings.TrimSpace(strings.TrimLeft(s, ":—- "))
}

// shorten keeps a long literal (a tool description) readable: the match with a little
// context either side.
func shorten(val, match string) string {
	const ctx = 30
	if len(val) <= 2*ctx+len(match) {
		return val
	}
	i := strings.Index(val, match)
	start, end := max(0, i-ctx), min(len(val), i+len(match)+ctx)
	for start > 0 && !utf8.RuneStart(val[start]) {
		start--
	}
	for end < len(val) && !utf8.RuneStart(val[end]) {
		end++
	}
	s := val[start:end]
	if start > 0 {
		s = "…" + s
	}
	if end < len(val) {
		s += "…"
	}
	return s
}
