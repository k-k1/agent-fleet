package bootinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// npmCLIs are the npm-distributed agent CLIs, in the order the entrypoint always used.
var npmCLIs = []struct{ Name, Pkg string }{
	{"claude", "@anthropic-ai/claude-code"},
	{"opencode", "@opencode/cli"},
	{"codex", "@openai/codex"},
	{"copilot", "@github/copilot"},
}

// npmPerPkgMiB is what one package asks of home: the tarball plus its unpacked tree, with room
// for npm's rollback copy.
const npmPerPkgMiB = 300

// npmInstallTimeout bounds one `npm install`; the shell version had no bound at all.
const npmInstallTimeout = 20 * time.Minute

func npmPkg(cli string) string {
	for _, c := range npmCLIs {
		if c.Name == cli {
			return c.Pkg
		}
	}
	return ""
}

func (e Env) npmBin() string {
	if e.NPM != "" {
		return e.NPM
	}
	return "npm"
}

var (
	reNoSpace  = regexp.MustCompile(`(?i)no space left|ENOSPC|quota exceeded`)
	reChecksum = regexp.MustCompile(`(?i)did NOT match|computed checksum|: FAILED( |$)|EINTEGRITY|integrity (check|checksum) failed`)
	reNetwork  = regexp.MustCompile(`(?i)curl: \(|could not resolve|connection|timed out|ETIMEDOUT|ENOTFOUND|ECONNRE|EAI_AGAIN|network|registry`)
)

// reasonFromStderr classifies a failed npm run by what it printed, in the order the entrypoint's
// bi_reason used: a full disk also breaks downloads and digests, and is the one the member can fix.
func reasonFromStderr(s string) string {
	switch {
	case reNoSpace.MatchString(s):
		return ReasonNoSpace
	case reChecksum.MatchString(s):
		return ReasonChecksum
	case reNetwork.MatchString(s):
		return ReasonNetwork
	}
	return ReasonOther
}

// npmInstall runs `npm install -g --prefix ~/.local <specs>` and returns what npm printed on
// stderr. npm checks every tarball against the registry's integrity hash itself (EINTEGRITY
// fails the install), so there is no digest of ours to compare, unlike the GitHub-release CLIs.
func (e Env) npmInstall(specs []string) (stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), npmInstallTimeout)
	defer cancel()
	args := append([]string{"install", "-g", "--prefix", filepath.Join(e.Home, ".local")}, specs...)
	cmd := exec.CommandContext(ctx, e.npmBin(), args...)
	// Postinstall scripts may outlive npm: kill the whole group at the deadline so none keeps
	// writing to ~/.local while the verify pass runs.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	se := &tailWriter{max: 64 << 10}
	cmd.Stderr = se
	err = cmd.Run()
	// Whatever npm answered, nothing it started may outlive it: Wait prefers the exit error over
	// ErrWaitDelay, so a failed npm with a postinstall child holding the pipes would otherwise
	// skip the group kill. ESRCH (nothing left) is the usual answer.
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	return se.String(), err
}

// npmVersions is `npm ls`: package name to installed version, empty when npm cannot tell.
func (e Env) npmVersions() map[string]string {
	got, _ := e.npmLs()
	return got
}

// npmLs is npmVersions that also says whether npm exited 0. npm ls exits non-zero for a tree
// with problems and still prints json; the self-update treats that as "cannot decide" (updates),
// as the shell's execSync did.
func (e Env) npmLs() (map[string]string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.npmBin(), "ls", "-g", "--prefix", filepath.Join(e.Home, ".local"), "--depth=0", "--json")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.WaitDelay = time.Second
	runErr := cmd.Run() // npm ls exits non-zero for a tree with problems and still prints the json
	var d struct {
		Dependencies map[string]struct {
			Version string `json:"version"`
		} `json:"dependencies"`
	}
	got := map[string]string{}
	if json.Unmarshal(out.Bytes(), &d) == nil {
		for k, v := range d.Dependencies {
			got[k] = v.Version
		}
	}
	return got, runErr == nil
}

