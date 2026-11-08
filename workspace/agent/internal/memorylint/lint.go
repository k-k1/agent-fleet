// Package memorylint is the deterministic, no-LLM lint of agent memories (ADR 0130 decision 5,
// step 1): it extracts anchors from each body on the fly and reports what no longer holds. It
// writes nothing and reads the repository only through the Repo it is given.
//
// It imports no other Agent Fleet package (TestImportsNoAFPackage): the engine of ADR 0130
// decision 8 is meant to move to its own module, and the Agent side only feeds it bodies, names
// and a repository reader.
package memorylint

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Check names, as they appear in a Finding and in the report's counts.
const (
	CheckBrokenLink   = "broken-link"    // [[name]] with no memory of that name
	CheckMissingPath  = "missing-path"   // a repository path absent from the default branch
	CheckMissingSym   = "missing-symbol" // a Test* or code-shaped symbol found nowhere in the default branch
	CheckMissingADR   = "missing-adr"    // an ADR number with no record under the ADR directory
	CheckRetiredTerm  = "retired-term"   // a term from the member's retired-term list
	CheckOversizeBody = "oversize"       // a body over MaxBytes
)

// DefaultMaxBytes is the size past which a body is reported: the part of a memory file that
// claude shows other sessions in recall.
const DefaultMaxBytes = 4096

// DefaultADRDir is where ADR records are looked up.
const DefaultADRDir = "docs/decisions"

// Memory is one memory to check. Name is the identity a [[link]] resolves against.
type Memory struct {
	Name string
	Body string
}

// Repo reads one revision of a repository. A nil Repo skips every repository check.
type Repo interface {
	// Paths lists every file path of the revision, slash-separated and relative to the root.
	Paths() ([]string, error)
	// FindSymbols answers which of syms occur as a whole word in any file of the revision.
	FindSymbols(syms []string) (map[string]bool, error)
}

// FileReader is implemented by a Repo that can also read file contents. The path check uses it to
// read the revision's committed .gitignore files, so a git-ignored path (a build output, an
// installed dependency) is not reported as missing. Without it, ignored paths are not skipped.
type FileReader interface {
	// ReadFile returns the content of one file of the revision.
	ReadFile(path string) ([]byte, error)
}

// Options tunes a run. The zero value uses the defaults and no retired terms.
type Options struct {
	// RetiredTerms is the member-maintained list. Matching ignores case and, for a term made of
	// word characters, requires word boundaries, so "muse" does not hit "museum".
	RetiredTerms []string
	// MaxBytes overrides DefaultMaxBytes when positive.
	MaxBytes int
	// ADRDir overrides DefaultADRDir when non-empty.
	ADRDir string
}

// Finding is one thing a check reports about one memory.
type Finding struct {
	Memory string `json:"memory"`
	Check  string `json:"check"`
	// Subject is the anchor, term or link target; for oversize it is the size in bytes.
	Subject string `json:"subject"`
	// Line is the 1-based line of the first occurrence, 0 when the check has no line.
	Line int `json:"line,omitempty"`
}

// Report is the outcome of one run.
type Report struct {
	Checked  int            `json:"checked"`
	Findings []Finding      `json:"findings"`
	Counts   map[string]int `json:"counts"`
	// Skipped names the checks that did not run and why, so an empty result is never mistaken
	// for a clean one.
	Skipped []string `json:"skipped,omitempty"`
}

