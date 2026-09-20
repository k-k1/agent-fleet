package harness

// tools_fs.go implements the read/write/edit/glob/grep/ls builtins §4.6 of
// docs/log/99 lists for segment E. Every path argument goes through resolvePath
// (cwd.go) before touching the filesystem — see that file's doc comment for why
// this is the same judgement as workspace/agent/fs.go's browse-root confinement,
// pulled into internal/pathguard rather than re-derived here.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	defaultReadLimitLines = 2000
	maxGlobMatches        = 500
	maxGrepMatches        = 200
	maxGrepFileBytes      = 2 << 20 // skip scanning a single file bigger than this
)

// --- read ------------------------------------------------------------------

type readArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

var readToolDef = ToolDef{
	Name:        "read",
	Description: "Read a text file (or a line range of one) from the working directory.",
	Parameters: json.RawMessage(`{
		"type":"object",
		"properties":{
			"path":{"type":"string","description":"File path, relative to the working directory."},
			"offset":{"type":"integer","description":"0-based line number to start from (default 0)."},
			"limit":{"type":"integer","description":"Maximum number of lines to return (default 2000)."}
		},
		"required":["path"]
	}`),
}

func runRead(_ context.Context, rt *Runtime, argsJSON string) (string, error) {
	var a readArgs
	if err := decodeArgs(argsJSON, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	full, err := resolvePath(rt.Cwd, a.Path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(full)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory, not a file (use ls)", a.Path)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	if looksBinary(data) {
		return "", fmt.Errorf("%s looks like a binary file", a.Path)
	}
	lines := strings.Split(string(data), "\n")
	offset := a.Offset
	if offset < 0 {
		offset = 0
	}
	limit := a.Limit
	if limit <= 0 {
		limit = defaultReadLimitLines
	}
	if offset >= len(lines) {
		return fmt.Sprintf("(%s has %d lines; offset %d is past the end)", a.Path, len(lines), offset), nil
	}
	end := offset + limit
	if end > len(lines) {
		end = len(lines)
	}
	var b strings.Builder
	for i := offset; i < end; i++ {
		fmt.Fprintf(&b, "%6d\t%s\n", i+1, lines[i])
	}
	return b.String(), nil
}

// --- write -------------------------------------------------------------------

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

var writeToolDef = ToolDef{
	Name:        "write",
	Description: "Create or overwrite a file in the working directory with the given content.",
	Parameters: json.RawMessage(`{
		"type":"object",
		"properties":{
			"path":{"type":"string","description":"File path, relative to the working directory."},
			"content":{"type":"string","description":"The full content to write."}
		},
		"required":["path","content"]
	}`),
}

func runWrite(_ context.Context, rt *Runtime, argsJSON string) (string, error) {
	var a writeArgs
	if err := decodeArgs(argsJSON, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	full, err := resolvePath(rt.Cwd, a.Path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(full, []byte(a.Content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(a.Content), a.Path), nil
}

// --- edit ----------------------------------------------------------------

type editArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

var editToolDef = ToolDef{
	Name:        "edit",
	Description: "Replace an exact substring in a file. Fails if old_string is not found, or (unless replace_all) if it is not unique.",
	Parameters: json.RawMessage(`{
		"type":"object",
		"properties":{
			"path":{"type":"string","description":"File path, relative to the working directory."},
			"old_string":{"type":"string","description":"Exact text to find. Must be unique in the file unless replace_all is set."},
			"new_string":{"type":"string","description":"Text to replace it with."},
			"replace_all":{"type":"boolean","description":"Replace every occurrence instead of requiring exactly one."}
		},
		"required":["path","old_string","new_string"]
	}`),
}

func runEdit(_ context.Context, rt *Runtime, argsJSON string) (string, error) {
	var a editArgs
	if err := decodeArgs(argsJSON, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	if a.OldString == "" {
		return "", fmt.Errorf("old_string must not be empty")
	}
	if a.OldString == a.NewString {
		return "", fmt.Errorf("old_string and new_string are identical")
	}
	full, err := resolvePath(rt.Cwd, a.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	content := string(data)
	count := strings.Count(content, a.OldString)
	switch {
	case count == 0:
		return "", fmt.Errorf("old_string not found in %s", a.Path)
	case count > 1 && !a.ReplaceAll:
		return "", fmt.Errorf("old_string is not unique in %s (%d matches); add context or set replace_all", a.Path, count)
	}
	var updated string
	if a.ReplaceAll {
		updated = strings.ReplaceAll(content, a.OldString, a.NewString)
	} else {
		updated = strings.Replace(content, a.OldString, a.NewString, 1)
	}
	if err := os.WriteFile(full, []byte(updated), 0o644); err != nil {
		return "", err
	}
	if a.ReplaceAll {
		return fmt.Sprintf("replaced %d occurrence(s) in %s", count, a.Path), nil
	}
	return fmt.Sprintf("edited %s", a.Path), nil
}

// --- ls ------------------------------------------------------------------

type lsArgs struct {
	Path string `json:"path,omitempty"`
}

var lsToolDef = ToolDef{
	Name:        "ls",
	Description: "List a directory's immediate contents (name, type, size).",
	Parameters: json.RawMessage(`{
		"type":"object",
		"properties":{
			"path":{"type":"string","description":"Directory path, relative to the working directory (default: the working directory itself)."}
		}
	}`),
}

func runLs(_ context.Context, rt *Runtime, argsJSON string) (string, error) {
	var a lsArgs
	if err := decodeArgs(argsJSON, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	p := a.Path
	if p == "" {
		p = "."
	}
	full, err := resolvePath(rt.Cwd, p)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var b strings.Builder
	for _, e := range entries {
		if e.IsDir() {
			fmt.Fprintf(&b, "%s/\n", e.Name())
			continue
		}
		info, ierr := e.Info()
		size := int64(0)
		if ierr == nil {
			size = info.Size()
		}
		fmt.Fprintf(&b, "%s\t%s\n", e.Name(), strconv.FormatInt(size, 10)+"B")
	}
	if b.Len() == 0 {
		return "(empty directory)", nil
	}
	return b.String(), nil
}

// --- glob ----------------------------------------------------------------

type globArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
}

var globToolDef = ToolDef{
	Name:        "glob",
	Description: "Find files by name pattern (supports ** for any depth), rooted at the working directory or a subdirectory of it.",
	Parameters: json.RawMessage(`{
		"type":"object",
		"properties":{
			"pattern":{"type":"string","description":"Glob pattern, e.g. \"**/*.go\" or \"src/*.ts\"."},
			"path":{"type":"string","description":"Subdirectory to search from (default: the working directory)."}
		},
		"required":["pattern"]
	}`),
}

func runGlob(ctx context.Context, rt *Runtime, argsJSON string) (string, error) {
	var a globArgs
	if err := decodeArgs(argsJSON, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	base := a.Path
	if base == "" {
		base = "."
	}
	root, err := resolvePath(rt.Cwd, base)
	if err != nil {
		return "", err
	}
	re, err := globToRegexp(a.Pattern)
	if err != nil {
		return "", fmt.Errorf("invalid pattern: %w", err)
	}
	var matches []string
	truncated := false
	err = walkFiles(root, func(full, rel string, _ os.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !re.MatchString(rel) {
			return nil
		}
		if len(matches) >= maxGlobMatches {
			truncated = true
			return filepath.SkipAll
		}
		matches = append(matches, filepath.ToSlash(filepath.Join(base, rel)))
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(matches)
	if len(matches) == 0 {
		return "(no matches)", nil
	}
	out := strings.Join(matches, "\n")
	if truncated {
		out += fmt.Sprintf("\n... [stopped at %d matches]", maxGlobMatches)
	}
	return out, nil
}

// --- grep ------------------------------------------------------------------

type grepArgs struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path,omitempty"`
	Glob       string `json:"glob,omitempty"`
	IgnoreCase bool   `json:"ignore_case,omitempty"`
}

var grepToolDef = ToolDef{
	Name:        "grep",
	Description: "Search file contents with a regular expression, rooted at the working directory or a subdirectory of it.",
	Parameters: json.RawMessage(`{
		"type":"object",
		"properties":{
			"pattern":{"type":"string","description":"RE2 regular expression to search for."},
			"path":{"type":"string","description":"Subdirectory to search from (default: the working directory)."},
			"glob":{"type":"string","description":"Only search files whose relative path matches this glob."},
			"ignore_case":{"type":"boolean"}
		},
		"required":["pattern"]
	}`),
}

func runGrep(ctx context.Context, rt *Runtime, argsJSON string) (string, error) {
	var a grepArgs
	if err := decodeArgs(argsJSON, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	base := a.Path
	if base == "" {
		base = "."
	}
	root, err := resolvePath(rt.Cwd, base)
	if err != nil {
		return "", err
	}
	pat := a.Pattern
	if a.IgnoreCase {
		pat = "(?i)" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return "", fmt.Errorf("invalid pattern: %w", err)
	}
	var globRe *regexp.Regexp
	if a.Glob != "" {
		globRe, err = globToRegexp(a.Glob)
		if err != nil {
			return "", fmt.Errorf("invalid glob: %w", err)
		}
	}

	var out []string
	truncated := false
	err = walkFiles(root, func(full, rel string, _ os.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(out) >= maxGrepMatches {
			truncated = true
			return filepath.SkipAll
		}
		if globRe != nil && !globRe.MatchString(rel) {
			return nil
		}
		info, ierr := os.Stat(full)
		if ierr != nil || info.Size() > maxGrepFileBytes {
			return nil
		}
		data, rerr := os.ReadFile(full)
		if rerr != nil || looksBinary(data) {
			return nil
		}
		displayPath := filepath.ToSlash(filepath.Join(base, rel))
		for i, line := range strings.Split(string(data), "\n") {
			if len(out) >= maxGrepMatches {
				truncated = true
				return filepath.SkipAll
			}
			if re.MatchString(line) {
				out = append(out, fmt.Sprintf("%s:%d:%s", displayPath, i+1, line))
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(out) == 0 {
		return "(no matches)", nil
	}
	result := strings.Join(out, "\n")
	if truncated {
		result += fmt.Sprintf("\n... [stopped at %d matches]", maxGrepMatches)
	}
	return result, nil
}
