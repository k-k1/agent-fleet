// Package cloudexec is the provider-neutral skeleton of the cloud wrappers (af-aws-exec;
// af-gcloud-exec of ADR 0107): the flag loop up to "--" with --help and --version, the
// exit codes, the environment helpers a wrapper builds its child's environment with,
// directories only this user can change, and the final syscall.Exec into the command (or,
// for a command that needs something beside it, Supervise).
//
// A backend (internal/awsx; internal/gcpx for ADR 0107) decides everything about
// identity: which variables carry its credentials, what is scrubbed, and when a failure
// means "log in" (exit 3). This package must import no backend.
package cloudexec

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"golang.org/x/sys/unix"
)

// Exit codes every wrapper shares; the command's own status replaces them on success.
const (
	// ExitRefused is any refusal or failure that a login does not fix.
	ExitRefused = 1
	// ExitUsage is a usage error.
	ExitUsage = 2
	// ExitLoginRequired means a login is needed and was not started (no terminal, a
	// --no-login, or a Console login still waiting): an agent hands it to the member as
	// "log in", so nothing else may use it.
	ExitLoginRequired = 3
)

// Wrapper is one wrapper command.
type Wrapper struct {
	// Name prefixes every message ("af-aws-exec").
	Name string
	// Usage is printed for --help and for a run without a command.
	Usage string
	// Version is the line after "<Name>, part of " for --version.
	Version func() string
}

// Fail prints msg as the wrapper's and exits with code.
func (w Wrapper) Fail(code int, msg string) {
	fmt.Fprintln(os.Stderr, w.Name+": "+msg)
	os.Exit(code)
}

// FailUsage prints the usage text and exits with ExitUsage.
func (w Wrapper) FailUsage() {
	fmt.Fprint(os.Stderr, w.Usage)
	os.Exit(ExitUsage)
}

// FailPlan exits for an error that kept the command from starting: ExitLoginRequired when
// err is loginRequired, ExitRefused for anything else.
func (w Wrapper) FailPlan(err, loginRequired error) {
	if errors.Is(err, loginRequired) {
		w.Fail(ExitLoginRequired, err.Error())
	}
	w.Fail(ExitRefused, err.Error())
}

// Exec replaces the wrapper with the command, so the command's exit status, signals and
// terminal are the caller's. It returns only by exiting with ExitRefused.
func (w Wrapper) Exec(prog string, argv, env []string) {
	if err := syscall.Exec(prog, argv, env); err != nil {
		w.Fail(ExitRefused, "exec "+prog+": "+err.Error())
	}
}

// Parse reads the flags up to "--"; everything after it is the command it returns.
// values are the flags that take a value, as "--flag v" or "--flag=v". flag handles every
// other argument and reports whether it knew it; -h/--help and --version are answered
// here, and anything else is a usage error.
func (w Wrapper) Parse(args []string, values map[string]*string, flag func(arg string) bool) []string {
	for len(args) > 0 {
		a := args[0]
		args = args[1:]
		if a == "--" {
			return args
		}
		if k, v, ok := strings.Cut(a, "="); ok && values[k] != nil {
			*values[k] = v
			continue
		}
		if dst := values[a]; dst != nil {
			if len(args) == 0 {
				w.Fail(ExitUsage, a+" needs a value")
			}
			*dst, args = args[0], args[1:]
			continue
		}
		if flag(a) {
			continue
		}
		switch a {
		case "-h", "--help":
			fmt.Print(w.Usage)
			os.Exit(0)
		case "--version":
			fmt.Println(w.Name + ", part of " + w.Version())
			os.Exit(0)
		default:
			w.Fail(ExitUsage, "unknown argument "+a+" (put the command after --)")
		}
	}
	return nil
}

// IsTerminal reports whether f is a terminal. A character-device check is not enough:
// /dev/null is one too, and treating a redirected, unattended run as interactive would
// start a login that waits for nobody.
func IsTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	return err == nil
}