// Lint checks every memory. Findings are ordered by memory name, then check, then position, so
// two runs over the same input are byte-identical.
func Lint(mems []Memory, opts Options, repo Repo) Report {
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	rep := Report{Checked: len(mems), Findings: []Finding{}, Counts: map[string]int{}}
	names := make(map[string]bool, len(mems))
	for _, m := range mems {
		names[m.Name] = true
	}
	terms := newTermMatcher(opts.RetiredTerms)

	var rc *repoCtx
	if repo != nil {
		var err error
		var notes []string
		if rc, notes, err = loadRepo(repo, opts.ADRDir); err != nil {
			rep.Skipped = append(rep.Skipped, fmt.Sprintf("repository checks: %v", err))
		}
		rep.Skipped = append(rep.Skipped, notes...)
	}

	type symUse struct {
		memory string
		sym    string
		line   int
	}
	var symUses []symUse
	symSet := map[string]bool{}

	for _, m := range mems {
		var fs []Finding
		for _, l := range extractLinks(m.Body) {
			if !names[l.text] {
				fs = append(fs, Finding{m.Name, CheckBrokenLink, l.text, l.line})
			}
		}
		for _, t := range terms.hits(m.Body) {
			fs = append(fs, Finding{m.Name, CheckRetiredTerm, t.text, t.line})
		}
		if len(m.Body) > maxBytes {
			fs = append(fs, Finding{Memory: m.Name, Check: CheckOversizeBody, Subject: fmt.Sprint(len(m.Body))})
		}
		if rc != nil {
			for _, a := range extractPaths(m.Body, rc.topDirs) {
				if !rc.found(a.text) {
					fs = append(fs, Finding{m.Name, CheckMissingPath, a.text, a.line})
				}
			}
			if rc.hasADRs {
				for _, a := range extractADRs(m.Body) {
					if !rc.adrs[a.text] {
						fs = append(fs, Finding{m.Name, CheckMissingADR, "ADR " + a.text, a.line})
					}
				}
			}
			for _, a := range extractSymbols(m.Body) {
				symUses = append(symUses, symUse{m.Name, a.text, a.line})
				symSet[a.text] = true
			}
		}
		rep.Findings = append(rep.Findings, fs...)
	}

	if rc != nil && len(symSet) > 0 {
		syms := make([]string, 0, len(symSet))
		for s := range symSet {
			syms = append(syms, s)
		}
		sort.Strings(syms)
		found, err := repo.FindSymbols(syms)
		if err != nil {
			rep.Skipped = append(rep.Skipped, fmt.Sprintf("symbol checks: %v", err))
		} else {
			for _, u := range symUses {
				if !found[u.sym] {
					rep.Findings = append(rep.Findings, Finding{u.memory, CheckMissingSym, u.sym, u.line})
				}
			}
		}
	}

	checkOrder := map[string]int{CheckBrokenLink: 0, CheckMissingPath: 1, CheckMissingSym: 2, CheckMissingADR: 3, CheckRetiredTerm: 4, CheckOversizeBody: 5}
	sort.SliceStable(rep.Findings, func(i, j int) bool {
		a, b := rep.Findings[i], rep.Findings[j]
		if a.Memory != b.Memory {
			return a.Memory < b.Memory
		}
		if a.Check != b.Check {
			return checkOrder[a.Check] < checkOrder[b.Check]
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Subject < b.Subject
	})
	for _, f := range rep.Findings {
		rep.Counts[f.Check]++
	}
	return rep
}

// repoCtx is what the path and ADR checks need from the revision's file list.
type repoCtx struct {
	files   map[string]bool
	dirs    map[string]bool
	topDirs map[string]bool
	adrs    map[string]bool
	hasADRs bool
	// numbered maps a directory to the numbers (leading zeros trimmed) of its entries named
	// "<number>-...", so a short reference "docs/log/99" finds "docs/log/99-name.md".
	numbered map[string]map[string]bool
	ignore   *ignoreSet
}

// loadRepo indexes the revision. A failure to read the .gitignore files is not fatal: the path
// check then runs without them, and the returned notes say so.
func loadRepo(repo Repo, adrDir string) (*repoCtx, []string, error) {
	paths, err := repo.Paths()
	if err != nil {
		return nil, nil, err
	}
	if len(paths) == 0 {
		return nil, nil, fmt.Errorf("the revision has no files")
	}
	if adrDir == "" {
		adrDir = DefaultADRDir
	}
	adrDir = strings.Trim(adrDir, "/") + "/"
	rc := &repoCtx{files: make(map[string]bool, len(paths)), dirs: map[string]bool{}, topDirs: map[string]bool{}, adrs: map[string]bool{}, numbered: map[string]map[string]bool{}}
	var ignoreFiles []string
	for _, p := range paths {
		rc.files[p] = true
		if p == ".gitignore" || strings.HasSuffix(p, "/.gitignore") {
			ignoreFiles = append(ignoreFiles, p)
		}
		for d := p; ; {
			rc.noteNumbered(d)
			i := strings.LastIndexByte(d, '/')
			if i < 0 {
				break
			}
			d = d[:i]
			if rc.dirs[d] {
				// Its own ancestors are indexed already; only this entry's name was new.
				break
			}
			rc.dirs[d] = true
		}
		if i := strings.IndexByte(p, '/'); i > 0 {
			rc.topDirs[p[:i]] = true
		}
		if rest, ok := strings.CutPrefix(p, adrDir); ok {
			if m := adrFileRe.FindStringSubmatch(rest); m != nil {
				rc.adrs[trimZeros(m[1])] = true
				rc.hasADRs = true
			}
		}
	}
	var notes []string
	if fr, ok := repo.(FileReader); ok && len(ignoreFiles) > 0 {
		sort.Slice(ignoreFiles, func(i, j int) bool {
			return strings.Count(ignoreFiles[i], "/") < strings.Count(ignoreFiles[j], "/") || (strings.Count(ignoreFiles[i], "/") == strings.Count(ignoreFiles[j], "/") && ignoreFiles[i] < ignoreFiles[j])
		})
		set := &ignoreSet{}
		for _, f := range ignoreFiles {
			b, err := fr.ReadFile(f)
			if err != nil {
				notes = append(notes, fmt.Sprintf("git-ignored paths are not skipped: %s unreadable: %v", f, err))
				set = nil
				break
			}
			dir := strings.TrimSuffix(strings.TrimSuffix(f, ".gitignore"), "/")
			set.files = append(set.files, ignoreFile{dir: dir, rules: parseIgnore(string(b))})
		}
		rc.ignore = set
	}
	return rc, notes, nil
}

// noteNumbered records p's name under its directory when it starts with "<digits>-".
func (rc *repoCtx) noteNumbered(p string) {
	dir, name := "", p
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		dir, name = p[:i], p[i+1:]
	}
	if m := numberedNameRe.FindStringSubmatch(name); m != nil {
		if rc.numbered[dir] == nil {
			rc.numbered[dir] = map[string]bool{}
		}
		rc.numbered[dir][trimZeros(m[1])] = true
	}
}

