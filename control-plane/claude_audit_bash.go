package main

import (
	"path"
	"regexp"
	"strings"
)

// What a Bash tool call leaves in the audit ledger. A command line is where tokens and
// keys end up (inline KEY=VALUE, Authorization headers, --password flags), and the
// ledger is readable by tenant admins, so by default only the program name is kept.
// AF_CLAUDE_AUDIT_BASH_FULL=1 opts into the first line of the command, masked with
// maskCommandSecrets; masking is best effort and is not a substitute for leaving the
// full line off (docs/build/07-security.md §7.7).

// bashProgramTarget returns the program name and, when it looks like a plain
// subcommand ("go test", "git push"), that word too. Leading VAR=value assignments are
// skipped, and a word that is a flag, a path, or carries "=" is never the second word.
func bashProgramTarget(info string) string {
	words := strings.Fields(info)
	for len(words) > 0 && assignWordRE.MatchString(words[0]) {
		words = words[1:]
	}
	if len(words) == 0 {
		return ""
	}
	prog := path.Base(words[0])
	if len(prog) > 40 {
		prog = prog[:40]
	}
	if len(words) > 1 && subcommandWordRE.MatchString(words[1]) {
		return prog + " " + words[1]
	}
	return prog
}

var (
	assignWordRE     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	subcommandWordRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,19}$`)

	// Order matters: structured forms first, then bare token shapes.
	secretValueREs = []struct {
		re   *regexp.Regexp
		repl string
	}{
		{regexp.MustCompile(`(?i)(authorization:?\s*)(bearer\s+|basic\s+|token\s+)?[^\s"']+`), "${1}${2}***"},
		{regexp.MustCompile(`(?i)\b(bearer)\s+[^\s"']+`), "${1} ***"},
		{regexp.MustCompile(`(?i)(--?[a-z-]*(?:token|password|passwd|secret|api-?key|auth|credential)[a-z-]*)(=|\s+)[^\s"']+`), "${1}${2}***"},
		{regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)=[^\s]+`), "${1}=***"},
		{regexp.MustCompile(`(://[^/\s:@]*):[^/\s@]+@`), "${1}:***@"},
		{regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{16,}|github_pat_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_-]{10,}|xox[abprs]-[A-Za-z0-9-]{10,}|sk-[A-Za-z0-9_-]{16,}|AKIA[0-9A-Z]{16}|AIza[0-9A-Za-z_-]{30,}|eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}(?:\.[A-Za-z0-9_-]*)?)`), "***"},
	}
)

// maskCommandSecrets replaces secret-looking values in a command line with "***".
func maskCommandSecrets(s string) string {
	for _, r := range secretValueREs {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}

// bashAuditTarget is the audit Target for a Bash call.
func bashAuditTarget(info string, full bool) string {
	if full {
		return maskCommandSecrets(info)
	}
	return bashProgramTarget(info)
}
