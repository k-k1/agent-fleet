package tmuxx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// fakeTmux puts a "tmux" first on PATH that runs script.
func fakeTmux(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AF_TMUX_SOCKET", "")
}

func serverRuns(t *testing.T, runs bool) {
	t.Helper()
	old := serverMayRun
	t.Cleanup(func() { serverMayRun = old })
	serverMayRun = func(string) bool { return runs }
}

// A delete decides on LiveSessionNamesChecked, so only tmux's own "nothing there" may read as an
// empty set; a server it cannot reach or any other failure is an error, not "nothing runs".
func TestLiveSessionNamesCheckedTellsAbsentFromFailed(t *testing.T) {
	serverRuns(t, false)
	for _, c := range []struct {
		name, script string
		want         []string
		fails        bool
	}{
		{"listed", "echo " + session.TmuxPrefix + "aa1\necho other\necho " + session.TmuxPrefix + "bb2\n", []string{"aa1", "bb2"}, false},
		{"no server", "echo 'no server running on /tmp/tmux-1/default' >&2\nexit 1\n", nil, false},
		{"no socket, no server process", "echo 'error connecting to /tmp/tmux-1/default (No such file or directory)' >&2\nexit 1\n", nil, false},
		{"permission denied", "echo 'error connecting to /tmp/tmux-1/default (Permission denied)' >&2\nexit 1\n", nil, true},
		{"other exit", "echo 'boom' >&2\nexit 2\n", nil, true},
		{"exit 1 without a word", "exit 1\n", nil, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			fakeTmux(t, c.script)
			live, err := LiveSessionNamesChecked()
			if (err != nil) != c.fails {
				t.Fatalf("err = %v, want failure %v", err, c.fails)
			}
			if len(live) != len(c.want) {
				t.Fatalf("live = %v, want %v", live, c.want)
			}
			for _, n := range c.want {
				if !live[n] {
					t.Fatalf("live = %v, want %v", live, c.want)
				}
			}
			if got := LiveSessionNames(); len(got) != len(c.want) {
				t.Fatalf("LiveSessionNames = %v, want %v", got, c.want)
			}
		})
	}
}

// A socket that is gone, or no tmux binary to ask, means nothing runs only when no tmux server
// process for the socket exists either: a server outlives both.
func TestLiveSessionNamesCheckedAsksForAServerProcess(t *testing.T) {
	noSocket := "echo 'error connecting to /tmp/tmux-1/default (No such file or directory)' >&2\nexit 1\n"
	for _, c := range []struct {
		name      string
		noBinary  bool
		serverRun bool
		fails     bool
	}{
		{"no socket, no server", false, false, false},
		{"no socket, a server", false, true, true},
		{"no listener, a server", false, true, true},
		{"no binary, no server", true, false, false},
		{"no binary, a server", true, true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			serverRuns(t, c.serverRun)
			switch {
			case c.noBinary:
				t.Setenv("PATH", t.TempDir())
				t.Setenv("AF_TMUX_SOCKET", "")
			case strings.HasPrefix(c.name, "no listener"):
				fakeTmux(t, "echo 'no server running on /tmp/tmux-1/default' >&2\nexit 1\n")
			default:
				fakeTmux(t, noSocket)
			}
			live, err := LiveSessionNamesChecked()
			if (err != nil) != c.fails || len(live) != 0 {
				t.Fatalf("live = %v err = %v, want failure %v", live, err, c.fails)
			}
		})
	}
}

