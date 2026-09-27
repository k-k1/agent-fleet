package awsx

import (
	"errors"
	"fmt"
	"io"
	"time"
)

// loginPollInterval is how often a waiting run looks at its request and the token
// cache. Both are file reads; the whole `aws` start happens only on a change.
var loginPollInterval = 2 * time.Second

// consoleEligible reports whether this run may ask the Console for the login: a
// Settings profile that the files define exactly as Settings does, through its own
// af-<name> sso-session. For anything else the Agent has no validated account and role
// to show the member (ADR 0102 decision 1).
func consoleEligible(sso ssoInfo, o ExecOptions) bool {
	if !o.ConsoleLogin || o.Login != "auto" || o.ConsoleWait <= 0 {
		return false
	}
	sp, listed := o.Settings[o.Profile]
	return listed && sameSSO(sp, sso) && sso.Session == "af-"+o.Profile
}

// errUnsettled means the cache kept changing under every check until the wait ran out,
// so no failure was ever pinned to a cache state and nothing was filed.
var errUnsettled = errors.New("the SSO token cache kept changing")

// settle applies the snapshot rule (ADR 0102 decision 1) after a failed check that was
// made against snap: while the cache differs from the snapshot a check was made
// against, a login may have landed in between, so check again instead of recording.
// It returns the credentials once a check succeeds, or the snapshot a check failed
// against while the cache held still.
func settle(aws awsRunner, ssoSession string, snap CacheState, deadline time.Time) (CacheState, processCreds, error) {
	for {
		cur := ReadCacheState(ssoSession)
		if cur == snap {
			return snap, processCreds{}, nil
		}
		if !time.Now().Before(deadline) {
			return snap, processCreds{}, errUnsettled
		}
		snap = cur
		c, err := exportCreds(aws, ssoOnlyProfile)
		if err == nil {
			return snap, c, nil
		}
		if !loginNeeded(err.Error()) {
			return snap, processCreds{}, err
		}
	}
}

// consoleLogin files a login request for the Console and waits for the member to approve
// it (ADR 0102 decisions 1 and 5). first is the failure of the check made against snap.
func consoleLogin(aws awsRunner, sso ssoInfo, snap CacheState, o ExecOptions, first error, hint string) (processCreds, error) {
	stderr := o.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	deadline := time.Now().Add(o.ConsoleWait)
	cancelled := func() error {
		return fmt.Errorf("%w for profile %q: the login request was cancelled in the Agent Fleet Console; "+
			"ask the member, or log in in a terminal with: %s", ErrLoginRequired, o.Profile, hint)
	}

	snap, creds, err := settle(aws, sso.Session, snap, deadline)
	switch {
	case errors.Is(err, errUnsettled):
		return processCreds{}, fmt.Errorf("%w for profile %q: %v\nlog in with: %s", ErrLoginRequired, o.Profile, first, hint)
	case err != nil:
		return processCreds{}, fmt.Errorf("could not get credentials for profile %q: %v", o.Profile, err)
	case creds.AccessKeyID != "":
		return creds, nil
	}

	req, _, err := FileLoginRequest(o.Profile, sso.Session, snap, o.Waiter)
	if errors.Is(err, ErrLoginHeld) {
		return processCreds{}, cancelled()
	}
	if err != nil {
		return processCreds{}, fmt.Errorf("%w for profile %q: %v (and the Console could not be asked: %v)\nlog in with: %s",
			ErrLoginRequired, o.Profile, first, err, hint)
	}
	// First, before any waiting: a run its caller kills on a timeout has still said it.
	fmt.Fprintf(stderr, "af-aws-exec: SSO login for profile %q requested in the Agent Fleet Console; "+
		"waiting up to %s for the member to approve it there\n", o.Profile, o.ConsoleWait.Round(time.Second))

	recorded := snap
	for time.Now().Before(deadline) {
		time.Sleep(loginPollInterval)
		state := requestStateFor(sso.Session, req.ID)
		if state == requestCancelled {
			return processCreds{}, cancelled()
		}
		cur := ReadCacheState(sso.Session)
		if state != requestGone && !(cur != recorded && cur.Unexpired(time.Now())) {
			continue
		}
		c, err := exportCreds(aws, ssoOnlyProfile)
		if err == nil {
			return c, nil
		}
		if !loginNeeded(err.Error()) {
			return processCreds{}, fmt.Errorf("could not get credentials for profile %q: %v", o.Profile, err)
		}
		s, c, err := settle(aws, sso.Session, cur, deadline)
		switch {
		case errors.Is(err, errUnsettled):
			continue
		case err != nil:
			return processCreds{}, fmt.Errorf("could not get credentials for profile %q: %v", o.Profile, err)
		case c.AccessKeyID != "":
			return c, nil
		}
		// Still no login, against a cache that held still: the Agent may already have
		// dropped the request as resolved, so file again (or join) rather than wait on
		// a request that is gone.
		next, created, err := FileLoginRequest(o.Profile, sso.Session, s, o.Waiter)
		if errors.Is(err, ErrLoginHeld) {
			return processCreds{}, cancelled()
		}
		if err != nil {
			continue
		}
		if created {
			fmt.Fprintf(stderr, "af-aws-exec: the SSO login for profile %q is still needed; requested again in the Agent Fleet Console\n", o.Profile)
		}
		req, recorded = next, s
	}
	return processCreds{}, fmt.Errorf("%w for profile %q: the login was requested in the Agent Fleet Console and is waiting "+
		"for the member to approve it there; run the command again once it is approved", ErrLoginRequired, o.Profile)
}
