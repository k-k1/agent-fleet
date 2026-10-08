package gcpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cloudexec"
)

// RefreshLead is how much token life is left when the refresher renews it. It is under
// MinRemaining on purpose: the mint passes --min-expiry MinRemaining, so gcloud replaces a
// cached token with less than that left and the call is a renewal, not the same token again.
const RefreshLead = MinRemaining - 2*time.Minute

// refreshRetry is the pause between renewals that failed, until the token ends.
const refreshRetry = 30 * time.Second

// Refresher keeps the run's token file current while the command lives. It renews through
// the same mint the run started with: the same account and configuration, the same
// --min-expiry, no --lifetime, so a renewed token lives no longer and reaches no further than
// the first one. It writes only the token file, which only the command's environment names:
// gcloud and gke-gcloud-auth-plugin read it afresh on each start (measured, SDK 587.0.0).
// What it cannot reach: GOOGLE_OAUTH_ACCESS_TOKEN is a copy in the command's environment, and
// Terraform's Google provider reads it once, at start (ADR 0107 note of 2026-10-08).
type Refresher struct {
	Profile   string
	TokenFile string
	// Token and Account are what the run started with. A renewal that signs in as another
	// account is refused: a login made meanwhile must not change who the command acts as.
	Token   Token
	Account string
	// Mint renews the token, waiting for the root's lock no longer than deadline.
	Mint func(ctx context.Context, deadline time.Time) (Token, string, error)
	// Relogin asks for the login when Mint says one is needed (the Console, waiting until
	// deadline); nil when nobody can be asked. It returns first when it cannot ask.
	Relogin func(ctx context.Context, first error, deadline time.Time) (Token, string, error)
	Now     func() time.Time
	// Wait sleeps d, or returns false when ctx ends first.
	Wait   func(ctx context.Context, d time.Duration) bool
	Stderr io.Writer
}

func (r *Refresher) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Refresher) wait(ctx context.Context, d time.Duration) bool {
	if r.Wait != nil {
		return r.Wait(ctx, d)
	}
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// Run renews the token before each expiry until ctx ends (the command exited). When the
// token has ended and no renewal worked, it asks stop to end the command: exit 3 when the
// reason is a login (the user has to sign in again), 1 for anything else.
func (r *Refresher) Run(ctx context.Context, stop func(cloudexec.Stop)) {
	cur := r.Token
	for {
		if !r.wait(ctx, cur.Expiry.Add(-RefreshLead).Sub(r.now())) {
			return
		}
		next, err := r.renew(ctx, cur)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			code := cloudexec.ExitRefused
			if errors.Is(err, ErrLoginRequired) {
				code = cloudexec.ExitLoginRequired
			}
			stop(cloudexec.Stop{Code: code, Msg: fmt.Sprintf(
				"af-gcloud-exec: the token of profile %s has ended and could not be renewed, so the command was stopped: %v", r.Profile, err)})
			return
		}
		cur = next
	}
}

