package sessionx

// The state machine of the resume that waits for a PERSON (docs/log/47 §4-11). Detecting the
// login failure itself is held down by internal/agents/claude/abort_test.go; what is watched
// here is the one rule that makes this different from the retryable resume — nothing is sent
// until the credentials in force are newer than the turn they killed. There is no tmux, no
// claude and no credentials file here, so the side effects are replaced (newAbortFixture) and
// so is the login moment.

import (
	"testing"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/claude"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/chatx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func authAbort(at time.Time) claude.Abort {
	return claude.Abort{
		Msg:  "Please run /login · API Error: 401 OAuth access token has expired. Re-authenticate to continue.",
		Auth: true,
		At:   at,
	}
}

// setLoginAt replaces the "when was the login in force written" reading. A zero time is what
// the real one returns when there is nothing to judge on.
func setLoginAt(t *testing.T, at time.Time) {
	t.Helper()
	orig := authResumeNow
	authResumeNow = func() time.Time { return at }
	t.Cleanup(func() { authResumeNow = orig })
}

func arState(t *testing.T, name string) authResumeState {
	t.Helper()
	st, _ := authResumeStates.Read(name)
	return st
}

func arMeta() session.Meta {
	return session.Meta{Name: "ar1", Dir: "/tmp/ar1", Kind: session.KindClaude}
}

// TestAuthResumeWaitsForRenewal is the heart of it: a login that has NOT been renewed must
// never trigger a resume, however long the wait — re-sending onto the same credentials
// reproduces the same 401 and burns an attempt. The credentials being valid *right now* is
// not the test either: after a server-side revocation they read valid from the first tick.
func TestAuthResumeWaitsForRenewal(t *testing.T) {
	f := newAbortFixture(t)
	m := arMeta()
	cut := time.Now()
	a := authAbort(cut)

	// The login on disk is OLDER than the turn that died: it is the one that failed.
	setLoginAt(t, cut.Add(-30*time.Minute))
	for _, d := range []time.Duration{time.Second, time.Hour, 24 * time.Hour} {
		authResumeAttempt(m, arState(t, m.Name), a, cut.Add(d))
		if len(f.sent) != 0 {
			t.Fatalf("resumed %v after the failure without a renewed login: %v", d, f.sent)
		}
	}
	if st := arState(t, m.Name); st.At == "" {
		t.Fatal("the episode is not persisted the moment it opens (it would be reopened every tick)")
	}
}

// TestAuthResumeSendsOnceRenewed: signing in again is what releases it, after the new
// credential has settled — and only one prompt goes out, with the auto-resume wording the
// mirror badges.
func TestAuthResumeSendsOnceRenewed(t *testing.T) {
	f := newAbortFixture(t)
	m := arMeta()
	cut := time.Now()
	a := authAbort(cut)

	authResumeAttempt(m, arState(t, m.Name), a, cut.Add(time.Minute)) // opens the episode
	renewed := cut.Add(10 * time.Minute)
	setLoginAt(t, renewed)

	authResumeAttempt(m, arState(t, m.Name), a, renewed.Add(time.Second))
	if len(f.sent) != 0 {
		t.Fatalf("sent before the new credential settled: %v", f.sent)
	}

	authResumeAttempt(m, arState(t, m.Name), a, renewed.Add(authResumeSettle+time.Second))
	if len(f.sent) != 1 {
		t.Fatalf("resume prompts = %d, want 1: %v", len(f.sent), f.sent)
	}
	if f.sent[0] != abortResumePrompt() {
		t.Errorf("prompt = %q, want the auto-resume wording %q", f.sent[0], abortResumePrompt())
	}
	if st := arState(t, m.Name); st.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", st.Attempts)
	}
	// The next sweep must not fire again — the episode is still open (the tail is still the
	// old failure until the resumed turn lands), so only the backoff holds it back.
	authResumeAttempt(m, arState(t, m.Name), a, renewed.Add(authResumeSettle+time.Minute))
	if len(f.sent) != 1 {
		t.Errorf("ignored the backoff and sent %d times", len(f.sent))
	}
}