// tailWriter keeps the last max bytes written: npm prints the reason it failed at the end, after
// any amount of warnings.
type tailWriter struct {
	buf []byte
	max int
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	if len(w.buf) > 2*w.max {
		w.buf = append([]byte(nil), w.buf[len(w.buf)-w.max:]...)
	}
	return len(p), nil
}

func (w *tailWriter) String() string {
	if len(w.buf) > w.max {
		return string(w.buf[len(w.buf)-w.max:])
	}
	return string(w.buf)
}

// lastStatus is the status recorded for tool by an earlier step of this start ("" when none).
func (e Env) lastStatus(tool string) string {
	if e.StatusPath == "" {
		return ""
	}
	b, err := os.ReadFile(e.StatusPath)
	if err != nil {
		return ""
	}
	last := ""
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Split(l, "\t"); len(f) >= 2 && f[0] == tool {
			last = f[1]
		}
	}
	return last
}

// room checks need MiB of free space in home for the named tools. When short it records each
// tool (a usable copy keeps working: its skipped upgrade is "skipped", only a tool with nothing
// usable is "failed", which is what makes the Agent refuse a start) and reports false.
func (e Env) room(needMiB int64, tools []string, what string) bool {
	free, ok := e.FreeMiB(e.Home)
	if !ok || free >= needMiB {
		return true
	}
	fmt.Fprintf(e.Stdout, "[entrypoint] WARN: only %d MiB free in home, %d MiB needed for %s; skipping (keeping what is installed)\n", free, needMiB, what)
	for _, t := range tools {
		if e.cliUsable(t) {
			e.note(t, statusSkip, ReasonNoSpace)
		} else {
			e.note(t, statusFailed, ReasonNoSpace)
		}
	}
	return false
}

// NPM installs the pinned npm CLIs into ~/.local that are missing or (with Repin) drifted from
// the pin, in one npm run, then verifies each. It returns the process exit code.
func NPM(e Env) int {
	var specs, wanted []string
	var cur map[string]string
	for _, c := range npmCLIs {
		pin := e.Pins[c.Name]
		if pin == "" {
			continue
		}
		present := e.cliPresent(c.Name)
		if present {
			e.note(c.Name, statusSkip, "")
		}
		need := !present
		if present && e.Repin {
			if _, err := os.Stat(filepath.Join(e.localBin(), c.Name)); err == nil {
				if cur == nil {
					cur = e.npmVersions()
				}
				// An install npm does not manage (no version) is left alone.
				if v := cur[c.Pkg]; v != "" && v != pin {
					need = true
				}
			}
		}
		if need {
			specs = append(specs, c.Pkg+"@"+pin)
			wanted = append(wanted, c.Name)
		}
	}
	if len(specs) == 0 {
		fmt.Fprintln(e.Stdout, "[entrypoint] boot-install: npm CLIs already present in ~/.local (skip)")
		return ExitOK
	}
	fmt.Fprintf(e.Stdout, "[entrypoint] boot-install (pinned): %s ...\n", strings.Join(specs, " "))
	if !e.room(int64(len(specs))*npmPerPkgMiB, wanted, strings.Join(specs, " ")) {
		return ExitNoSpace
	}
	stderr, err := e.npmInstall(specs)
	if err != nil {
		fmt.Fprint(e.Stderr, stderr)
		fmt.Fprintf(e.Stdout, "[entrypoint] WARN: npm boot-install failed (retrying next start): %v\n", err)
		reason := reasonFromStderr(stderr)
		for _, c := range wanted {
			e.note(c, statusFailed, reason)
		}
		return ExitFailed
	}
	fmt.Fprintln(e.Stdout, "[entrypoint] boot-install ok")
	// npm exits 0 when an optional dependency cannot be fetched, and codex ships its binary as
	// one, so "installed" is not "runs": ok is recorded only by the verify pass.
	return e.NPMVerify(wanted, false)
}

