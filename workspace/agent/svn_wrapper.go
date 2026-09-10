package main

// Transparent authentication for the `svn` an agent (or a person) runs itself
// (docs/log/41 amendment; ADR 0024's "limitation (intended)" is what this lifts).
//
// git gets this from its credential-helper protocol and `gh` from a PATH wrapper
// (gh-auth-wrapper.sh). SVN has neither: it speaks no helper protocol, and the only
// place it looks for a password by itself is `~/.subversion/auth`, which ADR 0024
// deliberately refuses to write (plaintext at rest). So the same PATH-wrapper shape
// closes the gap without a plaintext file: /usr/local/bin/svn is a shim that re-enters
// this binary, which resolves the credential from the ENCRYPTED store and hands it to the
// real svn on stdin. Nothing lands on disk and nothing appears in `ps` (the password is
// never an argv element).
//
// Three rules keep the wrapper from becoming a surprise of its own:
//
//   - It never changes what a command means. Every decision below is "inject, or pass
//     through untouched" — there is no case where svn does something OTHER than what was
//     typed. Anything the planner cannot reason about is passed through.
//   - It never takes stdin away from a command that wants it. --password-from-stdin
//     consumes stdin, so a command that reads stdin itself (`-F -`, `--targets -`,
//     `svn patch -`) is passed through, as is one that would open an editor for a log
//     message (`svn commit` with no -m): an editor whose stdin is our pipe is a hang.
//   - An explicit credential on the command line always wins; the wrapper only fills a
//     gap it can see is empty.
//
// Failure of the wrapper itself is never failure of the command: any error resolving a
// credential ends in exec'ing the real svn unchanged.

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/secrets"
)

// svnRealEnv is where the shim tells us the real binary is, and svnWrappedEnv is the
// re-entry guard: with it set, a `svn` reached through the shim from INSIDE this process
// tree goes straight to the real binary instead of wrapping itself forever.
const (
	svnRealEnv    = "AF_SVN_REAL"
	svnWrappedEnv = "AF_SVN_WRAPPED"
)

// svnAuthedSubcommands are the subcommands that accept --username / --password-from-stdin
// / --trust-server-cert-failures. svn validates options PER SUBCOMMAND ("Subcommand 'add'
// doesn't accept option '--username'"), so injecting into anything else would turn a
// working local command into a usage error. Local-only subcommands (add, revert, cleanup,
// resolve, patch, …) are absent on purpose: they need no credential anyway.
var svnAuthedSubcommands = map[string]bool{
	"blame": true, "praise": true, "annotate": true, "ann": true,
	"cat": true, "checkout": true, "co": true, "commit": true, "ci": true,
	"copy": true, "cp": true, "delete": true, "del": true, "remove": true, "rm": true,
	"diff": true, "di": true, "export": true, "import": true, "info": true,
	"list": true, "ls": true, "lock": true, "log": true, "merge": true, "mergeinfo": true,
	"mkdir": true, "move": true, "mv": true, "rename": true, "ren": true,
	"propdel": true, "pdel": true, "pd": true, "propedit": true, "pedit": true, "pe": true,
	"propget": true, "pget": true, "pg": true, "proplist": true, "plist": true, "pl": true,
	"propset": true, "pset": true, "ps": true, "relocate": true, "resolve": true,
	"status": true, "stat": true, "st": true, "switch": true, "sw": true,
	"unlock": true, "update": true, "up": true, "upgrade": true,
}

// svnEditorSubcommands would open $SVN_EDITOR when no log message is supplied. Feeding
// the password on stdin there leaves the editor reading from our pipe — vim prints
// "Input is not from a terminal" and the turn hangs on a command that normally just
// works. These inject only when a message is already on the command line.
var svnEditorSubcommands = map[string]bool{
	"commit": true, "ci": true, "import": true,
	"copy": true, "cp": true, "move": true, "mv": true, "rename": true, "ren": true,
	"delete": true, "del": true, "remove": true, "rm": true, "mkdir": true,
	"propedit": true, "pedit": true, "pe": true,
}

// svnValueOptions are the options that swallow the NEXT argv element as their value (in
// the separated form). Needed to find the subcommand and the first real target: without
// it, `svn --config-dir /tmp/x update` reads "/tmp/x" as the subcommand.
var svnValueOptions = map[string]bool{
	"--config-dir": true, "--config-option": true, "--username": true, "--password": true,
	"--depth": true, "--set-depth": true, "--encoding": true, "--editor-cmd": true,
	"--diff-cmd": true, "--diff3-cmd": true, "--merge-cmd": true, "--accept": true,
	"--show-item": true, "--changelist": true, "--cl": true, "--with-revprop": true,
	"--targets": true, "--limit": true, "--revision": true, "--change": true,
	"--message": true, "--file": true, "--extensions": true, "--native-eol": true,
	"-m": true, "-F": true, "-r": true, "-c": true, "-l": true, "-x": true,
	"--old": true, "--new": true, "--strip": true, "--search": true, "--search-and": true,
}