// TestAuthResumeCapsThenHandsOver: a renewed login that fails the same way is not the fix.
// Step back and line the report side's counter up, so what reaches the user carries the "cap
// reached" wording instead of the resume quietly retrying forever.
func TestAuthResumeCapsThenHandsOver(t *testing.T) {
	f := newAbortFixture(t)
	m := arMeta()
	cut := time.Now()
	a := authAbort(cut)
	renewed := cut.Add(time.Minute)
	setLoginAt(t, renewed)

	now := renewed.Add(authResumeSettle + time.Second)
	for i := 0; i < chatx.MaxAutoResumeAttempts; i++ {
		authResumeAttempt(m, arState(t, m.Name), a, now)
		now = now.Add(authResumeBackoff + time.Second)
	}
	if len(f.sent) != chatx.MaxAutoResumeAttempts {
		t.Fatalf("resumes = %d, want %d", len(f.sent), chatx.MaxAutoResumeAttempts)
	}
	if arState(t, m.Name).GaveUp != "" {
		t.Fatal("gave up before the cap was reached")
	}

	authResumeAttempt(m, arState(t, m.Name), a, now)
	st := arState(t, m.Name)
	if st.GaveUp != authGaveUpCapped {
		t.Fatalf("gaveUp = %q, want %q", st.GaveUp, authGaveUpCapped)
	}
	if len(f.sent) != chatx.MaxAutoResumeAttempts {
		t.Errorf("gave up and still sent: %v", f.sent)
	}
	if got := chatx.AutoResumeAttempts(m.Name); got != chatx.MaxAutoResumeAttempts {
		t.Errorf("the report side's counter = %d, want %d (the capped wording will not be used)", got, chatx.MaxAutoResumeAttempts)
	}
}

// TestAuthResumeUndeliverableGivesUp: signing in and then working in the pane by hand is
// exactly what a user does here, and every such sweep counts as undeliverable. It must step
// back rather than keep typing at a session somebody is using.
func TestAuthResumeUndeliverableGivesUp(t *testing.T) {
	f := newAbortFixture(t)
	f.pane.Idle = false // a turn is running in the pane
	m := arMeta()
	cut := time.Now()
	a := authAbort(cut)
	renewed := cut.Add(time.Minute)
	setLoginAt(t, renewed)

	now := renewed.Add(authResumeSettle + time.Second)
	for i := 0; i < authResumeMaxDeliverTries; i++ {
		authResumeAttempt(m, arState(t, m.Name), a, now)
		now = now.Add(authResumeBackoff + time.Second)
	}
	if len(f.sent) != 0 {
		t.Fatalf("typed into a busy pane: %v", f.sent)
	}
	if st := arState(t, m.Name); st.GaveUp != authGaveUpUndeliverable {
		t.Errorf("gaveUp = %q, want %q", st.GaveUp, authGaveUpUndeliverable)
	}
}

// TestAuthResumeClosesWhenTailMoves: the episode is closed by the tail no longer being a login
// failure — the resume landed, or the user carried on themselves. Left open, it would keep the
// attempt count of a failure that is over.
func TestAuthResumeClosesWhenTailMoves(t *testing.T) {
	newAbortFixture(t)
	m := arMeta()
	cut := time.Now()
	if err := authResumeStates.Write(m.Name, authResumeState{At: cut.Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	authResumeStep(m, claude.Abort{}, false, cut.Add(time.Minute))
	if _, has := authResumeStates.Read(m.Name); has {
		t.Error("the episode survived the tail moving on")
	}
}

// TestAuthResumeIgnoresRetryable: a cut-off a re-send fixes belongs to abort_resume.go. Both
// sweeps acting on one failure would send two "continue" prompts for one interruption.
func TestAuthResumeIgnoresRetryable(t *testing.T) {
	f := newAbortFixture(t)
	m := arMeta()
	cut := time.Now()
	setLoginAt(t, cut.Add(time.Hour))
	authResumeStep(m, retryableAbort(cut), true, cut.Add(2*time.Hour))
	if len(f.sent) != 0 {
		t.Fatalf("the auth sweep acted on a retryable cut-off: %v", f.sent)
	}
	if _, has := authResumeStates.Read(m.Name); has {
		t.Error("opened a login episode for a cut-off that has nothing to do with the login")
	}
}
