package bootinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
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
	parent := context.Background()
	if e.npmCtx != nil {
		parent = e.npmCtx
	}
	ctx, cancel := context.WithTimeout(parent, npmInstallTimeout)
	defer cancel()
	args := append([]string{"install", "-g", "--prefix", filepath.Join(e.Home, ".local")}, specs...)
	se := &tailWriter{max: 64 << 10}
	if e.npmLockFile != nil {
		// A supervisor holds the lock until npm's whole process group is gone, so a kill of this
		// process, or a postinstall child started with default child_process options (which does
		// not inherit the lock), cannot leave a writer running unlocked. /proc/self/exe is the
		// binary running now, even if its path was replaced or removed since it started.
		sup := append([]string{"boot-install", superviseStep, e.npmBin()}, args...)
		cmd := exec.CommandContext(ctx, "/proc/self/exe", sup...)
		cmd.ExtraFiles = []*os.File{e.npmLockFile}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
		cmd.WaitDelay = time.Second
		cmd.Stderr = se
		so := &tailWriter{max: 16 << 10} // the supervisor's own report; npm's stdout is /dev/null
		cmd.Stdout = so
		if err = cmd.Start(); err == nil {
			if e.afterNPMStart != nil {
				e.afterNPMStart(cmd.Process)
			}
			err = cmd.Wait()
			// Group kill and wait, as for a direct npm: nothing may write once this returns, and a
			// supervisor killed alone takes npm (same group) down with it.
			pgid := cmd.Process.Pid
			stuck := parseStuck(so.String())
			kill, grace := e.npmGroupKill, superviseGrace
			if kill == nil {
				kill = func(pgid int) { _ = syscall.Kill(-pgid, syscall.SIGKILL) }
			}
			if e.npmGrace > 0 {
				grace = e.npmGrace
			}
			kill(pgid)
			if !waitGroupGone(pgid, grace) {
				stuck = append(stuck, groupProcs(pgid)...)
				if err == nil {
					err = errors.New("npm's process group did not stop")
				}
			}
			if errors.Is(err, exec.ErrWaitDelay) {
				err = nil
			}
			// What still runs after SIGKILL and the grace may finish a write later: it is recorded
			// so that neither this start's rollback nor a later start's recovery restores under it.
			if runs, rerr := e.npmRecordStuck(stuck); runs {
				switch {
				case rerr != nil:
					err = fmt.Errorf("%w, and it could not be recorded in %s: %v (%v)", errNPMStuck, e.npmStuckPath(), rerr, err)
				default:
					err = fmt.Errorf("%w (recorded in %s; nothing is restored or installed until it is gone): %v", errNPMStuck, e.npmStuckPath(), err)
				}
			}
			return se.String(), err
		}
		// The supervisor could not be started at all: nothing ran, so run npm directly.
		err = nil
	}
	cmd := exec.CommandContext(ctx, e.npmBin(), args...)
	if e.npmLockFile != nil {
		cmd.ExtraFiles = []*os.File{e.npmLockFile}
	}
	// Postinstall scripts may outlive npm: kill the whole group at the deadline so none keeps
	// writing to ~/.local while the verify pass runs.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
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

// npmNoteSkips records "skipped" for each pinned CLI that is present and not in the adopted
// plan. It runs once per decision, so a plan computed twice does not write the status twice.
func (e Env) npmNoteSkips(wanted []string) {
	for _, c := range npmCLIs {
		if e.Pins[c.Name] == "" || e.npmPendingHas(c.Name) || !e.cliPresent(c.Name) {
			continue
		}
		in := false
		for _, w := range wanted {
			in = in || w == c.Name
		}
		if !in {
			e.note(c.Name, statusSkip, "")
		}
	}
}

