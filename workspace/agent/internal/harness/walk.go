package harness

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
)

// skipDirNames are directories a repo-wide glob/grep walk never descends into:
// large, machine-generated, or (for .git) binary-packed in a way that makes
// "grep every byte" both slow and useless.
var skipDirNames = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, ".hg": true, ".svn": true,
}

// walkFiles walks root (already resolved+confined by the caller via
// resolvePath), calling fn with each regular file's absolute path and its
// slash-separated path relative to root. An unreadable entry is skipped rather
// than aborting the whole walk — a single permission-denied subdirectory should
// not make glob/grep fail outright.
func walkFiles(root string, fn func(full, rel string, d fs.DirEntry) error) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && skipDirNames[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == "." {
			return nil
		}
		return fn(path, rel, d)
	})
}

// globToRegexp translates a doublestar-style glob (the "**" convention every
// modern file-glob tool understands, absent from Go's own path/filepath.Match)
// into an anchored regexp matched against a slash-separated relative path. Glob
// matching is not a security judgement (unlike cwd confinement, cwd.go) so
// writing a small translator here does not repeat a decision ADR 0081 warns
// against — there is simply no such helper already in this repo to reuse.
func globToRegexp(pattern string) (*regexp.Regexp, error) {
	pattern = strings.TrimPrefix(pattern, "./")
	var b strings.Builder
	b.WriteString("^")
	runes := []rune(pattern)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case c == '*' && i+1 < len(runes) && runes[i+1] == '*':
			i++ // consume the second '*'
			if i+1 < len(runes) && runes[i+1] == '/' {
				i++ // also consume the following '/': "**/x" matches "x" too
				b.WriteString("(?:.*/)?")
			} else {
				b.WriteString(".*")
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		case c == '[':
			j := i + 1
			for j < len(runes) && runes[j] != ']' {
				j++
			}
			if j < len(runes) {
				b.WriteString(string(runes[i : j+1]))
				i = j
			} else {
				b.WriteString(`\[`)
			}
		case strings.ContainsRune(`.+()|^$\{}`, c):
			b.WriteString(regexp.QuoteMeta(string(c)))
		default:
			b.WriteRune(c)
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// looksBinary is a cheap heuristic (a NUL byte in the sampled prefix) matching
// what git/grep/most editors use to decide "do not try to show this as text".
func looksBinary(sample []byte) bool {
	n := len(sample)
	if n > 8000 {
		n = 8000
	}
	for i := 0; i < n; i++ {
		if sample[i] == 0 {
			return true
		}
	}
	return false
}
