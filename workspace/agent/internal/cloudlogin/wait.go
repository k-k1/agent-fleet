package cloudlogin

import (
	"errors"
	"time"
)

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

var errUnsettled = errors.New("the credential state kept changing")

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
		time.Sleep(w.Poll)
		c, err := w.Check()
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
	case err != nil:
		return zero, &WaitError{Reason: WaitCheckFailed, Err: err}
	case ok:
		return c, nil
	}

	req, _, err := s.File(w.Profile, w.Key, snap, w.Waiter)
	if errors.Is(err, ErrHeld) {
		return zero, &WaitError{Reason: WaitCancelled}
	}
	if err != nil {
		return zero, &WaitError{Reason: WaitNotFiled, Err: err}
	}
	if w.Filed != nil {
		w.Filed()
	}

	recorded := snap
	// lost: the request is gone and could not be filed again, so only a state change is
	// worth another check.
	lost := false
	for time.Now().Before(deadline) {
		time.Sleep(w.Poll)
		state := s.stateOf(w.Key, req.ID)
		if state == requestCancelled {
			return zero, &WaitError{Reason: WaitCancelled}
		}
		cur := s.Backend.State(w.Key)
		if (state != requestGone || lost) && !s.Backend.Landed(cur, recorded, time.Now()) {
			continue
		}
		c, err := w.Check()
		if err == nil {
			return c, nil
		}
		if !w.LoginNeeded(err) {
			return zero, &WaitError{Reason: WaitCheckFailed, Err: err}
		}
		st, c, ok, err := settle(s, &w, cur, deadline)
		switch {
		case errors.Is(err, errUnsettled):
			continue
		case err != nil:
			return zero, &WaitError{Reason: WaitCheckFailed, Err: err}
		case ok:
			return c, nil
		}
		// Still no login, against a state that held still: the Agent may already have
		// dropped the request as resolved, so file again (or join) rather than wait on a
		// request that is gone.
		next, created, err := s.File(w.Profile, w.Key, st, w.Waiter)
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
