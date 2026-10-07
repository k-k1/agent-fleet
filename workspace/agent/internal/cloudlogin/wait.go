package cloudlogin

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Interrupt returns a channel that closes on the first SIGINT or SIGTERM, and a stop
// function that releases the handler. A run at a person's terminal installs it for the
// length of the Console wait only, so Ctrl-C ends the wait (exit 3) instead of killing
// the process mid-poll; outside the wait the default signal behaviour is untouched.
func Interrupt() (<-chan struct{}, func()) {
	sig := make(chan os.Signal, 1)
	done := make(chan struct{})
	stopped := make(chan struct{})
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sig:
			close(done)
		case <-stopped:
		}
	}()
	return done, func() {
		signal.Stop(sig)
		select {
		case <-stopped:
		default:
			close(stopped)
		}
	}
}

// Context returns a context that ends when cancel closes or the deadline passes; stop
// releases it. A run hands it to the credential check's subprocesses, so exec.CommandContext
// kills them and the check returns only after they have exited: nothing outlives the wait.
func Context(cancel <-chan struct{}, deadline time.Time) (context.Context, context.CancelFunc) {
	ctx, stop := context.WithDeadline(context.Background(), deadline)
	if cancel != nil {
		go func() {
			select {
			case <-cancel:
				stop()
			case <-ctx.Done():
			}
		}()
	}
	return ctx, stop
}

// failure classifies an error that ends the wait without credentials: the wait's own
// budget running out (a lock not obtained, or a check killed by the deadline) is a
// timeout like any other, not a failure of the check.
func failure(err error, otherwise WaitReason) *WaitError {
	if errors.Is(err, ErrLockTimeout) || errors.Is(err, context.DeadlineExceeded) {
		return &WaitError{Reason: WaitTimedOut}
	}
	return &WaitError{Reason: otherwise, Err: err}
}

// cancelled reports whether cancel has closed.
func cancelled(cancel <-chan struct{}) bool {
	select {
	case <-cancel:
		return true
	default:
		return false
	}
}

// sleep waits d, or until cancel closes; it reports whether the full time passed. A nil
// cancel never fires.
func sleep(d time.Duration, cancel <-chan struct{}) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-cancel:
		return false
	}
}

// WaitSpec is one wrapper run that asks the Console for the login and waits for the
// member (ADR 0102 decisions 1 and 5).
type WaitSpec[C any] struct {
	Profile, Key string
	Waiter       Waiter
	// Wait bounds the whole wait, from the call on; Poll is how often the request and the
	// backend's state are looked at. Both are file reads; Check runs only on a change.
	Wait, Poll time.Duration
	// Check obtains the credentials. LoginNeeded tells a failure a (re)login fixes from
	// any other: only those keep the run waiting, because an agent hands the resulting
	// exit 3 to the member as "log in".
	Check       func() (C, error)
	LoginNeeded func(error) bool
	// Filed runs once the request is filed or joined, before any waiting: a run its
	// caller kills on a timeout has still said it. FiledAgain runs when the request was
	// gone and a new one had to be filed.
	Filed, FiledAgain func()
	// Cancel, when it closes, ends the wait with WaitInterrupted and leaves the request
	// as it is, for the next run or the Console to settle (Ctrl-C at a terminal).
	Cancel <-chan struct{}
}

// WaitReason says why a wait ended without credentials.
type WaitReason int

const (
	// WaitUnsettled: the state kept changing under every check until the wait ran out,
	// so no failure was ever pinned to a state and nothing was filed.
	WaitUnsettled WaitReason = iota + 1
	// WaitCheckFailed: a check failed with an error a login does not fix (Err).
	WaitCheckFailed
	// WaitNotFiled: the request could not be filed (Err).
	WaitNotFiled
	// WaitCancelled: the request was cancelled in the Console, or a cancel holds it back.
	WaitCancelled
	// WaitTimedOut: the request is still pending when the wait ran out.
	WaitTimedOut
	// WaitInterrupted: Cancel closed; the request, if filed, is left pending.
	WaitInterrupted
)

// WaitError is how Wait ends without credentials. The backend words it: the wrapper's
// name, the Settings section and the terminal login are its own.
type WaitError struct {
	Reason WaitReason
	Err    error
}

func (e *WaitError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "the Console login did not complete"
}

func (e *WaitError) Unwrap() error { return e.Err }

var (
	errUnsettled   = errors.New("the credential state kept changing")
	errInterrupted = errors.New("interrupted")
)

