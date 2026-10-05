// Package testguard runs a package's tests in a process that cannot reach the workspace it
// runs in, and fails the run when a test typed into a pane it did not isolate.
//
// Every test binary of this module calls Run from its TestMain; that it does, and that this
// package stays out of the product binary's dependency graph, is checked by machine
// (testguard_coverage_test.go in the module root). It is imported from _test.go files only.
//
// Goroutines a test starts (delivery loops, mirrors, interim deliveries) can outlive it, and
// t.Setenv restores PATH, HOME and AF_TMUX_SOCKET under them. A straggling peer delivery then
// ran the real tmux on the default socket, where every live session of the workspace sits,
// and peer fixture text reached a live session (#1550). So the process-wide values a restore
// falls back to are made harmless:
//
//   - TMUX_TMPDIR points into a scratch root, so no tmux call of this process, shimmed or not,
//     can find the workspace's server, and every server a test starts dies with the process;
//   - PATH starts with a tmux that passes `-L` / `-S` calls (isolated sockets) to the real
//     binary and sends every default-socket call to a server private to this process;
//   - HOME and the sessions dir point into the scratch root and the agent config dirs follow
//     HOME, with the Go caches pinned to where they were so a test that runs `go` does not
//     rebuild the world. A contract binary (keepCredentials) keeps HOME and the config dirs,
//     since the real CLIs it drives are signed in there; it still gets the scratch sessions
//     dir and the tmux isolation;
//   - the variables that name this workspace's session, tmux client, Agent or Control Plane
//     are removed.
//
// Typing into a pane (send-keys, paste-buffer, load-buffer, set-buffer) or starting a session
// through the default socket is what no test does on purpose — those go through a fake tmux or
// IsolateTmux — and what a straggler does, so it fails the run after m.Run, naming the command.
package testguard

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
)

var (
	socket   string // the private server default-socket calls go to; empty without tmux
	typedLog string // append-only record of every recorded default-socket call
	realTmux string
	tmuxDir  string
	realHome string // HOME before the guard; the guard's own test checks what became of it
)

// selfTestText is what the guard's own test types; its line, and only its line, is not a
// violation. The log is never rewritten: a straggler typing while the self-test runs must
// still fail the run.
var selfTestText = fmt.Sprintf("guard-self-test-%d", os.Getpid())

// Run applies the guard, then setup (may be nil: a package's own process-wide stubs and
// environment, which therefore win over the guard's), runs the tests, tears the private tmux
// servers down and returns the exit code for os.Exit.
func Run(m *testing.M, setup func()) int {
	if helperChild() {
		if setup != nil {
			setup()
		}
		return m.Run()
	}
	sweepStale()
	root, err := os.MkdirTemp("", fmt.Sprintf("%s%d-", rootPrefix, os.Getpid()))
	if err != nil {
		fmt.Fprintln(os.Stderr, "test guard:", err)
		return 1
	}
	defer os.RemoveAll(root)
	if err := apply(root); err != nil {
		fmt.Fprintln(os.Stderr, "test guard:", err)
		return 1
	}
	defer killServers()
	if setup != nil {
		setup()
	}
	code := m.Run()
	b, _ := os.ReadFile(typedLog)
	if v := violations(b); len(v) > 0 {
		fmt.Fprintf(os.Stderr, "test guard: a test typed through the default tmux socket, where the workspace's live sessions are:\n\t%s\n",
			strings.Join(v, "\n\t"))
		return 1
	}
	return code
}

// markerEnv names the scratch root of the guarded process; its children inherit it.
const markerEnv = "AF_TESTGUARD_ROOT"

// ownerFile, inside a root, holds the pid of the binary that created it. Together with the
// root's name, mode and owner it is what makes a directory a guard root: sweepStale removes and
// helperChild trusts nothing else.
const ownerFile = ".testguard-owner"

// helperChild reports whether this process is a test binary a guarded test started as a
// helper (a fake CLI or MCP server re-exec'd with -test.run). Its environment is what that
// test built on purpose — its own HOME, a session name the test asserts on — so applying the
// guard again would overwrite it. It is a helper only when it runs the same executable as a
// guarded ancestor: the direct parent (an explicit environment, no marker), or the live owner
// of the root the inherited marker names (through a shell). A stale or forged marker, or
// another binary — a nested go test — gets a guard of its own.
func helperChild() bool {
	self, err := os.Readlink("/proc/self/exe")
	if err != nil {
		return false
	}
	if exeOf(os.Getppid()) == self {
		return true
	}
	root := os.Getenv(markerEnv)
	if root == "" {
		return false
	}
	owner, ok := rootOwner(root)
	return ok && exeOf(owner) == self && isAncestor(owner)
}