var (
	numberedNameRe = regexp.MustCompile(`^(\d+)-`)
	numberRefRe    = regexp.MustCompile(`^\d+$`)
)

// journalDir is where a bare "docs/<number>" reference resolves: the numbered work journals.
const journalDir = "docs/log"

// found reports whether the revision holds p. Beyond an exact file or directory it accepts a
// numbered short reference (see hasNumbered) and a path the revision's .gitignore files ignore.
func (rc *repoCtx) found(p string) bool {
	return rc.hasPath(p) || rc.hasNumbered(p) || rc.ignore.ignored(p, !rc.files[p])
}

func (rc *repoCtx) hasPath(p string) bool { return rc.files[p] || rc.dirs[p] }

// hasNumbered resolves "<dir>/<number>" to an entry "<dir>/<number>-*" (leading zeros compared
// as trimZeros does for ADRs). The literal placeholder "NN" accepts any numbered entry. A
// reference "docs/<number>" is a journal number written without "log/", so it also resolves in
// docs/log. Only the last segment is relaxed: the directory must exist, and a number with no
// entry stays a miss.
func (rc *repoCtx) hasNumbered(p string) bool {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return false
	}
	dir, last := p[:i], p[i+1:]
	dirs := []string{dir}
	if dir == "docs" {
		dirs = append(dirs, journalDir)
	}
	for _, d := range dirs {
		if !rc.dirs[d] {
			continue
		}
		switch {
		case last == "NN":
			if len(rc.numbered[d]) > 0 {
				return true
			}
		case numberRefRe.MatchString(last):
			if rc.numbered[d][trimZeros(last)] {
				return true
			}
		}
	}
	return false
}

var adrFileRe = regexp.MustCompile(`^(\d{3,5})-[^/]*$`)

func trimZeros(s string) string {
	if t := strings.TrimLeft(s, "0"); t != "" {
		return t
	}
	return "0"
}

// anchor is one extracted token with the line it first appears on.
type anchor struct {
	text string
	line int
}

// lineOf returns the 1-based line of byte offset off.
func lineOf(s string, off int) int { return strings.Count(s[:off], "\n") + 1 }

// dedupe keeps the first occurrence of each text.
func dedupe(as []anchor) []anchor {
	seen := map[string]bool{}
	out := as[:0]
	for _, a := range as {
		if !seen[a.text] {
			seen[a.text] = true
			out = append(out, a)
		}
	}
	return out
}

var (
	linkRe = regexp.MustCompile(`\[\[([^\[\]\n]+)\]\]`)
	// linkTargetRe is the form of a memory name: a target of any other shape (a shell's
	// `[[ -f x ]]`) is not a link.
	linkTargetRe = regexp.MustCompile(`^[a-z0-9_][a-z0-9._-]{0,63}$`)
)

func extractLinks(body string) []anchor {
	var out []anchor
	for _, m := range linkRe.FindAllStringSubmatchIndex(body, -1) {
		t := body[m[2]:m[3]]
		if i := strings.IndexAny(t, "|#"); i >= 0 {
			t = t[:i]
		}
		t = strings.TrimSpace(t)
		if linkTargetRe.MatchString(t) {
			out = append(out, anchor{t, lineOf(body, m[0])})
		}
	}
	return dedupe(out)
}

