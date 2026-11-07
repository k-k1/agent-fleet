// Package claudeaudit decides what a Claude Bash tool call leaves in the audit ledger.
//
// A command line is where tokens and keys end up (inline NAME=value, Authorization
// headers, --password flags), and the ledger is readable by tenant admins, so by default
// only a known program and a known subcommand are kept: a general argument is never
// recorded, because "looks like a subcommand" cannot be told from a plain-text secret.
// AF_CLAUDE_AUDIT_BASH_FULL=1 opts into the first line of the command, masked by
// MaskCommandSecrets; masking is best effort and no substitute for leaving it off
// (docs/build/07-security.md §7.7).
package claudeaudit

import (
	"path"
	"regexp"
	"strings"
)

// knownSubcommands lists, per program, the words that may follow it into the ledger.
// A program not listed is recorded by name alone.
var knownSubcommands = map[string]map[string]bool{
	"go":        set("build", "test", "vet", "run", "mod", "fmt", "get", "install", "generate", "work", "clean"),
	"git":       set("add", "commit", "push", "pull", "fetch", "clone", "checkout", "switch", "branch", "merge", "rebase", "reset", "stash", "status", "diff", "log", "tag", "remote", "worktree", "restore", "rm", "mv", "show", "cherry-pick", "revert", "submodule"),
	"npm":       set("run", "install", "ci", "test", "publish", "start", "build", "exec", "uninstall", "update", "pack"),
	"pnpm":      set("run", "install", "test", "build", "add", "remove", "exec", "dlx", "update"),
	"yarn":      set("run", "install", "test", "build", "add", "remove", "upgrade"),
	"npx":       set(),
	"cargo":     set("build", "test", "run", "check", "clippy", "fmt", "add", "install", "publish", "clean"),
	"docker":    set("build", "run", "ps", "pull", "push", "compose", "exec", "logs", "images", "stop", "rm"),
	"kubectl":   set("get", "apply", "delete", "describe", "logs", "exec", "rollout", "config", "port-forward"),
	"gh":        set("pr", "issue", "run", "repo", "api", "auth", "release", "workflow"),
	"make":      set(),
	"pip":       set("install", "uninstall", "list", "freeze"),
	"python":    set(),
	"python3":   set(),
	"systemctl": set("start", "stop", "restart", "status", "enable", "disable"),
	"terraform": set("init", "plan", "apply", "destroy", "fmt", "validate", "output"),
	"aws":       set(),
	"gcloud":    set(),
}

func set(v ...string) map[string]bool {
	m := make(map[string]bool, len(v))
	for _, s := range v {
		m[s] = true
	}
	return m
}

var (
	assignWordRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	progRE       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,39}$`)
)

// wordEnd returns the index where the shell word starting at i ends: whitespace outside
// quotes and escapes separates words; single quotes, double quotes, backslash escapes and
// their concatenation (a'b'"c"\ d) stay inside one word. An unterminated quote or a trailing
// backslash swallows the rest of the line (a transcript line is cut at a fixed length), which
// keeps whatever follows inside the same word.
func wordEnd(s string, i int) int {
	var q byte
	for i < len(s) {
		c := s[i]
		// An expansion ($(...), ${...}, $((...)), backticks) can hold spaces, quotes and a
		// whole pipeline; nothing here pretends to parse it, so the word runs to the end of
		// the line. Single quotes do not expand.
		if q != '\'' && (c == '`' || (c == '$' && i+1 < len(s) && (s[i+1] == '(' || s[i+1] == '{'))) {
			return len(s)
		}
		switch {
		case q == '\'':
			if c == '\'' {
				q = 0
			}
		case q == '"':
			if c == '\\' {
				i++
			} else if c == '"' {
				q = 0
			}
		case c == '\\':
			i++
		case c == '\'' || c == '"':
			q = c
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			return i
		}
		i++
	}
	return len(s)
}

// shellWords splits s into words with wordEnd's rules.
func shellWords(s string) []string {
	var words []string
	i := 0
	for i < len(s) {
		if c := s[i]; c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			i++
			continue
		}
		e := wordEnd(s, i)
		words = append(words, s[i:e])
		i = e
	}
	return words
}

// ProgramTarget is the program name, plus the next word only when the program and that
// word are both known. Leading NAME=value words are skipped (quoted values included).
// When the program name does not look like one, the result is empty: when in doubt, omit.
func ProgramTarget(info string) string {
	words := shellWords(info)
	for len(words) > 0 && assignWordRE.MatchString(words[0]) {
		words = words[1:]
	}
	if len(words) == 0 {
		return ""
	}
	prog := path.Base(words[0])
	if !progRE.MatchString(prog) {
		return ""
	}
	if len(words) > 1 && knownSubcommands[prog][words[1]] {
		return prog + " " + words[1]
	}
	return prog
}

var (
	assignPrefixRE = regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*=`)
	flagPrefixRE   = regexp.MustCompile(`(?i)--?[a-z-]*(?:token|password|passwd|secret|api-?key|auth|credential)[a-z-]*(?:=|\s+)`)
)

// maskValues replaces the whole shell word after every NAME= and secret-named flag with
// "***". The word is found with wordEnd, so quotes, escapes and quoted/unquoted runs are
// covered, and a line cut mid-quote is masked to its end.
func maskValues(s string) string {
	type span struct{ from, to int }
	var spans []span
	for _, re := range []*regexp.Regexp{assignPrefixRE, flagPrefixRE} {
		for _, m := range re.FindAllStringIndex(s, -1) {
			if m[1] < len(s) {
				spans = append(spans, span{m[1], wordEnd(s, m[1])})
			}
		}
	}
	if len(spans) == 0 {
		return s
	}
	mask := make([]bool, len(s))
	for _, sp := range spans {
		for k := sp.from; k < sp.to; k++ {
			mask[k] = true
		}
	}
	var b strings.Builder
	for k := 0; k < len(s); k++ {
		if !mask[k] {
			b.WriteByte(s[k])
			continue
		}
		b.WriteString("***")
		for k+1 < len(s) && mask[k+1] {
			k++
		}
	}
	return b.String()
}

var secretValueREs = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?i)(authorization:?\s*)(bearer\s+|basic\s+|token\s+)?[^\s"']+`), "${1}${2}***"},
	{regexp.MustCompile(`(?i)\b(bearer)\s+[^\s"']+`), "${1} ***"},
	{regexp.MustCompile(`(://[^/\s:@]*):[^/\s@]+@`), "${1}:***@"},
	{regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{16,}|github_pat_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_-]{10,}|xox[abprs]-[A-Za-z0-9-]{10,}|sk-[A-Za-z0-9_-]{16,}|AKIA[0-9A-Z]{16}|AIza[0-9A-Za-z_-]{30,}|eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}(?:\.[A-Za-z0-9_-]*)?)`), "***"},
}

// MaskCommandSecrets replaces secret-looking values in a command line with "***".
func MaskCommandSecrets(s string) string {
	s = maskValues(s)
	for _, r := range secretValueREs {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}

// BashTarget is the audit Target for a Bash call.
func BashTarget(info string, full bool) string {
	if full {
		return MaskCommandSecrets(info)
	}
	return ProgramTarget(info)
}
