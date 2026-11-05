package routegw

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// Caller binding, with real processes: the kernel's peer credentials, the real /proc and a real
// process tree. A helper is this test binary run again (TestHelperCaller); it reads a token per
// line on stdin, calls the gateway's socket with it, and writes "<status> <code>" back.

const helperEnv = "AF_ROUTEGW_TEST_HELPER_SOCKET"

func TestHelperCaller(t *testing.T) {
	sock := os.Getenv(helperEnv)
	if sock == "" {
		t.Skip("helper process only")
	}
	c := socketClient(sock)
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		req, _ := http.NewRequest("POST", SocketBaseURL+invokePath(), strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+in.Text())
		resp, err := c.Do(req)
		if err != nil {
			fmt.Printf("ANSWER 0 %s\n", err)
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var v struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(b, &v)
		fmt.Printf("ANSWER %d %s\n", resp.StatusCode, v.Code)
	}
}

// caller is a running helper process.
type caller struct {
	t   *testing.T
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Scanner
}

func startCaller(t *testing.T, sock string) *caller {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperCaller$", "-test.count=1")
	cmd.Env = append(os.Environ(), helperEnv+"="+sock)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := &caller{t: t, cmd: cmd, in: in, out: bufio.NewScanner(out)}
	t.Cleanup(func() { in.Close(); _ = cmd.Wait() })
	return c
}

func (c *caller) pid() int { return c.cmd.Process.Pid }

// present has the helper call with token and returns the status and refusal code it got.
func (c *caller) present(token string) (int, string) {
	c.t.Helper()
	if _, err := fmt.Fprintln(c.in, token); err != nil {
		c.t.Fatal(err)
	}
	for c.out.Scan() {
		f := strings.Fields(c.out.Text())
		if len(f) < 2 || f[0] != "ANSWER" {
			continue
		}
		st, _ := strconv.Atoi(f[1])
		code := ""
		if len(f) > 2 {
			code = f[2]
		}
		return st, code
	}
	c.t.Fatalf("the helper ended without an answer: %v", c.out.Err())
	return 0, ""
}

// Another session of the same member holds a valid token it read from this session's
// environment, and presents it from its own process: refused, nothing goes upstream, no
// approval request is filed on the owner's behalf, and the owner's token keeps working.
func TestAnotherSessionsProcessIsRefused(t *testing.T) {
	e := newEnv(t)
	tok := e.issue(e.session("owner", "p1", ""))
	e.session("other", "p1", "")
	own, other := startCaller(t, e.sock), startCaller(t, e.sock)
	e.mu.Lock()
	e.roots["owner"] = []int{own.pid()}
	e.roots["other"] = []int{other.pid()}
	e.mu.Unlock()

	if st, code := other.present(tok); st != http.StatusForbidden || code != "route_caller_forbidden" {
		t.Fatalf("another session's process = %d %s", st, code)
	}
	// The test process is the helpers' parent, outside the owner's tree.
	if resp, body := e.call(tok, invokePath(), `{}`); resp.StatusCode != http.StatusForbidden || codeOf(t, body) != "route_caller_forbidden" {
		t.Fatalf("a process outside the tree = %d %s", resp.StatusCode, body)
	}
	e.mu.Lock()
	e.held = false
	e.mu.Unlock()
	other.present(tok)
	if asked, credN := e.counts(); asked != 0 || credN != 0 || e.upstreamCalls() != 0 {
		t.Fatalf("a refused caller reached: asked %d, credentials %d, upstream %d", asked, credN, e.upstreamCalls())
	}
	e.mu.Lock()
	e.held = true
	e.mu.Unlock()
	if st, code := own.present(tok); st != http.StatusOK {
		t.Fatalf("the owner's process = %d %s (a refused foreign call must not revoke)", st, code)
	}
	// A process the owner's runtime started (a tool, a subagent) is in the tree: here the test
	// process is the root and the helper its child.
	e.mu.Lock()
	e.roots["owner"] = []int{os.Getpid()}
	e.mu.Unlock()
	if st, code := other.present(tok); st != http.StatusOK {
		t.Fatalf("a descendant of the owner's root = %d %s", st, code)
	}
	// The owner's runtime gone: nobody is in its tree.
	e.mu.Lock()
	e.roots["owner"] = nil
	e.mu.Unlock()
	if st, code := own.present(tok); st != http.StatusForbidden || code != "route_caller_forbidden" {
		t.Fatalf("with no running owner = %d %s", st, code)
	}
}

