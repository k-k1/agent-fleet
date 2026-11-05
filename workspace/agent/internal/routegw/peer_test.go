package routegw

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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

// inTree walks parents through /proc and fails closed.
func TestInTree(t *testing.T) {
	root := t.TempDir()
	write := func(pid, ppid int) {
		dir := filepath.Join(root, strconv.Itoa(pid))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		stat := fmt.Sprintf("%d (p %d) S %d %d %d 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0", pid, pid, ppid, pid, pid)
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(100, 1)   // pane
	write(101, 100) // claude
	write(102, 101) // a tool
	write(200, 1)   // another pane
	write(300, 301) // a loop
	write(301, 300)
	write(400, 999) // a parent that is gone
	write(500, 500) // its own parent
	write(1, 0)
	cases := []struct {
		pid   int
		roots []int
		want  bool
	}{
		{100, []int{100}, true},
		{102, []int{100}, true},
		{102, []int{200}, false},
		{200, []int{100}, false},
		{100, []int{102}, false}, // an ancestor is not in a descendant's tree
		{300, []int{100}, false},
		{400, []int{100}, false},
		{102, nil, false},
		{500, []int{100}, false},
		{1, []int{1}, false},
		{102, []int{1}, false}, // init is never a root: everything descends from it
		{0, []int{100}, false},
	}
	for _, c := range cases {
		if got := inTree(root, c.pid, c.roots); got != c.want {
			t.Errorf("inTree(%d, %v) = %v, want %v", c.pid, c.roots, got, c.want)
		}
	}
}

// The real /proc: the test process is in its own tree and in its parent's, not in a child's.
func TestInTreeOnTheRealProc(t *testing.T) {
	if !inTree("/proc", os.Getpid(), []int{os.Getppid()}) {
		t.Fatal("the test process is not under its parent")
	}
	c := startCaller(t, "/nonexistent")
	deadline := time.Now().Add(5 * time.Second)
	for !inTree("/proc", c.pid(), []int{os.Getpid()}) {
		if time.Now().After(deadline) {
			t.Fatal("a child is not under the test process")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if inTree("/proc", os.Getpid(), []int{c.pid()}) {
		t.Fatal("the parent was taken for a child's descendant")
	}
}
