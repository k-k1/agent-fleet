package cloudlogin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// testState stands in for a backend's credential state.
type testState struct {
	Mark string `json:"mark,omitempty"`
}

type testBackend struct{ state map[string]testState }

func (b testBackend) State(key string) testState { return b.state[key] }

func (testBackend) Landed(cur, recorded testState, _ time.Time) bool {
	return cur != recorded && cur.Mark != ""
}

func newStore(t *testing.T) *Store[testState] {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	return &Store[testState]{Dir: "test-login", NoticeKind: "test-login-required", NoticeKey: "test-login",
		LogPrefix: "test-login", Backend: testBackend{state: map[string]testState{}}}
}

// A request file written before the extraction (the AWS backend's names, at the same
// path) is still read: an Agent upgraded while a request is pending must not lose it.
func TestRequestFileFormatAndPathAreUnchanged(t *testing.T) {
	s := newStore(t)
	sum := sha256.Sum256([]byte("af-prod"))
	path := filepath.Join(os.Getenv("HOME"), ".local", "state", "agent-fleet", "test-login", hex.EncodeToString(sum[:12])+".request.json")
	if s.RequestPath("af-prod") != path {
		t.Fatalf("path = %s, want %s", s.RequestPath("af-prod"), path)
	}
	os.MkdirAll(filepath.Dir(path), 0o700)
	raw := `{"id":"0123456789abcdef01234567","profile":"prod","ssoSession":"af-prod","firstAt":"2026-10-01T00:00:00Z",` +
		`"lastAt":"2026-10-01T00:00:01Z","cache":{"mark":"m1"},"waiters":[{"session":"s1","command":"terraform","at":"2026-10-01T00:00:01Z"}]}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	r, ok := s.Read("af-prod")
	if !ok || r.ID != "0123456789abcdef01234567" || r.Key != "af-prod" || r.Snapshot.Mark != "m1" ||
		len(r.Waiters) != 1 || r.Waiters[0].Command != "terraform" {
		t.Fatalf("read %+v %v", r, ok)
	}
	// A file under another key's name is not that key's request.
	if _, ok := s.Read("af-other"); ok {
		t.Fatal("read a request for a key it was not filed for")
	}
}

func TestEndClearsTheURLAndCode(t *testing.T) {
	s := newStore(t)
	stopped := false
	a := s.Begin("k", "", "prod", func() { stopped = true })
	a.authorize("https://example.invalid/device", "ABCD-EFGH")
	if v := a.View(); v.Phase != PhaseAuthorize || v.Code != "ABCD-EFGH" {
		t.Fatalf("view = %+v", v)
	}
	a.End(PhaseCancelled, "logged out")
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.phase != PhaseCancelled || a.url != "" || a.code != "" || a.message != "logged out" || !stopped {
		t.Fatalf("ended attempt: phase=%s url=%q code=%q stopped=%t", a.phase, a.url, a.code, stopped)
	}
}

// ADR 0107 decision 3: a code goes to the attempt's process once, only while it waits for
// one; the process exits after reading it and the backend decides "done".
func TestSubmitWritesTheCodeOnceToAWaitingAttempt(t *testing.T) {
	s := newStore(t)
	out := filepath.Join(t.TempDir(), "got")
	a, err := s.Start("k", "", "prod", Process{
		Name: "test login", Path: "/bin/sh",
		Args:    []string{"-c", `echo "Go to https://example.invalid/auth"; read code; printf %s "$code" > "` + out + `"`},
		Timeout: 10 * time.Second, Stdin: true,
		Parse: func(o string) (string, string, error) {
			if strings.Contains(o, "https://example.invalid/auth") {
				return "https://example.invalid/auth", "", nil
			}
			return "", "", nil
		},
		Exited: func(err error) (bool, string) { return err == nil, "exited" },
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, a, PhaseAuthorize)
	if err := a.Submit("4/0AbCd"); err != nil {
		t.Fatal(err)
	}
	if err := a.Submit("again"); !errors.Is(err, ErrNotAwaitingCode) {
		t.Fatalf("second submit = %v", err)
	}
	waitFor(t, a, PhaseDone)
	if b, _ := os.ReadFile(out); string(b) != "4/0AbCd" {
		t.Fatalf("the process read %q", b)
	}

	// No stdin pipe (an AWS device-code login), or an attempt that has ended: refused.
	noStdin := s.Begin("k2", "", "prod", nil)
	noStdin.authorize("https://example.invalid/auth", "")
	if err := noStdin.Submit("x"); !errors.Is(err, ErrNotAwaitingCode) {
		t.Fatalf("submit without stdin = %v", err)
	}
	noStdin.End(PhaseCancelled, "")
	if err := noStdin.Submit("x"); !errors.Is(err, ErrNotAwaitingCode) {
		t.Fatalf("submit after cancel = %v", err)
	}
}

// A URL that fails the backend's validation ends the attempt and never reaches the view.
func TestParseErrorEndsTheAttempt(t *testing.T) {
	s := newStore(t)
	a, err := s.Start("k", "", "prod", Process{
		Name: "test login", Path: "/bin/sh", Args: []string{"-c", "echo https://evil.invalid/; sleep 5"},
		Timeout: 10 * time.Second,
		Parse:   func(string) (string, string, error) { return "", "", errors.New("unexpected sign-in URL") },
		Exited:  func(error) (bool, string) { return false, "exited" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if v := waitFor(t, a, PhaseFailed); v.URL != "" || v.Message != "unexpected sign-in URL" {
		t.Fatalf("view = %+v", v)
	}
}

// Exited runs before Cleanup: a backend that holds a lock from the start lets go of it in
// Cleanup, and its Exited still needs it (gcpx records the finished login under it).
func TestExitedRunsBeforeCleanup(t *testing.T) {
	s := newStore(t)
	var mu sync.Mutex
	var order []string
	note := func(s string) { mu.Lock(); order = append(order, s); mu.Unlock() }
	a, err := s.Start("k", "", "prod", Process{
		Name: "test login", Path: "/bin/sh", Args: []string{"-c", "true"}, Timeout: 10 * time.Second,
		Parse:   func(string) (string, string, error) { return "", "", nil },
		Exited:  func(error) (bool, string) { note("exited"); return true, "" },
		Cleanup: func() { note("cleanup") },
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, a, PhaseDone)
	<-a.Exited()
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(order, ",") != "exited,cleanup" {
		t.Fatalf("order = %v", order)
	}
}

// Exited sees everything the process printed: a failed login's reason is its last line,
// printed just before it exits, and Wait returns without waiting for the output reader.
// The slow first Parse holds the reader back while the process prints its last line and
// exits.
func TestExitedSeesTheLastOutput(t *testing.T) {
	s := newStore(t)
	var mu sync.Mutex
	var last string
	first := true
	exited := make(chan string, 1)
	a, err := s.Start("k", "", "prod", Process{
		Name: "test login", Path: "/bin/sh",
		Args:    []string{"-c", `echo working; sleep 0.05; echo "ERROR: the reason"; exit 1`},
		Timeout: 10 * time.Second,
		Parse: func(out string) (string, string, error) {
			mu.Lock()
			last = out
			slow := first
			first = false
			mu.Unlock()
			if slow {
				time.Sleep(300 * time.Millisecond)
			}
			return "", "", nil
		},
		Exited: func(error) (bool, string) {
			mu.Lock()
			defer mu.Unlock()
			exited <- last
			return false, "exited"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, a, PhaseFailed)
	if out := <-exited; !strings.Contains(out, "ERROR: the reason") {
		t.Fatalf("Exited saw %q, without the last line", out)
	}
}

// A process that leaves a child holding its output open still ends its attempt: the wait
// for the output to drain is bounded.
func TestAnOutputHeldOpenDoesNotHoldTheExit(t *testing.T) {
	old := outputDrainWait
	outputDrainWait = 100 * time.Millisecond
	t.Cleanup(func() { outputDrainWait = old })
	s := newStore(t)
	start := time.Now()
	a, err := s.Start("k", "", "prod", Process{
		Name: "test login", Path: "/bin/sh", Args: []string{"-c", "sleep 3 & exit 1"},
		Timeout: 10 * time.Second,
		Parse:   func(string) (string, string, error) { return "", "", nil },
		Exited:  func(error) (bool, string) { return false, "exited" },
	})
	if err != nil {
		t.Fatal(err)
	}
	<-a.Exited()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("the exit waited %v for the held output", d)
	}
}

func waitFor(t *testing.T, a *Attempt, phase string) AttemptWire {
	t.Helper()
	var v AttemptWire
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if v = a.View(); v.Phase == phase {
			return v
		}
	}
	t.Fatalf("attempt = %+v, want phase %s", v, phase)
	return v
}

// The neutral packages are what the backends build on; one importing a backend would
// make the backends depend on each other.
func TestNeutralPackagesImportNoBackend(t *testing.T) {
	checked := 0
	for _, dir := range []string{".", "../cloudbridge", "../cloudexec"} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			pf, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range pf.Imports {
				checked++
				if p := strings.Trim(imp.Path.Value, `"`); strings.HasSuffix(p, "/internal/awsx") || strings.HasSuffix(p, "/internal/gcpx") {
					t.Errorf("%s imports the backend %s", f, p)
				}
			}
		}
	}
	if checked < 10 {
		t.Fatalf("only %d imports checked; the scan found nothing to check", checked)
	}
}