// A connection whose peer the kernel cannot name (TCP) is refused whatever token it carries.
func TestACallerWithoutPeerCredentialsIsRefused(t *testing.T) {
	e := newEnv(t)
	tok := e.issue(e.session("s1", "p1", ""))
	srv := httptest.NewServer(e.g)
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL+invokePath(), strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || codeOf(t, string(b)) != "route_caller_forbidden" || e.upstreamCalls() != 0 {
		t.Fatalf("over TCP = %d %s", resp.StatusCode, b)
	}
}

// fakeProc is a /proc of stat files: pid → ppid, start ticks and state.
type fakeProc struct {
	t    *testing.T
	root string
}

func newFakeProc(t *testing.T) *fakeProc { return &fakeProc{t: t, root: t.TempDir()} }

func (f *fakeProc) set(pid, ppid int, start uint64, state byte) {
	f.t.Helper()
	dir := filepath.Join(f.root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		f.t.Fatal(err)
	}
	// Fields after the comm: state ppid pgrp session, then 15 more, then starttime (proc(5) 22).
	stat := fmt.Sprintf("%d (p %d) %c %d %d %d 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 %d 0 0", pid, pid, state, ppid, pid, pid, start)
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeProc) remove(pid int) { _ = os.RemoveAll(filepath.Join(f.root, strconv.Itoa(pid))) }

// inTree walks parents through /proc and fails closed: every process on the chain, the root
// included, must be readable, alive and started no later than its child, and the caller must
// still be the process pinned at accept.
func TestInTree(t *testing.T) {
	f := newFakeProc(t)
	f.set(1, 0, 1, 'S')
	f.set(100, 1, 100, 'S')   // pane
	f.set(101, 100, 101, 'S') // claude
	f.set(102, 101, 102, 'S') // a tool
	f.set(200, 1, 200, 'S')   // another pane
	f.set(300, 301, 300, 'S') // a loop
	f.set(301, 300, 301, 'S')
	f.set(400, 999, 400, 'S') // a parent that is gone
	f.set(500, 500, 500, 'S') // its own parent
	f.set(600, 100, 600, 'Z') // a zombie under the pane
	f.set(700, 600, 700, 'S') // the zombie's child (its parent has not reaped it yet)
	f.set(800, 1, 800, 'Z')   // a zombie pane
	f.set(801, 800, 801, 'S')
	f.set(900, 950, 900, 'S') // its parent's pid was reused by a later process
	f.set(950, 100, 960, 'S')
	id := func(pid int) procID { return procID{pid: pid, start: uint64(pid)} }
	cases := []struct {
		name  string
		id    procID
		roots []int
		want  bool
	}{
		{"the root itself", id(100), []int{100}, true},
		{"a descendant", id(102), []int{100}, true},
		{"another pane's tree", id(102), []int{200}, false},
		{"another pane", id(200), []int{100}, false},
		{"an ancestor", id(100), []int{102}, false},
		{"a loop", id(300), []int{100}, false},
		{"a parent gone", id(400), []int{100}, false},
		{"its own parent", id(500), []int{100}, false},
		{"no roots", id(102), nil, false},
		{"init", id(1), []int{1}, false},
		{"init as a root", id(102), []int{1}, false},
		{"pid 0", procID{}, []int{100}, false},
		{"the caller's pid reused by a later process", procID{pid: 102, start: 50}, []int{100}, false},
		{"a zombie caller", id(600), []int{100}, false},
		{"a zombie on the chain", id(700), []int{100}, false},
		{"a zombie root", id(801), []int{800}, false},
		{"a zombie root, matched directly", id(800), []int{800}, false},
		{"a parent started after its child", id(900), []int{100}, false},
		{"another listed root unreadable", id(102), []int{100, 4000}, true},
	}
	for _, c := range cases {
		if got := inTree(f.root, c.id, c.roots); got != c.want {
			t.Errorf("%s: inTree(%+v, %v) = %v, want %v", c.name, c.id, c.roots, got, c.want)
		}
	}
	// A root that is listed but cannot be read is never matched.
	f.remove(100)
	if inTree(f.root, id(102), []int{100}) || inTree(f.root, procID{pid: 100, start: 100}, []int{100}) {
		t.Error("a chain through a root that cannot be read was accepted")
	}
	if got := liveRoots(f.root, []int{100, 101, 800, 4000, 1}); len(got) != 1 || got[0] != 101 {
		t.Errorf("liveRoots = %v, want [101]", got)
	}
}