// npmRecheck narrows an earlier plan once the lock is held. A CLI leaves the plan only when what
// is on disk now shows it settled (present and, with Repin, at the pin, or parked for a restore).
// An unreadable or empty `npm ls` is not evidence of that: the earlier answer stands, as it
// would without a re-read.
func (e Env) npmRecheck(wanted []string, cur map[string]string) (specs, kept []string) {
	for _, name := range wanted {
		var c struct{ Name, Pkg string }
		for _, k := range npmCLIs {
			if k.Name == name {
				c = k
			}
		}
		pin := e.Pins[name]
		if pin == "" || e.npmPendingHas(name) {
			continue
		}
		if e.cliPresent(name) {
			if !e.Repin {
				continue
			}
			if cur == nil {
				cur = e.npmVersions()
			}
			if cur[c.Pkg] == pin {
				continue
			}
		}
		specs = append(specs, c.Pkg+"@"+pin)
		kept = append(kept, name)
	}
	return specs, kept
}

// npmPlan is the pinned installs this start owes: the specs to hand npm and the CLI names they
// stand for. It reads the marker, the presence of each CLI and (with Repin) the installed
// versions, so it is asked again once the lock is held.
func (e Env) npmPlan(cur map[string]string) (specs, wanted []string) {
	for _, c := range npmCLIs {
		pin := e.Pins[c.Name]
		if pin == "" {
			continue
		}
		if e.npmPendingHas(c.Name) {
			continue // its previous copy is still waiting to be put back
		}
		present := e.cliPresent(c.Name)
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
	return specs, wanted
}

// NPM installs the pinned npm CLIs into ~/.local that are missing or (with Repin) drifted from
// the pin, in one npm run, then verifies each. It returns the process exit code.
func NPM(e Env) int {
	specs, wanted := e.npmPlan(nil)
	if len(specs) == 0 {
		e.npmNoteSkips(nil)
		fmt.Fprintln(e.Stdout, "[entrypoint] boot-install: npm CLIs already present in ~/.local (skip)")
		return ExitOK
	}
	// Nothing to install returned above without touching the lock; from here npm writes.
	e, release, ok := e.lockNPM()
	if !ok {
		if e.npmLockErr != nil && !e.room(int64(len(specs))*npmPerPkgMiB, wanted, strings.Join(specs, " ")) {
			return ExitNoSpace // answerable without writing
		}
		e.npmBusy("npm boot-install")
		return ExitFailed
	}
	defer release()
	if e.afterNPMLock != nil {
		e.afterNPMLock()
	}
	// The plan above was read before the lock: a self-update that held it meanwhile may have
	// installed, repinned or parked these CLIs. Decide again from what is on disk now.
	// Decide every pin again (a self-update that held the lock may have moved a CLI that was fine
	// before), and keep what the earlier plan owed unless the disk now shows it settled.
	// One `npm ls` for both: a second one that failed must not undo what the first showed settled.
	// The versions are read only for a CLI they can decide: npmPlan compares one with a ~/.local
	// copy, npmRecheck one of the earlier plan that is present now. With neither (no local copy
	// at all), no `npm ls` runs under the lock.
	var cur map[string]string
	if e.Repin && slices.ContainsFunc(npmCLIs, func(c struct{ Name, Pkg string }) bool {
		if e.Pins[c.Name] == "" {
			return false
		}
		_, err := os.Stat(filepath.Join(e.localBin(), c.Name))
		return err == nil || slices.Contains(wanted, c.Name) && e.cliPresent(c.Name)
	}) {
		cur = e.npmVersions()
	}
	if cur == nil {
		cur = map[string]string{}
	}
	_, fresh := e.npmPlan(cur)
	_, kept := e.npmRecheck(wanted, cur)
	wanted = wanted[:0:0]
	specs = nil
	for _, c := range npmCLIs {
		if !slices.Contains(fresh, c.Name) && !slices.Contains(kept, c.Name) {
			continue
		}
		wanted = append(wanted, c.Name)
		specs = append(specs, c.Pkg+"@"+e.Pins[c.Name])
	}
	e.npmNoteSkips(wanted)
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
	e, release, ok := e.lockNPM()
	if !ok {
		if e.npmLockErr != nil {
			return e.npmVerifyReadOnly(clis, present)
		}
		e.npmBusy("npm verify")
		return ExitFailed
	}
	defer release()
	var fix, fixCLIs, broken, stuckRestore []string
	defer func() {
		// Not under a writer the repair could not stop: narrowing the marker deletes backups.
		if len(stuckRestore) > 0 && e.npmStuckCheck() == nil {
			e.npmKeepPendingFor(stuckRestore)
		}
	}()
	for _, c := range clis {
		link := filepath.Join(e.localBin(), c)
		if e.runs(link) {
			if !present || e.lastStatus(c) == statusFailed {
				e.note(c, statusOK, "")
			}
			continue
		}
		// The every-start pass can follow an accepted self-update whose backups are still on disk:
		// one probe that failed must not cost the previous copy, so it goes back first.
		if present && e.npmPendingHas(c) {
			// An earlier restore of this CLI has not gone through: its links are the copy being
			// restored, so no repair or removal runs over them.
			stuckRestore = append(stuckRestore, c)
			continue
		}
		if present {
			switch restored, failed := e.npmRestoreCLI(c); {
			case failed:
				// The previous copy could not be put back: it stays recorded for the next start
				// and the pin repair below must not run over it.
				stuckRestore = append(stuckRestore, c)
				continue
			case restored && e.runs(link):
				fmt.Fprintf(e.Stdout, "[entrypoint] WARN: updated %s does not run; kept the previous copy\n", c)
				e.note(c, statusOK, "")
				continue
			}
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
	// A repair whose npm left a process running (errNPMStuck) removes nothing it may still write.
	writerStuck := len(fix) > 0 && e.npmStuckCheck() != nil
	for _, c := range broken {
		if writerStuck {
			fmt.Fprintf(e.Stdout, "[entrypoint] WARN: %s does not run; a process of its repair did not stop, so its ~/.local link is left (retrying next start)\n", c)
		} else if e.removeLink(filepath.Join(e.localBin(), c)) {
			fmt.Fprintf(e.Stdout, "[entrypoint] WARN: %s does not run; removed its ~/.local link (retrying next start)\n", c)
		} else {
			fmt.Fprintf(e.Stdout, "[entrypoint] WARN: %s does not run (retrying next start)\n", c)
		}
		e.note(c, statusFailed, reasonFromStderr(lastErr))
	}
	// No space first: the entrypoint holds back its later downloads on it, and the individual
	// failures are in the status file either way.
	for _, c := range stuckRestore {
		fmt.Fprintf(e.Stdout, "[entrypoint] WARN: %s does not run and its previous copy could not be restored yet (retrying next start)\n", c)
		e.note(c, statusFailed, ReasonOther)
	}
	switch {
	case noSpace:
		return ExitNoSpace
	case len(broken) > 0 || len(stuckRestore) > 0:
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
	list := e.npmPresentList()
	if len(list) == 0 {
		return ExitOK
	}
	return e.NPMVerify(list, true)
}

// npmPresentList is the CLIs NPMVerifyPresent looks at.
func (e Env) npmPresentList() []string {
	var list []string
	_, marked, mok, mbad := e.npmReadMarker()
	for _, c := range npmCLIs {
		_, err := os.Lstat(filepath.Join(e.localBin(), c.Name))
		// A CLI of an accepted update is looked at even when its new link vanished: its previous
		// copy may still be on disk.
		listed := false
		for _, m := range marked {
			listed = listed || (mok && !mbad && e.npmEntryCLI(m) == c.Name)
		}
		if err == nil || listed {
			list = append(list, c.Name)
		}
	}
	return list
}

// npmVerifyReadOnly is NPMVerify when the lock file cannot be opened: each CLI is probed once and
// nothing is removed, restored or installed. A copy that runs, or a missing ~/.local copy a baked
// one covers, is fine as in NPMVerify; a broken ~/.local copy is reported failed and left for a
// start that can take the lock.
func (e Env) npmVerifyReadOnly(clis []string, present bool) int {
	rc := ExitOK
	for _, c := range clis {
		link := filepath.Join(e.localBin(), c)
		if e.runs(link) {
			if !present || e.lastStatus(c) == statusFailed {
				e.note(c, statusOK, "")
			}
			continue
		}
		if _, err := os.Lstat(link); err != nil && usable(filepath.Join(e.bakedBin(), c)) {
			continue
		}
		fmt.Fprintf(e.Stdout, "[entrypoint] WARN: %s in ~/.local does not run; not repairing it without the lock (%s)\n", c, e.npmLockWhy())
		if e.lastStatus(c) != statusFailed {
			e.note(c, statusFailed, ReasonOther) // an earlier failure's reason (no_space) stays the last line
		}
		rc = ExitFailed
	}
	return rc
}

// npmLockPath is the lock every writer of the npm trees (an install) and every restore or
// delete of their backups holds.
func (e Env) npmLockPath() string { return filepath.Join(e.Home, ".local", ".af-npm.lock") }

// lockNPM takes the npm lock without waiting and returns an Env that carries it (npmInstall hands
// it to npm), plus its release. A holder that already has it (the Env carries it) is not asked
// again. ok is false when another process holds it: its npm may still be writing, so nothing here
// may restore, delete or install. The lock is an flock: it is dropped when the last process
// holding the file description exits, whatever way that happens.
//
// A process an earlier install could not stop (errNPMStuck) refuses the lock the same way as a
// file that cannot be opened, even to a holder that already has it: npmLockErr is set, so the
// destructive steps refuse and the answers that need no write still come.
func (e Env) lockNPM() (_ Env, release func(), ok bool) {
	if e.npmLockFile != nil {
		if err := e.npmStuckCheck(); err != nil {
			e.npmLockErr = err
			return e, func() {}, false
		}
		return e, func() {}, true
	}
	e.npmLockErr = nil // each attempt reports its own outcome
	if e.beforeNPMLock != nil {
		e.beforeNPMLock()
	}
	f, busy, err := flockPath(e.npmLockPath())
	if busy {
		return e, func() {}, false
	}
	if err != nil {
		// The lock file cannot be made or opened. Going on unlocked would let this process
		// restore or write under a holder that opened the file before its mode changed, so
		// every destructive step refuses (ok false, npmLockErr set). Callers that can answer
		// without writing (nothing to install, no space, every copy runs) check npmLockErr.
		e.npmLockErr = err
		return e, func() {}, false
	}
	if err := e.npmStuckCheck(); err != nil {
		f.Close()
		e.npmLockErr = err
		return e, func() {}, false
	}
	e.npmLockFile = f
	return e, func() { f.Close() }, true
}

// npmLockWhy says why npmLockErr refused the lock.
func (e Env) npmLockWhy() string {
	if errors.Is(e.npmLockErr, errNPMStuck) {
		return e.npmLockErr.Error()
	}
	return fmt.Sprintf("cannot open %s: %v", e.npmLockPath(), e.npmLockErr)
}

// flockPath takes an exclusive flock on path without waiting. busy is true only when another
// holder has it; err is non-nil when the lock could not be set up at all (the file cannot be
// made or opened, or flock failed for a reason other than contention). "Cannot open" is not
// "free": the caller must not treat it as the lock being available.
func flockPath(path string) (f *os.File, busy bool, err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, false, err
	}
	f, err = os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, true, nil
		}
		return nil, false, err
	}
	return f, false, nil
}

// npmBusy says why a step did nothing when the lock was not free.
func (e Env) npmBusy(what string) {
	if e.npmLockErr != nil {
		fmt.Fprintf(e.Stdout, "[entrypoint] WARN: %s skipped (%s; no destructive step runs without the lock)\n", what, e.npmLockWhy())
		return
	}
	fmt.Fprintf(e.Stdout, "[entrypoint] WARN: %s skipped (another process holds %s; an earlier npm may still be writing)\n", what, e.npmLockPath())
}

// npmAside is one path npm may replace, moved aside before a self-update install.
type npmAside struct{ cli, orig, bak string }

const npmBakSuffix = ".af-bak"

// npmMarkerDiscardLine opens the marker's second group (see npmWriteMarkerDiscard).
const npmMarkerDiscardLine = "accepted"

// npmMarker records one self-update's backups: a first line "pending" or "accepted", then the
// names of the CLIs whose trees were moved aside. Paths are never stored: recovery rebuilds a CLI's
// tree and bin link from its name (npmAsidesOf), so a corrupt marker cannot point a restore or a
// delete outside the npm paths, and an unknown entry refuses the whole marker before any
// destructive step. It is written before anything is moved. A killed start leaves it behind, and
// recovery then acts on the listed CLIs' backups: pending puts them back whatever the new copy
// looks like, accepted deletes them. Where no backup exists at a rebuilt path there is nothing to do.
func (e Env) npmMarker() string { return filepath.Join(e.Home, ".local", ".af-npm-update") }

func (e Env) npmWriteMarker(state string, clis []string) error {
	return e.npmWriteMarkerDiscard(state, clis, nil)
}

// npmWriteMarkerDiscard is npmWriteMarker for a pending marker that also records accepted CLIs
// whose backups could not be deleted: after a line "accepted" come their names. Recovery puts the
// first group back and deletes the second; leaving them unrecorded would make the next
// npmMoveAside refuse on the name collision for good.
func (e Env) npmWriteMarkerDiscard(state string, clis, discard []string) error {
	body := state + "\n" + strings.Join(clis, "\n") + "\n"
	if len(discard) > 0 {
		body += npmMarkerDiscardLine + "\n" + strings.Join(discard, "\n") + "\n"
	}
	// An exclusive temp file beside the marker: a fixed name could be the member's own file or a link.
	f, err := os.CreateTemp(filepath.Dir(e.npmMarker()), ".af-npm-update.tmp.")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.WriteString(body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, e.npmMarker()); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// npmReadMarker parses the marker: its state and its entries. An entry is a CLI name (both of
// its paths) or, from an earlier release, the exact tree or bin path of a known CLI under this
// home (that one path only: the other side was never recorded and is not ours). ok is false when
// there is no marker; bad is true when it is present but not one we wrote.
func (e Env) npmReadMarker() (state string, entries []string, ok, bad bool) {
	state, entries, _, ok, bad = e.npmReadMarkerFull()
	return
}

// npmReadMarkerFull is npmReadMarker plus the entries of the discard group, which only a pending
// marker may carry; any other marker with that line is not one we wrote.
func (e Env) npmReadMarkerFull() (state string, entries, discard []string, ok, bad bool) {
	b, err := os.ReadFile(e.npmMarker())
	if err != nil {
		return "", nil, nil, false, false
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	state = lines[0]
	if state != "pending" && state != "accepted" {
		return state, nil, nil, true, true
	}
	inDiscard := false
	for _, l := range lines[1:] {
		if l == "" {
			continue
		}
		if l == npmMarkerDiscardLine {
			if state != "pending" || inDiscard {
				return state, nil, nil, true, true
			}
			inDiscard = true
			continue
		}
		if e.npmEntryCLI(l) == "" {
			return state, nil, nil, true, true
		}
		if inDiscard {
			discard = append(discard, l)
		} else {
			entries = append(entries, l)
		}
	}
	for _, d := range discard {
		for _, k := range entries {
			if e.npmEntryCLI(d) == e.npmEntryCLI(k) {
				return state, nil, nil, true, true // one CLI in both groups
			}
		}
	}
	return state, entries, discard, true, false
}

// npmEntryCLI is the CLI a marker entry belongs to ("" for an entry we do not understand).
func (e Env) npmEntryCLI(entry string) string {
	if npmPkg(entry) != "" {
		return entry
	}
	for _, c := range npmCLIs {
		if entry == e.npmTreeOf(c.Pkg) || entry == filepath.Join(e.localBin(), c.Name) {
			return c.Name
		}
	}
	return ""
}

// npmAsidesOf rebuilds the backups the marker entries stand for: a name gives the CLI's tree and
// bin link, a legacy path only itself; each counts when its <path>.af-bak exists.
func (e Env) npmAsidesOf(entries []string) []npmAside {
	var out []npmAside
	for _, ent := range entries {
		c := e.npmEntryCLI(ent)
		paths := []string{ent}
		if c == ent {
			paths = []string{e.npmTreeOf(npmPkg(c)), filepath.Join(e.localBin(), c)}
		}
		for _, orig := range paths {
			bak := orig + npmBakSuffix
			// Only a confirmed ENOENT means there is no backup: an unreadable parent says nothing.
			if _, err := os.Lstat(bak); !errors.Is(err, fs.ErrNotExist) {
				out = append(out, npmAside{cli: c, orig: orig, bak: bak})
			}
		}
	}
	return out
}

// npmEntriesOf selects the marker entries that belong to one of the named CLIs.
func (e Env) npmEntriesOf(entries, clis []string) []string {
	var out []string
	for _, ent := range entries {
		for _, c := range clis {
			if e.npmEntryCLI(ent) == c {
				out = append(out, ent)
				break
			}
		}
	}
	return out
}

// npmPendingHas reports whether a pending marker still lists the CLI: its previous copy is waiting
// to be put back, so nothing may repair, reinstall or remove the CLI's links meanwhile.
func (e Env) npmPendingHas(cli string) bool {
	state, entries, ok, bad := e.npmReadMarker()
	return ok && !bad && state == "pending" && len(e.npmEntriesOf(entries, []string{cli})) > 0
}

// npmCLINames is the distinct CLI names of the entries, in order.
func npmCLINames(as []npmAside) []string {
	var out []string
	seen := map[string]bool{}
	for _, a := range as {
		if !seen[a.cli] {
			seen[a.cli] = true
			out = append(out, a.cli)
		}
	}
	return out
}

// npmTreeOf is the package tree of a CLI under lib/node_modules.
func (e Env) npmTreeOf(pkg string) string {
	return filepath.Join(e.Home, ".local", "lib", "node_modules", filepath.FromSlash(pkg))
}

// npmManagedBin reports whether bin is a link that npm created for the package tree (it points
// into it). A regular file, or a link to something else, belongs to someone else and is left
// alone: npm refuses to overwrite it (EEXIST), as on main.
func npmManagedBin(bin, tree string) bool {
	st, err := os.Lstat(bin)
	if err != nil || st.Mode()&os.ModeSymlink == 0 {
		return false
	}
	t, err := os.Readlink(bin)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(t) {
		t = filepath.Join(filepath.Dir(bin), t)
	}
	t = filepath.Clean(t)
	return t == tree || strings.HasPrefix(t, tree+string(filepath.Separator))
}

// npmRecoverAside settles what a killed or finished self-update left, by what its marker lists.
// Run it before anything else looks at the npm CLIs. The marker goes only when every listed
// backup is settled. keepAccepted leaves an accepted update's backups in place (the final
// verify pass of the same start may still need them: see npmRestoreCLI).
func (e Env) npmRecoverAside(keepAccepted bool) {
	if _, err := os.Lstat(e.npmMarker()); err != nil {
		return // nothing to settle; do not even take the lock
	}
	e, release, ok := e.lockNPM()
	if !ok {
		e.npmBusy("recovery of an earlier agent CLI update")
		return
	}
	defer release()
	state, clis, discard, ok, bad := e.npmReadMarkerFull()
	if !ok {
		return
	}
	if bad {
		// Not ours: act on nothing, and say so; the backups (if any) stay where they are.
		fmt.Fprintf(e.Stdout, "[entrypoint] WARN: ignoring an unreadable %s (backups, if any, stay as *%s)\n", e.npmMarker(), npmBakSuffix)
		return
	}
	all := e.npmAsidesOf(clis)
	if state == "accepted" {
		if keepAccepted {
			return
		}
		if !npmDiscardAside(all) {
			return // something stayed behind: keep the marker so the next start sweeps it again
		}
	} else {
		// The discard group holds accepted CLIs whose backups the update could not delete.
		restored := e.npmRestoreAside(all)
		if !npmDiscardAside(e.npmAsidesOf(discard)) || !restored {
			return // something stayed behind: keep the marker so the next start tries again
		}
	}
	os.Remove(e.npmMarker())
}

// npmRestoreCLI puts back the previous copy of one CLI whose accepted new copy turned out not to
// run, while the backup is still there. restored says a previous copy went back; failed says one
// is there and could not be put back.
func (e Env) npmRestoreCLI(cli string) (restored, failed bool) {
	state, entries, ok, bad := e.npmReadMarker()
	if !ok || bad || state != "accepted" {
		return false, false
	}
	as := e.npmAsidesOf(e.npmEntriesOf(entries, []string{cli}))
	if len(as) == 0 {
		return false, false
	}
	if e.npmRestoreAside(as) {
		return true, false
	}
	return false, true
}

// npmKeepPendingFor turns an accepted marker into a pending one that keeps only the entries of the
// CLIs whose restore failed, so the next start retries it; the other CLIs' backups (all verified
// by now) go.
func (e Env) npmKeepPendingFor(stuck []string) {
	state, entries, ok, bad := e.npmReadMarker()
	if !ok || bad || state != "accepted" {
		return
	}
	keep := e.npmEntriesOf(entries, stuck)
	var rest []string
	for _, ent := range entries {
		in := false
		for _, k := range keep {
			in = in || k == ent
		}
		if !in {
			rest = append(rest, ent)
		}
	}
	e.npmNarrowMarker(keep, rest)
}

// npmNarrowMarker turns the marker into a pending one for the entries in keep and deletes the
// backups of the accepted entries in drop. The marker records the drop group, as the same entries
// the caller holds (a legacy path stays that one path), before anything is deleted, so a failed
// or interrupted delete leaves the backups listed; when that write fails, nothing is deleted and
// the earlier marker stays.
func (e Env) npmNarrowMarker(keep, drop []string) {
	asides := e.npmAsidesOf(drop)
	if len(asides) == 0 {
		_ = e.npmWriteMarker("pending", keep)
		return
	}
	if err := e.npmWriteMarkerDiscard("pending", keep, drop); err != nil {
		return
	}
	if npmDiscardAside(asides) {
		_ = e.npmWriteMarker("pending", keep)
	}
}

// npmMoveAside renames the installed tree and npm's own bin link of every npm CLI to
// <path>.af-bak, so an install that dies half-way (ENOSPC, a kill) cannot take the only copy
// with it. A rename needs no extra space. A backup name that already exists is not ours (the
// marker would list it): the update is refused rather than overwrite it. On an error everything
// moved so far is put back.
func (e Env) npmMoveAside() ([]npmAside, error) {
	type cand struct{ cli, orig string }
	var plan []cand
	for _, c := range npmCLIs {
		tree := e.npmTreeOf(c.Pkg)
		bin := filepath.Join(e.localBin(), c.Name)
		if _, err := os.Lstat(tree); err == nil {
			plan = append(plan, cand{c.Name, tree})
		}
		if npmManagedBin(bin, tree) {
			plan = append(plan, cand{c.Name, bin})
		}
	}
	var clis []string
	for _, c := range plan {
		// The marker names the CLI, so recovery treats both of its paths as ours: neither may
		// carry a *.af-bak we did not make, whichever of the two is moved.
		for _, p := range []string{e.npmTreeOf(npmPkg(c.cli)), filepath.Join(e.localBin(), c.cli)} {
			if _, err := os.Lstat(p + npmBakSuffix); !errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("%s already exists; not overwriting it", p+npmBakSuffix)
			}
		}
		if len(clis) == 0 || clis[len(clis)-1] != c.cli {
			clis = append(clis, c.cli)
		}
	}
	if len(plan) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(e.npmMarker()), 0o755); err != nil {
		return nil, err
	}
	if err := e.npmWriteMarker("pending", clis); err != nil {
		return nil, err
	}
	var moved []npmAside
	for _, c := range plan {
		bak := c.orig + npmBakSuffix
		if err := os.Rename(c.orig, bak); err != nil {
			if e.npmRestoreAside(moved) {
				os.Remove(e.npmMarker())
				return nil, err
			}
			// The marker stays so the next start puts the rest back; the error says where.
			return nil, fmt.Errorf("%w (previous copies are kept as *%s under ~/.local)", err, npmBakSuffix)
		}
		moved = append(moved, npmAside{c.cli, c.orig, bak})
	}
	return moved, nil
}

// npmRestoreAside puts the moved paths back, discarding whatever npm wrote at their names. It
// reports whether every one went back.
func (e Env) npmRestoreAside(moved []npmAside) bool {
	return len(e.npmRestoreAsideFailed(moved)) == 0
}

// npmRestoreAsideFailed is npmRestoreAside that returns the entries that did not go back.
func (e Env) npmRestoreAsideFailed(moved []npmAside) []npmAside {
	var failed []npmAside
	for i := len(moved) - 1; i >= 0; i-- {
		m := moved[i]
		if _, err := os.Lstat(m.bak); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // already back
			}
			failed = append(failed, m) // unreadable says nothing about the backup
			fmt.Fprintf(e.Stdout, "[entrypoint] WARN: could not look at %s: %v\n", m.bak, err)
			continue
		}
		removeTree(m.orig)
		if err := os.Rename(m.bak, m.orig); err != nil {
			failed = append(failed, m)
			fmt.Fprintf(e.Stdout, "[entrypoint] WARN: could not restore %s: %v (previous copy left at %s)\n", m.orig, err, m.bak)
		}
	}
	return failed
}

