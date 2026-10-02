package awsx

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cloudlogin"
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

// consoleLogin files a login request for the Console and waits for the member to approve
// it (ADR 0102 decisions 1 and 5). first is the failure of the check made against snap.
func consoleLogin(aws awsRunner, sso ssoInfo, snap CacheState, o ExecOptions, first error, hint string) (processCreds, error) {
	stderr := o.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	creds, err := cloudlogin.Wait(logins, snap, cloudlogin.WaitSpec[processCreds]{
		Profile: o.Profile, Key: sso.Session, Waiter: o.Waiter,
		Wait: o.ConsoleWait, Poll: loginPollInterval,
		Check:       func() (processCreds, error) { return exportCreds(aws, ssoOnlyProfile) },
		LoginNeeded: func(err error) bool { return loginNeeded(err.Error()) },
		Filed: func() {
			fmt.Fprintf(stderr, "af-aws-exec: SSO login for profile %q requested in the Agent Fleet Console; "+
				"waiting up to %s for the member to approve it there\n", o.Profile, o.ConsoleWait.Round(time.Second))
		},
		FiledAgain: func() {
			fmt.Fprintf(stderr, "af-aws-exec: the SSO login for profile %q is still needed; requested again in the Agent Fleet Console\n", o.Profile)
		},
	})
	var we *cloudlogin.WaitError
	if !errors.As(err, &we) {
		return creds, err
	}
	switch we.Reason {
	case cloudlogin.WaitUnsettled:
		return processCreds{}, fmt.Errorf("%w for profile %q: %v\nlog in with: %s", ErrLoginRequired, o.Profile, first, hint)
	case cloudlogin.WaitCheckFailed:
		return processCreds{}, fmt.Errorf("could not get credentials for profile %q: %v", o.Profile, we.Err)
	case cloudlogin.WaitNotFiled:
		return processCreds{}, fmt.Errorf("%w for profile %q: %v (and the Console could not be asked: %v)\nlog in with: %s",
			ErrLoginRequired, o.Profile, first, we.Err, hint)
	case cloudlogin.WaitCancelled:
		return processCreds{}, fmt.Errorf("%w for profile %q: the login request was cancelled in the Agent Fleet Console; "+
			"ask the member; they can press \"Log in\" on the profile in Settings > AWS profiles/SSM, "+
			"or log in in a terminal with: %s", ErrLoginRequired, o.Profile, hint)
	}
	return processCreds{}, fmt.Errorf("%w for profile %q: the login was requested in the Agent Fleet Console and is waiting "+
		"for the member to approve it there; run the command again once it is approved", ErrLoginRequired, o.Profile)
}