// settle applies the snapshot rule (ADR 0102 decision 1) after a failed check that was
// made against snap: while the state differs from the snapshot a check was made against,
// a login may have landed in between, so check again instead of recording. It returns
// the credentials once a check succeeds (ok), or the snapshot a check failed against
// while the state held still.
func settle[S comparable, C any](s *Store[S], w *WaitSpec[C], snap S, deadline time.Time) (S, C, bool, error) {
	var zero C
	for {
		cur := s.Backend.State(w.Key)
		if cur == snap {
			return snap, zero, false, nil
		}
		if !time.Now().Before(deadline) {
			return snap, zero, false, errUnsettled
		}
		snap = cur
		// A state that keeps moving must not turn this into back-to-back checks.
		if !sleep(w.Poll, w.Cancel) {
			return snap, zero, false, errInterrupted
		}
		c, err := w.Check()
		// A Ctrl-C that arrived during the check wins over its outcome: the person asked to
		// stop, and a success here would carry on into the command they meant to abandon.
		if cancelled(w.Cancel) {
			return snap, zero, false, errInterrupted
		}
		if err == nil {
			return snap, c, true, nil
		}
		if !w.LoginNeeded(err) {
			return snap, zero, false, err
		}
	}
}

// Wait files a login request for the Console and waits for the member to approve it.
// snap is the state the run's first, failed check was made against. It returns the
// credentials, or a *WaitError.
func Wait[S comparable, C any](s *Store[S], snap S, w WaitSpec[C]) (C, error) {
	var zero C
	deadline := time.Now().Add(w.Wait)

	snap, c, ok, err := settle(s, &w, snap, deadline)
	switch {
	case errors.Is(err, errUnsettled):
		return zero, &WaitError{Reason: WaitUnsettled}
	case errors.Is(err, errInterrupted):
		return zero, &WaitError{Reason: WaitInterrupted}
	case err != nil:
		return zero, failure(err, WaitCheckFailed)
	case ok:
		return c, nil
	}

	req, _, err := s.FileCancel(w.Profile, w.Key, snap, w.Waiter, w.Cancel, deadline)
	if errors.Is(err, ErrLockInterrupted) {
		return zero, &WaitError{Reason: WaitInterrupted}
	}
	if errors.Is(err, ErrHeld) {
		return zero, &WaitError{Reason: WaitCancelled}
	}
	if err != nil {
		return zero, failure(err, WaitNotFiled)
	}
	if w.Filed != nil {
		w.Filed()
	}

	recorded := snap
	// lost: the request is gone and could not be filed again, so only a state change is
	// worth another check.
	lost := false
	for time.Now().Before(deadline) {
		if !sleep(w.Poll, w.Cancel) {
			return zero, &WaitError{Reason: WaitInterrupted}
		}
		state := s.stateOf(w.Key, req.ID)
		if state == requestCancelled {
			return zero, &WaitError{Reason: WaitCancelled}
		}
		cur := s.Backend.State(w.Key)
		if (state != requestGone || lost) && !s.Backend.Landed(cur, recorded, time.Now()) {
			continue
		}
		c, err := w.Check()
		if cancelled(w.Cancel) {
			return zero, &WaitError{Reason: WaitInterrupted}
		}
		if err == nil {
			return c, nil
		}
		if !w.LoginNeeded(err) {
			return zero, failure(err, WaitCheckFailed)
		}
		st, c, ok, err := settle(s, &w, cur, deadline)
		switch {
		case errors.Is(err, errUnsettled):
			continue
		case errors.Is(err, errInterrupted):
			return zero, &WaitError{Reason: WaitInterrupted}
		case err != nil:
			return zero, failure(err, WaitCheckFailed)
		case ok:
			return c, nil
		}
		// Still no login, against a state that held still: the Agent may already have
		// dropped the request as resolved, so file again (or join) rather than wait on a
		// request that is gone.
		next, created, err := s.FileCancel(w.Profile, w.Key, st, w.Waiter, w.Cancel, deadline)
		if errors.Is(err, ErrLockInterrupted) {
			return zero, &WaitError{Reason: WaitInterrupted}
		}
		if errors.Is(err, ErrHeld) {
			return zero, &WaitError{Reason: WaitCancelled}
		}
		if err != nil {
			recorded, lost = st, true
			continue
		}
		if created && w.FiledAgain != nil {
			w.FiledAgain()
		}
		req, recorded, lost = next, st, false
	}
	return zero, &WaitError{Reason: WaitTimedOut}
}