// npmDiscardAside deletes the backups once the new copies are trusted and reports whether none
// is left.
func npmDiscardAside(moved []npmAside) bool {
	gone := true
	for _, m := range moved {
		removeTree(m.bak)
		// Only a confirmed ENOENT counts as gone: any other error (EACCES on the parent, say)
		// says nothing about the backup.
		if _, err := os.Lstat(m.bak); !errors.Is(err, fs.ErrNotExist) {
			gone = false
		}
	}
	return gone
}

// NPMVerifyPresentStep is the entrypoint's final pass: it recovers what a killed update left but
// keeps an accepted update's backups, verifies the copies, then settles the backups, all under one
// hold of the lock: a backup is deleted only after the pass that vouches for its new copy, and no
// other start can leave a backup in between.
func (e Env) NPMVerifyPresentStep() int {
	e, release, ok := e.lockNPM()
	if !ok && e.npmLockErr != nil {
		e, release, ok = e.lockNPM() // once more: an open that failed may have been transient
	}
	if !ok {
		if e.npmLockErr == nil {
			e.npmBusy("agent CLI verify")
			return ExitFailed
		}
		// No usable lock: nothing here takes it again, so nothing is recovered, removed or
		// settled; each copy is probed once.
		if _, err := os.Lstat(e.npmMarker()); err == nil {
			e.npmBusy("recovery of an earlier agent CLI update")
		}
		list := e.npmPresentList()
		if len(list) == 0 {
			return ExitOK
		}
		return e.npmVerifyReadOnly(list, true)
	}
	defer release()
	e.npmRecoverAside(true)
	rc := e.NPMVerifyPresent()
	e.npmRecoverAside(false)
	return rc
}
