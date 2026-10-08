package gcpx

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/cloudexec"
)

// refreshRig is a Refresher on a fake clock: Wait advances the clock instead of sleeping, so
// nothing here waits on a real timer.
type refreshRig struct {
	t      *testing.T
	r      *Refresher
	now    time.Time
	file   string
	stderr bytes.Buffer
	mints  int
	ctxs   map[int]context.Context
	cancel context.CancelFunc
	ctx    context.Context

	mu       sync.Mutex
	stopped  []cloudexec.Stop
	onMint   func(n int) (Token, string, error)
	onLogin  func(first error) (Token, string, error)
	seenFile []string // the token file's content after each mint call
}

func newRefreshRig(t *testing.T) *refreshRig {
	t.Helper()
	rg := &refreshRig{t: t, ctxs: map[int]context.Context{}, now: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)}
	rg.file = filepath.Join(t.TempDir(), "token")
	first := Token{Value: "fake-token-0", Expiry: rg.now.Add(MinRemaining + time.Minute)}
	if err := os.WriteFile(rg.file, []byte(first.Value), 0o600); err != nil {
		t.Fatal(err)
	}
	rg.ctx, rg.cancel = context.WithCancel(context.Background())
	t.Cleanup(rg.cancel)
	rg.r = &Refresher{
		Profile: "prod", TokenFile: rg.file, Token: first, Account: "me@example.com", Stderr: &rg.stderr,
		Now: func() time.Time { return rg.now },
		Wait: func(ctx context.Context, d time.Duration) bool {
			if ctx.Err() != nil {
				return false
			}
			if d > 0 {
				rg.now = rg.now.Add(d)
			}
			return true
		},
		Mint: func(ctx context.Context, deadline time.Time) (Token, string, error) {
			rg.mints++
			rg.ctxs[rg.mints] = ctx
			b, _ := os.ReadFile(rg.file)
			rg.seenFile = append(rg.seenFile, string(b))
			return rg.onMint(rg.mints)
		},
	}
	return rg
}

// ctxOf is the done channel of the context the n-th mint was given.
func (rg *refreshRig) ctxOf(n int) <-chan struct{} { return rg.ctxs[n].Done() }

func (rg *refreshRig) run() {
	rg.t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		rg.r.Run(rg.ctx, func(s cloudexec.Stop) { rg.mu.Lock(); rg.stopped = append(rg.stopped, s); rg.mu.Unlock() })
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		rg.t.Fatal("Refresher.Run did not return")
	}
}

func (rg *refreshRig) content() string {
	b, err := os.ReadFile(rg.file)
	if err != nil {
		rg.t.Fatal(err)
	}
	return string(b)
}

// TestRefresherRenewsBeforeExpiryAndKeepsTheFilePrivate: the token file is replaced with the
// renewed token while the command lives, at 0600, with no half-written file left behind.
func TestRefresherRenewsBeforeExpiryAndKeepsTheFilePrivate(t *testing.T) {
	rg := newRefreshRig(t)
	rg.onMint = func(n int) (Token, string, error) {
		tok := Token{Value: "fake-token-" + string(rune('0'+n)), Expiry: rg.now.Add(time.Hour)}
		if n == 2 {
			rg.cancel() // the command exits after the second renewal
		}
		return tok, "me@example.com", nil
	}
	start := rg.now
	rg.run()

	if rg.mints != 2 {
		t.Fatalf("renewals = %d, want 2", rg.mints)
	}
	if got := rg.content(); got != "fake-token-2" {
		t.Fatalf("token file = %q, want the second renewal", got)
	}
	// The first renewal happens RefreshLead before the first token ends, not earlier.
	if want := []string{"fake-token-0", "fake-token-1"}; rg.seenFile[0] != want[0] || rg.seenFile[1] != want[1] {
		t.Fatalf("file seen by the mints = %v, want %v", rg.seenFile, want)
	}
	if want := start.Add(MinRemaining + time.Minute - RefreshLead + time.Hour - RefreshLead); !rg.now.Equal(want) {
		t.Fatalf("clock at the second renewal = %v, want %v", rg.now, want)
	}
	fi, err := os.Stat(rg.file)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode = %v, %v; want 0600", fi.Mode().Perm(), err)
	}
	if _, err := os.Stat(rg.file + ".new"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the temporary file is left behind: %v", err)
	}
	if len(rg.stopped) != 0 {
		t.Fatalf("stop was asked for: %+v", rg.stopped)
	}
	if rg.stderr.Len() != 0 {
		t.Fatalf("a quiet renewal printed: %q", rg.stderr.String())
	}
}

