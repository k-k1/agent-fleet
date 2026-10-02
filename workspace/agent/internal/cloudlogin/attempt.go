package cloudlogin

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
)

// Every Agent route is callable with AGENT_TOKEN, which agents hold; what keeps a URL or
// code started by anyone else off the member's screen is the attempt id, returned only to
// the caller that started it.

// Phases of an attempt, as the Console reads them.
const (
	PhaseStarting  = "starting"
	PhaseAuthorize = "authorize"
	PhaseDone      = "done"
	PhaseFailed    = "failed"
	PhaseReplaced  = "replaced"
	PhaseCancelled = "cancelled"
	// PhaseGone answers for an attempt that is unknown, or lost with an Agent restart.
	PhaseGone = "gone"
)

// Attempt is one login process for a request key. Its identity fields never change after
// it is made.
type Attempt struct {
	ID, RequestID, Key, Profile string

	mu        sync.Mutex
	phase     string
	url       string
	code      string
	message   string
	ended     time.Time
	stop      func()
	stdin     io.WriteCloser
	submitted bool
	// done is closed once the process has exited and its cleanup has run, so nothing it
	// writes can land later. nil for an attempt Start did not run a process for.
	done chan struct{}
}

// Exited is closed once the attempt's process has exited; nil when it had none.
func (a *Attempt) Exited() <-chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.done
}

// Live reports whether the attempt has not ended.
func (a *Attempt) Live() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.phase == PhaseStarting || a.phase == PhaseAuthorize
}

// End moves a live attempt to phase and stops its process; a finished one keeps its phase.
func (a *Attempt) End(phase, message string) {
	a.mu.Lock()
	if a.phase == PhaseStarting || a.phase == PhaseAuthorize {
		a.phase, a.message, a.ended = phase, message, time.Now()
		a.url, a.code = "", ""
	}
	stop, stdin := a.stop, a.stdin
	a.mu.Unlock()
	if stop != nil {
		stop()
	}
	if stdin != nil {
		// Unblocks a Submit stuck on a pipe the process does not read.
		_ = stdin.Close()
	}
}

// authorize records what the member needs to finish the login, while the attempt is live.
func (a *Attempt) authorize(url, code string) {
	a.mu.Lock()
	if a.phase == PhaseStarting || a.phase == PhaseAuthorize {
		a.phase, a.url, a.code = PhaseAuthorize, url, code
	}
	a.mu.Unlock()
}

// ErrNotAwaitingCode refuses a code for an attempt that is not waiting for one: it never
// asked for a code, has ended (cancelled, replaced, failed, done), or already took one.
var ErrNotAwaitingCode = errors.New("this login attempt is not waiting for a code")

// Submit writes code to the attempt's process once, and only while the attempt waits for
// the member. The check and the claim happen under the attempt's lock; the write does
// not, because a process that never reads would otherwise hold the lock, and with it
// End, View and every Begin of the store (the prune takes each attempt's lock). Ending
// the attempt closes the pipe, which ends a stuck write. The code is never kept or
// logged.
func (a *Attempt) Submit(code string) error {
	a.mu.Lock()
	if a.phase != PhaseAuthorize || a.stdin == nil || a.submitted {
		a.mu.Unlock()
		return ErrNotAwaitingCode
	}
	a.submitted = true
	stdin := a.stdin
	a.mu.Unlock()
	_, err := io.WriteString(stdin, code+"\n")
	if cerr := stdin.Close(); err == nil {
		err = cerr
	}
	return err
}

