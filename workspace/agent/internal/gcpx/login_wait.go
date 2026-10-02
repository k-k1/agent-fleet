package gcpx

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cloudlogin"
)

// loginPollInterval is how often a waiting run looks at its request and the gcloud root.
// Both are file reads; gcloud starts only on a change.
var loginPollInterval = 2 * time.Second

// minted is what a successful check hands back to PlanExec.
type minted struct {
	tok     Token
	account string
}

// consoleEligible reports whether this run may ask the Console for the login: nobody at a
// terminal, no --login/--no-login, inside a workspace.
func consoleEligible(o ExecOptions) bool {
	return o.ConsoleLogin && o.Login == "auto" && !o.Interactive && o.ConsoleWait > 0
}

// consoleLogin files a login request for the Console and waits for the member (ADR 0107
// decision 3, as ADR 0102 decisions 1 and 5). snap is the state the failed mint was made
// against; first is its error.
func consoleLogin(gcloudBin string, env []string, p Profile, snap LoginState, o ExecOptions, first error, hint string) (Token, string, error) {
	stderr := o.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	m, err := cloudlogin.Wait(logins, snap, cloudlogin.WaitSpec[minted]{
		Profile: p.Name, Key: ConfigName(p.Name), Waiter: o.Waiter,
		Wait: o.ConsoleWait, Poll: loginPollInterval,
		Check: func() (minted, error) {
			tok, account, err := mintLocked(gcloudBin, env, p, nil)
			return minted{tok, account}, err
		},
		LoginNeeded: func(err error) bool { return errors.Is(err, ErrLoginRequired) },
		Filed: func() {
			fmt.Fprintf(stderr, "af-gcloud-exec: Google Cloud login for profile %q requested in the Agent Fleet Console; "+
				"waiting up to %s for the member to finish it there\n", p.Name, o.ConsoleWait.Round(time.Second))
		},
		FiledAgain: func() {
			fmt.Fprintf(stderr, "af-gcloud-exec: the Google Cloud login for profile %q is still needed; requested again in the Agent Fleet Console\n", p.Name)
		},
	})
	var we *cloudlogin.WaitError
	if !errors.As(err, &we) {
		return m.tok, m.account, err
	}
	switch we.Reason {
	case cloudlogin.WaitUnsettled:
		return Token{}, "", fmt.Errorf("%w: %v\nlog in from a terminal with: %s", ErrLoginRequired, first, hint)
	case cloudlogin.WaitCheckFailed:
		// Permission denied, a disabled API, the network, a profile changed in Settings: a
		// login cannot help, so the run ends with the reason instead of asking for one.
		return Token{}, "", we.Err
	case cloudlogin.WaitNotFiled:
		return Token{}, "", fmt.Errorf("%w: %v (and the Console could not be asked: %v)\nlog in from a terminal with: %s",
			ErrLoginRequired, first, we.Err, hint)
	case cloudlogin.WaitCancelled:
		return Token{}, "", fmt.Errorf("%w: the login request was cancelled in the Agent Fleet Console; ask the member; "+
			"they can press \"Log in\" on the profile in Settings > Google Cloud, or log in in a terminal with: %s", ErrLoginRequired, hint)
	}
	return Token{}, "", fmt.Errorf("%w: the login was requested in the Agent Fleet Console and is waiting for the member to "+
		"finish it there; run the command again once it is done", ErrLoginRequired)
}