// The socket a server process serves is read from tmux's global options only, which come before
// the command, and from its TMUX_TMPDIR, the way tmux derives it.
func TestServerSocketFlags(t *testing.T) {
	for _, c := range []struct {
		args        []string
		label, path string
	}{
		{[]string{"tmux", "new-session", "-d", "-s", "x"}, "", ""},
		{[]string{"tmux", "-L", "e2e", "new-session", "-d"}, "e2e", ""},
		{[]string{"tmux", "-Le2e", "new-session"}, "e2e", ""},
		{[]string{"tmux", "-S", "/tmp/s", "new-session"}, "", "/tmp/s"},
		{[]string{"tmux", "-f", "/x.conf", "-L", "a", "new-session"}, "a", ""},
		// Options of the command, and a shell command that mentions -L, are not the server's.
		{[]string{"tmux", "new-session", "-d", "ls -L", "-L", "z"}, "", ""},
	} {
		label, path := serverSocketFlags(c.args)
		if label != c.label || path != c.path {
			t.Errorf("serverSocketFlags(%q) = %q %q, want %q %q", c.args, label, path, c.label, c.path)
		}
	}
	uid := strconv.Itoa(os.Getuid())
	if got := socketPath("", "", ""); got != "/tmp/tmux-"+uid+"/default" {
		t.Errorf("default socket = %q", got)
	}
	if got := socketPath("/x", "e2e", ""); got != "/x/tmux-"+uid+"/e2e" {
		t.Errorf("labelled socket = %q", got)
	}
	if got := socketNamed("error connecting to /a/b/s (No such file or directory)"); got != "/a/b/s" {
		t.Errorf("socketNamed = %q", got)
	}
	if got := socketNamed("no server running on /a/b/s\n"); got != "/a/b/s" {
		t.Errorf("socketNamed(no server) = %q", got)
	}
	// TMUX_TMPDIR through a link: tmux reports the resolved directory.
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "tmp")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	rr, _ := filepath.EvalSymlinks(real)
	if got := socketPath(link, "x", ""); got != filepath.Join(rr, "tmux-"+uid, "x") {
		t.Errorf("socket under a linked TMUX_TMPDIR = %q", got)
	}
	// Cmd's socket: -L AF_TMUX_SOCKET first, then an inherited $TMUX, then the default.
	t.Setenv("TMUX_TMPDIR", "")
	t.Setenv("AF_TMUX_SOCKET", "")
	t.Setenv("TMUX", "/s/inherited,123,0")
	if got := ownSocket(); got != "/s/inherited" {
		t.Errorf("ownSocket with $TMUX = %q", got)
	}
	t.Setenv("AF_TMUX_SOCKET", "lab")
	if got := ownSocket(); got != "/tmp/tmux-"+uid+"/lab" {
		t.Errorf("ownSocket with AF_TMUX_SOCKET = %q", got)
	}
}

// The host's own server, if one runs, is found for its own socket and not for another.
func TestTmuxServerMayRunMatchesTheSocket(t *testing.T) {
	needProc(t)
	if tmuxServerMayRun(filepath.Join(t.TempDir(), "no-such-socket")) {
		t.Fatal("a socket no server serves reads as served")
	}
}

// A server of its own, on a private socket, is found by its socket path; once its socket file is
// removed the server still runs, and the read says it cannot tell rather than "nothing runs".
func TestTmuxServerMayRunFindsARunningServer(t *testing.T) {
	needProc(t)
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	label := "afprobe-" + strconv.Itoa(os.Getpid())
	if out, err := Cmd("-L", label, "-f", "/dev/null", "new-session", "-d", "-s", session.TmuxPrefix+"probe", "sleep 60").CombinedOutput(); err != nil {
		t.Skipf("could not start a private tmux server: %v %s", err, out)
	}
	t.Cleanup(func() { _ = Cmd("-L", label, "kill-server").Run() })
	sock := socketPath(os.Getenv("TMUX_TMPDIR"), label, "")
	if !tmuxServerMayRun(sock) {
		t.Fatal("a running server was not found for its socket")
	}
	t.Setenv("AF_TMUX_SOCKET", label)
	if live, err := LiveSessionNamesChecked(); err != nil || !live["probe"] {
		t.Fatalf("live = %v err = %v, want the probe session", live, err)
	}
	if err := os.Remove(sock); err != nil {
		t.Skipf("could not remove the socket: %v", err)
	}
	if live, err := LiveSessionNamesChecked(); err == nil {
		t.Fatalf("a server whose socket is gone read as %v, no error", live)
	}
}