// recorder is a stdin that keeps what was written to it.
type recorder struct {
	strings.Builder
	closed bool
}

func (r *recorder) Close() error { r.closed = true; return nil }

// Only an attempt waiting in PhaseAuthorize takes a code: every other phase refuses it
// without writing anything, even with a stdin to write to.
func TestSubmitRefusesEveryPhaseButAuthorize(t *testing.T) {
	s := newStore(t)
	for _, phase := range []string{PhaseStarting, PhaseCancelled, PhaseReplaced, PhaseFailed, PhaseDone} {
		a := s.Begin("k-"+phase, "", "prod", nil)
		w := &recorder{}
		a.mu.Lock()
		a.stdin = w
		a.mu.Unlock()
		if phase != PhaseStarting {
			a.authorize("https://example.invalid/auth", "")
			a.End(phase, "")
		}
		if err := a.Submit("code"); !errors.Is(err, ErrNotAwaitingCode) || w.Len() != 0 {
			t.Errorf("%s: submit = %v, wrote %q", phase, err, w.String())
		}
	}
}

// A process that never reads its stdin must not let a Submit hold the attempt: cancel,
// view and the store's other attempts go on while the write is stuck, and the cancel
// ends the write.
func TestASubmitStuckOnAFullPipeDoesNotBlockCancel(t *testing.T) {
	s := newStore(t)
	a, err := s.Start("k", "", "prod", Process{
		Name: "test login", Path: "/bin/sh", Args: []string{"-c", "echo https://example.invalid/auth; exec sleep 30"},
		Timeout: time.Minute, Stdin: true,
		Parse: func(o string) (string, string, error) {
			if strings.Contains(o, "https://") {
				return "https://example.invalid/auth", "", nil
			}
			return "", "", nil
		},
		Exited: func(error) (bool, string) { return false, "exited" },
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, a, PhaseAuthorize)
	submitted := make(chan error, 1)
	go func() { submitted <- a.Submit(strings.Repeat("x", 1<<20)) }()
	time.Sleep(100 * time.Millisecond) // let the write fill the pipe

	done := make(chan struct{})
	go func() {
		a.View()
		s.Begin("other", "", "other", nil)
		a.End(PhaseCancelled, "")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("view, another attempt or the cancel waited on a stuck Submit")
	}
	select {
	case err := <-submitted:
		if err == nil {
			t.Fatal("a write into a pipe nobody read reported success")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the cancel did not end the stuck write")
	}
}

// waitSpec is a Wait whose first check is made only after a login "lands" (Filed sets the
// mark), and whose check is check.
func waitSpec(s *Store[testState], cancel <-chan struct{}, check func() (string, error)) WaitSpec[string] {
	return WaitSpec[string]{
		Profile: "p", Key: "k", Wait: 5 * time.Second, Poll: 5 * time.Millisecond, Cancel: cancel,
		Check:       check,
		LoginNeeded: func(err error) bool { return err.Error() == "login" },
		Filed: func() {
			b := s.Backend.(testBackend)
			b.state["k"] = testState{Mark: "landed"}
		},
	}
}

// A Ctrl-C that arrives while the check runs wins over the check's outcome, success or not:
// carrying on into the command would swallow the person's request to stop.
func TestCancelDuringCheckWinsOverItsOutcome(t *testing.T) {
	for name, outcome := range map[string]error{"success": nil, "other failure": errors.New("boom"), "login needed": errors.New("login")} {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			cancel := make(chan struct{})
			spec := waitSpec(s, cancel, func() (string, error) {
				close(cancel)
				if outcome == nil {
					return "creds", nil
				}
				return "", outcome
			})
			c, err := Wait(s, testState{}, spec)
			var we *WaitError
			if !errors.As(err, &we) || we.Reason != WaitInterrupted || c != "" {
				t.Fatalf("got %q, %v; want WaitInterrupted", c, err)
			}
		})
	}
}