// pathRe finds slash-joined tokens. Only a token whose first segment is a top-level directory of
// the revision is checked (precision over recall: "and/or", URLs and absolute paths never match).
var pathRe = regexp.MustCompile(`[A-Za-z0-9_.@+-]+(?:/[A-Za-z0-9_.@+-]+)+/?`)

func extractPaths(body string, topDirs map[string]bool) []anchor {
	var out []anchor
	for _, m := range pathRe.FindAllStringIndex(body, -1) {
		if m[0] > 0 {
			// Part of a longer token: an absolute or home path, a URL, a glob or a template.
			if strings.IndexByte("/~:$*{<%\\", body[m[0]-1]) >= 0 {
				continue
			}
		}
		if m[1] < len(body) && strings.IndexByte("*{<$%", body[m[1]]) >= 0 {
			continue
		}
		t := strings.TrimRight(body[m[0]:m[1]], ".,;:")
		t = strings.TrimPrefix(t, "./")
		t = strings.TrimSuffix(t, "/")
		first, _, _ := strings.Cut(t, "/")
		if !strings.Contains(t, "/") || !topDirs[first] || strings.Contains(t, "..") {
			continue
		}
		out = append(out, anchor{t, lineOf(body, m[0])})
	}
	return dedupe(out)
}

var adrRe = regexp.MustCompile(`\bADR[ -]?0*(\d{1,5})\b`)

func extractADRs(body string) []anchor {
	var out []anchor
	for _, m := range adrRe.FindAllStringSubmatchIndex(body, -1) {
		out = append(out, anchor{trimZeros(body[m[2]:m[3]]), lineOf(body, m[0])})
	}
	return dedupe(out)
}

var (
	testSymRe = regexp.MustCompile(`\bTest[A-Z][A-Za-z0-9_]*`)
	tickRe    = regexp.MustCompile("`([^`\n]+)`")
	identRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*(?:\(\))?$`)
	camelRe   = regexp.MustCompile(`[a-z0-9][A-Z]|^[A-Z][a-z0-9]+[A-Z]`)
	snakeRe   = regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)+$`)
)

// extractSymbols finds Test* names anywhere and, inside backticks only, identifiers that look
// like code: camelCase or PascalCase with an inner capital, snake_case, or a call `f()`. A plain
// word in backticks is prose, and a name ending in a file extension is a file, not a symbol.
func extractSymbols(body string) []anchor {
	var out []anchor
	for _, m := range testSymRe.FindAllStringIndex(body, -1) {
		out = append(out, anchor{body[m[0]:m[1]], lineOf(body, m[0])})
	}
	for _, m := range tickRe.FindAllStringSubmatchIndex(body, -1) {
		t := body[m[2]:m[3]]
		if !identRe.MatchString(t) {
			continue
		}
		call := strings.HasSuffix(t, "()")
		t = strings.TrimSuffix(t, "()")
		segs := strings.Split(t, ".")
		last := segs[len(segs)-1]
		// x.go and package.json are files: a lowercase tail with no underscore and no call.
		// pkg.missing_func and pkg.removed() are qualified symbols and stay.
		if len(segs) > 1 && !call && last == strings.ToLower(last) && !strings.Contains(last, "_") {
			continue
		}
		if len(last) < 4 || !(call || camelRe.MatchString(last) || snakeRe.MatchString(last)) {
			continue
		}
		out = append(out, anchor{last, lineOf(body, m[0])})
	}
	return dedupe(out)
}

// termMatcher compiles the retired-term list once.
type termMatcher struct {
	terms []termRe
}

type termRe struct {
	text string
	re   *regexp.Regexp
}

var wordTermRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_ .-]*[A-Za-z0-9_]$|^[A-Za-z0-9_]$`)

func newTermMatcher(list []string) termMatcher {
	var tm termMatcher
	seen := map[string]bool{}
	for _, t := range list {
		t = strings.TrimSpace(t)
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		pat := regexp.QuoteMeta(t)
		if wordTermRe.MatchString(t) {
			pat = `(?:^|[^\p{L}\p{N}_])` + pat + `(?:$|[^\p{L}\p{N}_])`
		}
		re, err := regexp.Compile(`(?i)` + pat)
		if err != nil {
			continue
		}
		tm.terms = append(tm.terms, termRe{t, re})
	}
	return tm
}

func (tm termMatcher) hits(body string) []anchor {
	var out []anchor
	for _, t := range tm.terms {
		if m := t.re.FindStringIndex(body); m != nil {
			off := m[0]
			// The boundary group may have consumed a leading newline: the hit is on the term's line.
			if body[off] == '\n' {
				off++
			}
			out = append(out, anchor{t.text, lineOf(body, off)})
		}
	}
	return out
}
