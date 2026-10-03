package testguard

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) { os.Exit(Run(m, nil)) }

// The guard itself: a default-socket call is answered by the private server, and typing
// through it is recorded; a call naming an isolated socket goes to the real binary untouched.
func TestGuardKeepsTestsOffTheDefaultTmuxSocket(t *testing.T) {
	if socket == "" {
		t.Skip("tmux not installed: there is no server to keep the tests off")
	}
	before, _ := os.ReadFile(typedLog)
	probe := selfTestText
	if out, err := exec.Command("tmux", "new-session", "-d", "-s", probe, "sleep 30").CombinedOutput(); err != nil {
		t.Fatalf("new-session through the guard: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-session", "-t", "="+probe).Run() })
	if err := exec.Command("tmux", "-L", socket, "has-session", "-t", "="+probe).Run(); err != nil {
		t.Fatalf("a default-socket new-session did not land on the private server %s: %v", socket, err)
	}
	_ = exec.Command("tmux", "send-keys", "-t", "%0", "-l", selfTestText).Run()
	after, _ := os.ReadFile(typedLog)
	if got := string(after[len(before):]); !strings.Contains(got, "send-keys -t %0 -l "+selfTestText+"\n") {
		t.Fatalf("typing through the default socket was not recorded: %q", got)
	}
}