// NPMVerify checks that each named CLI's ~/.local copy answers --version and repairs the ones
// that do not: the broken link is removed (left in place it would shadow a working baked copy
// and the next start's presence check would call it installed), then the pin is reinstalled once,
// space permitting. A CLI with no ~/.local copy is left alone when a baked copy takes over.
// present=true is the every-start pass: a copy that runs keeps the status this start already gave
// it, except that it clears an earlier "failed". Exit 0 when all run, 1 when one still does not,
// 3 when a repair was skipped for lack of space.
func (e Env) NPMVerify(clis []string, present bool) int {
	var fix, fixCLIs, broken []string
	for _, c := range clis {
		link := filepath.Join(e.localBin(), c)
		if e.runs(link) {
			if !present || e.lastStatus(c) == statusFailed {
				e.note(c, statusOK, "")
			}
			continue
		}
		_, statErr := os.Lstat(link)
		if statErr != nil && usable(filepath.Join(e.bakedBin(), c)) {
			continue
		}
		e.removeLink(link)
		pin := e.Pins[c]
		fmt.Fprintf(e.Stdout, "[entrypoint] WARN: %s in ~/.local does not run after the npm install", c)
		if pin != "" {
			fmt.Fprintf(e.Stdout, "; reinstalling the pin %s", pin)
			fix = append(fix, npmPkg(c)+"@"+pin)
			fixCLIs = append(fixCLIs, c)
		} else {
			broken = append(broken, c)
		}
		fmt.Fprintln(e.Stdout)
	}
	noSpace := false
	lastErr := ""
	if len(fix) > 0 {
		if !e.room(int64(len(fix))*npmPerPkgMiB, fixCLIs, strings.Join(fix, " ")) {
			noSpace = true // room recorded them
		} else {
			stderr, err := e.npmInstall(fix)
			if err != nil {
				fmt.Fprint(e.Stderr, stderr)
				lastErr = stderr
			}
			for _, c := range fixCLIs {
				// A failed repair run can leave a copy that was placed before it stopped:
				// that copy is not the pin and must not be recorded as repaired.
				if err == nil && e.runs(filepath.Join(e.localBin(), c)) {
					fmt.Fprintf(e.Stdout, "[entrypoint] %s repaired at the pin %s\n", c, e.Pins[c])
					e.note(c, statusOK, "")
				} else {
					broken = append(broken, c)
				}
			}
		}
	}
	for _, c := range broken {
		if e.removeLink(filepath.Join(e.localBin(), c)) {
			fmt.Fprintf(e.Stdout, "[entrypoint] WARN: %s does not run; removed its ~/.local link (retrying next start)\n", c)
		} else {
			fmt.Fprintf(e.Stdout, "[entrypoint] WARN: %s does not run (retrying next start)\n", c)
		}
		e.note(c, statusFailed, reasonFromStderr(lastErr))
	}
	// No space first: the entrypoint holds back its later downloads on it, and the individual
	// failures are in the status file either way.
	switch {
	case noSpace:
		return ExitNoSpace
	case len(broken) > 0:
		return ExitFailed
	}
	return ExitOK
}

// removeLink removes a copy that does not run and reports whether it is gone; a failure is said
// out loud, because a copy left in place keeps shadowing a baked one.
func (e Env) removeLink(link string) bool {
	if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(e.Stdout, "[entrypoint] WARN: could not remove %s: %v\n", link, err)
		return false
	}
	return true
}

// NPMVerifyPresent is NPMVerify for the CLIs that have a ~/.local copy, on every start and not
// only after an install: a copy broken by an earlier start, or already at its version so that no
// install ran, is otherwise never looked at again.
func (e Env) NPMVerifyPresent() int {
	var list []string
	for _, c := range npmCLIs {
		if _, err := os.Lstat(filepath.Join(e.localBin(), c.Name)); err == nil {
			list = append(list, c.Name)
		}
	}
	if len(list) == 0 {
		return ExitOK
	}
	return e.NPMVerify(list, true)
}