// TestRefresherRetriesATransientFailureOnce: a failed renewal is warned about once, without
// the token, and tried again before the token ends.
func TestRefresherRetriesATransientFailureOnce(t *testing.T) {
	rg := newRefreshRig(t)
	rg.onMint = func(n int) (Token, string, error) {
		switch n {
		case 1, 2:
			return Token{}, "", errors.New("gcloud could not mint a token: network is unreachable")
		}
		rg.cancel()
		return Token{Value: "fake-token-new", Expiry: rg.now.Add(time.Hour)}, "me@example.com", nil
	}
	rg.run()
	if rg.content() != "fake-token-new" || len(rg.stopped) != 0 {
		t.Fatalf("file %q, stops %+v; want the renewal installed and no stop", rg.content(), rg.stopped)
	}
	if n := strings.Count(rg.stderr.String(), "could not renew"); n != 1 {
		t.Fatalf("warned %d times, want once:\n%s", n, rg.stderr.String())
	}
	if strings.Contains(rg.stderr.String(), "fake-token") {
		t.Fatalf("a token is in the output: %s", rg.stderr.String())
	}
}

// TestRefresherStopsTheCommandWhenTheLoginIsGone: a login that has to be done again ends the
// command when its token ends, with exit 3; the command is not stopped earlier.
func TestRefresherStopsTheCommandWhenTheLoginIsGone(t *testing.T) {
	rg := newRefreshRig(t)
	rg.onMint = func(int) (Token, string, error) {
		return Token{}, "", errors.Join(ErrLoginRequired, errors.New("invalid_grant"))
	}
	expiry := rg.r.Token.Expiry
	rg.run()
	if len(rg.stopped) != 1 || rg.stopped[0].Code != cloudexec.ExitLoginRequired {
		t.Fatalf("stops = %+v, want one with exit 3", rg.stopped)
	}
	if rg.now.Before(expiry) {
		t.Fatalf("stopped at %v, before the token ends at %v", rg.now, expiry)
	}
	if m := rg.stopped[0].Msg; !strings.Contains(m, "profile prod") || !strings.Contains(m, "login") {
		t.Fatalf("message = %q", m)
	}
	if rg.mints < 2 {
		t.Fatalf("gave up after %d tries; it has to keep trying until the token ends", rg.mints)
	}
}

// TestRefresherAsksForTheLoginOnce: when the login is needed and someone can be asked, the
// run waits for it and carries on with the token it brings.
func TestRefresherAsksForTheLoginOnce(t *testing.T) {
	rg := newRefreshRig(t)
	rg.onMint = func(int) (Token, string, error) { return Token{}, "", ErrLoginRequired }
	asked := 0
	rg.r.Relogin = func(ctx context.Context, first error, deadline time.Time) (Token, string, error) {
		asked++
		if !deadline.Equal(rg.r.Token.Expiry) {
			t.Errorf("the wait ends at %v, want the token's end %v", deadline, rg.r.Token.Expiry)
		}
		rg.cancel()
		return Token{Value: "fake-token-after-login", Expiry: rg.now.Add(time.Hour)}, "me@example.com", nil
	}
	rg.run()
	if asked != 1 || rg.content() != "fake-token-after-login" || len(rg.stopped) != 0 {
		t.Fatalf("asked %d, file %q, stops %+v", asked, rg.content(), rg.stopped)
	}
}