// A connection pins the process that connected. A keep-alive connection retained after its
// connector has gone is refused, even when the same pid shows up again inside the owner's tree.
func TestRetainedConnectionOfAGoneProcessIsRefused(t *testing.T) {
	f := newFakeProc(t)
	f.set(1, 0, 1, 'S')
	f.set(100, 1, 100, 'S')  // the owner's pane
	f.set(4242, 1, 300, 'S') // an outside process connects
	pinned := procID{pid: 4242, start: 300}
	if inTree(f.root, pinned, []int{100}) {
		t.Fatal("an outside process was accepted")
	}
	// It hands the connection on and exits; its pid comes back under the owner's pane.
	f.set(4242, 100, 900, 'S')
	if inTree(f.root, pinned, []int{100}) {
		t.Fatal("the retained connection was served as the process that now has its pid")
	}
	if !inTree(f.root, procID{pid: 4242, start: 900}, []int{100}) {
		t.Fatal("the new process itself, connecting on its own, was refused")
	}
}

// The real accept path: the kernel names the connecting process, it is pinned with its start
// time, and once it has exited it no longer counts, whatever its pid becomes.
func TestPeerIsPinnedAtAccept(t *testing.T) {
	dir, err := os.MkdirTemp("", "rgwp")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ln, err := listenSocket(filepath.Join(dir, "p.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c := startHolder(t, filepath.Join(dir, "p.sock"))
	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	id, ok := peerOf(conn)
	if !ok || id.pid != c.Process.Pid || id.start == 0 {
		t.Fatalf("peer = %+v %v, want the holder %d with its start time", id, ok, c.Process.Pid)
	}
	if !inTree("/proc", id, []int{c.Process.Pid}) || !inTree("/proc", id, []int{os.Getpid()}) {
		t.Fatal("the pinned holder is not in its own tree or its parent's")
	}
	if inTree("/proc", procID{pid: id.pid, start: id.start + 1}, []int{os.Getpid()}) {
		t.Fatal("another start time for the same pid was accepted")
	}
	_ = c.Process.Kill()
	_ = c.Wait()
	if inTree("/proc", id, []int{os.Getpid()}) || live("/proc", id) {
		t.Fatal("a connection whose process has exited still counts")
	}
}

// startHolder starts a helper that connects to sock and holds the connection until killed.
func startHolder(t *testing.T, sock string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHolder$", "-test.count=1")
	cmd.Env = append(os.Environ(), holderEnv+"="+sock)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
}

const holderEnv = "AF_ROUTEGW_TEST_HOLDER_SOCKET"

func TestHelperHolder(t *testing.T) {
	sock := os.Getenv(holderEnv)
	if sock == "" {
		t.Skip("helper process only")
	}
	c, err := net.Dial("unix", sock)
	if err != nil {
		os.Exit(3)
	}
	defer c.Close()
	_, _ = io.Copy(io.Discard, os.Stdin)
}

// The test process is in its own tree and in its parent's, not in a child's.
func TestInTreeOnTheRealProc(t *testing.T) {
	self, ok := readProc("/proc", os.Getpid())
	if !ok {
		t.Fatal("cannot read the test process")
	}
	me := procID{pid: os.Getpid(), start: self.StartTicks}
	if !inTree("/proc", me, []int{os.Getppid()}) {
		t.Fatal("the test process is not under its parent")
	}
	c := startCaller(t, "/nonexistent")
	if inTree("/proc", me, []int{c.pid()}) {
		t.Fatal("the parent was taken for a child's descendant")
	}
}

// Renewal through the real root lookup (paneRoots → tmux → /proc): a launch pane tmux reports
// dead (remain-on-exit), or alive but with a pid that no longer runs, lets the token expire;
// a live one keeps it.
func TestRenewalFollowsTheRealPaneLookup(t *testing.T) {
	gone := exec.Command(os.Args[0], "-test.run=^$")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		pid   func(*caller) int
		dead  string
		alive bool
	}{
		{"a live pane", func(c *caller) int { return c.pid() }, "0", true},
		{"a pane tmux reports dead", func(c *caller) int { return c.pid() }, "1", false},
		{"a pane whose pid has gone", func(*caller) int { return gone.Process.Pid }, "0", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.g.d.Roots, e.g.d.ProcRoot = paneRoots, "/proc"
			tok := e.issue(e.session("s1", "p1", ""))
			bin := t.TempDir()
			panes := filepath.Join(bin, "panes")
			line := fmt.Sprintf("%d %s claude; __af_ec=$?; %s \"$__af_ec\"\n", c.pid(startCaller(t, e.sock)), c.dead, session.ExitRecorder("s1"))
			if err := os.WriteFile(panes, []byte(line), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\ncat \"$AF_TEST_PANES\"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("AF_TMUX_SOCKET", "")
			t.Setenv("AF_TEST_PANES", panes)
			e.run(2 * TokenTTL)
			if _, ok := e.g.Registry().Lookup(tok); ok != c.alive {
				t.Fatalf("token alive after %v of sweeps = %v, want %v", 2*TokenTTL, ok, c.alive)
			}
		})
	}
}