// Waiting for a lock somebody else holds ends with Cancel or the deadline, not the holder.
func TestFlockExEndsWithCancelOrDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l")
	holder, _ := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	defer holder.Close()
	if err := FlockEx(holder, nil, time.Time{}); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(path, os.O_RDWR, 0o600)
	defer f.Close()
	if err := FlockEx(f, nil, time.Now().Add(80*time.Millisecond)); !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("deadline: %v", err)
	}
	cancel := make(chan struct{})
	time.AfterFunc(50*time.Millisecond, func() { close(cancel) })
	start := time.Now()
	if err := FlockEx(f, cancel, time.Now().Add(time.Minute)); !errors.Is(err, ErrLockInterrupted) || time.Since(start) > 2*time.Second {
		t.Fatalf("cancel: %v after %s", err, time.Since(start))
	}
}

// Filing the request waits for the store's directory lock; Ctrl-C ends that wait too.
func TestCancelEndsTheWaitForTheStoreLock(t *testing.T) {
	s := newStore(t)
	unlock, err := s.lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	cancel := make(chan struct{})
	time.AfterFunc(50*time.Millisecond, func() { close(cancel) })
	spec := waitSpec(s, cancel, func() (string, error) { return "", errors.New("login") })
	done := make(chan error, 1)
	go func() { _, err := Wait(s, testState{Mark: "old"}, spec); done <- err }()
	select {
	case err := <-done:
		var we *WaitError
		if !errors.As(err, &we) || we.Reason != WaitInterrupted {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Ctrl-C did not end the wait for the lock")
	}
}

// The wait's own budget running out is a timeout (exit 3 for a rerun), not a failure of the
// request or the check, and never a reason to fall back to a terminal login.
func TestLockOrCheckTimeoutIsATimeout(t *testing.T) {
	t.Run("store lock held past the budget", func(t *testing.T) {
		s := newStore(t)
		unlock, err := s.lock()
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		spec := waitSpec(s, nil, func() (string, error) { return "", errors.New("login") })
		spec.Wait = 100 * time.Millisecond
		_, err = Wait(s, testState{Mark: "old"}, spec)
		var we *WaitError
		if !errors.As(err, &we) || we.Reason != WaitTimedOut {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("check ended by the lock timeout or the deadline", func(t *testing.T) {
		for name, cerr := range map[string]error{"lock": ErrLockTimeout, "deadline": context.DeadlineExceeded} {
			s := newStore(t)
			spec := waitSpec(s, nil, func() (string, error) { return "", fmt.Errorf("check: %w", cerr) })
			_, err := Wait(s, testState{}, spec)
			var we *WaitError
			if !errors.As(err, &we) || we.Reason != WaitTimedOut {
				t.Fatalf("%s: err = %v", name, err)
			}
		}
	})
}

// exitBeforeClose forces the race behind a spurious Submit failure: after the write lands it
// holds Submit until the process has exited and cmd.Wait has closed the pipe, so Submit's
// own Close always loses.
type exitBeforeClose struct {
	io.WriteCloser
	exited <-chan struct{}
}

func (w exitBeforeClose) Write(p []byte) (int, error) {
	n, err := w.WriteCloser.Write(p)
	<-w.exited
	return n, err
}

// A code the process read and acted on is delivered even when the process has exited, and
// its pipe been closed, by the time Submit closes it.
func TestSubmitSucceedsWhenTheProcessExitedBeforeTheClose(t *testing.T) {
	s := newStore(t)
	a, err := s.Start("k", "", "prod", Process{
		Name: "test login", Path: "/bin/sh",
		Args:    []string{"-c", `echo "Go to https://example.invalid/auth"; read code`},
		Timeout: 10 * time.Second, Stdin: true,
		Parse: func(o string) (string, string, error) {
			if strings.Contains(o, "https://example.invalid/auth") {
				return "https://example.invalid/auth", "", nil
			}
			return "", "", nil
		},
		Exited: func(err error) (bool, string) { return err == nil, "exited" },
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, a, PhaseAuthorize)
	a.mu.Lock()
	a.stdin = exitBeforeClose{WriteCloser: a.stdin, exited: a.done}
	a.mu.Unlock()
	if err := a.Submit("4/0AbCd"); err != nil {
		t.Fatalf("submit after the process exited = %v", err)
	}
}

// A write that fails means the code was not delivered; that stays an error.
func TestSubmitKeepsAWriteError(t *testing.T) {
	s := newStore(t)
	a := s.Begin("k", "", "prod", nil)
	a.authorize("https://example.invalid/auth", "")
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	pr.Close()
	a.mu.Lock()
	a.stdin = pw
	a.mu.Unlock()
	if err := a.Submit("x"); !errors.Is(err, syscall.EPIPE) {
		t.Fatalf("submit to a pipe nobody reads = %v", err)
	}
}