// TestRefresherReloginThatFailsEndsWithExit3: the Console login not finishing is still a login.
func TestRefresherReloginThatFailsEndsWithExit3(t *testing.T) {
	rg := newRefreshRig(t)
	rg.onMint = func(int) (Token, string, error) { return Token{}, "", ErrLoginRequired }
	asked := 0
	rg.r.Relogin = func(ctx context.Context, first error, deadline time.Time) (Token, string, error) {
		asked++
		rg.now = deadline // the wait ran to the token's end
		return Token{}, "", errors.Join(ErrLoginRequired, errors.New("waiting for the member"))
	}
	rg.run()
	if asked != 1 || len(rg.stopped) != 1 || rg.stopped[0].Code != 3 {
		t.Fatalf("asked %d, stops %+v", asked, rg.stopped)
	}
}

// TestRefresherOtherFailureIsExit1: permission denied and the like are not a login.
func TestRefresherOtherFailureIsExit1(t *testing.T) {
	rg := newRefreshRig(t)
	rg.onMint = func(int) (Token, string, error) {
		return Token{}, "", errors.New("gcloud could not mint a token: PERMISSION_DENIED")
	}
	rg.run()
	if len(rg.stopped) != 1 || rg.stopped[0].Code != cloudexec.ExitRefused {
		t.Fatalf("stops = %+v, want one with exit 1", rg.stopped)
	}
}

// TestRefresherRefusesAnotherAccount: a login made meanwhile as someone else must not change
// who the command acts as; the old token stays in the file.
func TestRefresherRefusesAnotherAccount(t *testing.T) {
	rg := newRefreshRig(t)
	rg.onMint = func(int) (Token, string, error) {
		return Token{Value: "fake-token-other", Expiry: rg.now.Add(time.Hour)}, "someone@example.com", nil
	}
	rg.run()
	if rg.content() != "fake-token-0" {
		t.Fatalf("the file holds %q; another account's token was installed", rg.content())
	}
	if len(rg.stopped) != 1 || rg.stopped[0].Code != 1 || !strings.Contains(rg.stopped[0].Msg, "someone@example.com") {
		t.Fatalf("stops = %+v", rg.stopped)
	}
}

// TestRefresherRefusesATokenThatLastsNoLonger: gcloud handing back the same token is not a
// renewal; the run does not pretend it is one.
func TestRefresherRefusesATokenThatLastsNoLonger(t *testing.T) {
	rg := newRefreshRig(t)
	rg.onMint = func(int) (Token, string, error) { return rg.r.Token, "me@example.com", nil }
	rg.run()
	if len(rg.stopped) != 1 {
		t.Fatalf("stops = %+v, want one", rg.stopped)
	}
}

// TestRefresherEndsWithTheCommand: ctx ending (the command exited) stops the loop at once,
// in the wait and with no stop request.
func TestRefresherEndsWithTheCommand(t *testing.T) {
	rg := newRefreshRig(t)
	rg.onMint = func(int) (Token, string, error) { t.Error("minted after the command ended"); return Token{}, "", nil }
	rg.r.Wait = nil // the real wait: it has to return on ctx, not after RefreshLead
	rg.cancel()
	rg.run()
	if len(rg.stopped) != 0 {
		t.Fatalf("stops = %+v", rg.stopped)
	}
}

func TestRemoveTokenDeletesTheFile(t *testing.T) {
	rg := newRefreshRig(t)
	rg.r.RemoveToken()
	if _, err := os.Stat(rg.file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("token file still there: %v", err)
	}
}