// AttemptWire carries a URL and a code only while the attempt waits for the member.
type AttemptWire struct {
	Phase   string `json:"phase"`
	URL     string `json:"url,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// View is the attempt as the tab that started it may see it.
func (a *Attempt) View() AttemptWire {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := AttemptWire{Phase: a.phase, Message: a.message}
	if a.phase == PhaseAuthorize {
		out.URL, out.Code = a.url, a.code
	}
	return out
}

// StartWire answers the press that started an attempt, and only that press.
type StartWire struct {
	Attempt string `json:"attempt"`
}

// CancelWire answers a cancel.
type CancelWire struct {
	OK bool `json:"ok"`
}

// Attempt returns the attempt with id, or nil.
func (s *Store[S]) Attempt(id string) *Attempt { return s.attempts.byIDOf(id) }

// Current returns the attempt that holds key's one slot, live or not, or nil.
func (s *Store[S]) Current(key string) *Attempt { return s.attempts.current(key) }

// Begin makes a new attempt the current one of key, ending the one it replaces. stop is
// called when the attempt ends; Start passes the stop of the process it runs.
func (s *Store[S]) Begin(key, requestID, profile string, stop func()) *Attempt {
	a := &Attempt{ID: newID(), RequestID: requestID, Key: key, Profile: profile, phase: PhaseStarting, stop: stop}
	if prev := s.attempts.add(a); prev != nil {
		prev.End(PhaseReplaced, "")
	}
	return a
}

// Process is a login process a backend asks Start to run.
type Process struct {
	// Name is what the messages call the process ("aws sso login").
	Name string
	// Path, Args (after the program) and Env run it. Env is the whole environment.
	Path string
	Args []string
	Env  []string
	// Timeout kills the process (and the attempt fails) when it outlives it.
	Timeout time.Duration
	// Stdin keeps a pipe to the process's stdin for one Attempt.Submit; without it
	// stdin is the null device.
	Stdin bool
	// Parse reads the output so far (stdout and stderr together, kept in memory only and
	// never logged) and returns the URL, and a code if the process prints one, once they
	// are there. An error ends the attempt as failed with the error's text: a URL that
	// does not pass the backend's validation must never reach the member.
	Parse func(output string) (url, code string, err error)
	// Exited reads how the process ended (err from Wait) and says whether the login is
	// done, or the message the failed attempt shows.
	Exited func(err error) (done bool, message string)
	// Cleanup, when set, runs once the process is gone or could not be started.
	Cleanup func()
}

// Start runs p as a new attempt for key, replacing the running one. requestID is "" for
// a login started from Settings rather than from a request. The process runs detached from
// every pane, in its own process group so a replace or cancel kills everything it
// started, and dies with the Agent, whose restart loses the attempt anyway (ADR 0102
// decision 3).
func (s *Store[S]) Start(key, requestID, profile string, p Process) (*Attempt, error) {
	g := s.Gate(key)
	g.Lock()
	defer g.Unlock()
	cleanup := func() {
		if p.Cleanup != nil {
			p.Cleanup()
		}
	}
	ctx, stop := context.WithTimeout(context.Background(), p.Timeout)
	cmd := exec.CommandContext(ctx, p.Path, p.Args...)
	cmd.Env = p.Env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	pr, pw, err := os.Pipe()
	if err != nil {
		stop()
		cleanup()
		return nil, err
	}
	var stdin io.WriteCloser
	if p.Stdin {
		if stdin, err = cmd.StdinPipe(); err != nil {
			pr.Close()
			pw.Close()
			stop()
			cleanup()
			return nil, err
		}
	}
	cmd.Stdout, cmd.Stderr = pw, pw

	a := s.Begin(key, requestID, profile, stop)
	done := make(chan struct{})
	a.mu.Lock()
	a.stdin, a.done = stdin, done
	a.mu.Unlock()

	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		cleanup()
		a.End(PhaseFailed, "could not start "+p.Name+": "+err.Error())
		close(done)
		return a, nil
	}
	pw.Close()
	go watchOutput(a, pr, p.Parse)
	go func() {
		defer close(done)
		err := cmd.Wait()
		pr.Close()
		cleanup()
		if done, msg := p.Exited(err); done {
			a.End(PhaseDone, "")
		} else {
			a.End(PhaseFailed, msg)
		}
	}()
	return a, nil
}

// watchOutput feeds the process's output to parse until it yields a URL. The output is
// kept only in memory and never logged: it may hold the code.
func watchOutput(a *Attempt, r io.Reader, parse func(string) (string, string, error)) {
	var buf bytes.Buffer
	chunk := make([]byte, 4096)
	for {
		n, err := r.Read(chunk)
		if n > 0 && buf.Len() < 64<<10 {
			buf.Write(chunk[:n])
			url, code, perr := parse(buf.String())
			if perr != nil {
				a.End(PhaseFailed, perr.Error())
				return
			}
			if url != "" {
				a.authorize(url, code)
			}
		}
		if err != nil {
			return
		}
	}
}

// RelayedByCP is a hint for the log only: the Control Plane marks what it relays, and an
// agent calling the Agent directly could set the same header.
func RelayedByCP(r *http.Request) bool { return r.Header.Get("X-AF-Relay") == "cp" }

// WriteAttempt answers for attempt id with its phase, and its URL and code only while it
// is waiting for the member. belongs says whether the route asked through may read it, so
// one route never reads an attempt another route started.
func (s *Store[S]) WriteAttempt(w http.ResponseWriter, id string, belongs func(*Attempt) bool) {
	a := s.Attempt(id)
	if a == nil || !belongs(a) {
		// Unknown, or lost with an Agent restart: the modal offers to start again.
		httpx.WriteJSON(w, http.StatusOK, AttemptWire{Phase: PhaseGone})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, a.View())
}

// HandleCancel is POST /<prefix>/{id}/cancel: see Cancel.
func (s *Store[S]) HandleCancel(w http.ResponseWriter, r *http.Request) {
	req, err := s.Cancel(r.PathValue("id"))
	switch {
	case errors.Is(err, ErrNoRequest):
		httpx.WriteErr(w, http.StatusNotFound, "not_found", err.Error())
		return
	case err != nil:
		httpx.WriteErr(w, http.StatusInternalServerError, "cancel_failed", err.Error())
		return
	}
	log.Printf("%s: cancel profile=%s relayed=%t", s.LogPrefix, req.Profile, RelayedByCP(r))
	httpx.WriteJSON(w, http.StatusOK, CancelWire{OK: true})
}