func exeOf(pid int) string {
	p, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return ""
	}
	return p
}

// isAncestor reports whether pid is a parent, grandparent, … of this process.
func isAncestor(pid int) bool {
	cur := os.Getppid()
	for i := 0; i < 64 && cur > 1; i++ {
		if cur == pid {
			return true
		}
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", cur))
		if err != nil {
			return false
		}
		// The ppid is the second field after the parenthesised command, which may hold spaces.
		j := strings.LastIndexByte(string(b), ')')
		if j < 0 {
			return false
		}
		f := strings.Fields(string(b[j+1:]))
		if len(f) < 2 {
			return false
		}
		if cur, err = strconv.Atoi(f[1]); err != nil {
			return false
		}
	}
	return false
}

// rootOwner returns the pid that created root, when root is a guard root: a real directory
// (not a symlink) in the temp dir, named rootPrefix<pid>-…, mode 0700, owned by this uid, with
// an ownerFile (a regular file) naming the same pid.
func rootOwner(root string) (int, bool) {
	if filepath.Dir(root) != filepath.Clean(os.TempDir()) {
		return 0, false
	}
	pidStr, _, ok := strings.Cut(strings.TrimPrefix(filepath.Base(root), rootPrefix), "-")
	pid, err := strconv.Atoi(pidStr)
	if !ok || !strings.HasPrefix(filepath.Base(root), rootPrefix) || err != nil || pid <= 0 {
		return 0, false
	}
	if !ownDir(root) || ownMode(root, 0) != fs.ModeDir|0o700 {
		return 0, false
	}
	owner := filepath.Join(root, ownerFile)
	if !ownedRegular(owner) {
		return 0, false
	}
	b, err := os.ReadFile(owner)
	if err != nil || strings.TrimSpace(string(b)) != pidStr {
		return 0, false
	}
	return pid, true
}