// A tmux that skips the shim (an absolute path, or a test PATH without it) still cannot find
// the workspace's server: its default socket resolves under the scratch TMUX_TMPDIR.
func TestGuardMovesTheDefaultSocketDirectory(t *testing.T) {
	if realTmux == "" {
		t.Skip("tmux not installed")
	}
	dir := os.Getenv("TMUX_TMPDIR")
	if dir == "" || dir != tmuxDir {
		t.Fatalf("TMUX_TMPDIR = %q, want the scratch %q", dir, tmuxDir)
	}
	probe := "guard_unshimmed"
	if out, err := exec.Command(realTmux, "new-session", "-d", "-s", probe, "sleep 30").CombinedOutput(); err != nil {
		t.Fatalf("new-session on the real binary: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command(realTmux, "kill-server").Run() })
	socks, _ := filepath.Glob(filepath.Join(dir, "tmux-*", "default"))
	if len(socks) != 1 {
		t.Fatalf("the unshimmed default server's socket is not under %s: %v", dir, socks)
	}
}

// Only the self-test's own line is forgiven: another typing call recorded around it, a
// straggler's, is still a violation.
func TestGuardForgivesOnlyItsOwnLine(t *testing.T) {
	log := "send-keys -t %0 -l " + selfTestText + "\n" +
		"send-keys -t %99 Enter\n" +
		"send-keys -t %0 -l " + selfTestText + "-not\n"
	got := violations([]byte(log))
	want := []string{"send-keys -t %99 Enter", "send-keys -t %0 -l " + selfTestText + "-not"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("violations = %q, want %q", got, want)
	}
}

// The scratch environment is in place for every test, and the workspace's session, tmux
// client and Agent are not named in it.
func TestGuardScratchEnvironment(t *testing.T) {
	home := os.Getenv("HOME")
	if !strings.HasPrefix(home, filepath.Dir(tmuxDir)+string(filepath.Separator)) {
		t.Fatalf("HOME = %q is not under the scratch root %s", home, filepath.Dir(tmuxDir))
	}
	for _, k := range []string{"AF_SESSION_NAME", "TMUX", "TMUX_PANE", "AF_TMUX_SOCKET", "AGENT_TOKEN", "CLAUDE_CONFIG_DIR", "CODEX_HOME"} {
		if v, ok := os.LookupEnv(k); ok {
			t.Errorf("%s is still set (%q)", k, v)
		}
	}
	if os.Getenv("GOCACHE") == "" || strings.HasPrefix(os.Getenv("GOCACHE"), home) {
		t.Errorf("GOCACHE = %q: not pinned outside the scratch HOME", os.Getenv("GOCACHE"))
	}
}

// fakeRoot makes a directory shaped like a guard root for pid; owned writes the owner file.
func fakeRoot(t *testing.T, pid int, owned bool) string {
	t.Helper()
	r, err := os.MkdirTemp("", fmt.Sprintf("%s%d-", rootPrefix, pid))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(r) })
	if owned {
		if err := os.WriteFile(filepath.Join(r, ownerFile), []byte(fmt.Sprintf("%d\n", pid)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func deadPid(t *testing.T) int {
	t.Helper()
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	return c.Process.Pid
}

// startServer starts a tmux server on the socket at sock and stops it when the test ends.
func startServer(t *testing.T, sock string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(realTmux, "-S", sock, "new-session", "-d", "sleep 60").CombinedOutput(); err != nil {
		t.Fatalf("start a server on %s: %v %s", sock, err, out)
	}
	t.Cleanup(func() { _ = exec.Command(realTmux, "-S", sock, "kill-server").Run() })
}

func serverUp(sock string) bool {
	return exec.Command(realTmux, "-S", sock, "has-session").Run() == nil
}

// A root left by a binary that is gone is swept, with the tmux server in it. A live binary's
// root, and a directory that only looks like a root (no owner file), are not.
func TestSweepStaleRemovesOnlyDeadBinariesRoots(t *testing.T) {
	dead := deadPid(t)
	stale := fakeRoot(t, dead, true)
	forged := fakeRoot(t, dead, false)
	live := fakeRoot(t, os.Getpid(), true)
	var sock string
	if realTmux != "" {
		sock = filepath.Join(stale, "tmux", fmt.Sprintf("tmux-%d", os.Getuid()), "left")
		startServer(t, sock)
	}
	sweepStale()
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("the dead binary's root %s is still there (%v)", stale, err)
	}
	if _, err := os.Stat(forged); err != nil {
		t.Errorf("a directory without the owner file was swept: %v", err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Errorf("the live binary's root was swept: %v", err)
	}
	if sock != "" && serverUp(sock) {
		t.Error("the left-behind tmux server is still running")
	}
}

// A link inside a stale root is never followed: a server its tmux dir, uid dir or socket entry
// points at outside the root keeps running.
func TestSweepStaleFollowsNoLink(t *testing.T) {
	if realTmux == "" {
		t.Skip("tmux not installed")
	}
	outside := t.TempDir()
	outSock := filepath.Join(outside, fmt.Sprintf("tmux-%d", os.Getuid()), "outer")
	startServer(t, outSock)
	uid := fmt.Sprintf("tmux-%d", os.Getuid())

	viaDir := fakeRoot(t, deadPid(t), true)
	if err := os.Symlink(outside, filepath.Join(viaDir, "tmux")); err != nil {
		t.Fatal(err)
	}
	viaUID := fakeRoot(t, deadPid(t), true)
	_ = os.MkdirAll(filepath.Join(viaUID, "tmux"), 0o700)
	if err := os.Symlink(filepath.Dir(outSock), filepath.Join(viaUID, "tmux", uid)); err != nil {
		t.Fatal(err)
	}
	viaSock := fakeRoot(t, deadPid(t), true)
	_ = os.MkdirAll(filepath.Join(viaSock, "tmux", uid), 0o700)
	if err := os.Symlink(outSock, filepath.Join(viaSock, "tmux", uid, "default")); err != nil {
		t.Fatal(err)
	}
	sweepStale()
	if !serverUp(outSock) {
		t.Fatal("a server outside the swept roots was stopped through a link")
	}
	for _, r := range []string{viaDir, viaUID, viaSock} {
		if _, err := os.Stat(r); !os.IsNotExist(err) {
			t.Errorf("stale root %s is still there (%v)", r, err)
		}
	}
	if _, err := os.Stat(outSock); err != nil {
		t.Errorf("removing a root reached through a link: %v", err)
	}
}

// probeEnv makes TestHelperProbe report whether the guard ran in its process.
const probeEnv = "AF_TESTGUARD_TEST_PROBE"

func TestHelperProbe(t *testing.T) {
	if os.Getenv(probeEnv) == "" {
		t.Skip("only run as a child of TestHelperDetection")
	}
	fmt.Printf("guarded=%v\n", tmuxDir != "")
}

// A re-exec'd helper keeps the environment its test built, whether it is started directly or
// through a shell with the inherited marker; a stale or forged marker does not switch the
// guard off.
func TestHelperDetection(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	run := func(viaShell bool, marker string) string {
		t.Helper()
		args := []string{"-test.run=^TestHelperProbe$"}
		var c *exec.Cmd
		if viaShell {
			// Not exec: the shell stays the child's parent.
			c = exec.Command("sh", append([]string{"-c", `"$0" "$@"; exit $?`, self}, args...)...)
		} else {
			c = exec.Command(self, args...)
		}
		c.Env = append(os.Environ(), probeEnv+"=1", markerEnv+"="+marker)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("probe: %v %s", err, out)
		}
		for _, l := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(l, "guarded=") {
				return l
			}
		}
		t.Fatalf("probe printed no verdict: %s", out)
		return ""
	}
	root := filepath.Dir(tmuxDir)
	if got := run(false, ""); got != "guarded=false" {
		t.Errorf("direct re-exec, explicit env: %s, want the helper left alone", got)
	}
	if got := run(true, root); got != "guarded=false" {
		t.Errorf("through a shell with this process's marker: %s, want the helper left alone", got)
	}
	if got := run(true, "/nonexistent-leaked-marker"); got != "guarded=true" {
		t.Errorf("through a shell with a forged marker: %s, want a guard of its own", got)
	}
	stale := fakeRoot(t, deadPid(t), true)
	if got := run(true, stale); got != "guarded=true" {
		t.Errorf("through a shell with a dead owner's marker: %s, want a guard of its own", got)
	}
}