// svnPlan is what the wrapper decided: the argv to run (after "svn") and whether the
// password is to be written to the child's stdin.
type svnPlan struct {
	Args     []string
	Password string
}

// optName splits "--opt=value" into its name; a bare option returns itself.
func optName(a string) string {
	if i := strings.Index(a, "="); i > 0 && strings.HasPrefix(a, "--") {
		return a[:i]
	}
	return a
}

// optValue returns the value of `name` in args, in either form (`--name v` / `--name=v`),
// and whether it was present at all.
func optValue(args []string, name string) (string, bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == name {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", true
		}
		if strings.HasPrefix(a, name+"=") {
			return a[len(name)+1:], true
		}
	}
	return "", false
}

// hasOpt reports whether an option is present in either form.
func hasOpt(args []string, names ...string) bool {
	for _, n := range names {
		if _, ok := optValue(args, n); ok {
			return true
		}
	}
	return false
}

// svnSubcommandIndex finds the subcommand's position in args, skipping leading global
// options and their values. -1 when there is none (`svn --version`, `svn` alone).
func svnSubcommandIndex(args []string) int {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return -1 // everything after is a target; there was no subcommand
		}
		if strings.HasPrefix(a, "-") {
			if svnValueOptions[optName(a)] && !strings.Contains(a, "=") {
				i++ // its value is the next element
			}
			continue
		}
		return i
	}
	return -1
}

// svnArgsReadStdin reports whether the command itself is going to read standard input:
// a "-" given to --file/-F/--targets, or a "-" target of `svn patch`. Handing such a
// command --password-from-stdin would feed the password into the commit message.
func svnArgsReadStdin(args []string) bool {
	for _, n := range []string{"-F", "--file", "--targets"} {
		if v, ok := optValue(args, n); ok && v == "-" {
			return true
		}
	}
	for _, a := range args {
		if a == "-" {
			return true
		}
	}
	return false
}

// svnFirstURL returns the first argv element that looks like an absolute URL, skipping
// option values. A peg revision ("URL@42") rides along harmlessly: the credential lookup
// is a prefix match.
func svnFirstURL(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			if svnValueOptions[optName(a)] && !strings.Contains(a, "=") {
				i++
			}
			continue
		}
		if svnLooksLikeURL(a) {
			return a
		}
	}
	return ""
}

// svnLooksLikeURL reports whether s is an absolute URL (scheme://…), which is how SVN
// addresses a repository as opposed to a working copy path.
func svnLooksLikeURL(s string) bool {
	i := strings.Index(s, "://")
	if i <= 0 {
		return false
	}
	for j := 0; j < i; j++ {
		c := s[j]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'
		if !ok {
			return false
		}
	}
	return true
}

// svnFirstPath returns the first non-option, non-URL argument after the subcommand — the
// working copy the command is about. "" means "the current directory", svn's own default.
func svnFirstPath(args []string, sub int) string {
	for i := sub + 1; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			continue
		}
		if strings.HasPrefix(a, "-") {
			if svnValueOptions[optName(a)] && !strings.Contains(a, "=") {
				i++
			}
			continue
		}
		if !svnLooksLikeURL(a) {
			return a
		}
	}
	return ""
}