// SetEnv sets each KEY=value in env, removing every earlier entry for that key. Never
// append a variable that may already be there: getenv in C, Python and the AWS CLI
// returns the FIRST entry of a duplicated key, so an appended AWS_REGION loses to the
// caller's (measured with aws-cli 2.36.46: --region was ignored).
func SetEnv(env []string, kvs ...string) []string {
	drop := map[string]bool{}
	for _, kv := range kvs {
		k, _, _ := strings.Cut(kv, "=")
		drop[k] = true
	}
	out := make([]string, 0, len(env)+len(kvs))
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); !drop[k] {
			out = append(out, kv)
		}
	}
	return append(out, kvs...)
}

// EnvValue is getenv over env: the FIRST entry of a duplicated key, as C, Python and Go's
// own os.Getenv resolve it, so this reads what a child will read.
func EnvValue(env []string, key string) string {
	for _, kv := range env {
		if k, val, _ := strings.Cut(kv, "="); k == key {
			return val
		}
	}
	return ""
}

// EnvHas reports whether env sets key to a non-empty value.
func EnvHas(env []string, key string) bool {
	for _, kv := range env {
		if k, v, _ := strings.Cut(kv, "="); k == key && v != "" {
			return true
		}
	}
	return false
}

// Scrub returns env without every variable drop names. A backend removes what could
// hand its child another identity, by exact name or by prefix (DropPrefixes).
func Scrub(env []string, drop func(key string) bool) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); !drop(k) {
			out = append(out, kv)
		}
	}
	return out
}

// DropNames is a Scrub predicate for exact names.
func DropNames(names ...string) func(string) bool {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	return func(k string) bool { return set[k] }
}

// DropPrefixes is a Scrub predicate for every name that starts with one of prefixes.
func DropPrefixes(prefixes ...string) func(string) bool {
	return func(k string) bool {
		for _, p := range prefixes {
			if strings.HasPrefix(k, p) {
				return true
			}
		}
		return false
	}
}

// StateDir is the directory name under the Agent's state directory. It is there, not in
// the cloud's own directory (~/.aws, ~/.config/gcloud), so a directory linked onto other
// storage is never followed to write it.
func StateDir(name string) string { return filepath.Join(paths.AgentStateDir(), name) }

// RunDir makes a new private directory for one run under PrivateDir(parent); the caller
// removes it when the run no longer needs it.
func RunDir(parent, pattern string) (string, error) {
	p, err := PrivateDir(parent)
	if err != nil {
		return "", err
	}
	return os.MkdirTemp(p, pattern)
}

// PrivateDir makes dir (0700) and insists it is a real directory owned by this user,
// reached through directories nobody else can change: a wrapper's child reads what it
// runs and its credentials from there, so if another user could replace it (a
// group-writable directory, or one under a world-writable parent the directory links to)
// they would decide what the child runs and receive its credentials. It returns the
// resolved path, so the writes that follow do not pass through a link again.
func PrivateDir(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() || !ok || int(st.Uid) != os.Getuid() {
		return "", fmt.Errorf("%s must be a directory of your own, not a link; remove it and run again", dir)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return "", err
		}
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	// Every directory above must be this user's or root's (an owner can always give
	// themselves write access and rename what is inside), and writable by others only
	// with the sticky bit (as /tmp is), under which they cannot rename what they do not
	// own.
	for p := filepath.Dir(real); ; p = filepath.Dir(p) {
		pi, err := os.Stat(p)
		if err != nil {
			return "", err
		}
		ps, ok := pi.Sys().(*syscall.Stat_t)
		if !ok || (int(ps.Uid) != os.Getuid() && ps.Uid != 0) {
			return "", fmt.Errorf("%s belongs to another user, so %s under it is not private (it has to sit under directories you or root own)", p, dir)
		}
		if pi.Mode().Perm()&0o022 != 0 && pi.Mode()&os.ModeSticky == 0 {
			return "", fmt.Errorf("%s is writable by its group or other users, so %s under it is not private (`chmod go-w %s` fixes that)", p, dir, p)
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	return real, nil
}