// renew obtains and installs a token newer than cur, retrying until cur ends. The whole
// of it, a hung gcloud and a Console wait included, is bounded by cur's end: nothing new
// is started after it, and the reason the renewal failed (a login, say) is the one reported,
// not the context error that ended the last try.
func (r *Refresher) renew(ctx context.Context, cur Token) (Token, error) {
	rctx, cancel := context.WithTimeout(ctx, max(cur.Expiry.Sub(r.now()), time.Millisecond))
	defer cancel()
	var last, reloginErr error
	warned, asked := false, false
	fail := func(err error) (Token, error) {
		switch {
		case reloginErr != nil && errors.Is(last, ErrLoginRequired):
			return Token{}, reloginErr
		case last != nil:
			return Token{}, last
		}
		return Token{}, err
	}
	for {
		tok, account, err := r.Mint(rctx, cur.Expiry)
		if err == nil && ctx.Err() == nil && rctx.Err() != nil {
			// Arrived with the old token's end; the command has been left without one.
			err = rctx.Err()
		}
		if err != nil && errors.Is(err, ErrLoginRequired) && r.Relogin != nil && !asked && rctx.Err() == nil {
			asked = true
			var rerr error
			tok, account, rerr = r.Relogin(rctx, err, cur.Expiry)
			if rerr != nil {
				reloginErr = rerr
				last = err
				err = rerr
			} else {
				err = nil
			}
		}
		if err == nil {
			err = r.install(cur, tok, account)
		}
		if err == nil {
			return tok, nil
		}
		if ctx.Err() != nil {
			return Token{}, err
		}
		if rctx.Err() != nil {
			// The token ended during this try: its error is the deadline's, not the cause.
			return fail(errors.New("the token ended before it could be renewed"))
		}
		last = err
		left := cur.Expiry.Sub(r.now())
		if left <= 0 {
			return fail(err)
		}
		if !warned && r.Stderr != nil {
			warned = true
			fmt.Fprintf(r.Stderr, "af-gcloud-exec: could not renew the token of profile %s (%v); trying again until it ends in %d minutes\n",
				r.Profile, err, int(left/time.Minute))
		}
		if !r.wait(rctx, min(refreshRetry, left)) {
			if ctx.Err() != nil {
				return Token{}, err
			}
			return fail(err)
		}
	}
}

// install checks a renewal against the run and replaces the token file with it.
func (r *Refresher) install(cur, tok Token, account string) error {
	switch {
	case account != r.Account:
		return fmt.Errorf("the profile's login is now %s, not %s, which the command started as; run it again", account, r.Account)
	case !tok.Expiry.After(cur.Expiry):
		return errors.New("gcloud gave back a token that lasts no longer than the one in use")
	}
	return writeTokenFile(r.TokenFile, tok.Value)
}

// writeTokenFile replaces path with token (0600) in one rename, so a reader never sees half
// a token.
func writeTokenFile(path, token string) error {
	tmp := path + ".new"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(token)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return werr
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// RemoveToken deletes the run's token file once the command has ended.
func (r *Refresher) RemoveToken() {
	_ = os.Remove(r.TokenFile)
	_ = os.Remove(r.TokenFile + ".new")
}

// newRefresher wires a run's refresher to the real mint and, where a Console can be
// asked, the Console login.
func newRefresher(gcloudBin string, agentEnv []string, p Profile, o ExecOptions, tok Token, account, tokenFile, hint string, stderr io.Writer) *Refresher {
	// The state each mint is made against is read before it, as the first mint does, so a
	// login that lands in between shows as a change and the wait checks again.
	var snap LoginState
	r := &Refresher{
		Profile: p.Name, TokenFile: tokenFile, Token: tok, Account: account, Stderr: stderr, Now: o.Now,
		Mint: func(ctx context.Context, deadline time.Time) (Token, string, error) {
			snap = readLoginState(p.Name)
			return mintLockedCancel(ctx, gcloudBin, agentEnv, p, nil, ctx.Done(), deadline)
		},
	}
	// The command's own terminal is the command's: the wait here never installs a Ctrl-C
	// handler and ends with the command or the token.
	ask := o
	ask.Interactive, ask.TerminalConsole = false, false
	if !consoleEligible(ask) {
		// Nobody was asked (--login, --no-login, or no Console): the member has to log in
		// themselves, and the message says how.
		r.Relogin = func(_ context.Context, first error, _ time.Time) (Token, string, error) {
			return Token{}, "", fmt.Errorf("%w (no login was requested in the Agent Fleet Console)\nlog in from a terminal with: %s", first, hint)
		}
	} else {
		r.Relogin = func(ctx context.Context, first error, deadline time.Time) (Token, string, error) {
			ask.ConsoleWait = min(o.ConsoleWait, time.Until(deadline))
			if ask.ConsoleWait <= 0 {
				return Token{}, "", first
			}
			return consoleLogin(gcloudBin, agentEnv, p, snap, ask, first, hint, ctx.Done())
		}
	}
	return r
}
