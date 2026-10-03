package sessionx

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// guardTestProcess runs the package's tests in a process that cannot reach the workspace it
// runs in, and fails the run when a test typed into a pane it did not isolate.
//
// The delivery loops, mirrors and interim deliveries this package starts are goroutines that
// can outlive the test that started them, and t.Setenv restores PATH and HOME under them. A
// straggling peer delivery then ran the real tmux on the default socket, where every live
// session of the workspace sits, and peer fixture text reached a live session. Many tests also
// launch real sessions without naming a socket (measured: 33 new-session and 138 has-session
// per package run). So the process-wide values a restore falls back to are made harmless:
//
//   - PATH starts with a tmux that passes `-L` / `-S` (isolateAgentState's sockets) to the real
//     binary and sends every default-socket call to a server private to this process;
//   - HOME, every agent config dir and the sessions dir point into a scratch root;
//   - the variables that name this workspace's session, tmux client, Agent or Control Plane
//     are removed.
//
// Typing into a pane through the default socket (send-keys, paste-buffer, load-buffer,
// set-buffer) is what no test does on purpose — those go through a fake tmux or an isolated
// socket — and what a straggler does, so it fails the run after m.Run, naming the command.
// guardTmuxSocket is the private server default-socket calls go to; empty without tmux.
var guardTmuxSocket string

func guardTestProcess(m *testing.M) int {
	root, err := os.MkdirTemp("", "sessionx-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "test guard:", err)
		return 1
	}
	defer os.RemoveAll(root)
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "test guard:", err)
		return 1
	}
	// Without a real tmux there is no server to reach, and a shim on PATH would only make the
	// tests that skip without tmux run and fail.
	typed := filepath.Join(root, "typed.log")
	if real, err := exec.LookPath("tmux"); err == nil {
		guardTmuxSocket = fmt.Sprintf("sessionx-guard-%d", os.Getpid())
		defer exec.Command(real, "-L", guardTmuxSocket, "kill-server").Run()
		script := `#!/bin/sh
case "$1" in
  -L|-S) exec '` + real + `' "$@" ;;
  send-keys|paste-buffer|load-buffer|set-buffer|pasteb|loadb|setb|send) printf '%s\n' "$*" >> '` + typed + `' ;;
esac
exec '` + real + `' -L '` + guardTmuxSocket + `' "$@"
`
		if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "test guard:", err)
			return 1
		}
		_ = os.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	home := filepath.Join(root, "home")
	for k, v := range map[string]string{
		"HOME":              home,
		"AF_SESSIONS_DIR":   filepath.Join(root, "sessions"),
		"CLAUDE_CONFIG_DIR": filepath.Join(home, ".claude"),
		"CODEX_HOME":        filepath.Join(home, ".codex"),
		"COPILOT_HOME":      filepath.Join(home, ".copilot"),
		"KIRO_HOME":         filepath.Join(home, ".kiro"),
		"XDG_CONFIG_HOME":   filepath.Join(home, ".config"),
		"XDG_DATA_HOME":     filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME":    filepath.Join(home, ".cache"),
		"XDG_STATE_HOME":    filepath.Join(home, ".local", "state"),
	} {
		_ = os.Setenv(k, v)
	}
	for _, k := range []string{"AF_SESSION_NAME", "TMUX", "TMUX_PANE", "AF_TMUX_SOCKET",
		"AGENT_TOKEN", "AGENT_ADDR", "AF_CP_BASE_URL", "AF_CP_INTERNAL_URL"} {
		_ = os.Unsetenv(k)
	}
	code := m.Run()
	b, _ := os.ReadFile(typed)
	if v := guardViolations(b); len(v) > 0 {
		fmt.Fprintf(os.Stderr, "test guard: a test typed through the default tmux socket, where the workspace's live sessions are:\n\t%s\n",
			strings.Join(v, "\n\t"))
		return 1
	}
	return code
}

// guardSelfTestText is what the guard's own test types; its line, and only its line, is not a
// violation. The log is append-only and never rewritten: a straggler typing while the self-test
// runs must still fail the run.
var guardSelfTestText = fmt.Sprintf("guard-self-test-%d", os.Getpid())

// guardViolations is every recorded typing call except the self-test's.
func guardViolations(log []byte) []string {
	var out []string
	for _, l := range strings.Split(string(log), "\n") {
		if l == "" || strings.HasSuffix(l, " -l "+guardSelfTestText) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// The guard itself: a default-socket call is answered by the private server, and typing
// through it is recorded; a call naming an isolated socket goes to the real binary untouched.
func TestGuardKeepsTestsOffTheDefaultTmuxSocket(t *testing.T) {
	if guardTmuxSocket == "" {
		t.Skip("tmux not installed: there is no server to keep the tests off")
	}
	guard := filepath.Dir(filepath.Dir(firstOnPath(t, "tmux")))
	typed := filepath.Join(guard, "typed.log")
	before, _ := os.ReadFile(typed)
	probe := fmt.Sprintf("guard_probe_%d", os.Getpid())
	if out, err := exec.Command("tmux", "new-session", "-d", "-s", probe, "sleep 30").CombinedOutput(); err != nil {
		t.Fatalf("new-session through the guard: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", guardTmuxSocket, "kill-session", "-t", "="+probe).Run() })
	if err := exec.Command("tmux", "-L", guardTmuxSocket, "has-session", "-t", "="+probe).Run(); err != nil {
		t.Fatalf("a default-socket new-session did not land on the private server %s: %v", guardTmuxSocket, err)
	}
	_ = exec.Command("tmux", "send-keys", "-t", "%0", "-l", guardSelfTestText).Run()
	after, _ := os.ReadFile(typed)
	if got := string(after[len(before):]); !strings.Contains(got, "send-keys -t %0 -l "+guardSelfTestText+"\n") {
		t.Fatalf("typing through the default socket was not recorded: %q", got)
	}
}

// Only the self-test's own line is forgiven: another typing call recorded around it, a
// straggler's, is still a violation.
func TestGuardForgivesOnlyItsOwnLine(t *testing.T) {
	log := "send-keys -t %0 -l " + guardSelfTestText + "\n" +
		"send-keys -t %99 Enter\n" +
		"send-keys -t %0 -l " + guardSelfTestText + "-not\n"
	got := guardViolations([]byte(log))
	want := []string{"send-keys -t %99 Enter", "send-keys -t %0 -l " + guardSelfTestText + "-not"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("violations = %q, want %q", got, want)
	}
}

func firstOnPath(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