// planSvnWrapper decides what to run. Pure, so every rule above is unit-tested without
// spawning svn. creds == nil (nothing stored for this URL) means "pass through", which is
// exactly the behaviour that existed before this wrapper.
func planSvnWrapper(args []string, creds *secrets.SVNCred) svnPlan {
	pass := svnPlan{Args: args}
	if creds == nil {
		return pass
	}
	sub := svnSubcommandIndex(args)
	if sub < 0 || !svnAuthedSubcommands[args[sub]] {
		return pass
	}
	rest := args[sub+1:]
	// An explicit password (in any form) is the user's decision; never second-guess it.
	if hasOpt(args, "--password", "--password-from-stdin") {
		return pass
	}
	inject := []string{}
	// Cert trust is a server property, not a secret, and is useful even when no password
	// is injected (a public self-signed repository).
	if creds.TrustCert && !hasOpt(args, "--trust-server-cert", "--trust-server-cert-failures") {
		inject = append(inject, svnTrustFailures)
	}
	named, hasUser := optValue(args, "--username")
	feed := creds.Password != ""
	switch {
	case hasUser && named != creds.Username:
		feed = false // another account was asked for by name; we hold nothing for it
	case !hasUser && creds.Username == "":
		feed = false // trust-only entry: there is no account to send
	case svnArgsReadStdin(args):
		feed = false // stdin belongs to the command (`-F -`, `--targets -`, `svn patch -`)
	case svnEditorSubcommands[args[sub]] && !svnHasMessage(rest):
		feed = false // svn is about to open an editor; it must keep the terminal's stdin
	}
	if feed {
		if !hasUser {
			inject = append(inject, "--username", creds.Username)
		}
		inject = append(inject, "--password-from-stdin")
	}
	if len(inject) == 0 {
		return pass
	}
	out := make([]string, 0, len(args)+len(inject))
	out = append(out, args[:sub+1]...)
	out = append(out, inject...)
	out = append(out, rest...)
	plan := svnPlan{Args: out}
	if feed {
		plan.Password = creds.Password
	}
	return plan
}

// svnHasMessage reports whether a log message is already on the command line, including
// the attached short form (`-m"text"` arrives as one argv element). Without one, svn opens
// an editor, and the editor needs the terminal's stdin more than svn needs our password.
func svnHasMessage(args []string) bool {
	if hasOpt(args, "-m", "--message", "-F", "--file") {
		return true
	}
	for _, a := range args {
		if strings.HasPrefix(a, "-m") && len(a) > 2 {
			return true
		}
	}
	return false
}

// realSvnPath resolves the actual svn binary, never the shim. The shim passes it in
// AF_SVN_REAL; the fallbacks exist so running `workspace-agent svn-run` by hand still
// works. Any PATH entry under /usr/local/bin is skipped — that is where the shim lives,
// and exec'ing it would recurse.
func realSvnPath() string {
	if p := strings.TrimSpace(os.Getenv(svnRealEnv)); p != "" {
		return p
	}
	for _, p := range []string{"/usr/bin/svn", "/bin/svn", "/opt/homebrew/bin/svn"} {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	if p, err := exec.LookPath("svn"); err == nil && !strings.HasPrefix(p, "/usr/local/bin/") {
		return p
	}
	return ""
}

// svnWrapperURL works out which server the command is going to talk to: an explicit URL
// on the command line, else the URL of the working copy it names (asked of the real svn,
// a purely local read). "" when it cannot be determined — the caller then passes through.
func svnWrapperURL(real string, args []string) string {
	if u := svnFirstURL(args); u != "" {
		return u
	}
	sub := svnSubcommandIndex(args)
	if sub < 0 {
		return ""
	}
	path := svnFirstPath(args, sub)
	if path == "" {
		path = "."
	}
	cmd := exec.Command(real, "--non-interactive", "info", "--show-item", "url", path)
	cmd.Env = append(os.Environ(), svnWrappedEnv+"=1")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// runSvnWrapper is the `workspace-agent svn-run <svn args…>` subcommand behind the PATH
// shim. It execs the real svn (replacing this process) so signals, exit codes and the
// terminal behave exactly as they would without the wrapper.
func runSvnWrapper(args []string) {
	real := realSvnPath()
	if real == "" {
		fmt.Fprintln(os.Stderr, "svn: not found")
		os.Exit(127)
	}
	plan := svnPlan{Args: args}
	// The subcommand is checked BEFORE the URL is resolved. planSvnWrapper would reject a
	// local-only subcommand anyway, but resolving the URL costs an `svn info` per call —
	// paid on every `svn add` / `svn status` in a loop for an answer already known.
	if sub := svnSubcommandIndex(args); os.Getenv(svnWrappedEnv) == "" && sub >= 0 && svnAuthedSubcommands[args[sub]] {
		if url := svnWrapperURL(real, args); url != "" {
			plan = planSvnWrapper(args, svnCredsFor(url))
		}
	}
	env := append(os.Environ(), svnWrappedEnv+"=1")
	if plan.Password == "" {
		// Nothing to feed: hand the process over wholesale, stdin included.
		if err := syscall.Exec(real, append([]string{"svn"}, plan.Args...), env); err != nil {
			fmt.Fprintf(os.Stderr, "svn: %v\n", err)
			os.Exit(127)
		}
		return
	}
	cmd := exec.Command(real, plan.Args...)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(plan.Password + "\n")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "svn: %v\n", err)
		os.Exit(127)
	}
}