// ownMode is p's type and permission bits without following a symlink, or mode when p cannot
// be read or is not this uid's.
func ownMode(p string, mode fs.FileMode) fs.FileMode {
	fi, err := os.Lstat(p)
	if err != nil {
		return mode
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Getuid() {
		return mode
	}
	return fi.Mode() & (fs.ModeType | fs.ModePerm)
}

func ownDir(p string) bool { return ownMode(p, 0)&fs.ModeType == fs.ModeDir }

func ownedRegular(p string) bool { return ownMode(p, fs.ModeIrregular).IsRegular() }

// rootSockets lists the tmux sockets under a guard root's TMUX_TMPDIR without following a
// link anywhere on the way: root/tmux and root/tmux/tmux-<uid> must be this uid's real
// directories, and only real sockets of this uid are returned.
func rootSockets(dir string) []string {
	uidDir := filepath.Join(dir, fmt.Sprintf("tmux-%d", os.Getuid()))
	if !ownDir(dir) || !ownDir(uidDir) {
		return nil
	}
	ents, _ := os.ReadDir(uidDir)
	var out []string
	for _, e := range ents {
		p := filepath.Join(uidDir, e.Name())
		if ownMode(p, 0)&fs.ModeType == fs.ModeSocket {
			out = append(out, p)
		}
	}
	return out
}

func apply(root string) error {
	if err := os.WriteFile(filepath.Join(root, ownerFile), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		return err
	}
	bin := filepath.Join(root, "bin")
	tmuxDir = filepath.Join(root, "tmux")
	for _, d := range []string{bin, tmuxDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	// The Go caches default to paths under HOME; pin them before HOME moves.
	if os.Getenv("GOCACHE") == "" {
		if d, err := os.UserCacheDir(); err == nil {
			_ = os.Setenv("GOCACHE", filepath.Join(d, "go-build"))
		}
	}
	if os.Getenv("GOENV") == "" {
		if d, err := os.UserConfigDir(); err == nil {
			_ = os.Setenv("GOENV", filepath.Join(d, "go", "env"))
		}
	}
	if os.Getenv("GOPATH") == "" {
		if h, err := os.UserHomeDir(); err == nil {
			_ = os.Setenv("GOPATH", filepath.Join(h, "go"))
		}
	}
	if os.Getenv("GOMODCACHE") == "" && os.Getenv("GOPATH") != "" {
		_ = os.Setenv("GOMODCACHE", filepath.Join(filepath.SplitList(os.Getenv("GOPATH"))[0], "pkg", "mod"))
	}
	_ = os.Setenv("TMUX_TMPDIR", tmuxDir)
	typedLog = filepath.Join(root, "typed.log")
	// Without a real tmux there is no server to reach, and a shim on PATH would only make the
	// tests that skip without tmux run and fail.
	if real, err := exec.LookPath("tmux"); err == nil {
		realTmux = real
		socket = fmt.Sprintf("testguard-%d", os.Getpid())
		script := `#!/bin/sh
case "$1" in
  -L|-S) exec '` + real + `' "$@" ;;
  send-keys|paste-buffer|load-buffer|set-buffer|pasteb|loadb|setb|send|new-session|new) printf '%s\n' "$*" >> '` + typedLog + `' ;;
esac
exec '` + real + `' -L '` + socket + `' "$@"
`
		if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
			return err
		}
		_ = os.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	realHome = os.Getenv("HOME")
	_ = os.Setenv("AF_SESSIONS_DIR", filepath.Join(root, "sessions"))
	if !keepCredentials {
		_ = os.Setenv("HOME", filepath.Join(root, "home"))
		// Unset rather than pointed into the scratch root: each defaults to a path under HOME,
		// so a test that takes its own HOME gets the dirs under it, as in production. In this
		// container CLAUDE_CONFIG_DIR points at the live fleet's tree.
		for _, k := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "COPILOT_HOME", "KIRO_HOME",
			"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
			_ = os.Unsetenv(k)
		}
	}
	for _, k := range []string{"AF_SESSION_NAME", "TMUX", "TMUX_PANE", "AF_TMUX_SOCKET", "AF_WORK_DIR",
		"AGENT_TOKEN", "AGENT_ADDR", "AF_CP_BASE_URL", "AF_CP_INTERNAL_URL"} {
		_ = os.Unsetenv(k)
	}
	_ = os.Setenv(markerEnv, root)
	return nil
}

var isolatedSeq atomic.Int64

// IsolateTmux gives the test a tmux server of its own and returns its socket name. Every tmux
// call of the product goes through tmuxx.Cmd, which honours AF_TMUX_SOCKET, so a test that
// launches real sessions stays off the default socket — the one the workspace's live sessions
// sit on — without relying on Run. The server is killed when the test ends. It cannot be used
// from a parallel test (t.Setenv).
func IsolateTmux(t testing.TB) string {
	t.Helper()
	s := fmt.Sprintf("tg-%d-%d", os.Getpid(), isolatedSeq.Add(1))
	t.Setenv("AF_TMUX_SOCKET", s)
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", s, "kill-server").Run() })
	return s
}

// rootPrefix starts every scratch root's name; the creating test binary's pid follows it.
const rootPrefix = "af-testguard-"

// sweepStale removes the scratch roots of guarded test binaries that are gone. A binary that
// dies without returning from Run (a -timeout panic, a kill) leaves its root and any tmux
// server in it behind in the shared /tmp. A root whose pid is alive is left alone, whoever
// that pid now belongs to.
func sweepStale() {
	roots, _ := filepath.Glob(filepath.Join(os.TempDir(), rootPrefix+"*-*"))
	for _, r := range roots {
		pid, ok := rootOwner(r)
		if !ok || syscall.Kill(pid, 0) != syscall.ESRCH {
			continue
		}
		if realTmux, err := exec.LookPath("tmux"); err == nil {
			for _, s := range rootSockets(filepath.Join(r, "tmux")) {
				_ = exec.Command(realTmux, "-S", s, "kill-server").Run()
			}
		}
		_ = os.RemoveAll(r)
	}
}

// killServers stops every tmux server whose socket sits under the scratch TMUX_TMPDIR: the
// private one and any isolated server a test left behind. tmux removes a socket when its
// server exits, and the scratch root is removed after this, so nothing is left in /tmp.
func killServers() {
	if realTmux == "" {
		return
	}
	for _, s := range rootSockets(tmuxDir) {
		_ = exec.Command(realTmux, "-S", s, "kill-server").Run()
	}
}

// violations is every recorded call except the self-test's: a line naming selfTestText as
// one of its arguments.
func violations(log []byte) []string {
	var out []string
lines:
	for _, l := range strings.Split(string(log), "\n") {
		if l == "" {
			continue
		}
		for _, f := range strings.Fields(l) {
			if f == selfTestText {
				continue lines
			}
		}
		out = append(out, l)
	}
	return out
}