// TestPlanRunRefresherRenewsThroughTheRealMint: the refresher PlanRun builds names the token
// file in the child's environment, mints with the run's own call (same configuration, same
// --min-expiry, clean environment) and replaces the file; the environment variable the child
// started with is what it cannot change.
func TestPlanRunRefresherRenewsThroughTheRealMint(t *testing.T) {
	e := setup(t)
	p := prod()
	mustApply(t, p)
	addCredential(t, p.Account, "authorized_user")
	o := execOpts(e, p)
	o.Quiet = true
	pl, err := PlanRun(e.gcloud, hostile(t), o)
	if err != nil {
		t.Fatal(err)
	}
	var tokFile, envToken string
	for _, kv := range pl.Env {
		if v, ok := strings.CutPrefix(kv, "CLOUDSDK_AUTH_ACCESS_TOKEN_FILE="); ok {
			tokFile = v
		}
		if v, ok := strings.CutPrefix(kv, "GOOGLE_OAUTH_ACCESS_TOKEN="); ok {
			envToken = v
		}
	}
	if pl.Refresher == nil || pl.Refresher.TokenFile != tokFile || pl.Refresher.Account != p.Account {
		t.Fatalf("refresher %+v does not match the child's token file %q", pl.Refresher, tokFile)
	}

	// Google hands out a new token; the run's clock reaches the lead; the mint is gcloud's.
	renewed := "ya" + "29.renewed-" + randHex(t, 8)
	e.write(t, "token", renewed)
	e.write(t, "expiry", time.Now().Add(58*time.Minute).UTC().Format(time.RFC3339))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pl.Refresher.Wait = func(ctx context.Context, d time.Duration) bool { return ctx.Err() == nil }
	pl.Refresher.Now = func() time.Time { return pl.Refresher.Token.Expiry.Add(-RefreshLead) }
	done := make(chan struct{})
	go func() {
		defer close(done)
		pl.Refresher.Run(ctx, func(s cloudexec.Stop) { t.Errorf("stop: %+v", s) })
	}()
	deadline := time.After(5 * time.Second)
	for {
		if b, _ := os.ReadFile(tokFile); string(b) == renewed {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the token file was not renewed")
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	<-done

	calls := e.calls(t)
	if len(calls) < 2 || calls[1].args != calls[0].args {
		t.Fatalf("the renewal is not the run's mint call: %+v", calls)
	}
	for k := range calls[1].env {
		if k == "CLOUDSDK_AUTH_ACCESS_TOKEN_FILE" || strings.HasPrefix(k, "GOOGLE_") || strings.HasPrefix(k, "GCE_METADATA_") {
			t.Errorf("the renewal's gcloud saw %s", k)
		}
	}
	if envToken != e.token {
		t.Error("the child's GOOGLE_OAUTH_ACCESS_TOKEN is a copy made at start; it must not be what changes")
	}
	pl.Refresher.RemoveToken()
	if _, err := os.Stat(tokFile); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("token file after RemoveToken: %v", err)
	}
}

// TestRefresherHungMintEndsWithTheToken: a gcloud that hangs on the last try is cut off when
// the token ends, so the command does not run on past it, and the reason reported is the login
// the earlier tries met, not the deadline.
func TestRefresherHungMintEndsWithTheToken(t *testing.T) {
	rg := newRefreshRig(t)
	// The clock stands 200ms before the token ends when the renewal starts; the first try fails with a login
	// and the second try hangs, so only its context can end it.
	expiry := rg.r.Token.Expiry
	waits := 0
	rg.r.Wait = func(ctx context.Context, d time.Duration) bool {
		if ctx.Err() != nil {
			return false
		}
		if waits++; waits == 1 {
			rg.now = expiry.Add(-200 * time.Millisecond)
		} else {
			rg.now = rg.now.Add(50 * time.Millisecond) // the retry pause, shortened
		}
		return true
	}
	rg.onMint = func(n int) (Token, string, error) {
		if n == 1 {
			return Token{}, "", ErrLoginRequired
		}
		<-rg.ctxOf(n)
		// A gcloud that finishes just as it is cut off still hands over a token.
		return Token{Value: "fake-token-late", Expiry: rg.now.Add(time.Hour)}, "me@example.com", nil
	}
	rg.run()
	if rg.content() == "fake-token-late" {
		t.Fatal("a token that arrived after the old one ended was installed")
	}
	if len(rg.stopped) != 1 || rg.stopped[0].Code != cloudexec.ExitLoginRequired {
		t.Fatalf("stops = %+v; want exit 3 (the login), not the deadline's exit 1", rg.stopped)
	}
	if rg.mints != 2 {
		t.Fatalf("mints = %d; nothing new may start after the token ended", rg.mints)
	}
}

// TestRefresherWithoutConsoleSaysHowToLogIn: a run that could not ask the Console (--login,
// --no-login, no Console) ends a failed renewal with the terminal command, and does not claim
// a request is waiting.
func TestRefresherWithoutConsoleSaysHowToLogIn(t *testing.T) {
	rg := newRefreshRig(t)
	rg.onMint = func(int) (Token, string, error) { return Token{}, "", ErrLoginRequired }
	p := Profile{Name: "prod", Project: "prod-project"}
	built := newRefresher("/nonexistent/gcloud", nil, p, ExecOptions{Login: "never"}, rg.r.Token, rg.r.Account, rg.file,
		"af-gcloud-exec --profile prod --project prod-project --login -- true", &rg.stderr)
	if built.Relogin == nil {
		t.Fatal("no Relogin: the final message loses the terminal command")
	}
	rg.r.Relogin = built.Relogin
	rg.run()
	if len(rg.stopped) != 1 || rg.stopped[0].Code != 3 {
		t.Fatalf("stops = %+v", rg.stopped)
	}
	m := rg.stopped[0].Msg
	if !strings.Contains(m, "--login -- true") || strings.Contains(m, "waiting") {
		t.Fatalf("message = %q", m)
	}
}

// TestRefresherStartsNothingAfterTheTokenEnded: a token whose end has passed is not renewed,
// whatever the context timer says, and a gcloud that would succeed instantly is not asked.
func TestRefresherStartsNothingAfterTheTokenEnded(t *testing.T) {
	rg := newRefreshRig(t)
	rg.onMint = func(int) (Token, string, error) {
		return Token{Value: "fake-token-late", Expiry: rg.now.Add(time.Hour)}, "me@example.com", nil
	}
	rg.r.Token.Expiry = rg.now.Add(-time.Second)
	rg.run()
	if rg.mints != 0 || rg.content() != "fake-token-0" {
		t.Fatalf("mints %d, file %q: a token was fetched after the old one ended", rg.mints, rg.content())
	}
	if len(rg.stopped) != 1 || rg.stopped[0].Code != cloudexec.ExitRefused {
		t.Fatalf("stops = %+v", rg.stopped)
	}
}

// TestRefresherSlowReloginKeepsTheLoginError: a login finished after the token ended is not
// installed, and the run reports the login it needed (exit 3), not a deadline.
func TestRefresherSlowReloginKeepsTheLoginError(t *testing.T) {
	rg := newRefreshRig(t)
	rg.onMint = func(int) (Token, string, error) { return Token{}, "", ErrLoginRequired }
	expiry := rg.r.Token.Expiry
	rg.r.Relogin = func(ctx context.Context, first error, deadline time.Time) (Token, string, error) {
		rg.now = expiry.Add(time.Second) // the member finished the login late, by the clock
		return Token{Value: "fake-token-late", Expiry: rg.now.Add(time.Hour)}, "me@example.com", nil
	}
	rg.run()
	if rg.content() != "fake-token-0" {
		t.Fatalf("file %q: the late token was installed", rg.content())
	}
	if len(rg.stopped) != 1 || rg.stopped[0].Code != cloudexec.ExitLoginRequired {
		t.Fatalf("stops = %+v; want exit 3", rg.stopped)
	}
}
