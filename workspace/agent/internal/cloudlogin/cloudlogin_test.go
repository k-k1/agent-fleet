package cloudlogin

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
