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

// A root left by a binary that is gone is swept, with the tmux server in it; a live binary's
// root is not.
func TestSweepStaleRemovesOnlyDeadBinariesRoots(t *testing.T) {
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	stale, err := os.MkdirTemp("", fmt.Sprintf("%s%d-", rootPrefix, dead.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stale) })
	live, err := os.MkdirTemp("", fmt.Sprintf("%s%d-", rootPrefix, os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(live) })
	var sock string
	if realTmux != "" {
		sock = filepath.Join(stale, "tmux", "tmux-0", "left")
		if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(realTmux, "-S", sock, "new-session", "-d", "sleep 60").CombinedOutput(); err != nil {
			t.Fatalf("start the left-behind server: %v %s", err, out)
		}
	}
	sweepStale()
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("the dead binary's root %s is still there (%v)", stale, err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Errorf("the live binary's root was swept: %v", err)
	}
	if sock != "" {
		if err := exec.Command(realTmux, "-S", sock, "has-session").Run(); err == nil {
			_ = exec.Command(realTmux, "-S", sock, "kill-server").Run()
			t.Error("the left-behind tmux server is still running")
		}
	}
}
